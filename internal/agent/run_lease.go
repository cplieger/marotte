package agent

// The runtime side of a run lease: is a blocking row marotte's orphan, when must the run end, is it
// unattended, and which schedule row gets the outcome.

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/schedule"
)

// A struct because the scheduled fields only make sense together.
type launchOrigin struct {
	// slotAt is this run's next slot, an input to the deadline; launch fills it for a manual run (manualSlot).
	slotAt time.Time
	// row is the schedule row a scheduled run reports to, nil for any other.
	row *schedule.Row
	// chatID is the launching chat for an agent's run (runStartLaunch), empty for a parentless launch.
	chatID string
	origin runlease.Origin
}

// manualLaunch is the Run button and a parentless run's retry: attended, no schedule row. The slot depends on the resolved recipe, so launch sets it.
func manualLaunch() launchOrigin {
	return launchOrigin{origin: runlease.OriginManual}
}

// retryLaunch leases a retried run as its gate found it: an agent's run keeps its launching chat, whose session hosts
// the retry.
func retryLaunch(o *runOrigin) launchOrigin {
	if o.parent.chat == "" {
		return manualLaunch()
	}
	return launchOrigin{origin: runlease.OriginAgent, chatID: string(o.parent.chat)}
}

// grantRetryLease leases a retried run from Retry's read of its origin, parent session included: the retry's run_start finds
// the lease held and adds no chat to it. A parentless retry is a new launch decision, so its host session reloads
// with the profile's presets as they are now, recorded here.
func (rs *Runs) grantRetryLease(ctx context.Context, workflowID string, o *runOrigin, holders []marotte.ChatID) {
	var host *runlease.HostSecurity
	if o.parent.chat == "" {
		host = hostSecurityOf(rs.doorSettings(ctx).Presets)
	}
	lo := retryLaunch(o)
	rs.grantKeeping(ctx, workflowID, o.recipe, &lo, host, &leaseKeep{chats: chatIDs(holders)})
	rs.recordParentSession(ctx, workflowID, o.parent.session)
}

// settleMissedEnd ends a scheduled run whose terminal frame marotte missed, as observeComplete would have, before a
// person retries it: a scheduled lease is that one run's, and the retry is the person's (manual, attended, no row).
func (rs *Runs) settleMissedEnd(ctx context.Context, l *runlease.Lease, status marotte.RunStatus) {
	rs.recordScheduleEnding(ctx, scheduleRun(l), status, "")
	rs.forgetBounds(ctx, l.WorkflowID)
}

// scheduledLaunch carries the schedule row for outcomes and the slot the run may not outlive.
func scheduledLaunch(row schedule.Row, slotAt time.Time) launchOrigin {
	return launchOrigin{origin: runlease.OriginScheduled, row: &row, slotAt: slotAt}
}

// scheduleRow is the row the run reports to, zero for an unscheduled run.
func (o *launchOrigin) scheduleRow() schedule.Row {
	if o.row == nil {
		return schedule.Row{}
	}
	return *o.row
}

// runLabel is the label sent for a run, set only for a scheduled one so History shows which runs nobody started.
func (o launchOrigin) runLabel(recipe string) string {
	if o.origin != runlease.OriginScheduled {
		return ""
	}
	return runLabelFor(recipe + " · scheduled")
}

// runLabelMaxUnits is KAS's runLabel cap, counted in UTF-16 code units.
const runLabelMaxUnits = 100

// runLabelFor shapes s to KAS's runLabel rule, which refuses rather than cleans: no control, line-break
// or double quote, 1 to 100 UTF-16 units trimmed.
func runLabelFor(s string) string {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		switch {
		case r == '"':
			return '\''
		case unicode.IsControl(r), unicode.In(r, unicode.Zl, unicode.Zp):
			return ' '
		}
		return r
	}, s))
	units := 0
	for i, r := range s {
		n := utf16.RuneLen(r)
		if units+n > runLabelMaxUnits {
			s = s[:i]
			break
		}
		units += n
	}
	return strings.TrimSpace(s)
}

// manualSlot is the next slot a manual run of this recipe must yield to, matched on the launch source and
// floored at now via schedule.NextRunFrom (as the REST view). Zero when nothing schedules it.
func (rs *Runs) manualSlot(source string) time.Time {
	if rs.schedules == nil || source == "" {
		return time.Time{}
	}
	now := time.Now()
	var earliest time.Time
	list := rs.schedules.List()
	for i := range list {
		e := &list[i]
		if !e.Enabled || e.Source != source {
			continue
		}
		next, err := schedule.NextRunFrom(e.Spec, e.Anchor, now)
		if err != nil {
			// No slot, not a failure: the window and backstop still bound it.
			slog.Warn("schedule cannot name its next slot, so a manual run of its recipe "+
				"is bounded by its idle window alone", "schedule_id", e.ID(), "error", err)
			continue
		}
		if earliest.IsZero() || next.Before(earliest) {
			earliest = next
		}
	}
	return earliest
}

// leaseStore returns the lease registry, in memory when the runtime has no durable store (every unit
// test): a lease carries the run's clock. WithRunLeases adds durability.
func (rs *Runs) leaseStore() *runlease.Store {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.leases == nil {
		rs.leases = runlease.NewMemory()
	}
	return rs.leases
}

// grantLease records a run marotte put on the wire, between `new` and `invoke`, before anything executes; host is a
// parentless launch's host-session security input, nil for a chat-parented run. Unattended only for a scheduled run.
// A persist failure is logged: the in-memory lease still bounds the run.
func (rs *Runs) grantLease(ctx context.Context, workflowID, recipe string, o launchOrigin, host *runlease.HostSecurity) {
	rs.grantKeeping(ctx, workflowID, recipe, &o, host, nil)
}

// leaseKeep is what a grant records of the run's keep facts.
type leaseKeep struct {
	// chats hold the run's keep fact, recorded as the lease's KeepChats.
	chats []string
	// unattributed records a run whose launching chat could not be named (observeStart).
	unattributed bool
}

// grantKeeping is grantLease recording keep, the run's keep holders, on the lease; nil records none.
func (rs *Runs) grantKeeping(
	ctx context.Context, workflowID, recipe string, o *launchOrigin, host *runlease.HostSecurity, keep *leaseKeep,
) {
	if !ids.ValidChatID(workflowID) {
		slog.Warn("run lease not granted: the workflow id is not a plain name", "workflow_id", logsafe.Field(workflowID))
		return
	}
	l := runlease.Lease{
		StartedAt:    time.Now(),
		SlotAt:       o.slotAt,
		WorkflowID:   workflowID,
		ChatID:       o.chatID,
		Recipe:       recipe,
		Origin:       o.origin,
		ScheduleID:   o.scheduleRow().ID,
		ScheduleLife: o.scheduleRow().Life,
		Unattended:   o.origin == runlease.OriginScheduled,
		Host:         host,
	}
	if keep != nil {
		l.KeepChats, l.Unattributed = keep.chats, keep.unattributed
	}
	if err := rs.leaseStore().Put(ctx, &l); err != nil {
		slog.Error("run lease not persisted; this run's envelope will not survive a restart",
			"workflow_id", workflowID, "recipe", recipe, "origin", o.origin, "error", err)
	}
	rs.beginLife(workflowID)
	run := scheduleRun(&l)
	rs.writeSchedule(run, func() error { return rs.schedules.RecordStart(ctx, run) })
	// Named from the grant on, so every pending_snapshot names every run marotte answers for.
	rs.cellFor(workflowID)
}

// Idempotent: the terminal frame and cancel both release. Every path that ends a run comes here, so
// the run's asks end here too, after the store forgot the lease.
func (rs *Runs) releaseLease(ctx context.Context, workflowID string) {
	if workflowID == "" {
		return
	}
	l, held := rs.lease(workflowID)
	if err := rs.leaseStore().Release(ctx, workflowID); err != nil {
		slog.Warn("run lease not released on disk", "workflow_id", workflowID, "error", err)
	}
	if held && l.Unattributed {
		rs.marks.handOff(workflowID, l.ParentSessionID)
	}
	rs.marks.forget(workflowID)
	rs.endLife(workflowID)
	rs.endAsks(ctx, workflowID)
	rs.quietEdge()
	if held && rs.coord != nil {
		rs.coord.releaseIfUnheld(marotte.ChatID(l.ChatID))
	}
}

// recordParentSession stamps the run's parent session on its lease, the one host its asks are read through.
func (rs *Runs) recordParentSession(ctx context.Context, workflowID, session string) {
	if session == "" {
		return
	}
	l, held := rs.lease(workflowID)
	if !held || l.ParentSessionID == session {
		return
	}
	if err := rs.leaseStore().SetParentSessionID(ctx, workflowID, session); err != nil {
		slog.Warn("a run's parent session was not recorded on disk", "workflow_id", workflowID, "error", err)
	}
}

// originFromLease routes a verb on a leased run from its lease alone. A lease with no recorded parent session names no
// host, so the verb is refused. Only a run with no lease is looked up in the run list.
func (rs *Runs) originFromLease(ctx context.Context, workflowID string) runOrigin {
	l, held := rs.lease(workflowID)
	if !held {
		return rs.originOf(ctx, workflowID)
	}
	if l.ParentSessionID == "" {
		return runOrigin{err: fmt.Errorf("%w: the run's lease records no launching session", errLaunchSessionUnavailable)}
	}
	return runOrigin{
		parent: runParent{chat: marotte.ChatID(l.ChatID), session: l.ParentSessionID},
		recipe: l.Recipe,
	}
}

func (rs *Runs) lease(workflowID string) (runlease.Lease, bool) {
	return rs.leaseStore().Get(workflowID)
}

// autoResumeStopped reports whether marotte stopped resuming the run on its own (stopAutoResume).
func (rs *Runs) autoResumeStopped(workflowID string) bool {
	l, held := rs.lease(workflowID)
	return held && !l.AutoResumeStoppedAt.IsZero()
}

// stopAutoResume records that marotte no longer resumes the paused run on its own, until a resume or pause it did
// not write moves the run (endAutoResumeStop). The record lands after KAS's pause frame, so it is announced, also
// on a failed write: the store keeps the stamp in memory, where the run read finds it.
func (rs *Runs) stopAutoResume(ctx context.Context, workflowID string) error {
	err := rs.leaseStore().SetAutoResumeStoppedAt(ctx, workflowID, time.Now())
	rs.announcePaused(ctx, workflowID)
	return err
}

// endAutoResumeStop drops a run's stop and the spent heal budget behind it, reporting whether it had one: the run
// moved by someone else's hand, so its next involuntary pause is resumed again.
func (rs *Runs) endAutoResumeStop(ctx context.Context, workflowID string) bool {
	if !rs.autoResumeStopped(workflowID) {
		return false
	}
	rs.clearHeals(workflowID)
	if err := rs.leaseStore().SetAutoResumeStoppedAt(ctx, workflowID, time.Time{}); err != nil {
		slog.Warn("a run marotte stopped resuming was moved, but the cleared stop was not recorded on disk",
			"workflow_id", workflowID, "error", err)
	}
	return true
}

// announcePaused sends the paused run's chat a run-level `paused` frame, on which the run view reads the run again:
// a change to marotte's own record of the run comes with no frame from KAS.
func (rs *Runs) announcePaused(ctx context.Context, workflowID string) {
	l, held := rs.lease(workflowID)
	if !held {
		return
	}
	rs.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventRunProgress, marotte.ChatID(l.ChatID),
		marotte.RunProgressPayload{WorkflowID: workflowID, Kind: marotte.RunProgressPaused}))
}

// endAutoResumeStopAfter is endAutoResumeStop once a person's write succeeded, returning its error.
func (rs *Runs) endAutoResumeStopAfter(ctx context.Context, workflowID string, err error) error {
	if err == nil {
		rs.endAutoResumeStop(ctx, workflowID)
	}
	return err
}

// rewindCancelWait bounds a rewind's wait for a cancelled run's lease; KAS stops at the next node
// boundary, so it covers a tool call finishing. A var for tests.
var rewindCancelWait = 20 * time.Second

const rewindCancelPoll = 50 * time.Millisecond

// LiveRuns filters workflowIDs to those still leased, labelled by display name, satisfying command.runCutter.
func (rs *Runs) LiveRuns(workflowIDs []string) []command.LiveRunRef {
	var live []command.LiveRunRef
	for _, id := range workflowIDs {
		if l, ok := rs.lease(id); ok {
			live = append(live, command.LiveRunRef{ID: id, Label: rs.leasedRunName(&l)})
		}
	}
	return live
}

// CancelRun cancels one run for the reader and waits for its lease release. command.ErrRunStillLive
// when the wait runs out; the cancel landed either way.
func (rs *Runs) CancelRun(ctx context.Context, workflowID string) error {
	if err := rs.cancelAs(ctx, workflowID, userStop(stopWhyRewound)); err != nil {
		return err
	}
	deadline := time.NewTimer(rewindCancelWait)
	defer deadline.Stop()
	tick := time.NewTicker(rewindCancelPoll)
	defer tick.Stop()
	for {
		if _, held := rs.lease(workflowID); !held {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return command.ErrRunStillLive
		case <-tick.C:
		}
	}
}
