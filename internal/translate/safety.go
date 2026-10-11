package translate

// Infrastructure-Safety notifications: they fire once KAS installs the gate (the client
// declares infrastructureSafety and the setting or experiment is on).

import (
	"context"
	"encoding/json"

	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
)

// knownSafetyStatuses gates translation to the documented GateStatus set, so an unrecognized
// status is dropped rather than surfaced as a mystery banner.
var knownSafetyStatuses = map[marotte.SafetyStatus]struct{}{
	marotte.SafetyStatusIdle:        {},
	marotte.SafetyStatusFormalizing: {},
	marotte.SafetyStatusEvaluating:  {},
	marotte.SafetyStatusBlocked:     {},
	marotte.SafetyStatusError:       {},
}

// It carries no sessionId (KAS routes per-session on the outbound, not in params), so it is
// broadcast chat-scoped from the delivering bridge.
type v3SafetyStatusChanged struct {
	Status            string   `json:"status"`
	Detail            string   `json:"detail"`
	ToolID            string   `json:"toolId"`
	BlockedProperties []string `json:"blockedProperties"`
}

// v3SafetyPropertiesChanged is the _kiro/safety/propertiesChanged wire shape; properties[] is
// decoded tolerantly (object or bare string) by decodeSafetyProps.
type v3SafetyPropertiesChanged struct {
	SessionID  string            `json:"sessionId"`
	Reason     string            `json:"reason"`
	Properties []json.RawMessage `json:"properties"`
}

// HandleSafetyStatusChanged translates _kiro/safety/statusChanged into a chat-scoped
// safety_status SSE ("idle" clears a stale banner). It surfaces a refusal and gates nothing:
// the gate is a PreToolUse hook, and toolId is a tool NAME, not a call id.
func (t *Translator) HandleSafetyStatusChanged(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	p, ok := unmarshalParams[v3SafetyStatusChanged](msg, "safety/statusChanged")
	if !ok {
		return
	}
	status := marotte.SafetyStatus(p.Status)
	if _, known := knownSafetyStatuses[status]; !known {
		return
	}
	t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventSafetyStatus, chatID, marotte.SafetyStatusPayload{
		Status:            status,
		Detail:            p.Detail,
		ToolID:            p.ToolID,
		BlockedProperties: p.BlockedProperties,
	}))
	// A blocked status is ENFORCE mode's terminal outcome, so it is persisted beyond the banner.
	if status == marotte.SafetyStatusBlocked {
		t.persistSafetyBlock(ctx, chatID, p)
	}
}

// persistSafetyBlock records an enforce-mode block as a safety_blocked entry on the
// chat: inside the open turn, sealing every lane first, else after the newest
// turn's close. Chat scope is right because statusChanged names a tool by NAME
// rather than by call.
func (t *Translator) persistSafetyBlock(ctx context.Context, chatID marotte.ChatID, p v3SafetyStatusChanged) {
	props := safetyBlockProperties(p)
	t.appendLaneless(durable.Context(ctx), chatID, marotte.EntryKindSafetyBlocked, "",
		marotte.EntrySafetyBlocked{Properties: props},
		func(ctx context.Context, turn *turnlog.Turn) ([]turnlog.Sealed, error) {
			return turn.SafetyBlocked(ctx, props)
		})
}

// safetyBlockProperties is what the entry carries: the violated properties (the
// WHY), or the gate's detail string as the one property when it names none.
func safetyBlockProperties(p v3SafetyStatusChanged) []string {
	if len(p.BlockedProperties) > 0 {
		return p.BlockedProperties
	}
	if p.Detail == "" {
		return nil
	}
	return []string{p.Detail}
}

// HandleSafetyPropertiesChanged translates _kiro/safety/propertiesChanged into a chat-scoped
// safety_properties SSE. A foreign-session copy is skipped so a subagent's or a step's
// formalized properties are not attributed to the parent chat.
func (t *Translator) HandleSafetyPropertiesChanged(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	p, ok := unmarshalParams[v3SafetyPropertiesChanged](msg, "safety/propertiesChanged")
	if !ok {
		return
	}
	if t.foreignSession(chatID, p.SessionID) {
		return
	}
	props := decodeSafetyProps(p.Properties)
	if len(props) == 0 {
		return
	}
	t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventSafetyProperties, chatID, marotte.SafetyPropertiesPayload{
		Properties: props,
		Reason:     p.Reason,
	}))
}

// decodeSafetyProps normalizes the polymorphic properties[] — KAS sends either {index,
// description, enabled} objects or bare strings — into []marotte.SafetyProperty. A bare string
// maps to that description with Enabled=true; an empty description is dropped.
func decodeSafetyProps(raw []json.RawMessage) []marotte.SafetyProperty {
	out := make([]marotte.SafetyProperty, 0, len(raw))
	for _, r := range raw {
		var obj struct {
			Description string `json:"description"`
			Index       int    `json:"index"`
			Enabled     bool   `json:"enabled"`
		}
		if err := json.Unmarshal(r, &obj); err == nil && obj.Description != "" {
			out = append(out, marotte.SafetyProperty{
				Description: obj.Description,
				Index:       obj.Index,
				Enabled:     obj.Enabled,
			})
			continue
		}
		var s string
		if err := json.Unmarshal(r, &s); err == nil && s != "" {
			out = append(out, marotte.SafetyProperty{Description: s, Enabled: true})
		}
	}
	return out
}
