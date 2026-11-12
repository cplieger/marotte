// Package runlease holds what marotte knows about a workflow run KAS owns: the ENVELOPE
// (may it start, how long may it execute, is it unattended, what its ending is called).
//
// There is one lease per run marotte itself put on the wire. That scoping is the
// restart-orphan sweep's safety property: a TUI-launched run has no lease, so it is never swept.
package runlease

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/logsafe"
)

// Origin says which launch produced the run: scheduled (bounded by its next slot and the
// ceiling, unattended, sweepable), triggered (a forge event's launch rule: ceiling,
// unattended, sweepable), manual (ceiling, attended, sweepable), agent (ceiling alone,
// chat-parented, excluded from the orphan sweep's cancel arm).
type Origin string

// The four launches marotte knows. There is no `tui` value: a TUI-launched run has no
// lease, and that absence keeps the sweep off it.
const (
	OriginScheduled Origin = "scheduled"
	OriginTriggered Origin = "triggered"
	OriginManual    Origin = "manual"
	OriginAgent     Origin = "agent"
)

// valid reports whether an origin is one this build understands. An unknown origin
// cannot be reasoned about: sweepable and unattended are both unanswerable.
func (o Origin) valid() bool {
	return o == OriginScheduled || o == OriginTriggered || o == OriginManual || o == OriginAgent
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
	// AutoResumeStoppedAt is when marotte stopped resuming the run on its own after repeated involuntary pauses;
	// zero while it still does. Recorded here, not as a KAS pause, because KAS attributes every pause to the user.
	AutoResumeStoppedAt time.Time `json:"auto_resume_stopped_at,omitzero"`
	// WorkflowID is KAS's id for the run, and the lease's key.
	WorkflowID string `json:"workflow_id"`
	// ChatID is the chat whose agent launched the run. Empty means "no chat to
	// exempt": the parentless launch verbs mint with no chat by design.
	ChatID string `json:"chat_id,omitempty"`
	// ParentSessionID is the run's parent session as marotte recorded it at launch or from
	// `run_start`: a parentless run's host session, which a driving verb loads when it is not
	// resident, because the run's steps take their policy from it.
	ParentSessionID string `json:"parent_session_id,omitempty"`
	// Host is a parentless run's host-session security input, resolved once at launch and never
	// rewritten: a reload after a restart applies these presets, not whatever the profile is now.
	// Nil on a chat-parented run, and on a parentless one from a build that did not record it,
	// which HostSecurity refuses.
	Host *HostSecurity `json:"host,omitempty"`
	// Recipe is the run's recipe NAME, which is what the single-run rule keys on.
	Recipe string `json:"recipe"`
	Origin Origin `json:"origin"`
	// ScheduleID names the clock rule that asked for the run, the only row its outcome lands on. Set iff Origin
	// is scheduled.
	ScheduleID string `json:"schedule_id,omitempty"`
	// RuleID, EventKey and CoalesceKey name the event rule, the event and its coalesce key a triggered launch
	// was for; run_start confirms that event. Set iff Origin is triggered.
	RuleID      string `json:"rule_id,omitempty"`
	EventKey    string `json:"event_key,omitempty"`
	CoalesceKey string `json:"coalesce_key,omitempty"`
	// RunLabel is the label a triggered launch sent KAS, kept so the run is named the same after a restart.
	RunLabel string `json:"run_label,omitempty"`
	// KeepChats is every chat holding this run's keep fact, which each sign of life rewrites. It only grows.
	KeepChats []string `json:"keep_chats,omitempty"`
	// Unattended marks a run with nobody to answer a permission request, which arms
	// the deny-fast floor. Distinct from the origin: who launched it vs who watches.
	Unattended bool `json:"unattended"`
	// Unattributed marks a live run whose launching chat could not be named, which keeps every chat until it is.
	Unattributed bool `json:"unattributed,omitempty"`
}

// HostSecurity is what a parentless run's host session opens with.
type HostSecurity struct {
	// Presets are the KAS policy-preset ids the session sends; empty is the Custom profile.
	Presets []string `json:"presets"`
}

// errNoHostSecurity refuses to reload a parentless run's host session whose launch recorded no
// security input: any default would be a guess at the run's permissions.
var errNoHostSecurity = errors.New("this run's security profile was not recorded at launch, so marotte cannot load its session; cancel it and launch it again")

// HostPresets returns the presets a parentless run's host session reloads with, or
// errNoHostSecurity when the lease recorded none.
func (l *Lease) HostPresets() ([]string, error) {
	if l.Host == nil || l.Host.Presets == nil {
		return nil, errNoHostSecurity
	}
	return slices.Clone(l.Host.Presets), nil
}

// clone copies l, its host security included, so no caller shares the store's slice.
func (l *Lease) clone() Lease {
	out := *l
	out.KeepChats = slices.Clone(l.KeepChats)
	if out.Host != nil {
		h := HostSecurity{Presets: slices.Clone(out.Host.Presets)}
		out.Host = &h
	}
	return out
}

// addKeepChats unions add into KeepChats, sorted, and reports whether it grew.
func (l *Lease) addKeepChats(add []string) bool {
	merged := slices.Clone(l.KeepChats)
	for _, id := range add {
		if !slices.Contains(merged, id) {
			merged = append(merged, id)
		}
	}
	if len(merged) == len(l.KeepChats) {
		return false
	}
	slices.Sort(merged)
	l.KeepChats = merged
	return true
}

// validKeepChats drops a holder id that could not name a chat, with a WARN.
func validKeepChats(workflowID string, chats []string) []string {
	return slices.DeleteFunc(chats, func(id string) bool {
		if ids.ValidChatID(id) {
			return false
		}
		slog.Warn("run lease: dropped a keep holder that names no chat", "workflow_id", workflowID, "holder", logsafe.Field(id))
		return true
	})
}

// check refuses a lease the store cannot act on, on Put and on load alike, so the store never writes
// a lease its own load would refuse.
func (l *Lease) check() error {
	if !ids.ValidChatID(l.WorkflowID) {
		return fmt.Errorf("lease workflow id %q is not a valid workflow id", logsafe.Field(l.WorkflowID))
	}
	if !l.Origin.valid() {
		return fmt.Errorf("lease origin %q is not one of scheduled/manual/agent/triggered", l.Origin)
	}
	return nil
}

// Bounded reports whether marotte believes the run to be EXECUTING under a deadline it set.
func (l *Lease) Bounded() bool { return !l.Deadline.IsZero() }

// Bounds is the input set NextDeadline composes. A struct, so two same-typed durations
// cannot be transposed. Field order is govet fieldalignment's.
type Bounds struct {
	// SlotAt is when the run's next slot comes due. Zero means "no slot to respect".
	SlotAt time.Time
	// Idle is how long a run may execute without observable progress; a refill rolls it
	// forward, which makes the primary bound a STALL bound, not a total-duration one.
	Idle time.Duration
	// Floor is the smallest budget any run gets. It outranks the idle window and the slot.
	Floor time.Duration
}

// NextDeadline computes the ONE deadline a run gets (one timer, one instant):
//
//	min(now+Idle, SlotAt?) -> max(that, now+Floor)
//
// The order is the precedence.
func NextDeadline(now time.Time, b Bounds) time.Time {
	deadline := now.Add(b.Idle)
	if !b.SlotAt.IsZero() && b.SlotAt.Before(deadline) {
		deadline = b.SlotAt
	}
	if minimum := now.Add(b.Floor); deadline.Before(minimum) {
		deadline = minimum
	}
	return deadline
}
