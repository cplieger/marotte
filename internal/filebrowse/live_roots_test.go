package filebrowse

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cplieger/marotte/internal/settings"
)

// liveFixture is a handler with one boot-time mount and a live root list the test can change.
type liveFixture struct {
	h      *Handler
	static string
	live   string
	roots  atomic.Pointer[[]string]
}

func newLiveFixture(t *testing.T) *liveFixture {
	t.Helper()
	f := &liveFixture{static: t.TempDir(), live: t.TempDir()}
	f.set(f.live)
	h, err := New(Sensitive{}, []string{f.static}, WithLiveRoots(func(context.Context) []string { return *f.roots.Load() }))
	if err != nil {
		t.Fatal(err)
	}
	f.h = h
	return f
}

func (f *liveFixture) set(dirs ...string) { f.roots.Store(&dirs) }

func rootNames(t *testing.T, h *Handler) []string {
	t.Helper()
	rec := getReq(t, h, "/api/files?path=/")
	if rec.Code != 200 {
		t.Fatalf("root listing status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct{ Files []fileEntry }
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(resp.Files))
	for _, e := range resp.Files {
		names = append(names, e.Name)
	}
	return names
}

func TestLiveRoots_AreMountedBesideTheBootRoots(t *testing.T) {
	f := newLiveFixture(t)
	if err := os.WriteFile(filepath.Join(f.live, "notes.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	names := rootNames(t, f.h)
	for _, dir := range []string{f.static, f.live} {
		if !slices.Contains(names, strings.TrimPrefix(dir, "/")) {
			t.Errorf("root listing %v lacks %q", names, dir)
		}
	}
	if rec := getReq(t, f.h, "/api/file?path="+f.live+"/notes.md"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "hello") {
		t.Errorf("read in a live root = %d %s, want 200 with the content", rec.Code, rec.Body.String())
	}
	if rec := putReq(t, f.h, "/api/file?path="+f.live+"/new.md", `{"content":"written"}`); rec.Code != 200 {
		t.Errorf("write in a live root = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if got, err := os.ReadFile(filepath.Join(f.live, "new.md")); err != nil || string(got) != "written" {
		t.Errorf("written file = %q, %v; want %q", got, err, "written")
	}
	rec := getReq(t, f.h, "/api/files/search?path=/&q=notes")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "notes.md") {
		t.Errorf("search over every mount = %d %s, want a hit in the live root", rec.Code, rec.Body.String())
	}
}

func TestLiveRoots_RemovingOneRevokesItAtTheNextRequest(t *testing.T) {
	f := newLiveFixture(t)
	if err := os.WriteFile(filepath.Join(f.live, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec := getReq(t, f.h, "/api/file?path="+f.live+"/a.txt"); rec.Code != 200 {
		t.Fatalf("read before removal = %d, want 200", rec.Code)
	}
	f.set()
	if rec := getReq(t, f.h, "/api/file?path="+f.live+"/a.txt"); rec.Code != 403 {
		t.Errorf("read after removal = %d, want 403", rec.Code)
	}
	if names := rootNames(t, f.h); slices.Contains(names, strings.TrimPrefix(f.live, "/")) {
		t.Errorf("root listing after removal %v still shows %q", names, f.live)
	}
}

func TestLiveRoots_SymlinkOutOfALiveRootIsRefused(t *testing.T) {
	f := newLiveFixture(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(f.live, "escape")); err != nil {
		t.Fatal(err)
	}
	if rec := getReq(t, f.h, "/api/file?path="+f.live+"/escape/secret.txt"); rec.Code != 403 {
		t.Errorf("read through a symlink out of a live root = %d, want 403", rec.Code)
	}
	if rec := getReq(t, f.h, "/api/file?path="+f.live+"/../"+filepath.Base(outside)+"/secret.txt"); rec.Code != 403 {
		t.Errorf("read through .. out of a live root = %d, want 403", rec.Code)
	}
}

func TestLiveRoots_UnopenableOrBootDuplicateEntriesAddNoMount(t *testing.T) {
	f := newLiveFixture(t)
	f.set(filepath.Join(f.live, "missing"), f.static, f.live)
	names := rootNames(t, f.h)
	want := []string{strings.TrimPrefix(f.static, "/"), strings.TrimPrefix(f.live, "/")}
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Errorf("root listing = %v, want exactly %v", names, want)
	}
}

// Fed the granted additional directories, a hand-edited list naming a directory and one inside
// it, in either order, mounts only the earlier, so no delete removes a mounted root.
func TestLiveRoots_OverlappingEntriesNeverLetADeleteRemoveAMountedRoot(t *testing.T) {
	for _, tc := range []struct {
		name  string
		order func(outer, inner string) []string
	}{
		{"the outer one first", func(outer, inner string) []string { return []string{outer, inner} }},
		{"the inner one first", func(outer, inner string) []string { return []string{inner, outer} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			dirs := settings.AgentDirs{Workspace: filepath.Join(base, "workspace"), Config: filepath.Join(base, "config")}
			outer := filepath.Join(base, "data")
			parent := filepath.Join(outer, "parent")
			inner := filepath.Join(parent, "project")
			for _, d := range []string{dirs.Workspace, dirs.Config, inner} {
				if err := os.MkdirAll(d, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			data, err := json.Marshal(map[string][]string{settings.KeyAdditionalDirectories: tc.order(outer, inner)})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dirs.Config, settings.Filename), data, 0o600); err != nil {
				t.Fatal(err)
			}
			h, err := New(Sensitive{}, []string{dirs.Workspace}, WithLiveRoots(func(ctx context.Context) []string {
				granted, _, _ := dirs.Granted(ctx)
				return granted
			}))
			if err != nil {
				t.Fatal(err)
			}
			granted, _, _ := dirs.Granted(t.Context())
			for _, p := range []string{inner, parent, outer} {
				postReq(t, h, "/api/files/action", `{"action":"delete","path":"`+p+`"}`)
			}
			for _, root := range granted {
				if _, err := os.Stat(root); err != nil {
					t.Errorf("the mounted root %s is gone: %v", root, err)
				}
			}
		})
	}
}
