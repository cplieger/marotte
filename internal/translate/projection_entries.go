package translate

// The ENTRY projection: a chat's turn log rebuilt from a `session/load` replay. A compaction
// separator closes no turn, a resent steer is not inferred, and a user row settles at its
// arrival position.

import (
	"cmp"
	"encoding/json"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rpcerr"
	"github.com/cplieger/marotte/internal/turnlog"
	"github.com/cplieger/runesafe/v2"
)

// ProjectedTurn is one turn as the replay describes it, entries in KAS order with Seq
// unassigned (the merge assigns positions). The merge's rule-one key is the turn_open prompt
// id: KAS's record id for the opening user row.
type ProjectedTurn struct {
	Entries []marotte.Entry
}

// replaySteerSource is `_meta.kiro.source` on all four steering shapes: it marks the CHANNEL,
// so the workflow-progress and empty-boundary drops must run before it decides anything.
const replaySteerSource = "steer"

// subagentInvocationKind is `_meta.kiro.kind` on the tool_call that starts a delegate, filed
// in the issuer's lane; the stamp alone cannot say so.
const subagentInvocationKind = "agent-subtask"

// laneSay is the say one lane is still accumulating: its entry id and its kind, so a
// thinking delta arriving over an open text say is treated as the barrier it is.
type laneSay struct {
	id   string
	kind marotte.EntryKind
}

// projectedFacts are the open turn's turn_close inputs, held because turn_completion arrives
// before the turn_end the close keys on.
type projectedFacts struct {
	// conclusion is the turn_end's; nil means no turn_end reached this turn (merge rule 4 closes it).
	conclusion *marotte.TurnConclusion
	// refusal is first-wins, like the live turn's: the tagged chunk latches it, and
	// turn_end's stopDetails fills it only for a turn whose chunk carried none.
	refusal *marotte.RefusalInfo
	// engine is the replayed display_error, last write winning like the live latch.
	engine *displayErrorBlock
	// footer is the asks, request ids, throughput, recoveries and steering, the
	// live turn's own accumulator applied to the replayed frames.
	footer    turnlog.Facts
	credits   float64
	elapsedMs float64
}

// EntryProjection accumulates a replayed transcript as entries. Not safe for
// concurrent use; one belongs to one in-flight `session/load`.
type EntryProjection struct {
	newID func() string
	// prompt is the user row the next turn opens with, and its ID is the merge's
	// rule-one key.
	prompt *marotte.EntryPrompt
	// sayAt maps a say id to its entry's index in cur and sayText to its text, so a later delta
	// EXTENDS that entry: two entries under one id would give the merge two candidates for a key.
	sayAt   map[string]int
	sayText map[string]string
	// carry is the marker filter's withheld tail per lane, carrySay the say it belongs to.
	carry    map[string]string
	carrySay map[string]string
	// calls maps an unsettled tool_call's entry id to the lane its result belongs in,
	// fixed at the create frame; callOrder is the order the turn's close aborts them
	// in.
	calls map[string]string
	// laneOpen is the say each lane is accumulating, so an id-less delta extends it; cleared when
	// another kind seals the lane.
	laneOpen map[string]laneSay
	// turns are the settled turns, oldest first.
	turns []ProjectedTurn
	// cur is the newest turn's entries, turn_open at index 0. Not moved into turns at the close:
	// content after a turn_end with no turn_start continues that turn.
	cur       []marotte.Entry
	callOrder []string
	// workDir is the workspace root a projected diff path is made relative to. A plain
	// string keeps the projection dependency-free.
	workDir string
	// curID is the newest turn's id, carried by every entry of it.
	curID string
	// lane is the lane the newest text or thinking delta arrived in, which is the
	// ISSUER's lane for a subagent invocation: the lane whose text was open when the
	// frame arrived.
	lane string
	// The user row being accumulated. Its identity is its FIRST chunk's, so a row with another id
	// flushes the first.
	userID       string
	userText     string
	userSeverity string
	userSender   string
	facts        projectedFacts
	userTs       int64
	// promptTs is the pending prompt's own timestamp, held beside it because flushUser
	// clears the accumulating row before the next turn_start reads it.
	promptTs int64
	// compactAt is where a summarization_separator landed in cur, or -1. The summary
	// that follows lands there rather than at the tail.
	compactAt int
	// emptySummaries counts this projection's empty-summary compactions: the k in
	// CompactionEntryID's `compaction-empty-<k>`.
	emptySummaries int
	// closed reports that cur's close already ran, so a second turn_end appends no
	// second turn_close — the merge requires one turn_open and one turn_close per turn.
	closed      bool
	userSteer   bool
	userPending bool
}

// NewEntryProjection returns an empty EntryProjection. newID mints unique ids for the bracket
// entries and id-less deltas (injected for determinism); workDir relativizes diff paths.
func NewEntryProjection(newID func() string, workDir string) *EntryProjection {
	return &EntryProjection{newID: newID, workDir: workDir, compactAt: -1}
}

// relPath normalizes a wire path reference to workspace-relative form, the rule the
// live path applies.
func (p *EntryProjection) relPath(ref string) string {
	return relPathIn(p.workDir, ref)
}

// Ingest folds one replayed session/update frame into the projection; unknown kinds, and
// `hook_update` (record-only, no twin to pair on), are ignored.
func (p *EntryProjection) Ingest(kind marotte.ACPUpdateKind, raw json.RawMessage) {
	switch kind {
	case marotte.ACPUpdateSessionInfo:
		p.ingestInfo(raw)
	case marotte.ACPUpdateAgentChunk:
		p.ingestAgentText(raw, marotte.EntryKindText)
	case marotte.ACPUpdateThoughtChunk:
		p.ingestAgentText(raw, marotte.EntryKindThinking)
	case marotte.ACPUpdateToolCall:
		p.ingestToolCall(raw)
	case marotte.ACPUpdateToolUpdate:
		p.ingestToolUpdate(raw)
	default:
		// user_message_chunk's kind constant is outside the ACPUpdate* set.
		if kind == replayUserChunkKind {
			p.ingestUserText(raw)
		}
	}
}

// Turns closes the newest turn and answers the projected turns as a fresh slice, so
// a caller appending cannot write into the projection's backing array. Idempotent:
// every step below is a no-op once it has run.
func (p *EntryProjection) Turns() []ProjectedTurn {
	p.flushUser()
	// A trailing prompt with no bracket is a turn the process never ran; it is kept because it
	// carries rule one's key.
	if p.prompt != nil {
		p.openTurn()
	}
	p.closeTurn()
	p.sealCur()
	return slices.Clone(p.turns)
}

func (p *EntryProjection) ingestUserText(raw json.RawMessage) {
	var c replayChunk
	if json.Unmarshal(raw, &c) != nil {
		return
	}
	// A workflow-progress row also carries the steering source, so this drop leads.
	if isWorkflowProgress(&c.Meta) {
		return
	}
	// Without this, an empty steering-boundary row would hijack the following prompt's identity
	// and mark the reader's own words as a steer.
	if id := c.Meta.Kiro.MessageID; p.userPending && id != "" && id != p.userID {
		p.flushUser()
	}
	if !p.userPending {
		// On a replayed chunk `_meta.kiro.messageId` is KAS's record id: rule one's key.
		p.userID = c.Meta.Kiro.MessageID
		p.userTs = replayTS(c.Meta.Kiro.Timestamp)
		p.userSteer = c.Meta.Kiro.Source == replaySteerSource
		p.userSeverity = c.Meta.Kiro.Notification.Status
		p.userSender = c.Meta.Kiro.Notification.Sender
	}
	p.userText += c.Content.Text
	p.userPending = true
}

// flushUser settles the accumulated user row: a prompt-class row becomes the NEXT turn's
// turn_open.prompt; a steer row is an entry at its own arrival (queue) position.
func (p *EntryProjection) flushUser() {
	if !p.userPending {
		return
	}
	text, id, ts := p.userText, p.userID, p.userTs
	steer, severity, sender := p.userSteer, p.userSeverity, p.userSender
	p.userText, p.userID, p.userTs, p.userSeverity, p.userSender = "", "", 0, "", ""
	p.userSteer, p.userPending = false, false
	// The empty steering-boundary row, which says nothing and opens nothing.
	if text == "" {
		return
	}
	if !steer {
		p.prompt, p.promptTs = &marotte.EntryPrompt{ID: id, Text: text}, ts
		return
	}
	// Lane-less: it settles every lane's carry and lands in the newest turn, after its close when
	// it had ended.
	p.ensureTurn(marotte.TurnOpenNameEvent)
	p.sealLanes()
	// state stays empty; a steer with no record twin is stamped `dropped, restart` by the merge.
	p.appendEntry(marotte.EntryKindSteer, id, "", ts, marotte.EntrySteer{
		Text:     text,
		Origin:   projectedSteerOrigin(id, sender),
		Severity: severity,
	})
}

// projectedSteerOrigin is a replayed steer's origin from its id (`steer-` is the reader's) and
// KAS's send_message sender; the merge takes the record's origin where it has one.
func projectedSteerOrigin(id, sender string) marotte.SteerOrigin {
	switch {
	case strings.HasPrefix(id, marotte.SteerIDPrefix):
		return marotte.SteerOriginUser
	case sender == senderParent:
		return marotte.SteerOriginParent
	default:
		return marotte.SteerOriginAgent
	}
}

func (p *EntryProjection) ingestAgentText(raw json.RawMessage, kind marotte.EntryKind) {
	var c replayChunk
	if json.Unmarshal(raw, &c) != nil || c.Content.Text == "" {
		return
	}
	p.flushUser()
	p.ensureTurn(marotte.TurnOpenNameWireTurnStart)
	lane := c.Meta.Kiro.AgentSubtaskID
	p.lane = lane
	// markRefusal's rule: the tagged chunk seals the lane and never becomes prose.
	if r := refusalFrom(c.Meta.Kiro.Refusal); r != nil {
		p.sealLane(lane)
		if p.facts.refusal == nil {
			p.facts.refusal = r
		}
		return
	}
	sayID := p.sayIDFor(kind, lane, c.Meta.Kiro.MessageID)
	// Another say, or thinking over text, is a barrier: settle the held carry first so a `[` tail
	// stays off the next say's head.
	if held, ok := p.heldSay(lane); ok && (held.id != sayID || held.kind != kind) {
		p.sealLane(lane)
	}
	text := c.Content.Text
	if kind == marotte.EntryKindText {
		// The live marker filter, required here: KAS replays the acks it never scrubbed, so without it
		// the projected text outgrows the record's. The acks are discarded.
		emit, held, _ := stripSteerAcks(p.carry[lane], text)
		p.setCarry(lane, sayID, held)
		text = emit
		if text == "" {
			return
		}
	}
	p.extendSay(kind, sayID, lane, replayTS(c.Meta.Kiro.Timestamp), text)
}

// sayIDFor is the say a delta belongs to (`<uuid>-say`, or `<reasoningActionId>-say` for a
// thought). An id-less frame extends the lane's current say of that kind; only a lane with
// none mints one.
func (p *EntryProjection) sayIDFor(kind marotte.EntryKind, lane, sayID string) string {
	if sayID != "" {
		return sayID
	}
	if held, ok := p.heldSay(lane); ok && held.kind == kind {
		return held.id
	}
	return p.newID()
}

// heldSay is the say a lane is still accumulating: its open entry, or the say a carry
// belongs to when every byte of it so far was withheld and no entry exists yet.
func (p *EntryProjection) heldSay(lane string) (laneSay, bool) {
	if open, ok := p.laneOpen[lane]; ok {
		return open, true
	}
	if say, ok := p.carrySay[lane]; ok {
		return laneSay{id: say, kind: marotte.EntryKindText}, true
	}
	return laneSay{}, false
}

// extendSay appends delta to the turn's entry for id, opening one if needed: one entry per say
// id per turn, whatever the frame order.
func (p *EntryProjection) extendSay(kind marotte.EntryKind, id, lane string, ts int64, delta string) {
	p.laneOpen[lane] = laneSay{id: id, kind: kind}
	if at, held := p.sayAt[id]; held {
		p.sayText[id] += delta
		p.rewritePayload(at, payloadForSay(kind, p.sayText[id]))
		return
	}
	// The index is recorded AFTER the append: insertEntry shifts every index at or past its own.
	at := len(p.cur)
	if !p.insertEntry(at, kind, id, lane, ts, payloadForSay(kind, delta)) {
		return
	}
	p.sayAt[id], p.sayText[id] = at, delta
}

// payloadForSay is the payload for a text or thinking entry. Two types carrying one
// field, so the kind decides which.
func payloadForSay(kind marotte.EntryKind, text string) any {
	if kind == marotte.EntryKindThinking {
		return marotte.EntryThinking{Text: text}
	}
	return marotte.EntryText{Text: text}
}

// setCarry replaces a lane's withheld tail and records the say a release belongs to.
func (p *EntryProjection) setCarry(lane, sayID, held string) {
	if held == "" {
		delete(p.carry, lane)
		delete(p.carrySay, lane)
		return
	}
	p.carry[lane] = held
	p.carrySay[lane] = sayID
}

// sealLane settles one lane's carry and closes its say: a carry starting with SteerAckPrefix
// is an unclosed marker and is dropped, anything else returns to its say (turnlog's seal rule).
func (p *EntryProjection) sealLane(lane string) {
	held, sayID := p.carry[lane], p.carrySay[lane]
	p.setCarry(lane, "", "")
	if held != "" && !strings.HasPrefix(held, turnlog.SteerAckPrefix) {
		p.extendSay(marotte.EntryKindText, sayID, lane, 0, held)
	}
	delete(p.laneOpen, lane)
}

// sealLanes seals every lane, which is what a lane-less entry and a turn's close both do
// first. Sorted, so a lane-less entry's own position is deterministic.
func (p *EntryProjection) sealLanes() {
	for _, lane := range slices.Sorted(maps.Keys(p.laneOpen)) {
		p.sealLane(lane)
	}
	for _, lane := range slices.Sorted(maps.Keys(p.carry)) {
		p.sealLane(lane)
	}
}

func (p *EntryProjection) ingestToolCall(raw json.RawMessage) {
	var tc ACPToolCallWire
	if json.Unmarshal(raw, &tc) != nil || tc.ToolCallID == "" {
		return
	}
	tc.gate()
	// The live path's suppression: KAS's log stores the cloud-config fetch it announced.
	if isInternalTool(tc.Meta.Kiro.ToolID) {
		return
	}
	p.flushUser()
	p.ensureTurn(marotte.TurnOpenNameWireTurnStart)
	ts := p.frameTS(tc.Meta.Kiro.Timestamp)
	// The live path's own builder, so one frame decodes one way.
	wire := toolCallFromWire(&tc, "",
		parseToolContent(p.relPath, tc.ToolCallID, tc.Content), ts)
	call := marotte.EntryToolCallOf(&wire)
	lane := tc.Meta.Kiro.AgentSubtaskID
	if tc.Meta.Kiro.Kind == subagentInvocationKind {
		// An invocation is filed in the ISSUER's lane; its stamp (the DELEGATE's uuid) keys the
		// delegate's own lane.
		lane = p.lane
		call.AgentSubtaskID = tc.Meta.Kiro.AgentSubtaskID
	}
	// A laned entry seals its own lane first.
	p.sealLane(lane)
	p.trackCall(tc.ToolCallID, lane)
	p.appendEntry(marotte.EntryKindToolCall, tc.ToolCallID, lane, ts, call)
}

// ingestToolUpdate folds a replayed tool_call_update into the call's tool_result when the
// update SETTLES it (a replay carries the terminal status on the update). A non-terminal
// update leaves the call open for the close's abort rule.
func (p *EntryProjection) ingestToolUpdate(raw json.RawMessage) {
	var tu ACPToolCallUpdateWire
	if json.Unmarshal(raw, &tu) != nil || tu.ToolCallID == "" {
		return
	}
	tu.gate()
	if !tu.Status.Terminal() {
		return
	}
	lane, known := p.calls[tu.ToolCallID]
	if !known {
		// No create in this turn (dropped as internal, or malformed): nothing to settle.
		return
	}
	if stamp := tu.Meta.Kiro.AgentSubtaskID; stamp != "" && stamp != lane {
		// The call's lane is fixed at the create frame; the replayed update carries no stamp.
		slog.Warn("entry projection: tool_call_update lane disagrees with its call's lane",
			"turn", p.curID, "tool_call", tu.ToolCallID, "call_lane", lane, "update_lane", stamp)
	}
	content := parseToolContent(p.relPath, tu.ToolCallID, tu.Content)
	res := marotte.EntryToolResult{
		Title:      displayText(tu.Title),
		Kind:       tu.Kind,
		Status:     tu.Status,
		Output:     content.output,
		TerminalID: content.terminalID,
		Diffs:      content.diffs,
		Locations:  tu.Locations,
		Checkpoint: checkpointFrom(tu.Meta.Kiro.Checkpoint),
		Disclosed:  disclosedFrom(tu.Meta.Kiro.DisclosedContext),
		Denial:     denialFrom(tu.Meta.Kiro.PolicyDenial),
		Offload:    offloadFrom(tu.Meta.Kiro.OutputTransformation),
		// The meta read carries the id on a replay; rawOutput is the live path's spelling.
		WorkflowID: cmp.Or(tu.Meta.Kiro.WorkflowID, rawOutputWorkflowID(tu.RawOutput)),
	}
	p.sealLane(lane)
	p.settleCall(tu.ToolCallID)
	p.appendEntry(marotte.EntryKindToolResult, marotte.ToolResultID(tu.ToolCallID), lane,
		p.frameTS(tu.Meta.Kiro.Timestamp), *p.facts.footer.WithInteraction(tu.ToolCallID, &res))
}

// checkpointFrom maps KAS's checkpoint block onto the domain type, nil for the tool
// calls that touch no file.
func checkpointFrom(in *ACPCheckpointMeta) *marotte.ToolCheckpoint {
	if in == nil || (in.Original == "" && in.Modified == "" && in.Local == "") {
		return nil
	}
	return &marotte.ToolCheckpoint{Original: in.Original, Modified: in.Modified, Local: in.Local}
}

// noteAskFact records an ask, its answer or the steering added on the open turn;
// outside a turn there is nothing to record it on.
func (p *EntryProjection) noteAskFact(k *replayInfoKiro) {
	if p.cur == nil {
		return
	}
	switch {
	case k.PendingInteraction != nil:
		q := k.PendingInteraction
		p.facts.footer.NoteAsk(q.ToolCallID, q.InteractionType, q.askOptions())
	case k.InteractionResolved != nil:
		r := k.InteractionResolved
		p.facts.footer.NoteAnswer(r.ToolCallID, r.Outcome, displayText(r.SelectedOption))
	case k.Kind == infoKindSteeringInclusion:
		p.facts.footer.NoteSteering(steeringDocIDs(k.SteeringDocuments))
	}
}

func (p *EntryProjection) ingestInfo(raw json.RawMessage) {
	var u replayInfoMeta
	if json.Unmarshal(raw, &u) != nil {
		return
	}
	switch u.Meta.Kiro.Kind {
	case infoKindTurnStart:
		// Close, THEN flush, THEN open: flushing first would attribute an old-bracket steer to the new turn.
		p.closeTurn()
		p.flushUser()
		p.openTurn()
	case infoKindTurnCompletion:
		p.noteTurnMetering(u.Meta.Kiro.PromptTurnSummaries, u.Meta.Kiro.ElapsedTime)
		k := &u.Meta.Kiro
		p.facts.footer.NoteTurnCompletion(boundedIDs(k.RequestIDs), boundedIDs(k.Recoveries), k.Throughput.summary())
	case infoKindPendingInteraction, infoKindInteractionResolved, infoKindSteeringInclusion:
		p.noteAskFact(&u.Meta.Kiro)
	case infoKindDisplayError:
		if d := u.Meta.Kiro.DisplayError; d != nil && p.cur != nil && d.ErrorType != displayErrorMCPConnection && d.Message != "" {
			p.facts.engine = d
		}
	case infoKindTurnEnd:
		// The payload BEFORE the close, so closeTurn can stamp it; the close keys on the KIND.
		p.noteTurnEnd(u.Meta.Kiro.TurnEnd)
		p.closeTurn()
	case infoKindSeparator:
		// No close: a separator inside a bracket lands the compaction inside the open turn. The seal
		// runs here, where the compaction lands, so a released carry's say precedes it.
		p.flushUser()
		p.ensureTurn(marotte.TurnOpenNameEvent)
		p.sealLanes()
		p.compactAt = len(p.cur)
	case infoKindSummary:
		p.applySummary(u.Meta.Kiro.SummaryMessage)
	}
}

// noteTurnMetering records what the replayed turn_completion says this turn spent.
// The credit sum matches the live path's: an empty unit or the credit unit counts,
// anything else is a dimension this does not price.
func (p *EntryProjection) noteTurnMetering(summaries []promptTurnSummary, elapsedMs float64) {
	for i := range summaries {
		if summaries[i].Unit == "" || summaries[i].Unit == meteringUnitCredit {
			p.facts.credits += summaries[i].Usage
		}
	}
	p.facts.elapsedMs = elapsedMs
}

// noteTurnEnd reads the replayed turn_end payload into the open turn's facts. A bracket
// carrying no payload records ConcludeStopReason's answer for an unstated reason, because
// the wire requires an outcome on every turn_close. The mapping is delegated, never
// re-implemented.
func (p *EntryProjection) noteTurnEnd(e *turnEndBlock) {
	if e == nil {
		c := marotte.ConcludeStopReason("")
		p.facts.conclusion = &c
		return
	}
	c := marotte.ConcludeStopReason(marotte.StopReason(e.StopReason))
	p.facts.conclusion = &c
	if p.facts.refusal == nil {
		p.facts.refusal = stopDetailsRefusal(e.StopDetails)
	}
}

// applySummary appends the compaction entry at the separator's own position, with the
// summary-derived id both sides mint from the same bytes — which is what makes the
// live entry and its replayed twin one entry to the merge.
func (p *EntryProjection) applySummary(sum *struct {
	Content string `json:"content"`
},
) {
	if sum == nil || p.compactAt < 0 {
		return
	}
	at := min(p.compactAt, len(p.cur))
	p.compactAt = -1
	summary := []byte(sum.Content)
	if len(summary) == 0 {
		p.emptySummaries++
	}
	id := marotte.CompactionEntryID(summary, p.emptySummaries)
	// summary_message carries no timestamp: inherit the neighbour's, not the load's clock.
	var ts int64
	if at > 0 {
		ts = p.cur[at-1].Ts
	}
	p.insertEntry(at, marotte.EntryKindCompaction, id, "", ts,
		marotte.EntryCompaction{Summary: sum.Content})
}

// ensureTurn opens the turn an entry lands in: a pending prompt opens its own; else the newest
// turn, open or CLOSED, takes it; only with none does a headerless turn open with `source`.
func (p *EntryProjection) ensureTurn(source marotte.TurnOpenSourceName) {
	if p.prompt != nil {
		p.openTurn()
		return
	}
	if p.cur != nil {
		return
	}
	p.startTurn(source, nil, 0)
}

// openTurn settles the newest turn and starts one from the pending prompt.
func (p *EntryProjection) openTurn() {
	p.closeTurn()
	p.sealCur()
	prompt, ts := p.prompt, p.promptTs
	p.prompt, p.promptTs = nil, 0
	source := marotte.TurnOpenNameWireTurnStart
	if prompt != nil {
		source = marotte.TurnOpenNamePrompt
	}
	p.startTurn(source, prompt, ts)
}

// startTurn appends the turn_open later entries hang off. `n` stays zero: the merged log numbers turns.
func (p *EntryProjection) startTurn(source marotte.TurnOpenSourceName, prompt *marotte.EntryPrompt, ts int64) {
	p.curID = p.newID()
	p.cur = nil
	p.sayAt = map[string]int{}
	p.sayText = map[string]string{}
	p.carry = map[string]string{}
	p.carrySay = map[string]string{}
	p.laneOpen = map[string]laneSay{}
	p.calls = map[string]string{}
	p.callOrder = nil
	p.lane = ""
	p.closed = false
	p.compactAt = -1
	p.facts = projectedFacts{}
	p.appendEntry(marotte.EntryKindTurnOpen, p.curID, "", ts, marotte.EntryTurnOpen{
		Prompt: prompt,
		Source: source,
	})
}

// closeTurn settles the newest turn and appends its turn_close when a turn_end reached it.
// A closed turn gets no second closer; one no turn_end reached gets none here (merge rule 4).
func (p *EntryProjection) closeTurn() {
	if p.cur == nil {
		return
	}
	p.settle()
	c := p.facts.conclusion
	if p.closed || c == nil {
		return
	}
	p.closed = true
	reason, kind := c.Reason, c.FailureKind
	if e := p.facts.engine; e != nil && reason == "" && marotte.SeverityOf(c.Outcome) == marotte.TurnSeverityBroken {
		m := rpcerr.Mapped{ErrorType: e.ErrorType, RetryErrorType: e.RetryErrorType}
		reason, _ = runesafe.SanitizeSingleLineCapped(rpcerr.Account(m, e.Message), maxDisplayErrorBytes, "...")
		if m.ErrorType == rpcerr.ContextWindowExceededError {
			kind = marotte.FailureKindContextLimit
		}
	}
	footer := marotte.EntryTurnClose{
		Outcome:       c.Outcome,
		StopReasonRaw: string(c.RawStop),
		FailureReason: reason,
		FailureKind:   kind,
		Truncated:     c.Truncated,
		Credits:       p.facts.credits,
		ElapsedMs:     p.facts.elapsedMs,
		Refusal:       p.facts.refusal,
	}
	p.facts.footer.Stamp(&footer)
	if e := p.facts.engine; e != nil {
		footer.EngineErrorClass = turnlog.EngineClass(e.ErrorType, c.Outcome)
	}
	p.appendEntry(marotte.EntryKindTurnClose, p.newID(), "", 0, footer)
}

// settle seals every lane's carry and aborts every tool call the replay never settled,
// which is what the live closer does to a turn's open content.
func (p *EntryProjection) settle() {
	p.sealLanes()
	// A call with no result is aborted in its own lane; KAS's synthetic `failed` result pre-empts it.
	for _, id := range p.callOrder {
		lane := p.calls[id]
		p.appendEntry(marotte.EntryKindToolResult, marotte.ToolResultID(id), lane, 0,
			*p.facts.footer.WithInteraction(id, &marotte.EntryToolResult{Status: marotte.ToolAborted}))
	}
	p.calls, p.callOrder = map[string]string{}, nil
}

// sealCur moves the newest turn into the settled list.
func (p *EntryProjection) sealCur() {
	if p.cur == nil {
		return
	}
	p.turns = append(p.turns, ProjectedTurn{Entries: p.cur})
	p.cur = nil
	p.curID = ""
}

// trackCall records a call as unsettled, in the order a close aborts them in.
func (p *EntryProjection) trackCall(id, lane string) {
	if _, dup := p.calls[id]; dup {
		return
	}
	p.calls[id] = lane
	p.callOrder = append(p.callOrder, id)
}

func (p *EntryProjection) settleCall(id string) {
	delete(p.calls, id)
	p.callOrder = slices.DeleteFunc(p.callOrder, func(s string) bool { return s == id })
}

// frameTS is a frame's own timestamp, falling back to the turn's turn_open when the
// frame carries none.
func (p *EntryProjection) frameTS(timestamp string) int64 {
	if ts := replayTS(timestamp); ts != 0 {
		return ts
	}
	if len(p.cur) > 0 {
		return p.cur[0].Ts
	}
	return 0
}

// appendEntry appends one entry to the newest turn. Seq is left unassigned.
func (p *EntryProjection) appendEntry(kind marotte.EntryKind, id, lane string, ts int64, payload any) {
	p.insertEntry(len(p.cur), kind, id, lane, ts, payload)
}

// insertEntry places one entry at `at`, shifting the say index for what moved, and reports
// whether it landed.
func (p *EntryProjection) insertEntry(at int, kind marotte.EntryKind, id, lane string, ts int64, payload any) bool {
	raw, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("entry projection: could not marshal a replayed payload",
			"turn", p.curID, "kind", kind, "error", err)
		return false
	}
	e := marotte.Entry{ID: id, Turn: p.curID, Lane: lane, Kind: kind, Payload: raw, Ts: ts}
	p.cur = slices.Insert(p.cur, at, e)
	for say, idx := range p.sayAt {
		if idx >= at {
			p.sayAt[say] = idx + 1
		}
	}
	return true
}

// rewritePayload replaces one entry's payload in place, for a say a later delta
// extended.
func (p *EntryProjection) rewritePayload(at int, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("entry projection: could not marshal an extended say",
			"turn", p.curID, "error", err)
		return
	}
	p.cur[at].Payload = raw
}
