package composition

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/forges"
	"github.com/cplieger/marotte/internal/steering"
	"github.com/cplieger/marotte/internal/workspace"
)

// stubGitHubRepos is a plaintext loopback GitHub API answering alice's
// identity and the repository list repos holds, as JSON.
func stubGitHubRepos(t *testing.T, repos *atomic.Value) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/user":
			_, _ = io.WriteString(w, `{"login":"alice"}`)
		case "/api/v3/user/repos":
			_, _ = io.WriteString(w, repos.Load().(string))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

const (
	oneRepo  = `[{"id":1,"name":"app","full_name":"alice/app","owner":{"login":"alice"}}]`
	twoRepos = `[{"id":1,"name":"app","full_name":"alice/app","owner":{"login":"alice"}},` +
		`{"id":2,"name":"lib","full_name":"alice/lib","owner":{"login":"alice"}}]`
)

// countingSteering is w's steering sync with its regenerations counted.
func countingSteering(t *testing.T, w *wiredForges) (*forgeSteering, *atomic.Int32) {
	t.Helper()
	generated := &atomic.Int32{}
	s := &forgeSteering{
		appCtx: context.WithoutCancel(t.Context()), m: w.m,
		generate: func(context.Context) { generated.Add(1) },
	}
	w.notifier.steer = s
	return s, generated
}

func (s *forgeSteering) held() steering.ForgeSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// waitFor polls cond until it holds, failing with what after five seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func reposOf(snap steering.ForgeSnapshot) []string {
	if len(snap.Providers) != 1 {
		return nil
	}
	return snap.Providers[0].Repos
}

// A repository page landing behind a sync regenerates the steering file with no
// further sync or Generate call, a names-only change included.
func TestForgeSteering_ALandedRepoPageRegenerates(t *testing.T) {
	repos := &atomic.Value{}
	repos.Store(oneRepo)
	srv := stubGitHubRepos(t, repos)
	w := wireForges(t)
	s, generated := countingSteering(t, w)

	id := w.signIn(t, srv)
	waitFor(t, "the sign-in's two regenerations, the second with its names", func() bool {
		return generated.Load() == 2 && slices.Equal(reposOf(s.held()), []string{"alice/app"})
	})
	before := generated.Load()

	repos.Store(twoRepos)
	if rec := w.send(t, http.MethodGet, "/api/forges/"+id+"/repos?refresh=1", ""); rec.Code != http.StatusOK {
		t.Fatalf("GET the repository list = %d %s", rec.Code, rec.Body)
	}
	waitFor(t, "the refreshed names in the steering snapshot", func() bool {
		return slices.Equal(reposOf(s.held()), []string{"alice/app", "alice/lib"})
	})
	waitFor(t, "a regeneration for the new names", func() bool { return generated.Load() > before })
}

// At boot, the sync wire hands back writes a saved connection into the steering
// file's forge section. The forge is down, so no repository page lands to
// trigger a second write.
func TestForgeSteering_BootSyncWritesASavedConnectionToTheSteeringFile(t *testing.T) {
	repos := &atomic.Value{}
	repos.Store(oneRepo)
	srv := stubGitHubRepos(t, repos)
	w := wireForges(t)
	w.signIn(t, srv)
	srv.Close()

	notifier := &forgeNotifier{hub: &recordingHub{}}
	m := forges.NewManager(w.dir, notifier.notify)
	m.Load(t.Context())
	poller := forges.NewPRStatusPoller(forges.NewManagerPRSource(m, func(context.Context) []forges.RepoOrigin { return nil }),
		quietNotifier{}, func() forges.Gate { return forges.Gate{} })
	steer := steering.New(t.TempDir(), t.TempDir())

	notifier.wire(context.WithoutCancel(t.Context()), poller, m, steer).sync()

	path := workspace.KiroSteeringPath("environment.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the steering file: %v", err)
	}
	for _, want := range []string{"## Connected forges", "- Authenticated as: alice"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("%s lacks %q", path, want)
		}
	}
}

// sync regenerates only when the snapshot moved: a sync with nothing new does
// not, a sign-out does.
func TestForgeSteering_SyncRegeneratesOnlyWhenTheSnapshotMoves(t *testing.T) {
	repos := &atomic.Value{}
	repos.Store(oneRepo)
	srv := stubGitHubRepos(t, repos)
	w := wireForges(t)
	s, generated := countingSteering(t, w)

	id := w.signIn(t, srv)
	waitFor(t, "the sign-in's two regenerations, the second with its names", func() bool {
		return generated.Load() == 2 && slices.Equal(reposOf(s.held()), []string{"alice/app"})
	})
	settled := generated.Load()
	s.sync()
	if got := generated.Load(); got != settled {
		t.Errorf("a sync with nothing moved regenerated, %d times in all, want still %d", got, settled)
	}

	if rec := w.send(t, http.MethodDelete, "/api/forges/"+id, ""); rec.Code != http.StatusOK {
		t.Fatalf("DELETE the connection = %d %s", rec.Code, rec.Body)
	}
	waitFor(t, "a regeneration for the sign-out", func() bool {
		return generated.Load() > settled && len(s.held().Providers) == 0
	})
}

// A stored repository page reaches the steering sync and no tab.
func TestForgeNotify_AStoredRepoPageTellsNoTab(t *testing.T) {
	w := wireForges(t)
	before := w.hub.count()

	w.notifier.notify(t.Context(), forges.Change{ID: "github:github.com", ReposListed: true})

	if got := w.hub.count(); got != before {
		t.Errorf("a stored repository page broadcast %d frames, want none", got-before)
	}
}
