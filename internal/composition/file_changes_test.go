package composition

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/marotte/internal/marotte"
)

type fileChangeRig struct {
	events    []marotte.ServerEvent
	follows   [][2]string
	refreshes int
}

func (r *fileChangeRig) Broadcast(_ context.Context, evt marotte.ServerEvent) {
	r.events = append(r.events, evt)
}

func (r *fileChangeRig) FollowRename(_ context.Context, from, to string) {
	r.follows = append(r.follows, [2]string{from, to})
}

func (r *fileChangeRig) RefreshSlashCommands() { r.refreshes++ }

func (r *fileChangeRig) types() []marotte.EventType {
	out := make([]marotte.EventType, 0, len(r.events))
	for _, e := range r.events {
		out = append(out, e.Type)
	}
	return out
}

func (r *fileChangeRig) dirs(t *testing.T) []string {
	t.Helper()
	for _, e := range r.events {
		if e.Type == marotte.EventFilesChanged {
			p, ok := e.Payload.(marotte.FilesChangedPayload)
			if !ok {
				t.Fatalf("files_changed payload = %T", e.Payload)
			}
			return p.Dirs
		}
	}
	return nil
}

func newFileChangeRig() (*fileChangeRig, *fileChanges) {
	rig := &fileChangeRig{}
	return rig, &fileChanges{workDir: "/work", kiroHome: "/home/u/.kiro", hub: rig, tabs: rig, slash: rig}
}

// A prompt renamed in the workspace's .kiro moves its tabs, re-lists its folder, refetches the docs and re-reads the
// slash menu, in that order.
func TestFileChanges_APromptRenameReachesEverySurface(t *testing.T) {
	rig, fc := newFileChangeRig()
	from, to := "/work/.kiro/prompts/Remember.md", "/work/.kiro/prompts/Recall.md"
	fc.changed(t.Context(), filebrowse.Change{Paths: []string{from, to}, From: from, To: to})

	if !slices.Equal(rig.follows, [][2]string{{from, to}}) {
		t.Errorf("follows = %v, want one from %s to %s", rig.follows, from, to)
	}
	if got := rig.dirs(t); !slices.Equal(got, []string{"/work/.kiro/prompts"}) {
		t.Errorf("files_changed dirs = %v, want the prompts folder once", got)
	}
	if got := rig.types(); !slices.Equal(got, []marotte.EventType{marotte.EventFilesChanged, marotte.EventKiroDocsChanged}) {
		t.Errorf("events = %v, want files_changed then kiro_docs_changed", got)
	}
	if rig.refreshes != 1 {
		t.Errorf("slash refreshes = %d, want 1", rig.refreshes)
	}
}

func TestFileChanges_AWriteOutsideKiroOnlyReListsItsFolder(t *testing.T) {
	rig, fc := newFileChangeRig()
	fc.changed(t.Context(), filebrowse.Change{Paths: []string{"/work/src/main.go"}})
	if got := rig.types(); !slices.Equal(got, []marotte.EventType{marotte.EventFilesChanged}) {
		t.Errorf("events = %v, want files_changed alone", got)
	}
	if got := rig.dirs(t); !slices.Equal(got, []string{"/work/src"}) {
		t.Errorf("dirs = %v, want /work/src", got)
	}
	if len(rig.follows) != 0 || rig.refreshes != 0 {
		t.Errorf("follows %v, refreshes %d, want none", rig.follows, rig.refreshes)
	}
}

// The user's own prompts are slash commands but not docs rows, and a repository's .kiro is a docs root.
func TestFileChanges_KiroHomeAndRepoRoots(t *testing.T) {
	cases := []struct {
		desc, path  string
		wantDocs    bool
		wantRefresh bool
	}{
		{"a user prompt", filepath.Join("/home/u/.kiro", "prompts", "p.md"), false, true},
		{"a user skill", filepath.Join("/home/u/.kiro", "skills", "s", "SKILL.md"), false, true},
		{"a steering doc", "/work/.kiro/steering/a.md", true, false},
		{"a repository's prompt", "/work/repo/.kiro/prompts/p.md", true, false},
		{"a first-level folder, which may hold a .kiro", "/work/repo", true, false},
		{"a hidden first-level folder", "/work/.cache", false, false},
		{"the workspace .kiro itself", "/work/.kiro", true, true},
		{"a hidden folder's .kiro", "/work/.cache/.kiro/steering/a.md", false, false},
		{"a look-alike name", "/work/.kiro-old/prompts/p.md", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			rig, fc := newFileChangeRig()
			fc.changed(t.Context(), filebrowse.Change{Paths: []string{tc.path}})
			docs := slices.Contains(rig.types(), marotte.EventKiroDocsChanged)
			if docs != tc.wantDocs || (rig.refreshes == 1) != tc.wantRefresh {
				t.Errorf("%s: kiro_docs_changed %v, refreshes %d; want %v, refresh %v", tc.path, docs, rig.refreshes, tc.wantDocs, tc.wantRefresh)
			}
		})
	}
}
