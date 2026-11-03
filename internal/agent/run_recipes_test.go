package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func listRecipesCalls(br *fakeBridge) int {
	n := 0
	for _, m := range br.callLog() {
		if m == methodKiroWorkflowListRecipes {
			n++
		}
	}
	return n
}

func recipesChangedFrame() *marotte.RPCResponse {
	return &marotte.RPCResponse{Method: methodKiroWorkflowRecipesChanged, Params: json.RawMessage(`{}`)}
}

// A launch resolves its source against the catalog an earlier listing left, so a slow catalog walk on a busy engine
// cannot fail it.
func TestLaunch_ResolvesItsSourceWithoutAnotherListing(t *testing.T) {
	h, _, br := newTestHub()
	launchableRecipe(br, "wf_held")
	if _, err := h.runs.listRecipes(t.Context()); err != nil {
		t.Fatalf("listRecipes: %v", err)
	}
	br.forgetCalls()

	if _, _, err := h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{}); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if n := listRecipesCalls(br); n != 0 {
		t.Errorf("launch made %d listRecipes calls, want 0 with a catalog held: %v", n, br.callLog())
	}
}

// A schedule save resolves its source the same way.
func TestScheduleSave_ResolvesItsSourceWithoutAnotherListing(t *testing.T) {
	_, br, mux := runDefaultsHub(t)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/recipes", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/recipes = %d %s", rec.Code, rec.Body.String())
	}
	br.forgetCalls()

	rec = postRoute(t, mux, "/api/schedules",
		`{"source":"bundled://publish","spec":{"freq":"daily","hour":2,"minute":0},"enabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/schedules = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if n := listRecipesCalls(br); n != 0 {
		t.Errorf("schedule save made %d listRecipes calls, want 0 with a catalog held", n)
	}
}

// Readers arriving while a listing runs share it: KAS admits every recipe file per call, so overlapping walks are
// what stretched the 2026-11-02 listing past its deadline.
func TestListRecipes_ConcurrentReadersShareOneListing(t *testing.T) {
	h, _, br := newTestHub()
	launchableRecipe(br, "wf_shared")
	release := make(chan struct{})
	br.blockOn = map[string]chan struct{}{methodKiroWorkflowListRecipes: release}

	const readers = 4
	var wg sync.WaitGroup
	errs := make([]error, readers)
	for i := range readers {
		wg.Go(func() {
			got, err := h.runs.listRecipes(t.Context())
			if err == nil && len(got) != 1 {
				err = fmt.Errorf("got %d recipes, want 1", len(got))
			}
			errs[i] = err
		})
	}
	waitFor(t, func() bool { return listRecipesCalls(br) >= 1 })
	close(release)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("reader %d: %v", i, err)
		}
	}
	if n := listRecipesCalls(br); n != 1 {
		t.Errorf("%d concurrent readers made %d listRecipes calls, want 1", readers, n)
	}
}

// One reader walking away leaves the shared listing running for the others.
func TestListRecipes_AReaderWalkingAwayDoesNotFailTheOthers(t *testing.T) {
	h, _, br := newTestHub()
	launchableRecipe(br, "wf_walk")
	release := make(chan struct{})
	br.blockOn = map[string]chan struct{}{methodKiroWorkflowListRecipes: release}

	gone, cancel := context.WithCancel(t.Context())
	goneErr := make(chan error, 1)
	go func() {
		_, err := h.runs.listRecipes(gone)
		goneErr <- err
	}()
	waitFor(t, func() bool { return listRecipesCalls(br) >= 1 })
	stayed := make(chan error, 1)
	go func() {
		_, err := h.runs.listRecipes(t.Context())
		stayed <- err
	}()
	cancel()
	if err := <-goneErr; !errors.Is(err, context.Canceled) {
		t.Errorf("the departed reader's error = %v, want context.Canceled", err)
	}
	close(release)
	if err := <-stayed; err != nil {
		t.Errorf("the remaining reader failed with its peer's cancel: %v", err)
	}
}

// KAS's recipes_changed and the engine's end each drop the held catalog, so the next read lists again.
func TestListRecipes_ACatalogChangeOrEngineEndListsAgain(t *testing.T) {
	cases := []struct {
		drop func(h *Runtime)
		name string
	}{
		{name: "recipes_changed", drop: func(h *Runtime) {
			h.dispatchWorkspace(context.Background(), "chat-1", recipesChangedFrame())
		}},
		{name: "engine end", drop: func(h *Runtime) { h.onEngineEnded(1) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, br := newTestHub()
			launchableRecipe(br, "wf_drop")
			if _, err := h.runs.listRecipes(t.Context()); err != nil {
				t.Fatalf("listRecipes: %v", err)
			}
			tc.drop(h)
			br.forgetCalls()
			br.setCallResult(methodKiroWorkflowListRecipes,
				json.RawMessage(`{"recipes":[{"name":"renamed","source":"bundled://publish"}]}`))
			got, err := h.runs.listRecipes(t.Context())
			if err != nil {
				t.Fatalf("listRecipes after %s: %v", tc.name, err)
			}
			if n := listRecipesCalls(br); n != 1 || len(got) != 1 || got[0].Name != "renamed" {
				t.Errorf("after %s: %d listRecipes calls, recipes %+v; want one fresh listing", tc.name, n, got)
			}
		})
	}
}

// A listing that began before a catalog change never becomes the held catalog: its answer may predate the change.
func TestListRecipes_AListingOverlappingAChangeIsNotHeld(t *testing.T) {
	h, _, br := newTestHub()
	launchableRecipe(br, "wf_stale")
	release := make(chan struct{})
	br.blockOn = map[string]chan struct{}{methodKiroWorkflowListRecipes: release}
	done := make(chan error, 1)
	go func() {
		_, err := h.runs.listRecipes(t.Context())
		done <- err
	}()
	waitFor(t, func() bool { return listRecipesCalls(br) >= 1 })
	h.dispatchWorkspace(context.Background(), "chat-1", recipesChangedFrame())
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("the overlapping listing: %v", err)
	}
	br.forgetCalls()
	if _, err := h.runs.listRecipes(t.Context()); err != nil {
		t.Fatalf("listRecipes: %v", err)
	}
	if n := listRecipesCalls(br); n != 1 {
		t.Errorf("the read after the change made %d listRecipes calls, want 1: the stale answer was held", n)
	}
}

// A listing that runs out of time is the engine being busy: every route reading it answers 503 with a Retry-After,
// never a 400 that blames the user's input or a bare 500.
func TestRecipeRoutes_AListingDeadlineAnswers503WithARetryHint(t *testing.T) {
	routes := []struct {
		method, target, body string
	}{
		{http.MethodGet, "/api/recipes", ""},
		{http.MethodPost, "/api/runs", `{"source":"bundled://publish"}`},
		{http.MethodPost, "/api/schedules", `{"source":"bundled://publish","spec":{"freq":"daily","hour":2,"minute":0},"enabled":true}`},
	}
	for _, rt := range routes {
		t.Run(rt.method+" "+rt.target, func(t *testing.T) {
			_, br, mux := runDefaultsHub(t)
			br.callErrs = map[string]error{
				methodKiroWorkflowListRecipes: fmt.Errorf("engine call: %w", context.DeadlineExceeded),
			}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(rt.method, rt.target, nil)
			if rt.body != "" {
				req = httptest.NewRequest(rt.method, rt.target, strings.NewReader(rt.body))
			}
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("status = %d %s, want 503", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Retry-After"); got == "" {
				t.Error("no Retry-After header")
			}
			var body struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Code != "recipe_list_slow" {
				t.Errorf("body = %s, want code recipe_list_slow", rec.Body.String())
			}
		})
	}
}

// A schedule save tells a source the catalog lacks (400) from a catalog it could not read (500).
func TestScheduleSave_TellsAnUnknownSourceFromAnUnreadableCatalog(t *testing.T) {
	t.Run("unknown source", func(t *testing.T) {
		_, _, mux := runDefaultsHub(t)
		rec := postRoute(t, mux, "/api/schedules",
			`{"source":"/etc/passwd","spec":{"freq":"daily","hour":2,"minute":0},"enabled":true}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d %s, want 400", rec.Code, rec.Body.String())
		}
	})
	t.Run("unreadable catalog", func(t *testing.T) {
		_, br, mux := runDefaultsHub(t)
		br.callErrs = map[string]error{methodKiroWorkflowListRecipes: errors.New("engine gone")}
		rec := postRoute(t, mux, "/api/schedules",
			`{"source":"bundled://publish","spec":{"freq":"daily","hour":2,"minute":0},"enabled":true}`)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d %s, want 500", rec.Code, rec.Body.String())
		}
	})
}
