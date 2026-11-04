package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
)

// newPurgeRig seeds one chat per claimed session; complete false makes every owner scan report an unread chat.
func newPurgeRig(t *testing.T, runs json.RawMessage, claimed []string, complete bool) (*Runtime, *deleteRig) {
	t.Helper()
	var scan func(context.Context, chat.OwnerScan) chat.OwnerScan
	if !complete {
		scan = func(_ context.Context, s chat.OwnerScan) chat.OwnerScan {
			s.Complete, s.Skipped = false, append(s.Skipped, "c-unreadable")
			return s
		}
	}
	h, rig := newPurgeRigScan(t, runs, scan)
	for i, sess := range claimed {
		h.chatStore.(*testChatStore).seed(t, marotte.ChatID(fmt.Sprintf("c-claim-%d", i)), func(c *marotte.Chat) {
			c.RecordSession(sess)
		})
	}
	return h, rig
}

func newPurgeRigScan(t *testing.T, runs json.RawMessage, scan func(context.Context, chat.OwnerScan) chat.OwnerScan) (*Runtime, *deleteRig) {
	t.Helper()
	return newEngineRig(t, map[string]json.RawMessage{methodKiroWorkflowList: runs}, nil, scan)
}

// newEngineRig is a runtime over a real chat store and a tab store whose engine answers results and rpcErrs, with
// every delete taken.
func newEngineRig(
	t *testing.T, results map[string]json.RawMessage, rpcErrs map[string]*marotte.RPCError,
	scan func(context.Context, chat.OwnerScan) chat.OwnerScan,
) (*Runtime, *deleteRig) {
	t.Helper()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("Setup: tabs.NewStore: %v", err)
	}
	return newEngineRigOver(t, results, rpcErrs, scan, st)
}

// newEngineRigOver is newEngineRig over the tab store st.
func newEngineRigOver(
	t *testing.T, results map[string]json.RawMessage, rpcErrs map[string]*marotte.RPCError,
	scan func(context.Context, chat.OwnerScan) chat.OwnerScan, st *tabs.Store,
) (*Runtime, *deleteRig) {
	t.Helper()
	rig := &deleteRig{results: map[string]json.RawMessage{methodKiroWorkflowDelete: json.RawMessage(`{}`)}, rpcErrs: rpcErrs}
	maps.Copy(rig.results, results)
	if _, scripted := results[methodKiroWorkflowInspect]; !scripted {
		rig.answer = inspectFromList(results[methodKiroWorkflowList])
	}
	return newRigOver(t, rig, scan, st)
}

// newRigOver is a runtime over rig's engine, a real chat store scanned through scan, and the tab store st.
func newRigOver(t *testing.T, rig *deleteRig, scan func(context.Context, chat.OwnerScan) chat.OwnerScan, st *tabs.Store) (*Runtime, *deleteRig) {
	t.Helper()
	cs := newTestChatStore()
	cs.ownerScan = scan
	h := newRuntimeOn(t.Context(), testReaperWorkDir, rig.engine(), cs, WithTabs(st))
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	return h, rig
}

func purgeRuns(t *testing.T, rows ...map[string]string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"runs": rows})
	if err != nil {
		t.Fatalf("Setup: marshal runs: %v", err)
	}
	return raw
}

func purgeRow(id, status, parent string, updated time.Time) map[string]string {
	return map[string]string{
		"workflowId": id, "status": status, "parentSessionId": parent,
		"updatedAt": updated.UTC().Format(time.RFC3339),
	}
}

func deletedRunIDs(rig *deleteRig) []string {
	var out []string
	for _, c := range rig.of(methodKiroWorkflowDelete) {
		id, _ := c.params[keyWorkflowID].(string)
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// inspectOf is KAS's inspect of a run whose state reads status and whose root ended at ended, the instant KAS's list
// reports as the run's updatedAt; a zero ended records no time.
func inspectOf(workflowID string, status marotte.RunStatus, ended time.Time) json.RawMessage {
	return inspectOfRoot(workflowID, status, time.Time{}, ended)
}

// inspectOfRoot is inspectOf with the root's startedAt too; a zero time records none.
func inspectOfRoot(workflowID string, status marotte.RunStatus, started, ended time.Time) json.RawMessage {
	state := map[string]any{"status": status}
	root := map[string]any{"nodeId": "root"}
	if !started.IsZero() {
		root["startedAt"] = started.UTC().Format(time.RFC3339Nano)
	}
	if !ended.IsZero() {
		root["endedAt"] = ended.UTC().Format(time.RFC3339Nano)
	}
	if len(root) > 1 {
		state["root"] = root
	}
	raw, _ := json.Marshal(map[string]any{"workflowId": workflowID, "state": state})
	return raw
}

// inspectFromList answers an inspect of a listed run with the status and last activity the list gives it.
func inspectFromList(list json.RawMessage) func(string, map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
	var rows struct {
		Runs []struct {
			WorkflowID string            `json:"workflowId"`
			Status     marotte.RunStatus `json:"status"`
			UpdatedAt  string            `json:"updatedAt"`
		} `json:"runs"`
	}
	_ = json.Unmarshal(list, &rows)
	return func(method string, params map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method != methodKiroWorkflowInspect {
			return nil, nil, false
		}
		id, _ := params[keyWorkflowID].(string)
		for _, r := range rows.Runs {
			if r.WorkflowID == id {
				ended, _ := time.Parse(time.RFC3339, r.UpdatedAt)
				return inspectOf(id, r.Status, ended), nil, true
			}
		}
		return nil, nil, false
	}
}

// retryEffect is what KAS does with a retry of a purgedRun.
type retryEffect uint8

const (
	// retryRevives sets the run running and delivers its run_start before KAS answers.
	retryRevives retryEffect = iota
	// retryFailsAgain re-drives the run, which fails again at once: KAS delivers its run_start and run_complete, then
	// answers.
	retryFailsAgain
	// retryLost is a retry KAS takes and never answers or applies.
	retryLost
	// retryFailsBeforeItsStart answers, delivers the run's run_start and run_complete, and leaves the run failed with
	// its old end: the run loop threw after emitting run_start and before recording the root's startedAt.
	retryFailsBeforeItsStart
	// retryRevivesUnseen sets the run running from a new root startedAt and answers before marotte receives any frame.
	retryRevivesUnseen
	// retryRefused changes nothing and answers with the purgedRun's refusal.
	retryRefused
)

// purgedRun is one parentless run as KAS holds it, listed and inspected as it stands, and retried as retried says.
// updated is the root's endedAt and started its startedAt; the list's updatedAt is the first set.
type purgedRun struct {
	updated, started time.Time
	refusal          *marotte.RPCError
	id, parent       string
	status           marotte.RunStatus
	mu               sync.Mutex
	retried          retryEffect
}

func (k *purgedRun) answer(method string, params map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	switch method {
	case methodKiroWorkflowList:
		last := k.updated
		if last.IsZero() {
			last = k.started
		}
		raw, _ := json.Marshal(map[string]any{"runs": []map[string]string{purgeRow(k.id, string(k.status), k.parent, last)}})
		return raw, nil, true
	case methodKiroWorkflowInspect:
		if id, _ := params[keyWorkflowID].(string); id == k.id {
			return inspectOfRoot(k.id, k.status, k.started, k.updated), nil, true
		}
	case methodKiroWorkflowRetry:
		return k.retry()
	}
	return nil, nil, false
}

// retry applies a retry under k.mu.
func (k *purgedRun) retry() (json.RawMessage, []*marotte.RPCResponse, bool) {
	frames := []*marotte.RPCResponse{
		runNotif(methodWFRunStart, map[string]any{"workflowId": k.id, "workflowName": "build", "parentSessionId": k.parent}),
	}
	switch k.retried {
	case retryLost, retryRefused:
		return nil, nil, false
	case retryRevives:
		k.status = marotte.RunStatusRunning
	case retryFailsAgain:
		k.status, k.updated = marotte.RunStatusFailed, time.Now()
		frames = append(frames, runNotif(methodWFRunComplete, map[string]any{"workflowId": k.id, "status": marotte.RunStatusFailed}))
	case retryFailsBeforeItsStart:
		frames = append(frames, runNotif(methodWFRunComplete, map[string]any{"workflowId": k.id, "status": marotte.RunStatusFailed}))
	case retryRevivesUnseen:
		k.status, k.started, k.updated = marotte.RunStatusRunning, time.Now(), time.Time{}
		frames = nil
	}
	raw, _ := json.Marshal(map[string]any{"workflowId": k.id, "status": marotte.RunStatusRunning, "retriedNodeIds": []string{"build"}})
	return raw, frames, true
}

// ownerScan names one of a purge pass's chat-owner scans.
type ownerScan int32

const (
	// scanOfThePass is the pass's own scan, after its list and before it judges any row.
	scanOfThePass ownerScan = iota + 1
	// scanOfAdmission is admission's re-read, after the row was judged and before the delete's hold.
	scanOfAdmission
)

// newPurgedRunRig is a purge rig over kas whose settle ceiling fires at once. Once arm is called, every engine call
// runs onCall and owner scan number at runs during.
func newPurgedRunRig(
	t *testing.T, kas *purgedRun, onCall func(string, map[string]any), during func(context.Context, *Runtime), at ownerScan,
) (h *Runtime, rig *deleteRig, arm func()) {
	t.Helper()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("Setup: tabs.NewStore: %v", err)
	}
	var armed atomic.Bool
	var scans atomic.Int32
	rig = &deleteRig{
		results: map[string]json.RawMessage{methodKiroWorkflowDelete: json.RawMessage(`{}`)},
		answer:  kas.answer,
		onCall: func(method string, params map[string]any) {
			if onCall != nil && armed.Load() {
				onCall(method, params)
			}
		},
	}
	if kas.retried == retryLost {
		// Never closed: the retry's own deadline abandons the call.
		rig.blockOn = map[string]chan struct{}{methodKiroWorkflowRetry: make(chan struct{})}
	}
	if kas.retried == retryRefused {
		rig.refuseAfterCall = map[string]*marotte.RPCError{methodKiroWorkflowRetry: kas.refusal}
	}
	h, _ = newRigOver(t, rig, func(ctx context.Context, s chat.OwnerScan) chat.OwnerScan {
		if during != nil && armed.Load() && ownerScan(scans.Add(1)) == at {
			during(ctx, h)
		}
		return s
	}, st)
	settleBoot(t, h)
	m := h.runs.mutations
	m.mu.Lock()
	m.ceiling = 10 * time.Millisecond
	m.mu.Unlock()
	return h, rig, func() { armed.Store(true) }
}

// waitDrivesSettled waits until no write driving a run is unsettled.
func waitDrivesSettled(t *testing.T, h *Runtime) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !h.runs.mutations.empty() {
		if time.Now().After(deadline) {
			t.Fatal("Setup: a driving write stayed unsettled for 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// retryDuringTheScan is a purge rig's during that retries kas's run, then waits until marotte's registry of unsettled
// writes has settled the retry, so only what the purge reads can keep the run.
func retryDuringTheScan(t *testing.T, kas *purgedRun, retryErr *error) func(context.Context, *Runtime) {
	t.Helper()
	return func(ctx context.Context, h *Runtime) {
		_, *retryErr = h.runs.retry(ctx, kas.id, failedAffordance(h, kas.id))
		waitDrivesSettled(t, h)
	}
}

func failedAffordance(h *Runtime, workflowID string) *runAffordance {
	return h.runs.affordance(workflowID, &runHead{Status: string(marotte.RunStatusFailed)})
}

// shortRetryTimeout makes a lost retry give up within the test.
func shortRetryTimeout(t *testing.T) {
	t.Helper()
	prev := retryTimeout
	retryTimeout = 300 * time.Millisecond
	t.Cleanup(func() { retryTimeout = prev })
}

// The purge judges a run on its list row and again, in full, on a fresh read under the delete's hold, so a retry
// landing between the list and the delete decides the outcome as it would had the purge listed the run after it. A
// retry after the list marks the run before its row is judged; one during admission is seen only by the fresh read.
func TestPurgeParentlessRuns_DeletesOnlyARunAFreshReadStillFindsPurgeable(t *testing.T) {
	shortRetryTimeout(t)
	const window = 24 * time.Hour
	type outcome struct {
		deadline bool
		// marked is whether the retry's mark outlives the pass: only a fresh read of activity since it drops it.
		marked bool
	}
	cases := []struct {
		name               string
		retried            retryEffect
		afterList, inAdmit outcome
		answered           bool
	}{
		{
			name: "a retry revived it", answered: true, retried: retryRevives,
			afterList: outcome{deadline: true, marked: true}, inAdmit: outcome{marked: true},
		},
		{
			name: "a retry re-drove it and it failed again", answered: true, retried: retryFailsAgain,
			afterList: outcome{deadline: true, marked: true}, inAdmit: outcome{deadline: true},
		},
		{
			name: "a retry's answer was lost", retried: retryLost,
			afterList: outcome{deadline: true, marked: true}, inAdmit: outcome{deadline: true, marked: true},
		},
		{
			name: "a retry's run_start came and it failed before KAS recorded the start", answered: true,
			retried:   retryFailsBeforeItsStart,
			afterList: outcome{deadline: true, marked: true}, inAdmit: outcome{deadline: true, marked: true},
		},
		{
			name: "a retry revived it before marotte saw any frame", answered: true, retried: retryRevivesUnseen,
			afterList: outcome{deadline: true, marked: true}, inAdmit: outcome{},
		},
	}
	for _, tc := range cases {
		for _, at := range []ownerScan{scanOfThePass, scanOfAdmission} {
			want, when := tc.afterList, "after the list"
			if at == scanOfAdmission {
				want, when = tc.inAdmit, "during admission"
			}
			t.Run(tc.name+" "+when, func(t *testing.T) {
				kas := &purgedRun{
					id: "wf_1", parent: "sess_host", status: marotte.RunStatusFailed, updated: time.Now().Add(-2 * window),
					retried: tc.retried,
				}
				var retryErr error
				h, rig, arm := newPurgedRunRig(t, kas, nil, retryDuringTheScan(t, kas, &retryErr), at)
				arm()

				res := h.PurgeParentlessRuns(t.Context(), window)

				if got := len(rig.of(methodKiroWorkflowRetry)); got != 1 || (retryErr == nil) != tc.answered {
					t.Fatalf("Setup: the retry sent %d verbs and answered %v, want 1 verb, answered %v", got, retryErr, tc.answered)
				}
				if got := deletedRunIDs(rig); len(got) != 0 {
					t.Errorf("PurgeParentlessRuns deleted %v, want nothing", got)
				}
				if res.Purged != 0 || res.Kept != 1 {
					t.Errorf("PurgeParentlessRuns = purged %d kept %d, want purged 0 kept 1", res.Purged, res.Kept)
				}
				if got := !res.NextDeadline.IsZero(); got != want.deadline {
					t.Errorf("PurgeParentlessRuns.NextDeadline = %v, want one set %v: a keep that lapses by itself schedules the next pass",
						res.NextDeadline, want.deadline)
				}
				if got := retryMarked(h, "wf_1"); got != want.marked {
					t.Errorf("wf_1's retry mark held after the pass = %v, want %v", got, want.marked)
				}
			})
		}
	}
	t.Run("no retry", func(t *testing.T) {
		kas := &purgedRun{id: "wf_1", parent: "sess_host", status: marotte.RunStatusFailed, updated: time.Now().Add(-2 * window)}
		h, rig, arm := newPurgedRunRig(t, kas, nil, nil, 0)
		arm()

		res := h.PurgeParentlessRuns(t.Context(), window)

		if got := deletedRunIDs(rig); !slices.Equal(got, []string{"wf_1"}) {
			t.Errorf("PurgeParentlessRuns deleted %v, want [wf_1]", got)
		}
		if res.Purged != 1 || res.Kept != 0 || !res.NextDeadline.IsZero() {
			t.Errorf("PurgeParentlessRuns = purged %d kept %d next %v, want purged 1 kept 0 and no deadline",
				res.Purged, res.Kept, res.NextDeadline)
		}
	})
}

func retryMarked(h *Runtime, workflowID string) bool {
	m := &h.runs.retries
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.at[workflowID]
	return ok
}

// Each pass drops the retry marks whose bound has passed, including those of runs it never judges.
func TestPurgeParentlessRuns_SweepsLapsedRetryMarksOfRunsItDoesNotJudge(t *testing.T) {
	kas := &purgedRun{id: "wf_1", parent: "sess_host", status: marotte.RunStatusRunning, updated: time.Now().Add(-time.Hour)}
	h, _, _ := newPurgedRunRig(t, kas, nil, nil, 0)
	h.runs.retries.mark("wf_lapsed", time.Now().Add(-retryInFlightBound))
	h.runs.retries.mark("wf_recent", time.Now())

	h.PurgeParentlessRuns(t.Context(), 0)

	if retryMarked(h, "wf_lapsed") {
		t.Error("a mark past retryInFlightBound outlived the pass, want it swept")
	}
	if !retryMarked(h, "wf_recent") {
		t.Error("a mark within retryInFlightBound was swept, want it kept")
	}
}

// A lost retry keeps its run only for retryInFlightBound: past it, with KAS still showing the run unchanged, the
// purge deletes the run.
func TestPurgeParentlessRuns_PurgesARunOnceItsLostRetryLapses(t *testing.T) {
	shortRetryTimeout(t)
	const window = 24 * time.Hour
	kas := &purgedRun{
		id: "wf_1", parent: "sess_host", status: marotte.RunStatusFailed, updated: time.Now().Add(-2 * window),
		retried: retryLost,
	}
	var retryErr error
	h, rig, arm := newPurgedRunRig(t, kas, nil, retryDuringTheScan(t, kas, &retryErr), scanOfThePass)
	arm()
	if first := h.PurgeParentlessRuns(t.Context(), window); first.Kept != 1 || retryErr == nil {
		t.Fatalf("Setup: the pass with the retry lost = kept %d, retry answered %v; want kept 1 and no answer", first.Kept, retryErr)
	}
	lapseRetryMarks(h)

	res := h.PurgeParentlessRuns(t.Context(), window)

	if got := deletedRunIDs(rig); !slices.Equal(got, []string{"wf_1"}) {
		t.Errorf("PurgeParentlessRuns past the lost retry's bound deleted %v, want [wf_1]", got)
	}
	if res.Purged != 1 || res.Kept != 0 {
		t.Errorf("PurgeParentlessRuns past the lost retry's bound = purged %d kept %d, want purged 1 kept 0", res.Purged, res.Kept)
	}
}

// lapseRetryMarks moves every retry mark past retryInFlightBound.
func lapseRetryMarks(h *Runtime) {
	m := &h.runs.retries
	m.mu.Lock()
	defer m.mu.Unlock()
	for wf, at := range m.at {
		m.at[wf] = at.Add(-retryInFlightBound)
	}
}

// KAS refusing a retry does not drop the run's retry mark: the mark is per run, so a refusal says nothing of an earlier
// attempt KAS may still be running (a just-claimed refusal means one is). With KAS showing nothing since, the run is
// kept until the bound.
func TestPurgeParentlessRuns_KeepsARunWhoseRetryWasRefused(t *testing.T) {
	shortRetryTimeout(t)
	const window = 24 * time.Hour
	justClaimed := &marotte.RPCError{
		Code: -32603, Message: "Internal error",
		Data: json.RawMessage(`{"details":"Workflow wf_1 was just claimed by another process"}`),
	}
	parseErr := &marotte.RPCError{Code: marotte.RPCCodeParseError, Message: "Parse error"}
	cases := []struct {
		refusal *marotte.RPCError
		name    string
		// overlap leaves a first attempt unanswered and refuses a second one sent while it waits.
		overlap bool
	}{
		{name: "a second attempt refused as just claimed while the first is unanswered", refusal: justClaimed, overlap: true},
		{name: "a retry refused as just claimed", refusal: justClaimed},
		{name: "a retry refused with a parse error", refusal: parseErr},
	}
	for _, tc := range cases {
		for _, at := range []ownerScan{scanOfThePass, scanOfAdmission} {
			when := "after the list"
			if at == scanOfAdmission {
				when = "during admission"
			}
			t.Run(tc.name+" "+when, func(t *testing.T) {
				kas := &purgedRun{
					id: "wf_1", parent: "sess_host", status: marotte.RunStatusFailed, updated: time.Now().Add(-2 * window),
					retried: retryRefused, refusal: tc.refusal,
				}
				wantVerbs := 1
				if tc.overlap {
					kas.retried, wantVerbs = retryLost, 2
				}
				var errs []error
				var rig *deleteRig
				h, rig, arm := newPurgedRunRig(t, kas, nil, func(ctx context.Context, h *Runtime) {
					errs = retryAttempts(ctx, t, h, rig, kas, tc.overlap)
				}, at)
				arm()

				res := h.PurgeParentlessRuns(t.Context(), window)

				if got := len(rig.of(methodKiroWorkflowRetry)); got != wantVerbs || len(errs) != wantVerbs {
					t.Fatalf("Setup: the retries sent %d verbs and ended %d times, want %d", got, len(errs), wantVerbs)
				}
				if rpc, ok := errors.AsType[*marotte.RPCError](errs[wantVerbs-1]); !ok || rpc.Code != tc.refusal.Code {
					t.Fatalf("Setup: the refused attempt ended with %v, want the refusal coded %d", errs[wantVerbs-1], tc.refusal.Code)
				}
				if tc.overlap && !errors.Is(errs[0], context.DeadlineExceeded) {
					t.Fatalf("Setup: the first attempt ended with %v, want it abandoned on its deadline", errs[0])
				}
				if got := deletedRunIDs(rig); len(got) != 0 {
					t.Errorf("PurgeParentlessRuns deleted %v, want nothing", got)
				}
				if res.Purged != 0 || res.Kept != 1 || res.NextDeadline.IsZero() {
					t.Errorf("PurgeParentlessRuns = purged %d kept %d next %v, want purged 0 kept 1 and the mark's bound as deadline",
						res.Purged, res.Kept, res.NextDeadline)
				}
				if !retryMarked(h, "wf_1") {
					t.Error("wf_1's retry mark after the pass = dropped, want held: KAS shows no activity since the send")
				}
				lapseRetryMarks(h)
				before := len(deletedRunIDs(rig))

				res = h.PurgeParentlessRuns(t.Context(), window)

				if got := deletedRunIDs(rig)[before:]; !slices.Equal(got, []string{"wf_1"}) || res.Purged != 1 {
					t.Errorf("PurgeParentlessRuns past the mark's bound deleted %v and purged %d, want [wf_1] and 1", got, res.Purged)
				}
			})
		}
	}
}

// retryAttempts retries kas's run once, or with overlap twice: the second attempt starts while the first waits
// unanswered, and KAS refuses it with kas's refusal. It returns every attempt's error, first first, once all have
// ended and marotte's registry of unsettled writes has settled them.
func retryAttempts(ctx context.Context, t *testing.T, h *Runtime, rig *deleteRig, kas *purgedRun, overlap bool) []error {
	t.Helper()
	if !overlap {
		_, err := h.runs.retry(ctx, kas.id, failedAffordance(h, kas.id))
		waitDrivesSettled(t, h)
		return []error{err}
	}
	first := make(chan error, 1)
	// A run's ask cell holds the second attempt until the first ends, inside the second's own retryTimeout, so the
	// first gives up well before it.
	fctx, cancel := context.WithTimeout(ctx, retryTimeout/3)
	defer cancel()
	go func() {
		_, err := h.runs.retry(fctx, kas.id, failedAffordance(h, kas.id))
		first <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(rig.of(methodKiroWorkflowRetry)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("Setup: the first retry sent no verb within 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
	rig.of(methodKiroWorkflowRetry)[0].bridge.setRefuseAfterCall(methodKiroWorkflowRetry, kas.refusal)
	_, second := h.runs.retry(ctx, kas.id, failedAffordance(h, kas.id))
	errs := []error{<-first, second}
	waitDrivesSettled(t, h)
	return errs
}

// A retry mark keeps its run until KAS shows activity of the run at or after the send, to KAS's millisecond, or until
// retryInFlightBound passes.
func TestRetryMarks_HoldUntilTheRunMovesOrTheBoundPasses(t *testing.T) {
	t.Parallel()
	sentMilli := time.UnixMilli(1_800_000_000_000)
	sent := sentMilli.Add(400 * time.Microsecond)
	before := sent.Add(-time.Hour)
	cases := map[string]struct {
		last, now    time.Time
		marked, want bool
	}{
		"no retry sent":                                {last: before, now: sent},
		"KAS shows nothing since the send":             {marked: true, last: before, now: sentMilli.Add(retryInFlightBound - time.Millisecond), want: true},
		"KAS shows activity in the send's millisecond": {marked: true, last: sentMilli, now: sent.Add(time.Second)},
		"KAS shows activity after the send":            {marked: true, last: sent.Add(time.Second), now: sent.Add(2 * time.Second)},
		"the bound passed":                             {marked: true, last: before, now: sentMilli.Add(retryInFlightBound)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var m retryMarks
			if tc.marked {
				m.mark("wf_1", sent)
			}

			until, got := m.inFlight("wf_1", tc.last, tc.now)

			if got != tc.want {
				t.Errorf("inFlight = %v, want %v", got, tc.want)
			}
			if tc.want && !until.Equal(sentMilli.Add(retryInFlightBound)) {
				t.Errorf("inFlight lapses at %v, want %v", until, sentMilli.Add(retryInFlightBound))
			}
		})
	}
}

// A fresh read drops the mark only once it shows activity at or after the send; once dropped, a later read of older
// activity does not bring it back.
func TestRetryMarks_DropOvertakenNeedsActivitySinceTheSend(t *testing.T) {
	t.Parallel()
	sent := time.UnixMilli(1_800_000_000_000)
	var m retryMarks
	m.mark("wf_1", sent)

	m.dropOvertaken("wf_1", sent.Add(-time.Millisecond))
	if _, ok := m.inFlight("wf_1", sent.Add(-time.Hour), sent.Add(time.Second)); !ok {
		t.Fatal("dropOvertaken on activity before the send dropped the mark, want it held")
	}
	m.dropOvertaken("wf_1", sent)

	if _, ok := m.inFlight("wf_1", sent.Add(-time.Hour), sent.Add(time.Second)); ok {
		t.Error("inFlight after activity in the send's millisecond = true, want the mark dropped")
	}
}

// inFlight is a read: a read of activity since the send answers not in flight without dropping the mark, which only
// dropOvertaken and the bound do.
func TestRetryMarks_InFlightNeverDropsAMark(t *testing.T) {
	t.Parallel()
	sent := time.UnixMilli(1_800_000_000_000)
	var m retryMarks
	m.mark("wf_1", sent)
	if _, ok := m.inFlight("wf_1", sent.Add(time.Second), sent.Add(time.Second)); ok {
		t.Fatal("Setup: inFlight on activity since the send = true, want false")
	}

	if _, ok := m.inFlight("wf_1", sent.Add(-time.Hour), sent.Add(time.Second)); !ok {
		t.Error("inFlight after an earlier read of newer activity = false, want the mark still held")
	}
}

// While the purge holds a run for its re-read and delete, a retry is refused and sends no verb.
func TestPurgeParentlessRuns_ARetryDuringTheDeleteIsRefused(t *testing.T) {
	kas := &purgedRun{id: "wf_1", parent: "sess_host", status: marotte.RunStatusFailed, updated: time.Now().Add(-time.Hour)}
	var h *Runtime
	var retryErr error
	tried := false
	h, rig, arm := newPurgedRunRig(t, kas, func(method string, _ map[string]any) {
		if method != methodKiroWorkflowDelete || tried {
			return
		}
		tried = true
		_, retryErr = h.runs.retry(t.Context(), "wf_1", failedAffordance(h, "wf_1"))
	}, nil, 0)
	arm()

	h.PurgeParentlessRuns(t.Context(), 0)

	if !tried {
		t.Fatal("Setup: the purge sent no delete for wf_1, so the interleaving never happened")
	}
	if code, _, _ := command.StatusOf(retryErr); code != http.StatusConflict {
		t.Errorf("retry during the purge's delete = %v, want a 409 refusal", retryErr)
	}
	if got := rig.of(methodKiroWorkflowRetry); len(got) != 0 {
		t.Errorf("a retry during the purge's delete sent %d retry verbs, want none: the run it re-drives is being deleted", len(got))
	}
	if got := deletedRunIDs(rig); !slices.Equal(got, []string{"wf_1"}) {
		t.Errorf("PurgeParentlessRuns deleted %v, want [wf_1]", got)
	}
}

// Only a fresh read that finds the run finished and past the window by its own last activity lets the purge delete it.
func TestPurgeParentlessRuns_KeepsARunItsReReadDoesNotFindPurgeable(t *testing.T) {
	const window = 24 * time.Hour
	old := time.Now().Add(-30 * window)
	list := purgeRuns(t, purgeRow("wf_1", "completed", "sess_host", old))
	cases := map[string]struct {
		inspect      json.RawMessage
		rpcErr       *marotte.RPCError
		wantDeadline bool
	}{
		"running now":                 {inspect: inspectOf("wf_1", marotte.RunStatusRunning, old)},
		"paused now":                  {inspect: inspectOf("wf_1", marotte.RunStatusPaused, old)},
		"an unknown status":           {inspect: inspectOf("wf_1", "rewinding", old)},
		"active within the window":    {inspect: inspectOf("wf_1", marotte.RunStatusCompleted, time.Now()), wantDeadline: true},
		"no last activity":            {inspect: inspectOf("wf_1", marotte.RunStatusCompleted, time.Time{})},
		"an unparsable last activity": {inspect: json.RawMessage(`{"workflowId":"wf_1","state":{"status":"completed","root":{"endedAt":"yesterday"}}}`)},
		"no state":                    {inspect: json.RawMessage(`{"workflowId":"wf_1"}`)},
		"another run's state":         {inspect: inspectOf("wf_2", marotte.RunStatusCompleted, old)},
		"an unreadable inspect":       {rpcErr: &marotte.RPCError{Code: -32603, Message: "engine draining"}},
		"an incomplete search":        {rpcErr: incompleteNotFoundErr()},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			results := map[string]json.RawMessage{methodKiroWorkflowList: list}
			var rpcErrs map[string]*marotte.RPCError
			if tc.rpcErr != nil {
				rpcErrs = map[string]*marotte.RPCError{methodKiroWorkflowInspect: tc.rpcErr}
			} else {
				results[methodKiroWorkflowInspect] = tc.inspect
			}
			h, rig := newEngineRig(t, results, rpcErrs, nil)

			res := h.PurgeParentlessRuns(t.Context(), window)

			if got := deletedRunIDs(rig); len(got) != 0 {
				t.Errorf("PurgeParentlessRuns deleted %v, want nothing: the re-read did not find wf_1 purgeable", got)
			}
			if res.Purged != 0 || res.Kept != 1 || res.Errors != 0 {
				t.Errorf("PurgeParentlessRuns = purged %d kept %d errors %d, want purged 0 kept 1 errors 0",
					res.Purged, res.Kept, res.Errors)
			}
			if got := !res.NextDeadline.IsZero(); got != tc.wantDeadline {
				t.Errorf("PurgeParentlessRuns.NextDeadline = %v, want one set %v", res.NextDeadline, tc.wantDeadline)
			}
		})
	}
}

// A run KAS no longer finds, on the re-read or on the delete itself, leaves nothing to delete there, and no later list
// names it, so the purge drops everything marotte keeps about it there and then.
func TestPurgeParentlessRuns_DropsTheRecordOfARunKASNoLongerFinds(t *testing.T) {
	list := purgeRuns(t, purgeRow("wf_1", "completed", "sess_host", time.Now().Add(-30*24*time.Hour)))
	for name, method := range map[string]string{
		"on the re-read": methodKiroWorkflowInspect,
		"on the delete":  methodKiroWorkflowDelete,
	} {
		t.Run(name, func(t *testing.T) {
			h, _ := newEngineRig(t, map[string]json.RawMessage{methodKiroWorkflowList: list},
				map[string]*marotte.RPCError{method: workflowNotFoundErr()}, nil)
			h.runs.useLog(newRunLog(t.TempDir()))
			dir, err := h.runs.log.dir("wf_1")
			if err != nil {
				t.Fatalf("Setup: run directory: %v", err)
			}
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatalf("Setup: %v", err)
			}
			h.runs.router.seedSteps("wf_1", []string{"sess_step"})
			h.translator.RecordStepSession("sess_step", "wf_1", "build", "build")

			res := h.PurgeParentlessRuns(t.Context(), 0)

			if res.Purged != 1 || res.Kept != 0 || res.Errors != 0 {
				t.Errorf("PurgeParentlessRuns = purged %d kept %d errors %d, want purged 1 kept 0 errors 0",
					res.Purged, res.Kept, res.Errors)
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("wf_1's run directory after the purge: stat = %v, want it removed", err)
			}
			if wf, ok := h.runs.router.StepRun("sess_step"); ok {
				t.Errorf("the router still routes sess_step to %q, want wf_1's rows dropped", wf)
			}
			if got := h.translator.StepAttribution("wf_1", "sess_step", nil).NodePath; got != "" {
				t.Errorf("the translator still places sess_step at %q, want wf_1's step sessions forgotten", got)
			}
		})
	}
}

func TestPurgeParentlessRuns_DeletesOnlyFinishedUnclaimedRunsPastTheWindow(t *testing.T) {
	old := time.Now().Add(-10 * 24 * time.Hour)
	runs := purgeRuns(t,
		purgeRow("wf_old_done", "completed", "sess_runhost", old),
		purgeRow("wf_old_failed", "failed", "sess_runhost2", old),
		purgeRow("wf_recent", "completed", "sess_runhost3", time.Now().Add(-time.Hour)),
		purgeRow("wf_running", "running", "sess_runhost4", old),
		purgeRow("wf_paused", "paused", "sess_runhost5", old),
		purgeRow("wf_chat_run", "completed", "sess_chat", old),
	)
	h, rig := newPurgeRig(t, runs, []string{"sess_chat"}, true)

	res := h.PurgeParentlessRuns(t.Context(), 7*24*time.Hour)

	if got, want := deletedRunIDs(rig), []string{"wf_old_done", "wf_old_failed"}; !slices.Equal(got, want) {
		t.Fatalf("PurgeParentlessRuns(7d) deleted %v, want %v", got, want)
	}
	if res.Purged != 2 {
		t.Errorf("PurgeParentlessRuns(7d).Purged = %d, want 2", res.Purged)
	}
	for _, c := range rig.of(methodKiroWorkflowDelete) {
		wantStopParams(t, "purge delete", c.params, false, "")
	}
	if res.NextDeadline.IsZero() {
		t.Error("PurgeParentlessRuns(7d) reported no deadline, want the recent run's purge time")
	}
}

func TestPurgeParentlessRuns_PurgesNothingOnceShutdownBegan(t *testing.T) {
	old := time.Now().Add(-10 * 24 * time.Hour)
	h, rig := newPurgeRig(t, purgeRuns(t, purgeRow("wf_old_done", "completed", "sess_runbridge", old)), nil, true)
	beginDraining(h)

	res := h.PurgeParentlessRuns(t.Context(), 7*24*time.Hour)

	if got := deletedRunIDs(rig); len(got) != 0 {
		t.Errorf("PurgeParentlessRuns once Shutdown began deleted %v, want none", got)
	}
	if res.Purged != 0 {
		t.Errorf("PurgeParentlessRuns once Shutdown began reported %d purged, want 0", res.Purged)
	}
}

func TestPurgeParentlessRuns_ZeroWindowSparesARunATabShows(t *testing.T) {
	recent := time.Now().Add(-time.Minute)
	runs := purgeRuns(t,
		purgeRow("wf_open", "completed", "sess_a", recent),
		purgeRow("wf_closed", "aborted", "sess_b", recent),
	)
	h, rig := newPurgeRig(t, runs, nil, true)
	if _, err := h.Membership().OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_open"}, "op-open"); err != nil {
		t.Fatalf("Setup: OpenTab: %v", err)
	}

	res := h.PurgeParentlessRuns(t.Context(), 0)

	if got, want := deletedRunIDs(rig), []string{"wf_closed"}; !slices.Equal(got, want) {
		t.Fatalf("PurgeParentlessRuns(0) deleted %v, want %v", got, want)
	}
	if !res.NextDeadline.IsZero() {
		t.Errorf("PurgeParentlessRuns(0).NextDeadline = %v, want zero: a tab-kept run waits for its tab to close", res.NextDeadline)
	}
}

// A run tab opening during the delete must not point at a run being removed; the open runs inside the RPC.
func TestPurgeParentlessRuns_ARunTabCannotOpenWhileItsDeleteIsInFlight(t *testing.T) {
	recent := time.Now().Add(-time.Minute)
	h, rig := newPurgeRig(t, purgeRuns(t, purgeRow("wf_1", "completed", "sess_a", recent)), nil, true)
	var openErr error
	opened := false
	rig.onCall = func(method string, _ map[string]any) {
		if method != methodKiroWorkflowDelete || opened {
			return
		}
		opened = true
		_, openErr = h.Membership().OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1"}, "op-race")
	}

	h.PurgeParentlessRuns(t.Context(), 0)

	if !opened {
		t.Fatal("Setup: the purge sent no delete for wf_1, so the interleaving never happened")
	}
	if got := deletedRunIDs(rig); !slices.Equal(got, []string{"wf_1"}) {
		t.Errorf("PurgeParentlessRuns(0) deleted %v, want [wf_1]", got)
	}
	if openErr == nil {
		t.Error("OpenTab(wf_1) during its delete = nil, want a refusal: the tab now shows a deleted run")
	}
}

func resumeSession(opID, sessionID string) *command.ChatCreate {
	return &command.ChatCreate{OpID: opID, Init: func(c *marotte.Chat) { c.RecordSession(sessionID) }}
}

// A claim after the snapshot still spares the run; the order is fixed, not timed.
func TestPurgeParentlessRuns_AClaimAfterTheSnapshotSparesTheRun(t *testing.T) {
	recent := time.Now().Add(-time.Minute)
	var h *Runtime
	var claimErr error
	claimed := false
	h, rig := newPurgeRigScan(t, purgeRuns(t, purgeRow("wf_1", "completed", "sess_run", recent)),
		func(ctx context.Context, before chat.OwnerScan) chat.OwnerScan {
			if claimed {
				return before
			}
			claimed = true
			_, claimErr = h.Membership().CreateChatAndOpen(ctx, resumeSession("op-resume", "sess_run"))
			return before
		})

	res := h.PurgeParentlessRuns(t.Context(), 0)

	if claimErr != nil {
		t.Fatalf("Setup: resuming sess_run after the snapshot = %v", claimErr)
	}
	if got := deletedRunIDs(rig); len(got) != 0 {
		t.Errorf("PurgeParentlessRuns(0) deleted %v, want nothing: a chat now claims the run", got)
	}
	if res.Purged != 0 || res.Kept != 1 {
		t.Errorf("PurgeParentlessRuns(0) = purged %d kept %d, want purged 0 kept 1", res.Purged, res.Kept)
	}
}

// A claim during the delete is refused, then lands after it.
func TestPurgeParentlessRuns_AClaimDuringTheDeleteIsRefusedThenLands(t *testing.T) {
	recent := time.Now().Add(-time.Minute)
	h, rig := newPurgeRig(t, purgeRuns(t, purgeRow("wf_1", "completed", "sess_run", recent)), nil, true)
	var claimErr error
	claimed := false
	rig.onCall = func(method string, _ map[string]any) {
		if method != methodKiroWorkflowDelete || claimed {
			return
		}
		claimed = true
		_, claimErr = h.Membership().CreateChatAndOpen(t.Context(), resumeSession("op-race", "sess_run"))
	}

	h.PurgeParentlessRuns(t.Context(), 0)

	if !claimed {
		t.Fatal("Setup: the purge sent no delete for wf_1, so the interleaving never happened")
	}
	if got := deletedRunIDs(rig); !slices.Equal(got, []string{"wf_1"}) {
		t.Errorf("PurgeParentlessRuns(0) deleted %v, want [wf_1]", got)
	}
	if claimErr == nil {
		t.Error("resuming sess_run during its run's delete = nil, want a refusal: the chat would claim a deleted run")
	}
	opened, err := h.Membership().CreateChatAndOpen(t.Context(), resumeSession("op-race", "sess_run"))
	if err != nil {
		t.Fatalf("resuming sess_run after the delete returned = %v, want nil", err)
	}
	if got := opened.Chat.ACPSessionID; got != "sess_run" {
		t.Errorf("the retried resume bound %q, want sess_run", got)
	}
}

func TestPurgeParentlessRuns_AnIncompleteChatScanPurgesNothing(t *testing.T) {
	old := time.Now().Add(-30 * 24 * time.Hour)
	h, rig := newPurgeRig(t, purgeRuns(t, purgeRow("wf_old", "completed", "sess_x", old)), nil, false)

	h.PurgeParentlessRuns(t.Context(), 0)

	if got := deletedRunIDs(rig); len(got) != 0 {
		t.Fatalf("PurgeParentlessRuns with an unreadable chat file deleted %v, want nothing", got)
	}
}

func TestPurgeParentlessRuns_ARunWithNoReadableAgeIsKept(t *testing.T) {
	runs := purgeRuns(t, map[string]string{"workflowId": "wf_noage", "status": "completed", "parentSessionId": "sess_x"})
	h, rig := newPurgeRig(t, runs, nil, true)

	h.PurgeParentlessRuns(t.Context(), 0)

	if got := deletedRunIDs(rig); len(got) != 0 {
		t.Fatalf("PurgeParentlessRuns deleted %v for a run with no updatedAt, want nothing", got)
	}
}

// A terminal run wakes the purge (purgeable at once with retention off); a pause does not.
func TestObserveComplete_OnlyATerminalRunWakesRetention(t *testing.T) {
	for _, tc := range []struct {
		status string
		wakes  int
	}{
		{"completed", 1},
		{"aborted", 1},
		{"paused", 0},
	} {
		t.Run(tc.status, func(t *testing.T) {
			h := newBudgetRuntime(t)
			wakes := 0
			h.Membership().SetRetentionWake(func() { wakes++ })

			h.runs.observeComplete(t.Context(), "", runNotif(methodWFRunComplete, map[string]any{
				"workflowId": "wf_1", "status": tc.status,
			}))

			if wakes != tc.wakes {
				t.Errorf("observeComplete(%q) woke retention %d times, want %d", tc.status, wakes, tc.wakes)
			}
		})
	}
}

func TestPurgeParentlessRuns_ARunNamingNoValidLaunchingSessionIsKept(t *testing.T) {
	old := time.Now().Add(-30 * 24 * time.Hour)
	runs := purgeRuns(t,
		purgeRow("wf_no_parent", "completed", "", old),
		purgeRow("wf_bad_parent", "completed", "../sess", old),
		purgeRow("wf_parentless", "completed", "sess_host", old),
	)
	h, rig := newPurgeRig(t, runs, nil, true)

	res := h.PurgeParentlessRuns(t.Context(), 0)

	if got := deletedRunIDs(rig); !slices.Equal(got, []string{"wf_parentless"}) {
		t.Errorf("PurgeParentlessRuns deleted %v, want only [wf_parentless]: a run whose launcher is unknown is not parentless", got)
	}
	if res.Kept != 2 {
		t.Errorf("PurgeParentlessRuns kept %d, want 2", res.Kept)
	}
}

func TestPurgeParentlessRuns_DeletesNothingWhileTheArrangementIsUnread(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tabs.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("Setup: write tabs.json: %v", err)
	}
	st, _ := tabs.NewStore(dir)
	old := time.Now().Add(-30 * 24 * time.Hour)
	h, rig := newEngineRigOver(t, map[string]json.RawMessage{
		methodKiroWorkflowList: purgeRuns(t, purgeRow("wf_parentless", "completed", "sess_host", old)),
	}, nil, nil, st)

	res := h.PurgeParentlessRuns(t.Context(), 0)

	if got := deletedRunIDs(rig); len(got) != 0 {
		t.Errorf("PurgeParentlessRuns deleted %v with the tab arrangement unread, want nothing: no tab is known not to show it", got)
	}
	if res.Kept != 1 {
		t.Errorf("PurgeParentlessRuns kept %d, want 1", res.Kept)
	}
}
