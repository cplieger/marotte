// Stateless KAS RPC wrappers over the utility session. Each Call runs outside any lock, so none waits on the text
// turn mutex.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cplieger/marotte/internal/marotte"
)

// rawCall is the shared RPC core: acquire, Call, resetIf on error. params receives the leased bridge; label prefixes
// errors.
func (us *utilitySession) rawCall(ctx context.Context, label, method string, params func(bridge acpSession) map[string]any) (json.RawMessage, error) {
	raw, _, err := us.rawCallAt(ctx, label, method, params)
	return raw, err
}

// rawCallAt is rawCall plus the read-loop position and session of the response. KAS replays a step's session as
// notifications before the load result, so ordering a decision against them needs the position.
func (us *utilitySession) rawCallAt(ctx context.Context, label, method string, params func(bridge acpSession) map[string]any) (json.RawMessage, drainPoint, error) {
	lease, err := us.acquire(ctx)
	if err != nil {
		return nil, drainPoint{}, err
	}
	at := drainPoint{gen: lease.gen}
	resp, seq, err := lease.bridge.CallAt(ctx, method, params(lease.bridge))
	at.seq = seq
	if err != nil {
		// A caller walking away does not make the bridge unhealthy (startLocked spawns on shutdownCtx); a deadline still
		// resets, since that one may be wedged.
		if !errors.Is(ctx.Err(), context.Canceled) {
			us.resetIf(lease.gen)
		}
		return nil, at, fmt.Errorf("%s: %w", label, err)
	}
	if resp == nil {
		return nil, at, errors.New(label + ": nil response")
	}
	// KAS's in-band refusal is an error, not an empty reply: callers must tell an unknown workflow from a fault and a
	// refused cancel from a landed one. Wrapped so rpcerr.Details reaches the *marotte.RPCError.
	if resp.Error != nil {
		return nil, at, fmt.Errorf("%s: %w", label, resp.Error)
	}
	return resp.Result, at, nil
}

// notifyIfLive sends a notification to a running session and does nothing for
// a stopped one, which reads the current state when it next starts.
func (us *utilitySession) notifyIfLive(ctx context.Context, method string, params any) error {
	us.mu.Lock()
	running, b := us.started, us.bridge
	us.mu.Unlock()
	if !running || b == nil {
		return nil
	}
	return b.Notify(ctx, method, params)
}

// callerParams adapts a fixed caller-supplied parameter map (no session id) to rawCall's params signature.
func callerParams(params map[string]any) func(acpSession) map[string]any {
	return func(acpSession) map[string]any { return params }
}

// scopedParams builds session-scoped params (session id + extras) from the lease.
func scopedParams(extra map[string]any) func(acpSession) map[string]any {
	return func(bridge acpSession) map[string]any { return utilitySessionParams(bridge, extra) }
}

// codeIntelligenceInit issues _kiro/codeIntelligence init: KAS detects the workspace languages and writes
// .kiro/settings/lsp.json, leaving an existing config alone. Returns KAS's message.
func (us *utilitySession) codeIntelligenceInit(ctx context.Context) (string, error) {
	raw, err := us.rawCall(ctx, "code intelligence init", methodKiroCodeIntel,
		scopedParams(map[string]any{"subcommand": "init"}))
	if err != nil {
		return "", err
	}
	var out struct {
		Message string `json:"message"`
		Success bool   `json:"success"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("code intelligence init: decode: %w", err)
	}
	if !out.Success {
		return "", fmt.Errorf("code intelligence init: %s", out.Message)
	}
	return out.Message, nil
}

// accountUsageRaw issues _kiro/account/getUsage and returns the raw result; it needs only a live authenticated session.
func (us *utilitySession) accountUsageRaw(ctx context.Context) (json.RawMessage, error) {
	return us.rawCall(ctx, "account usage call", methodKiroGetUsage, scopedParams(nil))
}

// knowledgeRaw issues a _kiro/knowledge request and returns the raw result. No sessionId: that targets the
// workspace-global default store.
func (us *utilitySession) knowledgeRaw(ctx context.Context, params map[string]any) (json.RawMessage, error) {
	return us.rawCall(ctx, "knowledge call", methodKiroKnowledge, callerParams(params))
}

// memoryRaw issues a _kiro/memory/* request without a sessionId, so KAS resolves memory from the account entitlement
// rather than the utility session's disabled preference.
func (us *utilitySession) memoryRaw(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	return us.rawCall(ctx, "memory call "+method, method, callerParams(params))
}

// policyRaw issues a read-only, session-scoped _kiro/permissions/* request and returns the raw result.
func (us *utilitySession) policyRaw(ctx context.Context, method string, extra map[string]any) (json.RawMessage, error) {
	return us.rawCall(ctx, fmt.Sprintf("policy call %s", method), method, scopedParams(extra))
}

// hooksRaw issues a non-session _kiro/hooks request (list / setEnabled).
func (us *utilitySession) hooksRaw(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	return us.rawCall(ctx, fmt.Sprintf("hooks call %s", method), method, callerParams(params))
}

// configTemplateRaw issues the session-less _kiro/config/template request (kiro-cli 2.14+) and returns the raw result.
func (us *utilitySession) configTemplateRaw(ctx context.Context) (json.RawMessage, error) {
	return us.rawCall(ctx, "config template call", methodKiroConfigTemplate, callerParams(nil))
}

// No triggerHook wrapper: it runs `sh -c` on a command a hook file names.

// renameSessionRaw renames a session no live process here holds, through _kiro/session/rename's stored-metadata arm.
func (us *utilitySession) renameSessionRaw(ctx context.Context, sessionID, title string) (json.RawMessage, error) {
	return us.rawCall(ctx, "session rename", marotte.MethodSessionRename,
		callerParams(map[string]any{"sessionId": sessionID, "title": title}))
}

// exportSessionRaw exports a session no live process here holds; KAS loads it
// from disk.
func (us *utilitySession) exportSessionRaw(ctx context.Context, sessionID string) (json.RawMessage, error) {
	return us.rawCall(ctx, "session export", marotte.MethodSessionExport,
		callerParams(map[string]any{marotte.KeySessionID: sessionID}))
}
