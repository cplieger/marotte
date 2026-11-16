package procgroup

import (
	"bufio"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// startSession starts script as the leader of a new session and returns it; cleanup SIGKILLs the
// session's leader group and reaps the leader.
func startSession(t *testing.T, script string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sh", "-c", script) // #nosec G204 -- fixed test script
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("Setup: start session leader: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	return cmd
}

func TestSessionOf(t *testing.T) {
	t.Run("leader", func(t *testing.T) {
		leader := startSession(t, "exec sleep 60")
		sid, ok := SessionOf(leader.Process)
		if !ok || sid != leader.Process.Pid {
			t.Errorf("SessionOf(Setsid child) = (%d, %v), want (%d, true)", sid, ok, leader.Process.Pid)
		}
	})
	t.Run("member of the caller's session", func(t *testing.T) {
		member := exec.Command("sleep", "60")
		member.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := member.Start(); err != nil {
			t.Fatalf("Setup: start: %v", err)
		}
		t.Cleanup(func() {
			_ = member.Process.Kill()
			_ = member.Wait()
		})
		if sid, ok := SessionOf(member.Process); ok {
			t.Errorf("SessionOf(Setpgid child) = (%d, true), want false: it is in the caller's session", sid)
		}
	})
}

func TestReadStat_ReadsTheSession(t *testing.T) {
	leader := startSession(t, "exec sleep 60")
	st, ok := readStat(leader.Process.Pid)
	if !ok || st.session != leader.Process.Pid {
		t.Errorf("readStat(%d) = (session %d, ok %v), want session %d", leader.Process.Pid, st.session, ok, leader.Process.Pid)
	}
}

// The refusals run before any signal, so the ids here are never signalled.
func TestKillSession_RefusesInitTheCallersOwnAndAnEmptySession(t *testing.T) {
	self, err := unix.Getsid(0)
	if err != nil {
		t.Fatalf("Setup: getsid: %v", err)
	}
	for _, sid := range []int{-1, 0, 1, self} {
		if err := KillSession(sid, syscall.Signal(0)); !errors.Is(err, errSessionRefused) {
			t.Errorf("KillSession(%d) = %v, want errSessionRefused", sid, err)
		}
	}

	// A zombie leader, unreaped so its id cannot be reused, is the session's only member.
	zombie := startSession(t, "exit 0")
	pid := zombie.Process.Pid
	deadline := time.Now().Add(5 * time.Second)
	for st, ok := readStat(pid); !ok || st.state != 'Z'; st, ok = readStat(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("Setup: leader %d never reached the zombie state", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := KillSession(pid, syscall.SIGKILL); !errors.Is(err, errSessionRefused) {
		t.Errorf("KillSession(session of zombies) = %v, want errSessionRefused", err)
	}
}

// A member that left the leader's group (GNU timeout calls setpgid) still dies with the session.
func TestKillSession_ReachesEveryGroupInTheSession(t *testing.T) {
	if _, err := exec.LookPath("timeout"); err != nil {
		t.Skip("timeout is not on PATH")
	}
	cmd := exec.Command("sh", "-c", "sleep 300 & echo $!; timeout 300 sleep 300 & echo $!; wait") // #nosec G204 -- fixed test script
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("Setup: stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("Setup: start: %v", err)
	}
	sid := cmd.Process.Pid
	var pids []int
	t.Cleanup(func() {
		for _, p := range pids {
			_ = syscall.Kill(p, syscall.SIGKILL)
		}
		_ = syscall.Kill(-sid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	r := bufio.NewReader(out)
	for range 2 {
		line, rErr := r.ReadString('\n')
		p, cErr := strconv.Atoi(strings.TrimSpace(line))
		if rErr != nil || cErr != nil {
			t.Fatalf("Setup: read member pid %q: %v %v", line, rErr, cErr)
		}
		pids = append(pids, p)
	}
	sleepPID, timeoutPID := pids[0], pids[1]
	// timeout calls setpgid after its exec, so its pid is echoed before it leaves the group.
	deadline := time.Now().Add(5 * time.Second)
	for pg, gErr := syscall.Getpgid(timeoutPID); gErr != nil || pg == sid; pg, gErr = syscall.Getpgid(timeoutPID) {
		if time.Now().After(deadline) {
			t.Fatalf("Setup: timeout %d is in group %d (%v), want a group of its own; the test proves nothing", timeoutPID, pg, gErr)
		}
		time.Sleep(5 * time.Millisecond)
	}

	if err := KillSession(sid, syscall.SIGKILL); err != nil {
		t.Fatalf("KillSession: %v", err)
	}
	if !WaitSessionGone(sid, 3*time.Second) {
		t.Errorf("WaitSessionGone(%d) = false after KillSession: a member survived", sid)
	}
	for _, p := range []int{sleepPID, timeoutPID} {
		if st, ok := readStat(p); ok && st.state != 'Z' {
			t.Errorf("member %d still live (state %q) after KillSession", p, st.state)
		}
	}
}

func TestWaitSessionGone(t *testing.T) {
	t.Run("live member", func(t *testing.T) {
		leader := startSession(t, "exec sleep 60")
		start := time.Now()
		if WaitSessionGone(leader.Process.Pid, 150*time.Millisecond) {
			t.Error("WaitSessionGone(live session) = true")
		}
		if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
			t.Errorf("WaitSessionGone returned after %v, want at least the 150ms budget", elapsed)
		}
	})
	t.Run("init and below", func(t *testing.T) {
		for _, sid := range []int{-1, 0, 1} {
			start := time.Now()
			if !WaitSessionGone(sid, 5*time.Second) {
				t.Errorf("WaitSessionGone(%d) = false, want true without waiting", sid)
			}
			if elapsed := time.Since(start); elapsed > time.Second {
				t.Errorf("WaitSessionGone(%d) took %v, want no wait", sid, elapsed)
			}
		}
	})
}

// A group or session id read inside the command names its own tree, never the caller's.
func TestOwnSession_LeadsANewSession(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "sleep", "60")
	OwnSession(cmd, time.Second)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start under OwnSession: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	pid := cmd.Process.Pid
	st, ok := readStat(pid)
	if !ok {
		t.Fatalf("Setup: read /proc/%d/stat", pid)
	}
	self, err := unix.Getsid(0)
	if err != nil {
		t.Fatalf("Setup: getsid: %v", err)
	}
	if st.session != pid || st.session == self {
		t.Errorf("child %d is in session %d, want its own (the caller's is %d)", pid, st.session, self)
	}
	if st.pgrp != pid || st.pgrp == syscall.Getpgrp() {
		t.Errorf("child %d is in group %d, want its own (the caller's is %d)", pid, st.pgrp, syscall.Getpgrp())
	}
}
