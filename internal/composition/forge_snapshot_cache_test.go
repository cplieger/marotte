package composition

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/keyenc"
	"github.com/cplieger/marotte/internal/steering"
)

// newNamesForTest is a repository-names cache over build whose steering file
// lists one GitHub provider carrying the names held for id signed in as account.
func newNamesForTest(t *testing.T, build func(context.Context) map[string][]string) (*repoNamesCache, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	steer := steering.New(t.TempDir(), t.TempDir())
	steer.SetForgeSnapshot(func() steering.ForgeSnapshot { return steering.ForgeSnapshot{} })
	c := newRepoNamesCache(t.Context(), steer, build)
	steer.SetForgeSnapshot(func() steering.ForgeSnapshot {
		return steering.ForgeSnapshot{Providers: []steering.ForgeProvider{
			{Kind: "github", Host: "github.com", User: "alice", Repos: c.lookup("github:github.com", "sign-in-a")},
		}}
	})
	return c, filepath.Join(home, ".kiro", "steering", "environment.md")
}

func namesOf(names ...string) map[string][]string {
	return map[string][]string{keyenc.Join("github:github.com", "sign-in-a"): names}
}

// lookup returns at once even while the build (a network-bound repository
// listing in production) is stuck, which keeps steering.Generate safe on the
// pre-bridge-spawn path.
func TestRepoNamesCache_LookupNeverBlocksOnBuild(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	c, _ := newNamesForTest(t, func(context.Context) map[string][]string {
		started <- struct{}{}
		<-release
		return nil
	})

	done := make(chan []string, 1)
	go func() { done <- c.lookup("github:github.com", "sign-in-a") }()

	select {
	case names := <-done:
		if len(names) != 0 {
			t.Errorf("cold lookup = %v, want none", names)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("lookup() blocked on the build function")
	}
	<-started
	close(release)
}

// A refresh with new names lands in the cache and regenerates environment.md,
// and one with the same names leaves the file untouched.
func TestRepoNamesCache_RefreshRegeneratesOnChange(t *testing.T) {
	var current atomic.Pointer[map[string][]string]
	first := namesOf("alice/app")
	current.Store(&first)
	c, envPath := newNamesForTest(t, func(context.Context) map[string][]string { return *current.Load() })

	c.refresh()
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("refresh with changed names did not regenerate: %v", err)
	}
	if !strings.Contains(string(data), "alice/app") {
		t.Errorf("regenerated file missing the repository name:\n%s", data)
	}
	if got := c.lookup("github:github.com", "sign-in-a"); len(got) != 1 || got[0] != "alice/app" {
		t.Errorf("lookup = %v, want [alice/app]", got)
	}
	if got := c.lookup("github:github.com", "sign-in-b"); len(got) != 0 {
		t.Errorf("lookup under another account = %v, want none: the names are the account's", got)
	}

	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(envPath, old, old); err != nil {
		t.Fatal(err)
	}
	c.refresh()
	info, err := os.Stat(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Before(time.Now().Add(-30 * time.Minute)) {
		t.Error("a refresh with the same names regenerated the steering file")
	}
}

// A refresh arriving while a rebuild is in flight is not dropped: the rebuild
// may have read the names before the change, so one more pass reads them after.
func TestRepoNamesCache_CoalescesConcurrentRefresh(t *testing.T) {
	firstBuild := make(chan struct{})
	proceed := make(chan struct{})
	var calls atomic.Int32
	var current atomic.Pointer[map[string][]string]
	empty := map[string][]string{}
	current.Store(&empty)

	c, _ := newNamesForTest(t, func(context.Context) map[string][]string {
		if calls.Add(1) == 1 {
			close(firstBuild)
			<-proceed
		}
		return *current.Load()
	})

	var wg sync.WaitGroup
	wg.Go(c.refresh)
	<-firstBuild

	changed := namesOf("bob/tool")
	current.Store(&changed)
	c.refresh()
	close(proceed)
	wg.Wait()

	if got := calls.Load(); got != 2 {
		t.Errorf("build calls = %d, want 2 (coalesced re-run)", got)
	}
	if got := c.lookup("github:github.com", "sign-in-a"); len(got) != 1 || got[0] != "bob/tool" {
		t.Errorf("lookup = %v, want the post-change [bob/tool]", got)
	}
}
