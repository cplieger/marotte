package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/route/routetest"
	"github.com/cplieger/marotte/internal/schedule"
)

// runDefaultsHub is a hub whose live catalog offers opus (low, medium, high), haiku (low) and
// auto (none), with one launchable recipe and a launch-rule store behind the run routes.
func runDefaultsHub(t *testing.T) (*Runtime, *fakeBridge, *http.ServeMux) {
	t.Helper()
	h, _, br := newTestHub()
	launchableRecipe(br, "wf_pick")
	h.catalog.SetModels([]marotte.SessionModel{
		{ID: "opus", Name: "Opus", EffortTiers: []string{"low", "medium", "high"}},
		{ID: "haiku", Name: "Haiku", EffortTiers: []string{"low"}},
		{ID: "auto", Name: "Auto"},
	})
	st, _, err := launchrule.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("launchrule.NewStore: %v", err)
	}
	h.runs.launchRules = st
	mux := http.NewServeMux()
	(&runRoutes{runs: h.runs, epoch: h.Epoch}).register(routetest.On(mux))
	return h, br, mux
}

func postRoute(t *testing.T, mux *http.ServeMux, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, target, strings.NewReader(body)))
	return rec
}

// errorText is a refusal body's `error` sentence.
func errorText(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode refusal %q: %v", rec.Body.String(), err)
	}
	return body.Error
}

// A launch left on Default sends no launch default, so KAS's and the recipe's own pins win.
func TestLaunchRoute_SendsTheModelPickOnlyWhenSet(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantModel  any
		wantEffort any
	}{
		{name: "default", body: `{"source":"bundled://publish"}`},
		{name: "model only", body: `{"source":"bundled://publish","model":"opus"}`, wantModel: "opus"},
		{
			name:      "model and effort",
			body:      `{"source":"bundled://publish","model":"opus","effort":"high"}`,
			wantModel: "opus", wantEffort: "high",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, br, mux := runDefaultsHub(t)
			rec := postRoute(t, mux, "/api/runs", tc.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("POST /api/runs = %d %s, want 200", rec.Code, rec.Body.String())
			}
			params := br.paramsFor(methodKiroWorkflowNew)
			model, hasModel := params["modelId"]
			effort, hasEffort := params["effortLevel"]
			if hasModel != (tc.wantModel != nil) || model != tc.wantModel {
				t.Errorf("workflow/new modelId = %v (sent %v), want %v", model, hasModel, tc.wantModel)
			}
			if hasEffort != (tc.wantEffort != nil) || effort != tc.wantEffort {
				t.Errorf("workflow/new effortLevel = %v (sent %v), want %v", effort, hasEffort, tc.wantEffort)
			}
		})
	}
}

// A pick the catalog does not offer is refused before anything reaches KAS, which would take it
// and fail the run at its first model call.
func TestLaunchRoute_RefusesAPickTheCatalogDoesNotOffer(t *testing.T) {
	cases := []struct {
		name, body, wantText string
	}{
		{name: "unknown model", body: `{"source":"bundled://publish","model":"gpt-9"}`, wantText: `"gpt-9" is not offered`},
		{name: "effort with no model", body: `{"source":"bundled://publish","effort":"high"}`, wantText: "an effort needs a model"},
		{name: "effort on a tierless model", body: `{"source":"bundled://publish","model":"auto","effort":"high"}`, wantText: `"auto" offers no effort levels`},
		{name: "tier the model does not offer", body: `{"source":"bundled://publish","model":"opus","effort":"max"}`, wantText: `does not offer the effort level "max"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, br, mux := runDefaultsHub(t)
			rec := postRoute(t, mux, "/api/runs", tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("POST /api/runs = %d, want 400", rec.Code)
			}
			if got := errorText(t, rec); !strings.Contains(got, tc.wantText) {
				t.Errorf("refusal = %q, want it to say %q", got, tc.wantText)
			}
			if slices.Contains(br.callLog(), methodKiroWorkflowNew) {
				t.Errorf("calls = %v, want no workflow/new for a refused pick", br.callLog())
			}
		})
	}
}

// With no live catalog the template's list decides, as GET /api/config-template serves it; with
// neither, a pick cannot be checked and is refused as retryable.
func TestLaunchRoute_ChecksThePickAgainstTheTemplateWithNoLiveCatalog(t *testing.T) {
	newHub := func(t *testing.T) (*fakeBridge, *http.ServeMux) {
		t.Helper()
		h, _, br := newTestHub()
		launchableRecipe(br, "wf_pick")
		mux := http.NewServeMux()
		(&runRoutes{runs: h.runs, epoch: h.Epoch}).register(routetest.On(mux))
		return br, mux
	}
	t.Run("the template offers it", func(t *testing.T) {
		br, mux := newHub(t)
		br.callResults[methodKiroConfigTemplate] = json.RawMessage(`{"configOptions":[
			{"id":"model","currentValue":"m-default","options":[{"value":"m-default","name":"Default Model"}]}]}`)
		rec := postRoute(t, mux, "/api/runs", `{"source":"bundled://publish","model":"m-default"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("POST /api/runs = %d %s, want 200", rec.Code, rec.Body.String())
		}
		if got := br.paramsFor(methodKiroWorkflowNew)["modelId"]; got != "m-default" {
			t.Errorf("workflow/new modelId = %v, want m-default", got)
		}
	})
	t.Run("no catalog can be read", func(t *testing.T) {
		br, mux := newHub(t)
		br.callErrs = map[string]error{methodKiroConfigTemplate: errors.New("engine down")}
		rec := postRoute(t, mux, "/api/runs", `{"source":"bundled://publish","model":"m-default"}`)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("POST /api/runs = %d %s, want 503", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Retry-After"); got == "" {
			t.Error("no Retry-After header on a refusal the client is told to retry")
		}
		var body struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Code != "model_list_unreadable" {
			t.Errorf("body = %s, want code model_list_unreadable", rec.Body.String())
		}
		if slices.Contains(br.callLog(), methodKiroWorkflowNew) {
			t.Errorf("calls = %v, want no workflow/new for an unchecked pick", br.callLog())
		}
	})
	t.Run("a Default launch reads no catalog", func(t *testing.T) {
		br, mux := newHub(t)
		br.callErrs = map[string]error{methodKiroConfigTemplate: errors.New("engine down")}
		if rec := postRoute(t, mux, "/api/runs", `{"source":"bundled://publish"}`); rec.Code != http.StatusOK {
			t.Errorf("POST /api/runs = %d %s, want 200", rec.Code, rec.Body.String())
		}
	})
}

// A clock rule stores its pick, shows it, keeps it across a restart, and refuses an unknown one
// at save time without touching the stored row.
func TestLaunchRuleRoute_StoresThePickAndRefusesAnUnknownOne(t *testing.T) {
	h, _, mux := runDefaultsHub(t)
	const spec = `"when":{"clock":{"freq":"daily","hour":2,"minute":0}}`
	rec := postRoute(t, mux, "/api/launch-rules", `{"source":"bundled://publish","model":"opus","effort":"high",`+spec+`,"enabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/launch-rules = %d %s, want 200", rec.Code, rec.Body.String())
	}
	var view LaunchRuleView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if view.Model != "opus" || view.Effort != "high" {
		t.Errorf("saved view model/effort = %q/%q, want opus/high", view.Model, view.Effort)
	}

	rec = postRoute(t, mux, "/api/launch-rules", `{"source":"bundled://publish","model":"gpt-9",`+spec+`,"enabled":true}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(errorText(t, rec), `"gpt-9" is not offered`) {
		t.Errorf("POST /api/launch-rules with an unknown model = %d %s, want 400 naming it", rec.Code, rec.Body.String())
	}

	list := h.runs.launchRules.Clocks()
	if len(list) != 1 || list[0].Model != "opus" || list[0].Effort != "high" {
		t.Errorf("stored schedules = %+v, want the one opus/high row the refusal left alone", list)
	}
}

// A schedule's pick reaches workflow/new at its fire.
func TestLaunchScheduled_SendsTheSchedulesPick(t *testing.T) {
	h, _, br := newTestHub()
	launchableRecipe(br, "wf_sched")
	if _, _, err := h.runs.LaunchScheduled(t.Context(), "bundled://publish", launchrule.Row{ID: "sched-1"},
		nil, marotte.RunDefaults{Model: "opus", Effort: "low"}, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("LaunchScheduled: %v", err)
	}
	params := br.paramsFor(methodKiroWorkflowNew)
	if params["modelId"] != "opus" || params["effortLevel"] != "low" {
		t.Errorf("workflow/new modelId/effortLevel = %v/%v, want opus/low", params["modelId"], params["effortLevel"])
	}
}

// The Run and Schedule forms list a model's tiers from GET /api/config-template.
func TestHandleConfigTemplate_ListsEachModelsTiers(t *testing.T) {
	h, _, _ := runDefaultsHub(t)
	rec := httptest.NewRecorder()
	h.handleConfigTemplate(rec, httptest.NewRequest(http.MethodGet, "/api/config-template", nil))
	var got marotte.ConfigTemplateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	tiers := map[string][]string{}
	for _, m := range got.Models {
		tiers[m.ID] = m.EffortTiers
	}
	if want := []string{"low", "medium", "high"}; !slices.Equal(tiers["opus"], want) || len(tiers["auto"]) != 0 {
		t.Errorf("served tiers = %v, want opus %v and auto none", tiers, want)
	}
}

// planStep is one step node of a listRecipes plan as KAS's nodeToDescriptor writes it.
func planStep(id, model, effort string) string {
	return fmt.Sprintf(`{"nodeId":%q,"type":"step","agentName":"worker","modelId":%q,"effortLevel":%q}`, id, model, effort)
}

// checkPins checks plan against runDefaultsHub's catalog.
func checkPins(t *testing.T, plan string, defaults marotte.RunDefaults) error {
	t.Helper()
	h, _, _ := runDefaultsHub(t)
	return h.runs.checkRecipePins(t.Context(), json.RawMessage(plan), defaults)
}

// KAS keeps a step's effortLevel only when its model offers it and otherwise runs the model's
// default, with no error, so marotte refuses the launch instead.
func TestCheckRecipePins_RefusesUnofferedStepEffort(t *testing.T) {
	err := checkPins(t, "["+planStep("review", "opus", "xhigh")+"]", marotte.RunDefaults{})
	if !errors.Is(err, errDefaultsRefused) {
		t.Fatalf("checkRecipePins = %v, want errDefaultsRefused", err)
	}
	if got := err.Error(); !strings.Contains(got, `step "review"`) || !strings.Contains(got, `"xhigh"`) {
		t.Errorf("refusal = %q, want it to name the step and the level", got)
	}
	if err := checkPins(t, "["+planStep("review", "opus", "high")+"]", marotte.RunDefaults{}); err != nil {
		t.Errorf("checkRecipePins(opus, high) = %v, want nil for an offered pair", err)
	}
}

// A step pinned to a model the account is not served fails at its first model call.
func TestCheckRecipePins_RefusesUnknownStepModel(t *testing.T) {
	err := checkPins(t, "["+planStep("build", "gpt-9", "")+"]", marotte.RunDefaults{Model: "opus"})
	if !errors.Is(err, errDefaultsRefused) {
		t.Fatalf("checkRecipePins = %v, want errDefaultsRefused", err)
	}
	if got := err.Error(); !strings.Contains(got, `step "build"`) || !strings.Contains(got, `"gpt-9" is not offered`) {
		t.Errorf("refusal = %q, want it to name the step and the model", got)
	}
}

// KAS resolves a step's model and effort from its own pin, else the run's, else the parent
// session's (resolveModelIdWithSource), with an auto model declaring nothing and the workflow
// creator step skipping the run's level (workflowLevelReaches). A model marotte cannot resolve
// leaves only the served vocabulary to check the effort against.
func TestCheckRecipePins_InheritedModelChecksVocabularyOnly(t *testing.T) {
	cases := []struct {
		name     string
		plan     string
		defaults marotte.RunDefaults
		wantText string // empty means taken
	}{
		{name: "a level no model offers", plan: "[" + planStep("s", "", "hgih") + "]", wantText: `no model offers the effort level "hgih"`},
		{name: "a level some model offers", plan: "[" + planStep("s", "", "high") + "]"},
		{
			name: "the run's model does not run the level", plan: "[" + planStep("s", "", "high") + "]",
			defaults: marotte.RunDefaults{Model: "haiku"}, wantText: `"haiku" does not offer the effort level "high"`,
		},
		{
			name: "the run's model runs the level", plan: "[" + planStep("s", "", "high") + "]",
			defaults: marotte.RunDefaults{Model: "opus"},
		},
		{
			name: "the run's effort on the step's model", plan: "[" + planStep("s", "haiku", "") + "]",
			defaults: marotte.RunDefaults{Model: "opus", Effort: "high"}, wantText: `step "s": the model "haiku" does not offer`,
		},
		{
			name:     "the workflow creator ignores the run's model",
			plan:     `[{"nodeId":"s","type":"step","agentName":"wf-workflow-creator","effortLevel":"high"}]`,
			defaults: marotte.RunDefaults{Model: "haiku"},
		},
		{
			name: "a run default of auto declares no model", plan: "[" + planStep("s", "", "high") + "]",
			defaults: marotte.RunDefaults{Model: "auto"},
		},
		{
			name: "a step on auto takes the run's model", plan: "[" + planStep("s", "auto", "high") + "]",
			defaults: marotte.RunDefaults{Model: "haiku"}, wantText: `"haiku" does not offer the effort level "high"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkPins(t, tc.plan, tc.defaults)
			if tc.wantText == "" {
				if err != nil {
					t.Errorf("checkRecipePins = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, errDefaultsRefused) || !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("checkRecipePins = %v, want a refusal saying %q", err, tc.wantText)
			}
		})
	}
}

// KAS nests a sequence's and a repeat's body under steps and a parallel's under branches.
func TestCheckRecipePins_WalksNestedChildren(t *testing.T) {
	bad := planStep("deep", "opus", "max")
	cases := map[string]string{
		"sequence": `[{"nodeId":"seq","type":"sequence","steps":[` + planStep("ok", "", "") + `,` + bad + `]}]`,
		"repeat":   `[{"nodeId":"loop","type":"repeat","maxIterations":3,"onMaxIterations":"pause","steps":[` + bad + `]}]`,
		"parallel": `[{"nodeId":"fan","type":"parallel","branches":[` + planStep("ok", "opus", "low") + `,` + bad + `]}]`,
		"parallel in a repeat": `[{"nodeId":"loop","type":"repeat","maxIterations":2,"onMaxIterations":"pause","steps":[` +
			`{"nodeId":"fan","type":"parallel","branches":[{"nodeId":"seq","type":"sequence","steps":[` + bad + `]}]}]}]`,
	}
	for name, plan := range cases {
		t.Run(name, func(t *testing.T) {
			err := checkPins(t, plan, marotte.RunDefaults{})
			if !errors.Is(err, errDefaultsRefused) || !strings.Contains(err.Error(), `step "deep"`) {
				t.Errorf("checkRecipePins = %v, want the nested step \"deep\" refused", err)
			}
		})
	}
}

// pinnedRecipe makes the launchable recipe's plan pin one step.
func pinnedRecipe(br *fakeBridge, step string) {
	br.setCallResult(methodKiroWorkflowListRecipes, json.RawMessage(
		`{"recipes":[{"name":"publish","source":"bundled://publish","plan":[`+step+`]}]}`,
	))
}

// The refusal comes before the run's host session opens or any run is created.
func TestLaunch_RefusesBadStepPinBeforeWorkflowNew(t *testing.T) {
	_, br, mux := runDefaultsHub(t)
	pinnedRecipe(br, planStep("review", "opus", "xhigh"))
	rec := postRoute(t, mux, "/api/runs", `{"source":"bundled://publish"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST /api/runs = %d %s, want 400", rec.Code, rec.Body.String())
	}
	if got := errorText(t, rec); !strings.Contains(got, `step "review"`) {
		t.Errorf("refusal = %q, want it to name the step", got)
	}
	if slices.Contains(br.callLog(), methodKiroWorkflowNew) {
		t.Errorf("calls = %v, want no workflow/new for a refused pin", br.callLog())
	}
	if so := br.lastSessionOpts(); so != nil && so.Role == marotte.SessionRoleHost {
		t.Error("a run host session was opened for a launch the pin check refused")
	}
}

// With no catalog to check a pin against, a launch is refused as retryable; a plan with no pins
// reads no catalog.
func TestHandleLaunch_UncheckableStepPinsAnswersTryLater(t *testing.T) {
	newHub := func(t *testing.T, step string) (*fakeBridge, *http.ServeMux) {
		t.Helper()
		h, _, br := newTestHub()
		launchableRecipe(br, "wf_pins")
		pinnedRecipe(br, step)
		br.callErrs = map[string]error{methodKiroConfigTemplate: errors.New("engine down")}
		mux := http.NewServeMux()
		(&runRoutes{runs: h.runs, epoch: h.Epoch}).register(routetest.On(mux))
		return br, mux
	}
	t.Run("a pinned step", func(t *testing.T) {
		br, mux := newHub(t, planStep("review", "opus", "high"))
		rec := postRoute(t, mux, "/api/runs", `{"source":"bundled://publish"}`)
		if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
			t.Errorf("POST /api/runs = %d (Retry-After %q) %s, want 503 with Retry-After",
				rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
		}
		var body struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Code != "model_list_unreadable" {
			t.Errorf("body = %s, want code model_list_unreadable", rec.Body.String())
		}
		if slices.Contains(br.callLog(), methodKiroWorkflowNew) {
			t.Errorf("calls = %v, want no workflow/new for an unchecked pin", br.callLog())
		}
	})
	t.Run("no pinned step", func(t *testing.T) {
		_, mux := newHub(t, planStep("review", "", ""))
		if rec := postRoute(t, mux, "/api/runs", `{"source":"bundled://publish"}`); rec.Code != http.StatusOK {
			t.Errorf("POST /api/runs = %d %s, want 200", rec.Code, rec.Body.String())
		}
	})
}

// A schedule's slot whose pins cannot be checked waits on the engine like any unanswered launch;
// a refused pin is a plain failure.
func TestLaunchScheduled_StepPinRefusals(t *testing.T) {
	t.Run("uncheckable", func(t *testing.T) {
		h, _, br := newTestHub()
		launchableRecipe(br, "wf_sched")
		pinnedRecipe(br, planStep("review", "opus", "high"))
		br.callErrs = map[string]error{methodKiroConfigTemplate: errors.New("engine down")}
		_, _, err := h.runs.LaunchScheduled(t.Context(), "bundled://publish", launchrule.Row{ID: "sched-1"},
			nil, marotte.RunDefaults{}, time.Now().Add(time.Hour))
		if !errors.Is(err, schedule.ErrEngineBusy) {
			t.Errorf("LaunchScheduled = %v, want schedule.ErrEngineBusy", err)
		}
	})
	t.Run("refused", func(t *testing.T) {
		h, br, _ := runDefaultsHub(t)
		pinnedRecipe(br, planStep("review", "gpt-9", ""))
		_, _, err := h.runs.LaunchScheduled(t.Context(), "bundled://publish", launchrule.Row{ID: "sched-1"},
			nil, marotte.RunDefaults{}, time.Now().Add(time.Hour))
		if !errors.Is(err, errDefaultsRefused) || errors.Is(err, schedule.ErrEngineBusy) {
			t.Errorf("LaunchScheduled = %v, want errDefaultsRefused and no engine-busy retry", err)
		}
		if slices.Contains(br.callLog(), methodKiroWorkflowNew) {
			t.Errorf("calls = %v, want no workflow/new for a refused pin", br.callLog())
		}
	})
}
