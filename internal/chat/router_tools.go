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

// handleToolCall serves GET /api/chats/{id}/tools/{toolCallID}: the whole of one
// tool call's persisted input, output and diffs, read off its tool_call entry and
// the tool_result that settled it.
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

// findToolCall locates a tool call's create and its settle by id, newest first,
// and folds the two into the bulk: the input is the create's, and the output, the
// diffs and the spans are the result's where one settled it, else the create's.
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

// adoptToolResult copies the settle's output, diffs and spans into the bulk,
// answering whether the payload decoded.
func adoptToolResult(bulk *marotte.ToolCallBulk, e *marotte.Entry) bool {
	var res marotte.EntryToolResult
	if json.Unmarshal(e.Payload, &res) != nil {
		return false
	}
	bulk.Output, bulk.Diffs, bulk.OutputSpans = res.Output, res.Diffs, res.OutputSpans
	return true
}

// adoptToolCreate copies the create's id and input into the bulk, and its output
// too unless a result already settled it; answers whether the payload decoded.
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
