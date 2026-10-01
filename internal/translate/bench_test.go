package translate

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cplieger/marotte/internal/buffer"
	"github.com/cplieger/marotte/internal/marotte"
)

// baseDeps is a composable Deps implementation for tests and benchmarks.
// By default all methods are no-ops; set the hook fields to override
// specific behaviors (e.g. onBroadcast to capture events).
type baseDeps struct {
	store       ChatRecords
	bufStore    *turnBuffers
	lineTracker *buffer.LineTracker
	onBroadcast func(context.Context, marotte.ServerEvent)
	// onSetGovernance, when set, is invoked by SetGovernance so a test can
	// assert the runtime-side cache write (mirrors onBroadcast).
	onSetGovernance func(marotte.GovernanceStatePayload)
	// scheduledRuns are the workflow ids IsScheduled answers true for, so a
	// test can stage a scheduled run without a runtime or a scheduler.
	scheduledRuns map[string]bool
	// stepCapBreaches records every StepTurnCapExceeded call, so a test can
	// assert the per-step turn cap fired once and named the right step without a
	// agent behind it.
	stepCapBreaches []stepCapBreach
	// runProgress records the workflow id of every RunMadeProgress call, in
	// order, so a test can assert a step's frame refilled its run's idle window
	// without a lease store behind it.
	runProgress []string
	// foldSources records the turn source every fold site stated, in order, so a
	// test can assert a workflow step's frame would open the RUN's turn rather
	// than the chat's.
	foldSources []marotte.TurnOpenSource
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
	// sealRefusals are the chats whose SealTurnSegment declines, standing in for
	// the host refusing to split a turn holding an unsettled tool call.
	sealRefusals map[marotte.ChatID]bool
	// waiting is the steers this double has been told are in KAS's buffer, per
	// chat, standing in for the runtime's tracker. A map rather than a call log
	// because what a test asserts is the SET a reconnect would replay, and the
	// three arms of the cascade reach it as add / remove / remove-each.
	waiting map[marotte.ChatID][]marotte.SteerQueuedPayload
}

// termRendered is one terminal's rendered output in the stub registry.
type termRendered struct {
	text  string
	spans []marotte.TextSpan
}

func newBaseDeps() *baseDeps {
	return &baseDeps{
		store:       nopChatRecords{},
		bufStore:    newTurnBuffers(),
		lineTracker: buffer.NewLineTracker(),
		terminals:   map[string]termRendered{},
		waiting:     map[marotte.ChatID][]marotte.SteerQueuedPayload{},
	}
}

// SteerWaiting / SteerRead / SteerForgotten stand in for the runtime's steering
// buffer tracker, holding what a reconnect would re-offer.
func (d *baseDeps) SteerWaiting(chatID marotte.ChatID, p marotte.SteerQueuedPayload) {
	for i, e := range d.waiting[chatID] {
		if e.SteerID == p.SteerID {
			d.waiting[chatID][i] = p
			return
		}
	}
	d.waiting[chatID] = append(d.waiting[chatID], p)
}

func (d *baseDeps) SteerRead(chatID marotte.ChatID, steerID string) {
	d.SteerForgotten(chatID, []string{steerID})
}

// Returns the entries it HELD, like the real buffer: that subset is the steers
// nothing read, and the only place their text survives once the cleared frame
// (which carries ids alone) arrives.
func (d *baseDeps) SteerForgotten(chatID marotte.ChatID, steerIDs []string) []marotte.SteerQueuedPayload {
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

func (d *baseDeps) AppendMessage(ctx context.Context, chatID marotte.ChatID, msg *marotte.Message) error {
	return d.store.AppendMessage(ctx, chatID, msg)
}

func (d *baseDeps) UpsertTurnPlan(ctx context.Context, chatID marotte.ChatID, msg *marotte.Message) error {
	return d.store.UpsertTurnPlan(ctx, chatID, msg)
}

// TurnFoldTarget records the SOURCE each fold site stated, so a test can assert
// which kind of turn a frame would have opened. The buffer itself is per chat
// here, which is what keeps the fold sites' own behaviour observable without a
// turn registry.
func (d *baseDeps) OpenTurnBuffer(chatID marotte.ChatID) (*buffer.Buffer, bool) {
	buf := d.bufStore.Get(chatID)
	return buf, buf != nil
}

func (d *baseDeps) TurnFoldTarget(_ context.Context, chatID marotte.ChatID, source marotte.TurnOpenSource) *buffer.Buffer {
	d.foldSources = append(d.foldSources, source)
	return d.bufStore.GetOrInit(chatID)
}

// lastFoldSource is the source the most recent fold site stated, and whether any
// fold happened at all.
func (d *baseDeps) lastFoldSource() (marotte.TurnOpenSource, bool) {
	if len(d.foldSources) == 0 {
		return 0, false
	}
	return d.foldSources[len(d.foldSources)-1], true
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

// SealTurnSegment stands in for the host's mid-turn seal: it persists the chat's
// buffered content as an assistant message and clears the buffer, which is the
// PROPERTY this package's caller depends on — the segment reaches the store before
// the event that follows it. The host's own decline conditions are its to test;
// here the double declines exactly when the buffer has nothing to seal, and
// `sealRefusals` stages the in-flight-tool decline as an input.
func (d *baseDeps) SealTurnSegment(ctx context.Context, chatID marotte.ChatID) bool {
	d.brackets = append(d.brackets, turnBracket{chat: chatID, kind: "seal"})
	if d.sealRefusals[chatID] {
		return false
	}
	buf := d.bufStore.Get(chatID)
	if buf == nil {
		return false
	}
	snap, _ := buf.SplitSegment()
	if !snap.Started {
		return false
	}
	msg := marotte.Message{
		ID:        snap.MessageID,
		Role:      marotte.RoleAssistant,
		Content:   snap.Content,
		Reasoning: snap.Reasoning,
		ToolCalls: snap.ToolCalls,
		Blocks:    snap.Blocks,
	}
	_ = d.AppendMessage(ctx, chatID, &msg)
	return true
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

// turnBuffers stands in for the host's per-turn buffers: one buffer per chat,
// created on first fold. It is NOT what production does — there a fold with no
// open turn opens a wireTurnStart turn, and the buffer belongs to that turn's
// record — but the property this package is responsible for is the same either
// way: a fold gets somewhere to land, and the same somewhere for the rest of the
// turn.
type turnBuffers struct {
	bufs map[marotte.ChatID]*buffer.Buffer
}

func newTurnBuffers() *turnBuffers {
	return &turnBuffers{bufs: map[marotte.ChatID]*buffer.Buffer{}}
}

func (tb *turnBuffers) GetOrInit(chatID marotte.ChatID) *buffer.Buffer {
	if b, ok := tb.bufs[chatID]; ok {
		return b
	}
	b := buffer.New()
	tb.bufs[chatID] = b
	return b
}

func (tb *turnBuffers) Get(chatID marotte.ChatID) *buffer.Buffer { return tb.bufs[chatID] }

func (tb *turnBuffers) Delete(chatID marotte.ChatID) { delete(tb.bufs, chatID) }

func (d *baseDeps) RecordFromDiffs(chatID marotte.ChatID, diffs []marotte.ToolDiff, turn int, kind string) {
	d.lineTracker.RecordFromDiffs(chatID, diffs, turn, kind)
}
func (d *baseDeps) ParentACPSession(marotte.ChatID) string { return d.parent }
func (d *baseDeps) IsScheduled(workflowID string) bool {
	return d.scheduledRuns[workflowID]
}

// stepCapBreach is one recorded StepTurnCapExceeded call.
type stepCapBreach struct {
	workflowID string
	nodeID     string
	turns      int
}

func (d *baseDeps) StepTurnCapExceeded(workflowID, nodeID string, turns int) {
	d.stepCapBreaches = append(d.stepCapBreaches, stepCapBreach{workflowID, nodeID, turns})
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
		u.TurnCount++
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
	tr := New(rolesOf(newBaseDeps()), withIDGenerator(func() string { return "stub-msg-id" }))
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
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "stub-msg-id" }))
	ctx := b.Context()
	chatID := marotte.ChatID("bench-chunk")

	chunkPayload := json.RawMessage(`{"content":{"type":"text","text":"Hello world, this is a streaming token. "}}`)

	// Prime the buffer with a first chunk so subsequent iterations hit the
	// steady-state path (no message creation overhead).
	tr.HandleAssistantChunk(ctx, chatID, chunkPayload, false)

	b.ReportAllocs()
	for b.Loop() {
		tr.HandleAssistantChunk(ctx, chatID, chunkPayload, false)
	}
}

// BenchmarkTranslator_FullTurn simulates a complete streaming turn:
// 50 text chunks → 1 tool call → 1 tool call update → 50 more chunks.
// Measures end-to-end throughput including buffer management.
func BenchmarkTranslator_FullTurn(b *testing.B) {
	deps := newBaseDeps()
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "stub-msg-id" }))
	ctx := b.Context()

	chunkPayload := json.RawMessage(`{"content":{"type":"text","text":"Hello world, this is a streaming token. "}}`)
	toolCallPL := toolCallPayload
	toolUpdatePL := json.RawMessage(`{"toolCallId":"tc-1","status":"completed","content":[{"type":"text","content":{"text":"done"}}]}`)

	for b.Loop() {
		chatID := marotte.ChatID("bench-turn")
		// Phase 1: initial streaming chunks
		for range 50 {
			tr.HandleAssistantChunk(ctx, chatID, chunkPayload, false)
		}
		// Phase 2: tool call
		tr.HandleToolCall(ctx, chatID, toolCallPL, FrameAttribution{})
		tr.HandleToolCallUpdate(ctx, chatID, toolUpdatePL, FrameAttribution{})
		// Phase 3: more streaming
		for range 50 {
			tr.HandleAssistantChunk(ctx, chatID, chunkPayload, false)
		}
		// Cleanup: reset buffer for next iteration
		deps.bufStore.Delete(chatID)
	}
}

func BenchmarkTranslator_HandleUsageUpdate(b *testing.B) {
	deps := newBaseDeps()
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "stub-msg-id" }))
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
