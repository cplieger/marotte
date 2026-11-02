package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// retryRig is c-launcher's started run wf_1, which KAS's list and inspect both report failed, with a retry verb KAS
// accepts. The retry's new life has not started until the test delivers its run_start.
func retryRig(t *testing.T) (*Runtime, *testChatStore, *fakeBridge) {
	t.Helper()
	h, cs, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	cs.seed(t, "c-launcher", func(c *marotte.Chat) { c.RecordSession("sess_launcher") })
	br.setCallResult(methodKiroWorkflowList, kasRuns(t, map[string]any{
		"workflowId": "wf_1", "name": "r", "workflowName": "r", "status": "failed", "parentSessionId": "sess_launcher",
	}))
	br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, "wf_1", "failed", ""))
	br.setCallResult(methodKiroWorkflowRetry, retryReply(t, "wf_1", "running"))
	startRun(t, h, "c-launcher", "wf_1", "sess_launcher")
	return h, cs, br
}

// retryNow is a person's retry of wf_1 through the gate's own read.
func retryNow(t *testing.T, h *Runtime) error {
	t.Helper()
	_, err := h.runs.retry(t.Context(), "wf_1", h.runs.affordance(t.Context(), "wf_1", "failed"))
	return err
}

// onMethod runs f inside the engine's answer to method, once.
func onMethod(br *fakeBridge, method string, f func()) {
	var once sync.Once
	br.setOnCall(func(m string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if m == method {
			once.Do(f)
		}
		return nil, nil, false
	})
}

func staleFailedFrame(t *testing.T, h *Runtime) {
	t.Helper()
	h.runs.observeComplete(t.Context(), "c-launcher", runNotif(methodWFRunComplete, map[string]any{
		"workflowId": "wf_1", "status": "failed",
	}))
}

func recipeIdleNow(t *testing.T, h *Runtime) {
	t.Helper()
	_ = h.runs.recipeIdle(t.Context(), "r")
}

// Terminal evidence ends only a started life, and only the one it read before its KAS call: KAS reports the
// previous life's failure until a retry's verb lands, and a frame can be queued behind it.
func TestRunLife_TerminalEvidenceEndsOnlyAStartedLife(t *testing.T) {
	cells := map[string]struct {
		act      func(t *testing.T, h *Runtime, br *fakeBridge)
		wantKept bool
	}{
		"minted_stale_frame_during_the_verb": {
			act: func(t *testing.T, h *Runtime, br *fakeBridge) {
				onMethod(br, methodKiroWorkflowRetry, func() { staleFailedFrame(t, h) })
				if err := retryNow(t, h); err != nil {
					t.Fatalf("Retry: %v", err)
				}
			},
			wantKept: true,
		},
		"minted_failed_list_during_the_verb": {
			act: func(t *testing.T, h *Runtime, br *fakeBridge) {
				onMethod(br, methodKiroWorkflowRetry, func() { recipeIdleNow(t, h) })
				if err := retryNow(t, h); err != nil {
					t.Fatalf("Retry: %v", err)
				}
			},
			wantKept: true,
		},
		"minted_failed_inspect_during_the_verb": {
			act: func(t *testing.T, h *Runtime, br *fakeBridge) {
				onMethod(br, methodKiroWorkflowRetry, func() { h.runs.releaseIfOver(t.Context(), "wf_1") })
				if err := retryNow(t, h); err != nil {
					t.Fatalf("Retry: %v", err)
				}
			},
			wantKept: true,
		},
		"minted_failed_list_at_boot": {
			act: func(t *testing.T, h *Runtime, br *fakeBridge) {
				onMethod(br, methodKiroWorkflowRetry, func() { h.runs.sweepOrphaned(t.Context()) })
				if err := retryNow(t, h); err != nil {
					t.Fatalf("Retry: %v", err)
				}
			},
			wantKept: true,
		},
		"minted_stale_frame_after_the_reply": {
			act: func(t *testing.T, h *Runtime, _ *fakeBridge) {
				if err := retryNow(t, h); err != nil {
					t.Fatalf("Retry: %v", err)
				}
				staleFailedFrame(t, h)
			},
			wantKept: true,
		},
		"a_start_between_the_list_read_and_its_release": {
			act: func(t *testing.T, h *Runtime, br *fakeBridge) {
				if err := retryNow(t, h); err != nil {
					t.Fatalf("Retry: %v", err)
				}
				onMethod(br, methodKiroWorkflowList, func() { startRun(t, h, "c-launcher", "wf_1", "sess_launcher") })
				recipeIdleNow(t, h)
			},
			wantKept: true,
		},
		"started_then_its_terminal_frame": {
			act: func(t *testing.T, h *Runtime, _ *fakeBridge) {
				if err := retryNow(t, h); err != nil {
					t.Fatalf("Retry: %v", err)
				}
				startRun(t, h, "c-launcher", "wf_1", "sess_launcher")
				staleFailedFrame(t, h)
			},
		},
		"started_then_a_failed_list": {
			act: func(t *testing.T, h *Runtime, _ *fakeBridge) {
				if err := retryNow(t, h); err != nil {
					t.Fatalf("Retry: %v", err)
				}
				startRun(t, h, "c-launcher", "wf_1", "sess_launcher")
				recipeIdleNow(t, h)
			},
		},
		"a_refused_retry_leaves_its_life_endable": {
			act: func(t *testing.T, h *Runtime, br *fakeBridge) {
				br.setCallRPCErr(methodKiroWorkflowRetry, &marotte.RPCError{Code: -32603, Message: "run is not failed"})
				if err := retryNow(t, h); err == nil {
					t.Fatal("Setup: the refused retry succeeded")
				}
				recipeIdleNow(t, h)
			},
		},
		"a_retry_whose_answer_timed_out_keeps_waiting_for_its_start": {
			act: func(t *testing.T, h *Runtime, br *fakeBridge) {
				br.setFailAfterCall(methodKiroWorkflowRetry, context.DeadlineExceeded)
				if err := retryNow(t, h); err == nil {
					t.Fatal("Setup: the timed-out retry succeeded")
				}
				recipeIdleNow(t, h)
			},
			wantKept: true,
		},
	}
	for name, tc := range cells {
		t.Run(name, func(t *testing.T) {
			h, cs, br := retryRig(t)
			tc.act(t, h, br)
			_, held := h.runs.lease("wf_1")
			if held != tc.wantKept {
				t.Errorf("lease held = %v, want %v", held, tc.wantKept)
			}
			if got := holdsKey(t, cs, "c-launcher", runKey); got != tc.wantKept {
				t.Errorf("run fact held = %v, want %v", got, tc.wantKept)
			}
			if got := h.runs.lives.endedThisBoot("wf_1"); got == tc.wantKept {
				t.Errorf("ended mark = %v, want %v", got, !tc.wantKept)
			}
		})
	}
}

// A delete ends the workflow whatever life is current, and no retry starts one while a delete holds the run.
func TestRunLife_ADeleteEndsWhicheverLifeIsCurrent(t *testing.T) {
	origin := &runOrigin{parent: runParent{chat: "c-launcher", session: "sess_launcher"}}
	cells := map[string]struct {
		arm func(t *testing.T, h *Runtime, br *fakeBridge, after *func())
	}{
		"a_retry_during_the_delete_is_refused": {
			arm: func(t *testing.T, h *Runtime, br *fakeBridge, _ *func()) {
				aff := h.runs.affordance(t.Context(), "wf_1", "failed")
				onMethod(br, methodKiroWorkflowDelete, func() {
					_, err := h.runs.retry(t.Context(), "wf_1", aff)
					if code, _, _ := command.StatusOf(err); code != http.StatusConflict || !errors.Is(err, errRetryRunDeleting) {
						t.Errorf("a retry during the delete = %v, want 409 errRetryRunDeleting", err)
					}
				})
			},
		},
		"a_life_renewed_during_the_delete_is_ended": {
			arm: func(t *testing.T, h *Runtime, br *fakeBridge, after *func()) {
				var start retryStart
				onMethod(br, methodKiroWorkflowDelete, func() {
					var err error
					if start, err = h.runs.beginRetryLife(context.Background(), "wf_1", origin); err != nil {
						t.Errorf("Setup: the newer life: %v", err)
					}
				})
				*after = func() { h.runs.abandonRetry(t.Context(), "wf_1", &start, true) }
			},
		},
		"a_life_renewed_during_the_read_back_is_ended": {
			arm: func(t *testing.T, h *Runtime, br *fakeBridge, after *func()) {
				br.setFailAfterCall(methodKiroWorkflowDelete, marotte.ErrBridgeExited)
				br.setCallRPCErr(methodKiroWorkflowInspect, workflowNotFoundErr())
				var start retryStart
				onMethod(br, methodKiroWorkflowDelete, func() {
					var err error
					if start, err = h.runs.beginRetryLife(context.Background(), "wf_1", origin); err != nil {
						t.Errorf("Setup: the newer life: %v", err)
					}
				})
				*after = func() { h.runs.abandonRetry(t.Context(), "wf_1", &start, true) }
			},
		},
	}
	for name, tc := range cells {
		t.Run(name, func(t *testing.T) {
			h, cs, br := retryRig(t)
			br.setCallResult(methodKiroWorkflowDelete, json.RawMessage(`{}`))
			after := func() {}
			tc.arm(t, h, br, &after)
			if err := h.runs.deleteAs(t.Context(), "wf_1", userStop(""), notFoundTaken); err != nil {
				t.Fatalf("deleteAs = %v", err)
			}
			after()
			if l, held := h.runs.lease("wf_1"); held {
				t.Errorf("a deleted run kept a lease: %+v", l)
			}
			if holdsKey(t, cs, "c-launcher", runKey) {
				t.Error("a deleted run's keep fact stayed on its launching chat")
			}
			if !h.runs.lives.endedThisBoot("wf_1") {
				t.Error("a deleted run is not marked ended")
			}
		})
	}
}

// A person's retry of a scheduled run whose end marotte missed ends the scheduled life, and the retry's own life
// survives a terminal frame of the scheduled one still queued behind it.
func TestRetry_SettlingAMissedScheduledEndLeavesTheRetrysLife(t *testing.T) {
	h, br, st, entry := scheduledHub(t)
	const id = "wf_1"
	launchScheduled(t, h, br, &entry, id)
	scheduled, _ := h.runs.lease(id)
	br.setCallResult(methodKiroWorkflowRetry, retryReply(t, id, "running"))
	br.setCallResult(methodKiroWorkflowList, kasRuns(t, map[string]any{"workflowId": id, "name": "nightly", "status": "failed"}))

	if _, err := h.runs.retry(t.Context(), id, h.runs.affordance(t.Context(), id, "failed")); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	h.runs.observeComplete(t.Context(), "", runNotif(methodWFRunComplete, map[string]any{"workflowId": id, "status": "failed"}))

	if row := onlyRow(t, st); row.LastStatus != "failed" {
		t.Errorf("the schedule row reads %q, want the scheduled run's own failed ending", row.LastStatus)
	}
	l, held := h.runs.lease(id)
	if !held || l.Life == scheduled.Life || l.ScheduleID != "" {
		t.Errorf("lease = %+v held %v, want the retry's own life, not the scheduled one %s", l, held, scheduled.Life)
	}
	if h.runs.lives.endedThisBoot(id) {
		t.Error("a stale terminal frame marked the retried run ended")
	}
}
