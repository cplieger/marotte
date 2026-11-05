package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/agent"
	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/launchrule"
)

// TestScheduleDelete_ThroughTheRequestPathGuard drives the agent's real schedule routes behind
// canonicalAPIPath with the id the list answered, escaped as the client's encodeURIComponent does.
// One seeded rule is a recipe under /workspace: its source is an absolute path.
func TestScheduleDelete_ThroughTheRequestPathGuard(t *testing.T) {
	for name, source := range map[string]string{
		"workspace recipe": "/workspace/.kiro/workflows/weekly-smoke.workflow.json",
		"bundled recipe":   "bundled://release-notes",
		"dotted segments":  "/workspace/../workspace/./x.workflow.json",
	} {
		t.Run(name, func(t *testing.T) {
			st, err := launchrule.NewStore(t.TempDir())
			if err != nil {
				t.Fatalf("launchrule.NewStore: %v", err)
			}
			if _, err := st.Create(t.Context(), &launchrule.Rule{
				Source: source, When: launchrule.When{Clock: &launchrule.Spec{Freq: launchrule.FreqDaily, Hour: 3}},
			}); err != nil {
				t.Fatalf("Create: %v", err)
			}
			h := canonicalAPIPath(scheduleMux(t, st))

			list := do(h, http.MethodGet, "/api/schedules")
			var got struct {
				Schedules []struct {
					ID string `json:"id"`
				} `json:"schedules"`
			}
			if err := json.Unmarshal(list.Body.Bytes(), &got); err != nil || len(got.Schedules) != 1 {
				t.Fatalf("GET /api/schedules = %d %s, want the one seeded row", list.Code, list.Body.String())
			}

			id := got.Schedules[0].ID
			rec := do(h, http.MethodDelete, "/api/schedules/"+url.PathEscape(id))
			if rec.Code != http.StatusOK {
				t.Fatalf("DELETE /api/schedules/{%q} = %d %s, want 200", id, rec.Code, rec.Body.String())
			}
			if left := st.List(); len(left) != 0 {
				t.Errorf("the row survived its delete: %+v", left)
			}
		})
	}
}

func scheduleMux(t *testing.T, st *launchrule.Store) *http.ServeMux {
	t.Helper()
	chats, err := chat.NewStore(filepath.Join(t.TempDir(), "chats"))
	if err != nil {
		t.Fatalf("chat store: %v", err)
	}
	rt := agent.New(t.Context(), t.TempDir(), nil, chats, agent.WithLaunchRules(st))
	t.Cleanup(func() {
		if err := rt.Shutdown(context.Background()); err != nil {
			t.Errorf("runtime shutdown: %v", err)
		}
	})
	mux := http.NewServeMux()
	rt.RegisterRoutes(mux)
	return mux
}
