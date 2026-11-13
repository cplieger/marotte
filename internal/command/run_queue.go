package command

// The chat's run order as the commands drive it. The agent's runList owns the order: every send, queued row,
// compaction, revert and summary is an entry on it, and only its taker starts one. The commands place entries and
// launch the turn-kind ones the taker hands over (Dispatcher.LaunchEntry).

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// Submission is one sent message for the chat's run order (runQueue.Submit).
type Submission struct {
	Origin marotte.RunEntryOrigin
	// Mode is what the send does beside a steerable turn; empty takes the interrupt_mode setting.
	Mode marotte.InterruptMode
	Row  marotte.QueuedPrompt
	Kind marotte.SendKind
	// NoSteer rules steering out: the send is only ever a turn of its own.
	NoSteer bool
	// PromptOnly is text only a prompt carries (PromptCommand.PromptOnly).
	PromptOnly bool
	// Wait: the caller waits for its send's first outcome, its own take's turn open or kiro-cli holding its steer (a
	// prompt POST, a tangent delivery).
	Wait bool
	// Composer: a page's POST, which owes the composer settle once (settleComposerOnPrompt).
	Composer bool
	// Awaited opens the turn with TurnOpen.Awaited (a tangent delivery's receipt hold).
	Awaited bool
	// Retry marks a prompt whose turn, ending clean on the wire with nothing emitted, is re-sent once.
	Retry bool
	// SteerOnly places the send only as a steer into the running turn, else refuses it ErrSubmitNoSteer.
	SteerOnly bool
}

// SubmitAs is where a submitted message went.
type SubmitAs string

// A submission started its turn, was steered into the running one, or waits on the list.
const (
	SubmitStarted SubmitAs = "started"
	SubmitSteered SubmitAs = "steered"
	SubmitQueued  SubmitAs = "queued"
)

// SubmitAnswer is Submit's answer. Settle says the caller owes the composer settle before it answers.
type SubmitAnswer struct {
	As     SubmitAs
	TurnID string
	Run    marotte.RunView
	Settle bool
}

// Submit's refusals, which the caller answers as HTTP.
var (
	// ErrSubmitNoTurn refuses a queue_prompt with nothing live or waiting to queue behind.
	ErrSubmitNoTurn = errors.New("nothing is running to queue behind, so send this as a prompt instead")
	// ErrSubmitNoSteer refuses a steer-only send the running turn would not take as a steer.
	ErrSubmitNoSteer = errors.New("nothing is running to steer, so send this as a prompt instead")
	// ErrSubmitTooBig refuses a row past the draft cap behind a running turn.
	ErrSubmitTooBig = errors.New("a message queued behind a running turn is capped at the draft size")
	// ErrSubmitPromptOnly refuses prompt-only text that would be steered.
	ErrSubmitPromptOnly = errors.New("a steer cannot carry this message; send it once the turn ends, or queue it")
	// errSubmitFull refuses a row past the queue's row count or encoded size.
	errSubmitFull = errors.New("this chat already holds the most follow-ups it can queue; wait for one to send, then try again")
	// ErrSubmitStarting refuses a submission whose life a held close kept from starting within AdmissionWait.
	ErrSubmitStarting = errors.New("the chat is closing or starting; try again in a moment")
	// ErrTakeWithdrawn refuses a launch's turn open: a stop withdrew the take before it opened.
	ErrTakeWithdrawn = errors.New("the queued message was stopped before its turn opened")
	// ErrRowSending refuses an unqueue of a row whose turn is starting or running.
	ErrRowSending = errors.New("this follow-up is already being sent")
	// ErrRowInKAS answers an unqueue of a row kiro-cli holds as a steer, which only a clear of its buffer removes.
	ErrRowInKAS = errors.New("kiro-cli holds this message as a steer")
	// ErrRowHeld refuses a send-now of a row no turn end sends.
	ErrRowHeld = errors.New("this follow-up was queued before the server restarted; edit it to send it")
)

// IdleKind is a take that runs now or not at all (runQueue.TakeIdle).
type IdleKind uint8

const (
	// IdleRevert is a rewind's checkpoint revert.
	IdleRevert IdleKind = iota
	// IdleSummary is a tangent merge's summary turn.
	IdleSummary
)

// StopArm is how a stop of a run entry is carried out.
type StopArm uint8

const (
	// StopNothing is a target naming nothing a stop may stop.
	StopNothing StopArm = iota
	// StopCancel is a running turn or kiro-cli's run: one session/cancel, then the grace (ArmStopGrace).
	StopCancel
	// StopQuiet is a take whose prompt was never written: withdrawn or kept unsent, with no session/cancel.
	StopQuiet
)

// RunStop is one stop the run order applied (runQueue.Stop, runQueue.SendNow).
type RunStop struct {
	// Stop is the stop's send hold and captures; nil for StopNothing. The caller releases it.
	Stop *TurnStop
	Arm  StopArm
}

// EntryLaunch is one taken run entry whose turn the launcher opens on Res, which it owns from here. Claim is the
// composer settle's one-shot claim for a page's POST, nil for every other launch.
type EntryLaunch struct {
	Claim    func() bool
	Row      marotte.QueuedPrompt
	Kind     marotte.RunEntryKind
	Replaces string
	// Resends are the dock keys of the steers whose words Row carries, back from kiro-cli unread.
	Resends []string
	Res     Reservation
	// Fence is the take's: a re-fence since (a shutdown, a teardown) refuses the open with ErrTurnSuperseded.
	Fence   TurnFence
	Awaited bool
}

// runQueue is the chat's run order: the agent's runList and its glue.
type runQueue interface {
	// Submit places a sent message: steered into the running turn or taken now (a waiting caller is answered once
	// kiro-cli holds the steer or the turn opens, up to AdmissionWait, then queued) or queued. A send bound for the
	// running turn past kiro-cli's steer budget is refused ErrSteerFull. stamp is where the send's intent began.
	Submit(ctx context.Context, chatID marotte.ChatID, stamp uint64, s *Submission) (SubmitAnswer, error)
	// TakeIdle takes the chat for a revert or a summary, which run now or not at all: IdleTurnOpen while a turn runs,
	// IdleHeld past wait, IdleClosed after a close committed since stamp, IdleChatGone for a deleted chat.
	TakeIdle(ctx context.Context, chatID marotte.ChatID, kind IdleKind, stamp uint64, wait time.Duration) (IdleAdmission, Reservation, string)
	// SummaryLaunched reports a granted summary's open (its turn id), or its refusal before the open, for the take res
	// names.
	SummaryLaunched(chatID marotte.ChatID, entryID string, res Reservation, turnID string, err error)
	// SendNow moves the row to the front and stops the turn stop names while it is the lone live turn (hasStop false
	// names RunView.Cancel's target).
	SendNow(ctx context.Context, chatID marotte.ChatID, id, stop string, hasStop bool) (RunStop, error)
	// ClearNext undoes a send-now whose stop was not delivered.
	ClearNext(chatID marotte.ChatID, id string)
	// Unqueue removes a waiting row; a row whose turn starts or runs, or whose steer is being sent, answers
	// ErrRowSending, a steer kiro-cli holds ErrRowInKAS, an unknown id nil.
	Unqueue(ctx context.Context, chatID marotte.ChatID, id string) error
	// Resume lets the rows a stop paused go in order, and a row sent from now on joins the queue unpaused.
	Resume(ctx context.Context, chatID marotte.ChatID) error
	// Stop stops the live entry target names, or RunView.Cancel's with target "".
	Stop(ctx context.Context, chatID marotte.ChatID, target string) RunStop
	// StopUndone restores what a stop whose session/cancel was not delivered marked.
	StopUndone(chatID marotte.ChatID, stop *TurnStop)
	// RunView is the chat's run order as the header draws it.
	RunView(ctx context.Context, chatID marotte.ChatID) marotte.RunView
}

// CarryOutStop carries out a stop the run order applied outside a command, as a Cancel does: answered asks, one
// session/cancel, the turn's terminals killed and the grace armed. A cancel not delivered restores what the stop
// marked (runQueue.StopUndone) and answers its error.
func (d *Dispatcher) CarryOutStop(ctx context.Context, chatID marotte.ChatID, rs RunStop) error {
	if d == nil || d.carryStop == nil {
		return errStopUnwired
	}
	return d.carryStop(ctx, chatID, rs)
}

// errStopUnwired fails a stop carried out before RegisterDefaults bound its collaborators, a wiring error.
var errStopUnwired = errors.New("command: a stop was carried out before the dispatcher was registered")

// LaunchEntry opens the turn of a taken run entry on its reservation and answers the opened turn's id, or the
// refusal before the open (the reservation is then released). A retry runs its whole turn before it returns.
func (d *Dispatcher) LaunchEntry(ctx context.Context, chatID marotte.ChatID, l *EntryLaunch) (string, error) {
	if d == nil || d.prompts == nil {
		return "", errLaunchUnreserved
	}
	return launchEntry(ctx, d.prompts, chatID, l)
}

// SteerEntry routes a run-list entry the chat's list offered into its running turn and sends it, under the chat's
// steer lock. A refusal class answers that nothing was sent; kiro-cli's answer is the record's to report.
func (d *Dispatcher) SteerEntry(ctx context.Context, chatID marotte.ChatID, s *EntrySteer) (refuse string) {
	if d == nil || d.prompts == nil {
		return SteerRefuseNoTurn
	}
	return steerEntry(ctx, d.prompts, chatID, s)
}

func steerEntry(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, s *EntrySteer) (refuse string) {
	unlock, err := roles.queue.LockSteerOps(ctx, chatID)
	if err != nil {
		return SteerRefuseNoTurn
	}
	defer unlock()
	t := roles.steerTarget(chatID)
	sends, refuse := t.Queue.RouteEntry(chatID, s, steerHolder(roles, chatID))
	if refuse != "" {
		return refuse
	}
	_ = issueSteers(ctx, t, sends, "", func(snd SteerSend, queued bool, err error) {
		t.Queue.SteerSent(t.Key, snd, queued, err)
	})
	return ""
}

func launchEntry(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, l *EntryLaunch) (string, error) {
	switch l.Kind {
	case marotte.RunKindRetry:
		return launchRetry(ctx, roles, chatID, l)
	case marotte.RunKindRow:
		return launchQueuedRow(ctx, roles, l)
	case marotte.RunKindResume:
		return launchResume(ctx, roles, l)
	case marotte.RunKindTurn, marotte.RunKindCompaction, marotte.RunKindRevert:
	}
	roles.admission.ReleaseTurnReservation(l.Res)
	return "", errLaunchUnreserved
}

// launchQueuedRow opens row's turn on the take's reservation, which it owns from here (launchPrompt), with the
// options its submission carried.
func launchQueuedRow(ctx context.Context, roles *promptRoles, l *EntryLaunch) (string, error) {
	row := &l.Row
	p := &marotte.PromptCommand{
		Text: row.Text, MessageID: row.ID, DisplayText: row.Label, Attachments: row.Attachments, Caller: row.Caller,
		Model: row.Model,
	}
	turnID, err := startPrompt(ctx, roles, l.Res.Chat, p, &launch{
		res: l.Res, fence: l.Fence, awaited: l.Awaited, claimComposer: l.Claim, resends: l.Resends,
	})
	if err != nil {
		logLaunchRefused(l.Res.Chat, row.ID, err)
		return "", err
	}
	slog.Info("queued prompt dispatched", "chat_id", l.Res.Chat, "message_id", row.ID)
	return turnID, nil
}

// launchResume opens marotte's own prompt that continues a turn the compaction failsafe stopped, on the session's
// model. kiro-cli stores every prompt as the user's (kirodotdev/Kiro#12346), so only its turn_open's Automatic says
// marotte sent it.
func launchResume(ctx context.Context, roles *promptRoles, l *EntryLaunch) (string, error) {
	p := &marotte.PromptCommand{Text: l.Row.Text, MessageID: l.Row.ID}
	turnID, err := startPrompt(ctx, roles, l.Res.Chat, p, &launch{res: l.Res, fence: l.Fence, automatic: true})
	if err != nil {
		logLaunchRefused(l.Res.Chat, l.Row.ID, err)
		return "", err
	}
	slog.Info("resumed the turn the compaction failsafe stopped", "chat_id", l.Res.Chat, "message_id", l.Row.ID)
	return turnID, nil
}

// launchRetry re-sends a prompt whose turn produced nothing, once, as a turn of its own on a recreated session. Its
// open names the empty turn (Replaces), so a stop pressed since refuses it.
func launchRetry(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, l *EntryLaunch) (string, error) {
	defer roles.admission.ReleaseTurnReservation(l.Res)
	sb := roles.bridges.Bridge(chatID)
	if sb == nil {
		return "", marotte.ErrSessionClosed
	}
	p := &marotte.PromptCommand{
		Text: l.Row.Text, MessageID: l.Row.ID, DisplayText: l.Row.Label, Attachments: l.Row.Attachments,
		Caller: l.Row.Caller, Model: l.Row.Model,
	}
	params, _ := buildPromptParams(ctx, roles.workspace, sb, p, l.Replaces, historyInlineImages(ctx, roles.chats, chatID))
	slog.Warn("empty turn detected, recreating session", "chat_id", chatID)
	if err := roles.bridges.RefreshForRetry(ctx, chatID, sb); err != nil {
		if errors.Is(err, marotte.ErrChatClosedDuringCall) {
			slog.Info("empty turn: the chat closed, not retrying", "chat_id", chatID)
		} else {
			slog.Error("empty turn: clear session ID", "chat_id", chatID, keyError, err)
		}
		return "", err
	}
	return retryEmptyTurnPrompt(ctx, roles, l.Res, l.Replaces, p, params)
}

func logLaunchRefused(chatID marotte.ChatID, messageID string, err error) {
	switch {
	case errors.Is(err, ErrTakeWithdrawn):
		slog.Debug("a queued send was stopped before its turn opened", "chat_id", chatID, "message_id", messageID)
	case errors.Is(err, marotte.ErrChatClosedDuringCall):
		slog.Debug("a queued send stood down: the chat closed", "chat_id", chatID, "message_id", messageID)
	default:
		slog.Warn("queued send could not open; it waits for the next turn end", "chat_id", chatID, "message_id", messageID, keyError, err)
	}
}
