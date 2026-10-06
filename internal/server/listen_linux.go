//go:build linux

package server

import (
	"net"
	"syscall"

	"github.com/cplieger/marotte/internal/liveness"
)

// tcpUserTimeout is TCP_USER_TIMEOUT (tcp(7)). Package-local because syscall
// exports the constant on arm64 and not on amd64, and the app carries no x/sys.
const tcpUserTimeout = 0x12

// listenConfig returns the listener config with TCP_USER_TIMEOUT set to the alive window,
// inherited by every accepted socket, so a write to a half-open peer fails at the window
// rather than after 13-30 minutes of retransmission. It reaches only the TCP peer (a proxy,
// behind one).
func listenConfig() net.ListenConfig {
	return net.ListenConfig{Control: setUserTimeout}
}

func setUserTimeout(_, _ string, c syscall.RawConn) error {
	var setErr error
	if err := c.Control(func(fd uintptr) {
		setErr = syscall.SetsockoptInt(int(fd), syscall.SOL_TCP, tcpUserTimeout,
			int(liveness.AliveWindow.Milliseconds()))
	}); err != nil {
		return err
	}
	return setErr
}
