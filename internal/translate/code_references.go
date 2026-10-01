package translate

// v3 (KAS) _kiro/code_references handler.
//
// KAS emits _kiro/code_references when a completion reproduces a recognizable
// chunk of a referenced open-source file AND the account's code-reference
// tracker is enabled (governance.features.codeReferenceTracker — enterprise
// profiles with recommendationsWithReferences=ALLOW, or the non-enterprise
// construction opt-in). On a Builder ID / individual login the flag defaults
// off, so this handler is defensive: correct against the verified wire shape,
// but only exercised when the tracker is enabled server-side.
//
// Wire shape (verified against the KAS 2.12 acp-server bundle):
//
//	{ "sessionId": "sess_…", "references": [ { "licenseName", "repository", "url" } ] }
//
// The KAS ACP layer maps every reference down to those three fields
// (licenseName/repository/url); the raw CodeWhisperer recommendationContentSpan
// and information fields are stripped upstream before emission, so there is no
// span to attach a reference to a specific message region and no message/tool
// id — attributions are turn-scoped. KAS also broadcasts the SAME references
// under EVERY live session id in the bridge process (`for (sessionId of
// this.sessions.keys())`), so a chat with a subagent session receives
// duplicates; we process only the parent-session copy.

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

// HandleCodeReferences accumulates the turn's licensed-code attributions onto
// the open turn and broadcasts a code_references SSE so the client attaches an
// attribution footnote to that turn. The aggregate rides that turn's
// turn_close, so the footnote survives a reload without this frame being
// persisted as such.
func (t *Translator) HandleCodeReferences(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	p, ok := unmarshalParams[v3CodeReferences](msg, "code_references")
	if !ok {
		return
	}
	// Dedup the KAS fan-out: skip any copy keyed to a session that is not this
	// chat's — a subagent's or a workflow step's — because the parent-session
	// copy carries the same references (KAS broadcasts the identical list to
	// every session). foreignSession is false for the chat itself and when the
	// parent session is unknown.
	if t.foreignSession(chatID, p.SessionID) {
		return
	}
	refs := make([]marotte.CodeReference, 0, len(p.References))
	for _, r := range p.References {
		// Match KAS's own filter: a reference with no license name carries
		// no attribution value.
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
	// A step's copy is already dropped above, so this frame is the chat's own. Only
	// an OPEN turn takes them: references fire mid-completion (the model must
	// generate the licensed code first), and a frame with no turn to join is dropped
	// rather than opening one for it.
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
