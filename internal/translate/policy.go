package translate

// Native-policy notifications: KAS watches permissions.{yaml,json} and emits
// _kiro/policy/changed or _kiro/policy/error, broadcast with an EMPTY chatID (the policy is
// global).

import (
	"context"

	"github.com/cplieger/marotte/internal/marotte"
)

// v3PolicyChanged mirrors _kiro/policy/changed.
type v3PolicyChanged struct {
	SessionID string                    `json:"sessionId"`
	Status    string                    `json:"status"`
	Errors    []marotte.PolicyErrorItem `json:"errors"`
}

// HandlePolicyChanged translates _kiro/policy/changed → the
// permissions_changed SSE. Clients refetch the native policy view.
func (t *Translator) HandlePolicyChanged(ctx context.Context, _ marotte.ChatID, msg *marotte.RPCResponse) {
	p, ok := unmarshalParams[v3PolicyChanged](msg, "policy/changed")
	if !ok {
		return
	}
	t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventPermissionsChanged, "", marotte.PermissionsChangedPayload{
		Status: p.Status,
		Errors: p.Errors,
	}))
	t.governance.PolicyChanged(ctx, p.Errors, true)
}

// v3PolicyError mirrors _kiro/policy/error.
type v3PolicyError struct {
	SessionID string                    `json:"sessionId"`
	Errors    []marotte.PolicyErrorItem `json:"errors"`
}

// HandlePolicyError translates _kiro/policy/error → the policy_error SSE, which
// Settings → Permissions renders inline in its status row.
func (t *Translator) HandlePolicyError(ctx context.Context, _ marotte.ChatID, msg *marotte.RPCResponse) {
	p, ok := unmarshalParams[v3PolicyError](msg, "policy/error")
	if !ok {
		return
	}
	t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventPolicyError, "", marotte.PolicyErrorPayload{Errors: p.Errors}))
	t.governance.PolicyChanged(ctx, p.Errors, false)
}
