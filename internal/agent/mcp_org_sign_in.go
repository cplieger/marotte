package agent

// The organization sign-in: one engine-wide identity provider through which KAS signs in every server whose oauth.mode
// is enterprise or auto (_kiro/mcp/identityProvider/*). KAS runs the flow and keeps every token; marotte starts and
// cancels it, shows its page on the MCP tab's card, and relays its stranded callback as it does a server's.

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
	"github.com/cplieger/marotte/internal/rpcerr"
)

const (
	methodV3MCPIdentityProviderSignIn  = "_kiro/mcp/identityProvider/signIn"
	methodV3MCPIdentityProviderSignOut = "_kiro/mcp/identityProvider/signOut"

	// orgSignInTTL is KAS's callback wait for the organization sign-in (EId, five minutes).
	orgSignInTTL = 300 * time.Second
	// orgSignInTimeout bounds signIn, which KAS answers only once the sign-in ends: its callback wait plus up to
	// four identity-provider requests at KAS's 30 s deadline each (discovery and the code exchange). A shorter
	// budget turns a late success into a failure on the card.
	orgSignInTimeout = 420 * time.Second
)

// KAS's identityProvider states marotte acts on; the rest pass through verbatim.
const (
	orgStateSigningIn = "signing-in"
	orgStateSignedIn  = "signed-in"
	orgStateSignedOut = "signed-out"
)

var errOrgSignInInProgress = errors.New("an organization sign-in is already in progress")

// orgSignIn is an organization sign-in started from marotte. It stands until its signIn call answers, a report says
// signed-in, the user signs out, or the engine ends; deadline is when KAS stops waiting for the callback.
type orgSignIn struct {
	deadline time.Time
	attempt  uint64
}

// startOrgSignIn claims the organization sign-in and sends signIn in the background. Unlike a server's reset, the
// reply is the verdict: with no live session KAS sends no status, so the reply is all marotte hears.
func (reg *mcpRegistry) startOrgSignIn() error {
	reg.mu.Lock()
	if reg.orgSignIn.attempt != 0 {
		reg.mu.Unlock()
		return errOrgSignInInProgress
	}
	reg.signIns++
	attempt := reg.signIns
	reg.orgSignIn = orgSignIn{attempt: attempt, deadline: reg.now().Add(orgSignInTTL)}
	reg.orgFailure = ""
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
		reg.releaseOrgSignIn(attempt)
		return errMCPResetNoEngine
	}
	return nil
}

func (reg *mcpRegistry) sendOrgSignIn(h engineHandle, attempt uint64) {
	ctx, cancel := context.WithTimeout(reg.lifetime.shutdownCtx, orgSignInTimeout)
	defer cancel()
	_, err := h.Call(ctx, methodV3MCPIdentityProviderSignIn, map[string]any{})
	if err != nil {
		slog.Warn("mcp organization sign-in: kiro-cli refused or did not finish the sign-in", "error", err)
	}
	reg.settleOrgSignIn(attempt, err)
}

// settleOrgSignIn ends attempt's sign-in with its reply: a success reads signed-in until the next report names the
// account, an error stays on the card. A sign-in no longer attempt's (signed out, reported signed-in, or the engine
// ended) has its state already, so nothing is recorded.
func (reg *mcpRegistry) settleOrgSignIn(attempt uint64, err error) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.orgSignIn.attempt != attempt {
		return
	}
	reg.endOrgSignInLocked()
	if err != nil {
		reg.orgFailure = rpcerr.Text(err)
	} else {
		reg.orgStatus = withOrgState(reg.orgStatus, orgStateSignedIn)
	}
	reg.commitLocked()
}

// releaseOrgSignIn ends attempt's sign-in, which never reached the engine; the route answers why.
func (reg *mcpRegistry) releaseOrgSignIn(attempt uint64) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.orgSignIn.attempt == attempt {
		reg.endOrgSignInLocked()
		reg.commitLocked()
	}
}

// signOutOrg sends signOut, which also discards a sign-in in progress, so it is the card's Cancel too.
func (reg *mcpRegistry) signOutOrg(ctx context.Context) error {
	h, release, ok := reg.engine.tryDoor()
	if !ok {
		return errMCPResetNoEngine
	}
	defer release()
	cctx, cancel := context.WithTimeout(ctx, mcpReconnectTimeout)
	defer cancel()
	if _, err := h.Call(cctx, methodV3MCPIdentityProviderSignOut, map[string]any{}); err != nil {
		slog.Warn("mcp organization sign-out: kiro-cli refused or did not answer", "error", err)
		return err
	}
	reg.mu.Lock()
	reg.endOrgSignInLocked()
	reg.orgFailure = ""
	if reg.orgStatus != nil && reg.orgStatus.State != "invalid-config" {
		reg.orgStatus = withOrgState(reg.orgStatus, orgStateSignedOut)
	}
	reg.commitLocked()
	reg.mu.Unlock()
	return nil
}

// recordOrgLocked takes a frame's identity provider. Leaving signing-in ends the URLs KAS opened for it, since its
// listener has closed; a standing sign-in waits for its reply, which carries the reason a failure has, unless the
// report already says signed-in.
func (reg *mcpRegistry) recordOrgLocked(st *marotte.MCPIdentityProviderState) {
	was := reg.orgStatus
	reg.orgStatus = st
	if st != nil && st.State == orgStateSignedIn {
		reg.endOrgSignInLocked()
		reg.orgFailure = ""
		return
	}
	if was != nil && was.State == orgStateSigningIn && (st == nil || st.State != orgStateSigningIn) {
		reg.dropOrgAnchorsLocked()
	}
}

// endOrgSignInLocked ends the standing organization sign-in, its anchors with it; the caller commits.
func (reg *mcpRegistry) endOrgSignInLocked() {
	reg.orgSignIn = orgSignIn{}
	reg.dropOrgAnchorsLocked()
}

func (reg *mcpRegistry) dropOrgAnchorsLocked() {
	reg.anchors = slices.DeleteFunc(reg.anchors, func(a *signInAnchor) bool { return a.org })
}

// projectOrgLocked is the organization sign-in the GET serves and mcp_identity_provider announces. The read lock
// suffices.
func (reg *mcpRegistry) projectOrgLocked() marotte.MCPIdentityProviderStatus {
	st := marotte.MCPIdentityProviderStatus{Reported: reg.orgStatus, Error: reg.orgFailure}
	if s := reg.orgSignIn; s.attempt != 0 {
		st.SignInDeadline = &s.deadline
		if a := reg.attemptAnchorLocked(s.attempt); a != nil {
			st.SignInURL, st.Relayed = a.authURL, a.relayed
		}
	}
	return st
}

func sameOrgStatus(a, b *marotte.MCPIdentityProviderStatus) bool {
	sameReported := a.Reported == nil && b.Reported == nil || a.Reported != nil && b.Reported != nil && *a.Reported == *b.Reported
	sameDeadline := a.SignInDeadline == nil && b.SignInDeadline == nil ||
		a.SignInDeadline != nil && b.SignInDeadline != nil && a.SignInDeadline.Equal(*b.SignInDeadline)
	return sameReported && sameDeadline && a.SignInURL == b.SignInURL && a.Error == b.Error && a.Relayed == b.Relayed
}

// withOrgState is st with state, a new value since a committed status shares it; nil stays nil for signed-out and
// becomes a bare report for signed-in, the account filled in by the next frame.
func withOrgState(st *marotte.MCPIdentityProviderState, state string) *marotte.MCPIdentityProviderState {
	if st == nil && state != orgStateSignedIn {
		return nil
	}
	next := marotte.MCPIdentityProviderState{State: state}
	if st != nil {
		next.Issuer, next.DisplayName = st.Issuer, st.DisplayName
	}
	return &next
}

// handleOrgSignIn serves POST /api/mcp/identity-provider/sign-in: 202 once the sign-in is queued, 409 while one
// stands, 503 while no engine runs. kiro-cli's own refusals (no provider set up, MCP turned off by the organization)
// arrive as the sign-in's verdict on the card.
func (reg *mcpRegistry) handleOrgSignIn(w http.ResponseWriter, r *http.Request) {
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

// handleOrgSignOut serves POST /api/mcp/identity-provider/sign-out, which also cancels a sign-in in progress: 204
// once kiro-cli signed out, 503 while no engine runs, 502 when it refused or did not answer.
func (reg *mcpRegistry) handleOrgSignOut(w http.ResponseWriter, r *http.Request) {
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
		httpserver.WriteJSONStatus(w, http.StatusBadGateway, httpreply.ErrorJSON("kiro-cli did not sign out."))
	}
}
