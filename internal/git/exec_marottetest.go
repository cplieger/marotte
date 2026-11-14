//go:build marotte_test

package git

import (
	"os"
	"path/filepath"
)

func init() { useTestGitFromEnv() }

// useTestGitFromEnv points resolveGitBinary at an absolute MAROTTE_TEST_GIT: the performance
// harness counts fetches through a logging git (tests/perf/instance/git), which systembin never
// resolves because it lives outside /usr/bin and /bin.
func useTestGitFromEnv() {
	if shim := os.Getenv("MAROTTE_TEST_GIT"); filepath.IsAbs(shim) {
		resolveGitBinary = func() (string, bool) { return shim, true }
	}
}
