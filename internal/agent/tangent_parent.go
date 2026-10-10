package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

var _ command.TangentAccess = (*Runtime)(nil)

// TangentParent answers the chat chatID was forked from through KAS's own link: the first
// session of chatID's chain whose session/list row names a parentSessionId outside that chain,
// mapped to the chat whose chain holds it. A successor session (a compaction, a model switch)
// carries no link, so the whole chain is walked.
func (rt *Runtime) TangentParent(ctx context.Context, chatID marotte.ChatID) (marotte.ChatID, error) {
	c, ok := rt.chatStore.Get(ctx, chatID)
	if !ok {
		return "", command.ErrChatNotFound
	}
	chain := c.SessionChain()
	if len(chain) == 0 {
		return "", command.ErrNotATangent
	}
	rows, err := rt.workspaceSessionRows(ctx)
	if err != nil {
		return "", err
	}
	parents := make(map[string]string, len(rows))
	for i := range rows {
		parents[rows[i].SessionID] = rows[i].Meta.Kiro.ParentSessionID
	}
	own := make(map[string]bool, len(chain))
	for _, sid := range chain {
		own[sid] = true
	}
	for _, sid := range chain {
		p := parents[sid]
		if p == "" || own[p] {
			continue
		}
		if parent, claimed := rt.claimedSessions(ctx)[p]; claimed {
			return parent, nil
		}
		return "", command.ErrTangentParentGone
	}
	return "", command.ErrNotATangent
}

// TurnReply answers the main-lane text of one turn: consecutive text entries join as they
// streamed, and a tool call or any other entry between two says starts a new paragraph.
func (rt *Runtime) TurnReply(ctx context.Context, chatID marotte.ChatID, turnID string) (string, error) {
	page, err := rt.chatStore.TurnPage(ctx, chatID, turnID, 0)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	broken := false
	for i := range page.Entries {
		e := &page.Entries[i]
		if e.Lane != "" {
			continue
		}
		if e.Kind != marotte.EntryKindText {
			broken = b.Len() > 0
			continue
		}
		var say marotte.EntryText
		if json.Unmarshal(e.Payload, &say) != nil || say.Text == "" {
			continue
		}
		if broken {
			b.WriteString("\n\n")
			broken = false
		}
		b.WriteString(say.Text)
	}
	return strings.TrimSpace(b.String()), nil
}
