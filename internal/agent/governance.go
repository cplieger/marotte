package agent

// Account/workspace governance state (_kiro/governance/state).
//
// KAS pushes org/account feature-flag policy as an A→C notification on every
// session/new + session/load, re-pushed on a prompt when it changes. The
// chat-bridge copy flows through the normal dispatcher; the UTILITY bridge's
// copy bypasses that dispatcher (its own forward loop) and is captured via
// cacheGovernanceFromUtility. Either path populates the same cache, so the
// state is available with no chat open.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/webhttp/v3"
)

// governanceWarmTimeout bounds the cold GET /api/governance path: starting
// the utility bridge plus waiting for the first push. Generous because a
// cold spawn includes the kiro-cli subprocess start.
const governanceWarmTimeout = 12 * time.Second

// governanceCache holds the latest observed governance state. warm is closed
// exactly once, on the first push, so a cold reader can block for it.
type governanceCache struct {
	state *marotte.GovernanceStatePayload
	warm  chan struct{}
	mu    sync.RWMutex
	once  sync.Once
}

func newGovernanceCache() *governanceCache {
	return &governanceCache{warm: make(chan struct{})}
}

func (c *governanceCache) set(p marotte.GovernanceStatePayload) {
	c.mu.Lock()
	cp := p
	c.state = &cp
	c.mu.Unlock()
	c.once.Do(func() { close(c.warm) })
}

func (c *governanceCache) get() (marotte.GovernanceStatePayload, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.state == nil {
		return marotte.GovernanceStatePayload{}, false
	}
	return *c.state, true
}

// SetGovernance caches the latest governance state. Satisfies
// translate.GovernanceAccess (called from the chat-bridge path).
func (st *Settings) SetGovernance(p marotte.GovernanceStatePayload) {
	st.governance.set(p)
}

// cacheGovernanceFromUtility captures the utility bridge's copy of
// _kiro/governance/state (bypasses the main dispatcher) and broadcasts the
// same account-global governance_state SSE the chat path does.
func (st *Settings) cacheGovernanceFromUtility(raw json.RawMessage) {
	payload, ok := translate.DecodeGovernanceState(raw)
	if !ok {
		return
	}
	st.governance.set(payload)
	st.broadcast(st.lifecycle.shutdownCtx, marotte.NewEvent(marotte.EventGovernanceState, "", payload))
}

// Governance returns the cached governance state. A warm cache returns
// immediately; a cold cache starts the utility bridge and waits up to
// governanceWarmTimeout for the first push. A failed warm returns
// Known=false so the client leaves governed affordances at their permissive
// default rather than reading the zero value as "everything disabled".
func (st *Settings) Governance(ctx context.Context) marotte.GovernanceStatePayload {
	if p, ok := st.governance.get(); ok {
		return p
	}
	u := st.utility()
	warmCtx, cancel := context.WithTimeout(ctx, governanceWarmTimeout)
	defer cancel()
	if err := u.session.ensureStarted(warmCtx); err != nil {
		slog.Warn("governance: utility bridge start failed", "error", err)
		return marotte.GovernanceStatePayload{}
	}
	select {
	case <-st.governance.warm:
	case <-warmCtx.Done():
	}
	if p, ok := st.governance.get(); ok {
		return p
	}
	return marotte.GovernanceStatePayload{}
}

// handleGovernance: GET /api/governance → cached governance state.
func (st *Settings) handleGovernance(w http.ResponseWriter, r *http.Request) {
	webhttp.WriteJSON(w, st.Governance(r.Context()))
}

// registerGovernanceRoutes wires the governance snapshot endpoint.
func (st *Settings) registerGovernanceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/governance", st.handleGovernance)
}
