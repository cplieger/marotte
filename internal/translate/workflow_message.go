package translate

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
)

// workflowMessage is one send: its words and both ends. `to` is the receiver's steer scope and
// `from` the sender's, zero for KAS's own note, which no known step session sent. toSession is the
// KAS session whose buffer receives it.
type workflowMessage struct {
	id        string
	kasID     string
	text      string
	severity  string
	step      string
	run       string
	path      string
	toSession string
	chat      marotte.ChatID
	origin    marotte.SteerOrigin
	to        steerScope
	from      steerScope
	sentTs    int64
}

// row is the message's entry in scope's log, waiting: a take-up, its wake or its epoch's clear
// settles it. Each row names its peer, which the settlement stamps from the receiver's log alone:
// the step on the chat's row, the chat on the step's.
func (m *workflowMessage) row(scope steerScope) *marotte.EntrySteer {
	e := &marotte.EntrySteer{
		Text: m.text, Origin: m.origin, Severity: m.severity, KASID: m.kasID, ProducedTs: m.sentTs, StepPath: m.path,
	}
	if scope.run == "" {
		e.Step, e.OriginRun = m.step, m.run
	} else {
		e.Chat = m.chat
	}
	return e
}

// HandleWorkflowMessage records a notify's workflow message, a runtime note to the chat included
// (workflowMessageOf decides which notifies are one); any other notify records nothing.
func (t *Translator) HandleWorkflowMessage(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	p, ok := unmarshalParams[kasSessionNotify](msg, "_kiro/session/notify")
	if !ok || p.Message == "" {
		return
	}
	m, ok := t.workflowMessageOf(chatID, &p)
	if !ok {
		return
	}
	received := m.row(m.to)
	received.ToSession = m.toSession
	t.appendSteer(ctx, m.to, m.id, received)
	t.wakes.filed(m.toSession, m.id)
	if m.from.key != "" {
		t.appendSteer(ctx, m.from, m.id, m.row(m.from))
	}
}

// workflowMessageOf resolves both ends. A parent's message names the chat's own session as the
// caller and a known step as the target; a step's names a known step as the caller and the chat's
// session as the target. A step-sender notify to the chat whose caller is no known step session is
// one row in the chat's log, naming the node, with no sender's copy: KAS's own notes about a run (a
// repeat's cap, a network wait, a stopped step) name no caller or a step marotte may not know. A
// run host session is no chat.
func (t *Translator) workflowMessageOf(chatID marotte.ChatID, p *kasSessionNotify) (*workflowMessage, bool) {
	if strings.HasPrefix(string(chatID), marotte.RunSubjectPrefix) {
		return nil, false
	}
	own := t.sessions.ParentACPSession(chatID)
	if own == "" {
		return nil, false
	}
	m := &workflowMessage{
		id: marotte.WorkflowMessageIDPrefix + ids.NewMessageID(), kasID: p.NotifyID, text: p.Message,
		severity: p.Severity, toSession: p.SessionID, chat: chatID, sentTs: time.Now().UnixMilli(),
	}
	var stepSession string
	switch {
	case p.Sender == senderParent && p.CallerSessionID == own:
		m.origin, stepSession = marotte.SteerOriginParent, p.SessionID
	case p.Sender == senderStep && p.SessionID == own:
		m.origin, stepSession = marotte.SteerOriginStep, p.CallerSessionID
	default:
		return nil, false
	}
	ref := t.steps.refFor(stepSession)
	if ref.WorkflowID == "" || ref.NodePath == "" {
		if m.origin != marotte.SteerOriginStep || p.WorkflowID == "" || p.NodeID == "" {
			return nil, false
		}
		m.step, m.run, m.to = p.NodeID, p.WorkflowID, chatSteerScope(chatID)
		return m, true
	}
	step := steerScope{key: marotte.StepSteerKey(stepSession), run: ref.WorkflowID, path: ref.NodePath}
	m.step, m.run, m.path = ref.NodeID, ref.WorkflowID, ref.NodePath
	if m.origin == marotte.SteerOriginParent {
		m.to, m.from = step, chatSteerScope(chatID)
	} else {
		m.to, m.from = chatSteerScope(chatID), step
	}
	return m, true
}

const severitySuccess = "success"

// alwaysBuffered reports a message KAS buffers whatever the receiver is doing, so it never wakes one
// (kiro-cli 2.32.0 deliverSendMessageAdmitted: a step's success has no wake).
func alwaysBuffered(origin marotte.SteerOrigin, severity string) bool {
	return origin == marotte.SteerOriginStep && severity == severitySuccess
}

// workflowMessageWoke settles the waker of a send-message-wake turn at the turn's first such frame
// (settleWaker owns the rule).
func (t *Translator) workflowMessageWoke(ctx context.Context, chatID marotte.ChatID, attr FrameAttribution, reason agentInitiatedReason) {
	if reason == reasonSendMessageWake {
		t.settleWaker(ctx, frameSteerScope(chatID, attr), wakeOpen)
	}
}

// settleWaker settles delivered the message that started sc's session's send-message-wake turn,
// when one row alone can have: KAS starts such a turn for one notify reaching the idle session, so
// the waker is a row filed to it before the turn started, and never a step's success. While several
// such rows are unsettled it waits (wakePending) for take-ups, which prove the others buffered.
func (t *Translator) settleWaker(ctx context.Context, sc steerScope, at wakeState) {
	session := t.bufferSession(sc)
	before, ok := t.wakes.candidates(session, at)
	if !ok {
		return
	}
	epoch, ok := t.epochMessages(ctx, sc, session)
	if !ok {
		t.wakes.resolve(session, wakeDone, nil)
		return
	}
	var wakers []int
	for i := range epoch {
		if _, filedBefore := before[epoch[i].ID]; filedBefore && !alwaysBuffered(epoch[i].Steer.Origin, epoch[i].Steer.Severity) {
			wakers = append(wakers, i)
		}
	}
	state := wakePending
	switch len(wakers) {
	case 0:
		state = wakeDone
	case 1:
		m := &epoch[wakers[0]]
		t.settleMessage(ctx, sc, m, marotte.EntrySteerDelivered{SteerID: m.ID, ReadTs: time.Now().UnixMilli()})
		epoch = slices.Delete(epoch, wakers[0], wakers[0]+1)
		state = wakeDone
	}
	unsettled := make(map[string]struct{}, len(epoch))
	for i := range epoch {
		unsettled[epoch[i].ID] = struct{}{}
	}
	t.wakes.resolve(session, state, unsettled)
}

// wakeState is how far a session's newest turn got in settling its waker. The zero value is done, so
// a session with no recorded turn settles none.
type wakeState uint8

const (
	// wakeDone is a turn whose waker is settled, or one that can settle none.
	wakeDone wakeState = iota
	// wakeOpen is a turn whose first send-message-wake frame has not arrived.
	wakeOpen
	// wakePending is a wake turn with several candidate wakers, waiting for take-ups.
	wakePending
)

// wakeLedger holds, per receiving KAS session, the workflow-message rows filed to it in its current
// epoch, oldest first, and how many preceded its newest turn_start: process memory, so after a
// restart no row filed earlier is paired with a wake.
type wakeLedger struct {
	by map[string]*sessionWakes
	mu sync.Mutex
}

type sessionWakes struct {
	filed  []string
	before int
	state  wakeState
	// rewake is a reader's cancel that cleared a workflow message: KAS may re-deliver it by a
	// send-message-wake with no new notify (rewakeWithInfo), whose waker no row names.
	rewake bool
}

func newWakeLedger() *wakeLedger { return &wakeLedger{by: make(map[string]*sessionWakes)} }

func (w *wakeLedger) entry(session string) *sessionWakes {
	s := w.by[session]
	if s == nil {
		s = &sessionWakes{}
		w.by[session] = s
	}
	return s
}

// forgetIdle drops session's entry once it holds nothing a later call reads.
func (w *wakeLedger) forgetIdle(session string, s *sessionWakes) {
	if len(s.filed) == 0 && s.state == wakeDone && !s.rewake {
		delete(w.by, session)
	}
}

// filed records a row filed to session.
func (w *wakeLedger) filed(session, id string) {
	if session == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.entry(session)
	s.filed = append(s.filed, id)
}

// turnStarted records session's turn_start: the rows filed so far are its candidate wakers, unless a
// reader's cancel just cleared a message KAS may re-deliver in it.
func (w *wakeLedger) turnStarted(session string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	s, ok := w.by[session]
	if !ok {
		return
	}
	s.before, s.state = len(s.filed), wakeOpen
	if s.rewake || s.before == 0 {
		s.state = wakeDone
	}
	s.rewake = false
	w.forgetIdle(session, s)
}

// cleared ends session's epoch: every row filed to it is settled. rewake marks a reader's cancel
// that cleared a workflow message.
func (w *wakeLedger) cleared(session string, rewake bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.entry(session)
	s.filed, s.before, s.state, s.rewake = nil, 0, wakeDone, rewake
	w.forgetIdle(session, s)
}

// candidates answers the rows filed to session before its newest turn started, when that turn is at.
func (w *wakeLedger) candidates(session string, at wakeState) (map[string]struct{}, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	s, ok := w.by[session]
	if !ok || s.state != at {
		return nil, false
	}
	out := make(map[string]struct{}, s.before)
	for _, id := range s.filed[:s.before] {
		out[id] = struct{}{}
	}
	return out, true
}

// resolve moves session's turn to state, keeping only the filed rows still unsettled.
func (w *wakeLedger) resolve(session string, state wakeState, unsettled map[string]struct{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	s, ok := w.by[session]
	if !ok {
		return
	}
	kept, before := s.filed[:0], 0
	for i, id := range s.filed {
		if _, ok := unsettled[id]; ok {
			kept = append(kept, id)
			if i < s.before {
				before++
			}
		}
	}
	s.filed, s.before, s.state = kept, before, state
	w.forgetIdle(session, s)
}

// workflowMessageRead settles a notification steer's take-up against the oldest unsettled workflow
// message of sc's current epoch with these words (KAS drains its buffer in append order; the
// take-up names no sender, so two senders' identical words are told apart by order only). Both rows
// are stamped delivered and the caller appends no steer. kasID is KAS's id for the row it
// persisted, which the replay merge pairs on.
func (t *Translator) workflowMessageRead(ctx context.Context, sc steerScope, kasID, text string) bool {
	epoch, _ := t.epochMessages(ctx, sc, t.bufferSession(sc))
	for i := range epoch {
		if epoch[i].Steer.Text == text {
			t.settleMessage(ctx, sc, &epoch[i], marotte.EntrySteerDelivered{SteerID: epoch[i].ID, KASID: kasID, ReadTs: time.Now().UnixMilli()})
			t.settleWaker(ctx, sc, wakePending)
			return true
		}
	}
	return false
}

// settleClear files a clear of sc's buffer naming kasIDs, then settles cleared every unsettled
// message of its epoch (no frame says which one KAS re-delivers or drops), and reports whether the
// fact landed. The fact goes first and the receiver's fold settles the epoch from it alone, so a
// cut after it loses nothing and a refused fact has no effect. named counts the workflow messages
// among kasIDs: a reader's cancel that cleared one makes the next turn a re-wake no row names.
//
// The epoch stands in for an id: once kirodotdev/Kiro#12012 puts notifyId on the notify frame,
// settling each row whose kas_id the clear names, delivered or dropped by its cause, replaces it.
func (t *Translator) settleClear(ctx context.Context, sc steerScope, cause marotte.SteerClearCause, named int, kasIDs []string) bool {
	session := t.bufferSession(sc)
	epoch, ok := t.epochMessages(ctx, sc, session)
	c := &marotte.EntrySteerCleared{Cause: cause, Session: session, IDs: append([]string{}, kasIDs...)}
	if !t.appendCleared(ctx, sc, c) {
		return false
	}
	t.wakes.cleared(session, cause == marotte.SteerClearCauseCancel && named > 0)
	if !ok {
		return true
	}
	now := time.Now().UnixMilli()
	for i := range epoch {
		d := marotte.EntrySteerDelivered{SteerID: epoch[i].ID, ReadTs: now, Cleared: true}
		// The peer follows whether or not this lands: the receiver's fold reads it from the fact.
		t.appendDelivered(ctx, sc, &d)
		t.mirrorSettlement(ctx, sc, &epoch[i], d)
	}
	return true
}

// bufferSession is the KAS session whose steering buffer sc reads: the frame's, else the chat's.
func (t *Translator) bufferSession(sc steerScope) string {
	if sc.session != "" || sc.run != "" {
		return sc.session
	}
	return t.sessions.ParentACPSession(sc.key)
}

// epochMessages answers, oldest first, the messages sc's log holds as sent to session and unsettled
// (marotte.WorkflowMessageFold.Unsettled), which is session's current epoch: its clears settled the
// rest. None for an empty session, and false when the log is unreadable.
func (t *Translator) epochMessages(ctx context.Context, sc steerScope, session string) ([]marotte.WorkflowMessage, bool) {
	var waiting []marotte.WorkflowMessage
	var err error
	if sc.run != "" {
		waiting, err = t.runs.RunUnsettledWorkflowMessages(ctx, sc.run)
	} else {
		waiting, err = t.chats.UnsettledWorkflowMessages(ctx, sc.key)
	}
	if err != nil {
		slog.Warn("workflow message: the receiver's log is unreadable, so no waiting message is settled",
			"chat_id", sc.key, "run", sc.run, "node_path", sc.path, "error", err)
		return nil, false
	}
	var epoch []marotte.WorkflowMessage
	for i := range waiting {
		if m := &waiting[i]; session != "" && m.Steer.ToSession == session {
			epoch = append(epoch, *m)
		}
	}
	return epoch, true
}

// appendCleared files the clear's fact in sc's log, under an id of its own, and reports whether it landed.
func (t *Translator) appendCleared(ctx context.Context, sc steerScope, c *marotte.EntrySteerCleared) bool {
	ctx = durable.Context(ctx)
	id := marotte.SteerClearedIDPrefix + ids.NewMessageID()
	if sc.run != "" {
		return t.runs.RunSteerCleared(ctx, sc.run, sc.path, id, c)
	}
	return t.appendLaneless(ctx, sc.key, marotte.EntryKindSteerCleared, id, *c,
		func(ctx context.Context, turn *turnlog.Turn) ([]turnlog.Sealed, error) {
			return turn.SteerCleared(ctx, id, c)
		})
}

// settleMessage files d in the receiver's log and, once that landed, in the peer's, so the peer
// never reads a settlement its receiver lacks.
func (t *Translator) settleMessage(ctx context.Context, sc steerScope, m *marotte.WorkflowMessage, d marotte.EntrySteerDelivered) {
	if t.appendDelivered(ctx, sc, &d) {
		t.mirrorSettlement(ctx, sc, m, d)
	}
}

// mirrorSettlement files d, without KAS's id, in the log of the peer m's row names.
func (t *Translator) mirrorSettlement(ctx context.Context, sc steerScope, m *marotte.WorkflowMessage, d marotte.EntrySteerDelivered) {
	d.KASID = ""
	if sc.run != "" {
		if m.Steer.Chat != "" {
			t.appendDelivered(ctx, chatSteerScope(m.Steer.Chat), &d)
		}
		return
	}
	if m.Steer.OriginRun != "" && m.Steer.StepPath != "" {
		t.appendDelivered(ctx, steerScope{run: m.Steer.OriginRun, path: m.Steer.StepPath}, &d)
	}
}

func (t *Translator) appendDelivered(ctx context.Context, sc steerScope, d *marotte.EntrySteerDelivered) bool {
	ctx = durable.Context(ctx)
	if sc.run != "" {
		return t.runs.RunSteerDelivered(ctx, sc.run, sc.path, d)
	}
	return t.appendLaneless(ctx, sc.key, marotte.EntryKindSteerDelivered, marotte.SteerDeliveredID(d.SteerID), *d,
		func(ctx context.Context, turn *turnlog.Turn) ([]turnlog.Sealed, error) {
			return turn.SteerDelivered(ctx, d)
		})
}
