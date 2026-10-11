// Package runlease holds what marotte knows about a workflow run KAS owns: the ENVELOPE
// (may it start, how long may it execute, is it unattended, what its ending is called).
//
// There is one lease per run marotte itself put on the wire. That scoping is the
// restart-orphan sweep's safety property: a TUI-launched run has no lease, so it is never swept.
package runlease

import "time"

// Origin says which launch produced the run: scheduled (bounded by its next slot and the
// ceiling, unattended, sweepable), manual (ceiling, attended, sweepable), agent (ceiling
// alone, chat-parented, excluded from the orphan sweep's cancel arm).
type Origin string

// The three launches marotte knows. There is no `tui` value: a TUI-launched run has no
// lease, and that absence keeps the sweep off it.
const (
	OriginScheduled Origin = "scheduled"
	OriginManual    Origin = "manual"
	OriginAgent     Origin = "agent"
)

// valid reports whether an origin is one this build understands. An unknown origin
// cannot be reasoned about: sweepable and unattended are both unanswerable.
func (o Origin) valid() bool {
	return o == OriginScheduled || o == OriginManual || o == OriginAgent
}

// Lease is marotte's record of one run. Field order is govet fieldalignment's.
type Lease struct {
	// StartedAt is when marotte put the run on the wire; diagnosis only.
	StartedAt time.Time `json:"started_at"`
	// Deadline is the ONE instant this run is cancelled. Mutable: re-stamped on every start,
	// cleared on every pause, rolled forward by progress. ZERO means marotte is not bounding it.
	Deadline time.Time `json:"deadline,omitzero"`
	// SlotAt is when this run's own next scheduled slot comes due — an INPUT to
	// Deadline, immutable, so a re-arm re-applies it. Zero for every other origin.
	SlotAt time.Time `json:"slot_at,omitzero"`
	// FirstAbsentAt starts the continuous-absence clock. A listed run clears it.
	FirstAbsentAt time.Time `json:"first_absent_at,omitzero"`
	// WorkflowID is KAS's id for the run, and the lease's key.
	WorkflowID string `json:"workflow_id"`
	// ChatID is the chat whose agent launched the run. Empty means "no chat to
	// exempt": the parentless launch verbs mint with no chat by design.
	ChatID string `json:"chat_id,omitempty"`
	// Recipe is the run's recipe NAME, which is what the single-run rule keys on.
	Recipe string `json:"recipe"`
	Origin Origin `json:"origin"`
	// ScheduleID names the row that asked for the run. Set iff Origin is scheduled.
	ScheduleID string `json:"schedule_id,omitempty"`
	// Unattended marks a run with nobody to answer a permission request, which arms
	// the deny-fast floor. Distinct from the origin: who launched it vs who watches.
	Unattended bool `json:"unattended"`
}

// Bounded reports whether marotte believes the run to be EXECUTING under a deadline it set.
func (l *Lease) Bounded() bool { return !l.Deadline.IsZero() }

// Bounds is the input set NextDeadline composes. A struct, so two same-typed durations
// cannot be transposed. Field order is govet fieldalignment's.
type Bounds struct {
	// SlotAt is when the run's next slot comes due. Zero means "no slot to respect".
	SlotAt time.Time
	// BackstopAt is when the run's absolute EXECUTING-time budget is spent; zero does not bound.
	// An instant rather than a duration, because an omitted duration would read as spent.
	BackstopAt time.Time
	// Idle is how long a run may execute without observable progress; a refill rolls it
	// forward, which makes the primary bound a STALL bound, not a total-duration one.
	Idle time.Duration
	// Floor is the smallest budget any run gets. It outranks the idle window and the slot, not
	// a tighter BackstopAt: the floor is what a run should get, the backstop what is left.
	Floor time.Duration
}

// NextDeadline computes the ONE deadline a run gets (one timer, one instant):
//
//	min(now+Idle, SlotAt?) -> max(that, now+Floor) -> min(that, BackstopAt?)
//
// The order is the precedence. Clamping the backstop LAST keeps it terminal; otherwise
// each progress frame hands out a fresh floor and the absolute bound becomes a rolling one.
func NextDeadline(now time.Time, b Bounds) time.Time {
	deadline := now.Add(b.Idle)
	if !b.SlotAt.IsZero() && b.SlotAt.Before(deadline) {
		deadline = b.SlotAt
	}
	if minimum := now.Add(b.Floor); deadline.Before(minimum) {
		deadline = minimum
	}
	if !b.BackstopAt.IsZero() && b.BackstopAt.Before(deadline) {
		deadline = b.BackstopAt
	}
	return deadline
}
