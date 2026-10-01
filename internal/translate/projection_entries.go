package translate

// The ENTRY projection: a chat's turn log rebuilt from a `session/load` replay, beside
// the message projection rather than in place of it. A SECOND type rather than a flag
// on Projection because three rules differ: a compaction separator closes no turn, a
// resent steer is not inferred (the record carries the state), and a user row settles
// at its arrival position rather than at the next bracket.

import (
	"cmp"
	"encoding/json"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
)

// ProjectedTurn is one turn as the replay describes it: its entries in KAS order,
// Seq unassigned, because a projection holds no durable position and the merge is
// what assigns one.
//
// The turn id is Entries[0].Turn, minted by the injected generator, and the merge's
// rule-one pairing key is the turn_open payload's prompt id — on this side KAS's own
// record id for the user row that opened the bracket.
type ProjectedTurn struct {
	Entries []marotte.Entry
}

// replaySteerSource is `_meta.kiro.source` on every one of the four steering shapes,
// so it marks the CHANNEL rather than a reader's steer: the workflow-progress drop
// and the empty-boundary drop must both run before it decides anything. Declared
// here, beside the only code that reads it.
const replaySteerSource = "steer"

// subagentInvocationKind is `_meta.kiro.kind` on the tool_call that starts a
// delegate. It is what makes the call an INVOCATION, filed in the issuer's lane with
// the delegate uuid in its payload; the stamp alone cannot say so, because the
// pipeline driver carries none while an ordinary call inside a delegate carries one.
const subagentInvocationKind = "agent-subtask"

// laneSay is the say one lane is still accumulating: its entry id and its kind, so a
// thinking delta arriving over an open text say is treated as the barrier it is.
type laneSay struct {
	id   string
	kind marotte.EntryKind
}

// projectedFacts are the open turn's turn_close inputs, held rather than stamped on
// arrival: a turn replays as `turn_completion` then `turn_end`, and the close keys on
// the second, so the metering has to survive one frame.
type projectedFacts struct {
	// conclusion is what the turn_end frame said, and nil means no turn_end reached
	// this turn at all: the replay ended inside it, which is the crash the merge's
	// rule 4 closes rather than the projection.
	conclusion *marotte.TurnConclusion
	credits    float64
	elapsedMs  float64
}

// EntryProjection accumulates a replayed transcript as entries. Not safe for
// concurrent use; one belongs to one in-flight `session/load`.
type EntryProjection struct {
	newID func() string
	// prompt is the user row the next turn opens with, and its ID is the merge's
	// rule-one key.
	prompt *marotte.EntryPrompt
	// sayAt maps a say id to its entry's index in cur and sayText to that entry's
	// coalesced text, so a delta for a say the turn already holds EXTENDS that entry:
	// section 7.2 emits one text entry per say id and the merge pairs on it, so a
	// second entry under one id would give the merge two candidates for one key.
	sayAt   map[string]int
	sayText map[string]string
	// carry is the marker filter's withheld tail per lane, and carrySay the say the
	// released bytes belong to. Owned here rather than in a turnlog.Turn, which is the
	// live path's accumulator and holds no replay.
	carry    map[string]string
	carrySay map[string]string
	// calls maps an unsettled tool_call's entry id to the lane its result belongs in,
	// fixed at the create frame; callOrder is the order the turn's close aborts them
	// in.
	calls map[string]string
	// laneOpen is the say each lane is still accumulating, which is what lets a delta
	// whose frame carried NO id extend the entry it belongs to rather than minting a
	// second one. Cleared when an entry of another kind seals that lane, the live
	// accumulator's own barrier.
	laneOpen map[string]laneSay
	// turns are the settled turns, oldest first.
	turns []ProjectedTurn
	// cur is the newest turn's entries, open or closed, with its turn_open at index 0.
	// It is NOT moved into turns at the close: content arriving after a turn_end with
	// no turn_start of its own continues that turn, and keeping it here is what lets
	// the say index and the unsettled calls continue with it.
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
	// The user row being accumulated. KAS writes one record per row, so a replay sends
	// one chunk; the text still accumulates, because a row's identity is its FIRST
	// chunk's and a second row with another id has to flush the first.
	userID       string
	userText     string
	userSeverity string
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

// NewEntryProjection returns an empty EntryProjection. newID must produce unique
// ids; it mints the two bracket entries and a delta whose frame carried none, and the
// caller supplies it so a test is deterministic. workDir is the workspace root a diff
// path is made relative to; empty leaves paths as sent.
//
// The two parameters have different types, so a caller cannot transpose them.
func NewEntryProjection(newID func() string, workDir string) *EntryProjection {
	return &EntryProjection{newID: newID, workDir: workDir, compactAt: -1}
}

// relPath normalizes a wire path reference to workspace-relative form, the rule the
// live path applies.
func (p *EntryProjection) relPath(ref string) string {
	return relPathIn(p.workDir, ref)
}

// Ingest folds one replayed session/update frame into the projection. Unknown kinds
// are ignored: a replay carries catalog and telemetry frames a transcript cannot use,
// and `hook_update` is a session_info sub-kind this switch deliberately drops (a hook
// card is record-only, and the projection has no operationId twin to pair on).
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
		// user_message_chunk is handled here because its kind constant lives outside
		// the ACPUpdate* set marotte declares (it has never had a live handler).
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
	// A trailing prompt with no bracket after it is a turn the reader opened and the
	// process never ran. Represented rather than dropped: it carries rule one's key, so
	// the record's own prompt turn pairs with it instead of standing unpaired.
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
	// A workflow-progress row rides this same frame type and carries the steering
	// source too, so this drop must lead: it is machine state for the run card.
	if isWorkflowProgress(&c.Meta) {
		return
	}
	// Two rows with no frame of another kind between them merge into one carrying the
	// FIRST row's identity without this, so the empty steering-boundary row hijacks
	// the prompt that follows it and stamps `steer` onto the reader's own words.
	if id := c.Meta.Kiro.MessageID; p.userPending && id != "" && id != p.userID {
		p.flushUser()
	}
	if !p.userPending {
		// `_meta.kiro.messageId` on a REPLAYED user chunk is KAS's own record id (the
		// frame is built FROM the record), which is what makes it rule one's key.
		p.userID = c.Meta.Kiro.MessageID
		p.userTs = replayTS(c.Meta.Kiro.Timestamp)
		p.userSteer = c.Meta.Kiro.Source == replaySteerSource
		p.userSeverity = c.Meta.Kiro.Notification.Status
	}
	p.userText += c.Content.Text
	p.userPending = true
}

// flushUser settles the accumulated user row.
//
// A prompt-class row becomes the NEXT turn's turn_open.prompt and emits nothing. A
// steer row is an entry at its own arrival position, which is why every path that
// appends an entry flushes first: the replay places a steer at its QUEUE time inside
// the bracket, and landing it there rather than after the reply is the reordering
// this design exists for.
func (p *EntryProjection) flushUser() {
	if !p.userPending {
		return
	}
	text, id, ts := p.userText, p.userID, p.userTs
	steer, severity := p.userSteer, p.userSeverity
	p.userText, p.userID, p.userTs, p.userSeverity = "", "", 0, ""
	p.userSteer, p.userPending = false, false
	// The empty steering-boundary row, which says nothing and opens nothing.
	if text == "" {
		return
	}
	if !steer {
		p.prompt, p.promptTs = &marotte.EntryPrompt{ID: id, Text: text}, ts
		return
	}
	// Lane-less, so it settles every lane's carry first, and it lands in whatever turn
	// was newest when it arrived — after that turn's turn_close when the turn had
	// already ended, which is the between-turns rule.
	p.ensureTurn(marotte.TurnOpenNameEvent)
	p.sealLanes()
	// state stays empty: the projection knows only that KAS holds the words. A steer
	// with no record twin is stamped `dropped, restart` by the merge that inserts it.
	p.appendEntry(marotte.EntryKindSteer, id, "", ts, marotte.EntrySteer{
		Text:     text,
		Origin:   projectedSteerOrigin(id),
		Severity: severity,
	})
}

// projectedSteerOrigin is a replayed steer's origin from its id alone: KAS's
// `steer-` prefix is the reader's own and every other prefix is the agent's, which is
// the rule the live steerOrigin leads on.
//
// No ledger is consulted, unlike the live path: a projection has none, and the
// merge's union takes the record's origin wherever the record has one.
func projectedSteerOrigin(id string) marotte.SteerOrigin {
	if strings.HasPrefix(id, marotte.SteerIDPrefix) {
		return marotte.SteerOriginUser
	}
	return marotte.SteerOriginAgent
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
	sayID := p.sayIDFor(kind, lane, c.Meta.Kiro.MessageID)
	// A delta for another say, or a thinking delta over a text say, is the live
	// accumulator's barrier: the lane's held carry belongs to the say it was withheld
	// from, and settling it here is what keeps a `[` tail off the next say's head.
	if held, ok := p.heldSay(lane); ok && (held.id != sayID || held.kind != kind) {
		p.sealLane(lane)
	}
	text := c.Content.Text
	if kind == marotte.EntryKindText {
		// The same marker filter the live path runs, per delta, and not optional here:
		// KAS replays its own log with the acknowledgements it never scrubbed, so
		// without it the projected text is LONGER than the record's by the marker and
		// the union's text row appends machinery to the record's last segment on every
		// session/load. The acks are DISCARDED — the record has them, and an ack whose
		// seal a crash lost is one line of machinery text the reader never saw.
		emit, held, _ := stripSteerAcks(p.carry[lane], text)
		p.setCarry(lane, sayID, held)
		text = emit
		if text == "" {
			return
		}
	}
	p.extendSay(kind, sayID, lane, replayTS(c.Meta.Kiro.Timestamp), text)
}

// sayIDFor is the say a delta belongs to: `<uuid>-say` on a text chunk and a distinct
// `<reasoningActionId>-say` on a thought chunk, which is what makes the two sides'
// segment ids agree.
//
// A frame carrying NO id extends the lane's current say of the same kind, matching the
// live accumulator, and only a lane with none mints one — the message projection's own
// fallback for an id-less frame.
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

// extendSay appends delta to the turn's entry for id, opening one when the turn holds
// none. One entry per say id per turn, whatever the frame order: the merge pairs a
// projected `S` against every record entry whose id is `S` or `S#k`, so two projected
// entries under one id would give it two candidates for one key.
func (p *EntryProjection) extendSay(kind marotte.EntryKind, id, lane string, ts int64, delta string) {
	p.laneOpen[lane] = laneSay{id: id, kind: kind}
	if at, held := p.sayAt[id]; held {
		p.sayText[id] += delta
		p.rewritePayload(at, payloadForSay(kind, p.sayText[id]))
		return
	}
	// The index is recorded AFTER the append, because insertEntry shifts every index at
	// or past its own position and would otherwise bump the entry it just placed.
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

// sealLane settles one lane's carry and closes the say it was accumulating, which is
// what an entry of another kind in that lane does.
//
// The carry rule is turnlog's seal rule, restated rather than called because that
// runs inside the live accumulator this projection does not hold: a carry starting
// with SteerAckPrefix is a marker the model never closed and is dropped; anything
// else goes back into its say.
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
	// The live path's own suppression: KAS's log stores the cloud-config fetch it
	// announced during session creation, so without this a resumed chat regains the
	// card the live stream dropped.
	if isInternalTool(tc.Meta.Kiro.ToolID) {
		return
	}
	p.flushUser()
	p.ensureTurn(marotte.TurnOpenNameWireTurnStart)
	ts := p.frameTS(tc.Meta.Kiro.Timestamp)
	// The live path's own builder, so one frame decodes one way; the hand-written
	// literal it replaces is how the message projection came to drop the terminal
	// link, the diffs, the disclosure and the denial.
	wire := toolCallFromWire(&tc, "",
		parseToolContent(p.relPath, tc.ToolCallID, tc.Content), ts)
	call := marotte.EntryToolCallOf(&wire)
	lane := tc.Meta.Kiro.AgentSubtaskID
	if tc.Meta.Kiro.Kind == subagentInvocationKind {
		// An invocation is filed in the ISSUER's lane and the frame's stamp is the
		// DELEGATE's uuid, which becomes the key of the lane the delegate's own entries
		// carry. So the card sits between the issuer's two prose runs, as it arrived.
		lane = p.lane
		call.AgentSubtaskID = tc.Meta.Kiro.AgentSubtaskID
	}
	// A laned entry seals its own lane first.
	p.sealLane(lane)
	p.trackCall(tc.ToolCallID, lane)
	p.appendEntry(marotte.EntryKindToolCall, tc.ToolCallID, lane, ts, call)
}

// ingestToolUpdate folds a replayed tool_call_update into the tool_result entry for
// the call the preceding tool_call opened, when the update SETTLES the call. A replay
// sends the terminal status on the update (the persisted status is `approved`), so a
// projected card lands settled. An update carrying no status or a non-terminal one is
// progress the log never holds: a tool_result is a settled value, and the wire
// requires its status, so the call stays open for the close's abort rule instead.
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
		// An update with no create in this turn has nothing to settle: the create was
		// dropped as an internal tool, or the replay is malformed.
		return
	}
	if stamp := tu.Meta.Kiro.AgentSubtaskID; stamp != "" && stamp != lane {
		// The call's lane is fixed at the create frame and there is no late adoption.
		// The replayed invocation update carries no stamp at all, so the call's lane is
		// the only correct source on this side.
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
		// The meta read leads because it is what carries the id on a replay; the
		// rawOutput read is the live path's own spelling of the same fact.
		WorkflowID: cmp.Or(tu.Meta.Kiro.WorkflowID, rawOutputWorkflowID(tu.RawOutput)),
	}
	p.sealLane(lane)
	p.settleCall(tu.ToolCallID)
	p.appendEntry(marotte.EntryKindToolResult, marotte.ToolResultID(tu.ToolCallID), lane,
		p.frameTS(tu.Meta.Kiro.Timestamp), res)
}

// checkpointFrom maps KAS's checkpoint block onto the domain type, nil for the tool
// calls that touch no file.
func checkpointFrom(in *ACPCheckpointMeta) *marotte.ToolCheckpoint {
	if in == nil || (in.Original == "" && in.Modified == "" && in.Local == "") {
		return nil
	}
	return &marotte.ToolCheckpoint{Original: in.Original, Modified: in.Modified, Local: in.Local}
}

func (p *EntryProjection) ingestInfo(raw json.RawMessage) {
	var u replayInfoMeta
	if json.Unmarshal(raw, &u) != nil {
		return
	}
	switch u.Meta.Kiro.Kind {
	case infoKindTurnStart:
		// Close, THEN flush, THEN open. A start with a turn still open means its end
		// never arrived; flushing first would attribute a steer that arrived inside the
		// old bracket to the new turn.
		p.closeTurn()
		p.flushUser()
		p.openTurn()
	case infoKindTurnCompletion:
		p.noteTurnMetering(u.Meta.Kiro.PromptTurnSummaries, u.Meta.Kiro.ElapsedTime)
	case infoKindTurnEnd:
		// The payload BEFORE the close, so closeTurn can stamp it; the close still keys
		// on the KIND, so a bracket carrying no payload closes the turn either way.
		p.noteTurnEnd(u.Meta.Kiro.TurnEnd)
		p.closeTurn()
	case infoKindSeparator:
		// NO close, which is the message projection's one rule this type drops: that
		// close existed to keep a message boundary between the segment and the summary,
		// and under entries there is no message to keep a boundary between. So a
		// separator inside a bracket lands the compaction inside the open turn and the
		// frames after the summary continue it. The seal runs HERE, where the
		// compaction will land, so a say a released carry creates precedes it.
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
	c.Reason = displayText(stopDetailsText(e.StopDetails))
	p.facts.conclusion = &c
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
	// The summary_message frame carries no timestamp, so the entry inherits the entry
	// it lands behind; the load's own clock would date replayed history to now.
	var ts int64
	if at > 0 {
		ts = p.cur[at-1].Ts
	}
	p.insertEntry(at, marotte.EntryKindCompaction, id, "", ts,
		marotte.EntryCompaction{Summary: sum.Content})
}

// ensureTurn opens the turn an entry lands in when the projection holds none. Three
// arms in order: a pending prompt opens its own turn; otherwise the newest turn takes
// it, open or CLOSED, because content with neither a turn_start nor a user row before
// it continues the turn it follows; only with no turn at all does a headerless one
// open with the caller's `source`.
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

// startTurn appends the turn_open every later entry hangs off. `n` is left at zero:
// the transcript ordinal belongs to the merged log, which renumbers every turn by its
// own order.
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

// closeTurn settles the newest turn and appends the turn_close when a turn_end reached
// it. The settle runs on every call, closer or not: content after a turn_end continues
// the turn (7.2), and Turns() must settle that continuation too.
//
// A turn that already closed gets no second closer (the merge requires one per turn),
// and a turn no turn_end reached gets none from here either: KAS's account ends where
// the frames end, so the merge's rule 4 synthesizes the closer an inserted turn needs.
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
	p.appendEntry(marotte.EntryKindTurnClose, p.newID(), "", 0, marotte.EntryTurnClose{
		Outcome:       c.Outcome,
		StopReasonRaw: string(c.RawStop),
		FailureReason: c.Reason,
		Truncated:     c.Truncated,
		Credits:       p.facts.credits,
		ElapsedMs:     p.facts.elapsedMs,
	})
}

// settle seals every lane's carry and aborts every tool call the replay never settled,
// which is what the live closer does to a turn's open content.
func (p *EntryProjection) settle() {
	p.sealLanes()
	// A call with no result is aborted in its own lane. KAS's synthetic `failed` result
	// pre-empts it, having settled the call.
	for _, id := range p.callOrder {
		lane := p.calls[id]
		p.appendEntry(marotte.EntryKindToolResult, marotte.ToolResultID(id), lane, 0,
			marotte.EntryToolResult{Status: marotte.ToolAborted})
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

// insertEntry places one entry at `at`, shifting the say index for everything the
// insertion moved so a later delta still extends the entry it names. It reports whether
// the entry landed: a payload this file builds cannot fail to marshal, so a failure is
// said out loud rather than leaving a caller to record an index for a row that is not
// there.
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
