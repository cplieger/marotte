package tabs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// chatSpec is the smallest legal spec, its ref carrying the case's label.
func chatSpec(ref string) marotte.OpenTab {
	return marotte.OpenTab{Kind: marotte.TabKindChat, Ref: ref}
}

// newTestStore opens a store in a fresh directory and returns both, because
// almost every assertion here is either about the store or about the file beside
// it.
func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("Setup: NewStore(%q): %v", dir, err)
	}
	return s, dir
}

// mustOpen opens a tab and fails the test if it could not, so a case that is
// about something else does not spend three lines establishing its fixture.
func mustOpen(t *testing.T, s *Store, spec marotte.OpenTab) marotte.TabSubject {
	t.Helper()
	sub, created, _, err := s.Open(t.Context(), spec)
	if err != nil {
		t.Fatalf("Setup: Open(%+v): %v", spec, err)
	}
	if !created {
		t.Fatalf("Setup: Open(%+v) returned created=false; the fixture expected a new tab", spec)
	}
	return sub
}

// onDisk decodes the document the store wrote: the durable half of "not lost from memory OR disk".
func onDisk(t *testing.T, dir string) file {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Join(dir, FileName), err)
	}
	var doc file
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse %s: %v", filepath.Join(dir, FileName), err)
	}
	return doc
}

func idsOf(tabs []marotte.TabSubject) []string {
	out := make([]string, 0, len(tabs))
	for _, t := range tabs {
		out = append(out, t.ID)
	}
	return out
}

func refsOf(tabs []marotte.TabSubject) []string {
	out := make([]string, 0, len(tabs))
	for _, t := range tabs {
		out = append(out, string(t.Kind)+":"+t.Ref)
	}
	return out
}

// writeDoc writes a document by hand, which is how every load-path test stages
// the file a previous process (or a person with an editor) left behind.
func writeDoc(t *testing.T, dir string, doc file) {
	t.Helper()
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("Setup: encode %+v: %v", doc, err)
	}
	writeRaw(t, dir, data, fileMode)
}

func writeRaw(t *testing.T, dir string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(dir, dirMode); err != nil {
		t.Fatalf("Setup: mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), data, mode); err != nil {
		t.Fatalf("Setup: write %s: %v", filepath.Join(dir, FileName), err)
	}
}

func TestNewStore_FirstRunIsEmptyAndSilent(t *testing.T) {
	s, _ := newTestStore(t)
	tabs, version := s.List()
	if len(tabs) != 0 || version != 0 {
		t.Errorf("List() on a fresh store = %d tabs at version %d, want 0 tabs at version 0", len(tabs), version)
	}
}

// TestStore_RoundTripsAcrossProcesses pins the version persisting WITH the tabs, so a restart
// does not reuse a seen version for a different set.
func TestStore_RoundTripsAcrossProcesses(t *testing.T) {
	first, dir := newTestStore(t)
	a := mustOpen(t, first, chatSpec("c-a"))
	b := mustOpen(t, first, marotte.OpenTab{Kind: marotte.TabKindEditor, Ref: "/workspace/a.ts"})
	if _, err := first.SetPinned(t.Context(), b.ID, true); err != nil {
		t.Fatalf("Setup: SetPinned(%q, true): %v", b.ID, err)
	}
	wantTabs, wantVersion := first.List()
	if wantVersion != 3 {
		t.Fatalf("Setup: two opens and a pin left version %d, want 3", wantVersion)
	}

	second, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(%q) after a clean write: %v", dir, err)
	}
	gotTabs, gotVersion := second.List()
	if gotVersion != wantVersion {
		t.Errorf("NewStore(...).List() version = %d, want %d", gotVersion, wantVersion)
	}
	if !slices.Equal(gotTabs, wantTabs) {
		t.Errorf("NewStore(...).List() tabs = %+v, want %+v", gotTabs, wantTabs)
	}
	if len(gotTabs) == 2 && (gotTabs[0].ID != a.ID || !gotTabs[1].Pinned) {
		t.Errorf("reloaded tabs lost order or the pin: %+v", gotTabs)
	}
}

// TestNewStore_WarnsAndStartsEmpty pins, for every unreadable shape, a reported error, an empty
// set, and a store that still works (invariant 6).
func TestNewStore_WarnsAndStartsEmpty(t *testing.T) {
	oversized := append([]byte(`{"tabs":[{"id":"a","kind":"chat","ref":"`), []byte(strings.Repeat("x", MaxBytes))...)

	cases := []struct {
		desc string
		data []byte
	}{
		{desc: "a truncated document, which is what a crash mid-write used to leave", data: []byte(`{"tabs":[{"id":"a",`)},
		{desc: "not JSON at all", data: []byte("nonsense")},
		{desc: "a JSON array where the document is an object", data: []byte(`["a","b"]`)},
		{desc: "over the decode bound, refused before it is parsed", data: oversized},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.desc, " ", "-"), func(t *testing.T) {
			dir := t.TempDir()
			writeRaw(t, dir, tc.data, fileMode)

			s, err := NewStore(dir)
			if err == nil {
				t.Errorf("NewStore(%s) = nil error, want a diagnostic for the caller to warn with", tc.desc)
			}
			if s == nil {
				t.Fatalf("NewStore(%s) returned a nil store; the caller has nothing to run on", tc.desc)
			}
			tabs, version := s.List()
			if len(tabs) != 0 || version != 0 {
				t.Errorf("NewStore(%s).List() = %d tabs at version %d, want 0 tabs at version 0", tc.desc, len(tabs), version)
			}
			if _, _, _, err := s.Open(t.Context(), chatSpec("c-1")); err != nil {
				t.Errorf("Open after %s: %v, want the store to be usable", tc.desc, err)
			}
		})
	}
}

// TestNewStore_OversizedIsRefusedBeforeItIsParsed pins the bound to the READ.
func TestNewStore_OversizedIsRefusedBeforeItIsParsed(t *testing.T) {
	dir := t.TempDir()
	// Valid JSON, so a parse error cannot be what refuses it.
	padding := strings.Repeat("x", MaxBytes)
	writeRaw(t, dir, []byte(`{"version":9,"tabs":[],"pad":"`+padding+`"}`), fileMode)

	s, err := NewStore(dir)
	if err == nil || !strings.Contains(err.Error(), "decode bound") {
		t.Errorf("NewStore(oversized) error = %v, want one naming the decode bound", err)
	}
	if _, version := s.List(); version != 0 {
		t.Errorf("NewStore(oversized) adopted version %d, want 0: nothing in that file was read", version)
	}
}

// TestNewStore_TightensAWideMode pins the load-time enforcement for a file widened between writes.
func TestNewStore_TightensAWideMode(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, file{Version: 2, Tabs: []marotte.TabSubject{{ID: "a", Kind: marotte.TabKindGit}}})
	path := filepath.Join(dir, FileName)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Setup: chmod 0644 %s: %v", path, err)
	}

	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(%q) over a 0644 file: %v", dir, err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := fi.Mode().Perm(); got != fileMode {
		t.Errorf("mode of %s after NewStore = %#o, want %#o", FileName, got, fileMode)
	}
	if tabs, version := s.List(); len(tabs) != 1 || version != 2 {
		t.Errorf("List() = %d tabs at version %d, want 1 tab at version 2: tightening the mode must not lose the document", len(tabs), version)
	}
}

// TestNewStore_RefusesASymlinkAtTheName pins that the O_NOFOLLOW mode verdict comes first.
func TestNewStore_RefusesASymlinkAtTheName(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere.json")
	planted := file{Version: 7, Tabs: []marotte.TabSubject{{ID: "planted", Kind: marotte.TabKindDocs}}}
	data, err := json.Marshal(planted)
	if err != nil {
		t.Fatalf("Setup: encode: %v", err)
	}
	if err := os.WriteFile(target, data, fileMode); err != nil {
		t.Fatalf("Setup: write %s: %v", target, err)
	}
	if err := os.MkdirAll(dir, dirMode); err != nil {
		t.Fatalf("Setup: mkdir %s: %v", dir, err)
	}
	if err := os.Symlink(target, filepath.Join(dir, FileName)); err != nil {
		t.Fatalf("Setup: symlink %s: %v", target, err)
	}

	s, err := NewStore(dir)
	if err == nil {
		t.Error("NewStore(dir holding a symlink at tabs.json) = nil error, want a refusal")
	}
	tabs, version := s.List()
	if len(tabs) != 0 || version != 0 {
		t.Errorf("NewStore(symlink).List() = %+v at version %d, want nothing: the target's bytes must not be read", tabs, version)
	}
}

// TestSanitize_DropsWhatOpenCouldNotHaveWritten pins every Open rule on the way in too.
func TestSanitize_DropsWhatOpenCouldNotHaveWritten(t *testing.T) {
	cases := []struct {
		desc     string
		in       []marotte.TabSubject
		wantRefs []string
	}{
		{
			desc:     "an entry with no id",
			in:       []marotte.TabSubject{{ID: "", Kind: marotte.TabKindChat, Ref: "c-a"}, {ID: "b", Kind: marotte.TabKindChat, Ref: "c-b"}},
			wantRefs: []string{"chat:c-b"},
		},
		{
			desc:     "an id longer than anything this store mints",
			in:       []marotte.TabSubject{{ID: strings.Repeat("f", maxIDBytes+1), Kind: marotte.TabKindChat, Ref: "c-a"}},
			wantRefs: nil,
		},
		{
			desc:     "a kind that is not one of the eight, including the deleted plan",
			in:       []marotte.TabSubject{{ID: "a", Kind: "plan", Ref: "x"}, {ID: "b", Kind: "", Ref: "y"}, {ID: "c", Kind: marotte.TabKindDocs}},
			wantRefs: []string{"docs:"},
		},
		{
			desc:     "a singleton carrying a ref it has no meaning for",
			in:       []marotte.TabSubject{{ID: "a", Kind: marotte.TabKindSettings, Ref: "something"}},
			wantRefs: nil,
		},
		{
			desc:     "a kind that needs a ref with none",
			in:       []marotte.TabSubject{{ID: "a", Kind: marotte.TabKindEditor}, {ID: "b", Kind: marotte.TabKindRun}},
			wantRefs: nil,
		},
		{
			desc:     "a ref over the byte bound",
			in:       []marotte.TabSubject{{ID: "a", Kind: marotte.TabKindEditor, Ref: "/" + strings.Repeat("p", MaxRefBytes)}},
			wantRefs: nil,
		},
		{
			desc:     "the same id twice",
			in:       []marotte.TabSubject{{ID: "a", Kind: marotte.TabKindChat, Ref: "c-a"}, {ID: "a", Kind: marotte.TabKindChat, Ref: "c-b"}},
			wantRefs: []string{"chat:c-a"},
		},
		{
			desc:     "two ids for one subject, which Open cannot produce",
			in:       []marotte.TabSubject{{ID: "a", Kind: marotte.TabKindChat, Ref: "c-a"}, {ID: "b", Kind: marotte.TabKindChat, Ref: "c-a"}},
			wantRefs: []string{"chat:c-a"},
		},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.desc, " ", "-"), func(t *testing.T) {
			dir := t.TempDir()
			writeDoc(t, dir, file{Version: 5, Tabs: tc.in})
			s, err := NewStore(dir)
			if err != nil {
				t.Fatalf("NewStore over %s: %v", tc.desc, err)
			}
			tabs, _ := s.List()
			if got := refsOf(tabs); !slices.Equal(got, tc.wantRefs) {
				t.Errorf("NewStore over %s kept %v, want %v", tc.desc, got, tc.wantRefs)
			}
		})
	}
}

// TestSanitize_TruncatesAtTheDecodeBound keeps MaxTabs an outer wall rather than a
// product rule: a file over it loses its tail instead of taking the strip down.
func TestSanitize_TruncatesAtTheDecodeBound(t *testing.T) {
	dir := t.TempDir()
	over := make([]marotte.TabSubject, 0, MaxTabs+10)
	for i := range MaxTabs + 10 {
		over = append(over, marotte.TabSubject{ID: newID(), Kind: marotte.TabKindChat, Ref: "c-" + strconv.Itoa(i)})
	}
	writeDoc(t, dir, file{Version: 1, Tabs: over})

	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore over a %d-tab file: %v", len(over), err)
	}
	tabs, _ := s.List()
	if len(tabs) != MaxTabs {
		t.Errorf("NewStore over a %d-tab file kept %d, want MaxTabs (%d)", len(over), len(tabs), MaxTabs)
	}
}

// TestStore_PersistFailureLeavesNothingBehind pins no-rollback: the next successful mutation
// commits version 1. The failure is a FILE where the config directory belongs (ENOTDIR), since
// tests run as root.
func TestStore_PersistFailureLeavesNothingBehind(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "config")
	if err := os.WriteFile(dir, []byte("not a directory"), fileMode); err != nil {
		t.Fatalf("Setup: write %s: %v", dir, err)
	}

	s, err := NewStore(dir)
	if err == nil {
		t.Error("NewStore(dir that is a file) = nil error, want a diagnostic")
	}

	_, _, version, err := s.Open(t.Context(), chatSpec("c-a"))
	if err == nil {
		t.Fatal("Open with an unwritable path = nil error, want the write failure")
	}
	if version != 0 {
		t.Errorf("Open returned version %d on a failed write, want 0", version)
	}
	if tabs, v := s.List(); len(tabs) != 0 || v != 0 {
		t.Errorf("after a failed write List() = %d tabs at version %d, want 0 tabs at version 0: the clone was mutated, not the state", len(tabs), v)
	}

	// The directory comes back — the shape of a config volume that was remounted.
	if err := os.Remove(dir); err != nil {
		t.Fatalf("Setup: remove %s: %v", dir, err)
	}
	if err := os.MkdirAll(dir, dirMode); err != nil {
		t.Fatalf("Setup: mkdir %s: %v", dir, err)
	}
	sub, created, version, err := s.Open(t.Context(), chatSpec("c-a"))
	if err != nil {
		t.Fatalf("Open after the path became writable: %v", err)
	}
	if !created || version != 1 {
		t.Errorf("Open after a failed attempt = created %v at version %d, want created true at version 1: the failure consumed no version", created, version)
	}
	if doc := onDisk(t, dir); len(doc.Tabs) != 1 || doc.Tabs[0].ID != sub.ID || doc.Version != 1 {
		t.Errorf("tabs.json = %+v, want the one tab %q at version 1", doc, sub.ID)
	}
}

// TestBounds_AreConsistentWithEachOther pins the arithmetic: the largest document the bounds
// permit fits MaxBytes, and MaxOpenTabs sits under MaxTabs.
func TestBounds_AreConsistentWithEachOther(t *testing.T) {
	if MaxOpenTabs > MaxTabs {
		t.Errorf("MaxOpenTabs (%d) is over MaxTabs (%d): a full strip would reload truncated", MaxOpenTabs, MaxTabs)
	}
	tabs := make([]marotte.TabSubject, 0, MaxTabs)
	for i := range MaxTabs {
		tabs = append(tabs, marotte.TabSubject{
			ID:   newID(),
			Kind: marotte.TabKindEditor,
			Ref:  fmt.Sprintf("/%0*d", MaxRefBytes-1, i),
		})
	}
	data, err := json.MarshalIndent(file{Tabs: tabs, Version: 1}, "", "  ")
	if err != nil {
		t.Fatalf("encode %d tabs: %v", len(tabs), err)
	}
	if len(data) > MaxBytes {
		t.Errorf("a %d-tab document with %d-byte refs encodes to %d bytes, over MaxBytes (%d)",
			MaxTabs, MaxRefBytes, len(data), MaxBytes)
	}
}

// TestNewStore_AdoptsTheVersionEvenWhenSanitizeDropped records a deliberate
// decision rather than an accident: nothing bumps at load, because no event is
// emitted at load and a client's authority after a restart is the list endpoint.
func TestNewStore_AdoptsTheVersionEvenWhenSanitizeDropped(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, file{Version: 12, Tabs: []marotte.TabSubject{
		{ID: "good", Kind: marotte.TabKindGit},
		{ID: "bad", Kind: "plan", Ref: "x"},
	}})
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	tabs, version := s.List()
	if len(tabs) != 1 || version != 12 {
		t.Errorf("List() = %d tabs at version %d, want 1 tab at version 12", len(tabs), version)
	}
}

// TestNewID_IsOpaqueHexAndUnique pins 32 lowercase hex characters (the alphabet, not only the length).
func TestNewID_IsOpaqueHexAndUnique(t *testing.T) {
	const draws = 4096
	seen := make(map[string]struct{}, draws)
	for range draws {
		id := newID()
		if len(id) != 32 {
			t.Fatalf("newID() = %q, want 32 characters (16 bytes of hex)", id)
		}
		for i, r := range id {
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
				t.Fatalf("newID() = %q: byte %d is %q, want a lowercase hex digit", id, i, r)
			}
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("newID() minted %q twice in %d draws", id, draws)
		}
		seen[id] = struct{}{}
	}
}

func TestErrorsAreComparableWithIs(t *testing.T) {
	s, _ := newTestStore(t)
	_, _, _, err := s.Open(t.Context(), marotte.OpenTab{Kind: "plan", Ref: "x"})
	if !errors.Is(err, ErrBadKind) {
		t.Errorf("Open with kind %q = %v, want ErrBadKind", "plan", err)
	}
}
