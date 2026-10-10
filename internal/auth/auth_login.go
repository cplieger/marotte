package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os/exec"
	"time"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/procout"
	"github.com/cplieger/webhttp/v3"
)

// Empty passes for the default Builder ID flow. An attacker-controlled start URL would hand away
// the user's SSO session.
func validateProvider(v string) error {
	if v == "" {
		return nil
	}
	if len(v) > maxProviderLen {
		return errors.New("provider too long")
	}
	u, perr := url.Parse(v)
	if perr != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("provider must be an https URL")
	}
	// Some HTTP clients use embedded userinfo before a redirect (CWE-601).
	if u.User != nil {
		return errors.New("provider must not contain credentials")
	}
	return nil
}

// validateRegion rejects anything that is not a canonical AWS region id, in any
// partition. Empty strings pass through so kiro-cli picks its default.
func validateRegion(v string) error {
	if v == "" {
		return nil
	}
	if len(v) > maxRegionLen {
		return errors.New("region too long")
	}
	if !awsRegionRe.MatchString(v) {
		return errors.New("invalid region")
	}
	return nil
}

// buildLoginArgs returns the argv tail after the binary path for `kiro-cli login`.
// An empty provider or region is omitted so kiro-cli picks its default.
const flagDeviceFlow = "--use-device-flow"

func buildLoginArgs(provider, region string) []string {
	args := []string{"login", flagDeviceFlow}
	if provider != "" {
		args = append(args, "--identity-provider", provider)
	}
	if region != "" {
		args = append(args, "--region", region)
	}
	return args
}

// parseLoginRequest decodes and validates the POST body, writing the error response itself and returning
// ok=false. An empty body (the default Builder ID flow) yields zero values and ok=true.
func parseLoginRequest(w http.ResponseWriter, r *http.Request) (provider, region string, ok bool) {
	webhttp.LimitBody(w, r, webhttp.MaxJSONBody)
	var body struct {
		Provider string `json:"provider"`
		Region   string `json:"region"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			slog.Warn("login: body exceeds limit",
				"limit_bytes", webhttp.MaxJSONBody)
			webhttp.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
				httpreply.ErrorJSON("request too large"))
			return "", "", false
		}
		slog.Warn("login: decode body", "error", err)
		httpreply.BadRequest(w, "invalid JSON body")
		return "", "", false
	}
	if err := validateProvider(body.Provider); err != nil {
		httpreply.BadRequest(w, err.Error())
		return "", "", false
	}
	if err := validateRegion(body.Region); err != nil {
		httpreply.BadRequest(w, err.Error())
		return "", "", false
	}
	return body.Provider, body.Region, true
}

// handleLogin spawns `kiro-cli login --use-device-flow` and returns the first auth URL on its stdout. The process
// outlives the request, capped at LoginTimeout. One login at a time; a concurrent POST gets 409.
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	// Audit trail; client_ip is webhttp.ClientIP's spoof-safe host.
	slog.Info("login: request received",
		"client_ip", webhttp.ClientIP(r, h.trusted...),
		"user_agent", logsafe.Field(r.Header.Get("User-Agent")))
	select {
	case h.loginSem <- struct{}{}:
		// Once cmd.Start succeeds the reap goroutine owns the sem; earlier returns release through semReleased.
	default:
		httpreply.Conflict(w, "login in progress")
		return
	}
	semReleased := false
	defer func() {
		if !semReleased {
			<-h.loginSem
		}
	}()
	provider, region, ok := parseLoginRequest(w, r)
	if !ok {
		return
	}
	if msg := h.loginRefusal(provider); msg != "" {
		webhttp.WriteJSONStatus(w, http.StatusForbidden, httpreply.ErrorJSON(msg))
		return
	}
	// Capped on the subprocess, not r.Context(): the client leaves after the URL and returns minutes later. The select
	// below bounds URL discovery.
	ctx, cancel := context.WithTimeout(context.Background(), h.cfg.LoginTimeout)
	cmd := exec.CommandContext(ctx, h.cliPath(), buildLoginArgs(provider, region)...) //nolint:gosec // G204: binary path from config
	setProcGroup(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		slog.Error("login: stdout pipe failed",
			"error", err, "cli_path", h.cliPath())
		webhttp.WriteJSONStatus(w, http.StatusInternalServerError,
			httpreply.ErrorJSON("login unavailable"))
		return
	}
	// Separate from stdout so stderr does not count against maxLoginLines.
	stderrBuf := procout.NewBuffer(stderrCap)
	cmd.Stderr = stderrBuf
	if err := cmd.Start(); err != nil {
		cancel()
		status := classifyLoginStartErr(err, h.cliPath())
		webhttp.WriteJSONStatus(w, status, httpreply.ErrorJSON("login unavailable"))
		return
	}
	urlCh := make(chan map[string]string, 1)
	// The process keeps writing during the browser flow, so the drain runs in the background or the pipe fills.
	// stdoutDone gates the reap's cmd.Wait.
	stdoutDone := make(chan struct{})
	go func() {
		defer close(stdoutDone)
		scanLoginOutputWithDrain(stdout, urlCh)
	}()

	// Held until cmd.Wait returns, so a retry during the device-code window gets 409.
	semReleased = true

	// The browser polls /api/whoami every 3s during login; without this a fresh entry answers signed_out for a full TTL.
	h.identity.invalidate()

	// Reaped on either branch. On timeout the process group is killed (CommandContext kills only the PID, orphaning
	// helpers on the pipe) and waited for, reclaiming the FDs.
	waitDone := make(chan struct{})
	go h.reapLoginProcess(loginReap{
		ctx:        ctx,
		cancel:     cancel,
		cmd:        cmd,
		stderrBuf:  stderrBuf,
		stdoutDone: stdoutDone,
		waitDone:   waitDone,
	})

	select {
	case result := <-urlCh:
		if result["url"] == "" {
			// No completion is coming and the code is wasted, so reap now rather than at the hard cap.
			killProcessGroup(cmd)
			<-waitDone
		}
		webhttp.WriteJSON(w, result)
	case <-time.After(h.cfg.LoginURLTimeout):
		killProcessGroup(cmd)
		<-waitDone // bounded: Kill → SIGKILL → reap
		attrs := make([]any, 0, 4)
		attrs = append(attrs, "timeout", h.cfg.LoginURLTimeout)
		attrs = append(attrs, stderrAttr(stderrBuf)...)
		slog.Warn("login: timeout waiting for auth URL", attrs...)
		webhttp.WriteJSONStatus(w, http.StatusGatewayTimeout,
			httpreply.ErrorJSON("timeout waiting for auth URL"))
	}
}
