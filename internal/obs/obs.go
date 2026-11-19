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

	"github.com/cplieger/go/measure/v2"
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
var promptFailureClasses = []string{
	"fatal", "pipe_death", "busy", "transient", "throttled", "rejected", "auth", "signin_outage",
}

// signInStates are the names marotte.SignInState gives the Kiro sign-in's state.
var signInStates = []string{"ok", "failing", "signed_out", "none"}

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

// The answers the floor gives an automatic Bot turn's permission ask for the absent user.
const (
	BotAnswerRefused  = "refused"
	BotAnswerApproved = "approved"
)

var botAnswers = []string{BotAnswerRefused, BotAnswerApproved}

// The reasons an unattended run fails.
const (
	// RunNeedsApproval is a permission ask refused because nobody was there to approve it.
	RunNeedsApproval = "needs_approval"
	// RunOverran is a scheduled run still going when its next slot came due.
	RunOverran = "overran"
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
	RunNeedsApproval, RunOverran, RunStalled, RunPaused, RunOrphaned, RunCancelGivenUp,
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

// ruleTriggers are internal/rule's built trigger kinds plus "manual", a Run now press, which belongs to no trigger.
var ruleTriggers = []string{"every", "at", "manual"}

// The outcomes of a rule's evaluation.
const (
	RuleFired   = "fired"
	RuleSkipped = "skipped"
)

var ruleEvaluationOutcomes = []string{RuleFired, RuleSkipped}

// The outcomes of one delivery try of a rule's prompt.
const (
	RuleDeliveryDelivered = "delivered"
	RuleDeliveryRetry     = "retry"
	// RuleDeliveryFailed is a prompt dropped after its last try, one the door refused as no retry can pass, or an
	// event whose evaluation panicked or whose prompt did not render.
	RuleDeliveryFailed = "failed"
	// RuleDeliveryDropped is a prompt dropped for a Bot closed or deleted under it, or a queued prompt the user
	// discarded before its turn.
	RuleDeliveryDropped = "dropped"
)

var ruleDeliveryOutcomes = []string{RuleDeliveryDelivered, RuleDeliveryRetry, RuleDeliveryFailed, RuleDeliveryDropped}

// The label names more than one family carries.
const (
	labelReason = "reason"
	labelRuleID = "rule_id"
)

var buildInfo = measure.NewInfo("build_info", "Build information, always 1.", []string{"version"})

var (
	engineExits = measure.NewLabeledCounter("engine_exits_total",
		"Agent engine exits, by the cause the exit line names.", measure.UnitCount, []string{"cause"},
		measure.WithSeed(each(engineExitCauses)...))
	promptFailures = measure.NewLabeledCounter("prompt_failures_total",
		"Failed prompts, failed empty-turn retries included, by failure class; "+
			"pipe_death is a prompt the engine's own exit ended, signin_outage one refused at once during a known "+
			"Kiro sign-in outage.", measure.UnitCount, []string{"class"},
		measure.WithSeed(each(promptFailureClasses)...))
	signInSaveFailures = measure.NewLabeledCounter("signin_save_failures_total",
		"Sign-in changes the sign-in file could not take, by backend and reason.", measure.UnitCount,
		[]string{"backend", labelReason})
	signInState = measure.NewLabeledGauge("signin_state",
		"1 for the Kiro sign-in's current state and 0 for the others: ok, failing (renewals have failed for 2 "+
			"minutes and the identity provider is unreachable), signed_out (the sign-in ended), none (no account).",
		measure.UnitNone, []string{"state"}, measure.WithSeed(each(signInStates)...))
	engineUp = measure.NewGauge("engine_up", "1 while the agent engine is ready to take prompts, else 0.",
		measure.UnitNone)
	// The memory gauges carry no label; a labeled gauge is what lets Delete take a series off the page.
	engineResident = measure.NewLabeledGauge("engine_resident_memory_bytes",
		"Resident memory of the running agent engine's node process at its last sample.", measure.UnitBytes, nil)
	engineHeapLimit = measure.NewLabeledGauge("engine_heap_limit_bytes",
		"JavaScript heap limit of the running agent engine; absent while the limit is unknown.", measure.UnitBytes, nil)
	unattendedRunFailures = measure.NewLabeledCounter("unattended_run_failures_total",
		"Scheduled and triggered runs that failed with nobody watching, by origin and reason.", measure.UnitCount,
		[]string{"origin", labelReason}, measure.WithSeed(product(unattendedOrigins, unattendedRunFailureReasons)...))
	unattendedApprovals = measure.NewLabeledCounter("unattended_approvals_total",
		"Permission asks of scheduled and triggered runs approved automatically, by origin.", measure.UnitCount,
		[]string{"origin"}, measure.WithSeed(each(unattendedOrigins)...))
	botUnattendedAnswers = measure.NewLabeledCounter("bot_unattended_answers_total",
		"Permission asks of automatic Bot turns answered with nobody watching, by answer.", measure.UnitCount,
		[]string{"answer"}, measure.WithSeed(each(botAnswers)...))
	slotsNotStarted = measure.NewLabeledCounter("schedule_slots_not_started_total",
		"Schedule slots that produced no run, by what stopped them.", measure.UnitCount, []string{"event"},
		measure.WithSeed(each(slotNotStartedEvents)...))
	scheduleFailedSlots = measure.NewLabeledGauge("schedule_failed_slots",
		"Consecutive slots of an enabled schedule that ended without a completed run.", measure.UnitCount,
		[]string{labelRuleID})
	scheduleNextDue = measure.NewLabeledGauge("schedule_next_due_timestamp_seconds",
		"When an enabled schedule's next slot not yet judged falls due, or the end of its wait on the agent engine, "+
			"in Unix seconds.", measure.UnitSeconds, []string{labelRuleID})
	triggerBlocked = measure.NewLabeledGauge("trigger_rule_blocked",
		"1 while an enabled event rule launches nothing until someone acts, by reason, else 0.", measure.UnitNone,
		[]string{labelRuleID, labelReason})
	triggerQueueFull = measure.NewLabeledGauge("trigger_queue_full",
		"1 while an enabled event rule's last poll left an event at the forge for its full queue, else 0.",
		measure.UnitNone, []string{labelRuleID})
	triggerPollFailures = measure.NewLabeledGauge("trigger_poll_failures",
		"Consecutive polls in which an enabled event rule's read of its repository failed.", measure.UnitCount,
		[]string{labelRuleID})
	triggerGaps = measure.NewLabeledGauge("trigger_catch_up_gaps",
		"Undismissed time ranges an enabled event rule could not read back, so events in them may never launch.",
		measure.UnitCount, []string{labelRuleID})
	ruleEvaluations = measure.NewLabeledCounter("rule_evaluations_total",
		"Evaluations of a Bot's rules, by trigger kind and outcome.", measure.UnitCount,
		[]string{"trigger", "outcome"}, measure.WithSeed(product(ruleTriggers, ruleEvaluationOutcomes)...))
	ruleDeliveries = measure.NewLabeledCounter("rule_deliveries_total",
		"Delivery tries of a Bot's rule prompts, by outcome.", measure.UnitCount, []string{"outcome"},
		measure.WithSeed(each(ruleDeliveryOutcomes)...))
	ruleQueueEvents = measure.NewGauge("rule_queue_events",
		"Events waiting in the queues of every Bot's rules.", measure.UnitCount)
	ruleWriteFailing = measure.NewGauge("rule_engine_write_failing",
		"1 while the rule engine cannot write a Bot's record and has stopped, else 0.", measure.UnitNone)
)

func init() {
	registry.MustRegister(buildInfo, engineExits, promptFailures, signInSaveFailures, signInState, engineUp, engineResident,
		engineHeapLimit, unattendedRunFailures, unattendedApprovals, botUnattendedAnswers, slotsNotStarted, scheduleFailedSlots,
		scheduleNextDue, triggerBlocked, triggerQueueFull, triggerPollFailures, triggerGaps, ruleEvaluations,
		ruleDeliveries, ruleQueueEvents, ruleWriteFailing)
	buildInfo.Set(version.Build)
}

// each is one label set per value, for a one-label seed.
func each(values []string) [][]string {
	sets := make([][]string, 0, len(values))
	for _, v := range values {
		sets = append(sets, []string{v})
	}
	return sets
}

// product is every pair of a first and a second value, for a two-label seed.
func product(first, second []string) [][]string {
	sets := make([][]string, 0, len(first)*len(second))
	for _, f := range first {
		for _, s := range second {
			sets = append(sets, []string{f, s})
		}
	}
	return sets
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

// SignInState sets marotte_signin_state: 1 for state, one of signInStates, and 0 for the others.
func SignInState(state string) {
	for _, st := range signInStates {
		v := 0.0
		if st == state {
			v = 1
		}
		signInState.Set(v, st)
	}
}

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

// BotUnattendedAnswer counts one permission ask of an automatic Bot turn answered for the absent user, answer one of
// BotAnswerRefused and BotAnswerApproved.
func BotUnattendedAnswer(answer string) { botUnattendedAnswers.Inc(answer) }

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

// RuleEvaluated counts one evaluation of a rule from trigger, a trigger kind or "manual", under outcome, RuleFired
// or RuleSkipped.
func RuleEvaluated(trigger, outcome string) { ruleEvaluations.Inc(trigger, outcome) }

// RuleDelivery counts one delivery try under outcome, one of the RuleDelivery outcomes above.
func RuleDelivery(outcome string) { ruleDeliveries.Inc(outcome) }

// RuleQueueEvents sets the events waiting over every Bot's rules.
func RuleQueueEvents(n int) { ruleQueueEvents.Set(float64(n)) }

// RuleEngineWriteFailing sets marotte_rule_engine_write_failing: 1 while the engine's writes fail, else 0.
func RuleEngineWriteFailing(failing bool) { ruleWriteFailing.Set(flag(failing)) }

func flag(on bool) float64 {
	if on {
		return 1
	}
	return 0
}
