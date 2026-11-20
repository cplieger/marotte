package agent

// The engine's one organization sign-in (KAS keeps one identity provider connection per engine and re-sends its
// state to every session): its status from each _kiro/mcp/status frame, a session's or an observer pass's, the
// user's Sign in and Sign out, and the consent page KAS opens for a Sign in, which rides the status as the card's.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/mcp"
	"github.com/cplieger/marotte/internal/rpcerr"
	"github.com/cplieger/marotte/internal/subject"
	"github.com/cplieger/marotte/internal/translate"
)

const (
	// idpConsentTTL is how long KAS waits for the identity provider's callback (kiro-cli 3.5.0, 300 s).
	idpConsentTTL = 300 * time.Second
	// idpSignInTimeout bounds the signIn call, which KAS answers once the sign-in ends: the callback wait plus up
	// to 30 s each for discovery and the code exchange, and margin.
	idpSignInTimeout = 390 * time.Second
)

// idpWarmTimeout bounds the observer pass a GET opens for a status kiro-cli has not sent: an observer door plus the
// first frame. A var for tests.
var idpWarmTimeout = 12 * time.Second

// errOrgSignInInProgress refuses a second Sign in while one stands.
var errOrgSignInInProgress = errors.New("an organization sign-in is already in progress")

// idpState is the registry's organization sign-in, guarded by its lock.
type idpState struct {
	// status is kiro-cli's last report, nil when it has no provider; known says one arrived since the engine started.
	status *marotte.MCPIdentityProviderStatus
	// recorded is closed and replaced at each report, waking a warm.
	recorded chan struct{}
	// signIn is the Sign in the user started, the zero value when none stands.
	signIn orgSignIn
	// signInError is why kiro-cli ended the last Sign in without signing in.
	signInError string
	// warmedFor is the provider the last warm ran for, so a status that never matches it warms once.
	warmedFor string
	// served is the last committed projection, the one the GET serves and mcp_identity_provider announced.
	served marotte.MCPIdentityProviderPayload
	known  bool
}

// orgSignIn is a standing organization Sign in; attempt is the claim that filed it, as for a server's.
type orgSignIn struct {
	deadline time.Time
	attempt  uint64
}

// clear forgets everything kiro-cli said, for an engine that ended. The served projection stays, so the commit that
// follows announces the change.
func (s *idpState) clear() {
	s.signIn, s.status, s.known, s.signInError, s.warmedFor = orgSignIn{}, nil, false, "", ""
}

// recordIdentityProviderLocked takes a frame's provider block as the engine's.
func (reg *mcpRegistry) recordIdentityProviderLocked(status *marotte.MCPIdentityProviderStatus) {
	reg.idp.status, reg.idp.known = status, true
	if reg.idp.recorded != nil {
		close(reg.idp.recorded)
		reg.idp.recorded = nil
	}
}

// observeIdentityProvider takes an observer pass's _kiro/mcp/status: only its provider block, since the pass's
// session binds servers no chat uses.
func (reg *mcpRegistry) observeIdentityProvider(msg *marotte.RPCResponse) {
	status, ok := translate.DecodeMCPIdentityProvider(msg)
	if !ok {
		return
	}
	reg.mu.Lock()
	reg.recordIdentityProviderLocked(status)
	reg.commitIdentityProviderLocked()
	reg.mu.Unlock()
}

// identityProviderLocked is the status the state gives now. The read lock suffices.
func (reg *mcpRegistry) identityProviderLocked() marotte.MCPIdentityProviderPayload {
	p := marotte.MCPIdentityProviderPayload{Status: reg.idp.status, Known: reg.idp.known, SignInError: reg.idp.signInError}
	now := reg.now()
	if s := reg.idp.signIn; s.attempt != 0 && now.Before(s.deadline) {
		p.SigningIn = true
		if a := reg.attemptAnchorLocked(s.attempt); a != nil && now.Before(a.expires) {
			p.OAuthURL, p.Relayed = a.authURL, a.relayed
		}
	}
	return p
}

// commitIdentityProviderLocked serves the projection, minting `mcp_identity_provider` and broadcasting one stamped
// frame when it changed, under the write lock so frames go out in version order.
func (reg *mcpRegistry) commitIdentityProviderLocked() {
	next := reg.identityProviderLocked()
	if sameIdentityProvider(reg.idp.served, next) {
		return
	}
	reg.idp.served = next
	ev := marotte.NewEvent(marotte.EventMCPIdentityProvider, "", next)
	ev.Subject = marotte.NewSubjectStamp(string(subject.KindMCPIdentityProvider), "",
		reg.versions.BumpCounter(subject.KindMCPIdentityProvider, ""))
	reg.bus.Broadcast(context.Background(), ev)
}

func sameIdentityProvider(a, b marotte.MCPIdentityProviderPayload) bool {
	return samePtr(a.Status, b.Status) && a.Known == b.Known && a.SigningIn == b.SigningIn &&
		a.OAuthURL == b.OAuthURL && a.Relayed == b.Relayed && a.SignInError == b.SignInError
}

// anchorOrganizationLocked anchors an identity provider page to the standing organization Sign in while it has none,
// reporting whether it did; the anchor lives as long as KAS's callback wait.
func (reg *mcpRegistry) anchorOrganizationLocked(state, authURL string, now time.Time) bool {
	s := reg.idp.signIn
	if state == "" || s.attempt == 0 || !now.Before(s.deadline) || reg.attemptAnchorLocked(s.attempt) != nil {
		return false
	}
	reg.addAnchorLocked(&signInAnchor{expires: now.Add(idpConsentTTL), state: state, authURL: authURL, attempt: s.attempt})
	reg.commitLocked()
	return true
}

// identityProviderStatus is the served status and its stamp. When a provider is configured that the status does not
// show (none reported since the engine started, or another issuer), it first warms the status through one observer
// pass, once per configured provider: KAS sends status only at a session door.
func (reg *mcpRegistry) identityProviderStatus(ctx context.Context, cfg *mcp.IdentityProvider) (marotte.MCPIdentityProviderPayload, *marotte.SubjectStamp) {
	if key := idpConfigKey(cfg); key != "" {
		reg.mu.Lock()
		warm := key != reg.idp.warmedFor && !reg.idpShowsLocked(cfg)
		if warm {
			reg.idp.warmedFor = key
		}
		reg.mu.Unlock()
		if warm {
			wctx, cancel := context.WithTimeout(ctx, idpWarmTimeout)
			if err := reg.warmIdentityProvider(wctx); err != nil {
				slog.Warn("mcp: the observer pass that reads the organization sign-in failed", "error", err)
			}
			cancel()
		}
	}
	p, version := reg.identityProviderStamped()
	stamp := marotte.NewSubjectStamp(string(subject.KindMCPIdentityProvider), "", version)
	stamp.Epoch = reg.epoch()
	return p, stamp
}

// identityProviderStamped is the served status with the version it was served at, read under one lock.
func (reg *mcpRegistry) identityProviderStamped() (p marotte.MCPIdentityProviderPayload, version string) {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	version, _ = reg.versions.Current(subject.KindMCPIdentityProvider, "")
	return reg.idp.served, version
}

// idpConfigKey identifies a configured provider as KAS keys its session, "" for none.
func idpConfigKey(cfg *mcp.IdentityProvider) string {
	if cfg == nil {
		return ""
	}
	return cfg.Issuer + "|" + cfg.ClientID
}

// idpShowsLocked reports whether the status already describes cfg: invalid-config names no issuer.
func (reg *mcpRegistry) idpShowsLocked(cfg *mcp.IdentityProvider) bool {
	st := reg.idp.status
	return reg.idp.known && st != nil && (st.State == marotte.MCPIdentityProviderInvalidConfig || st.Issuer == cfg.Issuer)
}

// warmIdentityProvider holds an observer pass open until a status arrives or ctx ends.
func (reg *mcpRegistry) warmIdentityProvider(ctx context.Context) error {
	reg.mu.Lock()
	if reg.idp.recorded == nil {
		reg.idp.recorded = make(chan struct{})
	}
	recorded := reg.idp.recorded
	reg.mu.Unlock()
	return reg.observe(ctx, func(ctx context.Context, _ engineSession) error {
		select {
		case <-recorded:
		case <-ctx.Done():
		}
		return nil
	})
}

// refreshIdentityProvider re-reads the status in the background once a Sign in or Sign out ended while no session
// is open: KAS re-sends it only to sessions it holds.
func (reg *mcpRegistry) refreshIdentityProvider() {
	reg.mu.RLock()
	viewless := len(reg.views) == 0
	reg.mu.RUnlock()
	if !viewless {
		return
	}
	reg.lifetime.goUnlessDraining(func() {
		ctx, cancel := context.WithTimeout(reg.lifetime.shutdownCtx, idpWarmTimeout)
		defer cancel()
		if err := reg.warmIdentityProvider(ctx); err != nil {
			slog.Debug("mcp: re-reading the organization sign-in failed", "error", err)
		}
	})
}

// startOrgSignIn files the organization Sign in and calls signIn on the engine in the background: KAS answers once
// the sign-in ends, after up to idpConsentTTL waiting for the callback.
func (reg *mcpRegistry) startOrgSignIn() error {
	reg.mu.Lock()
	now := reg.now()
	if s := reg.idp.signIn; s.attempt != 0 && now.Before(s.deadline) {
		reg.mu.Unlock()
		return errOrgSignInInProgress
	}
	reg.signIns++
	attempt := reg.signIns
	reg.idp.signIn = orgSignIn{deadline: now.Add(idpSignInTimeout), attempt: attempt}
	reg.idp.signInError = ""
	reg.commitLocked()
	reg.mu.Unlock()
	started := reg.lifetime.goClaimed(func() func() {
		h, release, ok := reg.engine.tryDoor()
		if !ok {
			return nil
		}
		return func() {
			defer release()
			reg.sendOrgSignIn(h, attempt)
		}
	})
	if !started {
		reg.endOrgSignIn(attempt, "")
		return errMCPResetNoEngine
	}
	return nil
}

func (reg *mcpRegistry) sendOrgSignIn(h engineHandle, attempt uint64) {
	ctx, cancel := context.WithTimeout(reg.lifetime.shutdownCtx, idpSignInTimeout)
	defer cancel()
	_, err := h.Call(ctx, methodKiroMCPIdPSignIn, map[string]any{})
	reason := ""
	if err != nil {
		reason = rpcerr.Text(err)
		slog.Info("mcp: the organization sign-in ended without signing in", "error", err)
	}
	if reg.endOrgSignIn(attempt, reason) {
		reg.refreshIdentityProvider()
	}
}

// endOrgSignIn ends attempt's Sign in and its consent page, keeping reason for the card, and reports whether attempt
// still held it: a Sign in ended since (Sign out, the engine's end) records nothing.
func (reg *mcpRegistry) endOrgSignIn(attempt uint64, reason string) bool {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.idp.signIn.attempt != attempt {
		return false
	}
	reg.dropOrgSignInLocked()
	reg.idp.signInError = reason
	reg.commitLocked()
	return true
}

// dropOrgSignInLocked ends the standing organization Sign in and its anchor.
func (reg *mcpRegistry) dropOrgSignInLocked() {
	if a := reg.idp.signIn.attempt; a != 0 {
		reg.anchors = slices.DeleteFunc(reg.anchors, func(x *signInAnchor) bool { return x.attempt == a })
	}
	reg.idp.signIn = orgSignIn{}
}

// signOutOrg calls signOut on the engine, which also discards a sign-in in progress, then ends the standing Sign in.
func (reg *mcpRegistry) signOutOrg(ctx context.Context) error {
	h, release, ok := reg.engine.tryDoor()
	if !ok {
		return errMCPResetNoEngine
	}
	defer release()
	cctx, cancel := context.WithTimeout(ctx, mcpReconnectTimeout)
	defer cancel()
	if _, err := h.Call(cctx, methodKiroMCPIdPSignOut, map[string]any{}); err != nil {
		slog.Warn("mcp: the engine refused or did not answer the organization sign-out", "error", err)
		return err
	}
	reg.mu.Lock()
	reg.dropOrgSignInLocked()
	reg.idp.signInError = ""
	reg.commitLocked()
	reg.mu.Unlock()
	reg.refreshIdentityProvider()
	return nil
}

// handleIdentityProviderSignIn serves POST /api/mcp/identity-provider/sign-in: 202 once the sign-in is queued, 409
// while one stands, 503 while no engine runs.
func (reg *mcpRegistry) handleIdentityProviderSignIn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	switch err := reg.startOrgSignIn(); {
	case err == nil:
		w.WriteHeader(http.StatusAccepted)
	case errors.Is(err, errMCPResetNoEngine):
		httpserver.WriteJSONStatus(w, http.StatusServiceUnavailable,
			httpreply.ErrorJSON("The agent engine is not running. Try again once it is back."))
	default:
		httpreply.Conflict(w, err.Error())
	}
}

// handleIdentityProviderSignOut serves POST /api/mcp/identity-provider/sign-out, the card's Sign out and Cancel:
// 204 once kiro-cli signed out, 503 while no engine runs, 502 when it refused or did not answer.
func (reg *mcpRegistry) handleIdentityProviderSignOut(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	switch err := reg.signOutOrg(r.Context()); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, errMCPResetNoEngine):
		httpserver.WriteJSONStatus(w, http.StatusServiceUnavailable,
			httpreply.ErrorJSON("The agent engine is not running. Try again once it is back."))
	default:
		httpserver.WriteJSONStatus(w, http.StatusBadGateway, httpreply.ErrorJSON("kiro-cli did not sign out of the organization."))
	}
}

// MCPIdentityProviderStatus is the organization sign-in's status for the provider configured now
// (mcp.IdentityProviderStatusFunc).
func (rt *Runtime) MCPIdentityProviderStatus(ctx context.Context, cfg *mcp.IdentityProvider) (marotte.MCPIdentityProviderPayload, *marotte.SubjectStamp) {
	return rt.mcpRegistry.identityProviderStatus(ctx, cfg)
}
