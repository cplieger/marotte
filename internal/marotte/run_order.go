package marotte

// RunEntryKind is what a run-order entry runs.
type RunEntryKind string

const (
	// RunKindTurn is a turn no row started: kiro-cli's own (origin wire or run), or a tangent's summary.
	RunKindTurn RunEntryKind = "turn"
	// RunKindRow is a queued message.
	RunKindRow RunEntryKind = "row"
	// RunKindUnread is the steers a closed turn left unread, sent as one prompt.
	RunKindUnread RunEntryKind = "unread"
	// RunKindRetry is the one re-send of a prompt whose turn produced nothing.
	RunKindRetry RunEntryKind = "retry"
	// RunKindCompaction is one compaction.
	RunKindCompaction RunEntryKind = "compaction"
	// RunKindRevert is a rewind's checkpoint revert.
	RunKindRevert RunEntryKind = "revert"
)

// TurnKind reports a kind whose take opens a turn.
func (k RunEntryKind) TurnKind() bool {
	return k == RunKindTurn || k == RunKindRow || k == RunKindUnread || k == RunKindRetry
}

// RunEntryState is where an entry stands in the run order.
type RunEntryState string

const (
	// RunPending waits its turn.
	RunPending RunEntryState = "pending"
	// RunPaused is a row a stop or a broken close left: it waits for the next clean turn end or a send-now.
	RunPaused RunEntryState = "paused"
	// RunHeld is a row from before a restart, or a carried row: shown, never taken.
	RunHeld RunEntryState = "held"
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
// (request to context_limit) and a rewind's.
const (
	RunFromPrompt       RunEntryOrigin = "prompt"
	RunFromQueue        RunEntryOrigin = "queue"
	RunFromTangent      RunEntryOrigin = "tangent"
	RunFromSummary      RunEntryOrigin = "summary"
	RunFromUnread       RunEntryOrigin = "unread"
	RunFromRetry        RunEntryOrigin = "retry"
	RunFromWire         RunEntryOrigin = "wire"
	RunFromRun          RunEntryOrigin = "run"
	RunFromCarried      RunEntryOrigin = "carried"
	RunFromRequest      RunEntryOrigin = "request"
	RunFromThreshold    RunEntryOrigin = "threshold"
	RunFromTurnEnd      RunEntryOrigin = "turn_end"
	RunFromContextLimit RunEntryOrigin = "context_limit"
	RunFromRewind       RunEntryOrigin = "rewind"
)

// RunEntry is one entry of Chat.Run, in run order: the live entries first, then what runs after them.
type RunEntry struct {
	// Row is a row, unread or retry entry's message.
	Row    *QueuedPrompt  `json:"row,omitempty"`
	ID     string         `json:"id"`
	Kind   RunEntryKind   `json:"kind"`
	State  RunEntryState  `json:"state"`
	Origin RunEntryOrigin `json:"origin"`
	// Turn is the turn id once the entry's turn opened: the transcript then shows its message.
	Turn string `json:"turn,omitempty"`
	// Next marks the row send_queued_now moved to the front.
	Next      bool `json:"next,omitempty"`
	Steerable bool `json:"steerable,omitempty"`
	// Yields marks a fold-opened wire turn, which owes no close: a turn-kind take or a compaction goes beside it.
	Yields bool `json:"yields,omitempty"`
	// Admits, with Yields, is whether kiro-cli's state admits a take beside it now.
	Admits bool `json:"admits,omitempty"`
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

func pendingAfter(entries []RunEntry, from int) bool {
	for i := from; i < len(entries); i++ {
		if entries[i].State == RunPending {
			return true
		}
	}
	return false
}

// VerbFor is the one rule for what a send does: prompt (taken now), steer (into the running turn) or queue. A
// queue_prompt answered prompt is a send with nothing to queue behind, which the caller refuses no_turn.
func VerbFor(entries []RunEntry, mode InterruptMode, kind SendKind) SendVerb {
	live := LivePrefix(entries)
	waiting := pendingAfter(entries, len(live))
	switch {
	case len(live) == 0:
		if waiting {
			return SendQueue
		}
		return SendPrompt
	case len(live) > 1:
		return SendQueue
	}
	h := &live[0]
	if h.Kind == RunKindTurn && h.Origin == RunFromWire && h.Yields && h.State == RunRunning {
		return yieldingVerb(h, waiting, kind)
	}
	if h.Kind.TurnKind() && h.Origin != RunFromRun && h.State == RunRunning && h.Steerable &&
		kind == SendPlain && mode != InterruptQueue {
		return SendSteer
	}
	return SendQueue
}

// yieldingVerb is the verb beside a fold-opened wire turn: a send is taken beside it only while kiro-cli admits one
// and nothing waits ahead.
func yieldingVerb(h *RunEntry, waiting bool, kind SendKind) SendVerb {
	if waiting || !h.Admits || kind == SendQueuePrompt {
		return SendQueue
	}
	return SendPrompt
}

// RunWaitAfter names what an entry waits for.
type RunWaitAfter string

// What an entry waits for: it runs or starts, it goes next, after the compaction ahead, after Turns turn ends, or
// never by itself (held).
const (
	WaitRunning    RunWaitAfter = "running"
	WaitStarting   RunWaitAfter = "starting"
	WaitNext       RunWaitAfter = "next"
	WaitCompaction RunWaitAfter = "compaction"
	WaitTurns      RunWaitAfter = "turns"
	WaitHeld       RunWaitAfter = "held"
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
	// SendNowStop stops the running turn and sends the row next.
	SendNowStop SendNowKind = "stop"
	// SendNowStopThenCompaction stops the running turn; the row goes after the compaction pending behind it.
	SendNowStopThenCompaction SendNowKind = "stop_then_compaction"
	// SendNowAfterCompaction stops nothing: the row goes first once the compaction ends.
	SendNowAfterCompaction SendNowKind = "after_compaction"
	// SendNowNow stops nothing: the row goes next once what is live ends.
	SendNowNow SendNowKind = "now"
)

// RunEntryView is one entry as the header draws it.
type RunEntryView struct {
	// SendNow is the row's arrow; absent: no arrow.
	SendNow SendNowKind `json:"send_now,omitempty"`
	RunEntry
	Wait RunWait `json:"wait"`
	// CarriesUnread marks the entry whose turn the awaits-prompt steers ride.
	CarriesUnread bool `json:"carries_unread,omitempty"`
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

func waitAt(entries []RunEntry, order []int, pos int) RunWait {
	e := &entries[order[pos]]
	switch {
	case e.State == RunStarting:
		return RunWait{After: WaitStarting}
	case e.State.Live():
		return RunWait{After: WaitRunning}
	case e.State == RunHeld:
		return RunWait{After: WaitHeld}
	}
	if e.State == RunPending && pos > 0 && entries[order[pos-1]].Kind == RunKindCompaction {
		return RunWait{After: WaitCompaction}
	}
	paused := e.State == RunPaused
	n, liveCounted := turnsAhead(entries, order[:pos], paused)
	if paused && n == 0 {
		n = 1
	}
	if n == 0 {
		return RunWait{After: WaitNext}
	}
	w := RunWait{After: WaitTurns, Turns: n}
	if n == 1 && liveCounted {
		w.This = true
	}
	return w
}

// turnsAhead counts the turn ends ahead: a starting or running turn one each (liveCounted reports one), a pending
// one each, a paused one only for a paused entry, a stopping or closing one none, since it runs nothing more.
func turnsAhead(entries []RunEntry, ahead []int, paused bool) (n int, liveCounted bool) {
	// A fold turn ahead of a compaction is closed by it, so it runs no turn ahead of anything.
	live := LivePrefix(entries)
	foldAhead := len(live) == 2 && live[1].Kind == RunKindCompaction
	for _, j := range ahead {
		a := &entries[j]
		if !a.Kind.TurnKind() || (foldAhead && j == 0) {
			continue
		}
		switch a.State {
		case RunStarting, RunRunning:
			n++
			liveCounted = true
		case RunPending:
			n++
		case RunPaused:
			if paused {
				n++
			}
		case RunHeld, RunStopping, RunClosing:
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
			return SendNowStop
		case h.Kind == RunKindCompaction:
			return SendNowAfterCompaction
		case (h.State == RunStopping || h.State == RunClosing) && compactionNext:
			return SendNowAfterCompaction
		}
		return SendNowNow
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
	return SendNowNow
}

// InsertAt is where a new row goes: before the first paused row, so the list order is the send order.
func InsertAt(entries []RunEntry) int {
	for i := len(LivePrefix(entries)); i < len(entries); i++ {
		if entries[i].State == RunPaused {
			return i
		}
	}
	return len(entries)
}

// UnreadSlot is where a closed turn's unsent steers go: into the front pending row's turn when send-now marked it
// (rides names that row, index -1), else behind a compaction pending at the front, else at the front.
func UnreadSlot(entries []RunEntry) (index int, rides string) {
	front := len(LivePrefix(entries))
	if front < len(entries) {
		e := &entries[front]
		if e.State == RunPending && e.Next && e.Kind == RunKindRow {
			return -1, e.ID
		}
		if e.State == RunPending && e.Kind == RunKindCompaction {
			return front + 1, ""
		}
	}
	return front, ""
}

func waitIfInserted(entries []RunEntry, at int, e *RunEntry) RunWait {
	with := make([]RunEntry, 0, len(entries)+1)
	with = append(with, entries[:at]...)
	with = append(with, *e)
	with = append(with, entries[at:]...)
	order := waitOrder(with)
	for pos, i := range order {
		if i == at {
			return waitAt(with, order, pos)
		}
	}
	return RunWait{After: WaitNext}
}

// RunViewOf is the header's run order for a chat's list, its version and its interrupt mode.
func RunViewOf(entries []RunEntry, version uint64, mode InterruptMode) RunView {
	v := RunView{
		Entries: make([]RunEntryView, len(entries)),
		Version: version,
		Send:    VerbFor(entries, mode, SendPlain),
		Cancel:  CancelTarget(entries),
	}
	order := waitOrder(entries)
	arrow := sendNowKind(entries)
	carried := false
	for pos, i := range order {
		e := entries[i]
		ev := RunEntryView{RunEntry: e, Wait: waitAt(entries, order, pos)}
		if e.Kind == RunKindRow && (e.State == RunPending || e.State == RunPaused) &&
			ev.Wait.After != WaitNext && ev.Wait.After != WaitCompaction {
			ev.SendNow = arrow
		}
		if !carried && e.Kind.TurnKind() && !e.kiroCLI() && (e.State == RunStarting || e.State == RunPending) {
			ev.CarriesUnread, carried = true, true
		}
		v.Entries[i] = ev
	}
	v.NextWait = waitIfInserted(entries, InsertAt(entries), &RunEntry{Kind: RunKindRow, State: RunPending})
	v.UnreadWait = unreadWait(entries, v.Entries)
	return v
}

func unreadWait(entries []RunEntry, views []RunEntryView) RunWait {
	for i := range entries {
		if entries[i].Kind == RunKindUnread {
			return views[i].Wait
		}
	}
	at, rides := UnreadSlot(entries)
	if rides != "" {
		for i := range entries {
			if entries[i].ID == rides {
				return views[i].Wait
			}
		}
	}
	return waitIfInserted(entries, at, &RunEntry{Kind: RunKindUnread, State: RunPending})
}

// SanitizeRun is the list a new life starts from: only rows whose turn never opened. A process's first life holds
// them all (a restart sends nothing unasked); a later life keeps held rows held and pauses the rest, so they wait
// for that life's next clean turn end.
func SanitizeRun(entries []RunEntry, firstLifeInProcess bool) []RunEntry {
	var out []RunEntry
	for _, e := range entries {
		if e.Kind != RunKindRow || e.Row == nil || e.Turn != "" {
			continue
		}
		if e.State.Live() && e.State != RunStarting {
			continue
		}
		e.Next = false
		switch {
		case firstLifeInProcess || e.State == RunHeld || e.Row.Carried():
			e.State = RunHeld
		default:
			e.State = RunPaused
		}
		out = append(out, e)
	}
	return out
}

// RunRows returns the messages of the row, unread and retry entries on a list.
func RunRows(entries []RunEntry) []*QueuedPrompt {
	var out []*QueuedPrompt
	for i := range entries {
		k := entries[i].Kind
		if (k == RunKindRow || k == RunKindUnread || k == RunKindRetry) && entries[i].Row != nil {
			out = append(out, entries[i].Row)
		}
	}
	return out
}
