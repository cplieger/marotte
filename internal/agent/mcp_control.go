package agent

// Live MCP control over KAS's _kiro/mcp/*: a reconnect resets the server in the session its row represents
// (resolveServers), on the user's press or for each failed server before a scheduled run's host session opens; a
// sign-in resets it in the session waiting for one.
// _kiro/mcp/toggle is not wired: it is a global notification with no serverName.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rpcerr"
	"github.com/cplieger/marotte/internal/translate"
)

const (
	// mcpReconnectTimeout bounds an engine that never answers resetServer (an engine call has no timeout). KAS itself
	// answers within 60 s whether or not the reset finished (kiro-cli 2.31.0 awaitBoundedReset), so the reply is
	// never the verdict: the server's state arrives on _kiro/mcp/status.
	mcpReconnectTimeout = 90 * time.Second
	// maxLaunchResets bounds the automatic resets of one server between connects, so a server that can never start
	// is not respawned at every scheduled fire.
	maxLaunchResets = 3
)

// launchResetBound is how long a scheduled launch waits for its resets' answers, KAS's own answer ceiling. A var
// for tests.
var launchResetBound = 60 * time.Second

// keyServerName is the _kiro/mcp/* params key naming the server.
const keyServerName = "serverName"

const keyExitCode = "exitCode"

// keyStartOAuth asks resetServer for KAS's explicit sign-in: it discards the server's stored credentials, then
// waits on its redirect listener for the callback, the one attempt a pasted callback can complete.
const keyStartOAuth = "startOAuth"

// serverReachable reports whether live control may name this server: enabled in marotte's store,
// or reported running by KAS. A nil mcpConfig accepts any non-empty name.
func (reg *mcpRegistry) serverReachable(ctx context.Context, name string) bool {
	if name == "" {
		return false
	}
	if reg.config == nil {
		return true
	}
	if _, ok := reg.config.EnabledNames(ctx)[name]; ok {
		return true
	}
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	s, ok := reg.servers[name]
	return ok && s.State != mcpStateDisabled
}

var (
	// errMCPResetNoEngine is a reconnect asked while no engine is ready to take it.
	errMCPResetNoEngine = errors.New("the agent engine is not running")
	// errMCPResetNoSession is a reconnect of a server no live session binds: nothing holds an instance to reset,
	// and the next session that binds it connects it afresh.
	errMCPResetNoSession = errors.New("no live session binds the MCP server")
)

// reconnectServer sends _kiro/mcp/resetServer for the session the server's row represents: errMCPResetNoEngine
// while the engine is down, errMCPResetNoSession when no session binds it (KAS's own McpServerNotDeclaredError
// included), else the call's failure or refusal (logged).
func (reg *mcpRegistry) reconnectServer(ctx context.Context, name string) error {
	sid, ok := reg.actionSession(name)
	if !ok {
		return errMCPResetNoSession
	}
	h, release, ok := reg.engine.tryDoor()
	if !ok {
		return errMCPResetNoEngine
	}
	defer release()
	cctx, cancel := context.WithTimeout(ctx, mcpReconnectTimeout)
	defer cancel()
	_, err := h.Call(cctx, methodV3MCPResetServer, map[string]any{marotte.KeySessionID: sid, keyServerName: name})
	if m, ok := rpcerr.MappedOf(err); ok && m.ErrorType == rpcerr.McpServerNotDeclaredError {
		return fmt.Errorf("%w: %w", errMCPResetNoSession, err)
	}
	if err != nil {
		slog.Warn("mcp reconnect: the engine refused or did not answer the reset", "server", name, "error", err)
	}
	return err
}

type mcpReconnectReq struct {
	Server string `json:"server"`
}

// handleReconnect serves POST /api/mcp/reconnect {server}: 204 once the engine took the reset, 409 when no live
// session binds the server, 503 while no engine is running, 502 when it refused or did not answer.
func (reg *mcpRegistry) handleReconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	var body mcpReconnectReq
	if !httpreply.DecodeJSON(w, r, &body) {
		return
	}
	if !reg.serverReachable(r.Context(), body.Server) {
		httpreply.NotFound(w, "unknown or disabled MCP server")
		return
	}
	reg.mu.Lock()
	delete(reg.launchResets, body.Server)
	reg.mu.Unlock()
	switch err := reg.reconnectServer(r.Context(), body.Server); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, errMCPResetNoSession):
		httpserver.WriteJSONStatus(w, http.StatusConflict,
			httpreply.ErrorJSON("No open chat uses this server yet; it connects when a chat that uses it starts."))
	case errors.Is(err, errMCPResetNoEngine):
		httpserver.WriteJSONStatus(w, http.StatusServiceUnavailable,
			httpreply.ErrorJSON("The agent engine is not running. Try again once it is back."))
	default:
		httpserver.WriteJSONStatus(w, http.StatusBadGateway, httpreply.ErrorJSON("kiro-cli did not reset the server."))
	}
}

// Sign-in refusals: the server answers 409 with the text.
var (
	errSignInConnected     = errors.New("this server is already connected, so signing in again would only discard its credentials")
	errSignInInProgress    = errors.New("a sign-in for this server is already in progress")
	errSignInNotNeeded     = errors.New("this server is not waiting for a sign-in")
	errAnotherSignInStands = errors.New("another server's sign-in is in progress; finish it or let it time out, then try again")
)

// claimSignIn starts name's sign-in on the oldest view waiting for it. A server no view waits on is refused, connected
// or not: KAS's startOAuth discards every credential the server holds. A session connected through another
// declaration of the name keeps its live connection and needs a sign-in at its next reset. At most one unexpired
// sign-in stands across the views, because the consent URL KAS opens names neither its session nor its server. A
// timer ends the sign-in at its deadline if nothing else has. The attempt it returns is what the claimant's own
// release names (releaseAttempt, failSignIn).
func (reg *mcpRegistry) claimSignIn(ctx context.Context, name string) (signInClaim, error) {
	configured := reg.configuredNames(ctx)
	reg.mu.Lock()
	d := reg.expireSignInsLocked(configured)
	c, err := reg.fileSignInLocked(name)
	if err == nil && c.clearedFailure {
		d.then(reg.deriveLocked(configured))
	}
	reg.mu.Unlock()
	reg.publish(ctx, d)
	return c, err
}

// signInClaim is a filed sign-in: the session its reset goes to and the attempt that filed it.
type signInClaim struct {
	session marotte.SessionID
	attempt uint64
	// clearedFailure is whether the claim cleared an earlier refusal the row still shows.
	clearedFailure bool
}

func (reg *mcpRegistry) fileSignInLocked(name string) (signInClaim, error) {
	if i := slices.IndexFunc(reg.views, func(v mcpView) bool { return v.signIn != nil }); i >= 0 {
		if reg.views[i].signIn.server == name {
			return signInClaim{}, errSignInInProgress
		}
		return signInClaim{}, errAnotherSignInStands
	}
	r, ok := reg.resolveLocked(name)
	if rec := reg.servers[name]; !ok || rec == nil || rec.State != mcpStateOAuth || r.report.State != translate.MCPReportNeedsAuth {
		if r.connected {
			return signInClaim{}, errSignInConnected
		}
		return signInClaim{}, errSignInNotNeeded
	}
	v := &reg.views[r.view]
	cleared := v.failedSignIn.server != ""
	v.failedSignIn = viewSignInFailure{}
	reg.signIns++
	v.signIn = &viewSignIn{server: name, deadline: reg.now().Add(signInTTL), attempt: reg.signIns}
	reg.lifetime.afterUnlessDraining(signInTTL, func() { reg.expireSignIns(reg.lifetime.shutdownCtx) })
	return signInClaim{session: v.session, attempt: reg.signIns, clearedFailure: cleared}, nil
}

// expireSignIns ends every sign-in past its deadline, so its server leaves signing_in with no frame from its session.
func (reg *mcpRegistry) expireSignIns(ctx context.Context) {
	configured := reg.configuredNames(ctx)
	reg.mu.Lock()
	d := reg.expireSignInsLocked(configured)
	reg.mu.Unlock()
	reg.publish(ctx, d)
}

// expireSignInsLocked drops the sign-ins past their deadline and re-derives when one was dropped; the zero
// mcpDerived publishes nothing.
func (reg *mcpRegistry) expireSignInsLocked(configured map[string]struct{}) mcpDerived {
	now := reg.now()
	expired := false
	for i := range reg.views {
		if s := reg.views[i].signIn; s != nil && !now.Before(s.deadline) {
			reg.views[i].signIn = nil
			expired = true
		}
	}
	if !expired {
		return mcpDerived{}
	}
	return reg.deriveLocked(configured)
}

// attemptViewLocked is the index of the view whose standing sign-in attempt filed, -1 once it has ended.
func (reg *mcpRegistry) attemptViewLocked(attempt uint64) int {
	return slices.IndexFunc(reg.views, func(v mcpView) bool { return v.signIn != nil && v.signIn.attempt == attempt })
}

// releaseAttempt ends the sign-in only while attempt still holds it.
func (reg *mcpRegistry) releaseAttempt(attempt uint64) {
	reg.mu.Lock()
	if i := reg.attemptViewLocked(attempt); i >= 0 {
		reg.views[i].signIn = nil
	}
	reg.mu.Unlock()
}

// claimSignInURL gives a consent URL KAS opens while a sign-in stands to that sign-in: its server's record turns
// signing_in with the URL as the relay's anchor. A lazy sign-in another server starts meanwhile is attributed to the
// standing one too; the relay still delivers to the listener that URL names.
func (reg *mcpRegistry) claimSignInURL(ctx context.Context, url string) (string, bool) {
	configured := reg.configuredNames(ctx)
	reg.mu.Lock()
	now := reg.now()
	i := slices.IndexFunc(reg.views, func(v mcpView) bool { return v.signIn != nil && now.Before(v.signIn.deadline) })
	if i < 0 {
		reg.mu.Unlock()
		return "", false
	}
	server := reg.views[i].signIn.server
	reg.views[i].signIn.authURL = url
	d := reg.deriveLocked(configured)
	reg.mu.Unlock()
	reg.publish(ctx, d)
	reg.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventMCPSignInOpen, "", marotte.MCPSignInOpenPayload{Server: server, URL: url}))
	return server, true
}

// startSignIn claims the sign-in and sends resetServer {startOAuth} for its session in the background: KAS answers
// within 60 s whatever the connect does, so the answer is no verdict, and the sign-in ends with the server's next
// status in that session.
func (reg *mcpRegistry) startSignIn(ctx context.Context, name string) error {
	c, err := reg.claimSignIn(ctx, name)
	if err != nil {
		return err
	}
	started := reg.lifetime.goClaimed(func() func() {
		h, release, ok := reg.engine.tryDoor()
		if !ok {
			return nil
		}
		return func() {
			defer release()
			reg.sendSignIn(h, c, name)
		}
	})
	if !started {
		reg.releaseAttempt(c.attempt)
		return errMCPResetNoEngine
	}
	return nil
}

func (reg *mcpRegistry) sendSignIn(h engineHandle, c signInClaim, name string) {
	ctx, cancel := context.WithTimeout(reg.lifetime.shutdownCtx, mcpReconnectTimeout)
	defer cancel()
	_, err := h.Call(ctx, methodV3MCPResetServer, map[string]any{marotte.KeySessionID: c.session, keyServerName: name, keyStartOAuth: true})
	if err == nil {
		return
	}
	slog.Warn("mcp sign-in: the engine refused or did not answer the reset", "server", name, "error", err)
	reg.failSignIn(reg.lifetime.shutdownCtx, c.attempt, name, rpcerr.Text(err))
}

// failSignIn ends attempt's sign-in, which the engine never took, and records why on its session's view: the server
// still waits there, so its row stays needs_auth with the reason and Sign in files again. A sign-in no longer
// attempt's (the session or engine ended, the server reported, it expired, or a later Sign in filed another) has its
// status, so nothing is recorded.
func (reg *mcpRegistry) failSignIn(ctx context.Context, attempt uint64, name, reason string) {
	configured := reg.configuredNames(ctx)
	reg.mu.Lock()
	i := reg.attemptViewLocked(attempt)
	if i < 0 {
		reg.mu.Unlock()
		return
	}
	v := &reg.views[i]
	v.signIn = nil
	v.failedSignIn = viewSignInFailure{server: name, reason: reason}
	d := reg.deriveLocked(configured)
	reg.mu.Unlock()
	reg.publish(ctx, d)
}

// handleSignIn serves POST /api/mcp/sign-in {server}: 202 once the sign-in is queued, 404 for an unknown or disabled
// server, 409 when no session is waiting for sign-in on this server or another sign-in stands, 503 while no engine
// runs.
func (reg *mcpRegistry) handleSignIn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	var body mcpReconnectReq
	if !httpreply.DecodeJSON(w, r, &body) {
		return
	}
	if !reg.serverReachable(r.Context(), body.Server) {
		httpreply.NotFound(w, "unknown or disabled MCP server")
		return
	}
	switch err := reg.startSignIn(r.Context(), body.Server); {
	case err == nil:
		w.WriteHeader(http.StatusAccepted)
	case errors.Is(err, errMCPResetNoEngine):
		httpserver.WriteJSONStatus(w, http.StatusServiceUnavailable,
			httpreply.ErrorJSON("The agent engine is not running. Try again once it is back."))
	default:
		httpreply.Conflict(w, err.Error())
	}
}

// resetFailedForLaunch resets, in parallel and for at most launchResetBound, every failed server live control may
// name that is under maxLaunchResets since it last connected. A server awaiting sign-in is left alone: a reset
// re-mints its authorization URL under a paste in progress. Failures are logged, never returned, because a down
// integration must not cost the run.
func (reg *mcpRegistry) resetFailedForLaunch(ctx context.Context) {
	names := reg.claimLaunchResets(ctx)
	if len(names) == 0 {
		return
	}
	slog.Info("mcp: reset failed servers before a scheduled run", "servers", names)
	bctx, cancel := context.WithTimeout(ctx, launchResetBound)
	defer cancel()
	var wg sync.WaitGroup
	for _, name := range names {
		wg.Go(func() {
			err := reg.reconnectServer(bctx, name)
			if errors.Is(err, errMCPResetNoSession) || errors.Is(err, errMCPResetNoEngine) {
				// Nothing was reset, so the attempt is not spent.
				reg.mu.Lock()
				if reg.launchResets[name] > 0 {
					reg.launchResets[name]--
				}
				reg.mu.Unlock()
			}
		})
	}
	wg.Wait()
}

// claimLaunchResets is the failed, reachable servers under the cap, each with one attempt counted.
func (reg *mcpRegistry) claimLaunchResets(ctx context.Context) []string {
	reg.mu.RLock()
	var failed []string
	for name, s := range reg.servers {
		if s.State == mcpStateFailed {
			failed = append(failed, name)
		}
	}
	reg.mu.RUnlock()
	failed = slices.DeleteFunc(failed, func(name string) bool { return !reg.serverReachable(ctx, name) })
	slices.Sort(failed)
	reg.mu.Lock()
	defer reg.mu.Unlock()
	return slices.DeleteFunc(failed, func(name string) bool {
		if reg.launchResets[name] >= maxLaunchResets {
			return true
		}
		reg.launchResets[name]++
		return false
	})
}
