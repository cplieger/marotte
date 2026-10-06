package translate

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/buffer"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/sanitize"
	"github.com/cplieger/marotte/internal/turnlog"
	"github.com/cplieger/pathinside/v2"
)

// HandleToolCall appends a tool_call entry in the lane fixed for the call's whole life:
// the frame's agent_subtask_id, or for a subagent invocation the ISSUER's lane, with the
// delegate's uuid in the payload as the lane it opens.
func (t *Translator) HandleToolCall(ctx context.Context, chatID marotte.ChatID, raw json.RawMessage, attr FrameAttribution) {
	var tc ACPToolCallWire
	if json.Unmarshal(raw, &tc) != nil {
		return
	}
	tc.gate()
	// Dropped before the fold target, which would open a wire turn and split the user's own
	// (the cloud-config fetch runs during session creation).
	if isInternalTool(tc.Meta.Kiro.ToolID) {
		return
	}
	if tc.Meta.Kiro.AgentInitiated && !attr.Step {
		t.bracket.ReviseTurnBinding(ctx, chatID)
	}
	turn, sc, ok := t.foldTarget(ctx, chatID, attr)
	if !ok {
		return
	}
	// The create frame does NOT adopt content.output: the following update repeats it.
	content := t.parseToolUpdateContent(tc.ToolCallID, tc.Content)
	call := toolCallFromWire(&tc, tc.Meta.Kiro.AgentSubtaskID, content, time.Now().UnixMilli())
	entry := marotte.EntryToolCallOf(&call)
	var sealed []turnlog.Sealed
	var err error
	if tc.Meta.Kiro.Kind == subagentInvocationKind {
		sealed, err = turn.Invocation(ctx, turn.IssuerLane(), tc.Meta.Kiro.AgentSubtaskID, &entry)
	} else {
		sealed, err = turn.ToolCall(ctx, call.AgentSubtaskID, &entry)
	}
	t.publishSealed(ctx, sc, sealed)
	if err != nil {
		appendFailed(sc, "tool_call", err)
		return
	}
	if len(content.diffs) > 0 {
		isNew := tc.Kind == marotte.ToolKindEdit && tc.Status == marotte.ToolPending
		t.trackFileChanges(turn, content.diffs, isNew)
		t.lines.RecordFromDiffs(chatID, content.diffs, recency(), string(tc.Kind))
	}
	t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventWorkingLabel, chatID,
		marotte.WorkingLabelPayload{Label: marotte.WorkingLabelForKind(tc.Kind, tc.Title)}))
}

// trackFileChanges folds a tool call's diffs into the turn's changed-files
// aggregate, one line delta per path.
func (t *Translator) trackFileChanges(turn *turnlog.Turn, diffs []marotte.ToolDiff, isNew bool) {
	for _, d := range diffs {
		added, removed := buffer.LineDelta(d.OldText, d.NewText)
		turn.ChangedFile(d.Path, added, removed, isNew)
	}
}

// toolCallFromWire builds the domain tool call a `tool_call` frame describes. Shared with
// the run-bridge path and the session/load replay so one frame decodes one way. ts is the
// caller's: a replayed call keeps the frame's own timestamp.
func toolCallFromWire(
	tc *ACPToolCallWire, subtask string, content toolUpdateContent, ts int64,
) marotte.ToolCall {
	return marotte.ToolCall{
		ID:             tc.ToolCallID,
		Title:          displayText(tc.Title),
		Kind:           tc.Kind,
		Status:         tc.Status,
		Input:          tc.RawInput,
		AgentSubtaskID: subtask,
		TerminalID:     content.terminalID,
		Locations:      tc.Locations,
		Diffs:          content.diffs,
		Disclosed:      disclosedFrom(tc.Meta.Kiro.DisclosedContext),
		Denial:         denialFrom(tc.Meta.Kiro.PolicyDenial),
		Ts:             ts,
	}
}

// HandleToolCallUpdate folds an update into the call's in-flight value. A non-terminal
// frame is announced as tool_progress (the fold's delta); a terminal one seals the
// settled value as the tool_result entry.
func (t *Translator) HandleToolCallUpdate(ctx context.Context, chatID marotte.ChatID, raw json.RawMessage, attr FrameAttribution) {
	var tu ACPToolCallUpdateWire
	if json.Unmarshal(raw, &tu) != nil {
		return
	}
	tu.gate()
	content := t.parseToolUpdateContent(tu.ToolCallID, tu.Content)
	sc := scopeOf(chatID, attr)
	// An update never OPENS a turn; with none open it is an orphan (chat) or a late frame
	// for a closed step turn.
	turn, ok := t.updateTarget(chatID, attr)
	if !ok {
		t.lateStepResult(ctx, chatID, attr, &tu, content)
		return
	}
	open, ok := turn.OpenCallFor(tu.ToolCallID)
	if !ok {
		return
	}
	// Folded on a COPY and written back: the fold reaches the terminal registry, the line
	// tracker and the event bus.
	before := open.Call
	tc := open.Call
	t.applyToolCallUpdate(ctx, chatID, turn, &open, &tc, &tu, content)
	if !tc.Status.Terminal() {
		turn.SetOpenCall(tu.ToolCallID, &tc)
		p := toolProgress(turn.ID(), &before, &tc)
		p.WorkflowID = sc.runID
		t.bus.Broadcast(ctx, sc.event(marotte.EventToolProgress, p))
		return
	}
	res := marotte.EntryToolResultOf(&tc)
	sealed, err := turn.ToolResult(ctx, tu.Meta.Kiro.AgentSubtaskID, tu.ToolCallID, &res)
	t.publishSealed(ctx, sc, sealed)
	if err != nil {
		appendFailed(sc, "tool_result", err)
	}
}

// updateTarget is the open turn an update folds into: the run's turn for the step
// path, else the chat's own turn. Neither is opened here.
func (t *Translator) updateTarget(chatID marotte.ChatID, attr FrameAttribution) (*turnlog.Turn, bool) {
	if attr.Step {
		return t.runs.RunFoldTarget(context.Background(), attr.RunID, attr.NodePath, attr.SessionID, chatID)
	}
	return t.turns.OwnTurn(chatID)
}

// lateStepResult files a terminal update for a step path whose turn already closed: KAS's
// own account of how the aborted call ended. A non-terminal late frame is dropped.
func (t *Translator) lateStepResult(ctx context.Context, chatID marotte.ChatID, attr FrameAttribution, tu *ACPToolCallUpdateWire, content toolUpdateContent) {
	if !attr.Step || !tu.Status.Terminal() {
		return
	}
	tc := marotte.ToolCall{ID: tu.ToolCallID, Title: displayText(tu.Title), Kind: tu.Kind}
	applyToolCallOutput(&tc, tu, content)
	tc.Status = tu.Status
	tc.Diffs = content.diffs
	tc.Locations = tu.Locations
	tc.TerminalID = content.terminalID
	t.adoptTerminalOutput(chatID, &tc)
	mergeCheckpoint(&tc, tu.Meta.Kiro.Checkpoint)
	mergeToolMeta(&tc, tu)
	e := marotte.Entry{Kind: marotte.EntryKindToolResult, ID: marotte.ToolResultID(tu.ToolCallID), Lane: tu.Meta.Kiro.AgentSubtaskID}
	setPayload(&e, marotte.EntryToolResultOf(&tc))
	if err := t.runs.RunAppendAfterClosed(ctx, attr.RunID, attr.NodePath, &e); err != nil {
		appendFailed(runScope(attr.RunID), "late tool_result", err)
		return
	}
	t.publishAppended(ctx, runScope(attr.RunID), &e)
}

// toolProgress describes what one fold changed about an in-flight tool call. Every
// omitted field means "unchanged", so applying it to `before` reconstructs `after`
// exactly; the client keeps no accumulation rules. Input never changes on an update.
func toolProgress(turn string, before, after *marotte.ToolCall) marotte.ToolProgressPayload {
	d := marotte.ToolProgressPayload{Turn: turn, ToolCallID: after.ID}
	if after.Title != before.Title {
		d.Title = after.Title
	}
	if after.Kind != before.Kind {
		d.Kind = after.Kind
	}
	if after.Status != before.Status {
		d.Status = after.Status
	}
	if after.DurationMs != before.DurationMs {
		d.DurationMs = after.DurationMs
	}
	d.OutputDelta, d.OutputReplace = outputDelta(before.Output, after.Output)
	deltaContent(&d, before, after)
	deltaAttachments(&d, before, after)
	return d
}

// deltaContent carries the three collections. Only Diffs accumulates; the other
// two are absolute and go entire whenever they change.
func deltaContent(d *marotte.ToolProgressPayload, before, after *marotte.ToolCall) {
	if !slices.Equal(after.OutputSpans, before.OutputSpans) {
		d.OutputSpans = after.OutputSpans
	}
	// Diffs only append (applyToolCallDiffs), so the tail is the whole change.
	if len(after.Diffs) > len(before.Diffs) {
		d.DiffsAppended = after.Diffs[len(before.Diffs):]
	}
	if !slices.Equal(after.Locations, before.Locations) {
		d.Locations = after.Locations
	}
}

// deltaAttachments carries the late identity ids and the metadata blocks. Each is adopted
// once and never overwritten, so it appears on at most one frame per call.
func deltaAttachments(d *marotte.ToolProgressPayload, before, after *marotte.ToolCall) {
	if after.TerminalID != before.TerminalID {
		d.TerminalID = after.TerminalID
	}
	if after.AgentSubtaskID != before.AgentSubtaskID {
		d.AgentSubtaskID = after.AgentSubtaskID
	}
	if after.WorkflowID != before.WorkflowID {
		d.WorkflowID = after.WorkflowID
	}
	if after.Checkpoint != nil && *after.Checkpoint != derefCheckpoint(before.Checkpoint) {
		d.Checkpoint = after.Checkpoint
	}
	if before.Disclosed == nil && after.Disclosed != nil {
		d.Disclosed = after.Disclosed
	}
	if before.Denial == nil && after.Denial != nil {
		d.Denial = after.Denial
	}
	if before.Offload == nil && after.Offload != nil {
		d.Offload = after.Offload
	}
	// One-way: the fold never clears the mark, so only the frame that SETS it carries it.
	if after.Declined && !before.Declined {
		d.Declined = true
	}
}

// outputDelta describes the change from one accumulated output to the next: the appended
// tail, or the whole new value when it does not extend the old (adoptTerminalOutput's
// replace at completion).
func outputDelta(before, after string) (delta string, replace bool) {
	if after == before {
		return "", false
	}
	if strings.HasPrefix(after, before) {
		return after[len(before):], false
	}
	return after, true
}

// derefCheckpoint returns the checkpoint's value, or the zero value for nil, so
// a nil-to-set transition compares as a change without a second nil branch.
func derefCheckpoint(c *marotte.ToolCheckpoint) marotte.ToolCheckpoint {
	if c == nil {
		return marotte.ToolCheckpoint{}
	}
	return *c
}

// parseToolUpdateContent extracts the sanitized output delta, any file diffs (made
// workspace-relative), and the terminal id from a tool_call_update's content blocks.
// A terminal block's text is not folded in: its bytes arrive on terminal/*.
func (t *Translator) parseToolUpdateContent(toolCallID string, items []ACPToolCallContentBlock) toolUpdateContent {
	return parseToolContent(t.relPath, toolCallID, items)
}

// parseToolContent is the parser, taking the path normalizer so the session/load replay
// reuses it rather than a second switch over the content union. A func rather than a
// workDir string avoids two adjacent same-typed strings.
func parseToolContent(
	relPath func(string) string, toolCallID string, items []ACPToolCallContentBlock,
) toolUpdateContent {
	var out toolUpdateContent
	var outputDelta, rawDelta strings.Builder
	for _, item := range items {
		switch {
		case item.Type == ContentTypeContent && item.Content.Text != "":
			outputDelta.WriteString(sanitize.Output(item.Content.Text))
			outputDelta.WriteByte('\n')
			rawDelta.WriteString(item.Content.Text)
			rawDelta.WriteByte('\n')
		case item.Type == ContentTypeDiff && item.Path != "":
			out.diffs = append(out.diffs, marotte.ToolDiff{
				Path: relPath(item.Path), OldText: item.OldText, NewText: item.NewText,
			})
		case item.Type == ContentTypeTerminal && item.TerminalID != "":
			out.terminalID = item.TerminalID
		case !knownToolContentType(item.Type):
			// Guarded on the TYPE, not a bare default, which would also catch a known type whose
			// payload arm did not match.
			slog.Debug("tool call content block of an unmodelled type, dropped",
				"tool_call_id", toolCallID, "type", item.Type)
		}
	}
	out.output = outputDelta.String()
	out.rawText = rawDelta.String()
	// Once per frame, and only when nothing was extracted, so a claim-only tool logs nothing.
	if len(items) > 0 && out.output == "" && out.diffs == nil && out.terminalID == "" {
		slog.Debug("tool call carried content blocks that produced nothing to render",
			"tool_call_id", toolCallID, "blocks", len(items))
	}
	return out
}

// knownToolContentType reports whether the ACP content-block discriminator is one
// parseToolUpdateContent models. Separate from the switch, whose arms pair a type with
// a payload condition.
func knownToolContentType(t string) bool {
	switch t {
	case ContentTypeContent, ContentTypeDiff, ContentTypeTerminal:
		return true
	default:
		return false
	}
}

// toolUpdateContent is one tool_call_update's parsed content blocks.
type toolUpdateContent struct {
	output string
	// rawText is output before sanitizing, compared against the unsanitized rawOutput.
	rawText    string
	terminalID string
	diffs      []marotte.ToolDiff
}

// applyToolCallUpdate folds a parsed tool_call_update into the in-flight tool call:
// status, appended output, replaced locations, appended diffs with line tracking.
func (t *Translator) applyToolCallUpdate(ctx context.Context, chatID marotte.ChatID, turn *turnlog.Turn, open *turnlog.OpenCall, tc *marotte.ToolCall, tu *ACPToolCallUpdateWire, content toolUpdateContent) {
	// KAS sends title and kind nullish on an update; applying them unconditionally wipes the
	// initial tool_call's values.
	if tu.Title != "" {
		tc.Title = displayText(tu.Title)
	}
	if tu.Kind != "" {
		tc.Kind = tu.Kind
	}
	// BEFORE the status fold: one frame can carry both the link and `completed`, and the
	// completion's adoption needs the id or the output is never persisted.
	if tc.TerminalID == "" && content.terminalID != "" {
		tc.TerminalID = content.terminalID
	}
	t.applyToolCallStatus(ctx, chatID, open.StartedTs, tc, tu)
	applyToolCallOutput(tc, tu, content)
	if len(tu.Locations) > 0 {
		tc.Locations = tu.Locations
	}
	t.applyToolCallDiffs(chatID, turn, tc, content.diffs)
	if tu.Meta.Kiro.AgentSubtaskID != "" && tu.Meta.Kiro.AgentSubtaskID != open.Lane {
		slog.Warn("tool_call_update lane disagrees with its call's lane; folding where the call is",
			"chat_id", chatID, "tool_call_id", tc.ID, "call_lane", open.Lane, "update_lane", tu.Meta.Kiro.AgentSubtaskID)
	}
	// AFTER the two folds above: it only grades a settled call and never competes for the output.
	applyUpdateRefusal(tc, tu.RawOutput)
	// Adopted once: a later frame cannot name a different run. An update call is excluded
	// because `update_workflow` echoes the same `workflowId` while starting nothing; without
	// this a refused plan update inherits the run's card and its refusal renders nowhere.
	// KAS names no tool on an ordinary call, so the payload shape is the only discriminator.
	if _, isUpdate := rawOutputUpdate(tu.RawOutput); tc.WorkflowID == "" && !isUpdate {
		tc.WorkflowID = rawOutputWorkflowID(tu.RawOutput)
	}
	mergeCheckpoint(tc, tu.Meta.Kiro.Checkpoint)
	mergeToolMeta(tc, tu)
}

// applyUpdateRefusal folds a workflow-update tool's own verdict onto the card. One-way:
// keyed on the field (rawOutputUpdate is present=false for every other tool), absent
// means taken, it only marks a call already settled `completed`, and never clears the
// mark. It reads a domain fact no other channel carries, never a restated outcome.
func applyUpdateRefusal(tc *marotte.ToolCall, raw json.RawMessage) {
	if tc.Status != marotte.ToolCompleted {
		return
	}
	if updated, present := rawOutputUpdate(raw); present && !updated {
		tc.Declined = true
	}
}

func toolCallContentOutput(tu *ACPToolCallUpdateWire, content toolUpdateContent) string {
	if content.output == "" {
		return ""
	}
	if message := stringifiedRawOutputMessage(tu.RawOutput, content.rawText); message != "" {
		return sanitize.Output(message) + "\n"
	}
	if response := mcpEnvelopeResponse(tu.RawOutput, content.rawText); response != "" {
		return sanitize.Output(response) + "\n"
	}
	return content.output
}

// applyToolCallOutput folds an update's output text onto the card. Content wins unless it
// is the stringified copy (stringifiedRawOutputMessage); a bare rawOutput string is the
// fallback when KAS suppresses an edit's diff block.
func applyToolCallOutput(tc *marotte.ToolCall, tu *ACPToolCallUpdateWire, content toolUpdateContent) {
	tc.Output += toolCallContentOutput(tu, content)
	if tc.Output != "" {
		return
	}
	text := rawOutputString(tu.RawOutput)
	if text == "" && tc.Status == marotte.ToolFailed {
		text = rawOutputFailureText(tu.RawOutput)
	}
	if text != "" {
		tc.Output = sanitize.Output(text)
	}
}

// applyToolCallStatus folds an update's status in, and on a terminal status stamps
// the duration and takes the terminal's output for keeping.
func (t *Translator) applyToolCallStatus(
	ctx context.Context, chatID marotte.ChatID, startedTs int64,
	tc *marotte.ToolCall, tu *ACPToolCallUpdateWire,
) {
	if tu.Status == "" {
		return
	}
	tc.Status = tu.Status
	if tu.Status != marotte.ToolCompleted && tu.Status != marotte.ToolFailed {
		return
	}
	// KAS can send several terminal status frames for one call; the first measurement stands.
	if tc.DurationMs == 0 && startedTs > 0 {
		tc.DurationMs = int(time.Now().UnixMilli() - startedTs)
	}
	t.adoptTerminalOutput(chatID, tc)
	t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventWorkingLabel, chatID,
		marotte.WorkingLabelPayload{Label: marotte.WorkingLabelThinking}))
}

// applyToolCallDiffs appends an update's diffs to the card and, once the tool succeeded,
// records the file changes. Only `completed` feeds the ledger: KAS repeats a write's
// diff on every streaming frame, so a failed write would otherwise count.
func (t *Translator) applyToolCallDiffs(
	chatID marotte.ChatID, turn *turnlog.Turn, tc *marotte.ToolCall, diffs []marotte.ToolDiff,
) {
	if len(diffs) == 0 {
		return
	}
	tc.Diffs = append(tc.Diffs, diffs...)
	if tc.Status != marotte.ToolCompleted {
		return
	}
	t.trackFileChanges(turn, diffs, false)
	t.lines.RecordFromDiffs(chatID, diffs, recency(), string(tc.Kind))
}

// recency is the line tracker's eviction key for a change recorded now (oldest-first).
func recency() int {
	return int(time.Now().UnixMilli())
}

// adoptTerminalOutput copies a finished terminal's output onto its tool call so it
// survives a reload. A one-time copy because the terminal's first output precedes the
// update naming it; it wins over earlier ACP fragments. A miss is logged: an empty card
// looks exactly like a command that printed nothing.
func (t *Translator) adoptTerminalOutput(chatID marotte.ChatID, tc *marotte.ToolCall) {
	if tc.TerminalID == "" {
		return
	}
	text, spans, ok := t.terminals.Output(tc.TerminalID)
	if !ok {
		slog.Warn("terminal output missing at completion",
			"chat_id", chatID, "tool_call_id", tc.ID,
			"terminal_id", tc.TerminalID, "status", tc.Status,
			"output_bytes", len(tc.Output))
		return
	}
	if text == "" {
		return
	}
	tc.Output = text
	tc.OutputSpans = spans
}

// mergeToolMeta folds a tool_call_update's disclosure, denial and offload metadata into
// the buffered call, never overwriting a held value. A denial can arrive on the update.
func mergeToolMeta(tc *marotte.ToolCall, tu *ACPToolCallUpdateWire) {
	if tc.Disclosed == nil {
		tc.Disclosed = disclosedFrom(tu.Meta.Kiro.DisclosedContext)
	}
	if tc.Denial == nil {
		tc.Denial = denialFrom(tu.Meta.Kiro.PolicyDenial)
	}
	if tc.Offload == nil {
		tc.Offload = offloadFrom(tu.Meta.Kiro.OutputTransformation)
	}
}

// offloadFrom maps KAS's outputTransformation block onto the domain type: nil
// unless KAS offloaded the output to an absolute path.
func offloadFrom(in *ACPOutputTransformation) *marotte.ToolOffload {
	if in == nil || in.Kind != "offloaded" || !filepath.IsAbs(in.AbsFilePath) || in.TotalChars < 0 {
		return nil
	}
	return &marotte.ToolOffload{Path: filepath.Clean(in.AbsFilePath), TotalChars: in.TotalChars}
}

// disclosedFrom maps KAS's disclosedContext block onto the domain type; nil for anything
// but a disclose_context.
func disclosedFrom(in *ACPDisclosedContext) *marotte.ToolDisclosed {
	if in == nil {
		return nil
	}
	return &marotte.ToolDisclosed{Type: in.Type, DisplayName: in.DisplayName, URI: in.URI}
}

// denialFrom maps KAS's policyDenial block onto the domain type. The outer `effect` is
// always "deny" and dropped; the rule's own effect is kept (an unanswered "ask" lands here).
func denialFrom(in *ACPPolicyDenial) *marotte.ToolDenial {
	if in == nil {
		return nil
	}
	out := &marotte.ToolDenial{
		Capability: in.Capability,
		Resource:   in.Resource,
		Scope:      in.Scope,
		Source:     in.Source,
	}
	if in.MatchedRule != nil {
		out.Rule = &marotte.ToolDenialRule{
			Capability: in.MatchedRule.Capability,
			Effect:     in.MatchedRule.Effect,
			Match:      in.MatchedRule.Match,
			Exclude:    in.MatchedRule.Exclude,
		}
	}
	return out
}

// mergeCheckpoint folds a tool_call_update's _meta.kiro.checkpoint into the buffered call
// field by field: the key set varies between frames, so replacing the struct is lossy.
func mergeCheckpoint(tc *marotte.ToolCall, in *ACPCheckpointMeta) {
	if in == nil || (in.Original == "" && in.Modified == "" && in.Local == "") {
		return
	}
	if tc.Checkpoint == nil {
		tc.Checkpoint = &marotte.ToolCheckpoint{}
	}
	if in.Original != "" {
		tc.Checkpoint.Original = in.Original
	}
	if in.Modified != "" {
		tc.Checkpoint.Modified = in.Modified
	}
	if in.Local != "" {
		tc.Checkpoint.Local = in.Local
	}
}

// localPath converts a wire path reference (KAS sends some as file:// URIs) to a local
// filesystem path. Anything that is not a LOCAL file:// URI is returned unchanged.
func localPath(ref string) string {
	// Cheap gate: this runs on every location and diff of every tool call.
	if !strings.Contains(ref, "://") {
		return ref
	}
	u, err := url.Parse(ref)
	if err != nil || u.Scheme != "file" || u.Path == "" {
		return ref
	}
	if u.Host != "" && u.Host != "localhost" {
		return ref
	}
	// u.Path is percent-decoded: "file:///w/hello%20world.sh" is "hello world.sh".
	return u.Path
}

// relPath strips the workspace root prefix from an absolute path; a path outside the
// workspace is returned unchanged. URI normalising lives here because that branch
// returns its input. The escape test is separator-precise (pathinside.RelEscapes).
func (t *Translator) relPath(ref string) string {
	return relPathIn(t.workDir, ref)
}

// relPathIn is relPath's rule without a Translator, for the session/load replay: a path
// left absolute would key ChangedFiles differently and leak the workspace root.
func relPathIn(workDir, ref string) string {
	abs := localPath(ref)
	if workDir == "" {
		return abs
	}
	clean := filepath.Clean(abs)
	root := filepath.Clean(workDir)
	rel, err := filepath.Rel(root, clean)
	if err != nil || pathinside.RelEscapes(rel) {
		return abs
	}
	return filepath.ToSlash(rel)
}
