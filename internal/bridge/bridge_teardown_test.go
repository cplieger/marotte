package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/go/slogx/capture"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/procgroup"
)

// startTeardownFixture runs script as the engine's process, in its own group as startProcess spawns kiro-cli.
func startTeardownFixture(t *testing.T, script string) *Engine {
	t.Helper()
	b := NewEngine("cli", t.TempDir())
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
	return outsideChild(t, pidFile) + `while [ ! -e "` + release + `" ]; do sleep 0.05; done`
}

// outsideChild is outsideChildScript's spawn line alone, for a head with its own ending.
func outsideChild(t *testing.T, pidFile string) string {
	t.Helper()
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid is not on PATH")
	}
	return `setsid sh -c 'echo $$ > "` + pidFile + `.tmp"; mv "` + pidFile + `.tmp" "` + pidFile + `"; trap "" TERM; while :; do sleep 0.05; done' &
`
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
	if _, err := driveSessionCall(t, b, resp, false, b.initialize); err != nil {
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

// With the head alive at Stop, the teardown refresh finds the outside group itself. Not
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
	if n := logs.CountExact("killed MCP and engine survivors after the engine ended"); n != 1 {
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
	b := NewEngine("cli", t.TempDir())
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

	b.sweepEngineGroups()

	if processAlive(member.Process.Pid) {
		t.Errorf("sweep left the reaped leader's group member (pid %d) alive", member.Process.Pid)
	}
	if n := logs.CountExact("killed MCP and engine survivors after the engine ended"); n != 1 {
		t.Errorf("survivor-kill Warn logged %d times, want 1", n)
	}
}

// An id found again in teardown takes the fresh stamp, so a group the initialize-time stamp no longer
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
	b := NewEngine("cli", t.TempDir())
	// A zero leader stamp never matches the live leader's start time.
	b.engineGroups = []procgroup.Group{{ID: later.Process.Pid}}

	start := time.Now()
	b.sweepEngineGroups()

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

// startEOFFixture is startTeardownFixture with a real stdin pipe published, as startProcess publishes kiro-cli's.
func startEOFFixture(t *testing.T, script string) *Engine {
	t.Helper()
	b := NewEngine("cli", t.TempDir())
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	w, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fixture: %v", err)
	}
	b.cmd = cmd
	b.stdin.Store(&stdinPipe{w: w})
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	return b
}

func setStdinEOFGrace(t *testing.T, d time.Duration) {
	t.Helper()
	old := stdinEOFGrace
	stdinEOFGrace = d
	t.Cleanup(func() { stdinEOFGrace = old })
}

// kiro-cli ends on stdin EOF, and KAS's teardown runs before it does, so Stop must not signal a process still
// finishing that exit.
func TestStop_LetsTheEngineExitOnStdinEOF(t *testing.T) {
	dir := t.TempDir()
	termed, done, ready := filepath.Join(dir, "termed"), filepath.Join(dir, "done"), filepath.Join(dir, "ready")
	b := startEOFFixture(t, `trap ': > "`+termed+`"; exit 0' TERM; : > "`+ready+`"; cat >/dev/null; sleep 0.3; : > "`+done+`"; exit 1`)
	waitForFile(t, ready)

	b.Stop()

	if _, err := os.Stat(done); err != nil {
		t.Errorf("Stop() returned before the process finished its EOF exit (marker %s: %v)", done, err)
	}
	if _, err := os.Stat(termed); err == nil {
		t.Errorf("Stop() signalled a process that was exiting on stdin EOF")
	}
	exit := b.Exit()
	if exit.Reason != ExitStopped {
		t.Errorf("Exit().Reason = %q, want %q", exit.Reason, ExitStopped)
	}
	if exit.SidecarExit != "exit_code=1" {
		t.Errorf("Exit().SidecarExit = %q, want %q", exit.SidecarExit, "exit_code=1")
	}
}

// A process that ignores EOF is signalled once the EOF grace has passed, and its SIGTERM exit is no escalation. Not
// parallel: it reassigns both graces and swaps the slog default.
func TestStop_SignalsOnlyAfterTheEOFGrace(t *testing.T) {
	setStdinEOFGrace(t, 300*time.Millisecond)
	setTeardownGrace(t, 2*time.Second)
	logs := capture.Default(t)
	dir := t.TempDir()
	stamp, ready := filepath.Join(dir, "termed"), filepath.Join(dir, "ready")
	b := startEOFFixture(t, `trap ': > "`+stamp+`"; exit 0' TERM; : > "`+ready+`"; while :; do sleep 0.05; done`)
	waitForFile(t, ready)

	start := time.Now()
	b.Stop()

	info, err := os.Stat(stamp)
	if err != nil {
		t.Fatalf("the EOF-ignoring process was never signalled: %v", err)
	}
	if after := info.ModTime().Sub(start); after < 300*time.Millisecond {
		t.Errorf("SIGTERM landed %v after Stop began, want at least the 300ms EOF grace", after)
	}
	if n := logs.CountExact("kiro-cli did not exit within the teardown grace; killed"); n != 0 {
		t.Errorf("teardown-grace Warn logged %d times for a process that exited on SIGTERM, want 0", n)
	}
}

// A group born after initialize is found only by teardown's refresh, which must run before the stdin close: on EOF
// the head exits, its setsid child becomes init's and no later walk finds it. Not parallel: it swaps the slog default.
func TestStop_SweepsAGroupBornAfterInitializeOnAnEOFExit(t *testing.T) {
	logs := capture.Default(t)
	pidFile := filepath.Join(t.TempDir(), "outside.pid")
	b := startEOFFixture(t, outsideChild(t, pidFile)+`cat >/dev/null; exit 0`)
	outside := waitForBridgePID(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(-outside, syscall.SIGKILL) })

	b.Stop()

	if processAlive(outside) {
		t.Errorf("Stop() left the group born after initialize (pid %d) alive after the EOF exit", outside)
	}
	if n := logs.CountExact("killed MCP and engine survivors after the engine ended"); n != 1 {
		t.Errorf("survivor-kill Warn logged %d times, want 1", n)
	}
}

// A frame sent while the engine finishes its EOF exit is refused, not written to the closed pipe and logged as a
// failed write. Not parallel: it swaps the slog default.
func TestStop_FencesAWriteInTheEOFWindow(t *testing.T) {
	logs := capture.Default(t)
	dir := t.TempDir()
	eof, ready := filepath.Join(dir, "eof"), filepath.Join(dir, "ready")
	b := startEOFFixture(t, `: > "`+ready+`"; cat >/dev/null; : > "`+eof+`"; sleep 1; exit 0`)
	waitForFile(t, ready)
	stopped := make(chan struct{})
	go func() { b.Stop(); close(stopped) }()
	waitForFile(t, eof)

	err := b.writeFrame([]byte("{}\n"))
	<-stopped

	if !errors.Is(err, errWriteFenced) {
		t.Errorf("writeFrame in the EOF window = %v, want errWriteFenced", err)
	}
	if n := logs.CountExact("ACP write failed; reaping the agent engine"); n != 0 {
		t.Errorf("write-failure Error logged %d times on a clean stop, want 0", n)
	}
}

// The lifecycle backstop runs the same staged stop, recorded as a stop marotte asked for.
func TestCancel_StopsThroughTheTeardown(t *testing.T) {
	dir := t.TempDir()
	termed, eof, ready := filepath.Join(dir, "termed"), filepath.Join(dir, "eof"), filepath.Join(dir, "ready")
	script := "#!/bin/sh\ntrap ': > \"" + termed + "\"; exit 0' TERM\n: > \"" + ready + "\"\ncat >/dev/null\n: > \"" + eof + "\"\nexit 0\n"
	scriptPath := filepath.Join(dir, "fake-kiro-cli")
	syscall.ForkLock.RLock()
	err := os.WriteFile(scriptPath, []byte(script), 0o755)
	syscall.ForkLock.RUnlock()
	if err != nil {
		t.Fatalf("Setup: write fake kiro-cli: %v", err)
	}
	// Not t.Context(): the cleanup's Stop must outlive it.
	ctx, cancel := context.WithCancel(context.Background())
	b := NewEngine(scriptPath, dir)
	b.lifecycleCtx = ctx
	if err := b.startProcess(); err != nil {
		t.Fatalf("Setup: startProcess: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		b.Stop()
	})
	waitForFile(t, ready)

	cancel()
	select {
	case <-b.done:
	case <-time.After(StopCeiling() + 5*time.Second):
		t.Fatal("the engine never ended after its lifecycle context was cancelled")
	}

	if got := b.Exit().Reason; got != ExitStopped {
		t.Errorf("Exit().Reason after a lifecycle cancel = %q, want %q", got, ExitStopped)
	}
	if _, err := os.Stat(eof); err != nil {
		t.Errorf("the fake kiro-cli never saw stdin EOF: %v", err)
	}
	if _, err := os.Stat(termed); err == nil {
		t.Errorf("the lifecycle cancel signalled a process that was exiting on stdin EOF")
	}
}
