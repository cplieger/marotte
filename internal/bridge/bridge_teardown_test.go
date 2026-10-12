package bridge

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/procgroup"
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

// outsideChildScript is a head that starts a TERM-ignoring setsid child, the shape of kiro-cli's
// engine group, writes the child's pid to pidFile, and runs until release exists.
func outsideChildScript(t *testing.T, pidFile, release string) string {
	t.Helper()
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid is not on PATH")
	}
	return `setsid sh -c 'echo $$ > "` + pidFile + `.tmp"; mv "` + pidFile + `.tmp" "` + pidFile + `"; trap "" TERM; while :; do sleep 0.05; done' &
while [ ! -e "` + release + `" ]; do sleep 0.05; done`
}

// The relay-crash shape: the head exits first, so its outside child is init's by the time Stop
// runs and only the group initialize recorded can reach it.
func TestStop_SweepsTheEngineGroupAfterTheHeadExits(t *testing.T) {
	dir := t.TempDir()
	pidFile, release := filepath.Join(dir, "outside.pid"), filepath.Join(dir, "release")
	b := startTeardownFixture(t, outsideChildScript(t, pidFile, release))
	outside := waitForBridgePID(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(-outside, syscall.SIGKILL) })
	resp := &marotte.RPCResponse{Result: json.RawMessage(`{"agentCapabilities":{}}`)}
	if _, err := driveSessionCall(t, b, resp, b.initialize); err != nil {
		t.Fatalf("Setup: initialize = %v, want nil", err)
	}

	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatalf("Setup: release: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for processAlive(b.cmd.Process.Pid) {
		if !time.Now().Before(deadline) {
			t.Fatal("Setup: the head never exited")
		}
		time.Sleep(10 * time.Millisecond)
	}

	b.Stop()

	if processAlive(outside) {
		t.Errorf("Stop() left the engine-group survivor (pid %d) alive after its head exited", outside)
	}
}

// With the head alive at Stop, the reap-time refresh finds the outside group itself. Not
// parallel: it swaps the slog default.
func TestStop_EscalatesSweepOfAnOutsideGroupAndLogsIt(t *testing.T) {
	logs := capture.Default(t)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "outside.pid")
	b := startTeardownFixture(t, outsideChildScript(t, pidFile, filepath.Join(dir, "never")))
	outside := waitForBridgePID(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(-outside, syscall.SIGKILL) })

	b.Stop()

	if processAlive(outside) {
		t.Errorf("Stop() left the TERM-ignoring outside group (pid %d) alive", outside)
	}
	if n := logs.CountExact("killed MCP/engine survivors after the bridge ended"); n != 1 {
		t.Errorf("survivor-kill Warn logged %d times, want 1", n)
	}
}

// The abrupt-death shape: the engine leader exited and was reaped while an MCP server still holds
// its group, so no process sits at the noted id and the sweep must still kill the member. Not
// parallel: it swaps the slog default.
func TestSweepEngineGroups_KillsAGroupWhoseLeaderWasReaped(t *testing.T) {
	logs := capture.Default(t)
	leader := exec.Command("sleep", "60")
	leader.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := leader.Start(); err != nil {
		t.Fatalf("Setup: start leader: %v", err)
	}
	t.Cleanup(func() { _ = leader.Process.Kill(); _ = leader.Wait() })
	pgid := leader.Process.Pid
	member := exec.Command("sleep", "60")
	member.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: pgid}
	if err := member.Start(); err != nil {
		t.Fatalf("Setup: start member: %v", err)
	}
	t.Cleanup(func() { _ = member.Process.Kill(); _ = member.Wait() })
	if got, err := syscall.Getpgid(member.Process.Pid); err != nil || got != pgid {
		t.Fatalf("Setup: member pgid = %d, %v; want the leader's group %d", got, err, pgid)
	}
	b := New("cli", t.TempDir())
	for _, g := range procgroup.GroupsBelow(os.Getpid()) {
		if g.ID == pgid {
			b.engineGroups = []procgroup.Group{g}
		}
	}
	if len(b.engineGroups) != 1 {
		t.Fatalf("Setup: GroupsBelow(test) did not report the leader's group %d", pgid)
	}
	if err := leader.Process.Kill(); err != nil {
		t.Fatalf("Setup: kill leader: %v", err)
	}
	_ = leader.Wait()
	if processAlive(pgid) {
		t.Fatalf("Setup: a process still sits at the reaped leader's pid %d", pgid)
	}

	b.sweepEngineGroups("s1")

	if processAlive(member.Process.Pid) {
		t.Errorf("sweep left the reaped leader's group member (pid %d) alive", member.Process.Pid)
	}
	if n := logs.CountExact("killed MCP/engine survivors after the bridge ended"); n != 1 {
		t.Errorf("survivor-kill Warn logged %d times, want 1", n)
	}
}

// An id found again at reap takes the fresh stamp, so a group the initialize-time stamp no longer
// matches is still swept. Not parallel: it swaps the slog default.
func TestStop_RenotingAGroupReplacesItsStamp(t *testing.T) {
	capture.Default(t)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "outside.pid")
	b := startTeardownFixture(t, outsideChildScript(t, pidFile, filepath.Join(dir, "never")))
	outside := waitForBridgePID(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(-outside, syscall.SIGKILL) })
	// A zero stamp never matches the live leader's start time.
	b.engineGroups = []procgroup.Group{{ID: outside}}

	b.Stop()

	if processAlive(outside) {
		t.Errorf("Stop() left the re-noted outside group (pid %d) alive; the old stamp marked it stale", outside)
	}
}

// A noted group whose id a later group now holds is skipped at once: no grace spent on it, no
// signal and no kill Error. Not parallel: it swaps the slog default.
func TestSweepEngineGroups_SkipsAnIdALaterGroupHolds(t *testing.T) {
	logs := capture.Default(t)
	later := exec.Command("sleep", "30")
	later.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := later.Start(); err != nil {
		t.Fatalf("Setup: start the later group: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-later.Process.Pid, syscall.SIGKILL); _ = later.Wait() })
	b := New("cli", t.TempDir())
	// A zero leader stamp never matches the live leader's start time.
	b.engineGroups = []procgroup.Group{{ID: later.Process.Pid}}

	start := time.Now()
	b.sweepEngineGroups("s1")

	if elapsed := time.Since(start); elapsed >= bridgeGroupGrace {
		t.Errorf("sweep took %v on a reused id, want under the %v grace", elapsed, bridgeGroupGrace)
	}
	if !processAlive(later.Process.Pid) {
		t.Errorf("sweep killed the later group's leader (pid %d)", later.Process.Pid)
	}
	if n := logs.CountExact("kill kiro-cli engine group"); n != 0 {
		t.Errorf("kill Error logged %d times for a reused id, want 0", n)
	}
}
