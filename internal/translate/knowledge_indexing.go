package translate

import (
	"context"
	"strings"

	"github.com/cplieger/marotte/internal/marotte"
)

type kasKnowledgeIndexing struct {
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	FileCount int    `json:"fileCount"`
	ItemCount int    `json:"itemCount"`
}

// HandleKnowledgeIndexing returns the handler for one phase of a custom agent's
// knowledge-base build. Only the chat's own session counts: a step's or a
// subagent's agent indexes into a store of its own, so its build says nothing
// about what the chat's agent can search.
func (t *Translator) HandleKnowledgeIndexing(phase marotte.KnowledgeIndexingPhase) func(context.Context, marotte.ChatID, *marotte.RPCResponse) {
	return func(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
		p, ok := unmarshalParams[kasKnowledgeIndexing](msg, "knowledge/indexing")
		if !ok || chatID == "" || p.SessionID == "" || t.foreignSession(chatID, p.SessionID) {
			return
		}
		name := strings.TrimSpace(displayText(p.Name))
		if name == "" {
			return
		}
		payload := marotte.KnowledgeIndexingPayload{Name: name, Phase: phase}
		switch phase {
		case marotte.KnowledgeIndexingStarted:
			payload.FileCount = max(p.FileCount, 0)
		case marotte.KnowledgeIndexingCompleted:
			switch status := marotte.KnowledgeIndexingStatus(p.Status); status {
			case marotte.KnowledgeIndexingSuccess:
				payload.Status = status
				payload.ItemCount = max(p.ItemCount, 0)
			case marotte.KnowledgeIndexingFailed:
				payload.Status = status
			default:
				return
			}
		default:
			return
		}
		t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventKnowledgeIndexing, chatID, payload))
	}
}
