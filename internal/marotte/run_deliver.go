package marotte

import "slices"

// DeliverKind is what Deliver hands out.
type DeliverKind uint8

const (
	// DeliverNothing delivers nothing now.
	DeliverNothing DeliverKind = iota
	// DeliverSteer steers one entry into the running turn.
	DeliverSteer
	// DeliverPrompt takes entries as one new turn's prompt.
	DeliverPrompt
)

// Delivery is what a chat delivers next.
type Delivery struct {
	// IDs is the entry to steer, or the entries one prompt carries, lead first.
	IDs  []string
	Kind DeliverKind
	// UserNext marks a prompt the user's send-now or stop asked for: no door entry is steered into its turn.
	UserNext bool
}

// HeadFacts are the facts Deliver reads that the entries do not carry.
type HeadFacts struct {
	// Mode is the interrupt mode of a send VerbFor and WantFor place; the list's own rows carry theirs in Want.
	Mode InterruptMode
	// SteerRefused names the door entries kiro-cli refused as a steer since a turn last opened or closed.
	SteerRefused []string
	// Prompted reports that the running head's session/prompt is written.
	Prompted bool
	// UserNext reports that the outstanding session/prompt is one the user's send-now or stop asked for.
	UserNext bool
	// Stopped reports that the last turn end was a stop.
	Stopped bool
	// EndsOwed reports a closed turn whose ends are still held.
	EndsOwed bool
}

// ByUser reports an entry the user sent, one with no caller; a row with a caller is a door entry.
func (e *RunEntry) ByUser() bool { return e.Row == nil || e.Row.Caller == nil }

func sameSender(a, b *RunEntry) bool {
	if a.ByUser() || b.ByUser() {
		return a.ByUser() == b.ByUser()
	}
	return a.Row.Caller.Label == b.Row.Caller.Label
}

// awaitsDelivery reports a row waiting to be delivered (a held row waits for nothing): a user's holds back every
// door entry behind it.
func (e *RunEntry) awaitsDelivery() bool {
	return e.Kind == RunKindRow &&
		(e.State == RunPending || e.State == RunPaused || e.State == RunOffered || e.State == RunInKAS)
}

// Deliver is the one rule for which rows a chat delivers next, and how, from its whole list and its head's facts.
// Rows only: a compaction, revert, retry or resume pending ahead keeps its own take rule, so Deliver takes no turn
// past one. A user entry outranks a door entry for the running turn, a door entry never goes while a
// user entry waits ahead of it, and one prompt never joins two senders.
func Deliver(entries []RunEntry, h HeadFacts) Delivery {
	if h.EndsOwed {
		return Delivery{}
	}
	live := LivePrefix(entries)
	switch {
	case len(live) == 0:
		return takeNext(entries, 0, h)
	case len(live) > 1:
		return Delivery{}
	}
	head := &live[0]
	if head.Kind == RunKindTurn && head.Origin == RunFromWire && head.Yields && head.State == RunRunning {
		if head.Admits {
			return takeNext(entries, 1, h)
		}
		return Delivery{}
	}
	if !steerSlot(entries, head, h) {
		return Delivery{}
	}
	return steerNext(entries[1:], h)
}

// steerSlot reports a head a steer may go into now: one entry offered at a time keeps kiro-cli's steer buffer in list
// order.
func steerSlot(entries []RunEntry, head *RunEntry, h HeadFacts) bool {
	return steerHead(head, h) && !slices.ContainsFunc(entries, func(e RunEntry) bool { return e.State == RunOffered })
}

// steerHead reports a running turn that takes steers. A wire head counts as prompted, since kiro-cli wrote its own
// prompt.
func steerHead(head *RunEntry, h HeadFacts) bool {
	return head.Kind.TurnKind() && head.Origin != RunFromRun && head.State == RunRunning && head.Steerable &&
		h.Mode != InterruptQueue && (h.Prompted || head.Origin == RunFromWire)
}

// steerBound reports a user row the running turn takes as a steer: Deliver steers it now, or will once the head's
// prompt is written and the one steer offered is answered, both of which settle by themselves. The row's Want holds
// its send's interrupt mode.
func steerBound(entries []RunEntry, i int) bool {
	e := &entries[i]
	live := LivePrefix(entries)
	return e.ByUser() && steerable(e) && len(live) == 1 && steerHead(&live[0], HeadFacts{Prompted: true})
}

func steerable(e *RunEntry) bool {
	return e.Kind == RunKindRow && e.State == RunPending && e.Want == RunWantEither
}

// steerNext is the steer slot's pick: the first user entry that may be steered, else the first door entry once
// nothing of the user's waits and the turn is not one the user asked for.
func steerNext(rest []RunEntry, h HeadFacts) Delivery {
	userWaiting := false
	for i := range rest {
		e := &rest[i]
		if !e.ByUser() {
			continue
		}
		if steerable(e) {
			return Delivery{Kind: DeliverSteer, IDs: []string{e.ID}}
		}
		userWaiting = userWaiting || e.awaitsDelivery()
	}
	if userWaiting || h.UserNext {
		return Delivery{}
	}
	for i := range rest {
		if e := &rest[i]; !e.ByUser() && steerable(e) && !slices.Contains(h.SteerRefused, e.ID) {
			return Delivery{Kind: DeliverSteer, IDs: []string{e.ID}}
		}
	}
	return Delivery{}
}

// takeNext is the pick of a new turn from entries[from:]: a send-now row ahead of any other pending entry first, else
// the first pending row, skipping a door entry while a user entry waits ahead of it.
func takeNext(entries []RunEntry, from int, h HeadFacts) Delivery {
	end := len(entries)
	if i := slices.IndexFunc(entries[from:], func(e RunEntry) bool {
		return e.Kind != RunKindRow && e.State == RunPending
	}); i >= 0 {
		end = from + i
	}
	at := slices.IndexFunc(entries[from:end], func(e RunEntry) bool {
		return e.Kind == RunKindRow && e.State == RunPending && e.Next
	})
	if at >= 0 {
		at += from
	} else {
		at = firstTakeable(entries, from)
	}
	if at < 0 || otherPendingBefore(entries, from, at) {
		return Delivery{}
	}
	lead := &entries[at]
	ids := []string{lead.ID}
	if lead.Unread {
		ids = append(ids, unreadRun(entries, from, at)...)
	}
	return Delivery{
		Kind: DeliverPrompt, IDs: ids,
		UserNext: lead.Next || (lead.ByUser() && lead.Unread && h.Stopped),
	}
}

func firstTakeable(entries []RunEntry, from int) int {
	userAhead := false
	for i := from; i < len(entries); i++ {
		e := &entries[i]
		if e.Kind == RunKindRow && e.State == RunPending && (e.ByUser() || !userAhead) {
			return i
		}
		userAhead = userAhead || (e.ByUser() && e.awaitsDelivery())
	}
	return -1
}

func otherPendingBefore(entries []RunEntry, from, at int) bool {
	return slices.ContainsFunc(entries[from:at], func(e RunEntry) bool {
		return e.Kind != RunKindRow && e.State == RunPending
	})
}

// unreadRun is the pending Unread rows of the lead's sender next to entries[at] in list order, the lead excluded:
// one sender's steers a turn left unread go as one prompt.
func unreadRun(entries []RunEntry, from, at int) []string {
	lead := &entries[at]
	joins := func(e *RunEntry) bool {
		return e.Kind == RunKindRow && e.State == RunPending && e.Unread && sameSender(lead, e)
	}
	lo, hi := at, at+1
	for lo > from && joins(&entries[lo-1]) {
		lo--
	}
	for hi < len(entries) && joins(&entries[hi]) {
		hi++
	}
	var ids []string
	for i := lo; i < hi; i++ {
		if i != at {
			ids = append(ids, entries[i].ID)
		}
	}
	return ids
}
