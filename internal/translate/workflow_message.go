package translate

import (
	"cmp"
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
// the step on the chat's row, the chat on the step's, the other chat on a chat-to-chat row's.
func (m *workflowMessage) row(scope steerScope) *marotte.EntrySteer {
	e := &marotte.EntrySteer{
		Text: m.text, Origin: m.origin, Severity: m.severity, KASID: m.kasID, ProducedTs: m.sentTs, StepPath: m.path,
	}
	switch {
	case m.origin == marotte.SteerOriginPeer && scope.key == m.to.key:
		e.Chat = m.from.key
	case m.origin == marotte.SteerOriginPeer:
		e.Chat = m.to.key
	case scope.run == "":
		e.Step, e.OriginRun = m.step, m.run
	default:
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
	if m.origin == marotte.SteerOriginStep {
		t.wakeMem.classify(m.run, cmp.Or(p.NodeID, m.step), m.id, m.path == "")
	}
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
// run host session is no chat. A peer message is peerMessageOf's.
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
	if p.Sender == senderPeer {
		return t.peerMessageOf(m, own, p)
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

// peerMessageOf resolves a chat-to-chat message whose frame reached either end's chat: the
// receiver's row names the sender's chat, and the sender's copy is filed only while the sender's
// session is a resident chat's. A frame reaching the sender whose receiver is no resident chat
// records nothing, since no log takes the received row.
func (t *Translator) peerMessageOf(m *workflowMessage, own string, p *kasSessionNotify) (*workflowMessage, bool) {
	m.origin = marotte.SteerOriginPeer
	switch {
	case p.SessionID == own:
		m.to = chatSteerScope(m.chat)
		if from, ok := t.sessions.ChatForACPSession(p.CallerSessionID); ok && from != m.chat {
			m.from = chatSteerScope(from)
		}
	case p.CallerSessionID == own:
		to, ok := t.sessions.ChatForACPSession(p.SessionID)
		if !ok || to == m.chat {
			return nil, false
		}
		m.to, m.from = chatSteerScope(to), chatSteerScope(m.chat)
	default:
		return nil, false
	}
	return m, true
}

const severitySuccess = "success"

// alwaysBuffered reports a message KAS buffers whatever the receiver is doing, so it never wakes one
// (kiro-cli 2.32.0 deliverSendMessageAdmitted: a step's success has no wake).
func alwaysBuffered(origin marotte.SteerOrigin, severity string) bool {
	return origin == marotte.SteerOriginStep && severity == severitySuccess
}

// workflowMessageWoke settles, at the turn's first agent-initiated frame, the rows a reader's cancel
// left for this re-wake turn, delivered, then the waker of a send-message-wake turn (settleWaker
// owns the rule). KAS's re-wake after a cancel is a notification-rewake, or a send-message-wake
// when rewakeWithInfo re-delivers an info note.
func (t *Translator) workflowMessageWoke(ctx context.Context, chatID marotte.ChatID, attr FrameAttribution, reason agentInitiatedReason) {
	if reason != reasonSendMessageWake && reason != reasonNotificationRewake {
		return
	}
	sc := frameSteerScope(chatID, attr)
	if rowIDs := t.wakes.rewoken(t.bufferSession(sc)); len(rowIDs) > 0 {
		t.settleRows(ctx, sc, rowIDs, marotte.EntrySteerDelivered{ReadTs: time.Now().UnixMilli()})
	}
	if reason == reasonSendMessageWake {
		t.settleWaker(ctx, sc, wakeOpen)
	}
}

// settleRows files d for each of sc's unsettled rows rowIDs names.
func (t *Translator) settleRows(ctx context.Context, sc steerScope, rowIDs []string, d marotte.EntrySteerDelivered) {
	epoch, ok := t.epochMessages(ctx, sc, t.bufferSession(sc))
	if !ok {
		return
	}
	for i := range epoch {
		if slices.Contains(rowIDs, epoch[i].ID) {
			d.SteerID = epoch[i].ID
			t.settleMessage(ctx, sc, &epoch[i], d)
		}
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
	// pausedFiled is the rows filed while paused: KAS buffers a message to a running step, so only
	// these can have woken a resume.
	pausedFiled map[string]struct{}
	filed       []string
	// rewaking is the rows the rewake cancel named by id, which the re-wake turn delivers;
	// rewakeTurn is set once a turn started after it, so the next turn_start expires them.
	rewaking []string
	before   int
	state    wakeState
	// rewake is a reader's cancel that cleared a workflow message: KAS may re-deliver it by a
	// send-message-wake with no new notify (rewakeWithInfo), whose waker no row names.
	rewake     bool
	rewakeTurn bool
	// A step session's resume is the wake no frame names: KAS wakes a paused step with a parent's
	// message and stamps no reason on the step's frames (kiro-cli 2.37.1 driveExecution), so the
	// pause, the node's next node_start and that turn_start are the evidence. paused runs from
	// node_paused to node_start, resumed from there to the turn_start, and resume marks that turn.
	paused, resumed, resume bool
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
	if len(s.filed) == 0 && s.state == wakeDone && !s.rewake && len(s.rewaking) == 0 && !s.paused && !s.resumed {
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
	if s.paused {
		if s.pausedFiled == nil {
			s.pausedFiled = map[string]struct{}{}
		}
		s.pausedFiled[id] = struct{}{}
	}
}

// nodePaused records that KAS paused the node step session runs.
func (w *wakeLedger) nodePaused(session string) {
	if session == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.entry(session)
	s.paused, s.resumed = true, false
}

// nodeStarted records session's node_start: after a pause, the session's next turn is its resume.
func (w *wakeLedger) nodeStarted(session string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	s, ok := w.by[session]
	if !ok || !s.paused {
		return
	}
	s.paused, s.resumed = false, true
}

// forget drops session's entry: its run ended, so no later frame settles a row filed to it.
func (w *wakeLedger) forget(session string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.by, session)
}

// turnStarted records session's turn_start: the rows filed so far are its candidate wakers, unless a
// reader's cancel just cleared a message KAS may re-deliver in it. It reports a step's resume, whose
// waker the caller settles at once, because none of the turn's frames names it, and the rows a
// cancel left for a re-wake that the turn before this one did not deliver.
func (w *wakeLedger) turnStarted(session string) (resume bool, expired []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	s, ok := w.by[session]
	if !ok {
		return false, nil
	}
	if s.rewakeTurn {
		expired, s.rewaking, s.rewakeTurn = s.rewaking, nil, false
	} else if len(s.rewaking) > 0 {
		s.rewakeTurn = true
	}
	s.before, s.state = len(s.filed), wakeOpen
	s.resume, s.resumed = s.resumed, false
	if s.rewake || s.before == 0 {
		s.state = wakeDone
	}
	s.rewake = false
	resume = s.resume && s.state == wakeOpen
	w.forgetIdle(session, s)
	return resume, expired
}

// rewoken answers and forgets the rows session's re-wake turn delivers, once that turn started.
func (w *wakeLedger) rewoken(session string) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	s, ok := w.by[session]
	if !ok || !s.rewakeTurn {
		return nil
	}
	rowIDs := s.rewaking
	s.rewaking, s.rewakeTurn = nil, false
	w.forgetIdle(session, s)
	return rowIDs
}

// cleared ends session's epoch: every row filed to it is settled, or left in rewaking for the
// re-wake turn. rewake marks a reader's cancel that cleared a workflow message.
func (w *wakeLedger) cleared(session string, rewake bool, rewaking []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.entry(session)
	s.filed, s.before, s.state, s.rewake, s.pausedFiled = nil, 0, wakeDone, rewake, nil
	s.rewaking, s.rewakeTurn = rewaking, false
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
		if _, whilePaused := s.pausedFiled[id]; whilePaused || !s.resume {
			out[id] = struct{}{}
		}
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
	for id := range s.pausedFiled {
		if _, ok := unsettled[id]; !ok {
			delete(s.pausedFiled, id)
		}
	}
	s.filed, s.before, s.state = kept, before, state
	w.forgetIdle(session, s)
}

// workflowMessageRead settles a notification steer's take-up against the unsettled workflow
// message of sc's current epoch that KAS's id kasID names, else the oldest with these words among
// those KAS gave no id (KAS drains its buffer in append order; the take-up names no sender, so two
// senders' identical words are told apart by order only). Both rows are stamped delivered and the
// caller appends no steer. The replay merge pairs on kasID.
func (t *Translator) workflowMessageRead(ctx context.Context, sc steerScope, kasID, text string) bool {
	epoch, _ := t.epochMessages(ctx, sc, t.bufferSession(sc))
	match := func(m *marotte.WorkflowMessage) bool { return m.Steer.KASID == "" && m.Steer.Text == text }
	if slices.ContainsFunc(epoch, func(m marotte.WorkflowMessage) bool { return m.Steer.KASID == kasID && kasID != "" }) {
		match = func(m *marotte.WorkflowMessage) bool { return m.Steer.KASID == kasID }
	}
	for i := range epoch {
		if match(&epoch[i]) {
			t.settleMessage(ctx, sc, &epoch[i], marotte.EntrySteerDelivered{SteerID: epoch[i].ID, KASID: kasID, ReadTs: time.Now().UnixMilli()})
			t.settleWaker(ctx, sc, wakePending)
			return true
		}
	}
	return false
}

// settleClear files a clear of sc's buffer naming kasIDs, then settles its epoch's messages by the
// fold's rule (marotte.EntrySteerCleared.Settlement), and reports whether the fact landed. The fact
// goes first and the receiver's fold settles from it alone, so a cut after it loses nothing and a
// refused fact has no effect. named counts the workflow messages among kasIDs: a reader's cancel
// that cleared one makes the next turn a re-wake.
func (t *Translator) settleClear(ctx context.Context, sc steerScope, cause marotte.SteerClearCause, named int, kasIDs []string) bool {
	session := t.bufferSession(sc)
	epoch, _ := t.epochMessages(ctx, sc, session)
	c := &marotte.EntrySteerCleared{Cause: cause, Session: session, IDs: append([]string{}, kasIDs...)}
	if !t.appendCleared(ctx, sc, c) {
		return false
	}
	now := time.Now().UnixMilli()
	var rewaking []string
	for i := range epoch {
		d, settles := c.Settlement(&epoch[i], now)
		if !settles {
			if c.Rewakes(&epoch[i]) {
				rewaking = append(rewaking, epoch[i].ID)
			}
			continue
		}
		// The peer follows whether or not this lands: the receiver's fold reads it from the fact.
		t.appendDelivered(ctx, sc, &d)
		t.mirrorSettlement(ctx, sc, &epoch[i], d)
	}
	t.wakes.cleared(session, cause == marotte.SteerClearCauseCancel && named > 0, rewaking)
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
	switch {
	case m.Steer.Origin == marotte.SteerOriginPeer && m.Steer.Chat != "":
		t.appendDelivered(ctx, chatSteerScope(m.Steer.Chat), &d)
	case m.Steer.OriginRun != "" && m.Steer.StepPath != "":
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
