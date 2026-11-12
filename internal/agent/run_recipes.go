package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
)

// engineSlowRetryAfter is the Retry-After, in seconds, of a listing or a launch the engine did not answer in time.
const engineSlowRetryAfter = 10

// recipeListPatience is how long the recipe list waits on a fresh listing before it answers the held catalog: a
// healthy engine lists about forty recipes in a second. A var so a test can shorten it.
var recipeListPatience = 5 * time.Second

var (
	// errRecipeListSlow is a listing the engine did not answer, past its deadline or because it exited: retryable,
	// not a refusal.
	errRecipeListSlow = errors.New("the recipe list is still loading, try again in a few seconds")
	// errEngineSlow is a launch the engine did not answer in time before the run was created (unanswered).
	errEngineSlow          = errors.New("the agent engine is busy, try again in a few seconds")
	errUnknownRecipe       = errors.New("unknown recipe source")
	errMissingRecipeSource = errors.New("missing recipe source")
)

// recipeCatalog shares one listRecipes call among concurrent readers and holds its last answer for the recipe list a
// person opens while a listing is slow. The hold is a display hint only: KAS 2.37.1 reports a catalog change only
// through a session opened with workflows on, so a held name can be stale, and a decision (a launch, a schedule save,
// the one-live-run rule) reads live.
type recipeCatalog struct {
	flight *recipeListing
	held   []marotte.Recipe
	// gen counts invalidations: a listing that began before one never commits its answer, so a slow walk cannot
	// restore the catalog a change just dropped.
	gen uint64
	mu  sync.Mutex
}

// recipeListing is one listRecipes call in flight; done closes once recipes and err are set.
type recipeListing struct {
	done    chan struct{}
	err     error
	recipes []marotte.Recipe
}

// invalidate drops the held catalog and detaches the listing in flight, whose waiters still get its answer, so a
// reader after a reported change starts a listing of its own.
func (c *recipeCatalog) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	c.held, c.flight = nil, nil
}

// heldCatalog is the held catalog, nil when there is none.
func (c *recipeCatalog) heldCatalog() []marotte.Recipe {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.held
}

// live joins or starts the one listing, never answering the hold. list runs on lt's context, never on a caller's,
// so a caller walking away does not fail the others; each caller waits only as long as its ctx.
func (c *recipeCatalog) live(
	ctx context.Context, lt *lifetime, list func(context.Context) ([]marotte.Recipe, error),
) ([]marotte.Recipe, error) {
	c.mu.Lock()
	f, err := c.joinLocked(lt, list)
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return f.wait(ctx)
}

// display is live for the recipe list a person opens, answering the hold, when there is one, once the listing has
// run patience long or ran out of time.
func (c *recipeCatalog) display(
	ctx context.Context, lt *lifetime, list func(context.Context) ([]marotte.Recipe, error), patience time.Duration,
) ([]marotte.Recipe, error) {
	c.mu.Lock()
	held := c.held
	f, err := c.joinLocked(lt, list)
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if held == nil {
		return f.wait(ctx)
	}
	t := time.NewTimer(patience)
	defer t.Stop()
	select {
	case <-f.done:
		if errors.Is(f.err, errRecipeListSlow) {
			return held, nil
		}
		return f.recipes, f.err
	case <-t.C:
		return held, nil
	case <-ctx.Done():
		return nil, classifyRecipeListErr(ctx.Err())
	}
}

// joinLocked returns the listing in flight, starting one when there is none. c.mu is held.
func (c *recipeCatalog) joinLocked(
	lt *lifetime, list func(context.Context) ([]marotte.Recipe, error),
) (*recipeListing, error) {
	if c.flight != nil {
		return c.flight, nil
	}
	f := &recipeListing{done: make(chan struct{})}
	gen := c.gen
	if !lt.goUnlessDraining(func() { c.run(lt.shutdownCtx, f, gen, list) }) {
		return nil, errRuntimeDraining
	}
	c.flight = f
	return f, nil
}

func (f *recipeListing) wait(ctx context.Context) ([]marotte.Recipe, error) {
	select {
	case <-f.done:
		return f.recipes, f.err
	case <-ctx.Done():
		return nil, classifyRecipeListErr(ctx.Err())
	}
}

func (c *recipeCatalog) run(
	bg context.Context, f *recipeListing, gen uint64, list func(context.Context) ([]marotte.Recipe, error),
) {
	cctx, cancel := context.WithTimeout(bg, sessionListTimeout)
	defer cancel()
	recipes, err := list(cctx)
	if recipes == nil && err == nil {
		recipes = []marotte.Recipe{}
	}
	f.recipes, f.err = recipes, classifyRecipeListErr(err)
	c.mu.Lock()
	if c.flight == f {
		c.flight = nil
	}
	if err == nil && c.gen == gen {
		c.held = recipes
	}
	c.mu.Unlock()
	close(f.done)
}

func classifyRecipeListErr(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, marotte.ErrBridgeExited) {
		return fmt.Errorf("%w: %w", errRecipeListSlow, err)
	}
	return err
}

// writeEngineSlow answers a listing or a launch the engine did not answer in time with 503 and a Retry-After,
// reporting whether err was one.
func writeEngineSlow(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, errRecipeListSlow):
		writeTryLater(w, errRecipeListSlow.Error(), recipeListSlowCode)
	case errors.Is(err, errEngineSlow):
		writeTryLater(w, errEngineSlow.Error(), engineSlowCode)
	default:
		return false
	}
	return true
}

func writeTryLater(w http.ResponseWriter, reason, code string) {
	w.Header().Set("Retry-After", strconv.Itoa(engineSlowRetryAfter))
	httpserver.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSONWithCode(reason, code))
}

// The 503s' machine reasons, so a client branches on them rather than on the sentence.
const (
	recipeListSlowCode      = "recipe_list_slow"
	engineSlowCode          = "engine_slow"
	modelListUnreadableCode = "model_list_unreadable"
)
