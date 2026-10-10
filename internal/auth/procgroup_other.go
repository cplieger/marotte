//go:build !unix

package auth

import (
	"errors"
	"os/exec"
)

// Marotte runs in Linux containers. Without a group, boundChild's Cancel reaches only the parent,
// so WaitDelay is the whole bound.
func setProcGroup(_ *exec.Cmd) {}

// errUnsupportedKill is killGroup's answer on non-unix, so killProcessGroup falls back to a single-PID Kill.
var errUnsupportedKill = errors.New("process-group kill unsupported on this platform")

func killGroup(_ *exec.Cmd) error { return errUnsupportedKill }
