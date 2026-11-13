package marotte

import "slices"

// RunEntryKind is what a run-order entry runs.
type RunEntryKind string

const (
	// RunKindTurn is a turn no row started: kiro-cli's own (origin wire or run), or a tangent's summary.
	RunKindTurn RunEntryKind = "turn"
	// RunKindRow is a queued message.
	RunKindRow RunEntryKind = "row"
	// RunKindRetry is the one re-send of a prompt whose turn produced nothing.
	RunKindRetry RunEntryKind = "retry"
	// RunKindCompaction is one compaction.
	RunKindCompaction RunEntryKind = "compaction"
	// RunKindRevert is a rewind's checkpoint revert.
	RunKindRevert RunEntryKind = "revert"
	// RunKindResume is marotte's prompt that continues a turn the failsafe stopped, once its compaction ran.
	RunKindResume RunEntryKind = "resume"
)

// TurnKind reports a kind whose take opens a turn.
func (k RunEntryKind) TurnKind() bool {
	return k == RunKindTurn || k == RunKindRow || k == RunKindRetry || k == RunKindResume
}

// RunEntryState is where an entry stands in the run order.
type RunEntryState string

const (
	// RunPending waits its turn.
	RunPending RunEntryState = "pending"
	// RunPaused is a row a stop or a broken close left: a broken close's waits for the next clean turn end or a
	// send-now, a reader's stop's (StopPaused) for the reader's next send, a send-now or Resume.
	RunPaused RunEntryState = "paused"
	// RunHeld is a row from before a restart, or a carried row: shown, never taken.
	RunHeld RunEntryState = "held"
	// RunOffered is a row handed to kiro-cli as a steer, its answer not in yet.
	RunOffered RunEntryState = "offered"
	// RunInKAS is a row kiro-cli queued as a steer and has not read yet.
	RunInKAS RunEntryState = "in_kas"
	// RunStarting is taken and granted, its call not written yet.
	RunStarting RunEntryState = "starting"
	// RunRunning is live and written.
	RunRunning RunEntryState = "running"
	// RunStopping is a turn the reader stopped, still open.
	RunStopping RunEntryState = "stopping"
	// RunClosing is a closed turn whose close tail still runs.
	RunClosing RunEntryState = "closing"
)

// Live reports a state that holds the chat: live entries form the list's prefix.
func (s RunEntryState) Live() bool {
	return s == RunStarting || s == RunRunning || s == RunStopping || s == RunClosing
}

// RunEntryOrigin is the path that put an entry on the list.
type RunEntryOrigin string

// The paths that put an entry on the list: a message's (prompt to carried), kiro-cli's (wire, run), a compaction's
// (request to context_limit), a rewind's, the failsafe's (its compaction and its resume), and the notify door's.
const (
	RunFromPrompt       RunEntryOrigin = "prompt"
	RunFromQueue        RunEntryOrigin = "queue"
	RunFromTangent      RunEntryOrigin = "tangent"
	RunFromSummary      RunEntryOrigin = "summary"
	RunFromRetry        RunEntryOrigin = "retry"
	RunFromWire         RunEntryOrigin = "wire"
	RunFromRun          RunEntryOrigin = "run"
	RunFromCarried      RunEntryOrigin = "carried"
	RunFromRequest      RunEntryOrigin = "request"
	RunFromThreshold    RunEntryOrigin = "threshold"
	RunFromTurnEnd      RunEntryOrigin = "turn_end"
	RunFromContextLimit RunEntryOrigin = "context_limit"
	RunFromRewind       RunEntryOrigin = "rewind"
	RunFromFailsafe     RunEntryOrigin = "failsafe"
	RunFromNotify       RunEntryOrigin = "notify"
)

// RunWant is how a waiting row may be delivered, fixed when it is placed.
type RunWant string

const (
	// RunWantEither may be steered into a running turn or taken as its own turn.
	RunWantEither RunWant = "either"
	// RunWantPrompt is only ever taken as its own turn.
	RunWantPrompt RunWant = "prompt"
)

// RunEntry is one entry of Chat.Run, in run order: the live entries first, then what runs after them.
type RunEntry struct {
	// Row is a row or retry entry's message.
	Row    *QueuedPrompt  `json:"row,omitempty"`
	ID     string         `json:"id"`
	Kind   RunEntryKind   `json:"kind"`
	State  RunEntryState  `json:"state"`
	Origin RunEntryOrigin `json:"origin"`
	// Turn is the turn id once the entry's turn opened: the transcript then shows its message.
	Turn string  `json:"turn,omitempty"`
	Want RunWant `json:"want,omitempty"`
	// Next marks the row send_queued_now moved to the front.
	Next bool `json:"next,omitempty"`
	// Unread marks a row a turn end left unread in kiro-cli's steer buffer: one sender's run of them is one prompt.
	Unread    bool `json:"unread,omitempty"`
	Steerable bool `json:"steerable,omitempty"`
	// Yields marks a fold-opened wire turn, which owes no close: a turn-kind take or a compaction goes beside it.
	Yields bool `json:"yields,omitempty"`
	// Admits, with Yields, is whether kiro-cli's state admits a take beside it now.
	Admits bool `json:"admits,omitempty"`
	// StopPaused marks a paused row the reader's stop paused: the queue is paused until the reader sends, sends a row
	// now or resumes it.
	StopPaused bool `json:"stop_paused,omitempty"`
}

func (e *RunEntry) kiroCLI() bool {
	return e.Kind == RunKindTurn && (e.Origin == RunFromWire || e.Origin == RunFromRun)
}

// SendVerb is what a plain send does on the list as it stands.
type SendVerb string

// The verbs: taken now, steered into the running turn, or queued behind what runs.
const (
	SendPrompt SendVerb = "prompt"
	SendSteer  SendVerb = "steer"
	SendQueue  SendVerb = "queue"
)

// SendKind is the shape of a send VerbFor places.
type SendKind uint8

const (
	// SendPlain is a composer send with no label.
	SendPlain SendKind = iota
	// SendLabelled carries display text, which _session/steer cannot.
	SendLabelled
	// SendQueuePrompt is queue_prompt.
	SendQueuePrompt
)

// LivePrefix returns the list's live entries, which form its prefix.
func LivePrefix(entries []RunEntry) []RunEntry {
	n := 0
	for n < len(entries) && entries[n].State.Live() {
		n++
	}
	return entries[:n]
}

// sendProbeID names VerbFor's hypothetical send, an id no entry carries.
const sendProbeID = "\x00send"

// VerbFor is what a user's send of kind does on the list as it stands, by Deliver: prompt (taken now), steer (into
// the running turn, once its prompt is written and the steer offered ahead is answered) or queue. It reads no
// HeadFacts but the mode, so the header projected from a stored list answers it too; a held close answers as if
// resolved, since the send is taken when it is. A queue_prompt answered prompt is a send with nothing to queue
// behind, which the caller refuses no_turn.
func VerbFor(entries []RunEntry, mode InterruptMode, kind SendKind) SendVerb {
	send := RunEntry{ID: sendProbeID, Kind: RunKindRow, State: RunPending, Want: WantFor(entries, mode, kind)}
	at := insertAt(entries)
	with := slices.Insert(slices.Clone(entries), at, send)
	if steerBound(with, at) {
		return SendSteer
	}
	dl := Deliver(with, HeadFacts{Mode: mode})
	if dl.Kind != DeliverPrompt || dl.IDs[0] != sendProbeID ||
		(kind == SendQueuePrompt && len(LivePrefix(entries)) > 0) {
		return SendQueue
	}
	return SendPrompt
}

// WantFor is the delivery a user's send of kind placed on entries now is fixed to: either only for a plain send
// meeting a running turn that takes steers, so a send queued behind anything else stays a turn of its own.
func WantFor(entries []RunEntry, mode InterruptMode, kind SendKind) RunWant {
	live := LivePrefix(entries)
	if kind == SendPlain && len(live) == 1 && steerHead(&live[0], HeadFacts{Mode: mode, Prompted: true}) {
		return RunWantEither
	}
	return RunWantPrompt
}

// RunWaitAfter names what an entry waits for.
type RunWaitAfter string

// What an entry waits for: it runs or starts, it goes next, after the compaction ahead, after Turns turn ends, never
// by itself (held), or for the reader's send or Resume (paused by the reader's stop).
const (
	waitRunning    RunWaitAfter = "running"
	waitStarting   RunWaitAfter = "starting"
	waitNext       RunWaitAfter = "next"
	WaitCompaction RunWaitAfter = "compaction"
	waitTurns      RunWaitAfter = "turns"
	waitHeld       RunWaitAfter = "held"
	waitPaused     RunWaitAfter = "paused"
)

// RunWait is when an entry runs, counted from the entries ahead of it.
type RunWait struct {
	After RunWaitAfter `json:"after"`
	// Turns is the turn ends ahead, for "turns".
	Turns int `json:"turns,omitempty"`
	// This marks one turn that is the running head's.
	This bool `json:"this,omitempty"`
}

// SendNowKind is what a row's send-now arrow does when pressed.
type SendNowKind string

const (
	// sendNowStop stops the running turn and sends the row next.
	sendNowStop SendNowKind = "stop"
	// SendNowStopThenCompaction stops the running turn; the row goes after the compaction pending behind it.
	SendNowStopThenCompaction SendNowKind = "stop_then_compaction"
	// SendNowAfterCompaction stops nothing: the row goes first once the compaction ends.
	SendNowAfterCompaction SendNowKind = "after_compaction"
	// sendNowNow stops nothing: the row goes next once what is live ends.
	sendNowNow SendNowKind = "now"
)

// RunEntryView is one entry as the header draws it.
type RunEntryView struct {
	// SendNow is the row's arrow; absent: no arrow.
	SendNow SendNowKind `json:"send_now,omitempty"`
	RunEntry
	Wait RunWait `json:"wait"`
	// CarriesUnread marks the entry whose turn the awaits-prompt steers ride.
	CarriesUnread bool `json:"carries_unread,omitempty"`
	// SteerInFlight marks a user row the running turn takes as a steer once the turn's prompt is written and the one
	// steer offered is answered: drawn as a steer being sent, never as a queue row.
	SteerInFlight bool `json:"steer_in_flight,omitempty"`
}

// WaitingToSend reports a queue row: a row waiting to be sent as its own turn, never a steer offered, in kiro-cli or
// in flight.
func (v *RunEntryView) WaitingToSend() bool {
	return v.Kind == RunKindRow && (v.State == RunPending || v.State == RunPaused || v.State == RunHeld) &&
		!v.SteerInFlight
}

// RunView is the header's run order: each entry with its wait, and the composer's facts.
type RunView struct {
	// Send is VerbFor for a plain send.
	Send SendVerb `json:"send"`
	// Cancel is the entry id a Cancel with no target stops; empty: nothing to stop.
	Cancel  string         `json:"cancel,omitempty"`
	Entries []RunEntryView `json:"entries"`
	// NextWait is the wait a message queued now gets.
	NextWait RunWait `json:"next_wait"`
	// UnreadWait is where the chat's unsent steers go.
	UnreadWait RunWait `json:"unread_wait"`
	Version    uint64  `json:"version"`
}

// CancelTarget is the first live turn-kind entry starting or running: what a Cancel naming no target stops. It
// never names a compaction or a revert.
func CancelTarget(entries []RunEntry) string {
	for _, e := range LivePrefix(entries) {
		if e.Kind.TurnKind() && (e.State == RunStarting || e.State == RunRunning) {
			return e.ID
		}
	}
	return ""
}

// waitOrder is the list in the order its entries run: kiro-cli's turn under a running compaction runs before it,
// since kiro-cli refuses or aborts that compaction and it is requeued behind the turn.
func waitOrder(entries []RunEntry) []int {
	order := make([]int, len(entries))
	for i := range order {
		order[i] = i
	}
	live := LivePrefix(entries)
	if len(live) == 2 && live[0].Kind == RunKindCompaction && live[1].kiroCLI() {
		order[0], order[1] = 1, 0
	}
	return order
}

// ridesOf marks the entries that ride the running turn as steers: offered, in kiro-cli, or bound to it (steerBound).
func ridesOf(entries []RunEntry) []bool {
	rides := make([]bool, len(entries))
	for i := range entries {
		s := entries[i].State
		rides[i] = s == RunOffered || s == RunInKAS || steerBound(entries, i)
	}
	return rides
}

func waitAt(entries []RunEntry, order []int, pos int, rides []bool) RunWait {
	e := &entries[order[pos]]
	if w, ok := ownWait(e, rides[order[pos]]); ok {
		return w
	}
	if e.State == RunPending && pos > 0 && entries[order[pos-1]].Kind == RunKindCompaction {
		return RunWait{After: WaitCompaction}
	}
	paused := e.State == RunPaused
	n, liveCounted := turnsAhead(entries, order[:pos], rides, paused || !e.ByUser())
	if paused && n == 0 {
		n = 1
	}
	if n == 0 {
		return RunWait{After: waitNext}
	}
	w := RunWait{After: waitTurns, Turns: n}
	if n == 1 && liveCounted {
		w.This = true
	}
	return w
}

// ownWait is the wait e's own state fixes, whatever stands ahead of it: taken, live, held, riding the running turn, or
// paused by the reader's stop.
func ownWait(e *RunEntry, rides bool) (RunWait, bool) {
	switch {
	case e.State == RunStarting:
		return RunWait{After: waitStarting}, true
	case e.State.Live():
		return RunWait{After: waitRunning}, true
	case e.State == RunHeld:
		return RunWait{After: waitHeld}, true
	case rides:
		return RunWait{After: waitTurns, Turns: 1, This: true}, true
	case e.State == RunPaused && e.StopPaused:
		return RunWait{After: waitPaused}, true
	}
	return RunWait{}, false
}

// turnsAhead counts the turn ends ahead: a starting or running turn one each (liveCounted reports one), a pending
// one each, a paused one only when behindPaused (a paused entry, or a wake, which waits for the user's paused rows), a
// stopping or closing one none, since it runs nothing more, and a steer riding the running turn none.
func turnsAhead(entries []RunEntry, ahead []int, rides []bool, behindPaused bool) (n int, liveCounted bool) {
	// A fold turn ahead of a compaction is closed by it, so it runs no turn ahead of anything.
	live := LivePrefix(entries)
	foldAhead := len(live) == 2 && live[1].Kind == RunKindCompaction
	for _, j := range ahead {
		a := &entries[j]
		if !a.Kind.TurnKind() || (foldAhead && j == 0) || rides[j] {
			continue
		}
		switch a.State {
		case RunStarting, RunRunning:
			n++
			liveCounted = true
		case RunPending:
			n++
		case RunPaused:
			if behindPaused {
				n++
			}
		case RunHeld, RunStopping, RunClosing, RunOffered, RunInKAS:
		}
	}
	return n, liveCounted
}

func pendingCompactionFront(entries []RunEntry) bool {
	live := LivePrefix(entries)
	return len(live) < len(entries) && entries[len(live)].Kind == RunKindCompaction &&
		entries[len(live)].State == RunPending
}

func sendNowKind(entries []RunEntry) SendNowKind {
	live := LivePrefix(entries)
	compactionNext := pendingCompactionFront(entries)
	if len(live) == 1 {
		h := &live[0]
		switch {
		case h.Kind.TurnKind() && h.Origin != RunFromRun && (h.State == RunStarting || h.State == RunRunning):
			if compactionNext {
				return SendNowStopThenCompaction
			}
			return sendNowStop
		case h.Kind == RunKindCompaction:
			return SendNowAfterCompaction
		case (h.State == RunStopping || h.State == RunClosing) && compactionNext:
			return SendNowAfterCompaction
		}
		return sendNowNow
	}
	for _, e := range live {
		if e.Kind == RunKindCompaction {
			// A compaction running, or taken behind a fold turn.
			return SendNowAfterCompaction
		}
	}
	if len(live) == 0 && compactionNext {
		return SendNowAfterCompaction
	}
	return sendNowNow
}

// insertAt is where a new row goes: before the first paused row, so the list order is the send order.
func insertAt(entries []RunEntry) int {
	for i := len(LivePrefix(entries)); i < len(entries); i++ {
		if entries[i].State == RunPaused {
			return i
		}
	}
	return len(entries)
}

func waitIfInserted(entries []RunEntry, at int, e *RunEntry) RunWait {
	with := slices.Insert(slices.Clone(entries), at, *e)
	order := waitOrder(with)
	rides := ridesOf(with)
	for pos, i := range order {
		if i == at {
			return waitAt(with, order, pos, rides)
		}
	}
	return RunWait{After: waitNext}
}

// RunViewOf is the header's run order for a chat's list and its version. Its Send is what a plain send naming steer
// does, so steer means both halves are open to the composer.
func RunViewOf(entries []RunEntry, version uint64) RunView {
	v := RunView{
		Entries: make([]RunEntryView, len(entries)),
		Version: version,
		Send:    VerbFor(entries, InterruptSteer, SendPlain),
		Cancel:  CancelTarget(entries),
	}
	order := waitOrder(entries)
	rides := ridesOf(entries)
	arrow := sendNowKind(entries)
	carried := false
	for pos, i := range order {
		e := entries[i]
		ev := RunEntryView{RunEntry: e, Wait: waitAt(entries, order, pos, rides)}
		ev.SteerInFlight = rides[i] && e.State == RunPending
		if ev.WaitingToSend() && e.State != RunHeld && e.ByUser() &&
			ev.Wait.After != waitNext && ev.Wait.After != WaitCompaction {
			ev.SendNow = arrow
		}
		if !carried && e.Kind.TurnKind() && !e.kiroCLI() && (e.State == RunStarting || e.State == RunPending) {
			ev.CarriesUnread, carried = true, true
		}
		v.Entries[i] = ev
	}
	v.NextWait = waitIfInserted(entries, insertAt(entries), &RunEntry{Kind: RunKindRow, State: RunPending})
	v.UnreadWait = unreadWait(entries)
	return v
}

// unreadWait is what a steer kiro-cli still holds unsent waits for: a turn of its own at the front of the pending
// entries, behind a compaction pending there.
func unreadWait(entries []RunEntry) RunWait {
	at := len(LivePrefix(entries))
	if at < len(entries) && entries[at].State == RunPending && entries[at].Kind == RunKindCompaction {
		at++
	}
	return waitIfInserted(entries, at, &RunEntry{Kind: RunKindRow, State: RunPending})
}

// SanitizeRun is the list a new life starts from: only rows whose turn never opened, and no steer handed to kiro-cli
// (offered or in its buffer), whose words the life's end or the shutdown recorded. A process's first life drops the
// notify door's entries (their callers send again while unread) and holds the rest, so a restart sends nothing
// unasked; a later life keeps a door entry as it was, pending or paused (a close never undoes a stop), keeps held rows
// held and pauses the rest, so they wait for that life's next clean turn end; a row paused there keeps its StopPaused.
func SanitizeRun(entries []RunEntry, firstLifeInProcess bool) []RunEntry {
	var out []RunEntry
	for _, e := range entries {
		if e.Kind != RunKindRow || e.Row == nil || e.Turn != "" {
			continue
		}
		if (e.State.Live() && e.State != RunStarting) || e.State == RunOffered || e.State == RunInKAS {
			continue
		}
		if e.Origin == RunFromNotify && firstLifeInProcess {
			continue
		}
		e.Next, e.State = false, sanitizedState(&e, firstLifeInProcess)
		e.StopPaused = e.StopPaused && e.State == RunPaused
		out = append(out, e)
	}
	return out
}

// sanitizedState is the state SanitizeRun gives a row it keeps.
func sanitizedState(e *RunEntry, firstLifeInProcess bool) RunEntryState {
	switch {
	case e.Origin == RunFromNotify && e.State == RunPaused:
		return RunPaused
	case e.Origin == RunFromNotify:
		return RunPending
	case firstLifeInProcess || e.State == RunHeld || e.Row.Carried():
		return RunHeld
	}
	return RunPaused
}

// RunRows returns the messages of the row and retry entries on a list.
func RunRows(entries []RunEntry) []*QueuedPrompt {
	var out []*QueuedPrompt
	for i := range entries {
		k := entries[i].Kind
		if (k == RunKindRow || k == RunKindRetry) && entries[i].Row != nil {
			out = append(out, entries[i].Row)
		}
	}
	return out
}
