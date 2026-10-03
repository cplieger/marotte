package translate

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/buffer"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
)

// baseDeps is a composable Deps implementation for tests and benchmarks.
// By default all methods are no-ops; set the hook fields to override
// specific behaviors (e.g. onBroadcast to capture events).
type baseDeps struct {
	store       ChatRecords
	turns       *turnLogs
	lineTracker *buffer.LineTracker
	onBroadcast func(context.Context, marotte.ServerEvent)
	// onSetGovernance, when set, is invoked by SetGovernance so a test can
	// assert the runtime-side cache write (mirrors onBroadcast).
	onSetGovernance func(marotte.GovernanceStatePayload)
	// scheduledRuns are the workflow ids IsScheduled answers true for, so a
	// test can stage a scheduled run without a runtime or a scheduler.
	scheduledRuns map[string]bool
	// runNotices is the per-chat queue RunNotice consumes, oldest first, so a test
	// can stage a finished run behind a notify-wf- notice without a run registry.
	runNotices map[marotte.ChatID][]stagedRunNotice
	// runProgress records the workflow id of every RunMadeProgress call, in
	// order, so a test can assert a step's frame refilled its run's idle window
	// without a lease store behind it.
	runProgress []string
	// folds records where every fold site filed a frame's content, in order, so
	// a test can assert a workflow step's frame reached the RUN's turn rather
	// than the chat's.
	folds []foldRecord
	// runCalls records every RunAppender call, in order.
	runCalls []runCall
	// between holds the entries filed after a chat's newest turn_close, and
	// runBetween the entries filed after a run path's, keyed by runPathKey.
	between    map[marotte.ChatID][]marotte.Entry
	runBetween map[string][]marotte.Entry
	// prompts stages the chat's prompt-class turn PromptTurn answers.
	prompts map[marotte.ChatID]*turnlog.Turn
	// compactionFailures records failed-compaction facts sent to the host.
	compactionFailures []compactionFailure
	// turnInterrupts records every InterruptTurn call, so a test can assert the
	// sentinel ended the turn exactly once and named its cause, with no bridge
	// behind it.
	turnInterrupts []turnInterrupt
	// catalogModels is the last model list SetModels was handed: the workspace
	// catalog a config_option_update publishes, which used to be a field on the
	// chat record.
	catalogModels []marotte.SessionModel
	// parent is returned by ParentACPSession; zero value "" preserves the
	// historical "parent unknown" behavior for existing callers.
	parent string
	// asked records every BridgeRespond call, so a test can assert that a frame
	// marotte declined to process was still ANSWERED on its own id, with no
	// bridge behind it. respondErr, when set, is what BridgeRespond reports.
	asked      []askAnswer
	respondErr error
	// terminals stands in for the runtime's agent-terminal registry, keyed by
	// terminal id, so adoptTerminalOutput is exercisable without one. A key
	// present with an empty text is a REGISTERED terminal that printed nothing,
	// which the real registry reports as ok — the distinction the miss warning
	// keys on.
	terminals map[string]termRendered
	// brackets records every turn-lifecycle call, so a test can assert which
	// bracket a frame drove without a turn registry behind it.
	brackets []turnBracket
	// userSteers are the steer ids this double reports as the USER's, standing
	// in for the host's ledger of what marotte itself sent. Absent means the
	// agent's, which is the real ledger's answer too.
	userSteers map[string]bool
	// steerResends are the resends the double reports per steer id.
	steerResends map[string][]string
	// waiting is the steers this double has been told are in KAS's buffer, per
	// chat, standing in for the runtime's tracker. A map rather than a call log
	// because what a test asserts is the SET a reconnect would replay, and the
	// three arms of the cascade reach it as add / remove / remove-each.
	waiting map[marotte.ChatID][]marotte.SteerQueuedPayload
	// queuedAnswer, when set, is the frame SteerWaiting answers, standing in for
	// the record rewriting a user row's frame or refusing a chat that is going.
	queuedAnswer func(marotte.SteerQueuedPayload) (marotte.SteerQueuedPayload, bool)
}

// termRendered is one terminal's rendered output in the stub registry.
type termRendered struct {
	text  string
	spans []marotte.TextSpan
}

func newBaseDeps() *baseDeps {
	return &baseDeps{
		store:       nopChatRecords{},
		turns:       newTurnLogs(),
		lineTracker: buffer.NewLineTracker(),
		terminals:   map[string]termRendered{},
		waiting:     map[marotte.ChatID][]marotte.SteerQueuedPayload{},
		between:     map[marotte.ChatID][]marotte.Entry{},
		runBetween:  map[string][]marotte.Entry{},
		prompts:     map[marotte.ChatID]*turnlog.Turn{},
	}
}

// SteerWaiting / SteerRead / SteerForgotten / SteerCleared stand in for the
// runtime's steering buffer tracker, holding what a reconnect would re-offer.
func (d *baseDeps) SteerWaiting(chatID marotte.ChatID, in *marotte.SteerQueuedPayload) (marotte.SteerQueuedPayload, bool) {
	p := *in
	p.State = marotte.SteerRowQueued
	if d.queuedAnswer != nil {
		var ok bool
		if p, ok = d.queuedAnswer(p); !ok {
			return p, false
		}
	}
	for i, e := range d.waiting[chatID] {
		if e.SteerID == p.SteerID {
			d.waiting[chatID][i] = p
			return p, true
		}
	}
	d.waiting[chatID] = append(d.waiting[chatID], p)
	return p, true
}

// SteerCleared answers the agent rows alone, like the record: a user row's entry
// is written at its own terminal transition.
func (d *baseDeps) SteerCleared(chatID marotte.ChatID, steerIDs []string) []marotte.SteerQueuedPayload {
	return slices.DeleteFunc(d.forget(chatID, steerIDs), func(p marotte.SteerQueuedPayload) bool {
		return p.Origin == marotte.SteerOriginUser
	})
}

func (d *baseDeps) SteerRead(chatID marotte.ChatID, steerID string) {
	d.forget(chatID, []string{steerID})
}

func (d *baseDeps) SteerForgotten(chatID marotte.ChatID, steerIDs []string) []marotte.SteerQueuedPayload {
	return d.forget(chatID, steerIDs)
}

// forget returns the entries it HELD, like the real buffer: that subset is the
// steers nothing read, and the only place their text survives once the cleared
// frame (which carries ids alone) arrives.
func (d *baseDeps) forget(chatID marotte.ChatID, steerIDs []string) []marotte.SteerQueuedPayload {
	gone := make(map[string]bool, len(steerIDs))
	for _, id := range steerIDs {
		gone[id] = true
	}
	var held []marotte.SteerQueuedPayload
	kept := d.waiting[chatID][:0]
	for _, e := range d.waiting[chatID] {
		if gone[e.SteerID] {
			held = append(held, e)
			continue
		}
		kept = append(kept, e)
	}
	d.waiting[chatID] = kept
	return held
}

func (d *baseDeps) Output(terminalID string) (string, []marotte.TextSpan, bool) {
	t, ok := d.terminals[terminalID]
	return t.text, t.spans, ok
}

// SteerOrigin stands in for the host's steer ledger. Absent from the set means
// the agent's, matching command.SteerLedger, whose answer is total.
func (d *baseDeps) SteerOrigin(_ marotte.ChatID, steerID string) marotte.SteerOrigin {
	if d.userSteers[steerID] {
		return marotte.SteerOriginUser
	}
	return marotte.SteerOriginAgent
}

// SteerResends stands in for the ledger's resends record, keyed by steer id.
func (d *baseDeps) SteerResends(_ marotte.ChatID, steerID string) []string {
	return d.steerResends[steerID]
}

func (d *baseDeps) Broadcast(ctx context.Context, evt marotte.ServerEvent) {
	if d.onBroadcast != nil {
		d.onBroadcast(ctx, evt)
	}
}

// The three GETTERS this double used to answer (ChatRecords, BufferStore,
// LineTracker) are gone with the composites: Roles holds each interface
// directly, so the double implements the methods instead of handing back a
// narrower self.
func (d *baseDeps) Get(ctx context.Context, id marotte.ChatID) (*marotte.Chat, bool) {
	return d.store.Get(ctx, id)
}

func (d *baseDeps) Mutate(ctx context.Context, id marotte.ChatID, fn func(*marotte.Chat, bool) bool) (string, error) {
	return d.store.Mutate(ctx, id, fn)
}

func (d *baseDeps) PromptTexts(ctx context.Context, id marotte.ChatID) ([]string, error) {
	return d.store.PromptTexts(ctx, id)
}

func (d *baseDeps) EmptyCompactions(ctx context.Context, id marotte.ChatID) (int, error) {
	return d.store.EmptyCompactions(ctx, id)
}

// The TurnAccess half: one open turn per chat over the recording sink, so a test
// reads what a frame SEALED rather than a buffer's state. Every fold is recorded
// with its target, which is how a test tells a chat fold from a run fold.
func (d *baseDeps) OwnTurn(chatID marotte.ChatID) (*turnlog.Turn, bool) {
	t := d.turns.chats[chatID]
	return t, t != nil && !t.Closed()
}

func (d *baseDeps) TurnFoldTarget(_ context.Context, chatID marotte.ChatID) *turnlog.Turn {
	d.folds = append(d.folds, foldRecord{chat: chatID})
	return d.turns.chatTurn(chatID)
}

func (d *baseDeps) PromptTurn(chatID marotte.ChatID) (*turnlog.Turn, bool) {
	t := d.prompts[chatID]
	return t, t != nil
}

func (d *baseDeps) AppendBetweenTurns(_ context.Context, chatID marotte.ChatID, e *marotte.Entry) (*marotte.Entry, error) {
	if d.turns.appendErr != nil {
		return nil, d.turns.appendErr
	}
	d.between[chatID] = append(d.between[chatID], *e)
	return nil, nil
}

// chatEntries is every entry the chat's current turn sealed, in seal order; the
// content still coalescing is on the turn's OpenEntries.
func (d *baseDeps) chatEntries(chatID marotte.ChatID) []marotte.Entry {
	return d.turns.entriesOf(d.turns.chats[chatID])
}

// runEntries is every entry the run path's turn sealed, in seal order.
func (d *baseDeps) runEntries(runID, nodePath string) []marotte.Entry {
	return d.turns.entriesOf(d.turns.runs[runPathKey(runID, nodePath)])
}

// toolCallsOf decodes every tool_call entry in entries, in seal order.
func toolCallsOf(t *testing.T, entries []marotte.Entry) []marotte.EntryToolCall {
	t.Helper()
	var out []marotte.EntryToolCall
	for i := range entries {
		if entries[i].Kind != marotte.EntryKindToolCall {
			continue
		}
		var c marotte.EntryToolCall
		if err := json.Unmarshal(entries[i].Payload, &c); err != nil {
			t.Fatalf("decode tool_call %q: %v", entries[i].ID, err)
		}
		out = append(out, c)
	}
	return out
}

// toolResultsOf decodes every tool_result entry in entries, in seal order.
func toolResultsOf(t *testing.T, entries []marotte.Entry) []marotte.EntryToolResult {
	t.Helper()
	var out []marotte.EntryToolResult
	for i := range entries {
		if entries[i].Kind != marotte.EntryKindToolResult {
			continue
		}
		var r marotte.EntryToolResult
		if err := json.Unmarshal(entries[i].Payload, &r); err != nil {
			t.Fatalf("decode tool_result %q: %v", entries[i].ID, err)
		}
		out = append(out, r)
	}
	return out
}

// plansOf decodes every plan entry in entries, in seal order.
func plansOf(t *testing.T, entries []marotte.Entry) []marotte.EntryPlan {
	t.Helper()
	var out []marotte.EntryPlan
	for i := range entries {
		if entries[i].Kind != marotte.EntryKindPlan {
			continue
		}
		var p marotte.EntryPlan
		if err := json.Unmarshal(entries[i].Payload, &p); err != nil {
			t.Fatalf("decode plan %q: %v", entries[i].ID, err)
		}
		out = append(out, p)
	}
	return out
}

// turnCloseOf decodes the one turn_close entry in entries; the test fails when the
// turn is still open.
func turnCloseOf(t *testing.T, entries []marotte.Entry) marotte.EntryTurnClose {
	t.Helper()
	for i := range entries {
		if entries[i].Kind != marotte.EntryKindTurnClose {
			continue
		}
		var c marotte.EntryTurnClose
		if err := json.Unmarshal(entries[i].Payload, &c); err != nil {
			t.Fatalf("decode turn_close %q: %v", entries[i].ID, err)
		}
		return c
	}
	t.Fatal("no turn_close entry: the turn is still open")
	return marotte.EntryTurnClose{}
}

// hasEntryAppended reports whether events carries an entry_appended frame whose entry
// is of kind: the frame every entry born sealed travels as.
func hasEntryAppended(events *[]marotte.ServerEvent, kind marotte.EntryKind) bool {
	for _, e := range *events {
		if e.Type != marotte.EventEntryAppended {
			continue
		}
		if p, ok := e.Payload.(marotte.EntryAppendedPayload); ok && p.Entry.Kind == kind {
			return true
		}
	}
	return false
}

// lastFold is the target the most recent fold site stated, and whether any fold
// happened at all.
func (d *baseDeps) lastFold() (foldRecord, bool) {
	if len(d.folds) == 0 {
		return foldRecord{}, false
	}
	return d.folds[len(d.folds)-1], true
}

// The RunAppender half: one open turn per run path, recorded call by call.
func (d *baseDeps) RunNodeStart(_ context.Context, runID, nodePath, sessionID string, chatID marotte.ChatID) {
	d.runCalls = append(d.runCalls, runCall{kind: "node_start", runID: runID, nodePath: nodePath, sessionID: sessionID, chat: chatID})
	d.turns.runTurn(runID, nodePath)
}

func (d *baseDeps) RunNodeComplete(ctx context.Context, runID, nodePath, status, reason string) {
	d.runCalls = append(d.runCalls, runCall{kind: "node_complete", runID: runID, nodePath: nodePath, status: status, reason: reason})
	if t, ok := d.turns.runs[runPathKey(runID, nodePath)]; ok && !t.Closed() {
		_, _ = t.Close(ctx, marotte.TurnConclusion{Outcome: marotte.TurnOutcomeCompleted, Reason: reason})
	}
}

func (d *baseDeps) RunFoldTarget(_ context.Context, runID, nodePath, sessionID string, chatID marotte.ChatID) (*turnlog.Turn, bool) {
	d.folds = append(d.folds, foldRecord{chat: chatID, runID: runID, nodePath: nodePath})
	if t, ok := d.turns.runs[runPathKey(runID, nodePath)]; ok && t.Closed() {
		return nil, false
	}
	d.runCalls = append(d.runCalls, runCall{kind: "fold", runID: runID, nodePath: nodePath, sessionID: sessionID, chat: chatID})
	return d.turns.runTurn(runID, nodePath), true
}

func (d *baseDeps) RunAppendAfterClosed(_ context.Context, runID, nodePath string, e *marotte.Entry) error {
	d.runCalls = append(d.runCalls, runCall{kind: "after_closed", runID: runID, nodePath: nodePath})
	k := runPathKey(runID, nodePath)
	d.runBetween[k] = append(d.runBetween[k], *e)
	return nil
}

func (d *baseDeps) RunMeter(runID, nodePath string, credits, elapsedMs float64) bool {
	d.runCalls = append(d.runCalls, runCall{kind: "meter", runID: runID, nodePath: nodePath, credits: credits, elapsedMs: elapsedMs})
	t, ok := d.turns.runs[runPathKey(runID, nodePath)]
	return ok && !t.Closed()
}

func (d *baseDeps) RunStopReason(runID, nodePath string, raw marotte.StopReason) bool {
	d.runCalls = append(d.runCalls, runCall{kind: "stop_reason", runID: runID, nodePath: nodePath, stop: raw})
	t, ok := d.turns.runs[runPathKey(runID, nodePath)]
	return ok && !t.Closed()
}

// foldRecord is one fold site's target: a chat's own turn when runID is empty,
// else the run path's.
type foldRecord struct {
	chat     marotte.ChatID
	runID    string
	nodePath string
}

// runCall is one recorded RunAppender call.
type runCall struct {
	kind      string
	runID     string
	nodePath  string
	sessionID string
	status    string
	reason    string
	chat      marotte.ChatID
	stop      marotte.StopReason
	credits   float64
	elapsedMs float64
}

// The three turn-bracket operations, recorded rather than performed: the host
// owns the turn lifecycle, and what this package is responsible for is calling
// the right one on the right frame.
func (d *baseDeps) WireTurnStart(_ context.Context, chatID marotte.ChatID) {
	d.brackets = append(d.brackets, turnBracket{chat: chatID, kind: "start"})
}

func (d *baseDeps) WireTurnEnd(
	_ context.Context,
	chatID marotte.ChatID,
	stop marotte.StopReason,
	details string,
) {
	d.brackets = append(d.brackets, turnBracket{chat: chatID, kind: "end", stop: stop, details: details})
}

func (d *baseDeps) ReviseTurnBinding(_ context.Context, chatID marotte.ChatID) {
	d.brackets = append(d.brackets, turnBracket{chat: chatID, kind: "revise"})
}

// turnBracket is one recorded turn-lifecycle call.
type turnBracket struct {
	chat marotte.ChatID
	kind string
	stop marotte.StopReason
	// details is the wire's own account of the stop, from turn_end's stopDetails.
	// Recorded because it is the ONLY channel that can explain a `stopReason:
	// "error"` turn, and it reached the closer as "" for as long as nobody decoded
	// it — which is why a failed turn persisted a mark and no words.
	details string
}

// turnLogs stands in for the host's two registries: one open turn per chat and
// one per run path, each accumulating over a sink that keeps the sealed entries
// by turn id. It is NOT what production does — there a fold with no open turn
// opens a wire_turn_start turn through the store — but the property this package
// is responsible for is the same either way: a fold gets somewhere to land, and
// the same somewhere for the rest of the turn.
type turnLogs struct {
	chats  map[marotte.ChatID]*turnlog.Turn
	runs   map[string]*turnlog.Turn
	sealed map[string][]marotte.Entry
	// appendErr, when set, is what every sink append and every between-turns
	// append answers, recording nothing: the store refusing a write.
	appendErr error
	minted    int
}

func newTurnLogs() *turnLogs {
	return &turnLogs{
		chats:  map[marotte.ChatID]*turnlog.Turn{},
		runs:   map[string]*turnlog.Turn{},
		sealed: map[string][]marotte.Entry{},
	}
}

// chatTurn is the chat's open turn, opened on first use and reopened once a
// close has landed, so a second turn on one chat gets its own id.
func (tl *turnLogs) chatTurn(chatID marotte.ChatID) *turnlog.Turn {
	if t, ok := tl.chats[chatID]; ok && !t.Closed() {
		return t
	}
	t := tl.open()
	tl.chats[chatID] = t
	return t
}

func (tl *turnLogs) runTurn(runID, nodePath string) *turnlog.Turn {
	k := runPathKey(runID, nodePath)
	if t, ok := tl.runs[k]; ok {
		return t
	}
	t := tl.open()
	tl.runs[k] = t
	return t
}

func (tl *turnLogs) open() *turnlog.Turn {
	tl.minted++
	id := fmt.Sprintf("t%d", tl.minted)
	return turnlog.Open(id, &recordingSink{logs: tl, turn: id})
}

// entriesOf is every entry the turn sealed so far, in seal order.
func (tl *turnLogs) entriesOf(t *turnlog.Turn) []marotte.Entry {
	if t == nil {
		return nil
	}
	return tl.sealed[t.ID()]
}

// recordingSink is the store the accumulator seals into: the entries land in
// the log keyed by turn, in seal order, which is what a test reads back.
type recordingSink struct {
	logs *turnLogs
	turn string
}

func (s *recordingSink) Append(_ context.Context, e *marotte.Entry) error {
	if s.logs.appendErr != nil {
		return s.logs.appendErr
	}
	s.logs.sealed[s.turn] = append(s.logs.sealed[s.turn], *e)
	return nil
}

func runPathKey(runID, nodePath string) string { return runID + "\x00" + nodePath }

func (d *baseDeps) RecordFromDiffs(chatID marotte.ChatID, diffs []marotte.ToolDiff, turn int, kind string) {
	d.lineTracker.RecordFromDiffs(chatID, diffs, turn, kind)
}
func (d *baseDeps) ParentACPSession(marotte.ChatID) string { return d.parent }
func (d *baseDeps) IsScheduled(workflowID string) bool {
	return d.scheduledRuns[workflowID]
}

// stagedRunNotice is one finished run a test hands RunNotice.
type stagedRunNotice struct {
	workflowID string
	producedTs int64
}

func (d *baseDeps) RunNotice(chatID marotte.ChatID) (string, int64, bool) {
	q := d.runNotices[chatID]
	if len(q) == 0 {
		return "", 0, false
	}
	d.runNotices[chatID] = q[1:]
	return q[0].workflowID, q[0].producedTs, true
}

func (d *baseDeps) RunMadeProgress(workflowID string) {
	d.runProgress = append(d.runProgress, workflowID)
}

type compactionFailure struct {
	chatID marotte.ChatID
	detail string
}

func (d *baseDeps) CompactionFailed(chatID marotte.ChatID, detail string) {
	d.compactionFailures = append(d.compactionFailures, compactionFailure{chatID: chatID, detail: detail})
}

// turnInterrupt is one recorded InterruptTurn call.
type turnInterrupt struct {
	chatID marotte.ChatID
	reason string
}

func (d *baseDeps) InterruptTurn(chatID marotte.ChatID, reason string) {
	d.turnInterrupts = append(d.turnInterrupts, turnInterrupt{chatID, reason})
}

// AccumulateSpend and StageConversationTurnSummary stand in for the host's
// per-turn accounting. The double writes the chat's usage directly, which is what
// the host does for a frame that reaches it with no turn open — the only state a
// translate fixture can be in, since the turn record lives on the host.
func (d *baseDeps) AccumulateSpend(ctx context.Context, chatID marotte.ChatID, credits float64) {
	if credits <= 0 {
		return
	}
	d.mutateUsage(ctx, chatID, func(u *marotte.Usage) {
		u.Credits += credits
		u.HasRealData = true
	})
}

func (d *baseDeps) StageConversationTurnSummary(ctx context.Context, chatID marotte.ChatID, elapsedMs float64) {
	d.mutateUsage(ctx, chatID, func(u *marotte.Usage) {
		if elapsedMs > 0 {
			u.LastTurnMs = elapsedMs
		}
	})
}

func (d *baseDeps) mutateUsage(ctx context.Context, chatID marotte.ChatID, apply func(*marotte.Usage)) {
	_, _ = d.Mutate(ctx, chatID, func(c *marotte.Chat, exists bool) bool {
		if !exists {
			return false
		}
		apply(&c.Usage)
		return true
	})
}

func (d *baseDeps) WorkDir() string { return "/tmp" }
func (d *baseDeps) BridgeNotify(context.Context, marotte.ChatID, string, map[string]any) error {
	return nil
}

// askAnswer is one recorded BridgeRespond call.
type askAnswer struct {
	chatID    marotte.ChatID
	requestID int64
	result    any
	rpcErr    error
}

func (d *baseDeps) BridgeRespond(
	_ context.Context, chatID marotte.ChatID, requestID int64, result any, rpcErr error,
) error {
	d.asked = append(d.asked, askAnswer{chatID, requestID, result, rpcErr})
	return d.respondErr
}
func (d *baseDeps) MCPRecorder() MCPRecorder { return nopMCPRecorder{} }
func (d *baseDeps) SetGovernance(g marotte.GovernanceStatePayload) {
	if d.onSetGovernance != nil {
		d.onSetGovernance(g)
	}
}
func (d *baseDeps) PendingPermsAdd(int64, marotte.ServerEvent)                           {}
func (d *baseDeps) PendingPermsRemove(int64)                                             {}
func (d *baseDeps) NotifyPush(context.Context, string, marotte.PushKind, marotte.ChatID) {}
func (d *baseDeps) IsHookStatusEnabled() bool                                            { return false }

// SetModels stands in for the workspace catalog holder, recording what the
// translator published so a test can assert on the list without an agent
// runtime. Empty is ignored, matching the production holder's rule.
func (d *baseDeps) SetModels(models []marotte.SessionModel) bool {
	if len(models) == 0 {
		return false
	}
	d.catalogModels = models
	return true
}

var toolCallPayload = json.RawMessage(`{"toolCallId":"tc-1","title":"ReadFile","kind":"read","status":"pending","rawInput":{},"locations":[],"content":[{"type":"text","content":{"text":"reading file"}}]}`)

func BenchmarkTranslator_HandleToolCall(b *testing.B) {
	tr := New(rolesOf(newBaseDeps()))
	ctx := b.Context()
	chatID := marotte.ChatID("bench-chat")

	for b.Loop() {
		tr.HandleToolCall(ctx, chatID, toolCallPayload, FrameAttribution{})
	}
}

// BenchmarkTranslator_HandleAssistantChunk measures per-token allocation
// overhead on the steady-state path (buffer already started).
func BenchmarkTranslator_HandleAssistantChunk(b *testing.B) {
	deps := newBaseDeps()
	tr := New(rolesOf(deps))
	ctx := b.Context()
	chatID := marotte.ChatID("bench-chunk")

	chunkPayload := json.RawMessage(`{"content":{"type":"text","text":"Hello world, this is a streaming token. "}}`)

	// Prime the buffer with a first chunk so subsequent iterations hit the
	// steady-state path (no message creation overhead).
	tr.HandleAssistantChunk(ctx, chatID, chunkPayload, false, FrameAttribution{})

	b.ReportAllocs()
	for b.Loop() {
		tr.HandleAssistantChunk(ctx, chatID, chunkPayload, false, FrameAttribution{})
	}
}

// BenchmarkTranslator_FullTurn simulates a complete streaming turn:
// 50 text chunks → 1 tool call → 1 tool call update → 50 more chunks.
// Measures end-to-end throughput including buffer management.
func BenchmarkTranslator_FullTurn(b *testing.B) {
	deps := newBaseDeps()
	tr := New(rolesOf(deps))
	ctx := b.Context()

	chunkPayload := json.RawMessage(`{"content":{"type":"text","text":"Hello world, this is a streaming token. "}}`)
	toolCallPL := toolCallPayload
	toolUpdatePL := json.RawMessage(`{"toolCallId":"tc-1","status":"completed","content":[{"type":"text","content":{"text":"done"}}]}`)

	for b.Loop() {
		chatID := marotte.ChatID("bench-turn")
		// Phase 1: initial streaming chunks
		for range 50 {
			tr.HandleAssistantChunk(ctx, chatID, chunkPayload, false, FrameAttribution{})
		}
		// Phase 2: tool call
		tr.HandleToolCall(ctx, chatID, toolCallPL, FrameAttribution{})
		tr.HandleToolCallUpdate(ctx, chatID, toolUpdatePL, FrameAttribution{})
		// Phase 3: more streaming
		for range 50 {
			tr.HandleAssistantChunk(ctx, chatID, chunkPayload, false, FrameAttribution{})
		}
		// Cleanup: a fresh turn for the next iteration
		delete(deps.turns.chats, chatID)
	}
}

func BenchmarkTranslator_HandleUsageUpdate(b *testing.B) {
	deps := newBaseDeps()
	tr := New(rolesOf(deps))
	ctx := b.Context()
	chatID := marotte.ChatID("bench-usage")
	raw := json.RawMessage(`{"size":100000,"used":42500}`)

	// Pre-create a chat so Mutate finds it.
	_, _ = deps.store.Mutate(ctx, chatID, func(_ *marotte.Chat, _ bool) bool { return true })

	b.ReportAllocs()
	for b.Loop() {
		tr.HandleUsageUpdate(ctx, chatID, raw)
	}
}
