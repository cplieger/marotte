package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
)

// TestRegisterSchedule_MountsTheSurfaceOnlyWithAStoreBehindIt pins that without a store the handlers would panic;
// with one, skipping them would 404 a working feature.
func TestRegisterSchedule_MountsTheSurfaceOnlyWithAStoreBehindIt(t *testing.T) {
	t.Run("a configured store gets the routes", func(t *testing.T) {
		st, err := launchrule.NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("launchrule.NewStore: %v", err)
		}
		mux := http.NewServeMux()
		(&runRoutes{runs: &Runs{launchRules: st}}).register(mux)

		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/schedules", nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET /api/schedules = %d, want 200; the schedule UI reads this: %s",
				rec.Code, rec.Body.String())
		}
	})

	t.Run("no store means no routes, not broken ones", func(t *testing.T) {
		mux := http.NewServeMux()
		(&runRoutes{runs: &Runs{}}).register(mux)

		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/schedules", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET /api/schedules = %d with no store, want 404; a registered route with no "+
				"store behind it fails inside the handler instead: %s", rec.Code, rec.Body.String())
		}
	})
}

func TestSchedulesRouteOverLaunchRules(t *testing.T) {
	h, _, br := newTestHub()
	launchableRecipe(br, "wf_route")
	dir := t.TempDir()
	st, err := launchrule.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h.runs.launchRules = st
	mux := http.NewServeMux()
	(&runRoutes{runs: h.runs, epoch: h.Epoch}).register(mux)
	rules := func(t *testing.T) []map[string]any {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(dir, "launch-rules.json"))
		if err != nil {
			t.Fatalf("read launch-rules.json: %v", err)
		}
		var out []map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode launch-rules.json: %v", err)
		}
		return out
	}
	const body = `{"source":"bundled://publish","spec":{"freq":"daily","hour":3,"minute":0},"enabled":true}`
	if rec := postRoute(t, mux, "/api/schedules", body); rec.Code != http.StatusOK {
		t.Fatalf("POST /api/schedules = %d %s, want 200", rec.Code, rec.Body.String())
	}
	first := rules(t)
	t.Run("a post creates a clock rule", func(t *testing.T) {
		when, _ := first[0]["when"].(map[string]any)
		clock, _ := when["clock"].(map[string]any)
		if len(first) != 1 || first[0]["source"] != "bundled://publish" || clock["hour"] != float64(3) {
			t.Errorf("launch-rules.json = %v, want one clock rule at 03:00", first)
		}
	})
	if rec := postRoute(t, mux, "/api/schedules", strings.Replace(body, `"hour":3`, `"hour":5`, 1)); rec.Code != http.StatusOK {
		t.Fatalf("second POST /api/schedules = %d %s, want 200", rec.Code, rec.Body.String())
	}
	t.Run("a second post for the source replaces it", func(t *testing.T) {
		got := rules(t)
		when, _ := got[0]["when"].(map[string]any)
		clock, _ := when["clock"].(map[string]any)
		if len(got) != 1 || got[0]["id"] != first[0]["id"] || clock["hour"] != float64(5) {
			t.Errorf("launch-rules.json = %v, want the same rule at 05:00", got)
		}
	})
	var list struct {
		Schedules []struct {
			NextRunAt *time.Time      `json:"next_run_at"`
			ID        string          `json:"id"`
			Source    string          `json:"source"`
			Spec      launchrule.Spec `json:"spec"`
			Enabled   bool            `json:"enabled"`
		} `json:"schedules"`
	}
	t.Run("a get answers today's shape", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/schedules", nil))
		if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("GET /api/schedules = %d %s", rec.Code, rec.Body.String())
		}
		s := list.Schedules
		if len(s) != 1 || s[0].ID != first[0]["id"] || s[0].Source != "bundled://publish" || s[0].Spec.Hour != 5 ||
			!s[0].Enabled || s[0].NextRunAt == nil {
			t.Errorf("GET /api/schedules = %s, want the one enabled 05:00 schedule with its next run", rec.Body.String())
		}
	})
	t.Run("a delete removes it", func(t *testing.T) {
		if len(list.Schedules) != 1 {
			t.Fatalf("the list answered %d schedules, so there is no id to delete", len(list.Schedules))
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/schedules/"+list.Schedules[0].ID, nil))
		if rec.Code != http.StatusOK || len(rules(t)) != 0 {
			t.Errorf("DELETE = %d %s, rules left %v", rec.Code, rec.Body.String(), rules(t))
		}
	})
}

// anchorClockRule moves a clock rule's anchor off its creation time.
func anchorClockRule(t *testing.T, st *launchrule.Store, row launchrule.Row, anchor time.Time) {
	t.Helper()
	if err := st.UpdateClock(t.Context(), row, func(s *launchrule.Status) bool { s.Anchor = anchor; return true }); err != nil {
		t.Fatalf("UpdateClock: %v", err)
	}
}

// putClockRule stores a clock rule for source, anchored at its creation.
func putClockRule(t *testing.T, st *launchrule.Store, source string, spec launchrule.Spec, enabled bool) launchrule.Rule {
	t.Helper()
	r, err := st.Create(t.Context(), &launchrule.Rule{Source: source, Enabled: enabled, When: launchrule.When{Clock: &spec}})
	if err != nil {
		t.Fatalf("Create(%s): %v", source, err)
	}
	return r
}
