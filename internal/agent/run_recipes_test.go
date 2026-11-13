package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/schedule"
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

// holdCatalog lists once through the recipe list a person opens, leaving its answer held.
func holdCatalog(t *testing.T, h *Runtime) {
	t.Helper()
	if _, err := h.runs.recipeList(t.Context()); err != nil {
		t.Fatalf("recipeList: %v", err)
	}
}

// A held catalog is a display hint: a launch reads its recipe's name from a live listing, so a file whose recipe was
// renamed since the hold still meets that recipe's live run under the one-live-run rule.
func TestLaunch_ReadsTheRecipeNameLiveOverAHold(t *testing.T) {
	h, _, br := newTestHub()
	launchableRecipe(br, "wf_second")
	holdCatalog(t, h)
	br.setCallResult(methodKiroWorkflowListRecipes,
		json.RawMessage(`{"recipes":[{"name":"publish-v2","source":"bundled://publish"}]}`))
	br.setCallResult(methodKiroWorkflowList, json.RawMessage(
		`{"runs":[{"workflowId":"wf_live","name":"publish-v2","workflowName":"publish-v2","status":"running"}]}`,
	))
	br.forgetCalls()

	_, _, _, err := h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{})
	if busy, ok := errors.AsType[*schedule.BusyError](err); !ok || busy.WorkflowID != "wf_live" {
		t.Errorf("launch = %v, want the live run wf_live to refuse it", err)
	}
	if slices.Contains(br.callLog(), methodKiroWorkflowNew) {
		t.Errorf("calls = %v, want no workflow/new beside a live run of the same recipe", br.callLog())
	}
}

// A launch or a launch rule save of a source the held catalog still names, but the live listing no longer does, is
// refused as unknown.
func TestRecipeRoutes_RefuseASourceGoneSinceTheHold(t *testing.T) {
	routes := []struct{ name, target, body string }{
		{"a launch", "/api/runs", `{"source":"bundled://publish"}`},
		{
			"a launch rule save", "/api/launch-rules",
			`{"source":"bundled://publish","when":{"clock":{"freq":"daily","hour":2,"minute":0}},"enabled":true}`,
		},
	}
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			h, br, mux := runDefaultsHub(t)
			holdCatalog(t, h)
			br.setCallResult(methodKiroWorkflowListRecipes, json.RawMessage(`{"recipes":[]}`))
			rec := postRoute(t, mux, rt.target, rt.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("POST %s = %d %s, want 400", rt.target, rec.Code, rec.Body.String())
			}
			if slices.Contains(br.callLog(), methodKiroWorkflowNew) {
				t.Errorf("calls = %v, want no workflow/new for a source the live listing lacks", br.callLog())
			}
		})
	}
}

// Lookups arriving while a listing runs share it: KAS admits every recipe file per call, so overlapping walks are
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
			got, err := h.runs.recipeBySource(t.Context(), "bundled://publish")
			if err == nil && got.Name != "publish" {
				err = fmt.Errorf("got recipe %q, want publish", got.Name)
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
		_, err := h.runs.recipeBySource(gone, "bundled://publish")
		goneErr <- err
	}()
	waitFor(t, func() bool { return listRecipesCalls(br) >= 1 })
	stayed := make(chan error, 1)
	go func() {
		_, err := h.runs.recipeBySource(t.Context(), "bundled://publish")
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

// slowListing makes every later listing run out of time, as on a busy engine.
func slowListing(br *fakeBridge) {
	br.callErrs = map[string]error{methodKiroWorkflowListRecipes: fmt.Errorf("engine call: %w", context.DeadlineExceeded)}
}

// getRecipes is GET /api/recipes.
func getRecipes(mux *http.ServeMux) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/recipes", nil))
	return rec
}

// KAS's recipes_changed and the engine's end each drop the held catalog, so a slow listing has no stale list to show.
func TestRecipesRoute_ACatalogChangeOrEngineEndDropsTheHold(t *testing.T) {
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
			h, br, mux := runDefaultsHub(t)
			holdCatalog(t, h)
			tc.drop(h)
			slowListing(br)
			if rec := getRecipes(mux); rec.Code != http.StatusServiceUnavailable {
				t.Errorf("GET /api/recipes after %s = %d %s, want 503 with no hold to show", tc.name, rec.Code, rec.Body.String())
			}
		})
	}
}

// A listing that began before a catalog change never becomes the held catalog, since its answer may predate the
// change, and a lookup after the change starts a listing of its own.
func TestRecipeCatalog_AListingOverlappingAChangeIsNeitherHeldNorJoined(t *testing.T) {
	h, br, mux := runDefaultsHub(t)
	release := make(chan struct{})
	br.blockOn = map[string]chan struct{}{methodKiroWorkflowListRecipes: release}
	early := make(chan error, 1)
	go func() {
		_, err := h.runs.recipeList(t.Context())
		early <- err
	}()
	waitFor(t, func() bool { return listRecipesCalls(br) >= 1 })
	h.dispatchWorkspace(context.Background(), "chat-1", recipesChangedFrame())
	late := make(chan error, 1)
	go func() {
		_, err := h.runs.recipeBySource(t.Context(), "bundled://publish")
		late <- err
	}()
	waitFor(t, func() bool { return listRecipesCalls(br) >= 2 })
	h.dispatchWorkspace(context.Background(), "chat-1", recipesChangedFrame())
	close(release)
	if err := <-early; err != nil {
		t.Fatalf("the overlapping listing: %v", err)
	}
	if err := <-late; err != nil {
		t.Fatalf("the lookup after the change: %v", err)
	}
	slowListing(br)
	if rec := getRecipes(mux); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /api/recipes = %d %s, want 503: a listing that overlapped a change was held", rec.Code, rec.Body.String())
	}
}

// unansweredListings are the listing failures that mean the engine did not answer, not that it refused.
var unansweredListings = []struct {
	fail error
	name string
}{
	{name: "past its deadline", fail: fmt.Errorf("engine call: %w", context.DeadlineExceeded)},
	{name: "the engine exited", fail: fmt.Errorf("%w: the agent engine failed to start", marotte.ErrBridgeExited)},
}

// A listing the engine does not answer, past its deadline or because it exited, is the engine being busy: every
// route reading it answers 503 with a Retry-After, never a 400 that blames the user's input or a bare 500.
func TestRecipeRoutes_AnUnansweredListingAnswers503WithARetryHint(t *testing.T) {
	routes := []struct {
		name, method, target, body string
	}{
		{"the recipe list", http.MethodGet, "/api/recipes", ""},
		{"a launch", http.MethodPost, "/api/runs", `{"source":"bundled://publish"}`},
		{
			"a launch rule save", http.MethodPost, "/api/launch-rules",
			`{"source":"bundled://publish","when":{"clock":{"freq":"daily","hour":2,"minute":0}},"enabled":true}`,
		},
	}
	for _, rt := range routes {
		for _, lf := range unansweredListings {
			t.Run(rt.name+", "+lf.name, func(t *testing.T) {
				_, br, mux := runDefaultsHub(t)
				br.callErrs = map[string]error{methodKiroWorkflowListRecipes: lf.fail}
				answersRetryLater(t, mux, rt.method, rt.target, rt.body)
			})
		}
	}
}

// answersRetryLater asserts the route answers 503 recipe_list_slow with a Retry-After.
func answersRetryLater(t *testing.T, mux *http.ServeMux, method, target, body string) {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if body != "" {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("%s %s = %d %s, want 503", method, target, rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Errorf("%s %s: no Retry-After header", method, target)
	}
	var reply struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil || reply.Code != "recipe_list_slow" {
		t.Errorf("%s %s body = %s, want code recipe_list_slow", method, target, rec.Body.String())
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
// the held catalog behind: a launch or a save of its source finds it in a live listing.
func TestRecipeLookup_FindsARecipeAddedSinceTheCatalogWasHeld(t *testing.T) {
	t.Run("a launch", func(t *testing.T) {
		h, _, br := newTestHub()
		launchableRecipe(br, "wf_added")
		holdCatalog(t, h)
		addedRecipe(br)
		br.forgetCalls()
		if _, name, _, err := h.runs.launch(t.Context(), "bundled://added", nil, marotte.RunDefaults{}); err != nil || name != "added" {
			t.Fatalf("launch = (%q, %v), want the added recipe launched", name, err)
		}
		if n := listRecipesCalls(br); n != 1 {
			t.Errorf("launch made %d listRecipes calls, want 1", n)
		}
	})
	t.Run("a launch rule save", func(t *testing.T) {
		h, br, mux := runDefaultsHub(t)
		holdCatalog(t, h)
		addedRecipe(br)
		rec := postRoute(t, mux, "/api/launch-rules",
			`{"source":"bundled://added","when":{"clock":{"freq":"daily","hour":2,"minute":0}},"enabled":true}`)
		if rec.Code != http.StatusOK {
			t.Errorf("POST /api/launch-rules = %d %s, want 200", rec.Code, rec.Body.String())
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
				holdCatalog(t, h)
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
	holdCatalog(t, h)
	addedRecipe(br)
	rec := getRecipes(mux)
	var body marotte.RecipesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Recipes) != 2 {
		t.Errorf("GET /api/recipes = %d %s, want both recipes", rec.Code, rec.Body.String())
	}
}

// A fresh listing on a busy engine does not hold the recipe list up: past its patience it answers the held catalog.
func TestRecipesRoute_AnswersTheHeldCatalogWhileAListingIsSlow(t *testing.T) {
	saved := recipeListPatience
	recipeListPatience = 10 * time.Millisecond
	t.Cleanup(func() { recipeListPatience = saved })
	h, br, mux := runDefaultsHub(t)
	holdCatalog(t, h)
	addedRecipe(br)
	release := make(chan struct{})
	br.blockOn = map[string]chan struct{}{methodKiroWorkflowListRecipes: release}

	rec := getRecipes(mux)
	var body marotte.RecipesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK || len(body.Recipes) != 1 {
		t.Errorf("GET /api/recipes = %d %s, want the held catalog's one recipe", rec.Code, rec.Body.String())
	}
	close(release)
}

// A listing the engine did not answer shows the held catalog on the recipe list a person opens, whichever way it
// went unanswered.
func TestRecipesRoute_AnswersTheHeldCatalogWhenAListingGoesUnanswered(t *testing.T) {
	for _, lf := range unansweredListings {
		t.Run(lf.name, func(t *testing.T) {
			h, br, mux := runDefaultsHub(t)
			holdCatalog(t, h)
			br.callErrs = map[string]error{methodKiroWorkflowListRecipes: lf.fail}
			rec := getRecipes(mux)
			var body marotte.RecipesResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK || len(body.Recipes) != 1 {
				t.Errorf("GET /api/recipes = %d %s, want the held catalog's one recipe", rec.Code, rec.Body.String())
			}
		})
	}
}

// A launch rule save tells a source the catalog lacks (400) from a catalog it could not read (500).
func TestLaunchRuleSave_TellsAnUnknownSourceFromAnUnreadableCatalog(t *testing.T) {
	t.Run("unknown source", func(t *testing.T) {
		_, _, mux := runDefaultsHub(t)
		rec := postRoute(t, mux, "/api/launch-rules",
			`{"source":"/etc/passwd","when":{"clock":{"freq":"daily","hour":2,"minute":0}},"enabled":true}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d %s, want 400", rec.Code, rec.Body.String())
		}
	})
	t.Run("unreadable catalog", func(t *testing.T) {
		_, br, mux := runDefaultsHub(t)
		br.callErrs = map[string]error{methodKiroWorkflowListRecipes: errors.New("engine gone")}
		rec := postRoute(t, mux, "/api/launch-rules",
			`{"source":"bundled://publish","when":{"clock":{"freq":"daily","hour":2,"minute":0}},"enabled":true}`)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d %s, want 500", rec.Code, rec.Body.String())
		}
	})
}
