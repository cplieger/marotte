package agent

// The glue between a chat's run list (run_list.go) and everything around it. Every operation runs in one lc.mu
// section, writes the list's projection to the chat record in that section (lifecycle, then store) and carries out
// the effects the list recorded: a take's launch on its own goroutine, a compaction's start, a failure toast.

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"reflect"
	"slices"
	"time"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

var _ runEffectsHost = (*bridgeCoordinator)(nil)

// runEffectsHost carries out what a chat's run list owes outside it; nil (a bare registry in tests) carries out
// nothing.
type runEffectsHost interface {
	// storedRun reads the chat's stored list and its version, false for no record. Called under r.mu.
	storedRun(chatID marotte.ChatID) ([]marotte.RunEntry, uint64, bool)
	// writeRun applies mutate to the chat's record, which declines (false) for a missing record.
	writeRun(chatID marotte.ChatID, mutate func(c *marotte.Chat) bool) error
	// startEntry carries out a take on its own goroutine: a launch, or a compaction's start.
	startEntry(lc *chatLifecycle, e *runEntry)
	// toast tells the chat's pages, in detail's words, of a run-list outcome the dock alone does not explain.
	toast(chatID marotte.ChatID, code marotte.ErrorCode, detail string)
	// spawnRun runs fn on the process's in-flight count, refused once shutdown began.
	spawnRun(fn func())
}

var errRunRecordGone = errors.New("agent: the chat has no record for its run list")

// advanceLocked runs the list's one taker and carries out what it owes; wakeLocked calls it. Caller holds mu.
func (lc *chatLifecycle) advanceLocked() {
	if lc.reg == nil || lc.ended {
		return
	}
	f := lc.orderFactsLocked()
	lc.run.advance(&f)
	lc.flushRunLocked()
}

// flushRunLocked writes the projection when the list changed (or a failed write is owed) and starts each take.
// Caller holds mu.
func (lc *chatLifecycle) flushRunLocked() {
	fx := lc.run.takeEffects()
	if lc.ended || lc.reg == nil || lc.reg.runHost == nil {
		return
	}
	host := lc.reg.runHost
	if fx.changed || lc.run.dirty {
		lc.writeRunLocked()
	}
	for _, e := range fx.launch {
		host.startEntry(lc, e)
	}
	if len(fx.toast) > 0 {
		detail := compactFailed
		if slices.ContainsFunc(fx.toast, func(e *runEntry) bool { return e.origin == marotte.RunFromFailsafe }) {
			detail = compactFailsafeFailed
		}
		host.spawnRun(func() { host.toast(lc.chatID, marotte.ErrCodeCompactionFailed, detail) })
	}
	for _, detail := range fx.notices {
		host.spawnRun(func() { host.toast(lc.chatID, marotte.ErrCodeResumeNotSent, detail) })
	}
}

// writeRunLocked stores the list's projection by the version rule: the stored version and the list's, plus one. A
// run chat has no record, and a missing record declines; a failed write marks the list dirty, so the next write
// carries it. Caller holds mu.
func (lc *chatLifecycle) writeRunLocked() {
	if isRunChat(lc.chatID) {
		lc.run.dirty = false
		return
	}
	proj := lc.run.project()
	var version uint64
	err := lc.reg.runHost.writeRun(lc.chatID, func(c *marotte.Chat) bool {
		c.Run = proj
		c.RunVersion = max(c.RunVersion+1, lc.run.version)
		version = c.RunVersion
		return true
	})
	switch {
	case err == nil:
		if version != 0 {
			lc.run.version, lc.runWritten = version, version
		}
		lc.run.stored, lc.run.dirty = proj, false
	case errors.Is(err, errRunRecordGone), errors.Is(err, chat.ErrTombstoned):
		slog.Debug("run list not written: the chat has no record", "chat_id", lc.chatID, "error", err)
		lc.run.dirty = false
	default:
		slog.Warn("run list write failed; the next change carries it", "chat_id", lc.chatID, "error", err)
		lc.run.dirty = true
	}
}

// orderFactsLocked gathers the lifecycle's facts the list's operations read. Caller holds mu.
func (lc *chatLifecycle) orderFactsLocked() orderFacts {
	r := lc.reg
	may := lc.kas.may()
	return orderFacts{
		reserve: func(e *runEntry, stamp uint64) (command.Reservation, takeAnswer) {
			a := r.reserveLocked(lc, sourceOfEntry(e), stamp, messageIDOfEntry(e))
			switch {
			case a.gone:
				return command.Reservation{}, takeGone
			case a.verdict == gateClosedSince:
				return command.Reservation{}, takeClosed
			case a.verdict == gateWait:
				lc.parkGateLocked(a.idle)
				return command.Reservation{}, takeWait
			case a.res.Token == 0:
				return command.Reservation{}, takeWait
			}
			return a.res, takeGranted
		},
		unsent:       func() int { return lc.run.unsent },
		newMessageID: func() string { return "m-" + newMessageID() },
		stamp:        lc.stamp,
		nextSeq:      lc.nextSeq,
		stopSeq:      lc.stopSeq,
		stops:        len(lc.stops),
		admit:        may.admit,
		sessionCall:  may.sessionCall,
	}
}

func sourceOfEntry(e *runEntry) marotte.TurnOpenSource {
	switch e.kind {
	case marotte.RunKindRetry:
		return marotte.TurnSourceEmptyRetry
	case marotte.RunKindRevert:
		return marotte.TurnSourceRevert
	case marotte.RunKindTurn, marotte.RunKindRow, marotte.RunKindUnread, marotte.RunKindCompaction, marotte.RunKindResume:
	}
	return marotte.TurnSourcePrompt
}

func messageIDOfEntry(e *runEntry) string {
	if e.row != nil {
		return e.row.ID
	}
	return e.id
}

// parkGateLocked waits out a held close's gate on one goroutine per chat, then runs the taker again. Caller holds mu.
func (lc *chatLifecycle) parkGateLocked(idle <-chan struct{}) {
	if lc.gatePark || idle == nil || lc.reg.runHost == nil {
		return
	}
	lc.gatePark = true
	lc.reg.runHost.spawnRun(func() {
		select {
		case <-idle:
		case <-lc.ctx.Done():
		}
		lc.mu.Lock()
		lc.gatePark = false
		lc.wakeLocked()
		lc.mu.Unlock()
	})
}

// loadRunLocked seeds a new life's list from the chat's stored one. Caller holds r.mu.
func (r *turnRegistry) loadRunLocked(lc *chatLifecycle) {
	if r.runHost == nil || isRunChat(lc.chatID) {
		return
	}
	stored, version, ok := r.runHost.storedRun(lc.chatID)
	if !ok {
		return
	}
	lc.run.load(marotte.SanitizeRun(stored, false), stored, version)
	lc.runWritten = version
}

// endRunLocked is the run list's part of the life's end: every waiter is answered, and the list the next life
// starts from is recorded for writeFinalRun. Caller holds mu.
func (lc *chatLifecycle) endRunLocked() {
	final := lc.run.endLife()
	// Per entry, so an empty list matches whether it is stored as nil or as an empty slice.
	same := slices.EqualFunc(final, lc.run.stored, func(a, b marotte.RunEntry) bool { return reflect.DeepEqual(a, b) })
	if same && !lc.run.dirty {
		return
	}
	lc.runFinal, lc.runFinalOwed = final, true
}

// writeFinalRun stores the list an ended life left, once, as a compare-and-set on the version this life last wrote,
// so a later life's write is never overwritten. Called with no lock held.
func (lc *chatLifecycle) writeFinalRun() {
	lc.mu.Lock()
	final, owed, written := lc.runFinal, lc.runFinalOwed, lc.runWritten
	lc.runFinal, lc.runFinalOwed = nil, false
	lc.mu.Unlock()
	if !owed || lc.reg == nil || lc.reg.runHost == nil || isRunChat(lc.chatID) {
		return
	}
	err := lc.reg.runHost.writeRun(lc.chatID, func(c *marotte.Chat) bool {
		if c.RunVersion != written {
			return false
		}
		c.Run = final
		c.RunVersion++
		return true
	})
	if err != nil && !errors.Is(err, errRunRecordGone) && !errors.Is(err, chat.ErrTombstoned) {
		slog.Warn("the run list a closed chat left could not be written", "chat_id", lc.chatID, "error", err)
	}
}

func (bc *bridgeCoordinator) storedRun(chatID marotte.ChatID) ([]marotte.RunEntry, uint64, bool) {
	c, ok := bc.chatStore.Get(context.Background(), chatID)
	if !ok {
		return nil, 0, false
	}
	return slices.Clone(c.Run), c.RunVersion, true
}

func (bc *bridgeCoordinator) writeRun(chatID marotte.ChatID, mutate func(c *marotte.Chat) bool) error {
	gone := false
	_, err := bc.chatStore.Mutate(durable.Context(context.Background()), chatID, func(c *marotte.Chat, exists bool) bool {
		if !exists {
			gone = true
			return false
		}
		return mutate(c)
	})
	if err == nil && gone {
		return errRunRecordGone
	}
	return err
}

func (bc *bridgeCoordinator) toast(chatID marotte.ChatID, code marotte.ErrorCode, detail string) {
	if bc.autoCompact != nil && bc.autoCompact.toast != nil {
		bc.autoCompact.toast(context.Background(), chatID, code, detail)
	}
}

// startEntry carries out one take: a revert or a summary is its caller's (TakeIdle), a compaction's start runs here,
// and every other turn-kind entry goes to the command package's launcher. Caller holds lc.mu.
func (bc *bridgeCoordinator) startEntry(lc *chatLifecycle, e *runEntry) {
	switch {
	case e.kind == marotte.RunKindRevert, e.origin == marotte.RunFromSummary:
		return
	case e.kind == marotte.RunKindCompaction:
		bc.spawnRun(func() { bc.runCompaction(lc, e.id) })
		return
	}
	l := &command.EntryLaunch{
		Kind: e.kind, Replaces: e.replaces, Res: e.res, Awaited: e.opts.awaited,
		Fence: command.TurnFence{Epoch: lc.epoch, Seq: math.MaxUint64},
	}
	if e.row != nil {
		l.Row = *e.row
	}
	if e.opts.composer {
		id := e.id
		l.Claim = func() bool {
			lc.mu.Lock()
			defer lc.mu.Unlock()
			return lc.run.claimComposer(id)
		}
	}
	bc.spawnRun(func() {
		var (
			turnID string
			err    = errLaunchUnwired
		)
		if bc.launchEntry != nil {
			turnID, err = bc.launchEntry(context.Background(), lc.chatID, l)
		}
		bc.entryLaunched(lc, e.id, l.Res.Token, turnID, err)
	})
}

var errLaunchUnwired = errors.New("agent: no launcher for the chat's run list")

// spawnRun runs fn on the process's in-flight count, or on a bare goroutine with no lifetime (tests).
func (bc *bridgeCoordinator) spawnRun(fn func()) {
	if bc.lifecycle == nil {
		go fn()
		return
	}
	if !bc.lifecycle.goUnlessDraining(fn) {
		slog.Debug("run list: a take was not started, the server is shutting down")
	}
}

// spawnRole runs a Bot role keeper's set_mode on inflight unless Shutdown began (chatLifecycle.sendRoleLocked).
func (bc *bridgeCoordinator) spawnRole(work func()) bool {
	if bc.lifecycle == nil {
		go work()
		return true
	}
	return bc.lifecycle.goUnlessDraining(work)
}

// entryLaunched applies a launch's result to the list (runList.launched). A tangent delivery opened with a receipt
// hold its submitter no longer waits on releases that hold here; a refused row whose submitter still waits on an
// ended life is removed from the record too, so the next life never sends the text the client kept as its draft.
func (bc *bridgeCoordinator) entryLaunched(lc *chatLifecycle, id string, token uint64, turnID string, err error) {
	refusal := launchOpened
	switch {
	case turnID != "":
	case errors.Is(err, command.ErrTakeWithdrawn):
		refusal = launchWithdrawn
	default:
		refusal = launchClosed
	}
	if refusal == launchOpened {
		err = nil
	}
	lc.mu.Lock()
	_, e := lc.run.find(id)
	release := e != nil && refusal == launchOpened && e.opts.awaited && !e.submitter
	ended := lc.ended
	removed := lc.run.launched(id, token, turnID, refusal, err)
	if errors.Is(err, command.ErrUnreadGone) {
		if _, gone := lc.run.find(id); gone != nil {
			lc.run.remove(gone)
		}
	}
	lc.wakeLocked()
	lc.mu.Unlock()
	if release {
		bc.turns.release(lc.chatID, turnID)
	}
	if removed && ended {
		bc.dropRefusedRow(lc, id)
	}
}

// dropRefusedRow removes a row a closed chat's launch refused from the record, then from a life that loaded it since.
func (bc *bridgeCoordinator) dropRefusedRow(ended *chatLifecycle, id string) {
	for range 3 {
		err := bc.writeRun(ended.chatID, func(c *marotte.Chat) bool {
			n := len(c.Run)
			c.Run = slices.DeleteFunc(slices.Clone(c.Run), func(e marotte.RunEntry) bool { return e.ID == id })
			if len(c.Run) == n {
				return false
			}
			c.RunVersion++
			return true
		})
		if err != nil && !errors.Is(err, errRunRecordGone) {
			slog.Warn("a refused row could not be removed from the closed chat", "chat_id", ended.chatID, "error", err)
		}
		next, ok := bc.turns.lookup(ended.chatID)
		if !ok || next == ended {
			return
		}
		next.mu.Lock()
		if next.ended {
			next.mu.Unlock()
			continue
		}
		if _, e := next.run.find(id); e != nil && !e.live() {
			next.run.remove(e)
			next.wakeLocked()
		}
		next.mu.Unlock()
		return
	}
}

// runLife is the chat's lifecycle for a request whose intent began at stamp: it waits out a held close up to
// command.AdmissionWait, then answers command.ErrSubmitStarting; a close committed since stamp answers
// marotte.ErrChatClosedDuringCall.
func (bc *bridgeCoordinator) runLife(ctx context.Context, chatID marotte.ChatID, stamp uint64) (*chatLifecycle, error) {
	wait, cancel := context.WithTimeout(ctx, command.AdmissionWait)
	defer cancel()
	lc, err := bc.turns.lifecycleAt(wait, chatID, stamp)
	switch {
	case err == nil:
		return lc, nil
	case errors.Is(err, marotte.ErrChatClosedDuringCall):
		return nil, err
	case ctx.Err() != nil:
		return nil, ctx.Err()
	}
	return nil, command.ErrSubmitStarting
}

// sendMode is what s does beside a steerable turn: its own mode, else the instance's interrupt_mode setting.
func (bc *bridgeCoordinator) sendMode(ctx context.Context, s *command.Submission) marotte.InterruptMode {
	if s.Mode.Valid() {
		return s.Mode
	}
	return settings.InterruptModeIn(ctx, bc.lifecycle.configDir)
}

// Submit places a sent message on the chat's run list (command.runQueue.Submit).
func (bc *bridgeCoordinator) Submit(ctx context.Context, chatID marotte.ChatID, stamp uint64, s *command.Submission) (command.SubmitAnswer, error) {
	lc, err := bc.runLife(ctx, chatID, stamp)
	if err != nil {
		return command.SubmitAnswer{}, err
	}
	mode := bc.sendMode(ctx, s)
	lc.mu.Lock()
	if lc.ended {
		lc.mu.Unlock()
		return command.SubmitAnswer{}, marotte.ErrChatClosedDuringCall
	}
	out, e, err := lc.submitLocked(stamp, s, mode)
	lc.wakeLocked()
	out.Run = lc.runViewLocked()
	lc.mu.Unlock()
	if err != nil || e == nil {
		return out, err
	}
	return lc.awaitLaunch(ctx, e)
}

// submitLocked decides a send by VerbFor and places it, answering the entry whose launch the caller waits on (nil
// when it answers at once). Caller holds mu.
func (lc *chatLifecycle) submitLocked(stamp uint64, s *command.Submission, mode marotte.InterruptMode) (command.SubmitAnswer, *runEntry, error) {
	row := s.Row
	if _, have := lc.run.find(row.ID); have != nil {
		as := command.SubmitQueued
		if have.live() {
			as = command.SubmitStarted
		}
		return command.SubmitAnswer{As: as}, nil, nil
	}
	verb, err := submitVerb(lc.run.project(), s, mode)
	switch {
	case err != nil:
		return command.SubmitAnswer{}, nil, err
	case verb == marotte.SendSteer:
		return command.SubmitAnswer{Steer: true}, nil, nil
	}
	e := &runEntry{
		id: row.ID, kind: marotte.RunKindRow, origin: s.Origin, row: &row, retry: s.Retry,
		opts: launchOpts{model: row.Model, composer: s.Composer, awaited: s.Awaited},
	}
	waits := s.Wait && verb == marotte.SendPrompt
	f := lc.orderFactsLocked()
	a := lc.run.submit(e, &f, stamp, waits)
	switch {
	case a.refused == takeClosed:
		return command.SubmitAnswer{}, nil, marotte.ErrChatClosedDuringCall
	case a.refused == takeGone:
		return command.SubmitAnswer{}, nil, command.ErrChatNotFound
	case a.taken && waits:
		return command.SubmitAnswer{}, e, nil
	}
	return command.SubmitAnswer{As: command.SubmitQueued, Settle: s.Composer}, nil, nil
}

// submitVerb is where s goes on a list holding entries (VerbFor, with steering ruled out by NoSteer), or why it is
// refused there.
func submitVerb(entries []marotte.RunEntry, s *command.Submission, mode marotte.InterruptMode) (marotte.SendVerb, error) {
	verb := marotte.VerbFor(entries, mode, s.Kind)
	if verb == marotte.SendSteer && s.NoSteer {
		verb = marotte.SendQueue
	}
	switch verb {
	case marotte.SendSteer:
		if s.PromptOnly {
			return verb, command.ErrSubmitPromptOnly
		}
	case marotte.SendPrompt:
		if s.Kind == marotte.SendQueuePrompt {
			return verb, command.ErrSubmitNoTurn
		}
	case marotte.SendQueue:
		return verb, queueRefusal(entries, &s.Row)
	}
	return verb, nil
}

// queueRefusal is why row may not queue behind entries: past the draft cap behind a running turn of marotte's, or
// past the queue's caps. Nil when it may.
func queueRefusal(entries []marotte.RunEntry, row *marotte.QueuedPrompt) error {
	for _, h := range marotte.LivePrefix(entries) {
		if !h.Kind.TurnKind() || h.Origin == marotte.RunFromRun ||
			(h.State != marotte.RunRunning && h.State != marotte.RunStarting) {
			continue
		}
		if len(row.Text) > marotte.MaxDraftBytes {
			return command.ErrSubmitTooBig
		}
		break
	}
	return command.RowCapsRefusal(marotte.RunRows(entries), row)
}

// awaitLaunch waits for submit's own take to open its turn, up to command.AdmissionWait; past it, one section
// answers queued unless the launch answered first.
func (lc *chatLifecycle) awaitLaunch(ctx context.Context, e *runEntry) (command.SubmitAnswer, error) {
	lc.mu.Lock()
	w := e.launch
	lc.mu.Unlock()
	timer := time.NewTimer(command.AdmissionWait)
	defer timer.Stop()
	select {
	case <-w.ch:
	case <-timer.C:
	case <-ctx.Done():
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if !w.closed {
		settle := answerQueued(e)
		return command.SubmitAnswer{As: command.SubmitQueued, Settle: settle, Run: lc.runViewLocked()}, nil
	}
	view := lc.runViewLocked()
	switch {
	case w.err != nil:
		return command.SubmitAnswer{Run: view}, w.err
	case w.queued:
		return command.SubmitAnswer{As: command.SubmitQueued, Settle: w.composer, Run: view}, nil
	}
	return command.SubmitAnswer{As: command.SubmitStarted, TurnID: w.turnID, Run: view}, nil
}

// runViewLocked is the list as the header draws it. Caller holds mu.
func (lc *chatLifecycle) runViewLocked() marotte.RunView {
	return marotte.RunViewOf(lc.run.project(), lc.run.version)
}

// RunView is the chat's run list as the header draws it: the live list, else the stored one.
func (bc *bridgeCoordinator) RunView(ctx context.Context, chatID marotte.ChatID) marotte.RunView {
	if lc, ok := bc.turns.lookup(chatID); ok {
		lc.mu.Lock()
		defer lc.mu.Unlock()
		if !lc.ended {
			return lc.runViewLocked()
		}
	}
	c, ok := bc.chatStore.Get(ctx, chatID)
	if !ok {
		return marotte.RunViewOf(nil, 0)
	}
	return marotte.RunViewOf(c.Run, c.RunVersion)
}

// wakeOnChangeLocked wakes the chat's waiters only when a look changed the list. A waiter that woke on every look of
// its own would close the channel it is about to wait on, and two such waiters would wake each other in turn.
// Caller holds mu.
func (lc *chatLifecycle) wakeOnChangeLocked() {
	if lc.run.owesEffects() {
		lc.wakeLocked()
	}
}

// takeIdleLocked is one look at an idle take of e: its answer, and false while it waits. Caller holds mu.
func (lc *chatLifecycle) takeIdleLocked(e *runEntry, stamp uint64) (command.IdleAdmission, bool) {
	if lc.ended {
		return command.IdleClosed, true
	}
	f := lc.orderFactsLocked()
	lc.run.intentRefused = takeGranted
	a := lc.run.takeIdle(e, &f, stamp)
	refused := lc.run.intentRefused
	lc.wakeOnChangeLocked()
	switch {
	case a == idleGranted:
		return command.IdleAcquired, true
	case a == idleBusy:
		return command.IdleTurnOpen, true
	case refused == takeClosed || refused == takeGone:
		lc.run.idleExpired(e)
		lc.wakeLocked()
		if refused == takeGone {
			return command.IdleChatGone, true
		}
		return command.IdleClosed, true
	}
	return 0, false
}

// TakeIdle takes the chat for a revert or a summary, which run now or not at all (command.runQueue.TakeIdle).
func (bc *bridgeCoordinator) TakeIdle(
	ctx context.Context, chatID marotte.ChatID, kind command.IdleKind, stamp uint64, wait time.Duration,
) (command.IdleAdmission, command.Reservation, string) {
	deadline, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	lc, err := bc.turns.lifecycleAt(deadline, chatID, stamp)
	switch {
	case errors.Is(err, marotte.ErrChatClosedDuringCall):
		return command.IdleClosed, command.Reservation{}, ""
	case err != nil:
		return command.IdleHeld, command.Reservation{}, ""
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	e := &runEntry{kind: marotte.RunKindRevert, origin: marotte.RunFromRewind}
	if kind == command.IdleSummary {
		e.kind, e.origin = marotte.RunKindTurn, marotte.RunFromSummary
	}
	e.id = lc.run.mint(string(e.origin) + "-")
	for {
		if out, done := lc.takeIdleLocked(e, stamp); done {
			if out != command.IdleAcquired {
				return out, command.Reservation{}, ""
			}
			return out, e.res, e.id
		}
		changed := lc.changed
		lc.mu.Unlock()
		select {
		case <-changed:
			lc.mu.Lock()
		case <-deadline.Done():
			lc.mu.Lock()
			granted := lc.run.idleExpired(e)
			lc.wakeLocked()
			if granted {
				return command.IdleAcquired, e.res, e.id
			}
			return command.IdleHeld, command.Reservation{}, ""
		}
	}
}

// SummaryLaunched reports a granted summary's open, or its refusal before the open (command.runQueue).
func (bc *bridgeCoordinator) SummaryLaunched(chatID marotte.ChatID, entryID string, res command.Reservation, turnID string, err error) {
	lc, ok := bc.turns.lookup(chatID)
	if !ok || entryID == "" {
		return
	}
	bc.entryLaunched(lc, entryID, res.Token, turnID, err)
}

// SendNow moves a row to the front and stops the turn the press was drawn against (command.runQueue.SendNow).
func (bc *bridgeCoordinator) SendNow(ctx context.Context, chatID marotte.ChatID, id, stop string, hasStop bool) (command.RunStop, error) {
	lc, err := bc.runLife(ctx, chatID, bc.LifeStamp())
	if err != nil {
		return command.RunStop{}, err
	}
	lc.mu.Lock()
	h, err := lc.run.sendNow(id, stop, hasStop)
	var rs command.RunStop
	var post func()
	switch {
	case errors.Is(err, errRowHeld):
		err = command.ErrRowHeld
	case errors.Is(err, errRowNotQueued), errors.Is(err, errRowSending):
		err = nil
	case h != nil:
		token := bc.turns.stopTokens.Add(1)
		rs, post = bc.turns.carryStopLocked(lc, lc.run.readerStop(h.id, token), token, marotte.TurnStopReader)
	}
	lc.wakeLocked()
	lc.mu.Unlock()
	if post != nil {
		post()
	}
	return rs, err
}

// ClearNext undoes a send-now whose stop was not delivered.
func (bc *bridgeCoordinator) ClearNext(chatID marotte.ChatID, id string) {
	lc, ok := bc.turns.lookup(chatID)
	if !ok {
		return
	}
	lc.mu.Lock()
	lc.run.clearNext(id)
	lc.wakeLocked()
	lc.mu.Unlock()
}

// Unqueue removes a waiting row; an unknown id is success.
func (bc *bridgeCoordinator) Unqueue(ctx context.Context, chatID marotte.ChatID, id string) error {
	lc, err := bc.runLife(ctx, chatID, bc.LifeStamp())
	if err != nil {
		return err
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	err = lc.run.unqueue(id)
	lc.wakeLocked()
	if errors.Is(err, errRowSending) {
		return command.ErrRowSending
	}
	return nil
}

// Stop stops the live entry target names, or RunView.Cancel's with target "" (command.runQueue.Stop).
func (bc *bridgeCoordinator) Stop(_ context.Context, chatID marotte.ChatID, target string) command.RunStop {
	lc, ok := bc.turns.lookup(chatID)
	if !ok {
		return command.RunStop{}
	}
	lc.mu.Lock()
	token := bc.turns.stopTokens.Add(1)
	rs, post := bc.turns.carryStopLocked(lc, lc.run.readerStop(target, token), token, marotte.TurnStopReader)
	lc.wakeLocked()
	lc.mu.Unlock()
	if post != nil {
		post()
	}
	return rs
}

// carryStopLocked does a stop plan's lifecycle part in its section: a withdrawn take's reservation is given back
// (post runs its idle report once mu is released), an opened unsent turn is kept unsent, and a running turn or
// kiro-cli's run gets the stop record, under cause, its session/cancel goes out under. Caller holds lc.mu.
func (r *turnRegistry) carryStopLocked(
	lc *chatLifecycle, plan stopPlan, token uint64, cause marotte.TurnStopCause,
) (rs command.RunStop, post func()) {
	switch plan.arm {
	case stopNothing:
		return command.RunStop{Arm: command.StopNothing}, nil
	case stopWithdraw:
		post := r.releaseReservationLocked(lc, plan.res)
		return command.RunStop{Arm: command.StopQuiet, Stop: &command.TurnStop{Chat: lc.chatID}}, post
	case stopUnsent:
		stop := r.requestStopLocked(lc, token, cause)
		if t := lc.liveLocked(plan.entry.turnID); t != nil && t.awaitsSend() && !t.finalizing {
			t.stopped = true
		}
		return command.RunStop{Arm: command.StopQuiet, Stop: stop}, nil
	case stopRunning, stopRun, stopSession:
	}
	return command.RunStop{Arm: command.StopCancel, Stop: r.requestStopLocked(lc, token, cause)}, nil
}

// StopUndone restores what a stop whose session/cancel was not delivered marked.
func (bc *bridgeCoordinator) StopUndone(chatID marotte.ChatID, stop *command.TurnStop) {
	lc, ok := bc.turns.lookup(chatID)
	if !ok || stop == nil {
		return
	}
	lc.mu.Lock()
	lc.run.stopUndone(stop.Token, func(turnID string) bool { return lc.liveLocked(turnID) != nil })
	lc.wakeLocked()
	lc.mu.Unlock()
}

// runFactsChanged applies the steer queue's pending ends and unsent rows to the chat's list (runList.endsChanged).
// The steer record's lock is never taken under lc.mu: the death bits are read first, then the record, and a read
// with other bits than the list's is made again until one matches, since only an applied read sets the unsent count.
func (bc *bridgeCoordinator) runFactsChanged(chatID marotte.ChatID) {
	lc, ok := bc.turns.lookup(chatID)
	if !ok || bc.steerFacts == nil {
		return
	}
	drained := false
	for {
		ends, unsent, seq := bc.steerFacts(chatID, drained)
		live := bc.bridgeLive(chatID)
		lc.mu.Lock()
		f := lc.orderFactsLocked()
		reread, d := lc.run.endsChanged(seq, ends, unsent, drained, live, &f)
		lc.wakeLocked()
		lc.mu.Unlock()
		if !reread {
			return
		}
		drained = d
	}
}

// closeEndsResolved records a close's steer ends on its closing entry, with the steer queue's facts read first
// (outside lc.mu, as in runFactsChanged) and read again while the list holds other death bits than the read's.
func (bc *bridgeCoordinator) closeEndsResolved(lc *chatLifecycle, turnID string, ends command.EndFacts) {
	drained := ends.Death && !ends.Undrained
	read := drained
	for {
		var (
			n      int
			unsent []string
			seq    uint64
		)
		if bc.steerFacts != nil {
			n, unsent, seq = bc.steerFacts(lc.chatID, read)
		}
		live := bc.bridgeLive(lc.chatID)
		lc.mu.Lock()
		f := lc.orderFactsLocked()
		if bc.steerFacts != nil {
			reread, d := lc.run.endsChanged(seq, n, unsent, read, live, &f)
			if reread {
				lc.mu.Unlock()
				read = d
				continue
			}
		}
		if _, e := lc.run.byTurn(turnID); e != nil {
			lc.run.closeEndsResolved(e.id, drained, live, &f)
		}
		lc.wakeLocked()
		lc.mu.Unlock()
		return
	}
}

// awaitReleased waits until the entry whose turn is turnID has given its reservation back, or is gone: a holder a
// close's steer resolve then sees is another turn, not this close's prompt. It gives up at budget or ctx end.
func (lc *chatLifecycle) awaitReleased(ctx context.Context, turnID string, budget time.Duration) {
	timer := time.NewTimer(budget)
	defer timer.Stop()
	for {
		lc.mu.Lock()
		_, e := lc.run.byTurn(turnID)
		done, changed := e == nil || e.released, lc.changed
		lc.mu.Unlock()
		if done {
			return
		}
		select {
		case <-changed:
		case <-timer.C:
			return
		case <-ctx.Done():
			return
		}
	}
}

// endTail is a close tail's end: the close's KAS hold is released and the closing entry learns its tail ended, in one
// section, so nothing is taken between them. A release KAS refused (it reported a run) answers false and the tail
// waits again; a stand-down ends the entry outright. An ended life's tail hands nothing on: its list was written at
// its end.
func (lc *chatLifecycle) endTail(turnID string, seq uint64, standDown bool) bool {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.ended {
		return true
	}
	in := kasReleaseOwn
	if standDown {
		in = kasStandDownOwn
	}
	_, released := lc.kas.step(in, seq)
	if !standDown && !released {
		lc.wakeLocked()
		return false
	}
	if _, e := lc.run.byTurn(turnID); e != nil {
		lc.run.tailEnded(e.id, standDown)
	}
	lc.wakeLocked()
	return true
}

// holdCarried absorbs a shutdown's unread steers into the chat's carried row, on its live list when one is
// registered, else on the stored list by the version rule.
func (bc *bridgeCoordinator) holdCarried(ctx context.Context, chatID marotte.ChatID, row *marotte.QueuedPrompt) error {
	if lc, ok := bc.turns.lookup(chatID); ok {
		lc.mu.Lock()
		if !lc.ended {
			lc.run.holdCarried(row)
			lc.flushRunLocked()
			lc.mu.Unlock()
			return nil
		}
		lc.mu.Unlock()
	}
	_, err := bc.chatStore.Mutate(durable.Context(ctx), chatID, func(c *marotte.Chat, exists bool) bool {
		if !exists {
			return false
		}
		var l runList
		l.load(c.Run, c.Run, c.RunVersion)
		l.holdCarried(row)
		c.Run = l.project()
		c.RunVersion++
		return true
	})
	return err
}

// closeRunEntryLocked places a turn's close on the list (runList.turnClosed), with the close's facts finalizeTurn
// recorded on the turn. Caller holds mu.
func (lc *chatLifecycle) closeRunEntryLocked(t *activeTurn) {
	c := listClose{}
	if t.closeFacts != nil {
		c = *t.closeFacts
	}
	c.turnID, c.seq = t.ID, t.Seq
	f := lc.orderFactsLocked()
	lc.run.turnClosed(c, &f)
}
