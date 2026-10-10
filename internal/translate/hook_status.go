package translate

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// hookUpdateBlock is the kind=="hook_update" sub-block of a session_info_update, one
// frame per hook execution. Name is untrusted workspace file content.
type hookUpdateBlock struct {
	HookID      string `json:"hookId"`
	OperationID string `json:"operationId"`
	Name        string `json:"name"`
	Status      string `json:"status"`
}

// hookStatusCompleted is the wire status of a hook execution KAS reports as successful.
const hookStatusCompleted = "completed"

// handleHookUpdate appends a `Hook fired` tool_call card to the chat's open turn, gated on
// hooks.showStatus. A step's hook update never reaches here: the attribution gate drops it.
func (t *Translator) handleHookUpdate(ctx context.Context, chatID marotte.ChatID, h *hookUpdateBlock) {
	if !t.hookStatus.IsHookStatusEnabled() {
		return
	}
	turn := t.turns.TurnFoldTarget(ctx, chatID)
	if turn == nil {
		return
	}
	call := hookToolCall(h, time.Now().UnixMilli(), t.workDir)
	entry := marotte.EntryToolCallOf(&call)
	sealed, err := turn.ToolCall(ctx, "", &entry)
	t.publishSealed(ctx, chatScope(chatID), sealed)
	if err != nil {
		appendFailed(chatScope(chatID), "hook tool_call", err)
	}
}

func hookToolCall(h *hookUpdateBlock, ts int64, workDir string) marotte.ToolCall {
	return marotte.ToolCall{
		ID:         "hook-" + h.OperationID,
		Title:      "Hook fired: " + displayText(h.Name),
		Kind:       marotte.ToolKindHook,
		Status:     hookToolStatus(h.Status),
		SourcePath: hookSourcePath(workDir, h.HookID),
		Ts:         ts,
	}
}

// hookIDFileSep separates a hook file's absolute path from the hook's index in it.
const hookIDFileSep = "#hook-"

// hookSourcePath is the workspace-relative hook file a hook id names, or "". KAS mints a
// file hook's id as `<absolute path>#hook-<index>` and an agent profile's as
// `<profile id>#hook-<index>`; a file outside the workspace is a global hook.
func hookSourcePath(workDir, hookID string) string {
	at := strings.LastIndex(hookID, hookIDFileSep)
	if at <= 0 || workDir == "" {
		return ""
	}
	index := hookID[at+len(hookIDFileSep):]
	if index == "" || strings.Trim(index, "0123456789") != "" {
		return ""
	}
	file := hookID[:at]
	if !filepath.IsAbs(file) {
		return ""
	}
	rel := relPathIn(workDir, file)
	if filepath.IsAbs(rel) || rel == "." {
		return ""
	}
	return rel
}

// hookToolStatus maps the frame's status onto the card's terminal state. KAS hardcodes
// actionState:"Success" and sends no output, so a failed hook still arrives "completed"
// (kirodotdev/Kiro#11369); mapping from the wire paints red once a build reports failures.
func hookToolStatus(s string) marotte.ToolStatus {
	if s == hookStatusCompleted {
		return marotte.ToolCompleted
	}
	return marotte.ToolFailed
}
