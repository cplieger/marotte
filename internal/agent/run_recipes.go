package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/webhttp/v4"
)

// recipeListRetryAfter is the Retry-After, in seconds, of a listing that ran out of time: the catalog walk it
// waited on is still warming KAS's admission cache, so a retry a few seconds later usually lands on a held answer.
const recipeListRetryAfter = 10

var (
	// errRecipeListSlow is a listing that ran past its deadline: the engine is busy, not refusing.
	errRecipeListSlow      = errors.New("the recipe list is still loading, try again in a few seconds")
	errUnknownRecipe       = errors.New("unknown recipe source")
	errMissingRecipeSource = errors.New("missing recipe source")
)

// recipeCatalog holds the last listRecipes answer until KAS reports a catalog change or the engine ends, so a launch
// or a schedule save reads it instead of walking every recipe file. Concurrent readers share one listing.
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

// get answers the held catalog, or joins or starts the one listing. list runs on lt's context, never on a caller's,
// so a caller walking away does not fail the others; each caller still waits only as long as its own ctx allows.
func (c *recipeCatalog) get(
	ctx context.Context, lt *lifetime, list func(context.Context) ([]marotte.Recipe, error),
) ([]marotte.Recipe, error) {
	c.mu.Lock()
	if c.held != nil {
		held := c.held
		c.mu.Unlock()
		return held, nil
	}
	f := c.flight
	if f == nil {
		f = &recipeListing{done: make(chan struct{})}
		gen := c.gen
		if !lt.goUnlessDraining(func() { c.run(lt.shutdownCtx, f, gen, list) }) {
			c.mu.Unlock()
			return nil, errRuntimeDraining
		}
		c.flight = f
	}
	c.mu.Unlock()
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

// writeRecipeListSlow answers a listing that ran out of time with 503 and a Retry-After, reporting whether err was one.
func writeRecipeListSlow(w http.ResponseWriter, err error) bool {
	if !errors.Is(err, errRecipeListSlow) {
		return false
	}
	w.Header().Set("Retry-After", strconv.Itoa(recipeListRetryAfter))
	webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable,
		httpreply.ErrorJSONWithCode(errRecipeListSlow.Error(), recipeListSlowCode))
	return true
}

// recipeListSlowCode is the 503's machine reason, so a client branches on it rather than on the sentence.
const recipeListSlowCode = "recipe_list_slow"
