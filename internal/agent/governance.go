package agent

// Account and workspace governance: the account profile, the organization's MCP registry and the
// administrator's rules compose into GovernanceStatePayload.Locks. KAS enforces mcpEnabled,
// webToolsEnabled, usageAnalytics and codeReferenceTracker; autonomousAgents, promptLogging and
// contentCollection it only reports, so any lock on those is marotte's own.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"reflect"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/webhttp/v3"
)

// governanceWarmTimeout bounds the cold GET /api/governance: a utility bridge spawn plus the first push.
const governanceWarmTimeout = 12 * time.Second

// governanceCache holds the three sources and their composed payload; warm closes once, on the first profile.
type governanceCache struct {
	profile  *marotte.GovernanceStatePayload
	registry *marotte.GovernanceMCPRegistry
	snapshot *marotte.GovernanceStatePayload
	warm     chan struct{}
	admin    adminPolicy
	// adminFailed overrides admin with failClosedPolicy while the managed file fails to load; only a policy reload clears it.
	adminFailed bool
	// adminKnown is false until the administrator rules were read or failed closed; Powers' MCP servers stay suppressed until then.
	adminKnown bool
	mu         sync.RWMutex
	once       sync.Once
}

func newGovernanceCache() *governanceCache {
	return &governanceCache{warm: make(chan struct{})}
}

// governanceChange is one recomposition: the snapshot, the locks it replaced, and whether clients see a change.
type governanceChange struct {
	prevLocks map[string]marotte.GovernanceLock
	next      marotte.GovernanceStatePayload
	changed   bool
	// adminResolved is the first update that made the administrator rules known.
	adminResolved bool
}

// update applies one source change under the lock and recomposes.
func (c *governanceCache) update(apply func()) governanceChange {
	c.mu.Lock()
	defer c.mu.Unlock()
	wasKnown := c.adminKnown
	apply()
	var prev marotte.GovernanceStatePayload
	if c.snapshot != nil {
		prev = *c.snapshot
	}
	next := c.composeLocked()
	c.snapshot = &next
	return governanceChange{
		next: next, prevLocks: prev.Locks, changed: !reflect.DeepEqual(prev, next),
		adminResolved: !wasKnown && c.adminKnown,
	}
}

func (c *governanceCache) composeLocked() marotte.GovernanceStatePayload {
	var out marotte.GovernanceStatePayload
	if c.profile != nil {
		out = *c.profile
	}
	admin := c.admin
	if c.adminFailed {
		admin = failClosedPolicy()
	}
	out.Locks = composeLocks(c.profile, admin)
	out.MCPRegistry = c.registry
	out.AdminRestricted = admin.any
	return out
}

func (c *governanceCache) setProfile(p marotte.GovernanceStatePayload) governanceChange {
	p.Locks, p.MCPRegistry, p.AdminRestricted = nil, nil, false
	ch := c.update(func() { c.profile = &p })
	c.once.Do(func() { close(c.warm) })
	return ch
}

func (c *governanceCache) setRegistry(r *marotte.GovernanceMCPRegistry) governanceChange {
	return c.update(func() { c.registry = r })
}

func (c *governanceCache) setAdmin(a adminPolicy) governanceChange {
	return c.update(func() { c.admin, c.adminKnown = a, true })
}

func (c *governanceCache) setAdminFailed(failed bool) governanceChange {
	return c.update(func() {
		c.adminFailed = failed
		c.adminKnown = c.adminKnown || failed
	})
}

func (c *governanceCache) adminPolicyKnown() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.adminKnown
}

// get answers the composed payload; ok is false until the first profile arrived.
func (c *governanceCache) get() (marotte.GovernanceStatePayload, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.profile == nil || c.snapshot == nil {
		return marotte.GovernanceStatePayload{}, false
	}
	return *c.snapshot, true
}

// peek answers the composed payload, Known false before the first profile; a cold payload still carries the administrator's locks.
func (c *governanceCache) peek() marotte.GovernanceStatePayload {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.snapshot == nil {
		return marotte.GovernanceStatePayload{}
	}
	return *c.snapshot
}

// locks answers the lock map in force, warm or cold.
func (c *governanceCache) locks() map[string]marotte.GovernanceLock {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.snapshot == nil {
		return nil
	}
	return c.snapshot.Locks
}

// SetGovernance caches the account profile. Satisfies translate.GovernanceAccess.
func (st *Settings) SetGovernance(ctx context.Context, p marotte.GovernanceStatePayload) {
	st.publishGovernance(ctx, st.governance.setProfile(p))
}

// SetMCPRegistry caches the organization's MCP registry. Satisfies translate.GovernanceAccess.
func (st *Settings) SetMCPRegistry(ctx context.Context, r *marotte.GovernanceMCPRegistry) {
	st.publishGovernance(ctx, st.governance.setRegistry(r))
}

// PolicyChanged re-reads the administrator rules after a policy notification. A fatal
// administration error fails every lock closed until a clean reload. Satisfies translate.GovernanceAccess.
func (st *Settings) PolicyChanged(ctx context.Context, errs []marotte.PolicyErrorItem, reloaded bool) {
	if failed := hasFatalAdminError(errs); failed || reloaded {
		st.publishGovernance(ctx, st.governance.setAdminFailed(failed))
	}
	st.refreshAdminPolicy()
}

// GovernanceLocks answers the lock map in force.
func (st *Settings) GovernanceLocks() map[string]marotte.GovernanceLock {
	return st.governance.locks()
}

// AdminPolicyKnown reports whether the administrator rules were read or failed closed.
func (st *Settings) AdminPolicyKnown() bool {
	return st.governance.adminPolicyKnown()
}

// publishGovernance broadcasts a changed snapshot and runs the lock hook when the locks moved.
func (st *Settings) publishGovernance(ctx context.Context, ch governanceChange) {
	// Before the changed check: rules that lock nothing leave the snapshot unchanged.
	if ch.adminResolved && st.onAdminResolved != nil {
		st.onAdminResolved()
	}
	if !ch.changed {
		return
	}
	if st.broadcast != nil {
		st.broadcast(ctx, marotte.NewEvent(marotte.EventGovernanceState, "", ch.next))
	}
	if st.onLocksChanged == nil || locksEqual(ch.prevLocks, ch.next.Locks) || st.lifecycle == nil {
		return
	}
	// Off the caller's goroutine: a chat's forward loop may be the caller, and the hook calls that chat's bridge.
	st.lifecycle.goUnlessDraining(func() { st.onLocksChanged(st.lifecycle.shutdownCtx) })
}

// cacheGovernanceFromUtility captures the utility bridge's profile and re-reads the administrator
// rules: this arrives once per utility start and KAS does not watch the managed file.
func (st *Settings) cacheGovernanceFromUtility(raw json.RawMessage) {
	payload, ok := translate.DecodeGovernanceState(raw)
	if !ok {
		return
	}
	st.SetGovernance(st.lifecycle.shutdownCtx, payload)
	st.refreshAdminPolicy()
}

// adminRefresh coalesces administrator-rule reads: one in flight, at most one queued.
type adminRefresh struct {
	mu      sync.Mutex
	running bool
	again   bool
}

// refreshAdminPolicy schedules an administrator-rule read off the caller's goroutine: the utility
// forward loop is a caller, and a synchronous utility Call there deadlocks.
func (st *Settings) refreshAdminPolicy() {
	if st.utility == nil || st.lifecycle == nil {
		return
	}
	st.adminRefresh.mu.Lock()
	if st.adminRefresh.running {
		st.adminRefresh.again = true
		st.adminRefresh.mu.Unlock()
		return
	}
	st.adminRefresh.running = true
	st.adminRefresh.mu.Unlock()
	if !st.lifecycle.goUnlessDraining(st.runAdminRefresh) {
		st.adminRefresh.mu.Lock()
		st.adminRefresh.running = false
		st.adminRefresh.mu.Unlock()
	}
}

func (st *Settings) runAdminRefresh() {
	ctx := st.lifecycle.shutdownCtx
	for {
		rules, err := st.PolicyList(ctx, "administration")
		if err != nil {
			slog.Warn("governance: reading the administrator rules failed; the previous locks stand", "error", err)
		} else {
			st.publishGovernance(ctx, st.governance.setAdmin(reduceAdminRules(rules)))
		}
		st.adminRefresh.mu.Lock()
		if !st.adminRefresh.again || ctx.Err() != nil {
			st.adminRefresh.running = false
			st.adminRefresh.mu.Unlock()
			return
		}
		st.adminRefresh.again = false
		st.adminRefresh.mu.Unlock()
	}
}

// Governance returns the cached state, starting the utility bridge and waiting up to
// governanceWarmTimeout when cold. A failed warm returns Known=false so clients stay permissive.
func (st *Settings) Governance(ctx context.Context) marotte.GovernanceStatePayload {
	if p, ok := st.governance.get(); ok {
		return p
	}
	u := st.utility()
	warmCtx, cancel := context.WithTimeout(ctx, governanceWarmTimeout)
	defer cancel()
	if err := u.session.ensureStarted(warmCtx); err != nil {
		slog.Warn("governance: utility bridge start failed", "error", err)
		return st.governance.peek()
	}
	select {
	case <-st.governance.warm:
	case <-warmCtx.Done():
	}
	return st.governance.peek()
}

// handleGovernance serves GET /api/governance from the cache.
func (st *Settings) handleGovernance(w http.ResponseWriter, r *http.Request) {
	webhttp.WriteJSON(w, st.Governance(r.Context()))
}

// registerGovernanceRoutes wires the governance snapshot endpoint.
func (st *Settings) registerGovernanceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/governance", st.handleGovernance)
}
