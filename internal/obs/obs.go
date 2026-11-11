// Package obs owns marotte's Prometheus metrics and the registry that serves
// them, with the exposition prefix "marotte_". The registry is unexported, so
// a caller can scrape the page but cannot register, rename or delete a series.
//
// Each counter is pre-set at zero for every value of its labels, so increase()
// sees the first event after a start. The values are the owning package's
// names; a test there pins them to the lists below.
package obs

import (
	"net/http"
	"slices"
	"time"

	"github.com/cplieger/go/measure"
	"github.com/cplieger/marotte/internal/version"
)

var registry = measure.NewRegistry("marotte")

// requestedStopCauses are the names internal/agent's exitCause gives a stop marotte asked for.
var requestedStopCauses = []string{"restart_confirmed", "restart_auto", "shutdown", "stopped"}

// engineExitCauses are the names internal/agent's exitCause gives an engine's end.
var engineExitCauses = slices.Concat(requestedStopCauses,
	[]string{"stdin_stalled", "parse_breaker", "lane_overflow", "external_stop", "crash"})

// RequestedStop reports whether an engine exit cause names a stop marotte asked for, a restart or a shutdown. Every
// other cause is an end marotte did not ask for.
func RequestedStop(cause string) bool { return slices.Contains(requestedStopCauses, cause) }

// promptFailureClasses are the names internal/command gives a failed prompt's class.
var promptFailureClasses = []string{"fatal", "pipe_death", "busy", "transient", "throttled", "rejected", "auth"}

// The reasons a sign-in save fails.
const (
	// SignInStagedCopyLeft is a save refused because an earlier save's staged copy is still on disk.
	SignInStagedCopyLeft = "staged_copy_left"
	// SignInWriteFailed is a save whose write to the sign-in file failed.
	SignInWriteFailed = "write_failed"
)

var signInSaveReasons = []string{SignInStagedCopyLeft, SignInWriteFailed}

// unattendedOrigins are the runlease origins internal/agent leases as unattended.
var unattendedOrigins = []string{"scheduled", "triggered"}

// The reasons an unattended run fails.
const (
	// RunNeedsApproval is a permission ask refused because nobody was there to approve it.
	RunNeedsApproval = "needs_approval"
	// RunOverran is a scheduled run still going when its next slot came due.
	RunOverran = "overran"
	// RunBackstop is a run that spent its absolute executing-time budget.
	RunBackstop = "backstop"
	// RunStalled is a run that made no progress inside its idle window.
	RunStalled = "stalled"
	// RunPaused is a run left paused with nobody to resume it.
	RunPaused = "paused"
	// RunOrphaned is a run a restart orphaned.
	RunOrphaned = "orphaned"
	// RunCancelGivenUp is a stop marotte decided whose every cancel kiro-cli refused.
	RunCancelGivenUp = "cancel_given_up"
)

var unattendedRunFailureReasons = []string{
	RunNeedsApproval, RunOverran, RunBackstop, RunStalled, RunPaused, RunOrphaned, RunCancelGivenUp,
}

// The events that leave a schedule slot with no run.
const (
	// SlotLaunchFailed is a launch that returned an error.
	SlotLaunchFailed = "launch_failed"
	// SlotSignInHeld is a slot the sign-in gate held until its grace ran out.
	SlotSignInHeld = "signin_held"
	// SlotEngineBusy is a slot the agent engine did not answer before the schedule's next slot.
	SlotEngineBusy = "engine_busy"
)

var slotNotStartedEvents = []string{SlotLaunchFailed, SlotSignInHeld, SlotEngineBusy}

// The label names more than one family carries.
const (
	labelReason = "reason"
	labelRuleID = "rule_id"
)

var buildInfo = measure.NewLabeledGauge("build_info", "Build information, always 1.", []string{"version"})

var (
	engineExits = measure.NewLabeledCounter("engine_exits_total",
		"Agent engine exits, by the cause the exit line names.", []string{"cause"})
	promptFailures = measure.NewLabeledCounter("prompt_failures_total",
		"Failed prompts, failed empty-turn retries included, by failure class; "+
			"pipe_death is a prompt the engine's own exit ended.", []string{"class"})
	signInSaveFailures = measure.NewLabeledCounter("signin_save_failures_total",
		"Sign-in changes the sign-in file could not take, by backend and reason.", []string{"backend", labelReason})
	engineUp = measure.NewGauge("engine_up", "1 while the agent engine is ready to take prompts, else 0.")
	// The memory gauges carry no label; a labeled gauge is what lets Delete take a series off the page.
	engineResident = measure.NewLabeledGauge("engine_resident_memory_bytes",
		"Resident memory of the running agent engine's node process at its last sample.", nil)
	engineHeapLimit = measure.NewLabeledGauge("engine_heap_limit_bytes",
		"JavaScript heap limit of the running agent engine; absent while the limit is unknown.", nil)
	unattendedRunFailures = measure.NewLabeledCounter("unattended_run_failures_total",
		"Scheduled and triggered runs that failed with nobody watching, by origin and reason.",
		[]string{"origin", labelReason})
	unattendedApprovals = measure.NewLabeledCounter("unattended_approvals_total",
		"Permission asks of scheduled and triggered runs approved automatically, by origin.", []string{"origin"})
	slotsNotStarted = measure.NewLabeledCounter("schedule_slots_not_started_total",
		"Schedule slots that produced no run, by what stopped them.", []string{"event"})
	scheduleFailedSlots = measure.NewLabeledGauge("schedule_failed_slots",
		"Consecutive slots of an enabled schedule that ended without a completed run.", []string{labelRuleID})
	scheduleNextDue = measure.NewLabeledGauge("schedule_next_due_timestamp_seconds",
		"When an enabled schedule's next slot not yet judged falls due, or the end of its wait on the agent engine, "+
			"in Unix seconds.", []string{labelRuleID})
	triggerBlocked = measure.NewLabeledGauge("trigger_rule_blocked",
		"1 while an enabled event rule launches nothing until someone acts, by reason, else 0.",
		[]string{labelRuleID, labelReason})
	triggerQueueFull = measure.NewLabeledGauge("trigger_queue_full",
		"1 while an enabled event rule's last poll left an event at the forge for its full queue, else 0.",
		[]string{labelRuleID})
	triggerPollFailures = measure.NewLabeledGauge("trigger_poll_failures",
		"Consecutive polls in which an enabled event rule's read of its repository failed.", []string{labelRuleID})
	triggerGaps = measure.NewLabeledGauge("trigger_catch_up_gaps",
		"Undismissed time ranges an enabled event rule could not read back, so events in them may never launch.",
		[]string{labelRuleID})
)

func init() {
	registry.MustRegister(buildInfo, engineExits, promptFailures, signInSaveFailures, engineUp, engineResident,
		engineHeapLimit, unattendedRunFailures, unattendedApprovals, slotsNotStarted, scheduleFailedSlots,
		scheduleNextDue, triggerBlocked, triggerQueueFull, triggerPollFailures, triggerGaps)
	buildInfo.Set(1, version.Build)
	for _, c := range engineExitCauses {
		engineExits.Add(0, c)
	}
	for _, c := range promptFailureClasses {
		promptFailures.Add(0, c)
	}
	for _, o := range unattendedOrigins {
		for _, r := range unattendedRunFailureReasons {
			unattendedRunFailures.Add(0, o, r)
		}
		unattendedApprovals.Add(0, o)
	}
	for _, e := range slotNotStartedEvents {
		slotsNotStarted.Add(0, e)
	}
}

// Handler serves the Prometheus text exposition: every marotte series plus the
// Go and process families measure adds to every registry. It is the page's
// only writer, because a second writer of those families repeats their TYPE
// lines and Prometheus then rejects the whole scrape.
func Handler() http.Handler {
	return registry.Handler()
}

// EngineExited counts one engine exit under cause, one of engineExitCauses.
func EngineExited(cause string) { engineExits.Inc(cause) }

// PromptFailed counts one failed prompt under class, one of promptFailureClasses.
func PromptFailed(class string) { promptFailures.Inc(class) }

// SignInBackend pre-sets the sign-in save failure series of a backend at zero, for every reason.
func SignInBackend(backend string) {
	for _, r := range signInSaveReasons {
		signInSaveFailures.Add(0, backend, r)
	}
}

// SignInSaveFailed counts one failed sign-in save of backend under reason, SignInStagedCopyLeft or
// SignInWriteFailed.
func SignInSaveFailed(backend, reason string) { signInSaveFailures.Inc(backend, reason) }

// EngineUp sets marotte_engine_up: 1 when the engine is ready, else 0.
func EngineUp(ready bool) {
	v := 0.0
	if ready {
		v = 1
	}
	engineUp.Set(v)
}

// EngineMemory records the running engine's last memory sample. A heapLimitBytes of 0 or less is an unknown limit and
// removes the limit series, so a ratio over it has nothing to divide by.
func EngineMemory(residentBytes, heapLimitBytes int64) {
	engineResident.Set(float64(residentBytes))
	if heapLimitBytes <= 0 {
		engineHeapLimit.Delete()
		return
	}
	engineHeapLimit.Set(float64(heapLimitBytes))
}

// UnattendedRunFailed counts one failure of an unattended run of origin, one of unattendedOrigins, under reason, one
// of the Run reasons above.
func UnattendedRunFailed(origin, reason string) { unattendedRunFailures.Inc(origin, reason) }

// UnattendedApproved counts one permission ask of an unattended run of origin approved with nobody watching.
func UnattendedApproved(origin string) { unattendedApprovals.Inc(origin) }

// ScheduleSlotNotStarted counts one schedule slot that produced no run, under event, one of the Slot events above.
func ScheduleSlotNotStarted(event string) { slotsNotStarted.Inc(event) }

// Schedule exports schedule ruleID's consecutive failed slots and its next due slot. A zero nextDue, a slot nobody
// can name, removes the next-due series, since a 0 would read as a slot long overdue. nextDue is past for up to a
// tick after a slot falls due, so a rule over it needs a for: longer than a minute.
func Schedule(ruleID string, failedSlots int, nextDue time.Time) {
	scheduleFailedSlots.Set(float64(failedSlots), ruleID)
	if nextDue.IsZero() {
		scheduleNextDue.Delete(ruleID)
		return
	}
	scheduleNextDue.Set(float64(nextDue.Unix()), ruleID)
}

// ScheduleBlocked exports schedule ruleID, which cannot fire, as due at since and removes its failed-slot count,
// which nobody can read.
func ScheduleBlocked(ruleID string, since time.Time) {
	scheduleFailedSlots.Delete(ruleID)
	scheduleNextDue.Set(float64(since.Unix()), ruleID)
}

// ScheduleGone removes schedule ruleID's series, for a schedule disabled or deleted. It deletes the one
// rule's series, never resets the family, so a scrape never finds another schedule's series missing.
func ScheduleGone(ruleID string) {
	scheduleFailedSlots.Delete(ruleID)
	scheduleNextDue.Delete(ruleID)
}

// EngineMemoryGone removes both engine memory series, for an engine that is no longer running.
func EngineMemoryGone() {
	engineResident.Delete()
	engineHeapLimit.Delete()
}

// TriggerRuleState is what an enabled event rule's series show.
type TriggerRuleState struct {
	// PollFailures is the failed reads in a row of the rule's repository.
	PollFailures int
	// CatchUpGaps is the rule's gaps nobody has dismissed yet.
	CatchUpGaps int
	// LaunchFailed is a rule its launch failures blocked until it is edited or switched off and on.
	LaunchFailed bool
	// UnnamedRun is a rule held past its confirmation bound by a run no rule has a record of launching.
	UnnamedRun bool
	// TooManyItems is a labeled rule matching more open items than it can track.
	TooManyItems bool
	// StateUnreadable is a rule whose saved state does not load.
	StateUnreadable bool
	// StateUnwritten is a rule whose saved state marotte cannot write.
	StateUnwritten bool
	// QueueFull is a rule whose last poll left an event at the forge for its full queue.
	QueueFull bool
}

type blockReason struct {
	reason string
	on     bool
}

func (st *TriggerRuleState) reasons() []blockReason {
	return []blockReason{
		{"launch_failed", st.LaunchFailed},
		{"unnamed_run", st.UnnamedRun},
		{"too_many_items", st.TooManyItems},
		{"state_unreadable", st.StateUnreadable},
		{"state_unwritten", st.StateUnwritten},
	}
}

// TriggerRule exports event rule ruleID's state, every blocked reason included at 0 or 1.
func TriggerRule(ruleID string, st *TriggerRuleState) {
	for _, r := range st.reasons() {
		triggerBlocked.Set(flag(r.on), ruleID, r.reason)
	}
	triggerQueueFull.Set(flag(st.QueueFull), ruleID)
	triggerPollFailures.Set(float64(st.PollFailures), ruleID)
	triggerGaps.Set(float64(st.CatchUpGaps), ruleID)
}

// TriggerRuleGone removes event rule ruleID's series, for a rule disabled, deleted or no longer an event rule. It
// deletes the one rule's series, so a scrape never finds another rule's missing.
func TriggerRuleGone(ruleID string) {
	for _, r := range (&TriggerRuleState{}).reasons() {
		triggerBlocked.Delete(ruleID, r.reason)
	}
	triggerQueueFull.Delete(ruleID)
	triggerPollFailures.Delete(ruleID)
	triggerGaps.Delete(ruleID)
}

func flag(on bool) float64 {
	if on {
		return 1
	}
	return 0
}
