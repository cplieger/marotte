//go:build marotte_test

package git

import (
	"os"
	"path/filepath"
)

// The performance harness counts fetches through a logging git (tests/perf/instance/git), which
// systembin never resolves: it lives outside /usr/bin and /bin.
func init() {
	if shim := os.Getenv("MAROTTE_TEST_GIT"); filepath.IsAbs(shim) {
		resolveGitBinary = func() (string, bool) { return shim, true }
	}
}
