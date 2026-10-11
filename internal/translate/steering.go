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

type steerScope struct {
	key marotte.ChatID
	// run and path are set for a run step, whose log and run tab take the frame instead of a chat's.
	run  string
	path string
}

func chatSteerScope(chatID marotte.ChatID) steerScope { return steerScope{key: chatID} }

func stepSteering(attr FrameAttribution) bool {
	return attr.Step && attr.SessionID != "" && attr.RunID != "" && attr.NodePath != ""
}

// A step session's rows are the step's, never the launching chat's.
func stepSteerScope(attr FrameAttribution) steerScope {
	return steerScope{key: marotte.StepSteerKey(attr.SessionID), run: attr.RunID, path: attr.NodePath}
}

func (sc steerScope) event(p *marotte.SteerQueuedPayload) marotte.ServerEvent {
	q := *p
	if sc.run == "" {
		return marotte.NewEvent(marotte.EventSteerQueued, sc.key, q)
	}
	q.WorkflowID, q.NodePath = sc.run, sc.path
	return marotte.NewEvent(marotte.EventSteerQueued, "", q)
}

// handleSteeringUpdate forwards a steering sub-kind and reports whether it
// consumed the frame, so a steering frame never falls through to the unknown-kind
// tail. A recognised kind whose ids are empty is consumed and dropped: an event
// with no id would put a chip on screen nothing can resolve or clear.
func (t *Translator) handleSteeringUpdate(ctx context.Context, sc steerScope, u *sessionInfoUpdate) bool {
	k := &u.Meta.Kiro
	switch k.Kind {
	case kindSteeringQueued:
		if k.MessageID != "" {
			t.steeringQueued(ctx, sc, k)
		}
		return true
	case kindSteeringInjected:
		if k.MessageID != "" {
			t.steeringInjected(ctx, sc, k)
		}
		return true
	case kindSteeringCleared:
		// KAS clears at EVERY turn boundary, so an empty list is the normal case.
		if len(k.MessageIDs) > 0 {
			t.steeringCleared(ctx, sc, k)
		}
		return true
	}
	return false
}

// A severity (set only when KAS sniffed a `[notification/<sev>]` prefix, which command/steer.go
// refuses) marks an agent's notice, which goes to the ephemeral stack instead of the chip row. A run
// tab has no such stack, so a step's notice waits for its read entry.
func (t *Translator) steeringQueued(ctx context.Context, sc steerScope, k *sessionInfoKiroBlock) {
	if k.NotificationSeverity != "" {
		if sc.run == "" {
			t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventAgentNotice, sc.key, marotte.AgentNoticePayload{
				Severity: k.NotificationSeverity,
				Text:     k.Content,
			}))
		}
		return
	}
	queued := marotte.SteerQueuedPayload{
		SteerID: k.MessageID,
		Text:    k.Content,
		Origin:  t.steerOrigin(sc.key, k.MessageID),
	}
	// RECORDED as well as broadcast: nothing can read KAS's buffer back, so a reconnect replays this.
	queued, ok := t.steerBufferWaiting(sc.key, &queued)
	if ok {
		t.bus.Broadcast(ctx, sc.event(&queued))
	}
}

func (t *Translator) steeringInjected(ctx context.Context, sc steerScope, k *sessionInfoKiroBlock) {
	// The bare text, as KAS persists it, or the merge would see two steers.
	text := k.Content
	if k.NotificationSeverity != "" {
		text = stripNotificationPrefix(text)
		if t.workflowMessageRead(ctx, sc, k.MessageID, text) {
			t.steerBufferRead(sc.key, k.MessageID)
			return
		}
	}
	steer := &marotte.EntrySteer{
		Text: text, Origin: t.steerOrigin(sc.key, k.MessageID), State: marotte.SteerStateRead,
		Severity: k.NotificationSeverity,
	}
	t.stampRunNotice(sc, k.MessageID, steer)
	steer.Resends = t.steerBufferRead(sc.key, k.MessageID)
	t.appendSteer(ctx, sc, k.MessageID, steer)
}

// steeringCleared appends a dropped entry for every AGENT row still held (unread). A user row's
// entry is the host's. An agent note this process never held is recorded TEXT-LESS: the merge
// fills the missing words.
func (t *Translator) steeringCleared(ctx context.Context, sc steerScope, k *sessionInfoKiroBlock) {
	held := make(map[string]marotte.SteerQueuedPayload)
	for _, p := range t.steerBufferCleared(sc.key, k.MessageIDs) {
		held[p.SteerID] = p
	}
	for _, id := range k.MessageIDs {
		if p, ok := held[id]; ok {
			t.appendSteer(ctx, sc, id, &marotte.EntrySteer{
				Text: p.Text, Origin: p.Origin, State: marotte.SteerStateDropped,
				Reason: marotte.SteerReasonBoundary,
			})
			continue
		}
		if t.steerOrigin(sc.key, id) == marotte.SteerOriginUser {
			continue
		}
		// A workflow message's row already stands; the clear settles it.
		if !strings.HasPrefix(id, runNoticeIDPrefix) && t.workflowMessageCleared(ctx, sc) {
			continue
		}
		steer := &marotte.EntrySteer{
			Origin: marotte.SteerOriginAgent, State: marotte.SteerStateDropped,
			Reason: marotte.SteerReasonBoundary,
		}
		t.stampRunNotice(sc, id, steer)
		t.appendSteer(ctx, sc, id, steer)
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
func (t *Translator) stampRunNotice(sc steerScope, steerID string, steer *marotte.EntrySteer) {
	if t.runOrigin == nil || sc.run != "" || !strings.HasPrefix(steerID, runNoticeIDPrefix) {
		return
	}
	if wf, ts, ok := t.runOrigin.RunNotice(sc.key); ok {
		steer.OriginRun, steer.ProducedTs = wf, ts
	}
}

// notificationPrefixRe is KAS's own prefix on a notification-kind buffer entry,
// the shape command/steer.go refuses a user steer for imitating.
var notificationPrefixRe = regexp.MustCompile(`^\s*\[notification/(info|success|warning|error)\]\s*`)

func stripNotificationPrefix(text string) string {
	return notificationPrefixRe.ReplaceAllString(text, "")
}

// appendSteer writes a steer's DURABLE entry, lane-less: into the open turn (sealing every lane), else
// after the newest close. A chat's steer is the chat's whatever execution read it; a step's is its
// run's. The id is KAS's steer id, which the replay stamps too.
func (t *Translator) appendSteer(ctx context.Context, sc steerScope, steerID string, steer *marotte.EntrySteer) {
	if sc.run != "" {
		t.runs.RunSteer(durable.Context(ctx), sc.run, sc.path, steerID, steer)
		return
	}
	t.appendLaneless(durable.Context(ctx), sc.key, marotte.EntryKindSteer, steerID, steer,
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
		Text: p.Text, Origin: p.Origin, State: marotte.SteerStateRead, Resends: p.Replaces,
	}
	t.stampRunNotice(chatSteerScope(chatID), steerID, steer)
	t.appendSteerInLane(ctx, chatID, lane, steerID, steer)
}

// appendSteerInLane is appendSteer for a steer one lane's own acknowledgement
// evidenced: in that lane, so the entry says WHICH agent read it, sealing only it.
func (t *Translator) appendSteerInLane(
	ctx context.Context, chatID marotte.ChatID, lane, steerID string, steer *marotte.EntrySteer,
) {
	if lane == "" {
		t.appendSteer(ctx, chatSteerScope(chatID), steerID, steer)
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

func (t *Translator) steerBufferRead(chatID marotte.ChatID, steerID string) []string {
	if t.steerBuffer == nil {
		return nil
	}
	return t.steerBuffer.SteerRead(chatID, steerID)
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

// THE ID LEADS (KAS mints `steer-<messageID>` from CmdSteer's id; agent rows take `notify-`,
// `wf-progress-` or `steering_boundary_`) because the ledger is a lossy, racing cache. The ledger
// decides other ids.
func (t *Translator) steerOrigin(chatID marotte.ChatID, steerID string) marotte.SteerOrigin {
	if strings.HasPrefix(steerID, marotte.SteerIDPrefix) {
		return marotte.SteerOriginUser
	}
	if t.steers == nil {
		return marotte.SteerOriginAgent
	}
	return t.steers.SteerOrigin(chatID, steerID)
}
