package chat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/sanitize"
)

// renderChatMarkdown renders a chat's header and log as a self-contained Markdown
// transcript: title and metadata, then one section per turn (prompt, entries in
// file order, footer). Pure, so it is safe inline on the read path. Tool input and
// output pass through sanitize.Output so a hostile tool result cannot smuggle
// terminal escapes or prompt-injection codepoints into the exported file.
func renderChatMarkdown(c *marotte.Chat, entries []marotte.Entry) string {
	var b strings.Builder
	title := oneLine(c.Name)
	if title == "" {
		title = "Untitled chat"
	}
	fmt.Fprintf(&b, "# %s\n\n", title)
	turns := groupTurns(entries)
	writeChatMetadata(&b, c, len(turns))
	if len(turns) == 0 {
		b.WriteString("_No turns._\n")
		return b.String()
	}
	for _, t := range turns {
		writeTurnMarkdown(&b, t)
	}
	return b.String()
}

// groupTurns partitions entries by turn, turns in order of first appearance and
// entries in file order within a turn.
func groupTurns(entries []marotte.Entry) [][]marotte.Entry {
	byTurn := make(map[string]int)
	var turns [][]marotte.Entry
	for i := range entries {
		idx, seen := byTurn[entries[i].Turn]
		if !seen {
			idx = len(turns)
			byTurn[entries[i].Turn] = idx
			turns = append(turns, nil)
		}
		turns[idx] = append(turns[idx], entries[i])
	}
	return turns
}

// writeChatMetadata emits the header bullet list (id, model, mode, timestamps,
// turn count). Empty fields are skipped.
func writeChatMetadata(b *strings.Builder, c *marotte.Chat, turns int) {
	if c.ID != "" {
		fmt.Fprintf(b, "- **Chat ID:** `%s`\n", oneLine(c.ID))
	}
	if c.Model != "" {
		fmt.Fprintf(b, "- **Model:** %s\n", oneLine(c.Model))
	}
	if c.CurrentModeID != "" {
		fmt.Fprintf(b, "- **Mode:** %s\n", oneLine(c.CurrentModeID))
	}
	if ts := mdTimestamp(c.CreatedAt); ts != "" {
		fmt.Fprintf(b, "- **Created:** %s\n", ts)
	}
	if ts := mdTimestamp(c.UpdatedAt); ts != "" {
		fmt.Fprintf(b, "- **Updated:** %s\n", ts)
	}
	fmt.Fprintf(b, "- **Turns:** %d\n\n", turns)
}

// turnRender is one turn's rendering state: the results folded into their calls,
// the call ids the turn holds, and the plan rendered once at its first entry.
type turnRender struct {
	results  map[string]*marotte.EntryToolResult
	calls    map[string]struct{}
	plan     []marotte.PlanEntry
	planDone bool
}

// writeTurnMarkdown renders one turn: its heading and prompt from the turn_open,
// every rendering entry in file order, then the turn_close footer and a rule.
func writeTurnMarkdown(b *strings.Builder, turn []marotte.Entry) {
	results, calls := indexResults(turn)
	r := turnRender{results: results, calls: calls, plan: newestPlan(turn)}
	for i := range turn {
		r.writeEntry(b, &turn[i])
	}
	b.WriteString("---\n\n")
}

func (r *turnRender) writeEntry(b *strings.Builder, e *marotte.Entry) {
	switch e.Kind {
	case marotte.EntryKindTurnOpen:
		writeTurnOpenMarkdown(b, e)
	case marotte.EntryKindText, marotte.EntryKindSteerAck:
		// An ack's words are the agent's, lifted out of the reply they rode on, and its
		// payload carries them under the same `text` key a text entry does.
		writeTextMarkdown(b, e)
	case marotte.EntryKindThinking:
		writeThinkingMarkdown(b, e)
	case marotte.EntryKindToolCall:
		r.writeToolCall(b, e)
	case marotte.EntryKindToolResult:
		r.writeUnpairedResult(b, e)
	case marotte.EntryKindSteer:
		writeSteerMarkdown(b, e)
	case marotte.EntryKindPlan:
		r.writePlanOnce(b)
	case marotte.EntryKindCompaction, marotte.EntryKindCompactionFailed,
		marotte.EntryKindSafetyBlocked, marotte.EntryKindModelSwitched,
		marotte.EntryKindModeSwitched, marotte.EntryKindTurnRevert:
		writeEventMarkdown(b, e)
	case marotte.EntryKindTurnClose:
		writeTurnCloseEntry(b, e)
	case marotte.EntryKindTurnBind, marotte.EntryKindReconciled:
		// Neither renders, each for its own reason. turn_bind is bookkeeping: KAS's id for
		// the prompt renders nowhere. A reconciled states that a merge had nothing to
		// add, which is a fact about the RECORD rather than about the conversation.
	}
}

// writeEventMarkdown renders the six kinds the client draws as a boundary row, the set
// its EventEntryKind names.
func writeEventMarkdown(b *strings.Builder, e *marotte.Entry) {
	switch e.Kind {
	case marotte.EntryKindCompaction:
		writeCompactionMarkdown(b, e)
	case marotte.EntryKindCompactionFailed:
		writeCompactionFailedMarkdown(b, e)
	case marotte.EntryKindSafetyBlocked:
		writeSafetyBlockedMarkdown(b, e)
	case marotte.EntryKindModelSwitched:
		writeModelSwitchedMarkdown(b, e)
	case marotte.EntryKindModeSwitched:
		writeModeSwitchedMarkdown(b, e)
	case marotte.EntryKindTurnRevert:
		writeTurnRevertMarkdown(b)
	}
}

// writeTurnRevertMarkdown renders the revert's boundary row: the rule, then the words the
// client's REVERT_LABELS holds for the one cause. The payload is not read, because the
// entry's presence IS the cut — a payload this export could not decode still states it —
// and the wording is total over TurnRevertCause on the CLIENT, where a Record over the
// type makes a second cause a compile error rather than a silent default here.
func writeTurnRevertMarkdown(b *strings.Builder) {
	b.WriteString("---\n\n**Rewound to here**\n\n")
}

func (r *turnRender) writeToolCall(b *strings.Builder, e *marotte.Entry) {
	var call marotte.EntryToolCall
	if decodePayload(e, &call) {
		writeLaneNote(b, e.Lane)
		writeToolCallMarkdown(b, &call, r.results[call.ID])
	}
}

// writeUnpairedResult renders a result whose call a rewind cut away; a paired
// result was folded into its call's block.
func (r *turnRender) writeUnpairedResult(b *strings.Builder, e *marotte.Entry) {
	if _, paired := r.calls[toolCallIDOfResult(e.ID)]; paired {
		return
	}
	var res marotte.EntryToolResult
	if decodePayload(e, &res) {
		writeLaneNote(b, e.Lane)
		writeToolResultMarkdown(b, &res)
	}
}

func (r *turnRender) writePlanOnce(b *strings.Builder) {
	if r.planDone || len(r.plan) == 0 {
		return
	}
	writePlanMarkdown(b, r.plan)
	r.planDone = true
}

func writeTextMarkdown(b *strings.Builder, e *marotte.Entry) {
	var p marotte.EntryText
	if decodePayload(e, &p) {
		writeLaneNote(b, e.Lane)
		writeParagraph(b, p.Text)
	}
}

func writeThinkingMarkdown(b *strings.Builder, e *marotte.Entry) {
	var p marotte.EntryThinking
	if !decodePayload(e, &p) {
		return
	}
	text := strings.TrimSpace(p.Text)
	if text == "" {
		return
	}
	writeLaneNote(b, e.Lane)
	b.WriteString("<details>\n<summary>Reasoning</summary>\n\n")
	b.WriteString(text)
	b.WriteString("\n\n</details>\n\n")
}

func writeSteerMarkdown(b *strings.Builder, e *marotte.Entry) {
	var p marotte.EntrySteer
	if decodePayload(e, &p) {
		b.WriteString(steerHeading(&p))
		writeParagraph(b, p.Text)
	}
}

func writeCompactionMarkdown(b *strings.Builder, e *marotte.Entry) {
	var p marotte.EntryCompaction
	if !decodePayload(e, &p) {
		return
	}
	b.WriteString("**Event: compacted**\n\n")
	if summary := strings.TrimSpace(p.Summary); summary != "" {
		b.WriteString("<details>\n<summary>Summary</summary>\n\n")
		b.WriteString(summary)
		b.WriteString("\n\n</details>\n\n")
	}
}

func writeCompactionFailedMarkdown(b *strings.Builder, e *marotte.Entry) {
	var p marotte.EntryCompactionFailed
	if decodePayload(e, &p) {
		fmt.Fprintf(b, "**Event: compaction_failed** %s\n\n", oneLine(p.Reason))
	}
}

func writeSafetyBlockedMarkdown(b *strings.Builder, e *marotte.Entry) {
	var p marotte.EntrySafetyBlocked
	if !decodePayload(e, &p) {
		return
	}
	b.WriteString("**Event: infra_safety_blocked**\n\n")
	for _, prop := range p.Properties {
		fmt.Fprintf(b, "- %s\n", oneLine(prop))
	}
	b.WriteString("\n")
}

func writeModelSwitchedMarkdown(b *strings.Builder, e *marotte.Entry) {
	var p marotte.EntryModelSwitched
	if decodePayload(e, &p) {
		fmt.Fprintf(b, "**Event: model_switched** %s\n\n", modelSwitchedDetail(&p))
	}
}

func writeModeSwitchedMarkdown(b *strings.Builder, e *marotte.Entry) {
	var p marotte.EntryModeSwitched
	if !decodePayload(e, &p) {
		return
	}
	// Mode IDS, not display names, for modelSwitchedDetail's reason: an export is a
	// record, and the catalog that resolves a name is not in it.
	line := fmt.Sprintf("**Event: mode_switched** %s → %s", oneLine(p.From), oneLine(p.To))
	if p.Source == marotte.ModeSwitchSourceAgent {
		line += ", switched by the agent"
	}
	fmt.Fprintf(b, "%s\n\n", line)
}

// modelSwitchedDetail mirrors the transcript row's arms: ONE entry kind, TWO
// triggers, discriminated by From == To (see marotte.EntryModelSwitched). An empty
// To is the context reset and takes no tier.
//
// The tier is the WIRE VALUE, not a display label: an export is a record, and a
// second copy of the client's label table could disagree with it.
func modelSwitchedDetail(p *marotte.EntryModelSwitched) string {
	if p.To == "" {
		return "context reset"
	}
	tier := oneLine(p.Effort)
	if tier == "" {
		return fmt.Sprintf("%s → %s", oneLine(p.From), oneLine(p.To))
	}
	if p.From == p.To {
		return "reasoning effort: " + tier
	}
	return fmt.Sprintf("%s → %s (%s)", oneLine(p.From), oneLine(p.To), tier)
}

func writeTurnCloseEntry(b *strings.Builder, e *marotte.Entry) {
	var p marotte.EntryTurnClose
	if decodePayload(e, &p) {
		writeTurnCloseMarkdown(b, &p)
	}
}

// indexResults decodes every tool_result of a turn, keyed by the call it settles,
// beside the set of call ids the turn holds: a result whose call a rewind cut away
// is not in the second and renders on its own.
func indexResults(turn []marotte.Entry) (results map[string]*marotte.EntryToolResult, calls map[string]struct{}) {
	results = make(map[string]*marotte.EntryToolResult)
	calls = make(map[string]struct{})
	for i := range turn {
		switch turn[i].Kind {
		case marotte.EntryKindToolCall:
			calls[turn[i].ID] = struct{}{}
		case marotte.EntryKindToolResult:
			var res marotte.EntryToolResult
			if json.Unmarshal(turn[i].Payload, &res) == nil {
				results[toolCallIDOfResult(turn[i].ID)] = &res
			}
		default:
		}
	}
	return results, calls
}

// newestPlan is the turn's last plan entry's entries, or nil.
func newestPlan(turn []marotte.Entry) []marotte.PlanEntry {
	for _, e := range slices.Backward(turn) {
		if e.Kind != marotte.EntryKindPlan {
			continue
		}
		var p marotte.EntryPlan
		if json.Unmarshal(e.Payload, &p) == nil {
			return p.Entries
		}
	}
	return nil
}

// decodePayload decodes an entry's payload into p, answering whether it decoded: an
// entry that does not is skipped rather than failing the export.
func decodePayload(e *marotte.Entry, p any) bool {
	return json.Unmarshal(e.Payload, p) == nil
}

// writeTurnOpenMarkdown emits the turn heading, its timestamp, and the prompt with
// its attachment names when the reader opened the turn.
func writeTurnOpenMarkdown(b *strings.Builder, e *marotte.Entry) {
	var open marotte.EntryTurnOpen
	if !decodePayload(e, &open) {
		fmt.Fprintf(b, "## Turn\n\n")
		return
	}
	fmt.Fprintf(b, "## Turn %d\n\n", open.N)
	if ts := mdTimestamp(e.Ts); ts != "" {
		fmt.Fprintf(b, "_%s_\n\n", ts)
	}
	if open.Prompt == nil {
		if open.Source != "" {
			fmt.Fprintf(b, "_Opened by %s_\n\n", oneLine(string(open.Source)))
		}
		return
	}
	b.WriteString("**User**\n\n")
	writeParagraph(b, open.Prompt.Text)
	if len(open.Prompt.Attachments) > 0 {
		b.WriteString("Attachments:\n\n")
		for _, att := range open.Prompt.Attachments {
			fmt.Fprintf(b, "- `%s`\n", oneLine(att.Path))
		}
		b.WriteString("\n")
	}
}

// writeLaneNote marks an entry a delegate produced, so the export says whose words
// they are; the chat's own agent has lane "" and gets no note.
func writeLaneNote(b *strings.Builder, lane string) {
	if lane != "" {
		fmt.Fprintf(b, "_Delegate `%s`_\n\n", oneLine(lane))
	}
}

// writeParagraph emits trimmed text as a paragraph, nothing for empty text.
func writeParagraph(b *strings.Builder, text string) {
	if text = strings.TrimSpace(text); text != "" {
		b.WriteString(text)
		b.WriteString("\n\n")
	}
}

// steerHeading names a mid-turn message's origin and, for a user steer, whether the
// agent ever read it: a correction the agent never saw must stay distinguishable
// from the prompt above it, which is the one fact about a mid-turn message a reader
// most wants back out of an export.
func steerHeading(p *marotte.EntrySteer) string {
	if p.Origin == marotte.SteerOriginAgent {
		return "**Agent note**\n\n"
	}
	if p.State == marotte.SteerStateDropped {
		return "**User (mid-turn, not delivered)**\n\n"
	}
	return "**User (mid-turn)**\n\n"
}

// writePlanMarkdown renders the plan as a GitHub task-list checklist.
// Completed → [x]; in-progress → [ ] with a suffix (GFM has no third box).
func writePlanMarkdown(b *strings.Builder, plan []marotte.PlanEntry) {
	b.WriteString("**Plan**\n\n")
	for i := range plan {
		box, suffix := "[ ]", ""
		switch plan[i].Status {
		case marotte.PlanCompleted:
			box = "[x]"
		case marotte.PlanInProgress:
			suffix = " _(in progress)_"
		case marotte.PlanPending:
			// leave the default unchecked box
		}
		fmt.Fprintf(b, "- %s %s%s\n", box, oneLine(plan[i].Content), suffix)
	}
	b.WriteString("\n")
}

// untitledTool heads a tool call or result whose frame carried no title.
const untitledTool = "tool"

// writeToolCallMarkdown renders one tool call as a collapsible block: summary,
// duration, locations, then the sanitised input and output in fenced code blocks.
// The result, when the turn holds one, supplies the settled status, output and
// locations.
func writeToolCallMarkdown(b *strings.Builder, call *marotte.EntryToolCall, res *marotte.EntryToolResult) {
	title := oneLine(call.Title)
	if title == "" {
		title = oneLine(string(call.Kind))
	}
	if title == "" {
		title = untitledTool
	}
	status, declined := call.Status, call.Declined
	output, locations, durationMs := call.Output, call.Locations, call.DurationMs
	if res != nil {
		status, declined = res.Status, res.Declined
		output, durationMs = res.Output, res.DurationMs
		if len(res.Locations) > 0 {
			locations = res.Locations
		}
	}
	fmt.Fprintf(b, "<details>\n<summary>Tool: %s — %s</summary>\n\n", title, toolStatusWord(status, declined))
	if durationMs > 0 {
		fmt.Fprintf(b, "Duration: %dms\n\n", durationMs)
	}
	writeToolLocations(b, locations)
	if in := formatToolInput(call.Input); in != "" {
		b.WriteString("Input:\n\n")
		b.WriteString(fencedCode(sanitize.Output(in), "json"))
	}
	if out := strings.TrimSpace(output); out != "" {
		b.WriteString("Output:\n\n")
		b.WriteString(fencedCode(sanitize.Output(out), ""))
	}
	b.WriteString("</details>\n\n")
}

// writeToolResultMarkdown renders a result whose call the turn no longer holds.
func writeToolResultMarkdown(b *strings.Builder, res *marotte.EntryToolResult) {
	title := oneLine(res.Title)
	if title == "" {
		title = untitledTool
	}
	fmt.Fprintf(b, "<details>\n<summary>Tool result: %s — %s</summary>\n\n", title, toolStatusWord(res.Status, res.Declined))
	if res.DurationMs > 0 {
		fmt.Fprintf(b, "Duration: %dms\n\n", res.DurationMs)
	}
	writeToolLocations(b, res.Locations)
	if out := strings.TrimSpace(res.Output); out != "" {
		b.WriteString("Output:\n\n")
		b.WriteString(fencedCode(sanitize.Output(out), ""))
	}
	b.WriteString("</details>\n\n")
}

// toolStatusWord is the status the summary line prints. A DECLINED call ran
// correctly and refused, so the export says so rather than printing the bare
// `completed` its status carries — the same reason the card paints a fifth outcome
// instead of a green check.
func toolStatusWord(status marotte.ToolStatus, declined bool) string {
	if declined {
		return "declined"
	}
	if status == "" {
		return "unknown"
	}
	return string(status)
}

// writeToolLocations renders the tool call's file locations as a list.
func writeToolLocations(b *strings.Builder, locs []marotte.ToolLocation) {
	if len(locs) == 0 {
		return
	}
	b.WriteString("Locations:\n\n")
	for _, loc := range locs {
		if loc.Line > 0 {
			fmt.Fprintf(b, "- `%s:%d`\n", oneLine(loc.Path), loc.Line)
		} else {
			fmt.Fprintf(b, "- `%s`\n", oneLine(loc.Path))
		}
	}
	b.WriteString("\n")
}

// writeTurnCloseMarkdown emits the turn's footer: outcome, elapsed time, credits,
// model and the failure reason when there is one.
func writeTurnCloseMarkdown(b *strings.Builder, p *marotte.EntryTurnClose) {
	parts := []string{"Outcome: " + oneLine(string(p.Outcome))}
	if p.ElapsedMs > 0 {
		parts = append(parts, fmt.Sprintf("%.1fs", p.ElapsedMs/1000))
	}
	if p.Credits > 0 {
		parts = append(parts, fmt.Sprintf("%.2f credits", p.Credits))
	}
	if p.Model != "" {
		parts = append(parts, oneLine(p.Model))
	}
	fmt.Fprintf(b, "_%s_\n\n", strings.Join(parts, " · "))
	if reason := oneLine(p.FailureReason); reason != "" {
		fmt.Fprintf(b, "_Reason: %s_\n\n", reason)
	}
}

// formatToolInput pretty-prints a tool call's raw JSON input, or returns
// the trimmed raw string when it is not valid JSON. Empty/null yields "".
func formatToolInput(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err == nil {
		return pretty.String()
	}
	return s
}

// mdTimestamp formats a millisecond epoch as a UTC datetime, or "" for a
// zero/negative timestamp.
func mdTimestamp(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04:05 UTC")
}

// oneLine collapses CR/LF to spaces and trims, so a value can't break a
// heading, list item, or table cell it is interpolated into.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

// fencedCode wraps content in a Markdown code fence whose backtick run is
// always longer than the longest backtick run inside content, so embedded
// triple-backticks can't prematurely close the block.
func fencedCode(content, lang string) string {
	longest, run := 0, 0
	for _, r := range content {
		if r == '`' {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	fenceLen := 3
	if longest >= fenceLen {
		fenceLen = longest + 1
	}
	fence := strings.Repeat("`", fenceLen)
	var b strings.Builder
	b.WriteString(fence)
	b.WriteString(lang)
	b.WriteByte('\n')
	b.WriteString(strings.TrimRight(content, "\n"))
	b.WriteByte('\n')
	b.WriteString(fence)
	b.WriteString("\n\n")
	return b.String()
}
