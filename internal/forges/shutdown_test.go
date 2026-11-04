package forges

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/forgeapi"
)

// stalledFill is a fill that answers only after its context ends AND release
// closes, recording how its context ended, so a case can hold it in flight
// past a cancel and see whether a close waited for it.
type stalledFill struct {
	release chan struct{}
	ended   error
	calls   int
	mu      sync.Mutex
}

func newStalledFill() *stalledFill { return &stalledFill{release: make(chan struct{})} }

func (f *stalledFill) fill(ctx context.Context) ([]string, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	<-ctx.Done()
	<-f.release
	f.mu.Lock()
	f.ended = ctx.Err()
	f.mu.Unlock()
	return nil, ctx.Err()
}

func (f *stalledFill) state() (calls int, ended error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.ended
}

func closeAsync(p *fillPool) <-chan error {
	out := make(chan error, 1)
	go func() { out <- p.close(context.Background()) }()
	return out
}

// A fill no caller waits on is the pool's: close cancels it, returns only once
// it has, and starts nothing after.
func TestFillPool_CloseCancelsAndWaitsForAFillNoCallerWaitsOn(t *testing.T) {
	starts := map[string]func(t *testing.T, c *listCache[[]string], fill func(context.Context) ([]string, error)){
		"a missing entry's peek fill": func(t *testing.T, c *listCache[[]string], fill func(context.Context) ([]string, error)) {
			t.Helper()
			if _, ok := c.peek(t.Context(), testKey("k"), fill); ok {
				t.Fatal("peek(cold) answered a value, want none")
			}
		},
		"a shared fill its caller left": func(t *testing.T, c *listCache[[]string], fill func(context.Context) ([]string, error)) {
			t.Helper()
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() {
				_, err := c.get(ctx, testKey("k"), false, fill)
				done <- err
			}()
			synctest.Wait()
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("get(left) error = %v, want context.Canceled", err)
			}
		},
	}
	for name, start := range starts {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				pool := newFillPool(maxListFills)
				c := newListCache[[]string](time.Hour, pool)
				f := newStalledFill()
				start(t, c, f.fill)
				synctest.Wait()
				if calls, _ := f.state(); calls != 1 {
					t.Fatalf("fills in flight before the close = %d, want 1", calls)
				}

				closed := closeAsync(pool)
				synctest.Wait()
				select {
				case err := <-closed:
					t.Fatalf("close() = %v while its fill was still running, want it to wait", err)
				default:
				}

				close(f.release)
				synctest.Wait()
				select {
				case err := <-closed:
					if err != nil {
						t.Errorf("close() = %v, want nil", err)
					}
				default:
					t.Fatal("close() still waiting after its fill returned")
				}
				if _, ended := f.state(); !errors.Is(ended, context.Canceled) {
					t.Errorf("the fill's context ended with %v, want context.Canceled (the close, not the budget)", ended)
				}

				c.peek(t.Context(), testKey("k2"), f.fill)
				if _, err := c.get(t.Context(), testKey("k"), true, f.fill); !errors.Is(err, errFillsClosed) {
					t.Errorf("get(after close) error = %v, want errFillsClosed", err)
				}
				synctest.Wait()
				if calls, _ := f.state(); calls != 1 {
					t.Errorf("fills after the close = %d, want none beyond the first", calls-1)
				}
			})
		})
	}
}

// A close whose wait outlasts ctx answers ctx's error rather than blocking.
func TestFillPool_CloseGivesUpWithItsContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool := newFillPool(maxListFills)
		c := newListCache[[]string](time.Hour, pool)
		f := newStalledFill()
		c.peek(t.Context(), testKey("k"), f.fill)
		synctest.Wait()

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := pool.close(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("close() with a fill held past its ctx = %v, want context.DeadlineExceeded", err)
		}
		close(f.release)
		synctest.Wait()
	})
}

// stalledRepos is fakeCore with a repository list that answers only after its
// context ends and release closes.
type stalledRepos struct {
	*fakeCore
	fill *stalledFill
}

func (s *stalledRepos) ListRepos(ctx context.Context, _ ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Repository], error) {
	_, err := s.fill.fill(ctx)
	return forgeapi.Page[forgeapi.Repository]{}, err
}

// Shutdown owns the repository read HeldRepoNames leaves behind its caller:
// it cancels the read and returns once the read has.
func TestManagerShutdown_CancelsAndWaitsForARepositoryRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		core := &stalledRepos{fakeCore: &fakeCore{}, fill: newStalledFill()}
		m := recordManager(t, core)
		if names := m.HeldRepoNames(t.Context(), "github:github.com", testSignIn); names != nil {
			t.Fatalf("HeldRepoNames() before any read = %v, want nil", names)
		}
		synctest.Wait()
		if calls, _ := core.fill.state(); calls != 1 {
			t.Fatalf("repository reads HeldRepoNames started = %d, want 1", calls)
		}

		var returned atomic.Bool
		shut := make(chan error, 1)
		go func() {
			err := m.Shutdown(t.Context())
			returned.Store(true)
			shut <- err
		}()
		synctest.Wait()
		if returned.Load() {
			t.Fatal("Shutdown() returned while the repository read was still running")
		}
		close(core.fill.release)
		if err := <-shut; err != nil {
			t.Errorf("Shutdown() = %v, want nil", err)
		}
		if _, ended := core.fill.state(); !errors.Is(ended, context.Canceled) {
			t.Errorf("the read's context ended with %v, want context.Canceled", ended)
		}
	})
}
