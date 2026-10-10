package agent

// The runtime side of a run lease: is a blocking row marotte's orphan, when must the run end, is it
// unattended, and which schedule row gets the outcome.

import (
	"context"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/schedule"
)

// A struct because the scheduled fields only make sense together.
type launchOrigin struct {
	// slotAt is this run's next slot, an input to the deadline; launch fills it for a manual run (manualSlot).
	slotAt     time.Time
	scheduleID string
	// chatID is the launching chat for an agent's run (runStartLaunch), empty for a parentless launch.
	chatID string
	origin runlease.Origin
}

// manualLaunch is the Run button and retry: attended, no schedule row. The slot depends on the resolved recipe, so launch sets it.
func manualLaunch() launchOrigin {
	return launchOrigin{origin: runlease.OriginManual}
}

// scheduledLaunch carries the schedule row for outcomes and the slot the run may not outlive.
func scheduledLaunch(scheduleID string, slotAt time.Time) launchOrigin {
	return launchOrigin{origin: runlease.OriginScheduled, scheduleID: scheduleID, slotAt: slotAt}
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
				"is bounded by its idle window alone", "schedule_id", e.ID, "error", err)
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

// grantLease records a run's envelope between `new` and `invoke`, before anything executes. Unattended
// only for a scheduled run. A persist failure is logged: the in-memory lease still bounds the run.
func (rs *Runs) grantLease(ctx context.Context, workflowID, recipe string, o launchOrigin) {
	if workflowID == "" {
		return
	}
	l := runlease.Lease{
		StartedAt:  time.Now(),
		SlotAt:     o.slotAt,
		WorkflowID: workflowID,
		ChatID:     o.chatID,
		Recipe:     recipe,
		Origin:     o.origin,
		ScheduleID: o.scheduleID,
		Unattended: o.origin == runlease.OriginScheduled,
	}
	if err := rs.leaseStore().Put(ctx, &l); err != nil {
		slog.Error("run lease not persisted; this run's envelope will not survive a restart",
			"workflow_id", workflowID, "recipe", recipe, "origin", o.origin, "error", err)
	}
}

// Idempotent: the terminal frame and cancel both release.
func (rs *Runs) releaseLease(ctx context.Context, workflowID string) {
	if workflowID == "" {
		return
	}
	if err := rs.leaseStore().Release(ctx, workflowID); err != nil {
		slog.Warn("run lease not released on disk", "workflow_id", workflowID, "error", err)
	}
}

func (rs *Runs) lease(workflowID string) (runlease.Lease, bool) {
	return rs.leaseStore().Get(workflowID)
}

// rewindCancelWait bounds a rewind's wait for a cancelled run's lease; KAS stops at the next node
// boundary, so it covers a tool call finishing. A var for tests.
var rewindCancelWait = 20 * time.Second

const rewindCancelPoll = 50 * time.Millisecond

// LiveRuns filters workflowIDs to those still leased, labelled by recipe, satisfying command.runCutter.
func (rs *Runs) LiveRuns(workflowIDs []string) []command.LiveRunRef {
	var live []command.LiveRunRef
	for _, id := range workflowIDs {
		if l, ok := rs.lease(id); ok {
			live = append(live, command.LiveRunRef{ID: id, Label: l.Recipe})
		}
	}
	return live
}

// CancelRun cancels one run for the reader and waits for its lease release. command.ErrRunStillLive
// when the wait runs out; the cancel landed either way.
func (rs *Runs) CancelRun(ctx context.Context, workflowID string) error {
	if err := rs.cancelOn(ctx, workflowID, userStop(stopWhyRewound), nil); err != nil {
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

// RunChat answers which chat launched a run (command.runOwner), so an orphan run tab nests under its
// conversation. ok reports whether a lease exists: a parentless run has one and no chat. Lock order
// Membership.mu -> Runs.mu -> the lease store's.
func (rs *Runs) RunChat(workflowID string) (marotte.ChatID, bool) {
	l, ok := rs.lease(workflowID)
	if !ok {
		return "", false
	}
	return marotte.ChatID(l.ChatID), true
}
