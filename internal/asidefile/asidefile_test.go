package asidefile

import (
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
