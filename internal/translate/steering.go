package translate

// Mid-turn steering, inbound: steering_queued feeds the dock (or an agent notice when a
// severity is set), steering_injected appends the steer as read, steering_cleared appends the
// unread ones as dropped. These carry no sub-block (KAS spreads them flat into _meta.kiro), so
// this is the one place the cascade dispatches on the kind string.

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

// steeringQueued broadcasts the queued steer and records it as waiting. A severity (set only
// when KAS sniffed a `[notification/<sev>]` prefix, which command/steer.go refuses) marks an
// agent's notice, which goes to the ephemeral stack instead of the chip row.
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
	// RECORDED as well as broadcast: nothing can read KAS's buffer back, so a reconnect replays this.
	queued, ok := t.steerBufferWaiting(chatID, &queued)
	if ok {
		t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventSteerQueued, chatID, queued))
	}
}

// steeringInjected takes the read steer off the waiting set and appends its entry.
func (t *Translator) steeringInjected(ctx context.Context, chatID marotte.ChatID, k *sessionInfoKiroBlock) {
	// The bare text, as KAS persists it, or the merge would see two steers.
	text := k.Content
	if k.NotificationSeverity != "" {
		text = stripNotificationPrefix(text)
	}
	steer := &marotte.EntrySteer{
		Text: text, Origin: t.steerOrigin(chatID, k.MessageID), State: marotte.SteerStateRead,
		Severity: k.NotificationSeverity, Resends: t.steerResends(chatID, k.MessageID),
	}
	t.stampRunNotice(chatID, k.MessageID, steer)
	t.steerBufferRead(chatID, k.MessageID)
	t.appendSteer(ctx, chatID, k.MessageID, steer)
}

// steeringCleared appends a dropped entry for every AGENT row still held (unread). A user row's
// entry is the host's. An agent note this process never held is recorded TEXT-LESS: the merge
// fills the missing words.
func (t *Translator) steeringCleared(ctx context.Context, chatID marotte.ChatID, k *sessionInfoKiroBlock) {
	held := make(map[string]marotte.SteerQueuedPayload)
	for _, p := range t.steerBufferCleared(chatID, k.MessageIDs) {
		held[p.SteerID] = p
	}
	for _, id := range k.MessageIDs {
		if p, ok := held[id]; ok {
			t.appendSteer(ctx, chatID, id, &marotte.EntrySteer{
				Text: p.Text, Origin: p.Origin, State: marotte.SteerStateDropped,
				Reason: marotte.SteerReasonBoundary,
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

// appendSteer writes a steer's DURABLE entry, lane-less: into the chat's open turn (sealing
// every lane), else after its newest close. A steer is the chat's whatever session read it.
// The id is KAS's steer id, which the replay stamps too.
func (t *Translator) appendSteer(ctx context.Context, chatID marotte.ChatID, steerID string, steer *marotte.EntrySteer) {
	t.appendLaneless(durable.Context(ctx), chatID, marotte.EntryKindSteer, steerID, steer,
		func(ctx context.Context, turn *turnlog.Turn) ([]turnlog.Sealed, error) {
			return turn.Steer(ctx, steerID, steer)
		})
}

// steerReadByAck records the read a stripped ack marker evidences (a DELEGATE's read gets no
// steering_injected). The buffer's forget answers and removes under one lock, so nothing is
// recorded twice.
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
		// A lane exists only inside a turn: after a close, file lane-less like the ack beside it.
		t.appendBetweenTurns(ctx, chatID, marotte.EntryKindSteer, steerID, steer)
		return
	}
	sealed, err := turn.SteerInLane(ctx, lane, steerID, steer)
	t.publishSealed(ctx, chatScope(chatID), sealed)
	if err != nil {
		appendFailed(chatScope(chatID), string(marotte.EntryKindSteer), err)
	}
}

// Nil-guarded: the role is optional at construction.

func (t *Translator) steerBufferWaiting(chatID marotte.ChatID, p *marotte.SteerQueuedPayload) (marotte.SteerQueuedPayload, bool) {
	if t.steerBuffer == nil {
		out := *p
		out.State = marotte.SteerRowQueued
		return out, true
	}
	return t.steerBuffer.SteerWaiting(chatID, p)
}

func (t *Translator) steerBufferRead(chatID marotte.ChatID, steerID string) {
	if t.steerBuffer != nil {
		t.steerBuffer.SteerRead(chatID, steerID)
	}
}

func (t *Translator) steerBufferCleared(chatID marotte.ChatID, steerIDs []string) []marotte.SteerQueuedPayload {
	if t.steerBuffer == nil {
		return nil
	}
	return t.steerBuffer.SteerCleared(chatID, steerIDs)
}

func (t *Translator) steerBufferForgotten(chatID marotte.ChatID, steerIDs []string) []marotte.SteerQueuedPayload {
	if t.steerBuffer == nil {
		return nil
	}
	return t.steerBuffer.SteerForgotten(chatID, steerIDs)
}

// steerOrigin answers whose words a steer carries. THE ID LEADS (KAS mints `steer-<messageID>`
// from CmdSteer's id; agent rows take `notify-`, `wf-progress-` or `steering_boundary_`)
// because the ledger is a lossy, racing cache. The ledger decides other ids.
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
