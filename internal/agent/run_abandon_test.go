package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
)

// awaitOwnerQueue waits until n callers own or wait for the run's owner.
func awaitOwnerQueue(t *testing.T, rs *Runs, workflowID string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for ownerUsers(rs, workflowID) < n {
		if time.Now().After(deadline) {
			t.Fatalf("%d callers hold or wait for %s's owner, want %d", ownerUsers(rs, workflowID), workflowID, n)
		}
		time.Sleep(time.Millisecond)
	}
}

// blockCancel parks the rule's cancel of wf_1 until the returned func answers it, which a run that has ended reads
// back as aborted.
func blockCancel(t *testing.T, br *fakeBridge) (answer func()) {
	t.Helper()
	unblock := make(chan struct{})
	br.mu.Lock()
	br.blockOn = map[string]chan struct{}{methodKiroWorkflowCancel: unblock}
	br.callResults[methodKiroWorkflowCancel] = json.RawMessage(`{}`)
	br.callResults[methodKiroWorkflowInspect] = inspectReply(t, "wf_1", marotte.RunStatusAborted, "")
	br.mu.Unlock()
	return func() { close(unblock) }
}

// wantPersonsLease checks that wf_1 runs under a person's lease that bounds it.
func wantPersonsLease(t *testing.T, rs *Runs) {
	t.Helper()
	if l, held := rs.lease("wf_1"); !held || l.Origin != runlease.OriginManual || l.RuleID != "" {
		t.Errorf("lease = %+v held %v, want the person's manual lease", l, held)
	}
	if !rs.bounded("wf_1") {
		t.Error("the person's retried run is not bounded: its deadline was parked or its lease dropped")
	}
}

// A person's retry and the rule's cancel of the run it abandoned never interleave: whichever owns the run first
// finishes before the other starts, so the cancel never stops, parks or releases the person's run.
func TestAbandonedRunOwner_Concurrent(t *testing.T) {
	t.Run("a retry during the cancel waits for its answer", func(t *testing.T) {
		fc := &fakeConfirmer{retried: true}
		h, br := triggeredRetryHub(t, fc, t.TempDir())
		h.runs.armDeadline(t.Context(), "wf_1")
		answer := blockCancel(t, br)
		var answered, retriedFirst atomic.Bool
		br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
			if method == methodKiroWorkflowRetry && !answered.Load() {
				retriedFirst.Store(true)
			}
			return nil, nil, false
		}

		cancelled := make(chan error, 1)
		go func() { cancelled <- h.runs.CancelAbandoned(t.Context(), "wf_1") }()
		awaitCalls(t, br, methodKiroWorkflowCancel, 1, "the rule's cancel never went out")
		retried := make(chan error, 1)
		go func() { retried <- retryFailed(t, h) }()
		awaitOwnerQueue(t, h.runs, "wf_1", 2)

		if n := fc.count("Retried wf_1"); n != 0 {
			t.Errorf("Retried called %d times while the cancel was in flight, want 0", n)
		}
		if l, held := h.runs.lease("wf_1"); !held || l.Origin != runlease.OriginTriggered {
			t.Errorf("lease while the cancel is in flight = %+v held %v, want the triggered lease", l, held)
		}
		answered.Store(true)
		answer()
		if err := <-cancelled; err != nil {
			t.Errorf("CancelAbandoned: %v", err)
		}
		if err := <-retried; err != nil {
			t.Fatalf("retry: %v", err)
		}

		if retriedFirst.Load() {
			t.Error("the retry verb went out before the rule's cancel answered, so the cancel can stop it")
		}
		if n := fc.count("Retried wf_1"); n != 1 {
			t.Errorf("Retried called %d times, want 1", n)
		}
		wantPersonsLease(t, h.runs)
	})
	t.Run("a cancel queued behind the retry sends nothing", func(t *testing.T) {
		fc := &fakeConfirmer{retried: true}
		h, br := triggeredRetryHub(t, fc, t.TempDir())
		unblock := make(chan struct{})
		br.mu.Lock()
		br.blockOn = map[string]chan struct{}{methodKiroWorkflowRetry: unblock}
		br.callResults[methodKiroWorkflowCancel] = json.RawMessage(`{}`)
		br.mu.Unlock()

		retried := make(chan error, 1)
		go func() { retried <- retryFailed(t, h) }()
		awaitCalls(t, br, methodKiroWorkflowRetry, 1, "the retry verb never went out")
		cancelled := make(chan error, 1)
		go func() { cancelled <- h.runs.CancelAbandoned(t.Context(), "wf_1") }()
		awaitOwnerQueue(t, h.runs, "wf_1", 2)
		close(unblock)
		if err := <-retried; err != nil {
			t.Fatalf("retry: %v", err)
		}

		if err := <-cancelled; err == nil {
			t.Error("the rule's cancel of a run a person's retry took reported landing")
		}
		if n := callsOf(br, methodKiroWorkflowCancel); n != 0 {
			t.Errorf("workflow/cancel sent %d times, want none: the run is the person's", n)
		}
		wantPersonsLease(t, h.runs)
	})
	t.Run("a release queued behind the retry leaves the person's run", func(t *testing.T) {
		fc := &fakeConfirmer{retried: true}
		h, _ := triggeredRetryHub(t, fc, t.TempDir())
		if err := retryFailed(t, h); err != nil {
			t.Fatalf("retry: %v", err)
		}

		h.runs.ReleaseTriggered(t.Context(), "wf_1")

		wantPersonsLease(t, h.runs)
	})
	t.Run("a cancel queued while the launch names its attempt waits for the lease", func(t *testing.T) {
		fc := &fakeConfirmer{}
		h, _, br := newTestHub(WithTriggerConfirmer(fc))
		launchableRecipe(br, "wf_1")
		br.setCallResult(methodKiroWorkflowCancel, json.RawMessage(`{}`))
		var cancelSent, leasedAtCancel atomic.Bool
		br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
			if method == methodKiroWorkflowCancel {
				_, held := h.runs.lease("wf_1")
				leasedAtCancel.Store(held)
				cancelSent.Store(true)
			}
			return nil, nil, false
		}
		cancelled := make(chan error, 1)
		fc.onName = func() {
			go func() { cancelled <- h.runs.CancelAbandoned(context.WithoutCancel(t.Context()), "wf_1") }()
			awaitOwnerQueue(t, h.runs, "wf_1", 2)
		}

		if _, _, err := h.runs.launchAs(t.Context(), "bundled://publish", nil, marotte.RunDefaults{}, eventLaunch()); err != nil {
			t.Fatalf("launchAs: %v", err)
		}
		if err := <-cancelled; err != nil {
			t.Errorf("CancelAbandoned: %v", err)
		}

		if !cancelSent.Load() {
			t.Fatal("the queued cancel never went out")
		}
		if !leasedAtCancel.Load() {
			t.Error("the cancel went out before the launch wrote the run's lease, so nothing would release it")
		}
	})
}

// A retry that waits out its timeout behind the rule's cancel is refused with nothing changed.
func TestRetryBehindAbandonedCancelTimesOut(t *testing.T) {
	fc := &fakeConfirmer{retried: true}
	prev := retryTimeout
	retryTimeout = 50 * time.Millisecond
	t.Cleanup(func() { retryTimeout = prev })
	h, br := triggeredRetryHub(t, fc, t.TempDir())
	answer := blockCancel(t, br)
	cancelled := make(chan error, 1)
	go func() { cancelled <- h.runs.CancelAbandoned(t.Context(), "wf_1") }()
	awaitCalls(t, br, methodKiroWorkflowCancel, 1, "the rule's cancel never went out")

	err := retryFailed(t, h)
	answer()
	<-cancelled

	if !errors.Is(err, errRetryRunOwned) {
		t.Errorf("retry err = %v, want errRetryRunOwned", err)
	}
	if code, _, ok := command.StatusOf(err); !ok || code != http.StatusConflict {
		t.Errorf("retry status = %d (%v), want 409", code, ok)
	}
	if n := fc.count("Retried wf_1"); n != 0 {
		t.Errorf("Retried called %d times, want 0: the refused retry handed nothing back", n)
	}
	if br.called(methodKiroWorkflowRetry) {
		t.Error("the retry verb was sent though the retry was refused")
	}
}
