package powers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManager_TheCLIInheritsTheEnvironmentUnderTheOverlay(t *testing.T) {
	t.Setenv("MAROTTE_POWERS_INHERITED", "parent")
	t.Setenv("MAROTTE_POWERS_OVERLAID", "parent")
	dir := t.TempDir()
	out := filepath.Join(dir, "env")
	cli := filepath.Join(dir, "kiro-cli")
	writeFile(t, cli, `#!/bin/sh
echo "$MAROTTE_POWERS_INHERITED $MAROTTE_POWERS_OVERLAID" > "`+out+`"
`, 0o755)
	m := NewManager(nil, &recordingWriter{}, func() string { return cli },
		func() []string { return []string{"MAROTTE_POWERS_OVERLAID=overlay"} },
		filepath.Join(dir, "installed"))

	if err := m.Uninstall(t.Context(), "postman", fixedMode(ServersActive)); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("the fake kiro-cli did not run: %v", err)
	}
	if strings.TrimSpace(string(got)) != "parent overlay" {
		t.Errorf("kiro-cli saw %q, want the inherited value then the overlay's: parent overlay", got)
	}
}
