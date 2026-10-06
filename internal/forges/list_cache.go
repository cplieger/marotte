// A read-through cache over the forge listing routes (repositories, pull requests, affordances),
// first page only. No ticker: a fill happens only because a request asked, and the one goroutine is
// the revalidation of a stale value already served, bounded by listFillBudget.

package forges

import (
	"context"
	"log/slog"
	"maps"
	"strconv"
	"sync"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/keyenc"
	"golang.org/x/sync/semaphore"
	"golang.org/x/sync/singleflight"
)

// repoListTTL bounds how stale an account's repository listing may get. The set
// changes when someone clones, creates or archives a repository, which is rare
// against the rate this endpoint is read, and a forge connection change clears
// the cache outright (Manager.Invalidate) rather than waiting for the TTL.
const repoListTTL = 5 * time.Minute

// prListTTL is deliberately much shorter than repoListTTL, and CI is the reason
// rather than the PR set: a row carries the folded check verdict of its head
// commit, which moves far more often than the set of pull requests does.
const prListTTL = time.Minute

// affordancesTTL matches repoListTTL because both are read from the repository's
// own record. A strategy disabled inside the window costs a merge the forge
// refuses, never a merge of another kind, because the library sends the
// strategy the caller named.
const affordancesTTL = 5 * time.Minute

// maxListFills caps how many cache fills may read a forge at once, across every
// cache and every forge: a burst of cold requests is what upstream secondary rate
// limiting is for. 16 is a spike ceiling rather than a tuned value.
const maxListFills = 16

// listFillBudget bounds a fill detached from the request that started it: a
// revalidation, which no request waits on, or a shared fill whose first caller
// may leave while others still wait.
const listFillBudget = 60 * time.Second

// generation is what a fill started under: epoch moves when the whole cache is
// cleared, repo when the fill's scope is mutated. Zero is a live generation
// (ADR-0065), so it is never read as "not recorded".
type generation struct {
	epoch, repo uint64
}

// listEntry is one cached listing and the generation it was filled under.
type listEntry[T any] struct {
	// at is the zero time until the first successful fill, which is what
	// separates "nothing to serve" from "something stale to serve".
	at   time.Time
	val  T
	gen  generation
	busy bool // a revalidation is in flight; do not start a second
}

// listCache caches one KIND of listing, keyed by whatever identifies a single
// listing of that kind. The type parameter belongs on the type rather than on
// get: an instance holds entries of exactly one T, and a per-method parameter
// would let a []Repo and a []PR share one map.
type listCache[T any] struct {
	entries map[cacheKey]*listEntry[T]
	// gens is each mutated scope's generation, an absent one reading zero, and
	// epoch is bumped by clear(). A fill whose generation moved while it ran
	// still RETURNS its value to the caller who asked for it, but does not store
	// it: it may have read the forge before the change, and storing it would
	// strand that change until the TTL.
	gens  map[string]uint64
	sf    singleflight.Group
	fills *semaphore.Weighted
	ttl   time.Duration
	epoch uint64
	mu    sync.Mutex
}

func newListCache[T any](ttl time.Duration, fills *semaphore.Weighted) *listCache[T] {
	return &listCache[T]{
		entries: make(map[cacheKey]*listEntry[T]),
		gens:    make(map[string]uint64),
		fills:   fills,
		ttl:     ttl,
	}
}

func (c *listCache[T]) genLocked(scope string) generation {
	return generation{epoch: c.epoch, repo: c.gens[scope]}
}

// get serves key: force fills now and replaces (the UI's refresh), an empty
// entry fills now, a fresh one is answered with no forge call, and a stale one
// is answered at once and revalidated behind the caller, so a visit past the
// TTL paints without waiting on the forge.
func (c *listCache[T]) get(ctx context.Context, key cacheKey, force bool,
	fill func(context.Context) (T, error),
) (T, error) {
	if !force {
		if cached, ok := c.serve(ctx, key, fill); ok {
			return cached, nil
		}
	}
	return c.fillNow(ctx, key, fill)
}

// serve answers from the cache when it can, reporting whether it did. A stale
// hit also arms the revalidation, which is why this holds the lock across both
// decisions rather than reading state and acting on it afterwards.
func (c *listCache[T]) serve(ctx context.Context, key cacheKey,
	fill func(context.Context) (T, error),
) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || e.at.IsZero() {
		var zero T
		return zero, false
	}
	if time.Since(e.at) >= c.ttl && !e.busy {
		e.busy = true
		go c.revalidate(ctx, key, e.gen, fill)
	}
	return e.val, true
}

// fillNow fetches key and caches the result. Concurrent callers for one key
// share a single fetch, as two browser tabs opening one list together do. The
// generation is part of the shared fetch's key, so a read after a mutation never
// joins a fetch begun before it.
func (c *listCache[T]) fillNow(ctx context.Context, key cacheKey,
	fill func(context.Context) (T, error),
) (T, error) {
	c.mu.Lock()
	gen := c.genLocked(key.scope)
	c.mu.Unlock()

	flight := keyenc.Join(key.scope, key.page, strconv.FormatUint(gen.epoch, 10), strconv.FormatUint(gen.repo, 10))
	ch := c.sf.DoChan(flight, func() (any, error) {
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), listFillBudget)
		defer cancel()
		if err := c.fills.Acquire(fctx, 1); err != nil {
			return nil, err
		}
		defer c.fills.Release(1)
		val, err := fill(fctx)
		if err != nil {
			return nil, err
		}
		c.store(key, gen, val)
		return val, nil
	})

	var zero T
	select {
	case res := <-ch:
		if res.Err != nil {
			return zero, res.Err
		}
		val, _ := res.Val.(T)
		return val, nil
	case <-ctx.Done():
		// The shared fetch runs on until it settles, so a caller giving up does
		// not discard the work for whoever else is waiting on it.
		return zero, ctx.Err()
	}
}

// revalidate refreshes an entry whose stale value was already served, on a detached context bounded
// by its timeout, since the noticing request's ctx dies with its response. A failure is not
// reported; the next request retries.
func (c *listCache[T]) revalidate(ctx context.Context, key cacheKey, gen generation,
	fill func(context.Context) (T, error),
) {
	defer c.clearBusy(key)
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), listFillBudget)
	defer cancel()
	if err := c.fills.Acquire(bg, 1); err != nil {
		return
	}
	defer c.fills.Release(1)
	val, err := fill(bg)
	if err != nil {
		slog.Debug("forges: listing revalidation failed", "key", key, "error", err)
		return
	}
	c.store(key, gen, val)
}

// store records a successful fill, unless its generation moved while it was in
// flight (see listCache.gens).
func (c *listCache[T]) store(key cacheKey, gen generation, val T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.genLocked(key.scope) {
		return
	}
	e, ok := c.entries[key]
	if !ok {
		e = &listEntry[T]{}
		c.entries[key] = e
	}
	e.val = val
	e.gen = gen
	e.at = time.Now()
}

func (c *listCache[T]) clearBusy(key cacheKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		e.busy = false
	}
}

// clear drops every entry and invalidates the fills already in flight.
func (c *listCache[T]) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[cacheKey]*listEntry[T])
	clear(c.gens)
	c.epoch++
}

// evict drops scope's entries and moves its generation, so a fill in flight for
// it stores nothing; every other scope's entries and fills are untouched.
func (c *listCache[T]) evict(scope string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gens[scope]++
	maps.DeleteFunc(c.entries, func(k cacheKey, _ *listEntry[T]) bool { return k.scope == scope })
}

// listCaches is the Manager's listing caches. They share one fill semaphore
// because the thing being bounded is concurrent forge reads, which every
// listing makes.
type listCaches struct {
	repos       *listCache[RepoList]
	prs         *listCache[PRList]
	affordances *listCache[RepoAffordances]
}

func newListCaches() *listCaches {
	fills := semaphore.NewWeighted(maxListFills)
	return &listCaches{
		repos:       newListCache[RepoList](repoListTTL, fills),
		prs:         newListCache[PRList](prListTTL, fills),
		affordances: newListCache[RepoAffordances](affordancesTTL, fills),
	}
}

func (c *listCaches) clear() {
	c.repos.clear()
	c.prs.clear()
	c.affordances.clear()
}

// cacheKey is a first page's key: the scope a mutation evicts, and the page
// within it.
type cacheKey struct{ scope, page string }

// listKey is the key of one connection's first page of list under one state
// filter, scoped to one canonical repository id (empty for a list no repository
// addresses). keyenc rather than a separator: the id arrives in a URL.
func listKey(forgeID, repoID, list, state string) cacheKey {
	return cacheKey{scope: repoScope(forgeID, repoID), page: keyenc.Join(list, state)}
}

func repoScope(forgeID, repoID string) string { return keyenc.Join(forgeID, repoID) }

// evictRepo drops every cached entry of forgeID's repository repoID, its
// canonical id, after a mutation changed it.
func (m *Manager) evictRepo(forgeID, repoID string) {
	scope := repoScope(forgeID, repoID)
	m.lists.prs.evict(scope)
	m.lists.affordances.evict(scope)
}

// listRequest is one list call's options and the settings the library resolved
// them to.
type listRequest struct {
	opts []forgeapi.ListOption
	set  forgeapi.ListSettings
}

// resolveList checks opts through the library, which refuses an unknown state
// filter or a malformed cursor before any request.
func resolveList(opts ...forgeapi.ListOption) (listRequest, error) {
	set, err := forgeapi.ResolveList(opts...)
	return listRequest{opts: opts, set: set}, err
}

// repoPage answers one page of fc's repositories. Only a first page with no
// filter reads through the cache: a continuation is keyed by a caller's cursor,
// so caching it would grow without bound and could answer for the first page,
// and a named state must reach the library that refuses it.
func (m *Manager) repoPage(ctx context.Context, fc forgeClient, req *listRequest, force bool) (RepoList, error) {
	fill := func(ctx context.Context) (RepoList, error) {
		page, err := fc.core.ListRepos(ctx, req.opts...)
		if err != nil {
			return RepoList{}, err
		}
		return repoListWire(&page), nil
	}
	if req.set.After != "" || req.set.StateSet {
		return fill(ctx)
	}
	return m.lists.repos.get(ctx, listKey(fc.id, "", "repos", ""), force, fill)
}

// prPage answers one page of ref's pull requests, a first page through the
// cache under ref's canonical id and a continuation from the forge.
func (m *Manager) prPage(ctx context.Context, fc forgeClient, ref forgeapi.RepoRef, req *listRequest, force bool) (PRList, error) {
	fill := func(ctx context.Context) (PRList, error) {
		page, err := fc.core.ListPRs(ctx, ref, req.opts...)
		if err != nil {
			return PRList{}, err
		}
		return prListWire(&page), nil
	}
	if req.set.After != "" {
		return fill(ctx)
	}
	return m.lists.prs.get(ctx, listKey(fc.id, ref.ID, "prs", req.set.State.String()), force, fill)
}

// repoAffordances answers what ref allows, through the cache under ref's
// canonical id.
func (m *Manager) repoAffordances(ctx context.Context, fc forgeClient, ref forgeapi.RepoRef, force bool) (RepoAffordances, error) {
	key := cacheKey{scope: repoScope(fc.id, ref.ID), page: "affordances"}
	return m.lists.affordances.get(ctx, key, force, func(ctx context.Context) (RepoAffordances, error) {
		aff, err := fc.core.RepoAffordances(ctx, ref)
		if err != nil {
			return RepoAffordances{}, err
		}
		return affordancesOf(&aff), nil
	})
}
