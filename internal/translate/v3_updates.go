package translate

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"reflect"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
	"github.com/cplieger/runesafe/v2"
)

// v3Summarization is session_info_update's _meta.kiro.summarization block.
// Status is "running", "success" (with the summary), or a failure reason.
type v3Summarization struct {
	Summary *struct {
		ConversationSummary string `json:"conversationSummary"`
	} `json:"summary"`
	Status string `json:"status"`
}

// sessionInfoUpdate is the v3 session_info_update payload. A kind carrying none
// of the sub-blocks sessionInfoKiroBlock names is ignored.
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
	} `json:"contextUsage"`
	// Focus is the kind=="focus_update" block (see focus.go).
	Focus *focusUpdate `json:"focus"`
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
	// RequestIDs, Throughput and Recoveries ride turn_completion FLAT beside Kind.
	RequestIDs []string        `json:"requestIds"`
	Throughput *turnThroughput `json:"throughput"`
	Recoveries []string        `json:"recoveries"`
	// PromptTurnSummaries is KAS's per-turn metering record, emitted just before the
	// session/prompt response returns.
	PromptTurnSummaries []promptTurnSummary `json:"promptTurnSummaries"`
	ElapsedTime         float64             `json:"elapsedTime"`
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
	// The live frame also spreads every event field flat beside its nested block; those of
	// display_error and interaction_resolved are read from the block.
	censusMeta("session_info_update._meta.kiro", data, reflect.TypeFor[sessionInfoKiroShadow](),
		"message", "errorType", "retryErrorType", "toolCallId", "outcome", "selectedOption",
		"interactionType", "question", "options", "agentSubtaskId", "status")
	return nil
}

// turnEndBlock is the kind=="turn_end" sub-block. StopDetails is KAS's refusal object
// ({refusal:{category?, explanation?, recommendedModel?}}), kept raw so an upstream shape
// change fails one field (stopDetailsRefusal) rather than dropping the turn_end bracket.
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
		Refusal *ACPRefusalMeta `json:"refusal"`
	}
	if json.Unmarshal(raw, &d) != nil {
		return nil
	}
	return refusalFrom(d.Refusal)
}

// promptTurnSummary is one metering line of a turn-end summary.
type promptTurnSummary struct {
	Unit  string  `json:"unit"`
	Usage float64 `json:"usage"`
}

// meteringUnitCredit is the one unit persistTurnSummary counts as spend; the census
// reports every OTHER unit, so the two must agree or counting stops silently.
const meteringUnitCredit = "credit"

// HandleSessionInfoUpdate folds v3 context-usage into the chat's usage and routes v3
// compaction status. Parent-only, except a workflow step's turn_completion, let through
// for its metering (persistTurnSummary).
func (t *Translator) HandleSessionInfoUpdate(ctx context.Context, chatID marotte.ChatID, raw json.RawMessage, attr FrameAttribution) {
	var u sessionInfoUpdate
	if json.Unmarshal(raw, &u) != nil {
		return
	}
	// Before the parent-only gate: a steer belongs to the chat but is consumed by whichever
	// execution is running.
	if t.handleSteeringUpdate(ctx, chatID, &u) {
		return
	}
	// Before the gate too: a subagent's or a step's ask is filed under this chat.
	if t.handleAskInfo(chatID, &u.Meta.Kiro, attr) {
		return
	}
	// By session, never by payload: `_meta.kiro.workflow` never reaches a session_info_update.
	if attr.Subagent {
		return
	}
	// A step's frame stops here: only its metering and credits reach the chat.
	if attr.Step {
		t.billStepMetering(ctx, chatID, attr, &u.Meta.Kiro)
		return
	}
	if h := u.Meta.Kiro.Hook; h != nil {
		t.handleHookUpdate(ctx, chatID, h)
		return
	}
	if d := u.Meta.Kiro.DisplayError; d != nil {
		t.handleDisplayError(chatID, d)
		return
	}
	// After the gate: a step's answer prompts on the STEP's session, so its id must not be
	// stamped onto the launching chat's newest prompt row.
	if id := u.Meta.Kiro.UserMessageID; id != "" {
		t.handleUserMessageID(ctx, chatID, id)
		return
	}
	// After the gate: pre-gate, every step's turn_start would close the chat's live turn.
	if u.Meta.Kiro.TurnStart != nil {
		t.bracket.WireTurnStart(ctx, chatID)
		return
	}
	if t.handleWireTurnEnd(ctx, chatID, u.Meta.Kiro.TurnEnd) {
		return
	}
	// Adoption rules for an agent focus title are focus.go's.
	if f := u.Meta.Kiro.Focus; f != nil {
		t.handleFocusUpdate(ctx, chatID, f)
		return
	}
	if s := u.Meta.Kiro.Summarization; s != nil && s.Status != "" {
		t.handleV3Summarization(ctx, chatID, s)
		return
	}
	// The only v3 channel that reliably carries the turn's credit spend and duration;
	// usage_update.cost never arrived on the live 2.12.1 wire.
	if len(u.Meta.Kiro.PromptTurnSummaries) > 0 {
		t.persistTurnSummary(ctx, chatID, &u.Meta.Kiro)
		return
	}
	t.handleContextUsage(ctx, chatID, &u.Meta.Kiro)
}

// HandleStepInfoUpdate is the parentless run bridge's reading of a step's
// session_info_update: the run turn's metering and the withdrawal of an ask filed under
// `run:<id>`. It never reaches a chat header.
func (t *Translator) HandleStepInfoUpdate(ctx context.Context, key marotte.ChatID, raw json.RawMessage, attr FrameAttribution) {
	var u sessionInfoUpdate
	if json.Unmarshal(raw, &u) != nil {
		return
	}
	if r := u.Meta.Kiro.InteractionResolved; r != nil {
		t.handleInteractionResolved(key, r)
		return
	}
	t.stepMetering(ctx, attr, &u.Meta.Kiro)
}

// billStepMetering folds a chat-parented step's metering into its run turn and
// bills the credits it took to the chat as well.
func (t *Translator) billStepMetering(ctx context.Context, chatID marotte.ChatID, attr FrameAttribution, k *sessionInfoKiroBlock) {
	if credits, ok := t.stepMetering(ctx, attr, k); ok {
		t.metering.AccumulateSpend(ctx, chatID, credits)
	}
}

// stepMetering folds a step's turn_end and turn_completion into its run turn's aggregate
// (a run turn's bracket is node_start/node_complete, so turn_start is a no-op). It
// reports the credits taken, for a chat-parented caller to bill.
func (t *Translator) stepMetering(_ context.Context, attr FrameAttribution, k *sessionInfoKiroBlock) (credits float64, ok bool) {
	switch {
	case k.TurnEnd != nil:
		if !t.runs.RunStopReason(attr.RunID, attr.NodePath, marotte.StopReason(k.TurnEnd.StopReason)) {
			slog.Debug("run log: turn_end for a step path with no open turn, dropped",
				"workflow_id", attr.RunID, "node_path", attr.NodePath)
		}
		return 0, false
	case len(k.PromptTurnSummaries) > 0:
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
	t.persistUsage(ctx, chatID, *pct, 0, -1) // no size/credits on this channel
}

// handleWireTurnEnd closes the chat's live turn on the wire's own turn_end
// bracket. Reports whether the frame was a turn_end, so the caller stops.
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
// session_info_update (all 30 buildSessionInfoUpdate sites plus two via
// SessionInfoEmitter.send). It separates a deliberately ignored kind from a new one;
// membership implies nothing about consumption.
var knownSessionInfoKinds = map[string]struct{}{
	// Consumed kinds are ABSENT, so one reaching this table means its sub-block did not decode.
	infoKindTurnCompletion: {},
	"context_usage":        {}, infoKindSeparator: {}, infoKindSummary: {},
	"summarization_started": {}, "summarization_failed": {}, "summarization_completed": {},
	"focus_update": {},
	"recap":        {},
	"queued":       {}, "repositories_update": {},
}

// logUnconsumedInfoKind reports a session_info_update nothing in the cascade consumed: a
// kind absent from knownSessionInfoKinds at Warn (a probable KAS addition), else Debug.
func logUnconsumedInfoKind(chatID marotte.ChatID, kind string) {
	if kind == "" {
		return
	}
	// The kind is backend-controlled, and a raw newline forges a log line.
	safe := runesafe.SanitizeSingleLineBounded(kind, maxCensusNameBytes)
	if _, known := knownSessionInfoKinds[kind]; known {
		slog.Debug("session_info_update: known kind carries nothing marotte consumes",
			"chat_id", chatID, "kind", safe)
		return
	}
	slog.Warn("session_info_update: UNKNOWN kind, dropped — KAS may have added a sub-kind",
		"chat_id", chatID, "kind", safe)
}

// handleV3Summarization maps the v3 summarization sub-states onto the compaction
// domain events.
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

// persistTurnSummary routes a chat turn's metering frame to its three readers: the open
// turn's aggregate, the chat's credit bill, and the conversation's turn count and duration.
// usage_update.cost keeps overwrite precedence if KAS ever ships both.
func (t *Translator) persistTurnSummary(ctx context.Context, chatID marotte.ChatID, k *sessionInfoKiroBlock) {
	credits := creditsOf(k.PromptTurnSummaries)
	elapsedMs := k.ElapsedTime
	if turn, ok := t.turns.OwnTurn(chatID); ok {
		turn.Meter(credits, elapsedMs)
		turn.NoteTurnCompletion(boundedIDs(k.RequestIDs), boundedIDs(k.Recoveries), k.Throughput.summary())
	}
	t.metering.AccumulateSpend(ctx, chatID, credits)
	t.metering.StageConversationTurnSummary(ctx, chatID, elapsedMs)
}

// usageUpdate is the v3 usage_update payload: size is the context window, used the tokens
// consumed, cost the credit spend (nullish upstream; absent leaves credits untouched). A
// FALLBACK: no KAS build run against emits it (2.16.1 has no emitter), so the live channel
// is the context_usage session_info_update sub-kind.
type usageUpdate struct {
	Cost *struct {
		Amount float64 `json:"amount"`
	} `json:"cost"`
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
	pct := float64(u.Used) / float64(u.Size) * 100
	credits := -1.0 // sentinel: leave credits unchanged when cost is absent
	if u.Cost != nil {
		credits = u.Cost.Amount
	}
	t.persistUsage(ctx, chatID, pct, int(u.Size), credits)
}

// persistUsage writes the context percentage, and optionally the window size (size <= 0
// leaves it) and credits (credits < 0 leaves them), into the chat's usage. The percentage
// gate is a MATERIAL delta: each Mutate rewrites the whole chat file and KAS emits the
// percentage several times per response.
func (t *Translator) persistUsage(ctx context.Context, chatID marotte.ChatID, pct float64, size int, credits float64) {
	_, err := t.chats.Mutate(ctx, chatID, func(c *marotte.Chat, exists bool) bool {
		if !exists {
			return false
		}
		changed := false
		if !c.Usage.HasRealData || materialPctDelta(c.Usage.ContextPct, pct) {
			c.Usage.ContextPct = pct
			c.Usage.HasRealData = true
			changed = true
		}
		if size > 0 && c.Usage.ContextSize != size {
			c.Usage.ContextSize = size
			changed = true
		}
		if credits >= 0 && c.Usage.Credits != credits {
			c.Usage.Credits = credits
			changed = true
		}
		// Credits keep an exact gate: rounding a money change away would be wrong.
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

// contextPctTiers are thresholds a crossing must always persist through: 95 is the
// composer's cutoff, and 70 and 90 are the mid-range crossings nothing else pins.
var contextPctTiers = [...]float64{70, 90, 95}

// materialPctDelta reports whether prev → next is worth persisting: at least
// contextPctEpsilon, or any move crossing a tier boundary.
func materialPctDelta(prev, next float64) bool {
	if math.Abs(next-prev) >= contextPctEpsilon {
		return true
	}
	for _, tier := range contextPctTiers {
		if (prev < tier) != (next < tier) {
			return true
		}
	}
	return false
}

// configOptionUpdate is the v3 config_option_update payload: the live model/mode/effort
// catalog. On v3 session/new returns no models, so this populates the model picker.
type configOptionUpdate struct {
	ConfigOptions []configOption `json:"configOptions"`
}

// configOption is one entry in the config_option_update catalog. ID is the
// configId ("model" | "mode" | "effortLevel"); a select's choices may be grouped.
type configOption struct {
	ID           string          `json:"id"`
	Category     string          `json:"category"`
	Type         string          `json:"type"`
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
		if cat.applyCurrent(c) {
			changed = true
		}
		if repin != nil {
			// KAS cleared the level when it repinned; the tier was chosen for the old model.
			c.Effort = ""
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
				return turn.ModelSwitched(ctx, sw)
			})
	}
}

// configCatalog is what one config_option_update says about the model and effortLevel
// selects. sawEffort is separate because an EMPTY effort list (a model with no tiers)
// must be applied, while a frame with no effort option leaves the tiers alone.
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

// readConfigCatalog extracts both options from one frame.
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

// applyCurrent writes the session's current model and effort onto the chat, reporting
// whether anything changed; a repeated frame answers false.
func (cat *configCatalog) applyCurrent(c *marotte.Chat) bool {
	changed := false
	if cat.currentModel != "" && c.Model != cat.currentModel {
		c.Model = cat.currentModel
		changed = true
	}
	if cat.applyThinking(c) {
		changed = true
	}
	if !cat.sawEffort {
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
	return changed
}

// applyThinking records the thinking option's value. KAS sends it only for a toggleable
// model, so a frame naming the current model without it clears the value.
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
		out = append(out, marotte.SessionModel{
			ID: c.Value, Name: c.Name, Description: c.Description,
			HasEffort:          meta.Kiro.HasEffort,
			DefaultEffortLevel: meta.Kiro.DefaultEffortLevel,
			ThinkingToggleable: meta.Kiro.ThinkingToggleable,
			ThinkingDefaultOff: meta.ThinkingDefaultOff(),
			// handleConfigTemplate prefers this live catalog, so an unset multiplier renders `1x`.
			RateMultiplier: meta.Kiro.RateMultiplier,
		})
	}
	return out
}

// choiceMeta decodes a model choice's `_meta` block. Absent meta yields the zero value,
// which the client reads as "not plumbed": no tiers and no credit readout. The tier list
// belongs to the `effortLevel` option, not to a model choice.
func choiceMeta(raw json.RawMessage) marotte.ModelChoiceMeta {
	var m marotte.ModelChoiceMeta
	if len(raw) == 0 {
		return m
	}
	_ = json.Unmarshal(raw, &m)
	return m
}
