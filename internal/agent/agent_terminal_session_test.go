package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/testsupport/testchild"
)

// procIDs reads pid's process group and session from /proc/<pid>/stat (fields 5 and 6, after the
// last ')' because comm may hold parens).
func procIDs(t *testing.T, pid int) (pgrp, sid int) {
	t.Helper()
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)) // #nosec G304 -- pid of the test's own tree
	if err != nil {
		t.Fatalf("read /proc/%d/stat: %v", pid, err)
	}
	s := string(b)
	f := strings.Fields(s[strings.LastIndexByte(s, ')')+1:])
	if len(f) < 4 {
		t.Fatalf("/proc/%d/stat has %d fields after comm, want at least 4", pid, len(f))
	}
	pgrp, errG := strconv.Atoi(f[2])
	sid, errS := strconv.Atoi(f[3])
	if errG != nil || errS != nil {
		t.Fatalf("/proc/%d/stat: pgrp %q, session %q", pid, f[2], f[3])
	}
	return pgrp, sid
}

// An id read inside an agent command must name only that command's tree, so the shell leads its
// own session and group, distinct from its host's.
func TestAgentTerminal_LeadsItsOwnSession(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "head.pid")
	h, termID, pid := startBaitTerminal(t, "echo $$ > "+pidFile+"; sleep 300", pidFile)
	t.Cleanup(func() { h.translateACPEvent("c1", h.originOf("c1"), termIDMsg(t, 99, methodTermKill, termID)) })

	pgrp, sid := procIDs(t, pid)
	ownPgrp, ownSid := procIDs(t, os.Getpid())
	if sid != pid {
		t.Errorf("agent shell %d is in session %d, want its own session %d", pid, sid, pid)
	}
	if pgrp != pid {
		t.Errorf("agent shell %d is in process group %d, want its own group %d", pid, pgrp, pid)
	}
	if sid == ownSid {
		t.Errorf("agent shell shares its host's session %d: `kill -- -$(ps -o sid= -p $$)` inside it signals the host", sid)
	}
	if pgrp == ownPgrp {
		t.Errorf("agent shell shares its host's process group %d", pgrp)
	}
}

const incidentHarnessEnv = "MAROTTE_INCIDENT_HARNESS"

// incidentScript is the loop that took marotte down (pgrep -f of its own text, ps sid, kill --
// -sid) with every kill behind a guard: it refuses init, the runner's session and group, and any
// id but the harness's session and the shell's own, the only two this test's tree can hold.
func incidentScript(token string) string {
	return `guard() {
  t=$1
  case $t in ''|*[!0-9]*) return ;; esac
  case $t in 0|1|"$RUNNER_SID"|"$RUNNER_PGID") echo "REFUSED $t" >>"$LOG"; return ;; esac
  if [ "$t" != "$HARNESS_SID" ] && [ "$t" != "$$" ]; then echo "REFUSED $t" >>"$LOG"; return; fi
  echo "KILL $t" >>"$LOG"
  kill -- -"$t"
}
# marotte-incident-` + token + `
for s in $(pgrep -f "marotte-incident-` + token + `"); do sid=$(ps -o sid= -p $s | tr -d ' '); guard "$sid"; done
`
}

// killHarnessGroup SIGKILLs the harness's group, refusing any id the runner itself could hold.
func killHarnessGroup(harness, runnerPgrp, runnerSid int) error {
	if harness <= 1 || harness == runnerPgrp || harness == runnerSid {
		return fmt.Errorf("refusing to signal group %d (runner group %d, session %d)", harness, runnerPgrp, runnerSid)
	}
	return syscall.Kill(-harness, syscall.SIGKILL)
}

var harnessMarker = regexp.MustCompile(`HARNESS_ALIVE kills=(\d+) refused=(\d+)`)

// The incident, replayed: the loop runs inside an agent terminal spawned by a harness process that
// stands in for marotte in a session of its own, so a regression kills the harness, never the runner.
func TestAgentTerminal_IncidentLoopLeavesItsHostAlive(t *testing.T) {
	for _, bin := range []string{"bash", "pgrep", "ps"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not on PATH; the incident loop needs it", bin)
		}
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatalf("Setup: token: %v", err)
	}
	token := hex.EncodeToString(raw[:])
	runnerPgrp, runnerSid := procIDs(t, os.Getpid())

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	// The token travels in env only: in argv, pgrep -f would match the harness too.
	child := exec.CommandContext(ctx, os.Args[0], // #nosec G204 -- the test binary itself
		"-test.run=^TestAgentTerminalIncidentHarness$", "-test.count=1", "-test.v")
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	child.Env = testchild.Env(
		incidentHarnessEnv+"=1",
		"INCIDENT_TOKEN="+token,
		"RUNNER_SID="+strconv.Itoa(runnerSid),
		"RUNNER_PGID="+strconv.Itoa(runnerPgrp),
	)
	var out bytes.Buffer
	child.Stdout, child.Stderr = &out, &out
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatalf("Setup: stdin pipe: %v", err)
	}
	child.Cancel = func() error { return killHarnessGroup(child.Process.Pid, runnerPgrp, runnerSid) }
	if err := child.Start(); err != nil {
		t.Fatalf("Setup: start harness: %v", err)
	}
	harness := child.Process.Pid

	// The harness blocks on stdin until its isolation is proven, so the loop never runs in the runner's session.
	hPgrp, hSid := procIDs(t, harness)
	if hSid != harness || hPgrp != harness || hSid <= 1 || hSid == runnerSid || hPgrp == runnerPgrp {
		_ = child.Process.Kill()
		_ = child.Wait()
		t.Fatalf("Setup: harness %d has session %d and group %d (runner: session %d, group %d); refusing to run the loop",
			harness, hSid, hPgrp, runnerSid, runnerPgrp)
	}
	t.Logf("runner session %d group %d; harness session %d group %d", runnerSid, runnerPgrp, hSid, hPgrp)
	_, _ = io.WriteString(stdin, "go\n")
	_ = stdin.Close()

	waitErr := child.Wait()
	_ = killHarnessGroup(harness, runnerPgrp, runnerSid)
	if ws, ok := child.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		t.Fatalf("the incident loop's kill reached the marotte stand-in (%v): the agent terminal shares its host's session\n%s",
			ws.Signal(), out.String())
	}
	if waitErr != nil {
		t.Fatalf("harness: %v\n%s", waitErr, out.String())
	}
	m := harnessMarker.FindStringSubmatch(out.String())
	if m == nil {
		t.Fatalf("harness printed no HARNESS_ALIVE line\n%s", out.String())
	}
	if m[2] != "0" {
		t.Fatalf("the loop's guard refused %s target(s): the loop read an id outside its own tree\n%s", m[2], out.String())
	}
	if m[1] == "0" {
		t.Fatalf("the loop matched nothing; proves nothing\n%s", out.String())
	}
}

// TestAgentTerminalIncidentHarness is TestAgentTerminal_IncidentLoopLeavesItsHostAlive's child and
// skips when run on its own.
func TestAgentTerminalIncidentHarness(t *testing.T) {
	if os.Getenv(incidentHarnessEnv) != "1" {
		t.Skip("runs only as the child of TestAgentTerminal_IncidentLoopLeavesItsHostAlive")
	}
	token := os.Getenv("INCIDENT_TOKEN")
	if len(token) != 16 {
		t.Fatalf("INCIDENT_TOKEN = %q, want 16 hex characters", token)
	}
	if line, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil || line != "go\n" {
		t.Fatalf("no go-ahead from the runner (%q, %v)", line, err)
	}
	logPath := filepath.Join(t.TempDir(), "kills.log")
	h := hubWithBridge(t, t.TempDir(), newRecordingTermBridge())
	env := []map[string]string{
		{"name": "HARNESS_SID", "value": strconv.Itoa(os.Getpid())},
		{"name": "LOG", "value": logPath},
	}
	h.translateACPEvent("c1", h.originOf("c1"), termCreateMsg(t, 1, "bash", []string{"-c", incidentScript(token)}, env))
	waitClosed(t, singleTerm(t, h).done, "the incident terminal")

	b, err := os.ReadFile(logPath) // #nosec G304 -- t.TempDir path
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read kill log: %v", err)
	}
	t.Logf("kill log:\n%s", b)
	fmt.Printf("HARNESS_ALIVE kills=%d refused=%d\n",
		strings.Count(string(b), "KILL "), strings.Count(string(b), "REFUSED "))
}

// Teardown takes the whole tree, including a grandchild that left the group: GNU timeout calls
// setpgid unless --foreground is given.
func TestKillTerminalSession_ReapsGrandchildrenInOtherGroups(t *testing.T) {
	if _, err := exec.LookPath("timeout"); err != nil {
		t.Skip("timeout is not on PATH")
	}
	dir := t.TempDir()
	sameGroup := filepath.Join(dir, "a.pid")
	underTimeout := filepath.Join(dir, "b.pid")
	timeoutPID := filepath.Join(dir, "c.pid")
	script := "sleep 300 & echo $! > " + sameGroup +
		"; timeout 300 sh -c 'echo $$ > " + underTimeout + "; exec sleep 300' & echo $! > " + timeoutPID + "; wait"
	h, termID, c := startBaitTerminal(t, script, timeoutPID)
	pids := []int{waitForPIDFile(t, sameGroup), waitForPIDFile(t, underTimeout), c}
	names := []string{"the shell's background sleep", "timeout's child", "timeout"}
	t.Cleanup(func() {
		for _, pid := range pids {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	for i, pid := range pids {
		if !processAlive(pid) {
			t.Fatalf("Setup: %s (%d) not alive before the kill; the test proves nothing", names[i], pid)
		}
	}
	if pg, _ := procIDs(t, c); pg != c {
		t.Fatalf("Setup: timeout %d is in group %d, not its own; the test proves nothing", c, pg)
	}

	h.translateACPEvent("c1", h.originOf("c1"), termIDMsg(t, 2, methodTermKill, termID))

	deadline := time.Now().Add(3 * time.Second)
	for i, pid := range pids {
		for processAlive(pid) {
			if time.Now().After(deadline) {
				t.Fatalf("%s (%d) survived terminal/kill", names[i], pid)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}
