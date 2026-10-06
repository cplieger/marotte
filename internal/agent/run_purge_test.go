package agent

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
)

func newPurgeRig(t *testing.T, runs json.RawMessage, refs map[string]struct{}, complete bool) (*Runtime, *deleteRig) {
	t.Helper()
	return newPurgeRigRefs(t, runs, func(context.Context) (map[string]struct{}, bool) { return refs, complete })
}

func newPurgeRigRefs(t *testing.T, runs json.RawMessage, refs func(context.Context) (map[string]struct{}, bool)) (*Runtime, *deleteRig) {
	t.Helper()
	rig := &deleteRig{results: map[string]json.RawMessage{
		methodKiroWorkflowList:   runs,
		methodKiroWorkflowDelete: json.RawMessage(`{}`),
	}}
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("Setup: tabs.NewStore: %v", err)
	}
	cs := newTestChatStore()
	h := New(t.Context(), testReaperWorkDir, rig.factory, cs, WithTabs(st), WithSessionReaper(nil, refs))
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

func TestPurgeParentlessRuns_DeletesOnlyFinishedUnclaimedRunsPastTheWindow(t *testing.T) {
	old := time.Now().Add(-10 * 24 * time.Hour)
	runs := purgeRuns(t,
		purgeRow("wf_old_done", "completed", "sess_runbridge", old),
		purgeRow("wf_old_failed", "failed", "sess_runbridge2", old),
		purgeRow("wf_recent", "completed", "sess_runbridge3", time.Now().Add(-time.Hour)),
		purgeRow("wf_running", "running", "sess_runbridge4", old),
		purgeRow("wf_paused", "paused", "sess_runbridge5", old),
		purgeRow("wf_chat_run", "completed", "sess_chat", old),
	)
	h, rig := newPurgeRig(t, runs, map[string]struct{}{"sess_chat": {}}, true)

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

func TestPurgeParentlessRuns_ZeroWindowSparesARunATabShows(t *testing.T) {
	recent := time.Now().Add(-time.Minute)
	runs := purgeRuns(t,
		purgeRow("wf_open", "completed", "sess_a", recent),
		purgeRow("wf_closed", "aborted", "sess_b", recent),
	)
	h, rig := newPurgeRig(t, runs, map[string]struct{}{}, true)
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
	h, rig := newPurgeRig(t, purgeRuns(t, purgeRow("wf_1", "completed", "sess_a", recent)), map[string]struct{}{}, true)
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

func resumeSession(opID, sessionID string) command.ChatCreate {
	return command.ChatCreate{OpID: opID, Init: func(c *marotte.Chat) { c.RecordSession(sessionID) }}
}

// A claim after the snapshot still spares the run; the order is fixed, not timed.
func TestPurgeParentlessRuns_AClaimAfterTheSnapshotSparesTheRun(t *testing.T) {
	recent := time.Now().Add(-time.Minute)
	var h *Runtime
	var claimErr error
	h, rig := newPurgeRigRefs(t, purgeRuns(t, purgeRow("wf_1", "completed", "sess_run", recent)),
		func(ctx context.Context) (map[string]struct{}, bool) {
			_, claimErr = h.Membership().CreateChatAndOpen(ctx, resumeSession("op-resume", "sess_run"))
			return map[string]struct{}{}, true
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
	h, rig := newPurgeRig(t, purgeRuns(t, purgeRow("wf_1", "completed", "sess_run", recent)), map[string]struct{}{}, true)
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
	h, rig := newPurgeRig(t, purgeRuns(t, purgeRow("wf_old", "completed", "sess_x", old)), map[string]struct{}{}, false)

	h.PurgeParentlessRuns(t.Context(), 0)

	if got := deletedRunIDs(rig); len(got) != 0 {
		t.Fatalf("PurgeParentlessRuns with an unreadable chat file deleted %v, want nothing", got)
	}
}

func TestPurgeParentlessRuns_ARunWithNoReadableAgeIsKept(t *testing.T) {
	runs := purgeRuns(t, map[string]string{"workflowId": "wf_noage", "status": "completed", "parentSessionId": "sess_x"})
	h, rig := newPurgeRig(t, runs, map[string]struct{}{}, true)

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
