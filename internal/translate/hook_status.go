package translate

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/sanitize"
)

// hookUpdateBlock is the kind=="hook_update" sub-block of a session_info_update, one
// frame per hook execution. Name and Output are untrusted workspace content. Output
// stays raw so a malformed one costs the card its output, never the card.
type hookUpdateBlock struct {
	HookID      string          `json:"hookId"`
	OperationID string          `json:"operationId"`
	Name        string          `json:"name"`
	Status      string          `json:"status"`
	Output      json.RawMessage `json:"output"`
}

// hookOutput is a runCommand hook's `output` (KAS handleHookAction): once it ran, what it
// printed and its exit code.
type hookOutput struct {
	Result *struct {
		Output   *string `json:"output"`
		ExitCode *int    `json:"exitCode"`
	} `json:"result"`
}

// hookResult decodes the frame's output into what it printed and whether it exited
// non-zero; an absent or malformed output is neither.
func (h *hookUpdateBlock) hookResult() (printed string, exitFailed bool) {
	var out hookOutput
	if len(h.Output) == 0 || json.Unmarshal(h.Output, &out) != nil || out.Result == nil {
		return "", false
	}
	if out.Result.Output != nil {
		printed = sanitize.Output(*out.Result.Output)
	}
	return printed, out.Result.ExitCode != nil && *out.Result.ExitCode != 0
}

// hookStatusCompleted is the wire status of a hook execution KAS reports as successful.
const hookStatusCompleted = "completed"

// handleHookUpdate appends a `Hook fired` tool_call card to the turn a content frame with
// attr folds into (the chat's, or a step's run turn), in the lane of the sub-agent that ran
// it (subtask, "" for the session's own agent). Every hook is recorded: hooks.showStatus is
// applied where the transcript draws.
func (t *Translator) handleHookUpdate(ctx context.Context, chatID marotte.ChatID, attr FrameAttribution, h *hookUpdateBlock, subtask string) {
	turn, sc, ok := t.foldTarget(ctx, chatID, attr)
	if !ok {
		return
	}
	call := hookToolCall(h, time.Now().UnixMilli(), t.workDir)
	if trigger, ok := t.hookTriggers.HookTrigger(h.HookID); ok {
		call.HookTrigger = trigger
	}
	entry := marotte.EntryToolCallOf(&call)
	sealed, err := turn.ToolCall(ctx, subtask, &entry)
	t.publishSealed(ctx, sc, sealed)
	if err != nil {
		appendFailed(sc, "hook tool_call", err)
	}
}

func hookToolCall(h *hookUpdateBlock, ts int64, workDir string) marotte.ToolCall {
	printed, exitFailed := h.hookResult()
	status := hookToolStatus(h.Status)
	if exitFailed {
		status = marotte.ToolFailed
	}
	return marotte.ToolCall{
		ID:         "hook-" + h.OperationID,
		Title:      "Hook fired: " + displayText(h.Name),
		Kind:       marotte.ToolKindHook,
		Status:     status,
		Output:     printed,
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

// hookToolStatus maps the frame's status onto the card's terminal state. On the in-process
// hook path KAS hardcodes actionState:"Success" and sends no output, so a failed hook still
// arrives "completed" (kirodotdev/Kiro#11369); mapping from the wire paints red once a build
// reports failures.
func hookToolStatus(s string) marotte.ToolStatus {
	if s == hookStatusCompleted {
		return marotte.ToolCompleted
	}
	return marotte.ToolFailed
}
