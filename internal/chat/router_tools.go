package chat

import (
	"net/http"
	"slices"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/webhttp/v3"
)

// handleToolCall serves GET /api/chats/{id}/tools/{toolCallID}: the whole of one
// tool call's input, output and diffs.
func (rt *Router) handleToolCall(w http.ResponseWriter, r *http.Request, chatID marotte.ChatID, toolCallID string) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	if !chatIDPattern(chatID) {
		httpreply.BadRequest(w, ids.ErrMsgInvalidChatID)
		return
	}
	// Echoed back and used as a linear-scan key, so an unbounded or exotic value is
	// refused rather than searched for.
	if !ids.ValidMessageID(toolCallID) {
		httpreply.BadRequest(w, "invalid tool_call_id")
		return
	}
	c, ok := rt.store.Get(r.Context(), chatID)
	if !ok {
		httpreply.NotFound(w, errMsgChatNotFound)
		return
	}
	tc, found := findToolCall(c.Messages, toolCallID)
	if !found {
		httpreply.NotFound(w, "unknown tool call")
		return
	}
	webhttp.WriteJSON(w, marotte.ToolCallBulk{
		ID:          tc.ID,
		Input:       tc.Input,
		Output:      tc.Output,
		Diffs:       tc.Diffs,
		OutputSpans: tc.OutputSpans,
	})
}

// findToolCall locates a tool call by id, newest message first.
func findToolCall(msgs []marotte.Message, id string) (*marotte.ToolCall, bool) {
	for i := range slices.Backward(msgs) {
		for j := range msgs[i].ToolCalls {
			if msgs[i].ToolCalls[j].ID == id {
				return &msgs[i].ToolCalls[j], true
			}
		}
	}
	return nil, false
}

// previewMessage returns m with every oversized tool call replaced by its
// preview, or m unchanged when nothing needed cutting.
func previewMessage(m *marotte.Message) marotte.Message {
	out, _ := boundMessage(m, previewToolCall)
	return out
}

// previewToolCall returns a copy of tc bounded to previewBudget, and whether
// anything was cut.
//
// HasFull covers input, output and diffs together because the client fetches one
// bulk for all three.
func previewToolCall(tc *marotte.ToolCall) (marotte.ToolCall, bool) {
	out, cut := boundToolCall(tc, previewBudget)
	if cut == (marotte.ToolTruncation{}) {
		return out, false
	}
	out.OutputBytes = cut.OutputBytes
	out.HasFull = true
	return out, true
}
