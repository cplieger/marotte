package forges

// The listing cache exists so arriving at a view costs no forge request when
// the answer is already known, and every case here is about a CALL COUNT rather
// than a returned value: the value was never in doubt, the work was.
//
// The time-dependent cases run in a synctest BUBBLE. A TTL boundary is otherwise
// only reachable by sleeping past it, which trades a real second per case for an
// assertion that can still flake; in a bubble the clock advances only when every
// goroutine is durably blocked, so "one nanosecond past the TTL" is exact and the
// revalidation goroutine is joinable without polling for it.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/forgeapi"
	"golang.org/x/sync/semaphore"
)

// countingFill records how often the cache reached through it, and answers a
// different value each time, so a test can tell a cache hit from a refill.
type countingFill struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *countingFill) fill(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return []string{"v", string(rune('0' + f.calls))}, nil
}

func (f *countingFill) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func newTestCache(ttl time.Duration) *listCache[[]string] {
	return newListCache[[]string](ttl, semaphore.NewWeighted(maxListFills))
}

// testForge is the connection the cache-key cases address.
const testForge = "github:github.com"

// testKey is page of one repository, the scope every single-scope case shares.
func testKey(page string) cacheKey {
	return listKey(testForge, "v1.6f2f72", page, "")
}

func TestListCache_ColdFillThenServesFromCache(t *testing.T) {
	c := newTestCache(time.Hour)
	f := &countingFill{}

	first, err := c.get(t.Context(), testKey("k"), false, f.fill)
	if err != nil {
		t.Fatalf("get(cold) error = %v, want nil", err)
	}
	if len(first) != 2 {
		t.Fatalf("get(cold) = %v, want a 2-element listing", first)
	}
	if got := f.count(); got != 1 {
		t.Errorf("fill calls after cold get = %d, want 1", got)
	}

	// The whole point: a second read inside the TTL touches no forge.
	second, err := c.get(t.Context(), testKey("k"), false, f.fill)
	if err != nil {
		t.Fatalf("get(fresh) error = %v, want nil", err)
	}
	if got := f.count(); got != 1 {
		t.Errorf("fill calls after fresh get = %d, want 1 (served from cache)", got)
	}
	if first[1] != second[1] {
		t.Errorf("get(fresh) = %v, want the cached %v", second, first)
	}
}

func TestListCache_KeysAreIndependent(t *testing.T) {
	c := newTestCache(time.Hour)
	f := &countingFill{}

	for _, key := range []cacheKey{testKey("a"), testKey("b")} {
		if _, err := c.get(t.Context(), key, false, f.fill); err != nil {
			t.Fatalf("get(%+v) error = %v, want nil", key, err)
		}
	}
	if got := f.count(); got != 2 {
		t.Errorf("fill calls for two keys = %d, want 2", got)
	}
}

func TestListCache_ForceBypassesAFreshEntry(t *testing.T) {
	c := newTestCache(time.Hour)
	f := &countingFill{}

	if _, err := c.get(t.Context(), testKey("k"), false, f.fill); err != nil {
		t.Fatalf("get(cold) error = %v, want nil", err)
	}
	// A refresh control asks for the truth, so a fresh entry is no reason to
	// answer from it.
	got, err := c.get(t.Context(), testKey("k"), true, f.fill)
	if err != nil {
		t.Fatalf("get(force) error = %v, want nil", err)
	}
	if calls := f.count(); calls != 2 {
		t.Errorf("fill calls after forced get = %d, want 2", calls)
	}
	if got[1] != "2" {
		t.Errorf("get(force) = %v, want the second fill's value", got)
	}
}

func TestListCache_StaleEntryServesAtOnceThenRevalidates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const ttl = time.Minute
		c := newTestCache(ttl)
		// The revalidation must be observable as an EVENT rather than as a count
		// read after a sleep, or the test cannot tell "refreshed" from "not yet".
		refreshed := make(chan struct{}, 1)
		f := &countingFill{}
		fill := func(ctx context.Context) ([]string, error) {
			v, err := f.fill(ctx)
			select {
			case refreshed <- struct{}{}:
			default:
			}
			return v, err
		}

		if _, err := c.get(t.Context(), testKey("k"), false, fill); err != nil {
			t.Fatalf("get(cold) error = %v, want nil", err)
		}
		<-refreshed
		synctest.Sleep(ttl + time.Nanosecond)

		// Served from the stale copy: the caller gets the FIRST fill's value even
		// though a second one is on its way.
		got, err := c.get(t.Context(), testKey("k"), false, fill)
		if err != nil {
			t.Fatalf("get(stale) error = %v, want nil", err)
		}
		if got[1] != "1" {
			t.Errorf("get(stale) = %v, want the cached first value (served, not awaited)", got)
		}

		<-refreshed
		synctest.Wait()
		if calls := f.count(); calls != 2 {
			t.Fatalf("fill calls after stale get = %d, want 2 (one cold, one revalidation)", calls)
		}
		// And the revalidation landed, so the NEXT read is the fresh value with no
		// further work.
		next, err := c.get(t.Context(), testKey("k"), false, fill)
		if err != nil {
			t.Fatalf("get(after revalidation) error = %v, want nil", err)
		}
		if next[1] != "2" {
			t.Errorf("get(after revalidation) = %v, want the revalidated value", next)
		}
		if calls := f.count(); calls != 2 {
			t.Errorf("fill calls after the revalidated read = %d, want 2", calls)
		}
	})
}

func TestListCache_OneRevalidationAtATime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const ttl = time.Minute
		c := newTestCache(ttl)
		release := make(chan struct{})
		var inFlight atomic.Int64
		f := &countingFill{}
		fill := func(ctx context.Context) ([]string, error) {
			inFlight.Add(1)
			<-release
			return f.fill(ctx)
		}

		go func() { close(release) }()
		if _, err := c.get(t.Context(), testKey("k"), false, fill); err != nil {
			t.Fatalf("get(cold) error = %v, want nil", err)
		}
		synctest.Sleep(ttl + time.Nanosecond)

		// A blocked revalidation must not be joined by every later reader: a stale
		// entry read ten times in a row is one refresh, not ten.
		release = make(chan struct{})
		inFlight.Store(0)
		for range 10 {
			if _, err := c.get(t.Context(), testKey("k"), false, fill); err != nil {
				t.Fatalf("get(stale) error = %v, want nil", err)
			}
		}
		synctest.Wait()
		if got := inFlight.Load(); got != 1 {
			t.Errorf("revalidations in flight after 10 stale reads = %d, want 1", got)
		}
		close(release)
		synctest.Wait()
	})
}

// A caller leaving does not fail the others waiting on its shared fill.
func TestListCache_ASharedFillSurvivesTheFirstCallerLeaving(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newTestCache(time.Hour)
		release := make(chan struct{})
		fill := func(ctx context.Context) ([]string, error) {
			select {
			case <-release:
				return []string{"v"}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		first, cancel := context.WithCancel(t.Context())
		var wg sync.WaitGroup
		wg.Go(func() { _, _ = c.get(first, testKey("k"), false, fill) })
		synctest.Wait()
		var got []string
		var err error
		wg.Go(func() { got, err = c.get(t.Context(), testKey("k"), false, fill) })
		synctest.Wait()
		cancel()
		synctest.Wait()
		close(release)
		wg.Wait()

		if err != nil || len(got) != 1 {
			t.Errorf("get() of a caller still waiting after the first left = %v, %v; want the fill's value", got, err)
		}
	})
}

func TestListCache_ConcurrentColdReadsShareOneFill(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newTestCache(time.Hour)
		release := make(chan struct{})
		f := &countingFill{}
		fill := func(ctx context.Context) ([]string, error) {
			<-release
			return f.fill(ctx)
		}

		const readers = 8
		var wg sync.WaitGroup
		for range readers {
			wg.Go(func() {
				if _, err := c.get(t.Context(), testKey("k"), false, fill); err != nil {
					t.Errorf("get(concurrent cold) error = %v, want nil", err)
				}
			})
		}
		// Every reader is parked on the shared fetch before it is allowed to run,
		// which is what makes the count below a statement about sharing rather
		// than about scheduling luck.
		synctest.Wait()
		close(release)
		wg.Wait()

		if got := f.count(); got != 1 {
			t.Errorf("fill calls for %d concurrent cold reads = %d, want 1", readers, got)
		}
	})
}

func TestListCache_FillErrorIsReturnedAndNotCached(t *testing.T) {
	c := newTestCache(time.Hour)
	wantErr := errors.New("gh: exit status 1")
	f := &countingFill{err: wantErr}

	if _, err := c.get(t.Context(), testKey("k"), false, f.fill); !errors.Is(err, wantErr) {
		t.Fatalf("get(failing) error = %v, want %v", err, wantErr)
	}
	// A failure must not become a cached empty listing, or one bad round trip
	// renders "no pull requests" until the TTL expires.
	f.err = nil
	got, err := c.get(t.Context(), testKey("k"), false, f.fill)
	if err != nil {
		t.Fatalf("get(after failure) error = %v, want nil", err)
	}
	if len(got) == 0 {
		t.Errorf("get(after failure) = %v, want the retried listing", got)
	}
	if calls := f.count(); calls != 2 {
		t.Errorf("fill calls across a failure and a retry = %d, want 2", calls)
	}
}

func TestListCache_CancelledReadDoesNotBlock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newTestCache(time.Hour)
		f := &countingFill{}
		fill := func(ctx context.Context) ([]string, error) {
			<-ctx.Done()
			return f.fill(ctx)
		}

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			_, err := c.get(ctx, testKey("k"), false, fill)
			done <- err
		}()
		synctest.Wait()
		cancel()

		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("get(cancelled) error = %v, want context.Canceled", err)
		}
		// The fill runs on past the caller until its own budget ends it.
		time.Sleep(listFillBudget)
		synctest.Wait()
		if calls := f.count(); calls != 1 {
			t.Errorf("fills ended by the budget after the caller left = %d, want 1", calls)
		}
	})
}

func TestListCache_ClearDropsEntriesAndInFlightFills(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newTestCache(time.Hour)
		release := make(chan struct{})
		f := &countingFill{}
		fill := func(ctx context.Context) ([]string, error) {
			<-release
			return f.fill(ctx)
		}

		done := make(chan struct{})
		go func() {
			defer close(done)
			if _, err := c.get(t.Context(), testKey("k"), false, fill); err != nil {
				t.Errorf("get(cold) error = %v, want nil", err)
			}
		}()
		synctest.Wait()

		// A sign-in landing here is the real case: the fill already read the forge
		// as the PREVIOUS account, so its answer must reach the caller who asked
		// and go no further.
		c.clear()
		close(release)
		<-done

		if _, ok := c.entries[testKey("k")]; ok {
			t.Error("entry cached by a fill that started before clear(); want it dropped")
		}
		if _, err := c.get(t.Context(), testKey("k"), false, f.fill); err != nil {
			t.Fatalf("get(after clear) error = %v, want nil", err)
		}
		if calls := f.count(); calls != 2 {
			t.Errorf("fill calls after clear = %d, want 2 (the dropped one, then a real refill)", calls)
		}
	})
}

func TestListCache_FillsAreBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const limit = 3
		c := newListCache[[]string](time.Hour, semaphore.NewWeighted(limit))
		release := make(chan struct{})
		var live, peak atomic.Int64
		fill := func(context.Context) ([]string, error) {
			n := live.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			<-release
			live.Add(-1)
			return []string{"v"}, nil
		}

		var wg sync.WaitGroup
		for i := range limit * 4 {
			key := testKey(string(rune('a' + i)))
			wg.Go(func() {
				if _, err := c.get(t.Context(), key, false, fill); err != nil {
					t.Errorf("get(%+v) error = %v, want nil", key, err)
				}
			})
		}
		synctest.Wait()
		if got := peak.Load(); got > limit {
			t.Errorf("concurrent fills = %d, want at most %d", got, limit)
		}
		close(release)
		wg.Wait()
	})
}

// heldFill blocks the first call on release, after it took fill 1, and answers
// every later one at once, so a case can hold one fill in flight while others
// complete.
type heldFill struct {
	release chan struct{}
	countingFill
	once sync.Once
}

func newHeldFill() *heldFill { return &heldFill{release: make(chan struct{})} }

func (f *heldFill) fill(ctx context.Context) ([]string, error) {
	v, err := f.countingFill.fill(ctx)
	first := false
	f.once.Do(func() { first = true })
	if first {
		<-f.release
	}
	return v, err
}

// getAsync starts a read of key and answers the channel its answer arrives on.
func getAsync(t *testing.T, c *listCache[[]string], key cacheKey, fill func(context.Context) ([]string, error)) <-chan []string {
	t.Helper()
	out := make(chan []string, 1)
	go func() {
		v, err := c.get(t.Context(), key, false, fill)
		if err != nil {
			t.Errorf("get(%+v) error = %v, want nil", key, err)
		}
		out <- v
	}()
	return out
}

func TestListCache_MutationEvictsItsRepo(t *testing.T) {
	c := newTestCache(time.Hour)
	f := &countingFill{}
	const repo = "v1.6f2f72"
	evicted := []cacheKey{listKey(testForge, repo, "prs", "open"), listKey(testForge, repo, "prs", "closed")}
	kept := []cacheKey{
		listKey(testForge, "v1.6f2f73", "prs", "open"),
		listKey("gitlab:gitlab.com", repo, "prs", "open"),
		listKey(testForge, "", "repos", ""),
	}
	all := slices.Concat(evicted, kept)
	before := make(map[cacheKey]string, len(all))
	for _, key := range all {
		v, err := c.get(t.Context(), key, false, f.fill)
		if err != nil {
			t.Fatalf("Setup: get(%+v) error = %v", key, err)
		}
		before[key] = v[1]
	}

	c.evict(repoScope(testForge, repo))

	for _, key := range all {
		v, err := c.get(t.Context(), key, false, f.fill)
		if err != nil {
			t.Fatalf("get(%+v) after the mutation error = %v, want nil", key, err)
		}
		if wantRefill := slices.Contains(evicted, key); (v[1] != before[key]) != wantRefill {
			t.Errorf("get(%+v) after a mutation of %s on %s = fill %s (was %s), want refilled %v",
				key, repo, testForge, v[1], before[key], wantRefill)
		}
	}
}

func TestListCache_MutationOnOneRepoKeepsAnotherRepoFill(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newTestCache(time.Hour)
		f := newHeldFill()
		inFlight, cached := listKey(testForge, "v1.62", "prs", "open"), listKey(testForge, "v1.62", "prs", "closed")
		got := getAsync(t, c, inFlight, f.fill)
		synctest.Wait()
		if _, err := c.get(t.Context(), cached, false, f.fill); err != nil {
			t.Fatalf("Setup: get(%+v) error = %v", cached, err)
		}

		c.evict(repoScope(testForge, "v1.61"))
		close(f.release)
		<-got

		for _, key := range []cacheKey{inFlight, cached} {
			if _, err := c.get(t.Context(), key, false, f.fill); err != nil {
				t.Fatalf("get(%+v) error = %v, want nil", key, err)
			}
		}
		if calls := f.count(); calls != 2 {
			t.Errorf("fill calls after a mutation of another repository = %d, want 2: "+
				"this repository's in-flight fill and its cached entry are kept", calls)
		}
	})
}

func TestListCache_StaleFillAfterMutationDoesNotPublish(t *testing.T) {
	scope := repoScope(testForge, "v1.6f2f72")
	key := listKey(testForge, "v1.6f2f72", "prs", "open")

	t.Run("a fill in flight answers its caller and stores nothing", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			c := newTestCache(time.Hour)
			c.evict(scope)
			f := newHeldFill()
			got := getAsync(t, c, key, f.fill)
			synctest.Wait()

			c.evict(scope)
			close(f.release)
			if v := <-got; v[1] != "1" {
				t.Errorf("the caller of the fill in flight got %v, want its own answer", v)
			}
			if v, err := c.get(t.Context(), key, false, f.fill); err != nil || v[1] != "2" || f.count() != 2 {
				t.Errorf("get after the mutation = %v, %v after %d fills, want a refill (fill 2)", v, err, f.count())
			}
		})
	})

	t.Run("a revalidation in flight stores nothing", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			const ttl = time.Minute
			c := newTestCache(ttl)
			f := &countingFill{}
			if _, err := c.get(t.Context(), key, false, f.fill); err != nil {
				t.Fatalf("Setup: get(cold) error = %v", err)
			}
			synctest.Sleep(ttl + time.Nanosecond)
			release := make(chan struct{})
			held := func(ctx context.Context) ([]string, error) {
				<-release
				return f.fill(ctx)
			}
			if v, err := c.get(t.Context(), key, false, held); err != nil || v[1] != "1" {
				t.Fatalf("Setup: get(stale) = %v, %v, want the cached first value", v, err)
			}
			synctest.Wait()

			c.evict(scope)
			close(release)
			synctest.Wait()
			if v, err := c.get(t.Context(), key, false, f.fill); err != nil || v[1] != "3" {
				t.Errorf("get after a mutation during the revalidation = %v, %v, want a refill (fill 3), "+
					"not the revalidation that read the forge before the mutation", v, err)
			}
		})
	})

	t.Run("a read after the mutation joins no fill that began before it", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			c := newTestCache(time.Hour)
			f := newHeldFill()
			before := getAsync(t, c, key, f.fill)
			synctest.Wait()

			c.evict(scope)
			after := getAsync(t, c, key, f.fill)
			synctest.Wait()
			select {
			case v := <-after:
				if v[1] != "2" {
					t.Errorf("a read after the mutation got %v, want its own fill (fill 2)", v)
				}
			default:
				t.Error("a read after the mutation is waiting on the fill that began before it")
			}
			close(f.release)
			<-before
			synctest.Wait()
		})
	})
}

// ADR-0065: zero is a live generation, so a record filled before its
// repository's first mutation is served, and a fill in flight at zero is fenced
// by that mutation like any later one.
func TestListCache_GenerationZeroIsALiveGeneration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newTestCache(time.Hour)
		served := listKey(testForge, "v1.61", "prs", "open")
		fenced := listKey(testForge, "v1.62", "prs", "open")
		f := &countingFill{}
		for range 2 {
			if _, err := c.get(t.Context(), served, false, f.fill); err != nil {
				t.Fatalf("get(%+v) error = %v", served, err)
			}
		}
		if calls := f.count(); calls != 1 {
			t.Errorf("two reads of a repository never mutated made %d fills, want 1: a generation-zero record is served", calls)
		}

		held := newHeldFill()
		got := getAsync(t, c, fenced, held.fill)
		synctest.Wait()
		c.evict(repoScope(testForge, "v1.62"))
		close(held.release)
		<-got
		if _, err := c.get(t.Context(), fenced, false, held.fill); err != nil || held.count() != 2 {
			t.Errorf("get after the first mutation made %d fills (%v), want 2: the fill begun at generation zero stored nothing",
				held.count(), err)
		}
	})
}

func TestListCache_ARecordFilledAfterAMutationRevalidates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const ttl = time.Minute
		c := newTestCache(ttl)
		key := testKey("k")
		c.clear()
		c.evict(key.scope)
		f := &countingFill{}
		if _, err := c.get(t.Context(), key, false, f.fill); err != nil {
			t.Fatalf("Setup: get(cold) error = %v", err)
		}
		synctest.Sleep(ttl + time.Nanosecond)
		if _, err := c.get(t.Context(), key, false, f.fill); err != nil {
			t.Fatalf("get(stale) error = %v, want nil", err)
		}
		synctest.Wait()

		if v, err := c.get(t.Context(), key, false, f.fill); err != nil || v[1] != "2" || f.count() != 2 {
			t.Errorf("get after the revalidation = %v, %v after %d fills, want the revalidated fill 2: "+
				"a record filled after a clear and a mutation revalidates under the generation it was filled under",
				v, err, f.count())
		}
	})
}

// A mutation through an id that is not the canonical encoding is refused, so it
// neither reaches the library nor evicts the canonical entry (ADR-0100).
func TestListCache_MutationThroughANonCanonicalIDIsRefused(t *testing.T) {
	for name, seg := range map[string]string{"upper-case hex": "v1.6F2F72", "a mixed-case selector": "v1.4f2f52"} {
		t.Run(name, func(t *testing.T) {
			row := githubMergeRow()
			core := &mutationCore{pr: answeredPR(row, 3, forgeapi.PRStateClosed)}
			mux := mergeMux(t, row, core)
			prs := row.repoPath("prs")
			if rec := getRoute(t, mux, prs); rec.Code != http.StatusOK {
				t.Fatalf("Setup: GET %s = %d %s", prs, rec.Code, rec.Body)
			}

			path := "/api/forges/" + url.PathEscape(row.rec.ID) + "/repos/" + seg + "/prs/3/close"
			rec := sendRoute(t, mux, http.MethodPost, path, "")
			if rec.Code != http.StatusBadRequest || decodeBody(t, rec)["code"] != forgeapi.CodeRepoRefInvalid {
				t.Errorf("POST %s = %d %s, want 400 %s", path, rec.Code, rec.Body, forgeapi.CodeRepoRefInvalid)
			}
			if rec := getRoute(t, mux, prs); rec.Code != http.StatusOK || core.listed() != 1 {
				t.Errorf("GET %s after a refused close through %s = %d after %d lists, want 200 after 1: "+
					"a refused mutation evicts nothing", prs, seg, rec.Code, core.listed())
			}
		})
	}
}

func TestListCache_RerunBumpsNothing(t *testing.T) {
	row := githubMergeRow()
	core := &mutationCore{}
	mux := mergeMux(t, row, core)
	prs := row.repoPath("prs")
	if rec := getRoute(t, mux, prs); rec.Code != http.StatusOK {
		t.Fatalf("Setup: GET %s = %d %s", prs, rec.Code, rec.Body)
	}

	rerun := row.repoPath("prs/3/rerun?head_sha=" + mergeHead)
	if rec := sendRoute(t, mux, http.MethodPost, rerun, ""); rec.Code != http.StatusOK {
		t.Fatalf("POST %s = %d %s, want 200", rerun, rec.Code, rec.Body)
	}
	if rec := getRoute(t, mux, prs); rec.Code != http.StatusOK || core.listed() != 1 {
		t.Errorf("GET %s after a re-run = %d after %d lists, want 200 after 1: a re-run changes no forge object",
			prs, rec.Code, core.listed())
	}
}

func TestManager_InvalidateDropsCachedListings(t *testing.T) {
	core := &pagedCore{
		prPages:   onePRPage(1),
		repoPages: map[forgeapi.Cursor]forgeapi.Page[forgeapi.Repository]{"": {}},
	}
	m := recordManager(t, core)
	mux := repoRoutes(m)
	paths := []string{
		"/api/forges/github%3Agithub.com/repos", githubRepoPath("v1.6f2f72", "prs"), githubRepoPath("v1.6f2f72", "affordances"),
	}
	read := func() {
		for _, path := range paths {
			if rec := getRoute(t, mux, path); rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d %s, want 200", path, rec.Code, rec.Body)
			}
		}
	}

	read()
	// A sign-in or a disconnect decides which repositories are visible at all,
	// and what the credential may do in each, so the previous account's
	// listings and affordances must not survive it.
	m.Invalidate()
	read()

	if asked, _, _ := core.calls(); len(asked) != 2*len(paths) {
		t.Errorf("the %d reads reached the forge %d times across an Invalidate, want %d", len(paths), len(asked), 2*len(paths))
	}
}

func TestListCache_ContinuationNeverAliasesTheFirstPage(t *testing.T) {
	pages := func() *pagedCore {
		return &pagedCore{prPages: map[forgeapi.Cursor]forgeapi.Page[forgeapi.PullRequest]{
			"":   {Items: onePRPage(1)[""].Items, Next: "c2"},
			"c2": {Items: onePRPage(2)[""].Items},
		}}
	}
	first, next := githubRepoPath("v1.6f2f72", "prs"), githubRepoPath("v1.6f2f72", "prs?after=c2")
	type step struct {
		path string
		rows int
		// calls is how many list calls reached the library once the step ran.
		calls int
	}
	cases := map[string][]step{
		"first page then continuation": {
			{path: first, rows: 1, calls: 1},
			{path: next, rows: 2, calls: 2},
			{path: first, rows: 1, calls: 2},
		},
		"continuation then first page": {
			{path: next, rows: 2, calls: 1},
			{path: first, rows: 1, calls: 2},
		},
		"refreshed continuation after the first page": {
			{path: first, rows: 1, calls: 1},
			{path: next + "&refresh=1", rows: 2, calls: 2},
			{path: first, rows: 1, calls: 2},
		},
		"refreshed continuation then first page": {
			{path: next + "&refresh=1", rows: 2, calls: 1},
			{path: first, rows: 1, calls: 2},
		},
		"refreshed first page after a continuation": {
			{path: first, rows: 1, calls: 1},
			{path: next, rows: 2, calls: 2},
			{path: first + "?refresh=1", rows: 1, calls: 3},
		},
	}
	for name, steps := range cases {
		t.Run(name, func(t *testing.T) {
			core := pages()
			mux := repoRoutes(recordManager(t, core))
			for i, s := range steps {
				got := prNumbers(t, getRoute(t, mux, s.path))
				asked, cursors, _ := core.calls()
				if !slices.Equal(got, []int{s.rows}) || len(asked) != s.calls {
					t.Errorf("step %d GET %s: rows %v after %d library calls (cursors %q), want [%d] after %d",
						i, s.path, got, len(asked), cursors, s.rows, s.calls)
				}
			}
		})
	}
}

// TestListCacheTTLs pins the two windows, with the values written out rather
// than compared to each other. A self-referential assertion cannot see a
// constant collapse — `time.Minute` mutated to `0` satisfies
// `prListTTL < repoListTTL` just as well as the real value does — and these two
// numbers are the whole of the staleness argument the cache rests on.
func TestListCacheTTLs(t *testing.T) {
	// A repository set moves when someone clones, creates or archives one, and a
	// connection change clears the cache outright rather than waiting for this.
	if repoListTTL != 5*time.Minute {
		t.Errorf("repoListTTL = %v, want 5m", repoListTTL)
	}
	// The PR window is short because of CI rather than the PR set: a row carries
	// the folded check verdict of its head commit, so a stale entry can show a
	// green chip for a commit whose checks have since gone red. Widening this
	// widens exactly that window; the merge's head pin only stops the wrong
	// COMMIT being merged, not a merge against a changed verdict.
	if prListTTL != time.Minute {
		t.Errorf("prListTTL = %v, want 1m", prListTTL)
	}
	// Affordances come from the same repository record as the repository list's
	// rows, so the two windows agree.
	if affordancesTTL != 5*time.Minute {
		t.Errorf("affordancesTTL = %v, want 5m", affordancesTTL)
	}
	// The bound is a spike ceiling: a cold PRs tab asks for one listing per
	// cloned repository at once. It has to stay well above 1, or a cold visit
	// serialises and the client abandons the fan-out after 20s.
	if maxListFills != 16 {
		t.Errorf("maxListFills = %d, want 16", maxListFills)
	}
}
