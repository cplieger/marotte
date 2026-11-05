package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/agent"
	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/schedule"
)

// TestScheduleDelete_ThroughTheRequestPathGuard drives the agent's real schedule routes behind
// canonicalAPIPath with the id the list answered, escaped as the client's encodeURIComponent does.
// The seeded row is one a recipe under /workspace wrote: its source is an absolute path.
func TestScheduleDelete_ThroughTheRequestPathGuard(t *testing.T) {
	for name, source := range map[string]string{
		"workspace recipe": "/workspace/.kiro/workflows/weekly-smoke.workflow.json",
		"bundled recipe":   "bundled://release-notes",
		"dotted segments":  "/workspace/../workspace/./x.workflow.json",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			row := []map[string]any{{
				"id": source, "source": source, "anchor": "2026-01-01T00:00:00Z",
				"spec": map[string]any{"freq": "daily", "hour": 3}, "enabled": false,
			}}
			data, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "schedules.json"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			st, err := schedule.NewStore(dir)
			if err != nil {
				t.Fatalf("schedule.NewStore: %v", err)
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

func scheduleMux(t *testing.T, st *schedule.Store) *http.ServeMux {
	t.Helper()
	chats, err := chat.NewStore(filepath.Join(t.TempDir(), "chats"))
	if err != nil {
		t.Fatalf("chat store: %v", err)
	}
	rt := agent.New(t.Context(), t.TempDir(), nil, chats, agent.WithSchedules(st))
	t.Cleanup(func() {
		if err := rt.Shutdown(context.Background()); err != nil {
			t.Errorf("runtime shutdown: %v", err)
		}
	})
	mux := http.NewServeMux()
	rt.RegisterRoutes(mux)
	return mux
}
