package bridge_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// A live test's start of the pinned kiro-cli prunes every other version's complete entry in KIRO_RUNTIME_DIR
// (kiro-cli/runtime.md), so the guard names exactly those: not the pin's own entry, an incomplete one or a lock file.
func TestForeignRuntimes_NamesTheEntriesAStartWouldPrune(t *testing.T) {
	dir := t.TempDir()
	for name, complete := range map[string]bool{
		"3.2.0-7528c91717f5c427": true,
		"3.2.1-bdffd438adfe2cee": true,
		"3.1.0-0123456789abcdef": false,
		"scratch":                true,
	} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if complete {
			if err := os.WriteFile(filepath.Join(dir, name, ".complete"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "3.2.0-7528c91717f5c427.lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if got, want := foreignRuntimes(dir, "3.2.1"), []string{"3.2.0-7528c91717f5c427"}; !slices.Equal(got, want) {
		t.Errorf("foreignRuntimes = %v, want %v", got, want)
	}
	if got := foreignRuntimes(filepath.Join(dir, "absent"), "3.2.1"); len(got) != 0 {
		t.Errorf("foreignRuntimes of a missing folder = %v, want none", got)
	}
}
