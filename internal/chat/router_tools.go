package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/webhttp/v3"
)

// handleToolCall serves GET /api/chats/{id}/tools/{toolCallID}: one tool call's whole persisted input, output and
// diffs, from its tool_call and the settling tool_result.
func (rt *router) handleToolCall(w http.ResponseWriter, r *http.Request, chatID marotte.ChatID, toolCallID string) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	if !chatIDPattern(chatID) {
		httpreply.BadRequest(w, ids.ErrMsgInvalidChatID)
		return
	}
	// Echoed and used as a scan key, so an unbounded or exotic value is refused.
	if !ids.ValidMessageID(toolCallID) {
		httpreply.BadRequest(w, "invalid tool_call_id")
		return
	}
	entries, err := rt.store.All(r.Context(), chatID)
	if err != nil {
		if errors.Is(err, ErrChatNotFound) {
			httpreply.NotFound(w, errMsgChatNotFound)
			return
		}
		httpreply.ServerError(w, "chat read failed", fmt.Errorf("chat tool call %s: %w", logsafe.Field(string(chatID)), err))
		return
	}
	bulk, found := findToolCall(entries, toolCallID)
	if !found {
		httpreply.NotFound(w, "unknown tool call")
		return
	}
	webhttp.WriteJSON(w, bulk)
}

// findToolCall finds a tool call's create and settle by id, newest first, folding them into the bulk: input from the
// create; output, diffs and spans from the result if settled, else the create.
func findToolCall(entries []marotte.Entry, id string) (marotte.ToolCallBulk, bool) {
	resultID := marotte.ToolResultID(id)
	var bulk marotte.ToolCallBulk
	found, settled := false, false
	for i := len(entries) - 1; i >= 0 && (!found || !settled); i-- {
		e := &entries[i]
		switch {
		case !settled && e.Kind == marotte.EntryKindToolResult && e.ID == resultID:
			settled = adoptToolResult(&bulk, e)
		case !found && e.Kind == marotte.EntryKindToolCall && e.ID == id:
			found = adoptToolCreate(&bulk, e, settled)
		}
	}
	return bulk, found
}

func adoptToolResult(bulk *marotte.ToolCallBulk, e *marotte.Entry) bool {
	var res marotte.EntryToolResult
	if json.Unmarshal(e.Payload, &res) != nil {
		return false
	}
	bulk.Output, bulk.Diffs, bulk.OutputSpans = res.Output, res.Diffs, res.OutputSpans
	return true
}

// adoptToolCreate copies the create's id and input into the bulk, and its output unless already settled; reports
// whether it decoded.
func adoptToolCreate(bulk *marotte.ToolCallBulk, e *marotte.Entry, settled bool) bool {
	var call marotte.EntryToolCall
	if json.Unmarshal(e.Payload, &call) != nil {
		return false
	}
	bulk.ID, bulk.Input = call.ID, call.Input
	if !settled {
		bulk.Output, bulk.Diffs, bulk.OutputSpans = call.Output, call.Diffs, call.OutputSpans
	}
	return true
}
