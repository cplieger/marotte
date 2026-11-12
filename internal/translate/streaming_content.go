package translate

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
)

// HandleAssistantChunk folds a text or reasoning delta into the open entry of its
// lane and announces it: entry_opened for the delta that opens an entry,
// entry_delta for one that extends it, and whatever the fold sealed ahead of it.
func (t *Translator) HandleAssistantChunk(ctx context.Context, chatID marotte.ChatID, raw json.RawMessage, isReasoning bool, attr FrameAttribution) {
	var chunk acpChunkWire
	if json.Unmarshal(raw, &chunk) != nil || chunk.Content.Type != marotte.ContentTypeText || chunk.Content.Text == "" {
		return
	}
	turn, sc, ok := t.foldTarget(ctx, chatID, attr)
	if !ok {
		return
	}
	if chunk.Meta.Kiro.AgentInitiated && !attr.Step {
		t.recordWake(ctx, chatID, turn, chunk.Meta.Kiro.AgentInitiatedReason)
	}
	t.workflowMessageWoke(ctx, chatID, attr, chunk.Meta.Kiro.AgentInitiatedReason)
	turn.NoteFirstOutput(firstOutputFrom(chunk.Meta.Kiro.TTFT))
	lane := chunk.Meta.Kiro.AgentSubtaskID
	sayID := cmp.Or(chunk.Meta.Kiro.ReplayID, chunk.Meta.Kiro.MessageID)

	// A refusal-tagged chunk is the turn's EXPLANATION, never prose, on either stream: the tag is
	// turn-level.
	if t.markRefusal(ctx, sc, turn, &chunk, lane) {
		return
	}

	// Markers are stripped from text only: KAS reads acks from text entries.
	if isReasoning {
		t.foldDelta(ctx, sc, turn, lane, sayID, chunk.Content.Text, true)
	} else {
		t.foldText(ctx, sc, turn, chatID, lane, sayID, attr, chunk.Content.Text)
	}
}

// foldText folds a text delta with its steer ack markers stripped, filing each span of prose and
// each ack in stream order: an ack seals the lane, so prose the stream put after a marker opens
// the say's next segment. The carry is cleared before the chunk's own folds and stored after the
// last, so no seal they trigger releases bytes that belong after it.
func (t *Translator) foldText(
	ctx context.Context,
	sc entryScope,
	turn *turnlog.Turn,
	chatID marotte.ChatID,
	lane, sayID string,
	attr FrameAttribution,
	text string,
) {
	carry, sealed, err := turn.TakeCarry(ctx, lane, sayID)
	t.publishSealed(ctx, sc, sealed)
	if err != nil {
		appendFailed(sc, "text delta", err)
		return
	}
	texts, acks, held := splitSteerAcks(carry, text)
	for i, seg := range texts {
		if seg != "" {
			t.foldDelta(ctx, sc, turn, lane, sayID, seg, false)
		}
		if i < len(acks) {
			t.appendSteerAck(ctx, chatID, lane, attr, acks[i])
		}
	}
	turn.SetSteerCarry(lane, sayID, held)
}

// foldDelta folds one text or reasoning delta into lane's open entry and announces it, with
// whatever the fold sealed ahead of it.
func (t *Translator) foldDelta(ctx context.Context, sc entryScope, turn *turnlog.Turn, lane, sayID, delta string, isReasoning bool) {
	var sealed []turnlog.Sealed
	var err error
	if isReasoning {
		sealed, err = turn.ThinkingDelta(ctx, lane, sayID, delta)
	} else {
		sealed, err = turn.TextDelta(ctx, lane, sayID, delta)
	}
	t.publishSealed(ctx, sc, sealed)
	if err != nil {
		appendFailed(sc, "text delta", err)
		return
	}
	t.publishOpen(ctx, sc, turn, lane, delta)
}

// markRefusal seals lane and latches the turn's refusal when chunk carries _meta.kiro.refusal,
// reporting whether it consumed the frame (text or reasoning alike). It branches on the
// STRUCTURAL marker, never the words (service text a reader can paste). Sealing the chunk's
// own lane keeps the prose before it in its own entry.
func (t *Translator) markRefusal(
	ctx context.Context,
	sc entryScope,
	turn *turnlog.Turn,
	chunk *acpChunkWire,
	lane string,
) bool {
	refusal := refusalInfo(chunk)
	if refusal == nil {
		return false
	}
	sealed, err := turn.SealLane(ctx, lane)
	// The note rides the seal's OWN frame; a failed seal publishes nothing.
	t.publishSealedRefused(ctx, sc, sealed, refusal)
	if err != nil {
		appendFailed(sc, "refusal seal", err)
		return true
	}
	// First-wins: a second tagged chunk cannot relabel the turn.
	turn.SetRefusal(refusal)
	return true
}

// appendSteerAck records a steer whose ack marker just closed: the read, then the agent's
// statement, in the chat's own turn. A delegate's chunk keeps its lane; a step's is lane-less.
func (t *Translator) appendSteerAck(ctx context.Context, chatID marotte.ChatID, lane string, attr FrameAttribution, ack steerAck) {
	if ack.SteerID == "" || ack.Text == "" {
		return
	}
	if attr.Step {
		lane = ""
	}
	// The read before the ack: a reader meets the steer above the agent's statement.
	t.steerReadByAck(ctx, chatID, lane, ack.SteerID)
	turn, ok := t.turns.OwnTurn(chatID)
	if !ok {
		t.appendBetweenTurns(ctx, chatID, marotte.EntryKindSteerAck, marotte.SteerAckID(ack.SteerID),
			marotte.EntrySteerAck{SteerID: ack.SteerID, Text: ack.Text})
		return
	}
	sealed, err := turn.SteerAck(ctx, lane, ack.SteerID, ack.Text)
	t.publishSealed(ctx, chatScope(chatID), sealed)
	if err != nil {
		appendFailed(chatScope(chatID), "steer_ack", err)
	}
}

// foldTarget is the accumulator a content frame folds into and the scope its
// entries are announced under: the run's turn for the step path on a Step frame,
// else the chat's own turn, opened as wire_turn_start when none is open. False
// when the frame has nowhere to go and is dropped.
func (t *Translator) foldTarget(ctx context.Context, chatID marotte.ChatID, attr FrameAttribution) (*turnlog.Turn, entryScope, bool) {
	sc := scopeOf(chatID, attr)
	if attr.Step {
		turn, ok := t.runs.RunFoldTarget(ctx, runStepOf(&attr), chatID)
		if !ok {
			slog.Debug("run log: content frame for a step path with no open turn, dropped",
				"workflow_id", attr.RunID, "node_path", attr.NodePath)
		}
		return turn, sc, ok
	}
	turn := t.turns.TurnFoldTarget(ctx, chatID)
	return turn, sc, turn != nil
}

// setPayload marshals a payload onto an entry the caller files itself.
func setPayload(e *marotte.Entry, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		slog.Error("entry payload marshal", "kind", e.Kind, "error", err)
		return
	}
	e.Payload = raw
}

func refusalInfo(chunk *acpChunkWire) *marotte.RefusalInfo {
	return refusalFrom(chunk.Meta.Kiro.Refusal)
}

// refusalFrom maps KAS's refusal block onto the domain type. The explanation is CARRIED (it is
// the only copy once the chunk's text is withheld) and takes displayText. Every field is optional.
func refusalFrom(r *acpRefusalMeta) *marotte.RefusalInfo {
	if r == nil {
		return nil
	}
	return &marotte.RefusalInfo{
		Category:         r.Category,
		Explanation:      displayText(r.Explanation),
		RecommendedModel: r.RecommendedModel,
	}
}

// HandlePlan appends the agent's plan as a plan entry per distinct state: the wire
// resends the whole entries array on every update, and turnlog drops a frame equal
// to the turn's newest plan.
func (t *Translator) HandlePlan(ctx context.Context, chatID marotte.ChatID, raw json.RawMessage, attr FrameAttribution) {
	var p acpPlanWire
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	p.gate()
	turn, sc, ok := t.foldTarget(ctx, chatID, attr)
	if !ok {
		return
	}
	sealed, err := turn.Plan(ctx, marotte.EntryPlan{Entries: p.Entries})
	t.publishSealed(ctx, sc, sealed)
	if err != nil {
		appendFailed(sc, "plan", err)
	}
}

// HandleModeUpdate persists the agent's new mode and broadcasts mode_changed.
func (t *Translator) HandleModeUpdate(ctx context.Context, chatID marotte.ChatID, raw json.RawMessage) {
	var p acpModeUpdateWire
	if json.Unmarshal(raw, &p) != nil || p.ModeID == "" {
		return
	}
	var (
		changed  bool
		from, to string
	)
	_, err := t.chats.Mutate(ctx, chatID, func(c *marotte.Chat, ex bool) bool {
		if !ex {
			return false
		}
		from = c.CurrentModeID
		changed = c.AdoptMode(p.ModeID)
		to = c.CurrentModeID
		return changed
	})
	if errors.Is(err, chat.ErrTombstoned) {
		return
	}
	if err != nil {
		slog.Error("mode update persist", "chat_id", chatID, "error", err)
	}
	if !changed {
		return
	}
	t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventModeChanged, chatID, marotte.ModeChangedPayload{ModeID: to}))
	// The header write is the idempotence gate: KAS's echo of a reader's switch changes nothing
	// and appends no second entry.
	sw := marotte.EntryModeSwitched{From: from, To: to, Source: marotte.ModeSwitchSourceAgent}
	t.appendLaneless(ctx, chatID, marotte.EntryKindModeSwitched, "", sw,
		func(ctx context.Context, turn *turnlog.Turn) ([]turnlog.Sealed, error) {
			return turn.ModeSwitched(ctx, sw)
		})
}
