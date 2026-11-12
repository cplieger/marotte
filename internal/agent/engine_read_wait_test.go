package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// readBudget is a bound no engine-backed read reaches when it answers at once; a read that waits for the engine
// runs into it and fails with DeadlineExceeded.
const readBudget = 2 * time.Second

// A read marked WithoutEngineWait answers ErrBridgeExited at once while no engine is ready (starting, or down after a
// failed start), instead of holding its request until its budget ends.
func TestEngineCall_ReadWithoutEngineWaitAnswersAtOnce(t *testing.T) {
	cases := []struct {
		setup func(src *fakeEngineSource)
		name  string
	}{
		{name: "starting", setup: func(src *fakeEngineSource) { src.startGate = make(chan struct{}) }},
		{name: "down", setup: func(src *fakeEngineSource) { src.startErr = errors.New("exec: kiro-cli: not found") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
			tc.setup(src)
			o := ownerFixture(t, src, engineHooks{})
			o.start()
			ctx, cancel := context.WithTimeout(marotte.WithoutEngineWait(t.Context()), readBudget)
			defer cancel()
			_, err := o.rawCall(ctx, "config template call", methodKiroConfigTemplate, map[string]any{})
			if !errors.Is(err, marotte.ErrBridgeExited) {
				t.Errorf("rawCall error = %v, want one wrapping ErrBridgeExited", err)
			}
			if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				t.Errorf("rawCall waited for its budget (error %v), want an answer at once", err)
			}
		})
	}
}

// The recipe list's listing runs on the runtime's lifetime, not the request's, so the route itself answers a marked read
// while no engine is ready, at once: the held catalog when there is one, else 503 recipe_list_slow with a Retry-After.
func TestRecipesRoute_AReadWithoutEngineWaitAnswersAtOnceWhileTheEngineStarts(t *testing.T) {
	cases := []struct {
		held     []marotte.Recipe
		name     string
		wantCode int
		wantBody string
	}{
		{name: "no held catalog", wantCode: http.StatusServiceUnavailable, wantBody: "recipe_list_slow"},
		{
			name:     "a held catalog",
			held:     []marotte.Recipe{{Name: "held-recipe", Source: "bundled://held-recipe"}},
			wantCode: http.StatusOK, wantBody: "held-recipe",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
			src.startGate = make(chan struct{})
			o := ownerFixture(t, src, engineHooks{})
			o.start()
			runs := &Runs{engine: o, lifecycle: o.lt}
			runs.recipes.held = tc.held
			mux := http.NewServeMux()
			(&runRoutes{runs: runs}).register(mux)
			ctx, cancel := context.WithTimeout(marotte.WithoutEngineWait(t.Context()), readBudget)
			defer cancel()
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/recipes", nil))
			if ctx.Err() != nil {
				t.Errorf("GET /api/recipes waited for its budget (%d %s), want an answer at once", rec.Code, rec.Body.String())
			}
			if rec.Code != tc.wantCode || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Errorf("GET /api/recipes = %d %s, want %d with %q", rec.Code, rec.Body.String(), tc.wantCode, tc.wantBody)
			}
			if tc.wantCode == http.StatusServiceUnavailable && rec.Header().Get("Retry-After") == "" {
				t.Errorf("GET /api/recipes = %d with no Retry-After, want one", rec.Code)
			}
		})
	}
}

// An unmarked caller (a command, a session door) still waits for the engine, and a marked read on a ready engine
// takes its door as any other.
func TestEngineCall_UnmarkedCallerStillWaitsForTheEngine(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
		gate := make(chan struct{})
		src.startGate = gate
		o := ownerFixture(t, src, engineHooks{})
		o.start()
		got := make(chan error, 1)
		go func() {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			_, _, release, err := o.acquire(ctx)
			if err == nil {
				release()
			}
			got <- err
		}()
		synctest.Wait()
		select {
		case err := <-got:
			t.Fatalf("an unmarked acquire answered %v before the engine was ready, want it to wait", err)
		default:
		}
		close(gate)
		if err := <-got; err != nil {
			t.Errorf("an unmarked acquire across the start = %v, want the ready engine", err)
		}
		ctx, cancel := context.WithTimeout(marotte.WithoutEngineWait(t.Context()), readBudget)
		defer cancel()
		_, _, release, err := o.acquire(ctx)
		if err != nil {
			t.Fatalf("a marked acquire on the ready engine = %v, want its door", err)
		}
		release()
	})
}
