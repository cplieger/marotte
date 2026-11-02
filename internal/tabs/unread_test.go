package tabs

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// asidesIn lists the arrangement's asides in dir, oldest first.
func asidesIn(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, fileName+".unreadable-*"))
	if err != nil {
		t.Fatalf("glob asides: %v", err)
	}
	return matches
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func TestNewStore_MovesAnUnreadArrangementAside(t *testing.T) {
	oversized := append([]byte(`{"tabs":[],"pad":"`), bytes.Repeat([]byte("x"), maxBytes)...)
	cases := []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{name: "parse error", data: []byte(`{"tabs":[{"id":"a",`), mode: fileMode},
		{name: "zero length", data: []byte{}, mode: fileMode},
		{name: "over the decode bound", data: oversized, mode: fileMode},
		{name: "not an object", data: []byte(`["a"]`), mode: fileMode},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeRaw(t, dir, tc.data, tc.mode)

			s, err := NewStore(dir)
			if err == nil {
				t.Error("NewStore over an unreadable arrangement = nil error, want a diagnostic")
			}
			asides := asidesIn(t, dir)
			if len(asides) != 1 {
				t.Fatalf("asides after NewStore = %v, want exactly one", asides)
			}
			if s.Unread() != asides[0] {
				t.Errorf("Unread() = %q, want the aside %q", s.Unread(), asides[0])
			}
			if got := readFile(t, asides[0]); !bytes.Equal(got, tc.data) {
				t.Errorf("the aside holds %d bytes, want the %d unread bytes unchanged", len(got), len(tc.data))
			}
			if _, _, _, err := s.Open(t.Context(), chatSpec("c-1")); err != nil {
				t.Errorf("Open after the move aside = %v, want the store to write", err)
			}
			if got := readFile(t, asides[0]); !bytes.Equal(got, tc.data) {
				t.Error("a write after the move aside changed the aside")
			}
		})
	}
}

func TestNewStore_AReadArrangementIsNotUnread(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, file{Version: 1, Tabs: []marotte.TabSubject{{ID: "a", Kind: marotte.TabKindChat, Ref: "c-1"}}})
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if s.Unread() != "" {
		t.Errorf("Unread() = %q over a readable arrangement, want empty", s.Unread())
	}
}

func TestNewStore_AnAsideKeepsTheNextBootUnread(t *testing.T) {
	dir := t.TempDir()
	writeRaw(t, dir, []byte("nonsense"), fileMode)
	first, _ := NewStore(dir)
	aside := first.Unread()
	if _, _, _, err := first.Open(t.Context(), chatSpec("c-1")); err != nil {
		t.Fatalf("Setup: Open: %v", err)
	}

	second, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore after a clean write: %v", err)
	}
	if second.Unread() != aside {
		t.Errorf("Unread() on the next boot = %q, want the aside %q: a tab write must not clear it", second.Unread(), aside)
	}
	if tabs, _ := second.List(); len(tabs) != 1 {
		t.Errorf("the next boot loaded %d tabs, want the one written after the aside", len(tabs))
	}
}

func TestNewStore_ASecondUnreadArrangementKeepsTheFirstAside(t *testing.T) {
	dir := t.TempDir()
	writeRaw(t, dir, []byte("first"), fileMode)
	if _, err := NewStore(dir); err == nil {
		t.Fatal("Setup: the first garbage arrangement was read")
	}
	writeRaw(t, dir, []byte("second"), fileMode)
	if _, err := NewStore(dir); err == nil {
		t.Fatal("Setup: the second garbage arrangement was read")
	}

	asides := asidesIn(t, dir)
	if len(asides) != 2 {
		t.Fatalf("asides = %v, want two", asides)
	}
	got := []string{string(readFile(t, asides[0])), string(readFile(t, asides[1]))}
	if got[0] != "first" || got[1] != "second" {
		t.Errorf("asides hold %q, want [first second]", got)
	}
}

func TestNewStore_RefusesToWriteOverBytesItCouldNotMoveAside(t *testing.T) {
	restore := moveAside
	moveAside = func(string) (string, error) { return "", errors.New("rename: read-only file system") }
	t.Cleanup(func() { moveAside = restore })
	dir := t.TempDir()
	writeRaw(t, dir, []byte("nonsense"), fileMode)

	s, err := NewStore(dir)
	if err == nil {
		t.Error("NewStore with the move aside failing = nil error, want a diagnostic")
	}
	if want := filepath.Join(dir, fileName); s.Unread() != want {
		t.Errorf("Unread() = %q, want %q", s.Unread(), want)
	}
	if _, _, _, err := s.Open(t.Context(), chatSpec("c-1")); !errors.Is(err, errWriteRefused) {
		t.Errorf("Open = %v, want errWriteRefused", err)
	}
	if got := readFile(t, filepath.Join(dir, fileName)); string(got) != "nonsense" {
		t.Errorf("tabs.json = %q after a refused write, want the unread bytes", got)
	}
}

func TestNewStore_AListingFailureRefusesWrites(t *testing.T) {
	restore := findAside
	findAside = func(string, string) (string, error) { return "", errors.New("readdir: input/output error") }
	t.Cleanup(func() { findAside = restore })
	dir := t.TempDir()
	writeDoc(t, dir, file{Version: 1, Tabs: []marotte.TabSubject{{ID: "a", Kind: marotte.TabKindChat, Ref: "c-1"}}})

	s, err := NewStore(dir)
	if err == nil || !strings.Contains(err.Error(), "input/output error") {
		t.Errorf("NewStore with the listing failing = %v, want a diagnostic naming it", err)
	}
	if s.Unread() != dir {
		t.Errorf("Unread() = %q, want the directory %q", s.Unread(), dir)
	}
	if _, _, _, err := s.Open(t.Context(), chatSpec("c-2")); !errors.Is(err, errWriteRefused) {
		t.Errorf("Open = %v, want errWriteRefused", err)
	}
}
