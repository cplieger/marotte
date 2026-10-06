package agent

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cplieger/marotte/internal/schedule"
)

// TestRegisterSchedule_MountsTheSurfaceOnlyWithAStoreBehindIt pins that without a store the handlers would panic;
// with one, skipping them would 404 a working feature.
func TestRegisterSchedule_MountsTheSurfaceOnlyWithAStoreBehindIt(t *testing.T) {
	t.Run("a configured store gets the routes", func(t *testing.T) {
		st, err := schedule.NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("schedule.NewStore: %v", err)
		}
		mux := http.NewServeMux()
		(&runRoutes{runs: &Runs{schedules: st}}).registerSchedule(mux)

		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/schedules", nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET /api/schedules = %d, want 200; the schedule UI reads this: %s",
				rec.Code, rec.Body.String())
		}
	})

	t.Run("no store means no routes, not broken ones", func(t *testing.T) {
		mux := http.NewServeMux()
		(&runRoutes{runs: &Runs{}}).registerSchedule(mux)

		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/schedules", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET /api/schedules = %d with no store, want 404; a registered route with no "+
				"store behind it fails inside the handler instead: %s", rec.Code, rec.Body.String())
		}
	})
}
