package testsupport

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// EmptyEnv empties the process environment for the rest of t, as `env -i` does for a child, and restores it at
// cleanup. t.Setenv refuses a parallel t, and a serial test never overlaps a parallel one, so no other test sees the
// empty environment.
func EmptyEnv(t *testing.T) {
	t.Helper()
	// t.Setenv registers each variable's restore, then the environment is emptied under it.
	for _, kv := range os.Environ() {
		name, value, _ := strings.Cut(kv, "=")
		t.Setenv(name, value)
	}
	os.Clearenv()
}

// EnvRecorder writes an executable that records the value of the variable name each run sees, and returns its path
// and a reader of the last run's value: "unset" when the variable was absent, "never ran" before the first run. It
// needs no PATH, so it runs under EmptyEnv.
func EnvRecorder(t *testing.T, name string) (path string, seen func() string) {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "seen")
	path = filepath.Join(dir, "kiro-cli")
	script := "#!/bin/sh\nprintf '%s' \"${" + name + "-unset}\" > '" + out + "'\n"
	// Under ForkLock, so a concurrent fork cannot hold the file open for writing when it is executed (go.dev/issue/22315).
	syscall.ForkLock.RLock()
	err := os.WriteFile(path, []byte(script), 0o700) // #nosec G306 -- a fake kiro-cli must be executable
	syscall.ForkLock.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	return path, func() string {
		b, err := os.ReadFile(out)
		if err != nil {
			return "never ran"
		}
		return string(b)
	}
}
