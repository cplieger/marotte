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
	"time"

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

// A launch the engine leaves unanswered past the recipe listing (its run list, or an engine not ready) is the engine
// being busy too: 503 with a Retry-After, never a 400 that blames the user's input.
func TestLaunch_AnEngineDeadlineAnswers503WithARetryHint(t *testing.T) {
	cases := []struct {
		fail error
		name string
	}{
		{name: "the run list ran out of time", fail: fmt.Errorf("engine call: %w", context.DeadlineExceeded)},
		{name: "the engine was not ready", fail: fmt.Errorf("%w: the agent engine is not ready", marotte.ErrBridgeExited)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, br, mux := runDefaultsHub(t)
			br.callErrs = map[string]error{methodKiroWorkflowList: tc.fail}
			rec := postRoute(t, mux, "/api/runs", `{"source":"bundled://publish"}`)
			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("status = %d %s, want 503", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Retry-After"); got == "" {
				t.Error("no Retry-After header")
			}
			var body struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Code != "engine_slow" {
				t.Errorf("body = %s, want code engine_slow", rec.Body.String())
			}
		})
	}
}

// addedRecipe makes the fake's listing name a second recipe, as KAS would after a file was added.
func addedRecipe(br *fakeBridge) {
	const both = `{"recipes":[{"name":"publish","source":"bundled://publish"},{"name":"added","source":"bundled://added"}]}`
	br.setCallResult(methodKiroWorkflowListRecipes, json.RawMessage(both))
}

// KAS reports a catalog change only through a session with workflows on, so a recipe added while none is live leaves
// the held catalog behind: a launch or a save of its source lists again before refusing it.
func TestRecipeLookup_FindsARecipeAddedSinceTheCatalogWasHeld(t *testing.T) {
	t.Run("a launch", func(t *testing.T) {
		h, _, br := newTestHub()
		launchableRecipe(br, "wf_added")
		if _, err := h.runs.listRecipes(t.Context()); err != nil {
			t.Fatalf("listRecipes: %v", err)
		}
		addedRecipe(br)
		br.forgetCalls()
		if _, name, err := h.runs.launch(t.Context(), "bundled://added", nil, marotte.RunDefaults{}); err != nil || name != "added" {
			t.Fatalf("launch = (%q, %v), want the added recipe launched", name, err)
		}
		if n := listRecipesCalls(br); n != 1 {
			t.Errorf("launch made %d listRecipes calls, want 1", n)
		}
	})
	t.Run("a schedule save", func(t *testing.T) {
		h, br, mux := runDefaultsHub(t)
		if _, err := h.runs.listRecipes(t.Context()); err != nil {
			t.Fatalf("listRecipes: %v", err)
		}
		addedRecipe(br)
		rec := postRoute(t, mux, "/api/schedules",
			`{"source":"bundled://added","spec":{"freq":"daily","hour":2,"minute":0},"enabled":true}`)
		if rec.Code != http.StatusOK {
			t.Errorf("POST /api/schedules = %d %s, want 200", rec.Code, rec.Body.String())
		}
	})
}

// A source no listing names is refused after one listing, held catalog or not.
func TestRecipeLookup_RefusesAnUnknownSourceAfterOneListing(t *testing.T) {
	for _, held := range []bool{false, true} {
		t.Run(fmt.Sprintf("held=%v", held), func(t *testing.T) {
			h, _, br := newTestHub()
			launchableRecipe(br, "wf_unknown")
			if held {
				if _, err := h.runs.listRecipes(t.Context()); err != nil {
					t.Fatalf("listRecipes: %v", err)
				}
			}
			br.forgetCalls()
			_, err := h.runs.recipeBySource(t.Context(), "bundled://nope")
			if !errors.Is(err, errUnknownRecipe) {
				t.Errorf("recipeBySource = %v, want errUnknownRecipe", err)
			}
			if n := listRecipesCalls(br); n != 1 {
				t.Errorf("%d listRecipes calls, want 1", n)
			}
		})
	}
}

// The recipe list a person opens lists afresh, so it shows a recipe added with no catalog change reported.
func TestRecipesRoute_ListsAfreshOverAHeldCatalog(t *testing.T) {
	h, br, mux := runDefaultsHub(t)
	if _, err := h.runs.listRecipes(t.Context()); err != nil {
		t.Fatalf("listRecipes: %v", err)
	}
	addedRecipe(br)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/recipes", nil))
	var body marotte.RecipesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Recipes) != 2 {
		t.Errorf("GET /api/recipes = %d %s, want both recipes", rec.Code, rec.Body.String())
	}
}

// A fresh listing on a busy engine does not hold the recipe list up: past its patience it answers the held catalog,
// and the listing, once done, becomes the held catalog.
func TestRecipesRoute_AnswersTheHeldCatalogWhileAListingIsSlow(t *testing.T) {
	saved := recipeListPatience
	recipeListPatience = 10 * time.Millisecond
	t.Cleanup(func() { recipeListPatience = saved })
	h, br, mux := runDefaultsHub(t)
	if _, err := h.runs.listRecipes(t.Context()); err != nil {
		t.Fatalf("listRecipes: %v", err)
	}
	addedRecipe(br)
	release := make(chan struct{})
	br.blockOn = map[string]chan struct{}{methodKiroWorkflowListRecipes: release}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/recipes", nil))
	var body marotte.RecipesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK || len(body.Recipes) != 1 {
		t.Errorf("GET /api/recipes = %d %s, want the held catalog's one recipe", rec.Code, rec.Body.String())
	}
	close(release)
	waitFor(t, func() bool {
		got, err := h.runs.listRecipes(t.Context())
		return err == nil && len(got) == 2
	})
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
