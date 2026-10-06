package translate

// v3 (KAS) session/update sub-kind handlers: context usage, compaction status
// and the model catalog. Shapes verified against the KAS 2.12 acp-server
// bundle.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"reflect"
	"strings"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
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

// sessionInfoKiroBlock is the `kiro` object inside a session_info_update's
// `_meta`, named so it can carry the wire census.
//
// 22+ sub-kinds multiplex through it, dispatched on which sub-BLOCK is present
// rather than on the kind string — so a KAS addition is least likely to be
// noticed here: logUnconsumedInfoKind reports the kind and nothing about the
// payload that arrived with it.
type sessionInfoKiroBlock struct {
	Summarization   *v3Summarization `json:"summarization"`
	UsagePercentage *float64         `json:"usagePercentage"`
	// Workflow marks the frame a workflow STEP's: its metering passes the
	// parent-only gate below, its turn counters do not.
	Workflow     *ACPWorkflowMeta `json:"workflow"`
	ContextUsage struct {
		UsagePercentage *float64 `json:"usagePercentage"`
	} `json:"contextUsage"`
	// Focus is the kind=="focus_update" block (see focus.go).
	Focus *focusUpdate `json:"focus"`
	// Hook is the kind=="hook_update" block, one per hook execution (see hook_status.go).
	Hook *hookUpdateBlock `json:"hook"`
	// TurnStart and TurnEnd are the wire's own turn bracket, emitted for EVERY
	// turn including one marotte never prompted. Pointers because KAS gives
	// turn_end a nested object and turn_start a flat `true`.
	TurnStart *bool         `json:"turnStart"`
	TurnEnd   *turnEndBlock `json:"turnEnd"`
	// UserMessageID is KAS's own record id for the user message it has just
	// persisted, on the `user_message_id_assigned` kind. It arrives ONCE per
	// non-agent-initiated session/prompt, immediately after the append and before
	// the model runs, and it is the only channel carrying that id — the live
	// `user_message_chunk` echo is addressed away from the caller, so a
	// single-client stdio connection never receives it. Measured on kiro-cli 2.21.4.
	UserMessageID string `json:"userMessageId"`
	// The steering sub-kinds' fields, FLAT beside Kind because KAS's
	// legacyFields() returns {} for all three: there is no nested object to key
	// off, so these three must dispatch on the kind STRING (handleSteeringUpdate).
	MessageIDs           []string `json:"messageIds"`
	MessageID            string   `json:"messageId"`
	Content              string   `json:"content"`
	NotificationSeverity string   `json:"notificationSeverity"`
	Kind                 string   `json:"kind"`
	// PromptTurnSummaries is KAS's per-turn metering record, emitted just before
	// the session/prompt response returns (verified on the live 2.12.1 wire).
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
	censusMeta("session_info_update._meta.kiro", data, reflect.TypeFor[sessionInfoKiroShadow]())
	return nil
}

// turnEndBlock is the kind=="turn_end" sub-block.
//
// StopDetails is the only channel on this path carrying an abnormal stop's cause.
// `json.RawMessage` because the field is optional upstream and never observed
// live: a wrong concrete type would fail the whole frame's decode rather than one
// field's. stopDetailsText owns reading it.
type turnEndBlock struct {
	StopReason  string          `json:"stopReason"`
	StopDetails json.RawMessage `json:"stopDetails"`
}

// stopDetailsText reads a human sentence out of turn_end's stopDetails, answering
// "" for anything it does not recognise so the caller falls through to the
// outcome's own default rather than showing a reader a JSON fragment. Two shapes
// are accepted because the field is unmeasured and both are plausible from a
// TypeScript producer.
func stopDetailsText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var obj struct {
		Message string `json:"message"`
		Details string `json:"details"`
		Reason  string `json:"reason"`
	}
	if json.Unmarshal(raw, &obj) != nil {
		return ""
	}
	return strings.TrimSpace(cmp.Or(obj.Message, obj.Details, obj.Reason))
}

// promptTurnSummary is one metering line of a turn-end summary.
type promptTurnSummary struct {
	Unit  string  `json:"unit"`
	Usage float64 `json:"usage"`
}

// meteringUnitCredit is the one unit persistTurnSummary counts as spend. Named
// because the census reports every OTHER unit, so the two must agree by
// construction or counting stops silently.
const meteringUnitCredit = "credit"

// HandleSessionInfoUpdate folds v3 context-usage into the chat's usage and routes
// v3 compaction status. Parent-only, so a subagent update cannot overwrite the
// parent chat — except a workflow step's turn_completion, let through for its
// metering only (see persistTurnSummary).
func (t *Translator) HandleSessionInfoUpdate(ctx context.Context, chatID marotte.ChatID, raw json.RawMessage, attr FrameAttribution) {
	var u sessionInfoUpdate
	if json.Unmarshal(raw, &u) != nil {
		return
	}
	// Before the parent-only gate deliberately: a steer belongs to the chat but is
	// consumed by whichever execution is running, which may be a subagent's.
	if t.handleSteeringUpdate(ctx, chatID, &u) {
		return
	}
	// By session, never by this payload: `_meta.kiro.workflow` never reaches a
	// session_info_update.
	if attr.SubSessionID != "" {
		return
	}
	// A step's frame stops here: its turn_end and turn_completion are its run
	// turn's metering, its credits also the chat's bill, and nothing else of it
	// (its brackets, its hook cards, its focus) reaches the chat's log.
	if attr.Step {
		if credits, ok := t.stepMetering(ctx, attr, &u.Meta.Kiro); ok {
			t.metering.AccumulateSpend(ctx, chatID, credits)
		}
		return
	}
	if h := u.Meta.Kiro.Hook; h != nil {
		t.handleHookUpdate(ctx, chatID, h)
		return
	}
	// After the attribution gate for the same reason the bracket is: a workflow
	// step's answer prompts on the STEP's session, so its own assigned id must not
	// be stamped onto the launching chat's newest prompt row.
	if id := u.Meta.Kiro.UserMessageID; id != "" {
		t.handleUserMessageID(ctx, chatID, id)
		return
	}
	// After the attribution gate deliberately: pre-gate, every workflow step's
	// turn_start would close the launching chat's live turn.
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
	// The ONLY v3 channel that reliably carries the turn's credit spend and
	// duration; usage_update.cost never arrived on the live 2.12.1 wire.
	if len(u.Meta.Kiro.PromptTurnSummaries) > 0 {
		t.persistTurnSummary(ctx, chatID, u.Meta.Kiro.PromptTurnSummaries, u.Meta.Kiro.ElapsedTime)
		return
	}
	t.handleContextUsage(ctx, chatID, &u.Meta.Kiro)
}

// HandleStepInfoUpdate is the parentless run bridge's reading of a step's
// session_info_update: the run turn's metering and nothing else. It never reaches
// a chat header, because a `run:<id>` key names no chat.
func (t *Translator) HandleStepInfoUpdate(ctx context.Context, attr FrameAttribution, raw json.RawMessage) {
	var u sessionInfoUpdate
	if json.Unmarshal(raw, &u) != nil {
		return
	}
	t.stepMetering(ctx, attr, &u.Meta.Kiro)
}

// stepMetering folds a step's turn_end and turn_completion into its run turn's
// aggregate: the stop reason, the credits and the elapsed time. The step's own
// turn_start is a no-op, because a run turn's bracket is node_start and
// node_complete. Reports the credits of a turn_completion the run turn took, so
// a chat-parented caller can bill them to the chat as well.
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

// creditsOf sums a turn_completion's credit dimension, reporting any other unit to
// the census: a dimension KAS added or a rename of this one is invisible to a
// field-name probe, and without the report the spend line stops counting silently.
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

// handleContextUsage is the cascade's last arm: the context-usage channel that
// actually arrives (usageUpdate records why the standalone frame is only a
// fallback), and the report for a frame nothing consumed.
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
	details := stopDetailsText(e.StopDetails)
	if details == "" && len(e.StopDetails) > 0 {
		// The field's shape is unmeasured, so this line is the only thing that would
		// ever tell us it arrived in one stopDetailsText does not read.
		slog.Debug("turn_end carried stopDetails in an unread shape",
			"chat_id", chatID, "stop_reason", e.StopReason, "bytes", len(e.StopDetails))
	}
	t.bracket.WireTurnEnd(ctx, chatID, marotte.StopReason(e.StopReason), details)
	return true
}

// The five `_meta.kiro.kind` values a REPLAY projection switches on. Both projections
// read them and the table below names two, so the strings live here once: the live
// cascade dispatches on which sub-BLOCK is present rather than on the kind, which is
// why this is the only place in the package that spells them.
const (
	infoKindTurnStart      = "turn_start"
	infoKindTurnCompletion = "turn_completion"
	infoKindTurnEnd        = "turn_end"
	infoKindSeparator      = "summarization_separator"
	infoKindSummary        = "summary_message"
)

// knownSessionInfoKinds is every `_meta.kiro.kind` KAS is known to multiplex
// through session_info_update, enumerated from all 30 buildSessionInfoUpdate call
// sites plus the two reaching the wire via SessionInfoEmitter.send.
//
// It tells a sub-kind marotte deliberately ignores from one KAS added since this
// was written; membership implies nothing about consumption.
var knownSessionInfoKinds = map[string]struct{}{
	// turn_start, turn_end, user_message_id_assigned and hook_update are ABSENT because
	// all four are consumed, so one reaching this table means its sub-block did not decode.
	infoKindTurnCompletion: {},
	"context_usage":        {}, infoKindSeparator: {}, infoKindSummary: {},
	"summarization_started": {}, "summarization_failed": {}, "summarization_completed": {},
	"focus_update": {}, "display_error": {},
	"pending_interaction": {}, "interaction_resolved": {}, "recap": {},
	"steering_inclusion": {}, "queued": {}, "repositories_update": {},
}

// logUnconsumedInfoKind reports a session_info_update that reached the end of the
// dispatch cascade without being consumed. Most sub-kinds legitimately do: a kind
// absent from knownSessionInfoKinds logs at Warn as a probable KAS addition, a
// known-but-ignored one at Debug.
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
		// Benign: KAS reports it as a summarization reason and the IDE treats it as a
		// no-op, so no failed-compaction boundary and no error banner.
		slog.Debug("compaction canceled", "chat_id", chatID)
	default:
		// Any other non-empty status is a genuine failure reason.
		t.handleCompactionFailed(ctx, chatID, s.Status)
	}
}

// persistTurnSummary routes a chat turn's metering frame to its three readers:
// the open turn's aggregate (the turn_close footer), the chat's credit bill, and
// the conversation's turn count and duration. The absolute usage_update.cost
// channel keeps overwrite precedence if KAS ever ships both.
func (t *Translator) persistTurnSummary(ctx context.Context, chatID marotte.ChatID, summaries []promptTurnSummary, elapsedMs float64) {
	credits := creditsOf(summaries)
	if turn, ok := t.turns.OwnTurn(chatID); ok {
		turn.Meter(credits, elapsedMs)
	}
	t.metering.AccumulateSpend(ctx, chatID, credits)
	t.metering.StageConversationTurnSummary(ctx, chatID, elapsedMs)
}

// usageUpdate is the v3 usage_update payload: size is the context window in
// tokens, used the tokens consumed, cost the credit spend. cost is a pointer
// because it is nullish upstream, and absent leaves stored credits untouched.
//
// A FALLBACK, not the primary: the frame has no emit site in any KAS build
// marotte has run against (one bundle hit on 2.16.1, no emitter), so the live
// channel is the context_usage session_info_update sub-kind.
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

// persistUsage writes the context percentage, and optionally the context-window
// size and credits, into the chat's usage. size <= 0 leaves the stored size
// unchanged; credits < 0 leaves credits unchanged.
//
// The context-percentage gate is a MATERIAL delta, not any delta: a chat file is
// rewritten wholesale on every Mutate and KAS emits its percentage several times
// per model response, so an exact-inequality gate turned one 20-tool-call turn
// into dozens of full-transcript rewrites.
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
		// Credits keep an exact-inequality gate deliberately: a money value is where
		// rounding a change away would be wrong.
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

// contextPctTiers are the thresholds a crossing must always persist through. 95 is
// the composer's cutoff, so the epsilon must not round that crossing away; 70 and 90
// stay because the ring's fill is continuous now and no percentage is a colour
// boundary, which makes a mid-range crossing the one thing nothing else pins.
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

// configOptionUpdate is the v3 config_option_update payload: the live
// model/mode/effort catalog. On v3 session/new returns an empty model list, so
// this is the channel that populates the model picker.
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

// configChoice is one selectable value in a select-type config option, or a group
// of nested choices when Options is non-empty.
//
// Meta stays `json.RawMessage` so choiceMeta can early-return on an absent block
// rather than decoding an empty object into a zero value it cannot tell apart from
// a decode failure.
type configChoice struct {
	Name        string          `json:"name"`
	Value       string          `json:"value"`
	Description string          `json:"description"`
	Meta        json.RawMessage `json:"_meta"`
	Options     []configChoice  `json:"options"`
}

// HandleConfigOptionUpdate refreshes the model catalog from ANY session's frame and
// the chat's current model and effort from the chat's OWN session only: a workflow
// step's frame arrives under the launching chat's id carrying the STEP's current
// values, and the catalog is a workspace fact where those are session state. Modes
// are intentionally NOT refreshed: this catalog omits the bundled/workspace source
// tag the picker groups by, so the authoritative mode list is session/new's.
func (t *Translator) HandleConfigOptionUpdate(ctx context.Context, chatID marotte.ChatID, raw json.RawMessage, attr FrameAttribution) {
	var p configOptionUpdate
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	cat := readConfigCatalog(p.ConfigOptions)
	if len(cat.models) == 0 && !cat.sawEffort {
		return
	}
	t.catalog.SetModels(cat.models)
	_, err := t.chats.Mutate(ctx, chatID, func(c *marotte.Chat, exists bool) bool {
		if !exists {
			return false
		}
		changed := marotte.ApplyServedModels(c, cat.models)
		if attr.ChatOwned() && cat.applyCurrent(c) {
			changed = true
		}
		return changed
	})
	if errors.Is(err, chat.ErrTombstoned) {
		return
	}
	if err != nil {
		slog.Error("persist v3 config catalog", "chat_id", chatID, "error", err)
	}
}

// configCatalog is what one config_option_update says about the two options
// marotte consumes: the model select and the effortLevel select.
//
// sawEffort is tracked apart from the list because an EMPTY effort list is a real
// answer, reported for a model with no tiers, so it must be applied — while a
// frame carrying no effort option at all must leave the chat's tiers alone.
type configCatalog struct {
	currentModel  string
	currentEffort string
	models        []marotte.SessionModel
	efforts       []marotte.SessionEffortLevel
	sawEffort     bool
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
		}
	}
	return cat
}

// applyCurrent writes the session's current model and effort onto the chat,
// reporting whether anything changed: the store persists and broadcasts only on a
// change, so a repeated frame answers false.
func (cat *configCatalog) applyCurrent(c *marotte.Chat) bool {
	changed := false
	if cat.currentModel != "" && c.Model != cat.currentModel {
		c.Model = cat.currentModel
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

// flattenEffortChoices converts the effortLevel option's choices into the domain
// tier list. Flat by construction, since KAS groups only the model select.
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

// flattenModelChoices converts select choices, flat or grouped, into the UNFILTERED
// domain model catalog: it feeds the entitlement set, so dropping an end-of-life
// entry here would refuse a model the account can still run.
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
			// The field this catalog used to drop. handleConfigTemplate prefers this
			// live catalog over the template's, so an unset multiplier here is the
			// number the picker renders — `1x` for every model.
			RateMultiplier: meta.Kiro.RateMultiplier,
		})
	}
	return out
}

// choiceMeta decodes a model choice's `_meta` block. Absent meta yields the zero
// value, which the client reads as "not plumbed": no effort tiers, and — since
// `rate_multiplier` is `omitempty` — no credit readout rather than a wrong one.
//
// The TIER LIST is deliberately absent from the block — it belongs to the
// `effortLevel` option, not to a model choice.
func choiceMeta(raw json.RawMessage) marotte.ModelChoiceMeta {
	var m marotte.ModelChoiceMeta
	if len(raw) == 0 {
		return m
	}
	_ = json.Unmarshal(raw, &m)
	return m
}
