package agent

import (
	"maps"
	"slices"

	"github.com/cplieger/marotte/internal/marotte"
)

const (
	lockReasonSet          = "Set by your organization"
	lockReasonWebTools     = "Web tools are turned off by your organization"
	lockReasonSubagents    = "Subagents are blocked by your organization"
	lockReasonPowers       = "Powers are blocked by your organization"
	lockReasonMCPBlocked   = "MCP servers are blocked by your organization"
	lockReasonPolicyFailed = "Your organization's tool policy failed to load, so every tool is blocked"
)

// adminPolicy is the administrator's managed-settings rules reduced to the lock map's needs:
// capabilities refused outright and whether any rule exists. fatal is KAS's fail-closed state.
type adminPolicy struct {
	denied map[string]bool
	any    bool
	fatal  bool
}

// failClosedPolicy is KAS's state after a fatal administration-file error: no rules, every tool call denied.
func failClosedPolicy() adminPolicy {
	p := adminPolicy{denied: map[string]bool{}, any: true, fatal: true}
	for _, c := range capabilityGroups["all"] {
		p.denied[c] = true
	}
	return p
}

func hasFatalAdminError(errs []marotte.PolicyErrorItem) bool {
	return slices.ContainsFunc(errs, func(e marotte.PolicyErrorItem) bool {
		return e.Fatal && e.Scope == "administration"
	})
}

// capabilityGroups mirrors KAS's capability aliases (2.27.0 acp-server.js `X$t`); a group rule applies to every member.
var capabilityGroups = map[string][]string{
	"all":        append(slices.Clone(builtinCapabilities), "mcp"),
	"builtin":    builtinCapabilities,
	"filesystem": {"fs_read", "fs_write"},
}

// builtinCapabilities is KAS's `builtin` group; `all` is it plus `mcp`.
var builtinCapabilities = []string{
	"fs_read", "fs_write", "shell", "web_fetch", "web_search", "subagent", "skill", "power", "context", "diagnostics",
}

// reduceAdminRules records each capability an administration deny rule refuses for every
// resource. A glob-narrowed deny pins nothing: KAS removes tools only for an outright refusal.
func reduceAdminRules(rules []marotte.PolicyRule) adminPolicy {
	p := adminPolicy{denied: map[string]bool{}}
	for _, r := range rules {
		if r.Scope != "administration" {
			continue
		}
		p.any = true
		if r.Effect != "deny" || !matchesEverything(r.Match) || len(r.Exclude) > 0 {
			continue
		}
		members, ok := capabilityGroups[r.Capability]
		if !ok {
			members = []string{r.Capability}
		}
		for _, c := range members {
			p.denied[c] = true
		}
	}
	return p
}

func matchesEverything(match []string) bool {
	return len(match) == 0 || slices.ContainsFunc(match, func(m string) bool { return m == "*" || m == "**" })
}

// composeLocks is the one place a governance source becomes a lock. Profile governance binds
// only an enterprise identity: KAS resolves it for Enterprise and ExternalIdp alone.
func composeLocks(profile *marotte.GovernanceStatePayload, admin adminPolicy) map[string]marotte.GovernanceLock {
	locks := map[string]marotte.GovernanceLock{}
	org := func(key, reason string, value bool) {
		locks[key] = marotte.GovernanceLock{Value: value, Source: marotte.LockSourceOrganization, Reason: reason}
	}
	adm := func(key, reason string) {
		if admin.fatal {
			reason = lockReasonPolicyFailed
		}
		if _, ok := locks[key]; !ok {
			locks[key] = marotte.GovernanceLock{Value: false, Source: marotte.LockSourceAdministrator, Reason: reason}
		}
	}
	if profile != nil && profile.Known && profile.IsEnterprise {
		org(marotte.LockContentCollection, lockReasonSet, profile.Features.ContentCollection)
		org(marotte.LockTelemetry, lockReasonSet, profile.Features.UsageAnalytics)
		if !profile.Features.WebToolsEnabled {
			org(marotte.LockWebTools, lockReasonWebTools, false)
		}
	}
	if admin.denied["web_fetch"] && admin.denied["web_search"] {
		adm(marotte.LockWebTools, lockReasonWebTools)
	}
	if admin.denied["subagent"] {
		adm(marotte.LockWorkflows, lockReasonSubagents)
		adm(marotte.LockInlineAgents, lockReasonSubagents)
	}
	if admin.denied["power"] {
		adm(marotte.LockPowers, lockReasonPowers)
	}
	if admin.denied["mcp"] {
		adm(marotte.LockMCP, lockReasonMCPBlocked)
	}
	if len(locks) == 0 {
		return nil
	}
	return locks
}

// lockedBool answers the lock's value for a locked setting, else the stored one.
func lockedBool(locks map[string]marotte.GovernanceLock, key string, stored bool) bool {
	if l, ok := locks[key]; ok {
		return l.Value
	}
	return stored
}

func currentLocks(f func() map[string]marotte.GovernanceLock) map[string]marotte.GovernanceLock {
	if f == nil {
		return nil
	}
	return f()
}

func locksEqual(a, b map[string]marotte.GovernanceLock) bool {
	return maps.Equal(a, b)
}
