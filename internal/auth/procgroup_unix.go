//go:build unix

package auth

import (
	"os/exec"
	"syscall"
)

// setProcGroup starts a kiro-cli subprocess in its own process group so the whole tree can be killed: a PID-only
// kill orphans its children, which keep stdout open and pin cmd.Wait. Only the group half of a child wrapper:
// login wants a hard SIGKILL on timeout, not a graceful drain.
func setProcGroup(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killGroup(c *exec.Cmd) error {
	if c.Process == nil {
		return syscall.ESRCH
	}
	return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
}
