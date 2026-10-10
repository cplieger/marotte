package agent

import (
	"context"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/notice"
	"github.com/cplieger/marotte/internal/runlease"
)

// newWithholdHub is a runtime whose push service has a subscriber, so only a withhold keeps the channel empty.
func newWithholdHub(t *testing.T) (*Runtime, *runOutcomePush) {
	t.Helper()
	cs := newTestChatStore()
	fp := newRunOutcomePush()
	h := New(context.Background(), t.TempDir(), func() ACPBridge { return newFakeBridge() }, cs, WithPush(fp))
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	return h, fp
}

func seedRunLease(t *testing.T, h *Runtime, workflowID, chatID string) {
	t.Helper()
	l := runlease.Lease{
		StartedAt:  time.Now(),
		Deadline:   time.Now().Add(time.Hour),
		WorkflowID: workflowID,
		ChatID:     chatID,
		Recipe:     "code-review",
		Origin:     runlease.OriginAgent,
	}
	if err := h.runs.leaseStore().Put(t.Context(), &l); err != nil {
		t.Fatalf("seed lease %s: %v", workflowID, err)
	}
}

// awaitNoPush joins the lifecycle's inflight group before reading, so an empty read means no push was scheduled.
func awaitNoPush(t *testing.T, h *Runtime, fp *runOutcomePush, why string) {
	t.Helper()
	joinInflight(t, h)
	select {
	case got := <-fp.sent:
		t.Errorf("push sent %q (kind %q); %s", got.body, got.kind, why)
	default:
	}
}

// run_workflow returns once the run is created, so the launching turn ends cleanly while the run continues; a push
// then claims the work is done. The withhold sits ahead of the severity switch, so all four cases are covered.
func TestPushTurnOutcome_WithheldWhileALaunchedRunIsLive(t *testing.T) {
	tests := map[string]struct {
		stop     marotte.StopReason
		liveRun  bool
		wantPush bool
		why      string
	}{
		"a clean turn with a live run is withheld": {
			stop:     marotte.StopReasonEndTurn,
			liveRun:  true,
			wantPush: false,
			why:      "the turn ended; the work the turn started has not",
		},
		"a BROKEN turn with a live run is withheld too": {
			stop:     marotte.StopReasonError,
			liveRun:  true,
			wantPush: false,
			why: "the withhold leads the severity switch on purpose: a failed turn that " +
				"launched a still-running run makes the same false claim",
		},
		"a clean turn with no live run pushes": {
			stop:     marotte.StopReasonEndTurn,
			liveRun:  false,
			wantPush: true,
			why:      "the control: nothing outstanding, so the ordinary notification is correct",
		},
		"a broken turn with no live run pushes": {
			stop:     marotte.StopReasonError,
			liveRun:  false,
			wantPush: true,
			why:      "the other control, so a fix that silenced one severity cannot pass",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			h, fp := newWithholdHub(t)
			if tc.liveRun {
				seedRunLease(t, h, "wf_1", "c1")
			}

			h.coord.pushTurnOutcome(t.Context(), "c1", marotte.ConcludeStopReason(tc.stop), "")

			if !tc.wantPush {
				awaitNoPush(t, h, fp, tc.why)
				return
			}
			select {
			case got := <-fp.sent:
				if got.kind != marotte.PushKindAgentFinished {
					t.Errorf("push kind = %q, want %q (%s)",
						got.kind, marotte.PushKindAgentFinished, tc.why)
				}
			case <-time.After(2 * time.Second):
				t.Errorf("no push sent; %s", tc.why)
			}
		})
	}
}

// Another chat's run must not mute this chat: the withhold is keyed on the lease's ChatID.
func TestPushTurnOutcome_AnotherChatsRunDoesNotWithhold(t *testing.T) {
	h, fp := newWithholdHub(t)
	seedRunLease(t, h, "wf_1", "c2")

	h.coord.pushTurnOutcome(t.Context(), "c1", marotte.ConcludeStopReason(marotte.StopReasonEndTurn), "")

	select {
	case got := <-fp.sent:
		if got.subject.ChatID != "c1" {
			t.Errorf("push subject chat = %q, want c1", got.subject.ChatID)
		}
	case <-time.After(2 * time.Second):
		t.Error("c1's notification was withheld for a run c2 launched")
	}
}

// A parentless run (manual or scheduled) withholds nothing; its outcome travels on run_outcome.
func TestPushTurnOutcome_AParentlessRunDoesNotWithhold(t *testing.T) {
	h, fp := newWithholdHub(t)
	seedRunLease(t, h, "wf_1", "")

	h.coord.pushTurnOutcome(t.Context(), "c1", marotte.ConcludeStopReason(marotte.StopReasonEndTurn), "")

	select {
	case <-fp.sent:
	case <-time.After(2 * time.Second):
		t.Error("a chat's notification was withheld for a run no chat launched")
	}
}

// A nil chatHasLiveRun withholds nothing, so a coordinator built without the runtime wiring behaves as before.
func TestPushTurnOutcome_NilPredicateWithholdsNothing(t *testing.T) {
	h, fp := newWithholdHub(t)
	seedRunLease(t, h, "wf_1", "c1")
	h.coord.chatHasLiveRun = nil

	h.coord.pushTurnOutcome(t.Context(), "c1", marotte.ConcludeStopReason(marotte.StopReasonEndTurn), "")

	select {
	case <-fp.sent:
	case <-time.After(2 * time.Second):
		t.Error("an unwired coordinator withheld a notification; nil must mean nothing outstanding")
	}
}

// A cancel earns no push either way, so the withhold cannot take credit for the silence a stopped severity gives.
func TestPushTurnOutcome_ACancelPushesNothingEitherWay(t *testing.T) {
	for name, live := range map[string]bool{"with a live run": true, "with no live run": false} {
		t.Run(name, func(t *testing.T) {
			h, fp := newWithholdHub(t)
			if live {
				seedRunLease(t, h, "wf_1", "c1")
			}

			h.coord.pushTurnOutcome(t.Context(), "c1",
				marotte.ConcludeStopReason(marotte.StopReasonCancelled), "")

			awaitNoPush(t, h, fp, "a cancel is the reader's own gesture, so no severity arm notifies")
		})
	}
}

// A turn kiro-cli cut at its model-call limit did not report an error, so its push says what happened and what to do.
func TestPushTurnOutcome_AModelCallLimitStopPushesItsOwnRemedy(t *testing.T) {
	h, fp := newWithholdHub(t)

	h.coord.pushTurnOutcome(t.Context(), "c1", marotte.ConcludeStopReason(marotte.StopReasonToolUse), "")

	select {
	case got := <-fp.sent:
		if want := notice.TurnFailed(notice.Target{}, marotte.ModelCallLimitTurnReason).Body; got.body != want {
			t.Errorf("push body = %q, want %q", got.body, want)
		}
	case <-time.After(2 * time.Second):
		t.Error("no push sent for a turn kiro-cli stopped at its model-call limit")
	}
}
