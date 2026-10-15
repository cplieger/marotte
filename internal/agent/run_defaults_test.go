package agent

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/schedule"
)

// runDefaultsHub is a hub whose live catalog offers opus (with tiers) and auto (none), with
// one launchable recipe and a schedule store behind the run routes.
func runDefaultsHub(t *testing.T) (*Runtime, *fakeBridge, *http.ServeMux) {
	t.Helper()
	h, _, br := newTestHub()
	launchableRecipe(br, "wf_pick")
	h.catalog.SetModels([]marotte.SessionModel{
		{ID: "opus", Name: "Opus", HasEffort: true, EffortTiers: []string{"low", "medium", "high"}},
		{ID: "auto", Name: "Auto"},
	})
	st, err := schedule.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("schedule.NewStore: %v", err)
	}
	h.runs.schedules = st
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
		br.callErrs = map[string]error{methodKiroConfigTemplate: errors.New("utility down")}
		rec := postRoute(t, mux, "/api/runs", `{"source":"bundled://publish","model":"m-default"}`)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("POST /api/runs = %d %s, want 503", rec.Code, rec.Body.String())
		}
		if slices.Contains(br.callLog(), methodKiroWorkflowNew) {
			t.Errorf("calls = %v, want no workflow/new for an unchecked pick", br.callLog())
		}
	})
	t.Run("a Default launch reads no catalog", func(t *testing.T) {
		br, mux := newHub(t)
		br.callErrs = map[string]error{methodKiroConfigTemplate: errors.New("utility down")}
		if rec := postRoute(t, mux, "/api/runs", `{"source":"bundled://publish"}`); rec.Code != http.StatusOK {
			t.Errorf("POST /api/runs = %d %s, want 200", rec.Code, rec.Body.String())
		}
	})
}

// A schedule stores its pick, shows it, keeps it across a restart, and refuses an unknown one
// at save time without touching the stored row.
func TestScheduleRoute_StoresThePickAndRefusesAnUnknownOne(t *testing.T) {
	h, _, mux := runDefaultsHub(t)
	const spec = `"spec":{"freq":"daily","hour":2,"minute":0}`
	rec := postRoute(t, mux, "/api/schedules", `{"source":"bundled://publish","model":"opus","effort":"high",`+spec+`,"enabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/schedules = %d %s, want 200", rec.Code, rec.Body.String())
	}
	var view scheduleView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if view.Model != "opus" || view.Effort != "high" {
		t.Errorf("saved view model/effort = %q/%q, want opus/high", view.Model, view.Effort)
	}

	rec = postRoute(t, mux, "/api/schedules", `{"source":"bundled://publish","model":"gpt-9",`+spec+`,"enabled":true}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(errorText(t, rec), `"gpt-9" is not offered`) {
		t.Errorf("POST /api/schedules with an unknown model = %d %s, want 400 naming it", rec.Code, rec.Body.String())
	}

	list := h.runs.schedules.List()
	if len(list) != 1 || list[0].Model != "opus" || list[0].Effort != "high" {
		t.Errorf("stored schedules = %+v, want the one opus/high row the refusal left alone", list)
	}
}

// A schedule's pick reaches workflow/new at its fire.
func TestLaunchScheduled_SendsTheSchedulesPick(t *testing.T) {
	h, _, br := newTestHub()
	launchableRecipe(br, "wf_sched")
	if _, _, err := h.runs.LaunchScheduled(t.Context(), "bundled://publish", "sched-1",
		marotte.RunDefaults{Model: "opus", Effort: "low"}, time.Now().Add(time.Hour)); err != nil {
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
