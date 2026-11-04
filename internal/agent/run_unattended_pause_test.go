package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/schedule"
	"github.com/cplieger/marotte/internal/signin"
)

const stepQuestionReason = "Step requested user input via send_message."

// fastUnattendedPause shortens the budget a scheduled run's pause gets before it is ended.
func fastUnattendedPause(t *testing.T) {
	t.Helper()
	prev := unattendedPauseBudget
	unattendedPauseBudget = 20 * time.Millisecond
	t.Cleanup(func() { unattendedPauseBudget = prev })
}

// TestScheduledRun_APauseNobodyResumesEndsTheRun pins that a scheduled run never waits paused on nobody: a pause no
// person owns ends the run at the budget, long before its next slot, and says so on the row and in the log.
func TestScheduledRun_APauseNobodyResumesEndsTheRun(t *testing.T) {
	for name, tc := range map[string]struct {
		inspect func(t *testing.T) json.RawMessage
		// wantQuoted is the KAS pause reason the row must quote; "" for a fault, which gets reasonPausedFault.
		wantQuoted string
	}{
		"a step's question": {
			inspect:    func(t *testing.T) json.RawMessage { return inspectPaused(t, gateRunID, stepQuestionReason) },
			wantQuoted: stepQuestionReason,
		},
		"a repeat at its pass limit": {
			inspect: func(t *testing.T) json.RawMessage {
				return inspectPaused(t, gateRunID, "Repeat 'implement' reached maxIterations.")
			},
			wantQuoted: "Repeat 'implement' reached maxIterations.",
		},
		"a fault marotte does not resume": {
			inspect:    func(t *testing.T) json.RawMessage { return inspectPaused(t, gateRunID, permanentReason) },
			wantQuoted: permanentReason,
		},
		"a fault no heal cleared": {
			inspect: func(t *testing.T) json.RawMessage { return inspectPaused(t, gateRunID, interruptedReason) },
		},
	} {
		t.Run(name, func(t *testing.T) {
			logs := captureLogs(t)
			fastUnattendedPause(t)
			h, br, st, entry := scheduledHub(t)
			br.callResults[methodKiroWorkflowInspect] = tc.inspect(t)
			leaseScheduledWithSlot(t, h, br, &entry, gateRunID, time.Hour)
			h.runs.armDeadline(t.Context(), gateRunID)

			pauseRun(t, h, gateRunID, stepQuestionReason)

			if got := waitForEndReason(h, gateRunID, 5*time.Second); got != runEndUnattended {
				t.Fatalf("a scheduled run paused with nobody to resume it recorded %q, want %q; calls were %v",
					got, runEndUnattended, br.callLog())
			}
			if !slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
				t.Errorf("no cancel went out for the run: %v", br.callLog())
			}
			if out := logs.String(); !strings.Contains(out, logMsgRunPausedUnattended) {
				t.Errorf("the end did not log %q, which its alert keys on: %s", logMsgRunPausedUnattended, out)
			}
			row := awaitEndedRow(t, st)
			if tc.wantQuoted == "" {
				if row.LastStatus != schedule.StatusFailed || row.LastReason != reasonPausedFault {
					t.Errorf("the row reads (%q, %q), want (failed, %q)", row.LastStatus, row.LastReason, reasonPausedFault)
				}
				return
			}
			checkPauseQuoted(t, &row, tc.wantQuoted)
		})
	}
}

// checkPauseQuoted asserts the row says the run was cancelled with nobody watching and quotes KAS's pause reason,
// the only text that says which pause it was.
func checkPauseQuoted(t *testing.T, row *schedule.Entry, kasReason string) {
	t.Helper()
	if row.LastStatus != schedule.StatusFailed || !strings.Contains(row.LastReason, "nobody watching") ||
		!strings.Contains(row.LastReason, kasReason) {
		t.Errorf("the row reads (%q, %q), want failed, saying nobody was watching and quoting %q",
			row.LastStatus, row.LastReason, kasReason)
	}
}

// unattendedEndLines is every logMsgRunPausedUnattended record the capture holds.
func unattendedEndLines(logs *logCapture) []map[string]any {
	var lines []map[string]any
	for line := range strings.SplitSeq(logs.String(), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil && rec["msg"] == logMsgRunPausedUnattended {
			lines = append(lines, rec)
		}
	}
	return lines
}

// TestScheduledRun_AFaultPastItsHealsEndsTheRun pins that a scheduled run whose heals ran out is ended at once rather
// than parked for a person who is not there, and that the row and the log say what marotte waited for: its automatic
// resumes, not the pause budget it never waited out.
func TestScheduledRun_AFaultPastItsHealsEndsTheRun(t *testing.T) {
	for name, refused := range map[string]bool{"ended at once": false, "its cancel refused once": true} {
		t.Run(name, func(t *testing.T) {
			logs := captureLogs(t)
			h, br, st, entry := scheduledHub(t)
			if refused {
				setCancelRetryDelay(h.runs, time.Millisecond)
				refuseCancelOnce(br)
			}
			br.callResults[methodKiroWorkflowInspect] = inspectPaused(t, gateRunID, interruptedReason)
			br.callResults[methodKiroWorkflowPause] = json.RawMessage(`{"paused":true}`)
			leaseScheduledWithSlot(t, h, br, &entry, gateRunID, time.Hour)
			for range maxAutoHeals {
				h.runs.claimHeal(gateRunID)
			}

			h.runs.resumeIfInterrupted(t.Context(), runChatID(gateRunID), gateRunID)

			if got := waitForEndReason(h, gateRunID, 5*time.Second); got != runEndUnattended {
				t.Fatalf("a scheduled run past its heals recorded %q, want %q; calls were %v",
					got, runEndUnattended, br.callLog())
			}
			if got := stateWrites(br.callLog()); slices.Contains(got, methodKiroWorkflowPause) {
				t.Errorf("writes = %v, want no pause: a park for the user waits on nobody", got)
			}
			heals := strconv.Itoa(maxAutoHeals) + " automatic resumes"
			row := awaitEndedRow(t, st)
			if row.LastStatus != schedule.StatusFailed || !strings.Contains(row.LastReason, heals) ||
				strings.Contains(row.LastReason, pauseBudgetText) {
				t.Errorf("the row reads (%q, %q), want failed naming %q and not the %s pause budget",
					row.LastStatus, row.LastReason, heals, pauseBudgetText)
			}
			lines := unattendedEndLines(logs)
			if len(lines) == 0 {
				t.Fatalf("the end did not log %q: %s", logMsgRunPausedUnattended, logs.String())
			}
			for _, rec := range lines {
				if rec["heals"] != float64(maxAutoHeals) || rec["budget"] != nil {
					t.Errorf("the end logged heals=%v budget=%v, want heals=%d and no budget it never waited",
						rec["heals"], rec["budget"], maxAutoHeals)
				}
			}
		})
	}
}

// TestScheduledRun_AFaultThePauseBudgetEndsLogsTheBudget pins the other path to the same ending: a fault the heals
// left paused for the whole budget names the budget on the row and in the log.
func TestScheduledRun_AFaultThePauseBudgetEndsLogsTheBudget(t *testing.T) {
	logs := captureLogs(t)
	fastUnattendedPause(t)
	h, br, st, entry := scheduledHub(t)
	br.callResults[methodKiroWorkflowInspect] = inspectPaused(t, gateRunID, interruptedReason)
	leaseScheduledWithSlot(t, h, br, &entry, gateRunID, time.Hour)
	h.runs.armDeadline(t.Context(), gateRunID)

	pauseRun(t, h, gateRunID, stepQuestionReason)

	if got := waitForEndReason(h, gateRunID, 5*time.Second); got != runEndUnattended {
		t.Fatalf("recorded %q, want %q; calls were %v", got, runEndUnattended, br.callLog())
	}
	if row := awaitEndedRow(t, st); row.LastReason != reasonPausedFault {
		t.Errorf("the row reads %q, want %q", row.LastReason, reasonPausedFault)
	}
	lines := unattendedEndLines(logs)
	if len(lines) != 1 || lines[0]["budget"] == nil || lines[0]["heals"] != nil {
		t.Errorf("end lines = %v, want one naming the budget and no heal count", lines)
	}
}

// TestScheduledRun_APauseSomeoneOwnsIsLeftToItsSlot pins the pauses the budget must not end: a person's, the sign-in
// gate's and a heal the closed gate holds each have someone to resume them, and a resume since the pause owns the run.
func TestScheduledRun_APauseSomeoneOwnsIsLeftToItsSlot(t *testing.T) {
	for name, tc := range map[string]struct {
		inspect  func(t *testing.T) json.RawMessage
		setup    func(h *Runtime)
		resumeIt bool
	}{
		"a person's pause": {
			inspect: func(t *testing.T) json.RawMessage { return heldInspect(t, "") },
		},
		"the sign-in gate's hold": {
			inspect: func(t *testing.T) json.RawMessage { return heldInspect(t, signInHoldReason) },
		},
		"a fault whose heal the closed gate holds": {
			inspect: func(t *testing.T) json.RawMessage { return inspectPaused(t, gateRunID, interruptedReason) },
			setup:   func(h *Runtime) { wireGate(h, signin.NeedsSignIn) },
		},
		"a question resumed before the budget": {
			inspect:  func(t *testing.T) json.RawMessage { return inspectPaused(t, gateRunID, stepQuestionReason) },
			resumeIt: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			prev := unattendedPauseBudget
			unattendedPauseBudget = 50 * time.Millisecond
			t.Cleanup(func() { unattendedPauseBudget = prev })
			h, br, st, entry := scheduledHub(t)
			br.callResults[methodKiroWorkflowInspect] = tc.inspect(t)
			if tc.setup != nil {
				tc.setup(h)
			}
			leaseScheduledWithSlot(t, h, br, &entry, gateRunID, time.Hour)
			h.runs.armDeadline(t.Context(), gateRunID)

			pauseRun(t, h, gateRunID, stepQuestionReason)
			if tc.resumeIt {
				h.runs.armDeadline(t.Context(), gateRunID)
			}

			if got := waitForEndReason(h, gateRunID, 300*time.Millisecond); got != "" {
				t.Errorf("the run was stopped %q; its pause has someone to resume it", got)
			}
			if slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
				t.Errorf("a cancel went out: %v", br.callLog())
			}
			if row := onlyRow(t, st); row.LastStatus != "started" {
				t.Errorf("the row reads %q, want it still started", row.LastStatus)
			}
		})
	}
}

// TestManualRun_AStepQuestionStaysPaused pins that the budget is a scheduled run's only: a person launched a manual run
// and its tab shows the question.
func TestManualRun_AStepQuestionStaysPaused(t *testing.T) {
	fastUnattendedPause(t)
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowCancel:  json.RawMessage(`{}`),
		methodKiroWorkflowInspect: inspectPaused(t, gateRunID, stepQuestionReason),
	}
	h.bridge.mgr.insert(runChatID(gateRunID), &sharedBridge{bridge: br, state: bridgeIdle})
	h.runs.grantLease(t.Context(), gateRunID, "publish", launchOrigin{origin: runlease.OriginManual}, hostSec())
	h.runs.armDeadline(t.Context(), gateRunID)

	pauseRun(t, h, gateRunID, stepQuestionReason)

	if got := waitForEndReason(h, gateRunID, 300*time.Millisecond); got != "" {
		t.Errorf("a manual run waiting on its question was stopped %q", got)
	}
}

// TestBootSweep_EndsAScheduledRunLeftPausedOnAQuestion pins that a restart cannot drop the budget: the boot pass
// watches every parked scheduled lease it reads from disk.
func TestBootSweep_EndsAScheduledRunLeftPausedOnAQuestion(t *testing.T) {
	fastUnattendedPause(t)
	h, br, st, entry := scheduledHub(t)
	br.callResults[methodKiroWorkflowList] = kasRuns(t, map[string]any{
		"workflowId": gateRunID, "name": "nightly", "workflowName": "nightly", "status": "paused",
	})
	br.callResults[methodKiroWorkflowInspect] = inspectPaused(t, gateRunID, stepQuestionReason)
	leaseScheduledWithSlot(t, h, br, &entry, gateRunID, time.Hour)

	h.runs.BootSweep(t.Context())

	if got := waitForEndReason(h, gateRunID, 5*time.Second); got != runEndUnattended {
		t.Fatalf("a scheduled run left paused on a question across a restart recorded %q, want %q; calls were %v",
			got, runEndUnattended, br.callLog())
	}
	row := awaitEndedRow(t, st)
	checkPauseQuoted(t, &row, stepQuestionReason)
}

// TestParentlessRun_AFaultPauseIsResumedOnItsFrame pins that a parentless run's paused frame, which arrives with no
// chat id, still reaches the heal: otherwise nothing resumes a transient fault and a scheduled run is ended for it.
func TestParentlessRun_AFaultPauseIsResumedOnItsFrame(t *testing.T) {
	for name, origin := range map[string]launchOrigin{
		"scheduled": scheduledLaunch(schedule.Row{ID: "s1"}, time.Now().Add(time.Hour)),
		"manual":    {origin: runlease.OriginManual},
	} {
		t.Run(name, func(t *testing.T) {
			prev := healBaseDelay
			healBaseDelay = time.Millisecond
			t.Cleanup(func() { healBaseDelay = prev })
			h, _, br := newTestHub()
			br.callResults = map[string]json.RawMessage{
				methodKiroWorkflowInspect: inspectPaused(t, gateRunID, interruptedReason),
				methodKiroWorkflowResume:  json.RawMessage(`{}`),
			}
			h.bridge.mgr.insert(runChatID(gateRunID), &sharedBridge{bridge: br, state: bridgeIdle})
			h.runs.grantLease(t.Context(), gateRunID, "nightly", origin, hostSec())

			heal := h.runs.healPaused(func(context.Context, marotte.ChatID, *marotte.RPCResponse) {})
			heal(t.Context(), "", pausedFrame(t, gateRunID, interruptedReason))

			stop := time.Now().Add(3 * time.Second)
			for !slices.Contains(br.callLog(), methodKiroWorkflowResume) {
				if time.Now().After(stop) {
					t.Fatalf("no resume was issued for a parentless run's fault pause; calls were %v", br.callLog())
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

// TestScheduledRun_AnUnattendedEndRefusedOnceIsRetried pins that ending a paused run nobody resumes survives a
// refused cancel: the pause has no deadline to fire again, so only the retry stops the run before its next slot.
func TestScheduledRun_AnUnattendedEndRefusedOnceIsRetried(t *testing.T) {
	fastUnattendedPause(t)
	h, br, st, entry := scheduledHub(t)
	setCancelRetryDelay(h.runs, time.Millisecond)
	refuseCancelOnce(br)
	br.callResults[methodKiroWorkflowInspect] = inspectPaused(t, gateRunID, stepQuestionReason)
	leaseScheduledWithSlot(t, h, br, &entry, gateRunID, time.Hour)
	h.runs.armDeadline(t.Context(), gateRunID)

	pauseRun(t, h, gateRunID, stepQuestionReason)

	if got := waitForEndReason(h, gateRunID, 5*time.Second); got != runEndUnattended {
		t.Fatalf("a paused run whose end was refused once recorded %q, want %q; calls were %v",
			got, runEndUnattended, br.callLog())
	}
	if got := callsOf(br, methodKiroWorkflowCancel); got != 2 {
		t.Errorf("the cancel was sent %d times, want 2: the refused one and its retry", got)
	}
	row := awaitEndedRow(t, st)
	checkPauseQuoted(t, &row, stepQuestionReason)
}

// TestScheduledRun_AnUnattendedEndsRetryLeavesAPauseAfterAResume pins that a refused end's retry is for the pause
// that was refused: a run resumed and paused again before it gets the new pause's own budget, not the old one's end.
func TestScheduledRun_AnUnattendedEndsRetryLeavesAPauseAfterAResume(t *testing.T) {
	prev := unattendedPauseBudget
	unattendedPauseBudget = time.Hour
	t.Cleanup(func() { unattendedPauseBudget = prev })
	h, br, st, entry := scheduledHub(t)
	const retryDelay = 100 * time.Millisecond
	setCancelRetryDelay(h.runs, retryDelay)
	refuseCancelOnce(br)
	br.callResults[methodKiroWorkflowInspect] = inspectPaused(t, gateRunID, stepQuestionReason)
	leaseScheduledWithSlot(t, h, br, &entry, gateRunID, time.Hour)
	h.runs.armDeadline(t.Context(), gateRunID)
	pauseRun(t, h, gateRunID, stepQuestionReason)

	// What the pause's watch sends once its budget is spent.
	if _, err := h.runs.transition(t.Context(), gateRunID, &runRequest{want: pauseExpiry}); err == nil {
		t.Fatal("the first end was not refused; the test needs its retry pending")
	}
	h.runs.armDeadline(t.Context(), gateRunID)
	pauseRun(t, h, gateRunID, stepQuestionReason)
	time.Sleep(5 * retryDelay)

	if got := callsOf(br, methodKiroWorkflowCancel); got != 1 {
		t.Errorf("the cancel was sent %d times, want 1: the retry ended a pause that had its own budget left; calls were %v",
			got, br.callLog())
	}
	if got := h.runs.endReason(gateRunID); got != "" {
		t.Errorf("the run recorded end reason %q, want none: its new pause has not spent its budget", got)
	}
	if row := onlyRow(t, st); row.LastStatus != "started" {
		t.Errorf("the row reads (%q, %q), want started: the run is still paused within its budget",
			row.LastStatus, row.LastReason)
	}
}
