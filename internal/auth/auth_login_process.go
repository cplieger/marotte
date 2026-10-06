package auth

import (
	"bufio"
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/sanitize"
)

// classifyLoginStartErr maps a cmd.Start error to an HTTP status: 503 when the
// binary is missing (fork/exec ENOENT or a LookPath failure), 500 otherwise.
func classifyLoginStartErr(err error, cliPath string) int {
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		slog.Error("login: kiro-cli binary not found",
			"cli_path", cliPath)
		return http.StatusServiceUnavailable
	}
	slog.Error("login: kiro-cli start failed",
		"error", err, "cli_path", cliPath)
	return http.StatusInternalServerError
}

// reapLoginProcess waits for the login subprocess to exit, then releases the semaphore and closes waitDone,
// keeping one login at a time for the whole device-flow window.
func (h *Handler) reapLoginProcess(r loginReap) {
	// CommandContext SIGKILLs only the parent; a surviving helper holds stdout open and blocks cmd.Wait.
	killOnDeadline := make(chan struct{})
	go func() {
		select {
		case <-r.ctx.Done():
			if errors.Is(r.ctx.Err(), context.DeadlineExceeded) {
				killProcessGroup(r.cmd)
			}
		case <-killOnDeadline:
		}
	}()
	// cmd.Wait closes the stdout pipe; a concurrent reader would see "file already closed".
	<-r.stdoutDone
	werr := r.cmd.Wait()
	close(killOnDeadline)
	// Redundant unless we raced the watcher above.
	if errors.Is(r.ctx.Err(), context.DeadlineExceeded) {
		killProcessGroup(r.cmd)
	}
	r.cancel()
	switch {
	case werr == nil:
		slog.Info("login: subprocess completed cleanly")
	case errors.Is(r.ctx.Err(), context.DeadlineExceeded):
		attrs := make([]any, 0, 4)
		attrs = append(attrs, "cap", h.cfg.LoginTimeout)
		attrs = append(attrs, stderrAttr(r.stderrBuf)...)
		slog.Warn("login: subprocess hit hard cap", attrs...)
	default:
		slog.Debug("login: cmd wait returned", "error", werr)
	}
	// After the subprocess exits, so a POST during the browser flow still gets 409.
	<-h.loginSem
	close(r.waitDone)
	// Exit status proves neither sign-in nor failure, so re-read. Last: the read forks kiro-cli for up to
	// WhoamiTimeout and must not hold the semaphore or waitDone, which handleLogin's timeout branch waits on.
	h.identity.refresh()
}

// extractAuthURL returns the auth URL in one stripped login-output line, or "". An "Open this URL:" prefix anchors
// the search so a secondary URL never shadows the primary; only https:// tokens qualify.
func extractAuthURL(line string) string {
	if after, found := strings.CutPrefix(line, "Open this URL:"); found {
		for word := range strings.FieldsSeq(after) {
			if strings.HasPrefix(word, "https://") {
				return word
			}
		}
		return ""
	}
	if strings.Contains(line, "https://") {
		for word := range strings.FieldsSeq(line) {
			if strings.HasPrefix(word, "https://") {
				return word
			}
		}
	}
	return ""
}

// scanLoginOutputWithDrain runs scanLoginOutput, then drains stdout to io.Discard until it closes, so the banners
// kiro-cli writes after the URL cannot block it on write(2).
func scanLoginOutputWithDrain(stdout io.ReadCloser, urlCh chan<- map[string]string) {
	scanLoginOutput(stdout, urlCh)
	if _, err := io.Copy(io.Discard, stdout); err != nil {
		slog.Debug("login: stdout drain stopped", "error", err)
	}
}

// scanLoginOutput reads lines from r until it finds an auth URL and sends it, with any "Code:", into urlCh; on EOF,
// a scanner error or the line cap it sends an error map. urlCh must be buffered so this never blocks after the
// caller's select moved on. Memory is O(maxScanLineBytes).
func scanLoginOutput(stdout io.Reader, urlCh chan<- map[string]string) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), maxScanLineBytes)
	// Capped before storage so a hostile CLI cannot blow up the log event.
	ring := newLineRing(5, 128)
	var code, authURL string
	var lineCount int
	for scanner.Scan() {
		line := strings.TrimSpace(sanitize.StripANSI(scanner.Text()))
		lineCount++
		ring.Push(line)
		// kiro-cli refuses a fresh login when a session exists; that gets its own error key.
		if strings.Contains(strings.ToLower(line), "already logged in") {
			urlCh <- httpreply.ErrorJSON("already_logged_in")
			return
		}
		if after, found := strings.CutPrefix(line, "Code:"); found {
			code = strings.TrimSpace(after)
		}
		if authURL == "" {
			authURL = extractAuthURL(line)
		}
		if authURL != "" {
			slog.Info("login: auth URL extracted",
				"has_code", code != "",
				"lines_before_url", lineCount)
			urlCh <- map[string]string{"url": authURL, "code": code}
			return
		}
		if lineCount >= maxLoginLines {
			// Warn: format drift or a user cancel is recoverable and visible.
			slog.Warn("login: output line cap hit without auth URL",
				"lines", lineCount,
				"first_and_last_sample", ring.Sample())
			urlCh <- httpreply.ErrorJSON("CLI produced too much output without auth URL")
			return
		}
	}
	if err := scanner.Err(); err != nil {
		// Warn: EOF after killProcessGroup lands here too.
		slog.Warn("login: scanner failed before URL",
			"error", err, "lines_read", lineCount)
		urlCh <- httpreply.ErrorJSON("scanner error: " + err.Error())
		return
	}
	// handleLogin's URL-timeout branch adds context.
	urlCh <- httpreply.ErrorJSON("no auth URL found in CLI output")
}
