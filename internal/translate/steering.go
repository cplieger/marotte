package translate

// Mid-turn steering, inbound: steering_queued feeds the dock (or an agent notice when a
// severity is set), steering_injected appends the steer as read, steering_cleared settles the
// unread ones and records itself with its cause. These carry no sub-block (KAS spreads them flat into _meta.kiro), so
// this is the one place the cascade dispatches on the kind string.

import (
	"context"
	"regexp"
	"strings"
	"sync"

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
	// session is the KAS session whose steering buffer the frame reports, when the frame named one.
	session string
}

func chatSteerScope(chatID marotte.ChatID) steerScope { return steerScope{key: chatID} }

func stepSteering(attr FrameAttribution) bool {
	return attr.Step && attr.SessionID != "" && attr.RunID != "" && attr.NodePath != ""
}

// A step session's rows are the step's, never the launching chat's.
func stepSteerScope(attr FrameAttribution) steerScope {
	return steerScope{key: marotte.StepSteerKey(attr.SessionID), run: attr.RunID, path: attr.NodePath, session: attr.SessionID}
}

// frameSteerScope is the steer scope of a frame's session: a known step's, else the chat's with the
// frame's session.
func frameSteerScope(chatID marotte.ChatID, attr FrameAttribution) steerScope {
	if stepSteering(attr) {
		return stepSteerScope(attr)
	}
	sc := chatSteerScope(chatID)
	sc.session = attr.SessionID
	return sc
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
// tail. A queued or injected frame with no id is consumed and dropped: an event
// with no id would put a chip on screen nothing can resolve or clear. A clear
// naming none still empties the buffer.
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
		t.steeringCleared(ctx, sc, k, t.read.take(sc.key))
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
	// A repeated frame for an id this turn already read would settle a second identical message.
	if !t.read.add(sc.key, k.MessageID) {
		return
	}
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
	t.stampRunNotice(sc, k.MessageID, text, steer)
	steer.Resends = t.steerBufferRead(sc.key, k.MessageID)
	t.appendSteer(ctx, sc, k.MessageID, steer)
}

// steeringCleared settles what a clear took (settleClear owns the workflow messages and files the
// clear, before which nothing else is written and without which nothing is). A held AGENT row gets
// a dropped entry; a user row's entry is the host's; any other agent note (a run notice) is
// recorded TEXT-LESS for the merge to fill. An id `read` names was injected this turn and writes
// nothing beside its read row. A clear inside the reader's cancel drops no agent note: KAS
// re-prompts every unread notification it cleared as its own turn (cancelLocalTurn's
// rewakeClearedNotifications).
func (t *Translator) steeringCleared(ctx context.Context, sc steerScope, k *sessionInfoKiroBlock, read map[string]struct{}) {
	held := make(map[string]marotte.SteerQueuedPayload)
	cleared := t.steerBufferCleared(sc.key, k.MessageIDs)
	for i := range cleared {
		held[cleared[i].SteerID] = cleared[i]
	}
	cause := marotte.SteerClearCauseBoundary
	if t.clearIsCancels(sc) {
		cause = marotte.SteerClearCauseCancel
	}
	c := t.classifyCleared(sc, k.MessageIDs, read, held, cause)
	if !t.settleClear(ctx, sc, cause, c.messages, k.MessageIDs) {
		return
	}
	for _, d := range c.drops {
		t.appendSteer(ctx, sc, d.id, d.steer)
	}
	if c.notices > 0 && t.runOrigin != nil && sc.run == "" {
		t.runOrigin.ForgetRunNotices(sc.key, c.notices)
	}
}

// clearedIDs is a clear's unread ids sorted by what they write: the dropped rows, and the counts of
// run notices and workflow messages.
type clearedIDs struct {
	drops    []clearedDrop
	notices  int
	messages int
}

type clearedDrop struct {
	steer *marotte.EntrySteer
	id    string
}

func (t *Translator) classifyCleared(
	sc steerScope, ids []string, read map[string]struct{}, held map[string]marotte.SteerQueuedPayload, cause marotte.SteerClearCause,
) clearedIDs {
	var c clearedIDs
	boundary := cause == marotte.SteerClearCauseBoundary
	for _, id := range ids {
		if _, ok := read[id]; ok {
			continue
		}
		if strings.HasPrefix(id, runNoticeIDPrefix) {
			c.notices++
		}
		p, isHeld := held[id]
		switch {
		case isHeld:
			if boundary {
				c.drops = append(c.drops, clearedDrop{id: id, steer: &marotte.EntrySteer{
					Text: p.Text, Origin: p.Origin, State: marotte.SteerStateDropped, Reason: marotte.SteerReasonBoundary,
				}})
			}
		case t.steerOrigin(sc.key, id) == marotte.SteerOriginUser:
		case workflowMessageNotifyID(id):
			c.messages++
		case boundary:
			c.drops = append(c.drops, clearedDrop{id: id, steer: &marotte.EntrySteer{
				Origin: marotte.SteerOriginAgent, State: marotte.SteerStateDropped, Reason: marotte.SteerReasonBoundary,
			}})
		}
	}
	return c
}

// workflowMessageNotifyID reports whether a steering id has the shape KAS mints for a
// `_kiro/session/notify` message (`notify-<uuid>`); a run notice's `notify-wf-` is not one.
func workflowMessageNotifyID(id string) bool {
	return strings.HasPrefix(id, notifyIDPrefix) && !strings.HasPrefix(id, runNoticeIDPrefix)
}

const notifyIDPrefix = "notify-"

// clearIsCancels reports whether a chat's clear ends a turn the reader stopped: KAS clears the buffer inside
// session/cancel, before the turn's end. A teardown's cancel is a boundary (marotte.TurnStopTeardown).
func (t *Translator) clearIsCancels(sc steerScope) bool {
	if sc.run != "" {
		return false
	}
	turn, ok := t.turns.OwnTurn(sc.key)
	if !ok {
		turn, ok = t.turns.PromptTurn(sc.key)
	}
	return ok && t.turns.StopCauseAfter(sc.key, turn.ID()) == marotte.TurnStopReader
}

// runNoticeIDPrefix is the id shape KAS mints for a finished run's completion notice
// (`notify-wf-<uuid>`; autoWakeParentOnComplete, parent_busy_queued). The uuid is
// random, so the run it reports is not in the id: RunNotice finds it by the notice's words.
const runNoticeIDPrefix = "notify-wf-"

// stampRunNotice writes the provenance of a finished run's notice read with these words: its run id and
// when the run finished, so a notice read long after its run can say so. Any other steer, a step's
// mid-run send_message included, leaves both absent, as does a notice no recorded run's label matches.
func (t *Translator) stampRunNotice(sc steerScope, steerID, text string, steer *marotte.EntrySteer) {
	if t.runOrigin == nil || sc.run != "" || !strings.HasPrefix(steerID, runNoticeIDPrefix) {
		return
	}
	if wf, ts, ok := t.runOrigin.RunNotice(sc.key, text); ok {
		steer.OriginRun, steer.ProducedTs = wf, ts
	}
}

// notificationPrefixRe is KAS's own prefix on a notification-kind buffer entry,
// the shape command/steer.go refuses a user steer for imitating.
var notificationPrefixRe = regexp.MustCompile(`^\s*\[notification/(info|success|warning|error)\]\s*`)

func stripNotificationPrefix(text string) string {
	return notificationPrefixRe.ReplaceAllString(text, "")
}

// appendSteer writes a steer's DURABLE entry, lane-less: into the open turn (turnlog.Turn.Steer, which
// seals nothing), else after the newest close. A chat's steer is the chat's whatever execution read it; a step's is its
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
	t.read.add(chatID, steerID)
	steer := &marotte.EntrySteer{
		Text: p.Text, Origin: p.Origin, State: marotte.SteerStateRead, Resends: p.Replaces,
	}
	t.stampRunNotice(chatSteerScope(chatID), steerID, p.Text, steer)
	t.appendSteerInLane(ctx, chatID, lane, steerID, steer)
}

// appendSteerInLane is appendSteer for a steer one lane's own acknowledgement evidenced, filed by
// turnlog.Turn.SteerRead.
func (t *Translator) appendSteerInLane(
	ctx context.Context, chatID marotte.ChatID, lane, steerID string, steer *marotte.EntrySteer,
) {
	ctx = durable.Context(ctx)
	turn, ok := t.turns.OwnTurn(chatID)
	if !ok {
		// A lane exists only inside a turn: after a close, file lane-less like the ack beside it.
		t.appendBetweenTurns(ctx, chatID, marotte.EntryKindSteer, steerID, steer)
		return
	}
	sealed, err := turn.SteerRead(ctx, lane, steerID, steer)
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

// readSteers holds, per steer scope, the ids its turn read since the scope's last steering_cleared.
type readSteers struct {
	by map[marotte.ChatID]map[string]struct{}
	mu sync.Mutex
}

func newReadSteers() *readSteers {
	return &readSteers{by: make(map[marotte.ChatID]map[string]struct{})}
}

// add records id as read and reports whether it was new.
func (r *readSteers) add(key marotte.ChatID, id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := r.by[key]
	if ids == nil {
		ids = make(map[string]struct{})
		r.by[key] = ids
	}
	if _, seen := ids[id]; seen {
		return false
	}
	ids[id] = struct{}{}
	return true
}

// take answers the scope's ids and forgets them.
func (r *readSteers) take(key marotte.ChatID) map[string]struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := r.by[key]
	delete(r.by, key)
	return ids
}
