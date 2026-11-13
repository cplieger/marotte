package command

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// AdmissionOutcome is a test fixture's verdict on a prompt's take, which launchingRun answers from: the agent's run
// list decides it in production.
type AdmissionOutcome int

const (
	AdmissionAcquired AdmissionOutcome = iota
	AdmissionBusy
	AdmissionStarting
	AdmissionClosed
	AdmissionChatGone
)

// promptReserver is the fixture half launchingRun reads a prompt's verdict from.
type promptReserver interface {
	ReserveTurnForPrompt(ctx context.Context, chatID marotte.ChatID, wait time.Duration, stamp uint64, messageID string) (AdmissionOutcome, Reservation)
}

// idleReserver is the fixture half launchingRun reads a revert's or a summary's take from.
type idleReserver interface {
	ReserveIdleTurn(ctx context.Context, chatID marotte.ChatID, source marotte.TurnOpenSource, stamp uint64, wait time.Duration) (IdleAdmission, Reservation)
	ReserveTurn(ctx context.Context, chatID marotte.ChatID, source marotte.TurnOpenSource, stamp uint64, wait time.Duration) (IdleAdmission, Reservation)
}

// launchingRun is runQueue over a fixture's admission: a send the fixture admits is taken and launched at once
// (launchEntry), a busy holder steers it in steer mode or queues it, and every queued row waits in rows. Stops and
// send-now answers come from the scripted funcs, nothing when unset.
type launchingRun struct {
	roles   *promptRoles
	sendNow func(id, stop string, hasStop bool) (RunStop, error)
	stop    func(target string) RunStop
	unqueue func(id string) error
	rows    []marotte.QueuedPrompt
	cleared []string
	undone  []*TurnStop
	steer   bool
	// live queues every queue_prompt; compacting queues every send, as a running compaction does.
	live, compacting bool
	mu               sync.Mutex
}

var _ runQueue = (*launchingRun)(nil)

func (q *launchingRun) Submit(ctx context.Context, chatID marotte.ChatID, stamp uint64, s *Submission) (SubmitAnswer, error) {
	q.mu.Lock()
	queues := q.compacting || q.live && s.Kind == marotte.SendQueuePrompt
	q.mu.Unlock()
	if queues {
		return q.queue(s), nil
	}
	out := AdmissionAcquired
	var res Reservation
	if r, ok := q.roles.admission.(idleReserver); ok && s.Origin == marotte.RunFromTangent {
		var idle IdleAdmission
		idle, res = r.ReserveTurn(ctx, chatID, marotte.TurnSourcePrompt, stamp, AdmissionWait)
		out = map[IdleAdmission]AdmissionOutcome{
			IdleAcquired: AdmissionAcquired, IdleTurnOpen: AdmissionBusy, IdleHeld: AdmissionBusy,
			IdleClosed: AdmissionClosed, IdleChatGone: AdmissionChatGone,
		}[idle]
	} else if r, ok := q.roles.admission.(promptReserver); ok {
		out, res = r.ReserveTurnForPrompt(ctx, chatID, AdmissionWait, stamp, s.Row.ID)
	} else {
		res = testReservation(chatID, true)
	}
	switch {
	case out == AdmissionClosed:
		return SubmitAnswer{}, marotte.ErrChatClosedDuringCall
	case out == AdmissionChatGone:
		return SubmitAnswer{}, ErrChatNotFound
	case out == AdmissionStarting:
		return SubmitAnswer{}, ErrSubmitStarting
	case out == AdmissionAcquired && s.Kind == marotte.SendQueuePrompt:
		q.roles.admission.ReleaseTurnReservation(res)
		return SubmitAnswer{}, ErrSubmitNoTurn
	case out == AdmissionBusy && !s.NoSteer && q.steerMode(s) && s.Kind == marotte.SendPlain:
		if s.PromptOnly {
			return SubmitAnswer{}, ErrSubmitPromptOnly
		}
		return SubmitAnswer{Steer: true}, nil
	case out == AdmissionBusy:
		return q.queue(s), nil
	}
	claimed := false
	l := &EntryLaunch{Kind: marotte.RunKindRow, Row: s.Row, Res: res, Awaited: s.Awaited}
	if s.Composer {
		l.Claim = func() bool {
			was := claimed
			claimed = true
			return !was
		}
	}
	turnID, err := launchEntry(ctx, q.roles, chatID, l)
	if err != nil {
		return SubmitAnswer{}, err
	}
	return SubmitAnswer{As: SubmitStarted, TurnID: turnID}, nil
}

func (q *launchingRun) queue(s *Submission) SubmitAnswer {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !slices.ContainsFunc(q.rows, func(r marotte.QueuedPrompt) bool { return r.ID == s.Row.ID }) {
		q.rows = append(q.rows, s.Row)
	}
	return SubmitAnswer{As: SubmitQueued, Settle: s.Composer}
}

func (q *launchingRun) steerMode(s *Submission) bool {
	return q.steer || s.Mode != marotte.InterruptQueue
}

func (q *launchingRun) TakeIdle(ctx context.Context, chatID marotte.ChatID, kind IdleKind, stamp uint64, wait time.Duration) (IdleAdmission, Reservation, string) {
	r, ok := q.roles.admission.(idleReserver)
	if !ok {
		return IdleAcquired, testReservation(chatID, true), "idle-1"
	}
	if kind == IdleRevert {
		out, res := r.ReserveIdleTurn(ctx, chatID, marotte.TurnSourceRevert, stamp, wait)
		return out, res, "revert-1"
	}
	out, res := r.ReserveTurn(ctx, chatID, marotte.TurnSourcePrompt, stamp, wait)
	return out, res, "summary-1"
}

func (*launchingRun) SummaryLaunched(marotte.ChatID, string, Reservation, string, error) {}

func (q *launchingRun) SendNow(_ context.Context, _ marotte.ChatID, id, stop string, hasStop bool) (RunStop, error) {
	if q.sendNow != nil {
		return q.sendNow(id, stop, hasStop)
	}
	return RunStop{}, nil
}

func (q *launchingRun) ClearNext(_ marotte.ChatID, id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cleared = append(q.cleared, id)
}

func (q *launchingRun) Unqueue(_ context.Context, _ marotte.ChatID, id string) error {
	if q.unqueue != nil {
		return q.unqueue(id)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.rows = slices.DeleteFunc(q.rows, func(r marotte.QueuedPrompt) bool { return r.ID == id })
	return nil
}

func (q *launchingRun) Stop(_ context.Context, _ marotte.ChatID, target string) RunStop {
	if q.stop != nil {
		return q.stop(target)
	}
	return RunStop{}
}

func (q *launchingRun) StopUndone(_ marotte.ChatID, stop *TurnStop) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.undone = append(q.undone, stop)
}

func (*launchingRun) RunView(context.Context, marotte.ChatID) marotte.RunView {
	return marotte.RunView{}
}

// queued is the rows waiting, in order.
func (q *launchingRun) queued() []marotte.QueuedPrompt {
	q.mu.Lock()
	defer q.mu.Unlock()
	return slices.Clone(q.rows)
}

// runOf is the launchingRun a fixture's roles carry.
func runOf(roles *promptRoles) *launchingRun {
	q, _ := roles.runq.(*launchingRun)
	return q
}

// stoppingRun is runQueue for a cancel handler test: every stop is a stop of whatever runs, requested on stops, as the
// list's stop of a running turn is.
type stoppingRun struct {
	launchingRun
	stops turnStopper
}

func stopRunOf(stops turnStopper) *stoppingRun { return &stoppingRun{stops: stops} }

func (q *stoppingRun) Stop(_ context.Context, chatID marotte.ChatID, _ string) RunStop {
	return RunStop{Stop: q.stops.RequestStop(chatID, marotte.TurnStopReader), Arm: StopCancel}
}

// runOver is a launchingRun whose takes ask adm, for a handler test with no prompt roles.
func runOver(adm turnAdmission) *launchingRun {
	return &launchingRun{roles: &promptRoles{admission: adm}}
}
