package translate

// Mid-turn steering, the inbound half. KAS multiplexes three steering sub-kinds
// through session_info_update: steering_queued feeds the dock (EventSteerQueued, or
// EventAgentNotice when a severity marks an agent's own notice), steering_injected
// appends the steer entry as read, steering_cleared appends the unread ones as
// dropped. These carry no sub-block to key off (KAS spreads the update flat into
// _meta.kiro and legacyFields() returns {} for all three), so this is the one place
// the cascade dispatches on the kind string.

import (
	"context"
	"regexp"
	"strings"

	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
)

// Steering sub-kind names as they appear in `_meta.kiro.kind`.
const (
	kindSteeringQueued   = "steering_queued"
	kindSteeringInjected = "steering_injected"
	kindSteeringCleared  = "steering_cleared"
)

// handleSteeringUpdate forwards a steering sub-kind and reports whether it
// consumed the frame, so a steering frame never falls through to the unknown-kind
// tail. A recognised kind whose ids are empty is consumed and dropped: an event
// with no id would put a chip on screen nothing can resolve or clear.
func (t *Translator) handleSteeringUpdate(ctx context.Context, chatID marotte.ChatID, u *sessionInfoUpdate) bool {
	k := &u.Meta.Kiro
	switch k.Kind {
	case kindSteeringQueued:
		if k.MessageID != "" {
			t.steeringQueued(ctx, chatID, k)
		}
		return true
	case kindSteeringInjected:
		if k.MessageID != "" {
			t.steeringInjected(ctx, chatID, k)
		}
		return true
	case kindSteeringCleared:
		// KAS clears at EVERY turn boundary, so an empty list is the normal case.
		if len(k.MessageIDs) > 0 {
			t.steeringCleared(ctx, chatID, k)
		}
		return true
	}
	return false
}

// steeringQueued broadcasts the queued steer and records it as waiting. KAS
// multiplexes two authors onto this sub-kind and the severity is what separates
// them: it is set only when KAS sniffed a `[notification/<sev>]` prefix, which
// command/steer.go refuses to send, so a severity means a workflow step or a
// subagent reporting into this chat. A notice goes to the ephemeral stack rather
// than the chip row, because nobody is waiting on it.
func (t *Translator) steeringQueued(ctx context.Context, chatID marotte.ChatID, k *sessionInfoKiroBlock) {
	if k.NotificationSeverity != "" {
		t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventAgentNotice, chatID, marotte.AgentNoticePayload{
			Severity: k.NotificationSeverity,
			Text:     k.Content,
		}))
		return
	}
	queued := marotte.SteerQueuedPayload{
		SteerID: k.MessageID,
		Text:    k.Content,
		Origin:  t.steerOrigin(chatID, k.MessageID),
	}
	// RECORDED as well as broadcast: the buffer is KAS's and nothing can read it
	// back, so a client that missed this frame is replayed from the same payload.
	t.steerBufferWaiting(chatID, queued)
	t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventSteerQueued, chatID, queued))
}

// steeringInjected appends the read steer's entry and takes it off the waiting set.
func (t *Translator) steeringInjected(ctx context.Context, chatID marotte.ChatID, k *sessionInfoKiroBlock) {
	origin := t.steerOrigin(chatID, k.MessageID)
	t.steerBufferRead(chatID, k.MessageID)
	// The bare text: KAS's persisted row and therefore the replay carry it without
	// the prefix, and the merge would read the two spellings as two steers.
	text := k.Content
	if k.NotificationSeverity != "" {
		text = stripNotificationPrefix(text)
	}
	steer := &marotte.EntrySteer{
		Text: text, Origin: origin, State: marotte.SteerStateRead, Severity: k.NotificationSeverity,
		Resends: t.steerResends(chatID, k.MessageID),
	}
	t.stampRunNotice(chatID, k.MessageID, steer)
	t.appendSteer(ctx, chatID, k.MessageID, steer)
}

// steeringCleared appends a dropped entry for every steer the buffer still held
// (the ones nothing read; an injected frame removed the others). An agent-origin
// note this process never held is recorded TEXT-LESS, and that entry is itself the
// signal the next session/load's merge reads: the words it lacks are what the merge
// fills, so nothing beside it has to say so.
func (t *Translator) steeringCleared(ctx context.Context, chatID marotte.ChatID, k *sessionInfoKiroBlock) {
	held := make(map[string]marotte.SteerQueuedPayload)
	for _, p := range t.steerBufferForgotten(chatID, k.MessageIDs) {
		held[p.SteerID] = p
	}
	for _, id := range k.MessageIDs {
		if p, ok := held[id]; ok {
			t.appendSteer(ctx, chatID, id, &marotte.EntrySteer{
				Text: p.Text, Origin: p.Origin, State: marotte.SteerStateDropped,
				Reason: marotte.SteerReasonBoundary, Resends: t.steerResends(chatID, id),
			})
			continue
		}
		if t.steerOrigin(chatID, id) == marotte.SteerOriginUser {
			continue
		}
		steer := &marotte.EntrySteer{
			Origin: marotte.SteerOriginAgent, State: marotte.SteerStateDropped,
			Reason: marotte.SteerReasonBoundary,
		}
		t.stampRunNotice(chatID, id, steer)
		t.appendSteer(ctx, chatID, id, steer)
	}
}

// runNoticeIDPrefix is the id shape KAS mints for a finished run's completion notice
// (`notify-wf-<uuid>`; emitWorkflowNotification, parent_busy_queued). The uuid is
// random, so the run it reports is not in the id: RunNotice supplies it.
const runNoticeIDPrefix = "notify-wf-"

// stampRunNotice writes the provenance of a finished run's notice: its run id and
// when the run finished, so a notice read long after its run can say so. Any other
// steer, a step's mid-run send_message included, leaves both absent. Read or dropped,
// each notice consumes its run: the two queues pair in order.
func (t *Translator) stampRunNotice(chatID marotte.ChatID, steerID string, steer *marotte.EntrySteer) {
	if t.runOrigin == nil || !strings.HasPrefix(steerID, runNoticeIDPrefix) {
		return
	}
	if wf, ts, ok := t.runOrigin.RunNotice(chatID); ok {
		steer.OriginRun, steer.ProducedTs = wf, ts
	}
}

// notificationPrefixRe is KAS's own prefix on a notification-kind buffer entry,
// the shape command/steer.go refuses a user steer for imitating.
var notificationPrefixRe = regexp.MustCompile(`^\s*\[notification/(info|success|warning|error)\]\s*`)

// stripNotificationPrefix returns the bare text of an injected agent note.
func stripNotificationPrefix(text string) string {
	return notificationPrefixRe.ReplaceAllString(text, "")
}

// appendSteer writes a steer's DURABLE entry, read or dropped: lane-less, into the
// chat's own turn when one is open (every lane seals first), else after the newest
// turn's close by the between-turns rule. A steer is the chat's fact whatever
// session consumed it, so the routing ignores attribution. The id is KAS's own
// steer id, which the replay stamps too, so the merge pairs the two.
func (t *Translator) appendSteer(ctx context.Context, chatID marotte.ChatID, steerID string, steer *marotte.EntrySteer) {
	t.appendLaneless(durable.Context(ctx), chatID, marotte.EntryKindSteer, steerID, steer,
		func(ctx context.Context, turn *turnlog.Turn) ([]turnlog.Sealed, error) {
			return turn.Steer(ctx, steerID, steer)
		})
}

// steerReadByAck records the read a stripped acknowledgement marker evidences.
// steering_injected reaches this process only for the execution it owns, so a steer a
// DELEGATE consumed arrives as the marker alone, and without this the dock shows it
// waiting until a boundary clear calls it dropped — a steer applied two minutes
// earlier, labelled unread. The buffer's forget both answers and removes under one
// lock, which is what makes "nothing has recorded this id yet" decidable here: an
// injected frame takes the id when it writes its own entry. Taking it is also what
// leaves a later clear with nothing to drop.
func (t *Translator) steerReadByAck(ctx context.Context, chatID marotte.ChatID, lane, steerID string) {
	held := t.steerBufferForgotten(chatID, []string{steerID})
	if len(held) == 0 {
		return
	}
	p := held[0]
	steer := &marotte.EntrySteer{
		Text: p.Text, Origin: p.Origin, State: marotte.SteerStateRead,
		Resends: t.steerResends(chatID, steerID),
	}
	t.stampRunNotice(chatID, steerID, steer)
	t.appendSteerInLane(ctx, chatID, lane, steerID, steer)
}

// appendSteerInLane is appendSteer for a steer one lane's own acknowledgement
// evidenced: in that lane, so the entry says WHICH agent read it, sealing only it.
func (t *Translator) appendSteerInLane(
	ctx context.Context, chatID marotte.ChatID, lane, steerID string, steer *marotte.EntrySteer,
) {
	if lane == "" {
		t.appendSteer(ctx, chatID, steerID, steer)
		return
	}
	ctx = durable.Context(ctx)
	turn, ok := t.turns.OwnTurn(chatID)
	if !ok {
		// A lane exists only inside a turn, so a turn that closed under this frame
		// leaves no lane to record: the entry files lane-less after that close,
		// which is the fallback the ack beside it takes too.
		t.appendBetweenTurns(ctx, chatID, marotte.EntryKindSteer, steerID, steer)
		return
	}
	sealed, err := turn.SteerInLane(ctx, lane, steerID, steer)
	t.publishSealed(ctx, chatScope(chatID), sealed)
	if err != nil {
		appendFailed(chatScope(chatID), string(marotte.EntryKindSteer), err)
	}
}

// The three buffer writes, each nil-guarded for the same reason steerOrigin is:
// the role is optional at construction, and a Translator built without it has to
// translate rather than panic.

func (t *Translator) steerBufferWaiting(chatID marotte.ChatID, p marotte.SteerQueuedPayload) {
	if t.steerBuffer != nil {
		t.steerBuffer.SteerWaiting(chatID, p)
	}
}

func (t *Translator) steerBufferRead(chatID marotte.ChatID, steerID string) {
	if t.steerBuffer != nil {
		t.steerBuffer.SteerRead(chatID, steerID)
	}
}

func (t *Translator) steerBufferForgotten(chatID marotte.ChatID, steerIDs []string) []marotte.SteerQueuedPayload {
	if t.steerBuffer == nil {
		return nil
	}
	return t.steerBuffer.SteerForgotten(chatID, steerIDs)
}

// steerOrigin answers whose words a steer carries. THE ID LEADS because it is
// structural where the ledger is a cache: KAS mints `steer-<messageID>` from the id
// CmdSteer sent, and the agent's own rows take `notify-`, `wf-progress-` or
// `steering_boundary_`; the ledger is TTL'd, bounded, dropped at teardown, lost on
// restart, and its write races KAS's own steering_queued frame (measured: a user's
// correction labelled the agent's and dropped from the boundary resend). The
// ledger still decides for an id marotte did not derive.
func (t *Translator) steerOrigin(chatID marotte.ChatID, steerID string) marotte.SteerOrigin {
	if strings.HasPrefix(steerID, marotte.SteerIDPrefix) {
		return marotte.SteerOriginUser
	}
	if t.steers == nil {
		return marotte.SteerOriginAgent
	}
	return t.steers.SteerOrigin(chatID, steerID)
}

// steerResends answers the dropped steers a steer this server sent re-sends; nil
// for an agent's steer, an unrecorded id, or a Translator built without the role.
func (t *Translator) steerResends(chatID marotte.ChatID, steerID string) []string {
	if t.steers == nil {
		return nil
	}
	return t.steers.SteerResends(chatID, steerID)
}
