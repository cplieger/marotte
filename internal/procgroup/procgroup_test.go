package procgroup

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestOwns pins the guard that keeps the group-kill form off marotte's own
// process group.
//
// Without Setpgid a child inherits marotte's group, so `Kill(-pgid, sig)` would
// signal marotte itself. This comparison is the only thing preventing that, and
// it cannot be pinned by actually signalling a non-leader (the signal would land
// on the test binary), which is why the decision is a pure function.
func TestOwns(t *testing.T) {
	cases := []struct {
		name string
		pid  int
		pgid int
		want bool
	}{
		{name: "own leader: Setpgid took, group is the command's tree", pid: 4242, pgid: 4242, want: true},
		{name: "inherited group: Setpgid absent, group is marotte's", pid: 4242, pgid: 17, want: false},
		{name: "inherited group where marotte is the leader", pid: 4242, pgid: 1, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := owns(tc.pid, tc.pgid); got != tc.want {
				t.Errorf("Owns(%d, %d) = %v, want %v", tc.pid, tc.pgid, got, tc.want)
			}
		})
	}
}

// TestAlreadyGone pins which errors mean the signal target was already reaped; callers gate a Warn
// on it, so a wrong member hides a real failure.
func TestAlreadyGone(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "bare ESRCH: no such process", err: syscall.ESRCH, want: true},
		{name: "bare ErrProcessDone: this program already reaped it", err: os.ErrProcessDone, want: true},
		{name: "wrapped ESRCH", err: fmt.Errorf("kill group: %w", syscall.ESRCH), want: true},
		{name: "wrapped ErrProcessDone", err: fmt.Errorf("signal head: %w", os.ErrProcessDone), want: true},
		{name: "EPERM: the process is there and we may not signal it", err: syscall.EPERM, want: false},
		{name: "EINVAL: a bad signal number, a programmer error", err: syscall.EINVAL, want: false},
		{name: "an unrelated error", err: errors.New("write |1: broken pipe"), want: false},
		{name: "no error: the kill landed on something alive", err: nil, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AlreadyGone(tc.err); got != tc.want {
				t.Errorf("AlreadyGone(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestKill_ReapedProcessIsAlreadyGone ties the predicate to the error Kill really produces after
// awaitExit has reaped an agent terminal.
func TestKill_ReapedProcessIsAlreadyGone(t *testing.T) {
	cmd := exec.Command("true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	p := cmd.Process
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}

	err := Kill(p, syscall.SIGKILL)
	if err == nil {
		t.Fatal("Kill on a reaped process = nil; the release site would log nothing and this gate would be dead code")
	}
	if !AlreadyGone(err) {
		t.Errorf("AlreadyGone(Kill(reaped)) = false for %[1]v (%[1]T); the release site would warn on every normal release", err)
	}
	if !errors.Is(err, os.ErrProcessDone) {
		t.Errorf("Kill(reaped) = %v, want os.ErrProcessDone; os.convertESRCH is what makes the ESRCH half of the predicate belong to the syscall callers instead", err)
	}
}

// TestKill_ReapsTheWholeTree: Kill reclaims a tree whose head has children that outlive it, as an
// agent terminal's build tool and `kiro-cli acp`'s re-exec do.
func TestKill_ReapsTheWholeTree(t *testing.T) {
	head := exec.Command("sh", "-c", "echo $$; exec sleep 60")
	head.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := head.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if sErr := head.Start(); sErr != nil {
		t.Fatalf("start head: %v", sErr)
	}
	t.Cleanup(func() {
		_ = Kill(head.Process, syscall.SIGKILL)
		_ = head.Wait()
	})

	buf := make([]byte, 32)
	n, rErr := out.Read(buf)
	if rErr != nil || n == 0 {
		t.Fatalf("read head pid: n=%d err=%v", n, rErr)
	}
	leader, cErr := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	if cErr != nil {
		t.Fatalf("parse head pid %q: %v", buf[:n], cErr)
	}

	// A second member of the head's group, owned by this test so it can be
	// reaped deterministically.
	member := exec.Command("sleep", "60")
	member.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: leader}
	if sErr := member.Start(); sErr != nil {
		t.Fatalf("start group member: %v", sErr)
	}
	memberDone := make(chan error, 1)
	go func() { memberDone <- member.Wait() }()

	// Without this the fixture is vacuous: a member that failed to join leaves
	// the group empty and every assertion below passes for the wrong reason.
	pgid, gErr := syscall.Getpgid(member.Process.Pid)
	if gErr != nil {
		t.Fatalf("Getpgid(member): %v", gErr)
	}
	if pgid != leader {
		t.Fatalf("member pgid = %d, want the head's group %d; the fixture proves nothing", pgid, leader)
	}

	if kErr := Kill(head.Process, syscall.SIGKILL); kErr != nil {
		t.Fatalf("Kill: %v", kErr)
	}

	// The member must die with the group. A head-only signal leaves it running.
	select {
	case <-memberDone:
	case <-time.After(5 * time.Second):
		_ = member.Process.Kill()
		t.Fatal("group member survived Kill; the signal reached the head alone")
	}
}

// startHeadWithOutsideChild starts a sh head in its own group that forks one child in the head's
// group and one setsid child in a group of its own, and returns the head and the setsid child's
// pid (its group id). The head lives until release exists.
func startHeadWithOutsideChild(t *testing.T, release string) (head *exec.Cmd, outside int) {
	t.Helper()
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid is not on PATH")
	}
	pidFile := t.TempDir() + "/outside.pid"
	head = exec.Command("sh", "-c", `sleep 60 &
setsid sh -c 'echo $$ > "`+pidFile+`.tmp"; mv "`+pidFile+`.tmp" "`+pidFile+`"; trap "" TERM; while :; do sleep 0.05; done' &
while [ ! -e "`+release+`" ]; do sleep 0.05; done`)
	head.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := head.Start(); err != nil {
		t.Fatalf("Setup: start head: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-head.Process.Pid, syscall.SIGKILL)
		_ = head.Wait()
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, err := os.ReadFile(pidFile)
		if err == nil {
			outside, err = strconv.Atoi(strings.TrimSpace(string(raw)))
			if err != nil {
				t.Fatalf("Setup: parse outside pid %q: %v", raw, err)
			}
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatal("Setup: the setsid child never wrote its pid")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { _ = syscall.Kill(-outside, syscall.SIGKILL) })
	if g, err := syscall.Getpgid(outside); err != nil || g != outside {
		t.Fatalf("Setup: outside child pgid = %d, %v; want its own group %d", g, err, outside)
	}
	return head, outside
}

func TestGroupsBelow_ReportsAnOutsideGroupAndSkipsOwnAndInit(t *testing.T) {
	head, outside := startHeadWithOutsideChild(t, t.TempDir()+"/never")

	got := GroupsBelow(head.Process.Pid)
	if len(got) != 1 || got[0].ID != outside {
		t.Fatalf("GroupsBelow(head) = %v, want exactly [%d], the setsid child's group (never the head's %d or the test's %d)",
			got, outside, head.Process.Pid, syscall.Getpgrp())
	}
	if st, ok := readStat(outside); !ok || got[0].leader != st.start {
		t.Errorf("GroupsBelow(head) stamped leader %d, want the setsid child's start time %d", got[0].leader, st.start)
	}
}

// Signal 0 only: a guard that fails here would otherwise signal init's group, which is every
// process the test may signal.
func TestKillGroup_RefusesOwnInitAndEmptyGroups(t *testing.T) {
	zombie := exec.Command("sleep", "60")
	zombie.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := zombie.Start(); err != nil {
		t.Fatalf("Setup: start: %v", err)
	}
	t.Cleanup(func() { _ = zombie.Wait() })
	if err := zombie.Process.Kill(); err != nil {
		t.Fatalf("Setup: kill: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if st, ok := readStat(zombie.Process.Pid); ok && st.state == 'Z' {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatal("Setup: the killed group leader never reached the zombie state")
		}
		time.Sleep(10 * time.Millisecond)
	}

	for _, tc := range []struct {
		name string
		pgid int
	}{
		{name: "init", pgid: 1},
		{name: "zero", pgid: 0},
		{name: "own group", pgid: syscall.Getpgrp()},
		{name: "zombie-only group", pgid: zombie.Process.Pid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := KillGroup(stampGroup(tc.pgid), syscall.Signal(0)); err == nil {
				t.Errorf("KillGroup(%d) = nil, want a refusal", tc.pgid)
			}
		})
	}
}

func TestKillGroup_KillsAGroupWithALiveMember(t *testing.T) {
	pgid, member := startGroupWithMember(t)
	g := stampGroup(pgid)
	// The head dies alone and stays unreaped, so only the member keeps the group live.
	if err := syscall.Kill(pgid, syscall.SIGKILL); err != nil {
		t.Fatalf("Setup: kill head: %v", err)
	}

	if err := KillGroup(g, syscall.SIGKILL); err != nil {
		t.Fatalf("KillGroup(%d) = %v, want nil", pgid, err)
	}
	err := member.Wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Errorf("member wait = %v, want killed by SIGKILL through its group", err)
	}
}

// A live leader whose start time is not the stamped one leads a later group that reuses the id.
func TestKillGroup_RefusesAnIdALaterGroupReuses(t *testing.T) {
	pgid, _ := startGroupWithMember(t)
	later := stampGroup(pgid)
	later.leader++

	if err := KillGroup(later, syscall.Signal(0)); !errors.Is(err, errGroupRefused) {
		t.Errorf("KillGroup(stale stamp) = %v, want errGroupRefused", err)
	}
}

// The orphan shape a bridge sweep exists for: the leader exited and was reaped, a member still
// holds the group's id, and no process sits at that pid.
func TestKillGroup_KillsAGroupWhoseLeaderWasReaped(t *testing.T) {
	leader := exec.Command("sleep", "60")
	leader.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := leader.Start(); err != nil {
		t.Fatalf("Setup: start leader: %v", err)
	}
	t.Cleanup(func() { _ = leader.Process.Kill(); _ = leader.Wait() })
	pgid := leader.Process.Pid
	g := stampGroup(pgid)
	member := exec.Command("sleep", "60")
	member.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: pgid}
	if err := member.Start(); err != nil {
		t.Fatalf("Setup: start member: %v", err)
	}
	t.Cleanup(func() { _ = member.Process.Kill(); _ = member.Wait() })
	if got, err := syscall.Getpgid(member.Process.Pid); err != nil || got != pgid {
		t.Fatalf("Setup: member pgid = %d, %v; want the leader's group %d", got, err, pgid)
	}
	if err := leader.Process.Kill(); err != nil {
		t.Fatalf("Setup: kill leader: %v", err)
	}
	_ = leader.Wait()
	if _, ok := readStat(pgid); ok {
		t.Fatalf("Setup: a process still sits at the reaped leader's pid %d", pgid)
	}

	if g.Stale() {
		t.Error("Stale() = true for a group whose leader was reaped, want false: the sweep would skip its orphans")
	}
	if err := KillGroup(g, syscall.SIGKILL); err != nil {
		t.Fatalf("KillGroup(reaped-leader group) = %v, want nil", err)
	}
	err := member.Wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Errorf("member wait = %v, want killed by SIGKILL through its group", err)
	}
}

func TestGroup_StaleOnlyWhenTheLiveLeaderStartedAtAnotherTime(t *testing.T) {
	pgid, _ := startGroupWithMember(t)
	own := stampGroup(pgid)
	later := own
	later.leader++

	if own.Stale() {
		t.Errorf("Stale() = true for the group's own stamp, want false")
	}
	if !later.Stale() {
		t.Errorf("Stale() = false for another start time at a live pid, want true")
	}
}
