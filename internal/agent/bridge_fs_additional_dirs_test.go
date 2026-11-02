package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

// extraRootHub is a hub whose config.json grants f.extra, holding one file.
func extraRootHub(t *testing.T) (h *Runtime, br *respondingBridge, f agentRootsFixture, file string) {
	t.Helper()
	f = newAgentRootsFixture(t)
	file = filepath.Join(f.extra, "notes.txt")
	if err := os.WriteFile(file, []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, br = hubForFSTest(t, f.work, WithConfigDir(f.config))
	return h, br, f, file
}

func rootRead(t *testing.T, h *Runtime, br *respondingBridge, path string) (string, error) {
	t.Helper()
	id := int64(1)
	h.inbound.respondFSRead(t.Context(), "c1", h.originOf("c1"), &marotte.RPCResponse{
		ID: &id, Method: marotte.MethodFSRead, Params: mustJSON(t, map[string]any{"path": path}),
	})
	<-br.done
	res, _ := br.response.result.(map[string]any)
	content, _ := res["content"].(string)
	return content, br.response.err
}

func rootWrite(t *testing.T, h *Runtime, br *respondingBridge, path, content string) error {
	t.Helper()
	id := int64(2)
	h.inbound.respondFSWrite(t.Context(), "c1", h.originOf("c1"), &marotte.RPCResponse{
		ID: &id, Method: marotte.MethodFSWrite, Params: mustJSON(t, map[string]any{"path": path, "content": content}),
	})
	<-br.done
	return br.response.err
}

func rootDelete(t *testing.T, h *Runtime, br *respondingBridge, path string) error {
	t.Helper()
	h.inbound.respondKiroFSDelete(t.Context(), "c1", h.originOf("c1"), kiroFSMsg(t, 3, methodKiroFSDelete, path))
	<-br.done
	return br.response.err
}

func rootStat(t *testing.T, h *Runtime, br *respondingBridge, path string) error {
	t.Helper()
	h.inbound.respondKiroFSStat(t.Context(), "c1", h.originOf("c1"), kiroFSMsg(t, 4, methodKiroFSStat, path))
	<-br.done
	return br.response.err
}

// termCwd starts `pwd` in cwd and answers the directory it printed, or the create's error.
func termCwd(t *testing.T, h *Runtime, br *respondingBridge, cwd string) (string, error) {
	t.Helper()
	id := int64(5)
	msg := &marotte.RPCResponse{ID: &id, Method: methodTermCreate, Params: mustJSON(t,
		map[string]any{"command": "pwd", "cwd": cwd})}
	h.translateACPEvent("c1", h.originOf("c1"), msg)
	<-br.done
	if br.response.err != nil {
		return "", br.response.err
	}
	term := singleTerm(t, h)
	waitClosed(t, term.done, "terminal")
	return strings.TrimSpace(term.rawOutput()), nil
}

func TestAdditionalDirectory_EveryVerbReachesIt(t *testing.T) {
	h, br, f, file := extraRootHub(t)
	if got, err := rootRead(t, h, br, file); err != nil || got != "one\ntwo\n" {
		t.Errorf("read %s = %q, %v; want its content", file, got, err)
	}
	created := filepath.Join(f.extra, "new", "made.txt")
	if err := rootWrite(t, h, br, created, "made"); err != nil {
		t.Errorf("write %s = %v, want nil", created, err)
	}
	if got, err := os.ReadFile(created); err != nil || string(got) != "made" {
		t.Errorf("%s after the write = %q, %v; want %q", created, got, err, "made")
	}
	if err := rootStat(t, h, br, created); err != nil {
		t.Errorf("stat %s = %v, want nil", created, err)
	}
	if err := rootDelete(t, h, br, file); err != nil {
		t.Errorf("delete %s = %v, want nil", file, err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("%s after the delete: stat err = %v, want not-exist", file, err)
	}
	if dir, err := termCwd(t, h, br, f.extra); err != nil || dir != f.extra {
		t.Errorf("a shell started in %s ran in %q (%v)", f.extra, dir, err)
	}
}

func TestAdditionalDirectory_ItselfCannotBeDeleted(t *testing.T) {
	h, br, f, _ := extraRootHub(t)
	if err := rootDelete(t, h, br, f.extra); err == nil {
		t.Error("deleting the additional directory itself succeeded, want refused")
	}
	if _, err := os.Stat(f.extra); err != nil {
		t.Errorf("the additional directory is gone: %v", err)
	}
}

func TestAdditionalDirectory_PathOutsideEveryRootIsRefused(t *testing.T) {
	h, br, _, _ := extraRootHub(t)
	outside := canonDir(t, t.TempDir())
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte(outsideSecret), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := rootRead(t, h, br, secret); err == nil {
		t.Errorf("read %s = %q, want refused", secret, got)
	}
	if err := rootWrite(t, h, br, filepath.Join(outside, "planted.txt"), "x"); err == nil {
		t.Error("a write outside every root succeeded, want refused")
	}
	if err := rootDelete(t, h, br, secret); err == nil {
		t.Error("a delete outside every root succeeded, want refused")
	}
	if dir, err := termCwd(t, h, br, outside); err == nil {
		t.Errorf("a shell started outside every root, in %q", dir)
	}
	if got, err := os.ReadFile(secret); err != nil || string(got) != outsideSecret {
		t.Errorf("the outside file changed: %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(outside, "planted.txt")); !os.IsNotExist(err) {
		t.Errorf("a file was planted outside every root: %v", err)
	}
}

func TestAdditionalDirectory_SymlinkOutOfItIsRefused(t *testing.T) {
	h, br, f, _ := extraRootHub(t)
	outside := canonDir(t, t.TempDir())
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte(outsideSecret), 0o600); err != nil {
		t.Fatal(err)
	}
	fileLink := filepath.Join(f.extra, "secret-link.txt")
	dirLink := filepath.Join(f.extra, "out")
	if err := os.Symlink(secret, fileLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dirLink); err != nil {
		t.Fatal(err)
	}
	if got, err := rootRead(t, h, br, fileLink); err == nil {
		t.Errorf("read through %s = %q, want refused", fileLink, got)
	}
	if err := rootWrite(t, h, br, filepath.Join(dirLink, "planted.txt"), "x"); err == nil {
		t.Error("a write through a directory symlink out of the additional directory succeeded, want refused")
	}
	if err := rootWrite(t, h, br, fileLink, "clobber"); err == nil {
		t.Error("a write through a file symlink out of the additional directory succeeded, want refused")
	}
	if dir, err := termCwd(t, h, br, dirLink); err == nil {
		t.Errorf("a shell started through a symlink out of the additional directory, in %q", dir)
	}
	if got, err := os.ReadFile(secret); err != nil || string(got) != outsideSecret {
		t.Errorf("the outside file changed: %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(outside, "planted.txt")); !os.IsNotExist(err) {
		t.Errorf("a file was planted through the symlink: %v", err)
	}
}

func TestAdditionalDirectory_DotDotTraversalIsRefused(t *testing.T) {
	h, br, f, _ := extraRootHub(t)
	// The config directory sits beside the additional directory, so one ".." reaches it.
	escape := f.extra + "/../" + filepath.Base(f.config) + "/" + settings.Filename
	if got, err := rootRead(t, h, br, escape); err == nil {
		t.Errorf("read %s = %q, want refused", escape, got)
	}
	if err := rootWrite(t, h, br, escape, "{}"); err == nil {
		t.Errorf("write %s succeeded, want refused", escape)
	}
	if dir, err := termCwd(t, h, br, f.extra+"/../"+filepath.Base(f.config)); err == nil {
		t.Errorf("a shell started through .. in %q", dir)
	}
	got, _, err := settings.FieldStrict[[]string](t.Context(), f.config, settings.KeyAdditionalDirectories)
	if err != nil || len(got) != 1 || got[0] != f.extra {
		t.Errorf("config.json after the refused write holds %v (%v), want it unchanged", got, err)
	}
}

// A hand-edited config.json naming the config directory grants nothing there.
func TestAdditionalDirectory_ConfigDirectoryCanNeverBeAdded(t *testing.T) {
	f := newAgentRootsFixture(t)
	writeSetting(t, f.config, settings.KeyAdditionalDirectories, []string{f.config, filepath.Dir(f.config)})
	h, br := hubForFSTest(t, f.work, WithConfigDir(f.config))
	target := filepath.Join(f.config, settings.Filename)
	if got, err := rootRead(t, h, br, target); err == nil {
		t.Errorf("read %s = %q, want refused", target, got)
	}
	if err := rootWrite(t, h, br, filepath.Join(f.config, "planted.txt"), "x"); err == nil {
		t.Error("a write into the config directory succeeded, want refused")
	}
	if dir, err := termCwd(t, h, br, f.config); err == nil {
		t.Errorf("a shell started in the config directory, in %q", dir)
	}
	if got := h.lifecycle.sessionDirs(t.Context()); len(got) != 0 {
		t.Errorf("a session door would carry %v, want none", got)
	}
}

// The bridge reads the setting per operation, so removing it revokes access at once, for every
// session open and new.
func TestRemovingTheSetting_RevokesAccessForNewSessions_AtTheBridge(t *testing.T) {
	h, br, f, file := extraRootHub(t)
	if _, err := rootRead(t, h, br, file); err != nil {
		t.Fatalf("read %s with the directory granted = %v, want nil", file, err)
	}
	writeSetting(t, f.config, settings.KeyAdditionalDirectories, []string{})
	if got, err := rootRead(t, h, br, file); err == nil {
		t.Errorf("read %s after the removal = %q, want refused", file, got)
	}
	if err := rootWrite(t, h, br, file, "after"); err == nil {
		t.Error("a write after the removal succeeded, want refused")
	}
	if dir, err := termCwd(t, h, br, f.extra); err == nil {
		t.Errorf("a shell started in the removed directory, in %q", dir)
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != "one\ntwo\n" {
		t.Errorf("%s after the removal = %q, %v; want it untouched", file, got, err)
	}
}

// A hand-edited list may name a directory and one inside it, in either order. Only the earlier
// of the pair is granted, so no delete reaches a granted root as a path inside another.
func TestAdditionalDirectory_OverlappingEntriesNeverLetADeleteRemoveAGrantedRoot(t *testing.T) {
	for _, tc := range []struct {
		name  string
		order func(outer, inner string) []string
	}{
		{"the outer one first", func(outer, inner string) []string { return []string{outer, inner} }},
		{"the inner one first", func(outer, inner string) []string { return []string{inner, outer} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, br, f, _ := extraRootHub(t)
			parent := filepath.Join(f.extra, "parent")
			inner := filepath.Join(parent, "project")
			if err := os.MkdirAll(inner, 0o700); err != nil {
				t.Fatal(err)
			}
			writeSetting(t, f.config, settings.KeyAdditionalDirectories, tc.order(f.extra, inner))
			granted, _, err := settings.AgentDirs{Workspace: f.work, Config: f.config}.Granted(t.Context())
			if err != nil || len(granted) == 0 {
				t.Fatalf("Granted = %v, %v; want a granted root", granted, err)
			}
			for _, p := range []string{inner, parent, f.extra} {
				_ = rootDelete(t, h, br, p)
			}
			for _, root := range granted {
				if _, err := os.Stat(root); err != nil {
					t.Errorf("the granted root %s is gone: %v", root, err)
				}
			}
		})
	}
}
