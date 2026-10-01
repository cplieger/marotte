package translate

// Content streaming handlers: text chunks, plans, mode updates.

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
	var chunk ACPChunkWire
	if json.Unmarshal(raw, &chunk) != nil || chunk.Content.Type != marotte.ContentTypeText || chunk.Content.Text == "" {
		return
	}
	// Must run before the fold target is read: a revision re-targets routing to a
	// new wire_turn_start turn, so a target taken first would fold this frame into
	// the prompt's turn.
	if chunk.Meta.Kiro.AgentInitiated && !attr.Step {
		t.bracket.ReviseTurnBinding(ctx, chatID)
	}
	turn, sc, ok := t.foldTarget(ctx, chatID, attr)
	if !ok {
		return
	}
	lane := chunk.Meta.Kiro.AgentSubtaskID
	sayID := cmp.Or(chunk.Meta.Kiro.ReplayID, chunk.Meta.Kiro.MessageID)

	// kiro-cli's security filter cancelled a tool call: this chunk is the whole
	// notice and no session/prompt response is coming. Detected before the steer
	// filter so the two never eat each other's text. Skipped for a step frame,
	// where marotte issued no prompt call to release; the run ceiling catches it.
	interrupted := !isReasoning && !attr.Step && isInterruptSentinel(chunk.Content.Text)

	// A chunk carrying _meta.kiro.refusal is the turn's refusal EXPLANATION rather
	// than assistant prose, so it never reaches the fold below. REASONING chunks
	// included: the tag is a turn-level fact and the frame's method says only which
	// stream it arrived on, so gating the latch on the text stream would drop a
	// refusal KAS tagged on a thought chunk and leave the turn unmarked. `interrupted`
	// is already text-only, so interruptIfFiltered no-ops for a reasoning chunk and
	// the two rules stay independent.
	if t.markRefusal(ctx, sc, turn, chatID, &chunk, lane, interrupted) {
		return
	}

	text := chunk.Content.Text
	if !isReasoning {
		// Text only: KAS's recordSteeringAcks reads the marker from text entries and
		// never reasoning. The carry stays with turnlog, which settles it at the seal.
		var carry string
		var acks []steerAck
		text, carry, acks = stripSteerAcks(turn.Carry(lane), text)
		turn.SetSteerCarry(lane, sayID, carry)
		// BEFORE the empty-text return below: a marker closing a response usually
		// arrives as its own delta, which is exactly the case that returns early.
		t.appendSteerAcks(ctx, chatID, lane, attr, acks)
		if text == "" {
			return
		}
	}

	var sealed []turnlog.Sealed
	var err error
	if isReasoning {
		sealed, err = turn.ThinkingDelta(ctx, lane, sayID, text)
	} else {
		sealed, err = turn.TextDelta(ctx, lane, sayID, text)
	}
	t.publishSealed(ctx, sc, sealed)
	if err != nil {
		appendFailed(sc, "text delta", err)
		return
	}
	t.publishOpen(ctx, sc, turn, lane, text)

	// Last, and the ordering is the contract: the notice must be in the log and on
	// the wire before the turn ends.
	t.interruptIfFiltered(chatID, interrupted)
}

// markRefusal seals lane and latches the turn's refusal when chunk carries
// _meta.kiro.refusal, reporting whether it consumed the frame. It serves BOTH
// streams: a tagged reasoning chunk marks the turn and seals its lane exactly like a
// text one, so the thinking text is dropped with the explanation rather than opening
// a thinking entry the callout then duplicates.
//
// Branch on the STRUCTURAL marker and never on the words: the sentence is the
// SERVICE's, absent from every local kiro-cli binary, and a reader can paste it into
// a prompt, so a matcher would both rot and misfire. Sealing the lane is what keeps
// the preceding prose in its own entry and lets whatever follows open a fresh one — a
// refusal is not always the last thing in a turn. The lane comes off the chunk, so a
// delegate's refusal seals the delegate's lane and not the main one, and the seal
// settles that lane's steer carry like any other.
func (t *Translator) markRefusal(
	ctx context.Context,
	sc entryScope,
	turn *turnlog.Turn,
	chatID marotte.ChatID,
	chunk *ACPChunkWire,
	lane string,
	interrupted bool,
) bool {
	refusal := refusalInfo(chunk)
	if refusal == nil {
		return false
	}
	sealed, err := turn.SealLane(ctx, lane)
	// The note rides the seal's OWN frame, which is the live carrier. A failed seal
	// returns no Sealed at all, so nothing is published and nothing is claimed.
	t.publishSealedRefused(ctx, sc, sealed, refusal)
	if err != nil {
		appendFailed(sc, "refusal seal", err)
		return true
	}
	// First-wins, so KAS's one chunk per turn needs no dedupe here and a second
	// tagged chunk cannot relabel the turn.
	turn.SetRefusal(refusal)
	t.interruptIfFiltered(chatID, interrupted)
	return true
}

// interruptIfFiltered ends the turn when the chunk was kiro-cli's own tool-use
// filter notice. One owner, because both the ordinary fold and the refusal branch
// owe it and a branch that forgot it would leave the prompt call unreleased.
func (t *Translator) interruptIfFiltered(chatID marotte.ChatID, interrupted bool) {
	if !interrupted {
		return
	}
	slog.Warn("kiro-cli interrupted its own tool use; ending the turn",
		"chat_id", chatID, "reason", interruptReason)
	t.turnInterrupt.InterruptTurn(chatID, interruptReason)
}

// appendSteerAcks records each steer whose acknowledgement marker just closed: the
// read the ack evidences, then what the agent said it did about it, as entries in the
// chat's own turn — the ack is the chat's fact whichever session's chunk carried it.
// A delegate's chunk keeps its lane; a step's chunk went to the run's log, so its ack
// is lane-less here.
func (t *Translator) appendSteerAcks(ctx context.Context, chatID marotte.ChatID, lane string, attr FrameAttribution, acks []steerAck) {
	if len(acks) == 0 {
		return
	}
	if attr.Step {
		lane = ""
	}
	for _, ack := range acks {
		if ack.SteerID == "" || ack.Text == "" {
			continue
		}
		// Before the ack and never after: the read is what the ack reports, so a
		// reader meets the steer above the agent's statement about it.
		t.steerReadByAck(ctx, chatID, lane, ack.SteerID)
		turn, ok := t.turns.OwnTurn(chatID)
		if !ok {
			t.appendBetweenTurns(ctx, chatID, marotte.EntryKindSteerAck, marotte.SteerAckID(ack.SteerID),
				marotte.EntrySteerAck{SteerID: ack.SteerID, Text: ack.Text})
			continue
		}
		sealed, err := turn.SteerAck(ctx, lane, ack.SteerID, ack.Text)
		t.publishSealed(ctx, chatScope(chatID), sealed)
		if err != nil {
			appendFailed(chatScope(chatID), "steer_ack", err)
		}
	}
}

// foldTarget is the accumulator a content frame folds into and the scope its
// entries are announced under: the run's turn for the step path on a Step frame,
// else the chat's own turn, opened as wire_turn_start when none is open. False
// when the frame has nowhere to go and is dropped.
func (t *Translator) foldTarget(ctx context.Context, chatID marotte.ChatID, attr FrameAttribution) (*turnlog.Turn, entryScope, bool) {
	sc := scopeOf(chatID, attr)
	if attr.Step {
		turn, ok := t.runs.RunFoldTarget(ctx, attr.RunID, attr.NodePath, attr.SessionID, chatID)
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

// refusalInfo maps a chunk's _meta.kiro.refusal block to the domain shape.
func refusalInfo(chunk *ACPChunkWire) *marotte.RefusalInfo {
	return refusalFrom(chunk.Meta.Kiro.Refusal)
}

// refusalFrom maps KAS's refusal block onto the domain type. The explanation is
// CARRIED rather than dropped: it duplicates the chunk's text, and the chunk's text
// is exactly what no longer reaches the assistant entry, so this block is the only
// copy left. It is service-supplied text on its way to a human-read surface, so it
// takes displayText like every other such string in this package. A block with no
// category and no recommended model still marks the turn (every field optional).
func refusalFrom(r *ACPRefusalMeta) *marotte.RefusalInfo {
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
	var p ACPPlanWire
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
	var p ACPModeUpdateWire
	if json.Unmarshal(raw, &p) != nil || p.ModeID == "" {
		return
	}
	var (
		changed bool
		from    string
	)
	_, err := t.chats.Mutate(ctx, chatID, func(c *marotte.Chat, ex bool) bool {
		if !ex || c.CurrentModeID == p.ModeID {
			return false
		}
		from = c.CurrentModeID
		c.CurrentModeID = p.ModeID
		changed = true
		return true
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
	t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventModeChanged, chatID, marotte.ModeChangedPayload{ModeID: p.ModeID}))
	// The header write above is the idempotence gate for both producers: a switch
	// the reader asked for has already taken it, so KAS's echo of that same switch
	// reaches this point with nothing changed and appends no second entry.
	sw := marotte.EntryModeSwitched{From: from, To: p.ModeID, Source: marotte.ModeSwitchSourceAgent}
	t.appendLaneless(ctx, chatID, marotte.EntryKindModeSwitched, "", sw,
		func(ctx context.Context, turn *turnlog.Turn) ([]turnlog.Sealed, error) {
			return turn.ModeSwitched(ctx, sw)
		})
}
