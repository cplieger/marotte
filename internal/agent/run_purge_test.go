package agent

import (
	"context"
	"encoding/json"
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

func inspectOf(workflowID string, status marotte.RunStatus) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"workflowId": workflowID, "state": map[string]any{"status": status}})
	return raw
}

// inspectFromList answers an inspect of a listed run with the status the list gives it.
func inspectFromList(list json.RawMessage) func(string, map[string]any) (json.RawMessage, bool) {
	var rows struct {
		Runs []struct {
			WorkflowID string            `json:"workflowId"`
			Status     marotte.RunStatus `json:"status"`
		} `json:"runs"`
	}
	_ = json.Unmarshal(list, &rows)
	return func(method string, params map[string]any) (json.RawMessage, bool) {
		if method != methodKiroWorkflowInspect {
			return nil, false
		}
		id, _ := params[keyWorkflowID].(string)
		for _, r := range rows.Runs {
			if r.WorkflowID == id {
				return inspectOf(id, r.Status), true
			}
		}
		return nil, false
	}
}

// purgedRun is one parentless run as KAS holds it: listed and inspected at its current status, which a retry sets
// running before it answers.
type purgedRun struct {
	updated    time.Time
	id, parent string
	status     marotte.RunStatus
	mu         sync.Mutex
}

func (k *purgedRun) answer(method string, params map[string]any) (json.RawMessage, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	switch method {
	case methodKiroWorkflowList:
		raw, _ := json.Marshal(map[string]any{"runs": []map[string]string{purgeRow(k.id, string(k.status), k.parent, k.updated)}})
		return raw, true
	case methodKiroWorkflowInspect:
		if id, _ := params[keyWorkflowID].(string); id == k.id {
			return inspectOf(k.id, k.status), true
		}
	case methodKiroWorkflowRetry:
		k.status = marotte.RunStatusRunning
		raw, _ := json.Marshal(map[string]any{"workflowId": k.id, "status": k.status, "retriedNodeIds": []string{"build"}})
		return raw, true
	}
	return nil, false
}

func (k *purgedRun) statusNow() marotte.RunStatus {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.status
}

// newPurgedRunRig is a purge rig over kas. Once arm is called, every engine call runs onCall and the first owner scan
// runs during.
func newPurgedRunRig(t *testing.T, kas *purgedRun, onCall func(string, map[string]any), during func(context.Context, *Runtime)) (h *Runtime, rig *deleteRig, arm func()) {
	t.Helper()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("Setup: tabs.NewStore: %v", err)
	}
	var armed, fired atomic.Bool
	rig = &deleteRig{
		results: map[string]json.RawMessage{methodKiroWorkflowDelete: json.RawMessage(`{}`)},
		answer:  kas.answer,
		onCall: func(method string, params map[string]any) {
			if onCall != nil && armed.Load() {
				onCall(method, params)
			}
		},
	}
	h, _ = newRigOver(t, rig, func(ctx context.Context, s chat.OwnerScan) chat.OwnerScan {
		if during != nil && armed.Load() && fired.CompareAndSwap(false, true) {
			during(ctx, h)
		}
		return s
	}, st)
	settleBoot(t, h)
	return h, rig, func() { armed.Store(true) }
}

func failedAffordance(h *Runtime, workflowID string) *runAffordance {
	return h.runs.affordance(workflowID, &runHead{Status: string(marotte.RunStatusFailed)})
}

// A retry landing between the purge's list and its delete makes the run live again, so the purge keeps it.
func TestPurgeParentlessRuns_KeepsARunARetryRevivedAfterTheList(t *testing.T) {
	kas := &purgedRun{id: "wf_1", parent: "sess_host", status: marotte.RunStatusFailed, updated: time.Now().Add(-time.Hour)}
	var retryErr error
	h, rig, arm := newPurgedRunRig(t, kas, nil, func(ctx context.Context, h *Runtime) {
		_, retryErr = h.runs.retry(ctx, "wf_1", failedAffordance(h, "wf_1"))
	})
	arm()

	res := h.PurgeParentlessRuns(t.Context(), 0)

	if retryErr != nil {
		t.Fatalf("Setup: the retry between the purge's list and its delete = %v, want nil", retryErr)
	}
	if got := kas.statusNow(); got != marotte.RunStatusRunning {
		t.Fatalf("Setup: wf_1 is %q after the retry, want running", got)
	}
	if got := deletedRunIDs(rig); len(got) != 0 {
		t.Errorf("PurgeParentlessRuns deleted %v, want nothing: the retry made wf_1 live after the purge listed it finished", got)
	}
	if res.Purged != 0 || res.Kept != 1 {
		t.Errorf("PurgeParentlessRuns = purged %d kept %d, want purged 0 kept 1", res.Purged, res.Kept)
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
	}, nil)
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

// Only an inspect that confirms the run finished lets the purge delete it.
func TestPurgeParentlessRuns_KeepsARunItsReReadDoesNotConfirmFinished(t *testing.T) {
	old := time.Now().Add(-30 * 24 * time.Hour)
	list := purgeRuns(t, purgeRow("wf_1", "completed", "sess_host", old))
	cases := map[string]struct {
		inspect json.RawMessage
		rpcErr  *marotte.RPCError
	}{
		"running now":           {inspect: inspectOf("wf_1", marotte.RunStatusRunning)},
		"paused now":            {inspect: inspectOf("wf_1", marotte.RunStatusPaused)},
		"an unknown status":     {inspect: inspectOf("wf_1", "rewinding")},
		"no state":              {inspect: json.RawMessage(`{"workflowId":"wf_1"}`)},
		"another run's state":   {inspect: inspectOf("wf_2", marotte.RunStatusCompleted)},
		"an unreadable inspect": {rpcErr: &marotte.RPCError{Code: -32603, Message: "engine draining"}},
		"a run KAS cannot find": {rpcErr: workflowNotFoundErr()},
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

			res := h.PurgeParentlessRuns(t.Context(), 0)

			if got := deletedRunIDs(rig); len(got) != 0 {
				t.Errorf("PurgeParentlessRuns deleted %v, want nothing: the re-read did not confirm wf_1 finished", got)
			}
			if res.Purged != 0 || res.Kept != 1 || res.Errors != 0 {
				t.Errorf("PurgeParentlessRuns = purged %d kept %d errors %d, want purged 0 kept 1 errors 0",
					res.Purged, res.Kept, res.Errors)
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
