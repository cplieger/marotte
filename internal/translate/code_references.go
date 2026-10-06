package translate

// _kiro/code_references: KAS's licensed-code attributions, emitted only when the account's
// code-reference tracker is on. Wire (KAS 2.12): {sessionId, references: [{licenseName,
// repository, url}]}; no span, so attributions are turn-scoped. KAS sends the same list under
// every live session id, so only the parent-session copy is processed.

import (
	"context"

	"github.com/cplieger/marotte/internal/marotte"
)

// v3CodeReferences is the _kiro/code_references notification payload.
type v3CodeReferences struct {
	SessionID  string            `json:"sessionId"`
	References []v3CodeReference `json:"references"`
}

// v3CodeReference is one entry in the references list. Only licenseName,
// repository, and url survive KAS's ACP-layer mapping.
type v3CodeReference struct {
	LicenseName string `json:"licenseName"`
	Repository  string `json:"repository"`
	URL         string `json:"url"`
}

// HandleCodeReferences accumulates the turn's attributions onto the open turn and broadcasts a
// code_references SSE; the aggregate rides the turn's turn_close for reload.
func (t *Translator) HandleCodeReferences(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	p, ok := unmarshalParams[v3CodeReferences](msg, "code_references")
	if !ok {
		return
	}
	// Dedup KAS's fan-out: skip a copy keyed to another session (a subagent's or a step's).
	if t.foreignSession(chatID, p.SessionID) {
		return
	}
	refs := make([]marotte.CodeReference, 0, len(p.References))
	for _, r := range p.References {
		// KAS's own filter: no license name, no attribution value.
		if r.LicenseName == "" {
			continue
		}
		refs = append(refs, marotte.CodeReference{
			LicenseName: r.LicenseName,
			Repository:  r.Repository,
			URL:         r.URL,
		})
	}
	if len(refs) == 0 {
		return
	}
	// Only an OPEN turn takes them; a frame with no turn is dropped, not given one.
	turn, ok := t.turns.OwnTurn(chatID)
	if !ok {
		return
	}
	turn.AddCodeReferences(refs...)
	t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventCodeReferences, chatID, marotte.CodeReferencesPayload{
		Turn:       turn.ID(),
		References: turn.CodeReferences(),
	}))
}
