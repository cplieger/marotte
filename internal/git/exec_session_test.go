package git

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// hookIDs is what the pre-commit hook recorded about itself: /proc/<pid>/stat fields 1, 5 and 6.
type hookIDs struct{ pid, pgrp, sid int }

// startHookedCommit starts `git commit` through gitExec in a fixture repo whose pre-commit hook,
// which an agent can write, records its ids and then sleeps. It returns the started command and
// the hook's ids; cleanup SIGKILLs only the hook pid it recorded.
func startHookedCommit(t *testing.T, ctx context.Context) (*exec.Cmd, hookIDs) {
	t.Helper()
	dir := t.TempDir()
	initFixtureRepo(t, dir)
	idsFile := filepath.Join(t.TempDir(), "ids")
	hook := "#!/bin/sh\ncut -d' ' -f1,5,6 /proc/$$/stat > \"" + idsFile + ".tmp\"\nmv \"" + idsFile + ".tmp\" \"" + idsFile + "\"\nexec sleep 300\n"
	syscall.ForkLock.RLock()
	err := os.WriteFile(filepath.Join(dir, ".git", "hooks", "pre-commit"), []byte(hook), 0o755) // #nosec G306 -- a hook must be executable
	syscall.ForkLock.RUnlock()
	if err != nil {
		t.Fatalf("Setup: write hook: %v", err)
	}

	cmd := gitExec(ctx, dir, "commit", "--allow-empty", "-q", "-m", "hooked")
	// The ambient global config may sign commits or move hooksPath; the fixture needs neither.
	cmd.Env = append(cmd.Env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
	if err := cmd.Start(); err != nil {
		t.Fatalf("Setup: start git commit: %v", err)
	}
	var ids hookIDs
	t.Cleanup(func() {
		if ids.pid > 1 && ids.pid != os.Getpid() {
			_ = syscall.Kill(ids.pid, syscall.SIGKILL)
		}
		_ = cmd.Wait()
	})

	deadline := time.Now().Add(10 * time.Second)
	for {
		raw, rErr := os.ReadFile(idsFile)
		if rErr == nil {
			f := strings.Fields(string(bytes.TrimSpace(raw)))
			if len(f) != 3 {
				t.Fatalf("Setup: hook recorded %q, want three ids", raw)
			}
			n := make([]int, 3)
			for i, s := range f {
				v, cErr := strconv.Atoi(s)
				if cErr != nil {
					t.Fatalf("Setup: parse hook id %q: %v", s, cErr)
				}
				n[i] = v
			}
			ids = hookIDs{pid: n[0], pgrp: n[1], sid: n[2]}
			return cmd, ids
		}
		if time.Now().After(deadline) {
			t.Fatalf("Setup: the pre-commit hook never ran: %v", rErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A hook reading its session id, as the incident loop did with `ps -o sid=`, names git's tree.
func TestGitExec_HookRunsInGitsOwnSession(t *testing.T) {
	cmd, ids := startHookedCommit(t, t.Context())
	self, err := unix.Getsid(0)
	if err != nil {
		t.Fatalf("Setup: getsid: %v", err)
	}
	git := cmd.Process.Pid
	if ids.sid != git {
		t.Errorf("hook %d is in session %d, want git's own session %d", ids.pid, ids.sid, git)
	}
	if ids.sid == self {
		t.Errorf("hook shares the server's session %d: `kill -- -<sid>` inside it signals the server", self)
	}
	if ids.pgrp == syscall.Getpgrp() {
		t.Errorf("hook shares the server's process group %d", ids.pgrp)
	}
}

// A cancel kills the whole session: SIGKILL to git alone leaves its hook running.
func TestGitExec_CancelKillsTheHook(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cmd, ids := startHookedCommit(t, ctx)
	cancel()
	if err := cmd.Wait(); err == nil {
		t.Error("Wait after the cancel = nil, want the kill's error")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		raw, rErr := os.ReadFile("/proc/" + strconv.Itoa(ids.pid) + "/stat")
		// The state follows the LAST ')': the executable name before it may hold parens.
		state := bytes.TrimSpace(raw[bytes.LastIndexByte(raw, ')')+1:])
		if rErr != nil || bytes.HasPrefix(state, []byte("Z")) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("hook %d outlived git's cancel", ids.pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A post-commit hook that backgrounds a job inheriting git's output: git exits 0 and the commit
// lands, so the call succeeds once WaitDelay closes the pipe the job still holds.
func TestGitCmd_HookJobHoldingTheOutputIsNotAFailure(t *testing.T) {
	saved := gitWaitDelay
	gitWaitDelay = 200 * time.Millisecond
	t.Cleanup(func() { gitWaitDelay = saved })
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_AUTHOR_NAME", "test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
	dir := t.TempDir()
	initFixtureRepo(t, dir)
	pidFile := filepath.Join(t.TempDir(), "job")
	hook := "#!/bin/sh\nsleep 300 &\necho $! > \"" + pidFile + "\"\n"
	syscall.ForkLock.RLock()
	err := os.WriteFile(filepath.Join(dir, ".git", "hooks", "post-commit"), []byte(hook), 0o755) // #nosec G306 -- a hook must be executable
	syscall.ForkLock.RUnlock()
	if err != nil {
		t.Fatalf("Setup: write hook: %v", err)
	}
	var job int
	t.Cleanup(func() {
		if job > 1 && job != os.Getpid() {
			_ = syscall.Kill(job, syscall.SIGKILL)
		}
	})

	_, err = gitCmd(t.Context(), dir, "commit", "--allow-empty", "-q", "-m", "hooked")

	raw, rErr := os.ReadFile(pidFile)
	if rErr != nil {
		t.Fatalf("Setup: the post-commit hook never recorded its job: %v", rErr)
	}
	if job, rErr = strconv.Atoi(string(bytes.TrimSpace(raw))); rErr != nil {
		t.Fatalf("Setup: parse job pid %q: %v", raw, rErr)
	}
	if _, sErr := os.Stat("/proc/" + strconv.Itoa(job)); sErr != nil {
		t.Fatalf("Setup: job %d exited before gitCmd returned, so it held no output: %v", job, sErr)
	}
	if err != nil {
		t.Errorf("gitCmd(commit) = %v, want nil: git exited 0", err)
	}
	if subject, lErr := gitCmd(t.Context(), dir, "log", "-1", "--format=%s"); lErr != nil || subject != "hooked" {
		t.Errorf("last commit subject = %q, %v; want %q", subject, lErr, "hooked")
	}
}
