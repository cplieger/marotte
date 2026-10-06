package auth

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/procgroup"
	"github.com/cplieger/marotte/internal/procout"
	"github.com/cplieger/marotte/internal/sanitize"
	"github.com/cplieger/webhttp/v3"
)

// handleLogout runs `kiro-cli logout`, answering "y\n" to its confirmation, and returns stdout+stderr as "output".
// Failures: 504 at the LogoutTimeout backstop, 503 when kiro-cli is missing, 502 otherwise.
func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	// Audit trail for every /api/logout POST.
	slog.Info("logout: request received",
		"client_ip", webhttp.ClientIP(r, h.trusted...),
		"user_agent", r.Header.Get("User-Agent"))
	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.LogoutTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, h.cliPath(), "logout") //nolint:gosec // G204: binary path from config
	boundChild(cmd)
	cmd.Stdin = strings.NewReader("y\n")
	// Bounded so a runaway CLI cannot OOM the container. One Buffer for both streams: os/exec then uses one pipe.
	buf := procout.NewBuffer(logoutMaxOutput)
	cmd.Stdout = buf
	cmd.Stderr = buf
	err := cmd.Run()
	out := buf.Bytes()
	result := map[string]string{"output": sanitize.Output(string(out))}
	if err != nil {
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			// boundChild has already killed the group when Run returns.
			slog.Warn("logout: kiro-cli timed out",
				"timeout", h.cfg.LogoutTimeout, "output_bytes", len(out))
			result["error"] = "logout timed out"
			webhttp.WriteJSONStatus(w, http.StatusGatewayTimeout, result)
			return
		case errors.Is(err, exec.ErrNotFound), errors.Is(err, fs.ErrNotExist):
			slog.Error("logout: kiro-cli binary not found",
				"cli_path", h.cliPath())
			result["error"] = "logout unavailable"
			webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable, result)
			return
		default:
			// A generic sentinel, so paths and OS messages do not leak.
			slog.Warn("logout: kiro-cli failed",
				"error", err, "output_bytes", len(out))
			result["error"] = "logout failed"
			webhttp.WriteJSONStatus(w, http.StatusBadGateway, result)
			return
		}
	}
	slog.Info("logout: completed", "output_bytes", len(out))
	// Published, not re-read: a clean logout exit is the answer, and a second fork only prolongs the old identity.
	signedOut := signedOutIdentity()
	h.identity.publish(&signedOut)
	h.registrar.SignedOut()
	webhttp.WriteJSON(w, result)
}

// killProcessGroup SIGKILLs a kiro-cli subprocess's whole group: its helper children would otherwise pin the stdout
// pipe. A call after the process was reaped is a no-op.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	err := killGroup(cmd)
	if err == nil {
		return
	}
	// Both kills can race cmd.Wait's reap.
	if procgroup.AlreadyGone(err) {
		slog.Debug("auth: kill group no-op (already reaped)",
			"group_err", err)
		return
	}
	// Fallback: best-effort single-PID kill.
	kerr := cmd.Process.Kill()
	if kerr == nil {
		return
	}
	if procgroup.AlreadyGone(kerr) {
		slog.Debug("auth: kill pid no-op (already reaped)",
			"group_err", err, "pid_err", kerr)
		return
	}
	slog.Error("auth: kill subprocess group failed",
		"group_err", err, "pid_err", kerr)
}
