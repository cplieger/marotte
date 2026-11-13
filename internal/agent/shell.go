// A single global PTY session with server-side VT parsing over /api/shell/ws
// (github.com/cplieger/web-terminal-engine/v6). Control messages are JSON prefixed with 0x00, which no terminal input starts with.

package agent

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/web-terminal-engine/v6/terminal"
)

// shutdownBudget bounds one PTY teardown, above the terminal engine's 5s reap ceiling (terminal.Handler.Shutdown) so expiry means a real hang.
const shutdownBudget = 10 * time.Second

// All three teardown paths use it, since engine v4's Shutdown blocks until the child is reaped. A
// spent ctx still signals: Shutdown closes first, then waits.
func retireHandler(ctx context.Context, h *terminal.Handler, why string) {
	ctx, cancel := context.WithTimeout(ctx, shutdownBudget)
	defer cancel()
	if err := h.Shutdown(ctx); err != nil {
		slog.Warn("shell teardown did not finish",
			"why", why, "budget", shutdownBudget, "error", err)
	}
}

// shellManager wraps terminal.Handler, which owns the PTY, VT parsing, wire encoding, fan-out and replay.
// The handler is single-use, so it is replaced on exit (spent) or on restart() for a wedged shell.
type shellManager struct {
	handler *terminal.Handler
	workDir string
	mu      sync.Mutex
	// spent means the child exited, so this handler can never start another.
	spent bool
}

// newShellManager creates a ShellManager running bash --login in workDir. ctx is unused.
func newShellManager(_ context.Context, workDir string) *shellManager {
	sm := &shellManager{workDir: workDir}
	sm.handler = sm.newHandler()
	return sm
}

// bash --login sources Debian's /etc/profile, which assigns PATH over any inherited or passed one,
// so the /config entries reach this shell only through entrypoint.sh's root-only drop-in
// (offer_login_path).
func (sm *shellManager) newHandler() *terminal.Handler {
	return terminal.NewHandler([]string{"bash", "--login"},
		terminal.WithWorkDir(sm.workDir),
		// Latch spent: the client reattaches on socket close and gets a fresh handler.
		terminal.WithOnProcessExit(func(err error) {
			sm.mu.Lock()
			sm.spent = true
			sm.mu.Unlock()
			slog.Info("shell: child exited", "error", err)
		}),
	)
}

// current returns the handler for this connection, replacing a spent one, and returns the old one for the
// caller to shut down after unlocking: Shutdown can run the exit callback, which takes this mutex.
func (sm *shellManager) current() (h, retire *terminal.Handler) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.spent {
		retire = sm.handler
		sm.handler = sm.newHandler()
		sm.spent = false
	}
	return sm.handler, retire
}

// restart replaces the PTY unconditionally, for a wedged shell whose child never exits.
func (sm *shellManager) restart() {
	sm.mu.Lock()
	retire := sm.handler
	sm.handler = sm.newHandler()
	sm.spent = false
	sm.mu.Unlock()
	retireHandler(context.Background(), retire, "restart")
	slog.Info("shell: restarted")
}

func (sm *shellManager) handleWS(w http.ResponseWriter, r *http.Request) {
	h, retire := sm.current()
	if retire != nil {
		retireHandler(context.Background(), retire, "spent")
	}
	h.ServeHTTP(w, r)
}

// POST because it destroys running processes.
func (sm *shellManager) handleRestart(w http.ResponseWriter, _ *http.Request) {
	sm.restart()
	httpreply.OK(w)
}

// ReadShell answers a `#` terminal reference from the handler as it stands: current() would swap out a just-exited shell and lose its final output.
func (sm *shellManager) ReadShell(maxLines int) (command.ShellText, bool) {
	sm.mu.Lock()
	h := sm.handler
	sm.mu.Unlock()
	snap, ok := h.Text(maxLines)
	if !ok {
		return command.ShellText{}, false
	}
	return command.ShellText{Text: snap.Text, AltScreen: snap.AltScreen, Exited: h.Exited()}, true
}

// kill ends the PTY session and waits for its teardown, using the engine's blocking form (its one caller is
// Runtime.Shutdown). Read under the mutex: restart() can swap the handler.
func (sm *shellManager) kill(ctx context.Context) {
	sm.mu.Lock()
	h := sm.handler
	sm.mu.Unlock()
	retireHandler(ctx, h, "shutdown")
}
