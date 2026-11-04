package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/webhttp/v4"
)

// engineSlowRetryAfter is the Retry-After, in seconds, of a listing or a launch the engine did not answer in time.
const engineSlowRetryAfter = 10

// recipeListPatience is how long the recipe list waits on a fresh listing before it answers the held catalog: a
// healthy engine lists about forty recipes in a second. A var so a test can shorten it.
var recipeListPatience = 5 * time.Second

var (
	// errRecipeListSlow is a listing that ran past its deadline: the engine is busy, not refusing.
	errRecipeListSlow = errors.New("the recipe list is still loading, try again in a few seconds")
	// errEngineSlow is a launch the engine did not answer in time before the run was created (launchOrigin.unanswered).
	errEngineSlow          = errors.New("the agent engine is busy, try again in a few seconds")
	errUnknownRecipe       = errors.New("unknown recipe source")
	errMissingRecipeSource = errors.New("missing recipe source")
)

// recipeCatalog holds the last listRecipes answer until KAS reports a catalog change or the engine ends, so a launch
// or a schedule save reads it instead of walking every recipe file. Concurrent readers share one listing. KAS 2.37.1
// reports a change only through a session opened with workflows on, so a hold can lack a recipe added while none was
// live: a source missing from it is looked up afresh (fresh), and the recipe list a person opens lists afresh.
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

// invalidate drops the held catalog and detaches the listing in flight, whose waiters still get its answer.
func (c *recipeCatalog) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	c.held, c.flight = nil, nil
}

// get answers the held catalog, or joins or starts the one listing; held reports which. list runs on lt's context,
// never on a caller's, so a caller walking away does not fail the others; each caller waits only as long as its ctx.
func (c *recipeCatalog) get(
	ctx context.Context, lt *lifetime, list func(context.Context) ([]marotte.Recipe, error),
) (recipes []marotte.Recipe, held bool, err error) {
	c.mu.Lock()
	if c.held != nil {
		recipes = c.held
		c.mu.Unlock()
		return recipes, true, nil
	}
	f, err := c.joinLocked(lt, list)
	c.mu.Unlock()
	if err != nil {
		return nil, false, err
	}
	recipes, err = f.wait(ctx)
	return recipes, false, err
}

// fresh joins or starts a listing even over a held catalog, whose answer then replaces the hold. A positive patience
// answers the hold, when there is one, once the listing has run that long or ran out of time; zero waits it out.
func (c *recipeCatalog) fresh(
	ctx context.Context, lt *lifetime, list func(context.Context) ([]marotte.Recipe, error), patience time.Duration,
) ([]marotte.Recipe, error) {
	c.mu.Lock()
	held := c.held
	f, err := c.joinLocked(lt, list)
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if held == nil || patience <= 0 {
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
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", errRecipeListSlow, err)
	}
	return err
}

// writeEngineSlow answers a listing or a launch the engine did not answer in time with 503 and a Retry-After,
// reporting whether err was one.
func writeEngineSlow(w http.ResponseWriter, err error) bool {
	var reason, code string
	switch {
	case errors.Is(err, errRecipeListSlow):
		reason, code = errRecipeListSlow.Error(), recipeListSlowCode
	case errors.Is(err, errEngineSlow):
		reason, code = errEngineSlow.Error(), engineSlowCode
	default:
		return false
	}
	w.Header().Set("Retry-After", strconv.Itoa(engineSlowRetryAfter))
	webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSONWithCode(reason, code))
	return true
}

// The 503s' machine reasons, so a client branches on them rather than on the sentence.
const (
	recipeListSlowCode = "recipe_list_slow"
	engineSlowCode     = "engine_slow"
)
