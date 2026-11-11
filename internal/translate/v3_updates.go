package translate

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"reflect"
	"time"

	"github.com/cplieger/go/textsafe"
	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
)

// v3Summarization is session_info_update's _meta.kiro.summarization block.
// Status is "running", "success" (with the summary), or a failure reason.
type v3Summarization struct {
	Summary *struct {
		ConversationSummary string `json:"conversationSummary"`
	} `json:"summary"`
	Status string `json:"status"`
}

// A kind carrying none of the sub-blocks sessionInfoKiroBlock names is ignored.
type sessionInfoUpdate struct {
	Meta struct {
		Kiro sessionInfoKiroBlock `json:"kiro"`
	} `json:"_meta"`
}

// sessionInfoKiroBlock is the `kiro` object inside a session_info_update's `_meta`. 22+
// sub-kinds multiplex through it, dispatched on which sub-BLOCK is present, so
// logUnconsumedInfoKind reports a KAS addition by kind only.
type sessionInfoKiroBlock struct {
	Summarization   *v3Summarization `json:"summarization"`
	UsagePercentage *float64         `json:"usagePercentage"`
	// Workflow marks a workflow STEP's frame: its metering passes the parent-only gate, its
	// turn counters do not.
	Workflow     *ACPWorkflowMeta `json:"workflow"`
	ContextUsage struct {
		UsagePercentage *float64 `json:"usagePercentage"`
		// SummarizationThreshold rides only KAS's publication of its stored figure (publishContextUsage:
		// a prompt's end, a context refresh, a session load), never a model response's own reading.
		SummarizationThreshold *float64 `json:"summarizationThreshold"`
	} `json:"contextUsage"`
	// Breakdown marks a context_usage reading's twin, which KAS sends after the plain frame from an unawaited step
	// (handleContextUsageUpdate awaits the steering read), so it can land after the next response's frames. Only its
	// presence is read: its token estimate is coarser than turn_completion's contextBreakdown.
	Breakdown json.RawMessage `json:"breakdown"`
	// Focus is the kind=="focus_update" block (see focus.go).
	Focus *focusUpdate `json:"focus"`
	// Activity rides the engine's own focus_update, FLAT beside Kind (see focus.go).
	Activity *focusActivity `json:"activity"`
	// Hook is the kind=="hook_update" block, one per hook execution (see hook_status.go).
	Hook *hookUpdateBlock `json:"hook"`
	// DisplayError is the kind=="display_error" block (see display_error.go).
	DisplayError *displayErrorBlock `json:"displayError"`
	// InteractionResolved is the kind=="interaction_resolved" block.
	InteractionResolved *interactionResolvedBlock `json:"interactionResolved"`
	// PendingInteraction is the kind=="pending_interaction" block (see turn_facts.go).
	PendingInteraction *pendingInteractionBlock `json:"pendingInteraction"`
	// SteeringDocuments is the kind=="steering_inclusion" list, FLAT beside Kind.
	SteeringDocuments []json.RawMessage `json:"steeringDocuments"`
	// TurnStart and TurnEnd are the wire's turn bracket, emitted for EVERY turn. Pointers
	// because turn_end is a nested object and turn_start a flat `true`.
	TurnStart *bool         `json:"turnStart"`
	TurnEnd   *turnEndBlock `json:"turnEnd"`
	// UserMessageTag rides turn_start: the tag of the prompt the turn runs, absent on an agent-initiated
	// turn. TurnID and ExecutionID are KAS's ids for the turn (Vpe: turnId, else executionId).
	UserMessageTag string `json:"userMessageTag"`
	TurnID         string `json:"turnId"`
	ExecutionID    string `json:"executionId"`
	// UserMessageID is KAS's record id for the user message just persisted
	// (`user_message_id_assigned`), once per non-agent-initiated prompt, before the model
	// runs. The only channel: the `user_message_chunk` echo never reaches a single-client
	// stdio connection (kiro-cli 2.21.4).
	UserMessageID string `json:"userMessageId"`
	// The steering sub-kinds' fields, FLAT beside Kind: legacyFields() returns {} for all
	// three, so they dispatch on the kind STRING (handleSteeringUpdate).
	MessageIDs           []string `json:"messageIds"`
	MessageID            string   `json:"messageId"`
	Content              string   `json:"content"`
	NotificationSeverity string   `json:"notificationSeverity"`
	Kind                 string   `json:"kind"`
	// ActiveSessionID is the session holding KAS's one agent slot, on kind "queued", FLAT beside Kind.
	ActiveSessionID string `json:"activeSessionId"`
	// Message is model_routed's notice, FLAT beside Kind; display_error and interaction frames
	// spread one too, read from their blocks instead.
	Message string `json:"message"`
	modelFallbackFields
	// RequestIDs and Recoveries ride turn_completion FLAT beside Kind.
	RequestIDs []string `json:"requestIds"`
	Recoveries []string `json:"recoveries"`
	// ContextBreakdown rides turn_completion when the prompt asked for it (contextBreakdownFrom).
	ContextBreakdown json.RawMessage `json:"contextBreakdown"`
	// TTFT is kind turn_timing's first-output stamps.
	TTFT *acpTurnTiming `json:"ttft"`
	// PromptTurnSummaries is KAS's per-turn metering record, emitted just before the
	// session/prompt response returns.
	PromptTurnSummaries []promptTurnSummary `json:"promptTurnSummaries"`
	// Credits is session_usage's running total for the session, by unit, FLAT beside Kind: sent
	// after each turn_completion and once, replay-stamped, after a session/load.
	Credits     []promptTurnSummary `json:"credits"`
	ElapsedTime float64             `json:"elapsedTime"`
	Replay      bool                `json:"replay"`
}

// sessionInfoKiroShadow strips the UnmarshalJSON method so the real decode can
// run without recursing into it.
type sessionInfoKiroShadow sessionInfoKiroBlock

// UnmarshalJSON decodes the block and reports any member KAS sent that this type
// does not read. The census can contribute no error.
func (b *sessionInfoKiroBlock) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, (*sessionInfoKiroShadow)(b)); err != nil {
		return err
	}
	// The live frame spreads every event field flat beside its nested block, so most flat members
	// repeat one read from the block: display_error's and interaction_resolved's fields, turn_end's
	// stopReason and stopDetails, focus_update's title, a summarization's conversationSummary and
	// truncated, an interaction's consentRound (read on the permission request). A plan approval's
	// flat `_meta` (its workflowPlan) is declined unread; a live bracket's timestamp and
	// boundarySource add nothing, the boundary being observed at the frame's own time. A
	// context_usage's flat summarizationThreshold repeats the nested one.
	censusMeta("session_info_update._meta.kiro", data, reflect.TypeFor[sessionInfoKiroShadow](),
		"errorType", "retryErrorType", "toolCallId", "outcome", "selectedOption",
		"interactionType", "question", "options", "agentSubtaskId", "status", "_meta",
		"stopReason", "stopDetails", "timestamp", "boundarySource",
		"title", "conversationSummary", "truncated", "summarizationThreshold",
		"consentRound")
	return nil
}

// StopDetails is KAS's refusal object ({refusal:{category?, explanation?, recommendedModel?}}),
// kept raw so an upstream shape change fails one field (stopDetailsRefusal) rather than dropping
// the turn_end bracket.
type turnEndBlock struct {
	StopReason  string          `json:"stopReason"`
	StopDetails json.RawMessage `json:"stopDetails"`
}

// stopDetailsRefusal reads the refusal block out of turn_end's stopDetails, nil for
// an absent field, a non-object, or an object with no refusal.
func stopDetailsRefusal(raw json.RawMessage) *marotte.RefusalInfo {
	if len(raw) == 0 {
		return nil
	}
	var d struct {
		Refusal *acpRefusalMeta `json:"refusal"`
	}
	if json.Unmarshal(raw, &d) != nil {
		return nil
	}
	return refusalFrom(d.Refusal)
}

type promptTurnSummary struct {
	Unit  string  `json:"unit"`
	Usage float64 `json:"usage"`
}

// The census reports every OTHER unit, so the two must agree or counting stops silently.
const meteringUnitCredit = "credit"

// turnStarted marks a turn_start in both wake pairings: the workflow-message waker's and the chat's run completions.
// A row a cancel left for a re-wake the previous turn did not deliver settles cleared: KAS only logs
// a failed re-wake.
func (t *Translator) turnStarted(ctx context.Context, sc steerScope) {
	resume, expired := t.wakes.turnStarted(t.bufferSession(sc))
	if len(expired) > 0 {
		t.settleRows(ctx, sc, expired, marotte.EntrySteerDelivered{ReadTs: time.Now().UnixMilli(), Cleared: true})
	}
	if resume {
		t.settleWaker(ctx, sc, wakeOpen)
	}
	if sc.run == "" {
		t.wakeMem.turnStarted(sc.key)
	}
}

// HandleSessionInfoUpdate folds v3 context-usage into the chat's usage and routes v3
// compaction status. Parent-only, except a workflow step's hooks, steering, model fallback and
// metering, which go to its run turn.
func (t *Translator) HandleSessionInfoUpdate(ctx context.Context, chatID marotte.ChatID, raw json.RawMessage, attr FrameAttribution) {
	var u sessionInfoUpdate
	if json.Unmarshal(raw, &u) != nil {
		return
	}
	// Before the parent-only gate: a chat's steer belongs to the chat but is consumed by whichever
	// execution is running. A step session's buffer is the step's own, so its frames go to its run.
	sc := frameSteerScope(chatID, attr)
	if u.Meta.Kiro.TurnStart != nil {
		t.turnStarted(ctx, sc)
	}
	if t.handleSteeringUpdate(ctx, sc, &u) {
		return
	}
	// Before the step gate: a step's box lands in its run turn.
	if len(u.Meta.Kiro.SteeringDocuments) > 0 {
		t.handleSteeringInclusion(ctx, chatID, attr, &u.Meta.Kiro)
		return
	}
	// Before the step gate: a step's ask is filed under this chat too.
	if t.handleAskInfo(chatID, &u.Meta.Kiro, attr) {
		return
	}
	// A step's frame stops here: its hooks, model fallback and metering go to its run turn, and
	// only its credits reach the chat.
	if attr.Step {
		t.handleStepFrame(ctx, chatID, attr, &u.Meta.Kiro)
		return
	}
	t.handleChatInfo(ctx, chatID, attr.SessionID, &u.Meta.Kiro)
}

// handleChatInfo routes a chat's own frame to the first handler its _meta.kiro shape names, context usage last.
func (t *Translator) handleChatInfo(ctx context.Context, chatID marotte.ChatID, sessionID string, k *sessionInfoKiroBlock) {
	if k.Kind == infoKindTurnTiming {
		if turn, ok := t.turns.OwnTurn(chatID); ok {
			turn.NoteFirstOutput(firstOutputFrom(k.TTFT))
		}
		return
	}
	if h := k.Hook; h != nil {
		t.handleHookUpdate(ctx, chatID, FrameAttribution{}, h, k.subtask())
		return
	}
	if d := k.DisplayError; d != nil {
		t.handleDisplayError(chatID, d)
		return
	}
	// Only past the caller's step gate: a step's answer prompts on the STEP's session, so its id
	// must not be stamped onto the launching chat's newest prompt row.
	if id := k.UserMessageID; id != "" {
		t.handleUserMessageID(ctx, chatID, id)
		return
	}
	if k.Kind == infoKindQueued {
		t.handleSlotQueued(ctx, chatID, k.ActiveSessionID)
		return
	}
	if t.handleTurnEvent(ctx, chatID, k) {
		return
	}
	// Adoption rules for an agent focus title are focus.go's.
	if f := k.Focus; f != nil {
		t.handleFocusUpdate(ctx, chatID, f, k.Activity)
		return
	}
	if s := k.Summarization; s != nil && s.Status != "" {
		t.handleV3Summarization(ctx, chatID, s)
		return
	}
	if k.Credits != nil {
		t.persistSessionCredits(ctx, chatID, sessionID, k)
		return
	}
	// The turn's own spend and duration. Present, not non-empty: a turn that metered nothing
	// sends [] and still carries its request ids and recoveries.
	if k.PromptTurnSummaries != nil {
		t.persistTurnSummary(ctx, chatID, k)
		return
	}
	t.handleContextUsage(ctx, chatID, k)
}

func (t *Translator) handleStepFrame(ctx context.Context, chatID marotte.ChatID, attr FrameAttribution, k *sessionInfoKiroBlock) {
	if h := k.Hook; h != nil {
		t.handleHookUpdate(ctx, chatID, attr, h, k.subtask())
		return
	}
	if !t.stepModelFallback(ctx, chatID, attr, k) {
		t.billStepMetering(ctx, chatID, attr, k)
	}
}

// HandleStepInfoUpdate is the parentless run host session's reading of a step's
// session_info_update: the run turn's hooks, steering, metering and model fallback, the withdrawal of an
// ask filed under `run:<id>`, and the step's own steering frames. It never reaches a chat header.
func (t *Translator) HandleStepInfoUpdate(ctx context.Context, key marotte.ChatID, raw json.RawMessage, attr FrameAttribution) {
	var u sessionInfoUpdate
	if json.Unmarshal(raw, &u) != nil {
		return
	}
	if r := u.Meta.Kiro.InteractionResolved; r != nil {
		t.handleInteractionResolved(key, r)
		return
	}
	if stepSteering(attr) && t.handleSteeringUpdate(ctx, stepSteerScope(attr), &u) {
		return
	}
	if h := u.Meta.Kiro.Hook; h != nil {
		t.handleHookUpdate(ctx, key, attr, h, u.Meta.Kiro.subtask())
		return
	}
	if len(u.Meta.Kiro.SteeringDocuments) > 0 {
		t.handleSteeringInclusion(ctx, key, attr, &u.Meta.Kiro)
		return
	}
	if t.stepModelFallback(ctx, key, attr, &u.Meta.Kiro) {
		return
	}
	t.stepMetering(ctx, attr, &u.Meta.Kiro)
}

func (t *Translator) billStepMetering(ctx context.Context, chatID marotte.ChatID, attr FrameAttribution, k *sessionInfoKiroBlock) {
	if credits, ok := t.stepMetering(ctx, attr, k); ok {
		t.metering.AccumulateSpend(ctx, chatID, credits)
	}
}

// stepMetering folds a step's turn_start, turn_end and turn_completion into its run turn (a
// run turn's bracket is node_start/node_complete, so the turn edges only mark it). It
// reports the credits taken, for a chat-parented caller to bill.
func (t *Translator) stepMetering(_ context.Context, attr FrameAttribution, k *sessionInfoKiroBlock) (credits float64, ok bool) {
	switch {
	case k.TurnStart != nil:
		if !t.runs.RunTurnStart(attr.RunID, attr.NodePath) {
			slog.Debug("run log: turn_start for a step path with no open turn, dropped",
				"workflow_id", attr.RunID, "node_path", attr.NodePath)
		}
		return 0, false
	case k.TurnEnd != nil:
		if !t.runs.RunStopReason(attr.RunID, attr.NodePath, marotte.StopReason(k.TurnEnd.StopReason)) {
			slog.Debug("run log: turn_end for a step path with no open turn, dropped",
				"workflow_id", attr.RunID, "node_path", attr.NodePath)
		}
		return 0, false
	case k.DisplayError != nil:
		if d := k.DisplayError; d.ErrorType != displayErrorMCPConnection &&
			!t.runs.RunStepError(attr.RunID, attr.NodePath, marotte.EngineError{ErrorType: d.ErrorType, RetryErrorType: d.RetryErrorType}) {
			slog.Debug("run log: display_error for a step path with no open turn, dropped",
				"workflow_id", attr.RunID, "node_path", attr.NodePath)
		}
		return 0, false
	case k.Credits != nil:
		// A step's spend is billed per turn_completion; its session total adds nothing.
		return 0, false
	case k.PromptTurnSummaries != nil:
		credits = creditsOf(k.PromptTurnSummaries)
		if !t.runs.RunMeter(attr.RunID, attr.NodePath, credits, k.ElapsedTime) {
			slog.Debug("run log: turn_completion for a step path with no open turn, dropped",
				"workflow_id", attr.RunID, "node_path", attr.NodePath)
			return 0, false
		}
		return credits, true
	}
	return 0, false
}

// creditsOf sums a turn_completion's credit dimension, reporting any other unit to the
// census: a new or renamed dimension would otherwise stop the spend line silently.
func creditsOf(summaries []promptTurnSummary) float64 {
	var credits float64
	for i := range summaries {
		if summaries[i].Unit == "" || summaries[i].Unit == meteringUnitCredit {
			credits += summaries[i].Usage
			continue
		}
		censusMeteringUnit(summaries[i].Unit)
	}
	return credits
}

// handleContextUsage is the cascade's last arm: the context-usage channel that actually
// arrives (see usageUpdate), and the report for a frame nothing consumed.
func (t *Translator) handleContextUsage(ctx context.Context, chatID marotte.ChatID, k *sessionInfoKiroBlock) {
	pct := cmp.Or(k.ContextUsage.UsagePercentage, k.UsagePercentage)
	if pct == nil {
		logUnconsumedInfoKind(chatID, k.Kind)
		return
	}
	live := k.ContextUsage.SummarizationThreshold == nil && !k.Replay && len(k.Breakdown) == 0
	t.persistUsage(ctx, chatID, *pct, 0, live) // no size on this channel
}

// handleTurnEvent takes the wire's turn bracket and the routing notice inside it, reporting whether
// the frame was one. After the parent-only gate: pre-gate, every step's turn_start would close the
// chat's live turn.
func (t *Translator) handleTurnEvent(ctx context.Context, chatID marotte.ChatID, k *sessionInfoKiroBlock) bool {
	switch {
	case k.TurnStart != nil:
		t.endSlotWait(ctx, chatID, true)
		t.bracket.WireTurnStart(ctx, chatID, TurnStartFrame{KASTurnID: cmp.Or(k.TurnID, k.ExecutionID), Tag: k.UserMessageTag})
		return true
	case k.TurnEnd != nil:
		t.endSlotWait(ctx, chatID, false)
		return t.handleWireTurnEnd(ctx, chatID, k.TurnEnd)
	case k.Kind == infoKindModelRouted:
		t.handleModelRouted(ctx, chatID, k)
		return true
	case isModelFallbackKind(k.Kind):
		t.handleModelFallback(ctx, chatID, k)
		return true
	}
	return false
}

// Reports whether the frame was a turn_end, so the caller stops.
func (t *Translator) handleWireTurnEnd(ctx context.Context, chatID marotte.ChatID, e *turnEndBlock) bool {
	if e == nil {
		return false
	}
	refusal := stopDetailsRefusal(e.StopDetails)
	switch {
	case refusal != nil:
		// First-wins: usually a refusal chunk latched it already.
		if turn, ok := t.turns.OwnTurn(chatID); ok {
			turn.SetRefusal(refusal)
		}
	case len(e.StopDetails) > 0:
		// Refusal-only is the measured contract; this line is the only report of upstream drift.
		slog.Debug("turn_end stopDetails carried no refusal block",
			"chat_id", chatID, "stop_reason", e.StopReason, "bytes", len(e.StopDetails))
	}
	t.bracket.WireTurnEnd(ctx, chatID, marotte.StopReason(e.StopReason))
	return true
}

// InfoKindSessionUsage is the session's running credit total. KAS stamps its load-time frame replay,
// yet it is current state, so the agent's replay gate lets it through to the live handler.
const InfoKindSessionUsage = "session_usage"

// The `_meta.kiro.kind` values a REPLAY projection switches on, spelled once here: the
// live cascade dispatches on the sub-block present, not on the kind.
const (
	infoKindTurnStart      = "turn_start"
	infoKindTurnCompletion = "turn_completion"
	infoKindTurnEnd        = "turn_end"
	infoKindSeparator      = "summarization_separator"
	infoKindSummary        = "summary_message"
	infoKindDisplayError   = "display_error"
	// The three per-turn fact kinds (turn_facts.go).
	infoKindPendingInteraction  = "pending_interaction"
	infoKindInteractionResolved = "interaction_resolved"
	infoKindSteeringInclusion   = "steering_inclusion"
)

// knownSessionInfoKinds is every `_meta.kiro.kind` KAS multiplexes through
// session_info_update (its buildSessionInfoUpdate and SessionInfoEmitter.send sites).
// It separates a deliberately ignored kind from a new one;
// membership implies nothing about consumption.
var knownSessionInfoKinds = map[string]struct{}{
	// Consumed kinds are ABSENT, so one reaching this table means its sub-block did not decode.
	infoKindTurnCompletion: {},
	"context_usage":        {}, infoKindSeparator: {}, infoKindSummary: {},
	"summarization_started": {}, "summarization_failed": {}, "summarization_completed": {},
	"focus_update":        {},
	"recap":               {},
	"repositories_update": {},
}

// logUnconsumedInfoKind reports a session_info_update nothing in the cascade consumed: a
// kind absent from knownSessionInfoKinds once per process at Warn (a probable KAS
// addition), a known one at Debug on every frame.
func logUnconsumedInfoKind(chatID marotte.ChatID, kind string) {
	if kind == "" {
		return
	}
	// The kind is backend-controlled, and a raw newline forges a log line.
	safe := textsafe.SanitizeSingleLineBounded(kind, maxCensusNameBytes)
	if _, known := knownSessionInfoKinds[kind]; known {
		slog.Debug("session_info_update: known kind carries nothing marotte consumes",
			"chat_id", chatID, "kind", safe)
		return
	}
	censusInfoKind(chatID, safe)
}

func (t *Translator) handleV3Summarization(ctx context.Context, chatID marotte.ChatID, s *v3Summarization) {
	switch s.Status {
	case "running":
		t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventCompactionStarted, chatID, marotte.CompactionStartedPayload{}))
	case "success":
		var summary *string
		if s.Summary != nil {
			summary = &s.Summary.ConversationSummary
		}
		t.handleCompactionCompleted(ctx, chatID, summary)
	case "canceled", "cancelled":
		// Benign: the IDE treats cancel as a no-op, so no failed boundary and no banner.
		slog.Debug("compaction canceled", "chat_id", chatID)
	default:
		t.handleCompactionFailed(ctx, chatID, s.Status)
	}
}

// persistTurnSummary routes a chat turn's metering frame to the open turn's aggregate and the
// conversation's duration. The chat's credit figure is session_usage's (persistSessionCredits).
func (t *Translator) persistTurnSummary(ctx context.Context, chatID marotte.ChatID, k *sessionInfoKiroBlock) {
	credits := creditsOf(k.PromptTurnSummaries)
	elapsedMs := k.ElapsedTime
	if turn, ok := t.turns.OwnTurn(chatID); ok {
		turn.Meter(credits, elapsedMs)
		turn.NoteTurnCompletion(&turnlog.Completion{
			RequestIDs:       boundedIDs(k.RequestIDs),
			Recoveries:       boundedIDs(k.Recoveries),
			ContextBreakdown: contextBreakdownFrom(k.ContextBreakdown),
		})
	}
	t.metering.StageConversationTurnSummary(ctx, chatID, elapsedMs)
}

// persistSessionCredits overwrites the chat's session total with session_usage's (Chat.SetSessionCredits).
func (t *Translator) persistSessionCredits(ctx context.Context, chatID marotte.ChatID, sessionID string, k *sessionInfoKiroBlock) {
	total := creditsOf(k.Credits)
	_, err := t.chats.Mutate(ctx, chatID, func(c *marotte.Chat, exists bool) bool {
		return exists && c.SetSessionCredits(sessionID, total, k.Replay)
	})
	if errors.Is(err, chat.ErrTombstoned) {
		return
	}
	if err != nil {
		slog.Error("persist session credits", "chat_id", chatID, "error", err)
	}
}

// usageUpdate is the v3 usage_update payload: size is the context window, used the tokens
// consumed. A FALLBACK: no KAS build run against emits it (3.1.0 has no emitter), so the live
// channel is the context_usage session_info_update sub-kind. Its cost is not read: the chat's
// credit figure has one writer (persistSessionCredits).
type usageUpdate struct {
	Size int64 `json:"size"`
	Used int64 `json:"used"`
}

// HandleUsageUpdate folds v3 usage_update into the chat's usage. Parent
// attribution is ignoreSubSession's, in the dispatch table.
func (t *Translator) HandleUsageUpdate(ctx context.Context, chatID marotte.ChatID, raw json.RawMessage) {
	var u usageUpdate
	if json.Unmarshal(raw, &u) != nil || u.Size <= 0 {
		return
	}
	t.persistUsage(ctx, chatID, float64(u.Used)/float64(u.Size)*100, int(u.Size), true)
}

// persistUsage writes the context percentage, and optionally the window size (size <= 0 leaves it),
// into the chat's usage. The percentage gate is a MATERIAL delta: each Mutate rewrites the whole chat
// file and KAS emits the percentage several times per response. Only a live reading (a model
// response's own) reaches the compactor or replaces the figure a compaction cleared: KAS's published
// figure stays the context before a compaction until its next live reading (Usage.ContextCompacted).
func (t *Translator) persistUsage(ctx context.Context, chatID marotte.ChatID, pct float64, size int, live bool) {
	if live {
		t.usage.ObserveContextUsage(ctx, chatID, pct)
	}
	_, err := t.chats.Mutate(ctx, chatID, func(c *marotte.Chat, exists bool) bool {
		if !exists || !live && c.Usage.ContextCompacted {
			return false
		}
		changed := live && c.Usage.ContextCompacted
		c.Usage.ContextCompacted = false
		if !c.Usage.HasRealData || materialPctDelta(c.Usage.ContextPct, pct) {
			c.Usage.ContextPct = pct
			c.Usage.HasRealData = true
			changed = true
		}
		if size > 0 && c.Usage.ContextSize != size {
			c.Usage.ContextSize = size
			changed = true
		}
		return changed
	})
	if errors.Is(err, chat.ErrTombstoned) {
		return
	}
	if err != nil {
		slog.Error("persist v3 usage", "chat_id", chatID, "error", err)
	}
}

// contextPctEpsilon is the smallest context-percentage change worth a full
// transcript rewrite: one point, the resolution the context ring renders.
const contextPctEpsilon = 1.0

// materialPctDelta reports whether prev → next moves at least contextPctEpsilon.
func materialPctDelta(prev, next float64) bool {
	return math.Abs(next-prev) >= contextPctEpsilon
}

// configOptionUpdate is the v3 config_option_update payload: every config option the session
// offers, the model catalog among them. On v3 session/new returns no models, so this populates
// the model picker.
type configOptionUpdate struct {
	ConfigOptions []configOption `json:"configOptions"`
}

// ID is the configId (mode, model, effortLevel, thinking, autopilot, memoryReflection,
// contentCollection, workflowLaunch, outputStyle); a select's choices may be grouped.
type configOption struct {
	ID           string          `json:"id"`
	CurrentValue json.RawMessage `json:"currentValue"`
	Options      []configChoice  `json:"options"`
}

// configChoice is one selectable value in a select-type config option, or a group when
// Options is non-empty. Meta stays raw so choiceMeta can tell an absent block from a
// decode failure.
type configChoice struct {
	Name        string          `json:"name"`
	Value       string          `json:"value"`
	Description string          `json:"description"`
	Meta        json.RawMessage `json:"_meta"`
	Options     []configChoice  `json:"options"`
}

// HandleConfigOptionUpdate refreshes the model catalog from ANY session's frame, but the
// current model and effort from the chat's OWN session only (a step's frame carries the
// STEP's values). Modes are not refreshed: this catalog lacks the source tag the picker
// groups by, so session/new's list is authoritative.
func (t *Translator) HandleConfigOptionUpdate(ctx context.Context, chatID marotte.ChatID, raw json.RawMessage, attr FrameAttribution) {
	var p configOptionUpdate
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	cat := readConfigCatalog(p.ConfigOptions)
	if len(cat.models) == 0 && !cat.sawEffort && !cat.sawThinking {
		return
	}
	// A cold pin's model and effort are the session's saved ones, which the chat already holds; its one-entry
	// lists would replace the catalog, the served set and the tiers.
	if marotte.ColdCatalogPin(cat.currentModel, modelIDs(cat.models)) {
		return
	}
	t.catalog.SetModels(cat.models)
	var repin *marotte.EntryModelSwitched
	_, err := t.chats.Mutate(ctx, chatID, func(c *marotte.Chat, exists bool) bool {
		if !exists {
			return false
		}
		changed := marotte.ApplyServedModels(c, cat.models)
		if !attr.ChatOwned() {
			return changed
		}
		repin = cat.unsolicitedRepin(c)
		if repin != nil {
			// KAS cleared the level when it repinned; the tier was chosen for the old model.
			// applyCurrent's adoption rule decides whether the frame's tier replaces it.
			c.Effort = ""
		}
		if cat.applyCurrent(c) {
			changed = true
		}
		return changed
	})
	if errors.Is(err, chat.ErrTombstoned) {
		return
	}
	if err != nil {
		slog.Error("persist v3 config catalog", "chat_id", chatID, "error", err)
		return
	}
	if repin != nil {
		sw := *repin
		t.appendLaneless(ctx, chatID, marotte.EntryKindModelSwitched, "", sw,
			func(ctx context.Context, turn *turnlog.Turn) ([]turnlog.Sealed, error) {
				return turn.ModelSwitched(ctx, &sw)
			})
	}
}

// configCatalog is what one config_option_update says about the model and effortLevel
// selects. sawEffort is separate because an EMPTY effort list (a model with no tiers)
// must be applied, while a frame naming neither a model nor effort leaves the tiers alone.
type configCatalog struct {
	currentModel    string
	currentEffort   string
	currentThinking string
	models          []marotte.SessionModel
	efforts         []marotte.SessionEffortLevel
	sawEffort       bool
	sawThinking     bool
}

// unsolicitedRepin reports the chat's model moving without a pick of the reader's: the
// frame names another model while none is pending (PendingModel stays set through
// marotte's own SetModel until persisted). An empty or auto model is a first pin. Every
// such move counts as KAS's repin, even while the catalog still lists the old model.
func (cat *configCatalog) unsolicitedRepin(c *marotte.Chat) *marotte.EntryModelSwitched {
	if cat.currentModel == "" || c.Model == "" || c.Model == marotte.ModelAuto ||
		c.Model == cat.currentModel || c.PendingModel != "" {
		return nil
	}
	return &marotte.EntryModelSwitched{From: c.Model, To: cat.currentModel, Reason: marotte.ModelSwitchReasonUnavailable}
}

func readConfigCatalog(opts []configOption) configCatalog {
	var cat configCatalog
	for i := range opts {
		opt := &opts[i]
		switch opt.ID {
		case marotte.ConfigOptionModel:
			_ = json.Unmarshal(opt.CurrentValue, &cat.currentModel) // string; ignore non-string
			cat.models = flattenModelChoices(opt.Options)
		case marotte.ConfigOptionEffort:
			cat.sawEffort = true
			_ = json.Unmarshal(opt.CurrentValue, &cat.currentEffort) // string; ignore non-string
			cat.efforts = flattenEffortChoices(opt.Options)
		case marotte.ConfigOptionThinking:
			cat.sawThinking = true
			_ = json.Unmarshal(opt.CurrentValue, &cat.currentThinking) // string; ignore non-string
		}
	}
	return cat
}

// A repeated frame answers false.
func (cat *configCatalog) applyCurrent(c *marotte.Chat) bool {
	changed := false
	if cat.currentModel != "" && c.Model != cat.currentModel {
		c.MoveToModel(cat.currentModel)
		changed = true
	}
	if cat.applyThinking(c) {
		changed = true
	}
	// KAS builds every frame naming a model with that model's effort option, omitted for a
	// tierless one, so such a frame without it clears the tiers.
	if !cat.sawEffort && cat.currentModel == "" {
		return changed
	}
	if !sameEffortLevels(c.EffortLevels, cat.efforts) {
		c.EffortLevels = cat.efforts
		changed = true
	}
	if c.EffortActive != cat.currentEffort {
		c.EffortActive = cat.currentEffort
		changed = true
	}
	// A recorded session's level becomes the chat's when the record holds none, so every later
	// door re-sends it. A session still opening has no id yet: its door pins the chat's level itself.
	if c.Effort == "" && c.ACPSessionID != "" && marotte.EffortLevel(cat.currentEffort).Valid() {
		c.Effort = cat.currentEffort
		changed = true
	}
	return changed
}

// KAS sends it only for a toggleable model, so a frame naming the current model without it clears
// the value.
func (cat *configCatalog) applyThinking(c *marotte.Chat) bool {
	want := c.ThinkingActive
	switch {
	case cat.sawThinking:
		want = cat.currentThinking
	case cat.currentModel != "":
		want = ""
	}
	if want == c.ThinkingActive {
		return false
	}
	c.ThinkingActive = want
	return true
}

// flattenEffortChoices converts the effortLevel choices into the domain tier list (KAS
// groups only the model select).
func flattenEffortChoices(choices []configChoice) []marotte.SessionEffortLevel {
	out := make([]marotte.SessionEffortLevel, 0, len(choices))
	for i := range choices {
		c := &choices[i]
		if len(c.Options) > 0 {
			out = append(out, flattenEffortChoices(c.Options)...)
			continue
		}
		if c.Value == "" {
			continue
		}
		out = append(out, marotte.SessionEffortLevel{ID: c.Value, Name: c.Name})
	}
	return out
}

// sameEffortLevels reports whether two tier lists carry the same ids in the same
// order — the change-detector applyCurrent's answer depends on.
func sameEffortLevels(a, b []marotte.SessionEffortLevel) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Name != b[i].Name {
			return false
		}
	}
	return true
}

// flattenModelChoices converts select choices, flat or grouped, into the UNFILTERED model
// catalog: it feeds the entitlement set, so dropping an end-of-life entry would refuse a
// model the account can still run.
func flattenModelChoices(choices []configChoice) []marotte.SessionModel {
	var out []marotte.SessionModel
	for i := range choices {
		c := &choices[i]
		if len(c.Options) > 0 { // grouped: recurse into the group's choices
			out = append(out, flattenModelChoices(c.Options)...)
			continue
		}
		if c.Value == "" {
			continue
		}
		meta := choiceMeta(c.Meta)
		out = append(out, meta.SessionModel(c.Value, c.Name, c.Description))
	}
	return out
}

func modelIDs(models []marotte.SessionModel) []string {
	ids := make([]string, len(models))
	for i := range models {
		ids[i] = models[i].ID
	}
	return ids
}

// Absent meta yields the zero value, which the client reads as "not plumbed": no tiers and no
// credit readout. handleConfigTemplate prefers this live catalog, so an unset multiplier renders `1x`.
func choiceMeta(raw json.RawMessage) marotte.ModelChoiceMeta {
	var m marotte.ModelChoiceMeta
	if len(raw) == 0 {
		return m
	}
	_ = json.Unmarshal(raw, &m)
	return m
}
