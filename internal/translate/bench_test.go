package translate

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/buffer"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/notice"
	"github.com/cplieger/marotte/internal/turnlog"
	"github.com/cplieger/marotte/internal/workflow"
)

// baseDeps is a composable Deps for tests and benchmarks: no-ops by default, hook fields to
// override (e.g. onBroadcast).
type baseDeps struct {
	store       chatRecords
	turns       *turnLogs
	lineTracker *buffer.LineTracker
	onBroadcast func(context.Context, marotte.ServerEvent)
	// onSetGovernance, when set, is invoked by SetGovernance so a test can
	// assert the runtime-side cache write (mirrors onBroadcast).
	onSetGovernance func(marotte.GovernanceStatePayload)
	// onSetRegistry and onPolicyChanged observe the other two governance writes.
	onSetRegistry   func(*marotte.GovernanceMCPRegistry)
	onPolicyChanged func(errs []marotte.PolicyErrorItem, reloaded bool)
	// withdrawn records each PendingPermsWithdraw call as "<chat>/<toolCallID>".
	withdrawn []string
	// notices records each Notify call, in order.
	notices []marotte.NotificationPayload
	// scheduledRuns are the workflow ids IsScheduled answers true for, so a
	// test can stage a scheduled run without a runtime or a scheduler.
	scheduledRuns map[string]bool
	// runLabels are the labels RunLabel answers, keyed by workflow id.
	runLabels map[string]string
	// stepReading are the step steer keys RunStepReading answers true for.
	stepReading map[marotte.ChatID]bool
	planUpdates []marotte.RunPlanUpdate
	// runNotices is the per-chat queue RunNotice consumes, oldest first, so a test
	// can stage a finished run behind a notify-wf- notice without a run registry.
	runNotices map[marotte.ChatID][]stagedRunNotice
	// runProgress records each RunMadeProgress workflow id, in order.
	runProgress []string
	// folds records where every fold site filed a frame's content, in order.
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
	// catalogModels is the last model list SetModels was handed.
	catalogModels []marotte.SessionModel
	// parent is returned by ParentACPSession; "" means parent unknown.
	parent string
	// asked records every answer sent on origin(); respondErr, when set, is what it reports.
	asked      []askAnswer
	respondErr error
	// terminals stands in for the agent-terminal registry; a key with empty text is a registered
	// terminal that printed nothing (what the miss warning keys on).
	terminals map[string]termRendered
	// brackets records every turn-lifecycle call, so a test can assert which
	// bracket a frame drove without a turn registry behind it.
	brackets []turnBracket
	// userSteers are the steer ids this double reports as the USER's; absent means the agent's.
	userSteers map[string]bool
	// waiting is the steers this double holds as in KAS's buffer, per chat: the SET a reconnect
	// would replay.
	waiting map[marotte.ChatID][]marotte.SteerQueuedPayload
	// queuedAnswer, when set, is the frame SteerWaiting answers, standing in for
	// the record rewriting a user row's frame or refusing a chat that is going.
	queuedAnswer func(marotte.SteerQueuedPayload) (marotte.SteerQueuedPayload, bool)
	// stopped is the turn ids StopRequestedAfter answers true for.
	stopped map[string]bool
}

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

func (d *baseDeps) SteerRead(chatID marotte.ChatID, steerID string) []string {
	if held := d.forget(chatID, []string{steerID}); len(held) > 0 {
		return held[0].Replaces
	}
	return nil
}

func (d *baseDeps) SteerForgotten(chatID marotte.ChatID, steerIDs []string) []marotte.SteerQueuedPayload {
	return d.forget(chatID, steerIDs)
}

// forget returns the entries it HELD: the unread steers, whose text survives only here once
// the ids-only cleared frame arrives.
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

func (d *baseDeps) Broadcast(ctx context.Context, evt marotte.ServerEvent) {
	if d.onBroadcast != nil {
		d.onBroadcast(ctx, evt)
	}
}

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

func (d *baseDeps) DepartedName(id marotte.ChatID) (string, bool) {
	return d.store.DepartedName(id)
}

// WaitingWorkflowMessages folds what the double filed for the chat, after-close entries first, as
// the store's log would.
func (d *baseDeps) WaitingWorkflowMessages(_ context.Context, chatID marotte.ChatID) ([]marotte.WorkflowMessage, error) {
	return waitingIn(d.between[chatID], d.chatEntries(chatID))
}

func waitingIn(logs ...[]marotte.Entry) ([]marotte.WorkflowMessage, error) {
	var facts []marotte.WorkflowMessageFact
	for _, entries := range logs {
		for i := range entries {
			fact, ok, err := marotte.WorkflowMessageFactOf(&entries[i])
			if err != nil {
				return nil, err
			}
			if ok {
				facts = append(facts, fact)
			}
		}
	}
	fold := marotte.FoldWorkflowMessages(facts)
	return fold.Waiting(), nil
}

// The turnAccess half: one open turn per chat over the recording sink; every fold records its
// target (chat vs run).
func (d *baseDeps) OwnTurn(chatID marotte.ChatID) (*turnlog.Turn, bool) {
	t := d.turns.chats[chatID]
	return t, t != nil && !d.turns.closed(t)
}

func (d *baseDeps) TurnFoldTarget(_ context.Context, chatID marotte.ChatID) *turnlog.Turn {
	d.folds = append(d.folds, foldRecord{chat: chatID})
	return d.turns.chatTurn(chatID)
}

func (d *baseDeps) PromptTurn(chatID marotte.ChatID) (*turnlog.Turn, bool) {
	t := d.prompts[chatID]
	return t, t != nil
}

func (d *baseDeps) StopRequestedAfter(_ marotte.ChatID, turnID string) bool {
	return d.stopped[turnID]
}

func (d *baseDeps) AppendBetweenTurns(_ context.Context, chatID marotte.ChatID, e *marotte.Entry) ([]*marotte.Entry, error) {
	if d.turns.appendErr != nil {
		return nil, d.turns.appendErr
	}
	d.between[chatID] = append(d.between[chatID], *e)
	return nil, nil
}

// The content still coalescing is on the turn's OpenEntries.
func (d *baseDeps) chatEntries(chatID marotte.ChatID) []marotte.Entry {
	return d.turns.entriesOf(d.turns.chats[chatID])
}

func (d *baseDeps) runEntries(runID, nodePath string) []marotte.Entry {
	return d.turns.entriesOf(d.turns.runs[runPathKey(runID, nodePath)])
}

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

// The test fails when the turn is still open.
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

func (d *baseDeps) lastFold() (foldRecord, bool) {
	if len(d.folds) == 0 {
		return foldRecord{}, false
	}
	return d.folds[len(d.folds)-1], true
}

// The RunAppender half: one open turn per run path, recorded call by call.
func (d *baseDeps) RunNodeStart(_ context.Context, step *RunStep, chatID marotte.ChatID) {
	d.runCalls = append(d.runCalls, runCall{kind: "node_start", runID: step.RunID, nodePath: step.NodePath, nodeID: step.NodeID, sessionID: step.SessionID, chat: chatID})
	d.turns.runTurn(step.RunID, step.NodePath)
}

func (d *baseDeps) RunNodeComplete(ctx context.Context, runID string, path []string, status, reason string) {
	nodePath := workflow.PathKey(path)
	d.runCalls = append(d.runCalls, runCall{kind: "node_complete", runID: runID, nodePath: nodePath, status: status, reason: reason})
	if t, ok := d.turns.runs[runPathKey(runID, nodePath)]; ok && !d.turns.closed(t) {
		_, _ = t.Close(ctx, marotte.TurnConclusion{Outcome: marotte.TurnOutcomeCompleted, Reason: reason})
	}
}

func (d *baseDeps) RunFoldTarget(_ context.Context, step *RunStep, chatID marotte.ChatID) (*turnlog.Turn, bool) {
	d.folds = append(d.folds, foldRecord{chat: chatID, runID: step.RunID, nodePath: step.NodePath})
	if t, ok := d.turns.runs[runPathKey(step.RunID, step.NodePath)]; ok && d.turns.closed(t) {
		return nil, false
	}
	d.runCalls = append(d.runCalls, runCall{kind: "fold", runID: step.RunID, nodePath: step.NodePath, nodeID: step.NodeID, sessionID: step.SessionID, chat: chatID})
	return d.turns.runTurn(step.RunID, step.NodePath), true
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
	return ok && !d.turns.closed(t)
}

func (d *baseDeps) RunStopReason(runID, nodePath string, raw marotte.StopReason) bool {
	d.runCalls = append(d.runCalls, runCall{kind: "stop_reason", runID: runID, nodePath: nodePath, stop: raw})
	t, ok := d.turns.runs[runPathKey(runID, nodePath)]
	return ok && !d.turns.closed(t)
}

func (d *baseDeps) RunNodePaused(_ context.Context, runID string, path []string) {
	d.runCalls = append(d.runCalls, runCall{kind: "node_paused", runID: runID, nodePath: workflow.PathKey(path)})
}

// RunSteer files a step steer into the path's open run turn, as the run registry does.
func (d *baseDeps) RunSteer(ctx context.Context, runID, nodePath, steerID string, steer *marotte.EntrySteer) {
	d.runCalls = append(d.runCalls, runCall{kind: "steer", runID: runID, nodePath: nodePath, steerID: steerID})
	if t, ok := d.turns.runs[runPathKey(runID, nodePath)]; ok && !d.turns.closed(t) {
		_, _ = t.Steer(ctx, steerID, steer)
	}
}

func (d *baseDeps) RunSteerDelivered(ctx context.Context, runID, nodePath string, sd *marotte.EntrySteerDelivered) {
	d.runCalls = append(d.runCalls, runCall{kind: "steer_delivered", runID: runID, nodePath: nodePath, steerID: sd.SteerID})
	if t, ok := d.turns.runs[runPathKey(runID, nodePath)]; ok && !d.turns.closed(t) {
		_, _ = t.SteerDelivered(ctx, sd)
	}
}

func (d *baseDeps) RunStepReading(key marotte.ChatID) bool { return d.stepReading[key] }

func (d *baseDeps) RunWaitingWorkflowMessages(_ context.Context, runID string) ([]marotte.WorkflowMessage, error) {
	var logs [][]marotte.Entry
	for _, k := range slices.Sorted(maps.Keys(d.turns.runs)) {
		if strings.HasPrefix(k, runID+"\x00") {
			logs = append(logs, d.runBetween[k], d.turns.entriesOf(d.turns.runs[k]))
		}
	}
	return waitingIn(logs...)
}

func (d *baseDeps) RunPlanUpdate(_ context.Context, _ marotte.ChatID, runID string, u marotte.RunPlanUpdate) {
	d.planUpdates = append(d.planUpdates, u)
	d.runCalls = append(d.runCalls, runCall{kind: "plan_update", runID: runID})
}

// foldRecord is one fold site's target: a chat's own turn when runID is empty,
// else the run path's.
type foldRecord struct {
	chat     marotte.ChatID
	runID    string
	nodePath string
}

type runCall struct {
	kind      string
	runID     string
	nodePath  string
	nodeID    string
	sessionID string
	status    string
	reason    string
	steerID   string
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

func (d *baseDeps) WireTurnEnd(_ context.Context, chatID marotte.ChatID, stop marotte.StopReason) {
	d.brackets = append(d.brackets, turnBracket{chat: chatID, kind: "end", stop: stop})
}

func (d *baseDeps) ReviseTurnBinding(_ context.Context, chatID marotte.ChatID) {
	d.brackets = append(d.brackets, turnBracket{chat: chatID, kind: "revise"})
}

type turnBracket struct {
	chat marotte.ChatID
	kind string
	stop marotte.StopReason
}

// Unlike production, a fold with no turn opens one here directly.
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
	if t, ok := tl.chats[chatID]; ok && !tl.closed(t) {
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

// closed reports whether the turn's turn_close has been sealed.
func (tl *turnLogs) closed(t *turnlog.Turn) bool {
	for _, e := range tl.entriesOf(t) {
		if e.Kind == marotte.EntryKindTurnClose {
			return true
		}
	}
	return false
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
func (d *baseDeps) RunLabel(workflowID string) string { return d.runLabels[workflowID] }

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

type turnInterrupt struct {
	chatID marotte.ChatID
	reason string
}

func (d *baseDeps) InterruptTurn(chatID marotte.ChatID, reason string) {
	d.turnInterrupts = append(d.turnInterrupts, turnInterrupt{chatID, reason})
}

// AccumulateSpend and StageConversationTurnSummary stand in for the host's per-turn
// accounting by writing the chat's usage directly (the host's no-open-turn path).
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

func (*baseDeps) WorkDir() string { return "/tmp" }

type askAnswer struct {
	result    any
	rpcErr    error
	requestID int64
}

// askedOrigin is the bridge an ask arrived on, recording each answer in d.asked.
type askedOrigin struct{ d *baseDeps }

func (o askedOrigin) Respond(_ context.Context, requestID int64, result any, rpcErr error) error {
	o.d.asked = append(o.d.asked, askAnswer{requestID: requestID, result: result, rpcErr: rpcErr})
	return o.d.respondErr
}

func (d *baseDeps) origin() AskOrigin { return askedOrigin{d: d} }

// nopOrigin is the bridge an ask arrived on, where nothing reads the answers.
type nopOrigin struct{}

func (nopOrigin) Respond(context.Context, int64, any, error) error { return nil }
func (*baseDeps) MCPRecorder() mcpRecorder                         { return nopMCPRecorder{} }
func (d *baseDeps) SetGovernance(_ context.Context, g marotte.GovernanceStatePayload) {
	if d.onSetGovernance != nil {
		d.onSetGovernance(g)
	}
}

func (d *baseDeps) SetMCPRegistry(_ context.Context, r *marotte.GovernanceMCPRegistry) {
	if d.onSetRegistry != nil {
		d.onSetRegistry(r)
	}
}

func (d *baseDeps) PolicyChanged(_ context.Context, errs []marotte.PolicyErrorItem, reloaded bool) {
	if d.onPolicyChanged != nil {
		d.onPolicyChanged(errs, reloaded)
	}
}

func (*baseDeps) PendingPermsAdd(_ int64, evt marotte.ServerEvent, _ AskOrigin) marotte.ServerEvent {
	return evt
}

func (d *baseDeps) PendingPermsWithdraw(chatID marotte.ChatID, toolCallID string) bool {
	d.withdrawn = append(d.withdrawn, string(chatID)+"/"+toolCallID)
	return true
}

func (d *baseDeps) NoticeTarget(_ context.Context, chatID marotte.ChatID, runID string) notice.Target {
	if id := notice.AskRun(chatID, runID); id != "" {
		return notice.RunTarget(id, d.runLabels[id])
	}
	return notice.ChatTarget(chatID, "chat "+string(chatID))
}

func (d *baseDeps) Notify(_ context.Context, _ marotte.ChatID, n *marotte.NotificationPayload) {
	d.notices = append(d.notices, *n)
}
func (*baseDeps) IsHookStatusEnabled() bool { return false }

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

	// Prime the buffer so iterations hit the steady-state path.
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
		for range 50 {
			tr.HandleAssistantChunk(ctx, chatID, chunkPayload, false, FrameAttribution{})
		}
		tr.HandleToolCall(ctx, chatID, toolCallPL, FrameAttribution{})
		tr.HandleToolCallUpdate(ctx, chatID, toolUpdatePL, FrameAttribution{})
		for range 50 {
			tr.HandleAssistantChunk(ctx, chatID, chunkPayload, false, FrameAttribution{})
		}
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
