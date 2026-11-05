package asidefile

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestFind_NamesOnlyAnAsideOfTheGivenFile(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"runs.json.unreadable-1", "tabs.json", "tabs.json.bak"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatalf("Setup: %v", err)
		}
	}
	if got, err := Find(dir, "tabs.json"); err != nil || got != "" {
		t.Errorf("Find(tabs.json) = %q, %v; want none: runs.json's aside and tabs.json.bak are not tabs.json's", got, err)
	}

	moved, err := Move(filepath.Join(dir, "tabs.json"))
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if got, err := Find(dir, "tabs.json"); err != nil || got != moved {
		t.Errorf("Find(tabs.json) after Move = %q, %v; want %q", got, err, moved)
	}
}

func TestFind_AnAbsentDirectoryHoldsNone(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	for name, dir := range map[string]string{
		"missing":           filepath.Join(t.TempDir(), "gone"),
		"a file, not a dir": file,
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := Find(dir, "tabs.json"); err != nil || got != "" {
				t.Errorf("Find = %q, %v; want none and no error", got, err)
			}
		})
	}
}

func TestCopy_KeepsTheFileAndWritesAnAsideFindSees(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "entries.jsonl")
	want := []byte("one\ntwo\n")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	aside, err := Copy(path, 0o600)
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, want) {
		t.Errorf("the original holds %q, want it unchanged %q", got, want)
	}
	if got, _ := os.ReadFile(aside); !bytes.Equal(got, want) {
		t.Errorf("the aside holds %q, want %q", got, want)
	}
	if got, err := Find(dir, "entries.jsonl"); err != nil || got != aside {
		t.Errorf("Find = %q, %v; want %q", got, err, aside)
	}
}

func TestCopy_AMissingFileLeavesNoAside(t *testing.T) {
	dir := t.TempDir()
	if _, err := Copy(filepath.Join(dir, "entries.jsonl"), 0o600); !os.IsNotExist(err) {
		t.Errorf("Copy of a missing file = %v, want not-exist", err)
	}
	if got, _ := Find(dir, "entries.jsonl"); got != "" {
		t.Errorf("a failed Copy left the aside %q", got)
	}
}
