package translate

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
)

// workflowMessage is one send: its words and both ends. `to` is the receiver's steer scope and
// `from` the sender's.
type workflowMessage struct {
	id       string
	text     string
	severity string
	step     string
	run      string
	path     string
	chat     marotte.ChatID
	origin   marotte.SteerOrigin
	to       steerScope
	from     steerScope
	sentTs   int64
}

// row is the message's entry in scope's log. Each row names its peer, which the take-up stamps
// from the receiver's log alone: the step on the chat's row, the chat on the step's.
func (m *workflowMessage) row(scope steerScope, readTs int64) *marotte.EntrySteer {
	e := &marotte.EntrySteer{
		Text: m.text, Origin: m.origin, Severity: m.severity, ProducedTs: m.sentTs, ReadTs: readTs,
		StepPath: m.path,
	}
	if readTs != 0 {
		e.State = marotte.SteerStateRead
	}
	if scope.run == "" {
		e.Step, e.OriginRun = m.step, m.run
	} else {
		e.Chat = m.chat
	}
	return e
}

// HandleWorkflowMessage records a notify's workflow message. A notify that is no workflow message
// of this chat, or whose step this process does not know, records nothing.
func (t *Translator) HandleWorkflowMessage(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	p, ok := unmarshalParams[kasSessionNotify](msg, "_kiro/session/notify")
	if !ok || p.Message == "" {
		return
	}
	m, ok := t.workflowMessageOf(chatID, &p)
	if !ok {
		return
	}
	var readTs int64
	if !t.kasBuffers(m) {
		readTs = m.sentTs
	}
	t.appendSteer(ctx, m.to, m.id, m.row(m.to, readTs))
	t.appendSteer(ctx, m.from, m.id, m.row(m.from, readTs))
}

// workflowMessageOf resolves both ends. A parent's message names the chat's own session as the
// caller and a known step as the target; a step's names a known step as the caller and the chat's
// session as the target. A run bridge's own session is no chat.
func (t *Translator) workflowMessageOf(chatID marotte.ChatID, p *kasSessionNotify) (*workflowMessage, bool) {
	if strings.HasPrefix(string(chatID), marotte.RunSubjectPrefix) {
		return nil, false
	}
	own := t.sessions.ParentACPSession(chatID)
	if own == "" {
		return nil, false
	}
	m := &workflowMessage{
		id: marotte.WorkflowMessageIDPrefix + ids.NewMessageID(), text: p.Message, severity: p.Severity,
		chat: chatID, sentTs: time.Now().UnixMilli(),
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
		return nil, false
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

// kasBuffers mirrors KAS's choice in deliverSendMessageAdmitted (kiro-cli 2.28.0): a step's
// `success` is always buffered, anything else only while the receiver is mid-turn; otherwise KAS
// wakes the receiver with a prompt no frame shows.
func (t *Translator) kasBuffers(m *workflowMessage) bool {
	if m.origin == marotte.SteerOriginStep && m.severity == severitySuccess {
		return true
	}
	if m.to.run != "" {
		return t.runs.RunStepReading(m.to.key)
	}
	_, open := t.turns.OwnTurn(m.to.key)
	return open
}

const severitySuccess = "success"

// workflowMessageRead settles a notification steer's take-up against the oldest waiting workflow
// message to sc with these words (KAS drains its buffer in append order; the take-up names no
// sender, so two senders' identical words are told apart by order only). Both rows are stamped
// delivered and the caller appends no steer. kasID is KAS's id for the row it persisted, which the
// replay merge pairs on.
func (t *Translator) workflowMessageRead(ctx context.Context, sc steerScope, kasID, text string) bool {
	m, ok := t.waitingMessage(ctx, sc, func(s *marotte.EntrySteer) bool { return s.Text == text })
	if !ok {
		return false
	}
	t.settleMessage(ctx, sc, &m, marotte.EntrySteerDelivered{SteerID: m.ID, KASID: kasID, ReadTs: time.Now().UnixMilli()})
	return true
}

// workflowMessageCleared settles the oldest waiting workflow message to sc as dropped, for a clear
// naming an id no row carries.
func (t *Translator) workflowMessageCleared(ctx context.Context, sc steerScope) bool {
	m, ok := t.waitingMessage(ctx, sc, func(*marotte.EntrySteer) bool { return true })
	if !ok {
		return false
	}
	t.settleMessage(ctx, sc, &m, marotte.EntrySteerDelivered{SteerID: m.ID, ReadTs: time.Now().UnixMilli(), Dropped: true})
	return true
}

// waitingMessage answers the oldest message sc's log holds as received and not yet settled.
func (t *Translator) waitingMessage(
	ctx context.Context, sc steerScope, match func(*marotte.EntrySteer) bool,
) (marotte.WorkflowMessage, bool) {
	var waiting []marotte.WorkflowMessage
	var err error
	received := marotte.SteerOriginStep
	if sc.run != "" {
		waiting, err = t.runs.RunWaitingWorkflowMessages(ctx, sc.run)
		received = marotte.SteerOriginParent
	} else {
		waiting, err = t.chats.WaitingWorkflowMessages(ctx, sc.key)
	}
	if err != nil {
		slog.Warn("workflow message: the receiver's log is unreadable, so its take-up is recorded as a new row",
			"chat_id", sc.key, "run", sc.run, "node_path", sc.path, "error", err)
		return marotte.WorkflowMessage{}, false
	}
	for i := range waiting {
		s := &waiting[i].Steer
		if s.Origin == received && (sc.run == "" || s.StepPath == sc.path) && match(s) {
			return waiting[i], true
		}
	}
	return marotte.WorkflowMessage{}, false
}

// settleMessage files d in the receiver's log and, without KAS's id, in the peer's its row names.
func (t *Translator) settleMessage(ctx context.Context, sc steerScope, m *marotte.WorkflowMessage, d marotte.EntrySteerDelivered) {
	t.appendDelivered(ctx, sc, &d)
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

func (t *Translator) appendDelivered(ctx context.Context, sc steerScope, d *marotte.EntrySteerDelivered) {
	ctx = durable.Context(ctx)
	if sc.run != "" {
		t.runs.RunSteerDelivered(ctx, sc.run, sc.path, d)
		return
	}
	t.appendLaneless(ctx, sc.key, marotte.EntryKindSteerDelivered, marotte.SteerDeliveredID(d.SteerID), *d,
		func(ctx context.Context, turn *turnlog.Turn) ([]turnlog.Sealed, error) {
			return turn.SteerDelivered(ctx, d)
		})
}
