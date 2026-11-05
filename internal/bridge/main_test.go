package bridge

import (
	"fmt"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// TestMain lowers the soft RLIMIT_CORE to 1 for the fixture processes the tests start: a fixture killed by SIGSEGV
// is not waitable until the host's core handler has its dump, seconds on a loaded host. A pipe core_pattern ignores
// every other limit, 0 included (Linux fs/coredump.c, "RLIMIT_CORE is set to 1, aborting core").
func TestMain(m *testing.M) {
	var lim unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_CORE, &lim); err != nil {
		fmt.Fprintln(os.Stderr, "TestMain: read RLIMIT_CORE:", err)
		os.Exit(2)
	}
	lim.Cur = min(1, lim.Max)
	if err := unix.Setrlimit(unix.RLIMIT_CORE, &lim); err != nil {
		fmt.Fprintln(os.Stderr, "TestMain: set RLIMIT_CORE:", err)
		os.Exit(2)
	}
	os.Exit(m.Run())
}
