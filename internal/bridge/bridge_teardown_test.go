package bridge

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/slogx/capture"
)

// startTeardownFixture runs script as the bridge's process, in its own group as startProcess spawns kiro-cli.
func startTeardownFixture(t *testing.T, script string) *Bridge {
	t.Helper()
	b := New("cli", t.TempDir())
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fixture: %v", err)
	}
	b.cmd = cmd
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	return b
}

func setTeardownGrace(t *testing.T, d time.Duration) {
	t.Helper()
	old := bridgeTeardownGrace
	bridgeTeardownGrace = d
	t.Cleanup(func() { bridgeTeardownGrace = old })
}

// KAS runs SessionEnd hooks in its SIGTERM handler, so Stop must send a trappable signal and wait. Not parallel: it
// reassigns bridgeTeardownGrace.
func TestStop_GivesTheTreeItsSignalTeardown(t *testing.T) {
	setTeardownGrace(t, 5*time.Second)
	marker := filepath.Join(t.TempDir(), "teardown-ran")
	ready := filepath.Join(t.TempDir(), "trap-armed")
	b := startTeardownFixture(t, `trap 'sleep 0.3; : > "`+marker+`"; exit 0' TERM; : > "`+ready+`"; while :; do sleep 0.05; done`)
	waitForFile(t, ready)

	b.Stop()

	if _, err := os.Stat(marker); err != nil {
		t.Errorf("Stop() returned before the tree's SIGTERM handler finished (marker %s: %v)", marker, err)
	}
}

// A tree ignoring SIGTERM is killed after the grace, logged as an escalation. Not parallel: it reassigns
// bridgeTeardownGrace and swaps the slog default.
func TestStop_EscalatesWhenTheTreeIgnoresTerm(t *testing.T) {
	setTeardownGrace(t, 200*time.Millisecond)
	logs := capture.Default(t)
	ready := filepath.Join(t.TempDir(), "trap-armed")
	b := startTeardownFixture(t, `trap '' TERM; : > "`+ready+`"; while :; do sleep 0.05; done`)
	waitForFile(t, ready)
	pid := b.cmd.Process.Pid

	start := time.Now()
	b.Stop()
	elapsed := time.Since(start)

	if limit := bridgeTeardownGrace + bridgeGroupGrace + 2*time.Second; elapsed > limit {
		t.Errorf("Stop() took %v on a TERM-ignoring tree, want under %v", elapsed, limit)
	}
	if processAlive(pid) {
		t.Errorf("Stop() returned with the TERM-ignoring head (pid %d) still alive", pid)
	}
	if logs.CountExact("kiro-cli did not exit within the teardown grace; killed") != 1 {
		t.Errorf("Stop() escalated without logging the teardown-grace Warn")
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("fixture never wrote %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
