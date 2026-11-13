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
	"github.com/cplieger/marotte/internal/schedule"
)

// runDefaultsHub is a hub whose live catalog offers model-a (low, medium, high), model-c (low) and
// auto (none), with one launchable recipe and a launch-rule store behind the run routes.
func runDefaultsHub(t *testing.T) (*Runtime, *fakeBridge, *http.ServeMux) {
	t.Helper()
	h, _, br := newTestHub()
	launchableRecipe(br, "wf_pick")
	h.catalog.SetModels([]marotte.SessionModel{
		{ID: "model-a", Name: "Model A", EffortTiers: []string{"low", "medium", "high"}},
		{ID: "model-c", Name: "Model C", EffortTiers: []string{"low"}},
		{ID: "auto", Name: "Auto"},
	})
	st, _, err := launchrule.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("launchrule.NewStore: %v", err)
	}
	h.runs.launchRules = st
	mux := http.NewServeMux()
	(&runRoutes{runs: h.runs, epoch: h.Epoch}).register(mux)
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
		{name: "model only", body: `{"source":"bundled://publish","model":"model-a"}`, wantModel: "model-a"},
		{
			name:      "model and effort",
			body:      `{"source":"bundled://publish","model":"model-a","effort":"high"}`,
			wantModel: "model-a", wantEffort: "high",
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
		{name: "unknown model", body: `{"source":"bundled://publish","model":"model-e-9"}`, wantText: `"model-e-9" is not offered`},
		{name: "effort with no model", body: `{"source":"bundled://publish","effort":"high"}`, wantText: "an effort needs a model"},
		{name: "effort on a tierless model", body: `{"source":"bundled://publish","model":"auto","effort":"high"}`, wantText: `"auto" offers no effort levels`},
		{name: "tier the model does not offer", body: `{"source":"bundled://publish","model":"model-a","effort":"max"}`, wantText: `does not offer the effort level "max"`},
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
		(&runRoutes{runs: h.runs, epoch: h.Epoch}).register(mux)
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
	rec := postRoute(t, mux, "/api/launch-rules", `{"source":"bundled://publish","model":"model-a","effort":"high",`+spec+`,"enabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/launch-rules = %d %s, want 200", rec.Code, rec.Body.String())
	}
	var view launchRuleView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if view.Model != "model-a" || view.Effort != "high" {
		t.Errorf("saved view model/effort = %q/%q, want model-a/high", view.Model, view.Effort)
	}

	rec = postRoute(t, mux, "/api/launch-rules", `{"source":"bundled://publish","model":"model-e-9",`+spec+`,"enabled":true}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(errorText(t, rec), `"model-e-9" is not offered`) {
		t.Errorf("POST /api/launch-rules with an unknown model = %d %s, want 400 naming it", rec.Code, rec.Body.String())
	}

	list := h.runs.launchRules.Clocks()
	if len(list) != 1 || list[0].Model != "model-a" || list[0].Effort != "high" {
		t.Errorf("stored schedules = %+v, want the one model-a/high row the refusal left alone", list)
	}
}

// A schedule's pick reaches workflow/new at its fire.
func TestLaunchScheduled_SendsTheSchedulesPick(t *testing.T) {
	h, _, br := newTestHub()
	launchableRecipe(br, "wf_sched")
	if _, _, err := h.runs.LaunchScheduled(t.Context(), "bundled://publish", launchrule.Row{ID: "sched-1"},
		nil, marotte.RunDefaults{Model: "model-a", Effort: "low"}, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("LaunchScheduled: %v", err)
	}
	params := br.paramsFor(methodKiroWorkflowNew)
	if params["modelId"] != "model-a" || params["effortLevel"] != "low" {
		t.Errorf("workflow/new modelId/effortLevel = %v/%v, want model-a/low", params["modelId"], params["effortLevel"])
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
	if want := []string{"low", "medium", "high"}; !slices.Equal(tiers["model-a"], want) || len(tiers["auto"]) != 0 {
		t.Errorf("served tiers = %v, want model-a %v and auto none", tiers, want)
	}
}

// planStep is one step node of a listRecipes plan as KAS's nodeToDescriptor writes it.
func planStep(id, model, effort string) string {
	return fmt.Sprintf(`{"nodeId":%q,"type":"step","agentName":"worker","modelId":%q,"effortLevel":%q}`, id, model, effort)
}

// pinnedRecipe makes the launchable recipe's plan pin one step.
func pinnedRecipe(br *fakeBridge, step string) {
	br.setCallResult(methodKiroWorkflowListRecipes, json.RawMessage(
		`{"recipes":[{"name":"publish","source":"bundled://publish","plan":[`+step+`]}]}`,
	))
}

// KAS owns a step pin's validation: it launches, warns, and drops or fails the unserved pin at step start.
func TestLaunch_SendsAStepPinTheCatalogDoesNotOffer(t *testing.T) {
	_, br, mux := runDefaultsHub(t)
	pinnedRecipe(br, planStep("review", "model-e-9", "xhigh"))
	rec := postRoute(t, mux, "/api/runs", `{"source":"bundled://publish"}`)
	if rec.Code != http.StatusOK {
		t.Errorf("POST /api/runs = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if !slices.Contains(br.callLog(), methodKiroWorkflowNew) {
		t.Errorf("calls = %v, want workflow/new sent", br.callLog())
	}
}

// Only the run's own pick reads the model list, so a pinned plan launches while it is unreadable.
func TestHandleLaunch_PinnedPlanNeedsNoModelList(t *testing.T) {
	h, _, br := newTestHub()
	launchableRecipe(br, "wf_pins")
	pinnedRecipe(br, planStep("review", "model-a", "high"))
	br.callErrs = map[string]error{methodKiroConfigTemplate: errors.New("engine down")}
	mux := http.NewServeMux()
	(&runRoutes{runs: h.runs, epoch: h.Epoch}).register(mux)
	if rec := postRoute(t, mux, "/api/runs", `{"source":"bundled://publish"}`); rec.Code != http.StatusOK {
		t.Errorf("POST /api/runs = %d %s, want 200", rec.Code, rec.Body.String())
	}
}

// A schedule's fire sends a recipe whose step pin the catalog does not offer.
func TestLaunchScheduled_StepPinsReachWorkflowNew(t *testing.T) {
	h, br, _ := runDefaultsHub(t)
	pinnedRecipe(br, planStep("review", "model-e-9", ""))
	id, _, err := h.runs.LaunchScheduled(t.Context(), "bundled://publish", launchrule.Row{ID: "sched-1"},
		nil, marotte.RunDefaults{}, time.Now().Add(time.Hour))
	if err != nil || id != "wf_pick" {
		t.Errorf("LaunchScheduled = %q, %v, want wf_pick and no error", id, err)
	}
	if errors.Is(err, schedule.ErrEngineBusy) || errors.Is(err, errDefaultsRefused) {
		t.Errorf("LaunchScheduled = %v, want no pin refusal", err)
	}
}

// workflow/new's warnings reach the launching browser verbatim.
func TestHandleLaunch_CarriesWorkflowNewWarnings(t *testing.T) {
	_, br, mux := runDefaultsHub(t)
	const warning = "[workflow] step 'a' modelId 'm-x' is not a served model; the step fails at its first model call"
	br.setCallResult(methodKiroWorkflowNew, json.RawMessage(`{"workflowId":"wf_pick","warnings":["`+warning+`"]}`))
	rec := postRoute(t, mux, "/api/runs", `{"source":"bundled://publish"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/runs = %d %s, want 200", rec.Code, rec.Body.String())
	}
	var body marotte.RunLaunchedResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if !slices.Equal(body.Warnings, []string{warning}) {
		t.Errorf("warnings = %q, want [%q]", body.Warnings, warning)
	}
}

// A launch KAS gave no warning answers no warnings key.
func TestHandleLaunch_OmitsWarningsWhenKASSentNone(t *testing.T) {
	for name, answer := range map[string]string{
		"absent": `{"workflowId":"wf_pick"}`,
		"empty":  `{"workflowId":"wf_pick","warnings":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, br, mux := runDefaultsHub(t)
			br.setCallResult(methodKiroWorkflowNew, json.RawMessage(answer))
			rec := postRoute(t, mux, "/api/runs", `{"source":"bundled://publish"}`)
			var body map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode %s: %v", rec.Body.String(), err)
			}
			if _, ok := body["warnings"]; ok || rec.Code != http.StatusOK {
				t.Errorf("POST /api/runs = %d %s, want 200 with no warnings key", rec.Code, rec.Body.String())
			}
		})
	}
}
