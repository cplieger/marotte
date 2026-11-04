package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
)

func TestRunHolders_NamesEveryChainOwnerAndTheLeasesHolders(t *testing.T) {
	owners := chat.SessionOwners{"sess_a": {"c-a"}, "sess_shared": {"c-a", "c-b"}}
	cases := []struct {
		name      string
		parents   []string
		lease     *runlease.Lease
		want      []marotte.ChatID
		wantKnown bool
	}{
		{name: "the chain owner of the parent", parents: []string{"sess_a"}, want: []marotte.ChatID{"c-a"}, wantKnown: true},
		{name: "a session two chains share names both", parents: []string{"sess_shared"}, want: []marotte.ChatID{"c-a", "c-b"}, wantKnown: true},
		{name: "a parent no chat holds is parentless", parents: []string{"sess_host"}, wantKnown: true},
		{name: "no parent and no lease could be any chat's", parents: []string{""}},
		{name: "an invalid parent names nothing", parents: []string{"../sess"}},
		{
			name: "a lease adds its recorded holders", parents: []string{"sess_a"},
			lease: &runlease.Lease{ChatID: "c-lane", KeepChats: []string{"c-b"}, Origin: runlease.OriginAgent},
			want:  []marotte.ChatID{"c-a", "c-b", "c-lane"}, wantKnown: true,
		},
		{name: "an agent lease with no holder and no parent is unknown", lease: &runlease.Lease{Origin: runlease.OriginAgent}},
		{name: "a manual lease with no parent is parentless", lease: &runlease.Lease{Origin: runlease.OriginManual}, wantKnown: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, known := RunHolders(tc.parents, tc.lease, owners)
			if !slices.Equal(got, tc.want) || known != tc.wantKnown {
				t.Errorf("RunHolders(%v, %+v) = %v, known %v; want %v, known %v", tc.parents, tc.lease, got, known, tc.want, tc.wantKnown)
			}
		})
	}
}

// newOwnerRig is a runtime over a real chat store whose c-launcher holds sess_launcher, with the engine answering
// the given list and inspect replies.
func newOwnerRig(t *testing.T, results map[string]json.RawMessage, rpcErrs map[string]*marotte.RPCError) *Runtime {
	t.Helper()
	h, _ := newEngineRig(t, results, rpcErrs, nil)
	h.chatStore.(*testChatStore).seed(t, "c-launcher", func(c *marotte.Chat) { c.RecordSession("sess_launcher") })
	return h
}

func TestRunOwner_AnswersOneVerdictPerSource(t *testing.T) {
	inspect := func(parent string) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"workflowId": "wf_1", "state": map[string]any{
			"workflowId": "wf_1", "status": "paused", "parentSessionId": parent,
		}})
		return raw
	}
	notFound := map[string]*marotte.RPCError{methodKiroWorkflowInspect: {Code: -32603, Message: "Workflow not found: wf_1"}}
	cases := []struct {
		name    string
		results map[string]json.RawMessage
		rpcErrs map[string]*marotte.RPCError
		lease   *runlease.Lease
		want    command.RunOwner
		wantErr bool
	}{
		{
			name:    "a listed run of the chat's session",
			results: map[string]json.RawMessage{methodKiroWorkflowList: purgeRuns(t, purgeRow("wf_1", "completed", "sess_launcher", time.Now()))},
			want:    command.RunOwner{Verdict: command.RunOwned, Holders: []marotte.ChatID{"c-launcher"}, ParentSessions: []string{"sess_launcher"}},
		},
		{
			name:    "a listed run of a session no chat holds",
			results: map[string]json.RawMessage{methodKiroWorkflowList: purgeRuns(t, purgeRow("wf_1", "completed", "sess_host", time.Now()))},
			want:    command.RunOwner{Verdict: command.RunParentless, ParentSessions: []string{"sess_host"}},
		},
		{
			name:    "an unlisted run inspect names",
			results: map[string]json.RawMessage{methodKiroWorkflowList: purgeRuns(t), methodKiroWorkflowInspect: inspect("sess_launcher")},
			want:    command.RunOwner{Verdict: command.RunOwned, Holders: []marotte.ChatID{"c-launcher"}, ParentSessions: []string{"sess_launcher"}},
		},
		{
			name:    "an unlisted run inspect cannot find is unknown, never parentless",
			results: map[string]json.RawMessage{methodKiroWorkflowList: purgeRuns(t)},
			rpcErrs: notFound,
			want:    command.RunOwner{Verdict: command.RunUnknown},
		},
		{
			name:    "a leased run answers from its lease",
			results: map[string]json.RawMessage{methodKiroWorkflowList: purgeRuns(t)},
			rpcErrs: notFound,
			lease:   &runlease.Lease{WorkflowID: "wf_1", ParentSessionID: "sess_launcher", KeepChats: []string{"c-other"}, Origin: runlease.OriginAgent},
			want:    command.RunOwner{Verdict: command.RunOwned, Holders: []marotte.ChatID{"c-launcher", "c-other"}, ParentSessions: []string{"sess_launcher"}},
		},
		{
			name:    "an unreadable inventory is an error",
			results: map[string]json.RawMessage{},
			rpcErrs: map[string]*marotte.RPCError{methodKiroWorkflowList: {Code: -32603, Message: "engine draining"}},
			wantErr: true,
		},
		{
			name:    "a listed run naming no launching session is an error",
			results: map[string]json.RawMessage{methodKiroWorkflowList: purgeRuns(t, purgeRow("wf_1", "completed", "", time.Now()))},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newOwnerRig(t, tc.results, tc.rpcErrs)
			if tc.lease != nil {
				if err := h.runs.leaseStore().Put(t.Context(), tc.lease); err != nil {
					t.Fatalf("Setup: Put lease: %v", err)
				}
			}
			got, err := h.runs.RunOwner(t.Context(), "wf_1")
			if (err != nil) != tc.wantErr {
				t.Fatalf("RunOwner(wf_1) error = %v, want error %v", err, tc.wantErr)
			}
			if got.Verdict != tc.want.Verdict || !slices.Equal(got.Holders, tc.want.Holders) ||
				!slices.Equal(got.ParentSessions, tc.want.ParentSessions) {
				t.Errorf("RunOwner(wf_1) = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestRunOwner_AnIncompleteScanWithNoHolderIsAnError(t *testing.T) {
	h := newOwnerRig(t, map[string]json.RawMessage{
		methodKiroWorkflowList: purgeRuns(t, purgeRow("wf_1", "completed", "sess_unread", time.Now())),
	}, nil)
	h.chatStore.(*testChatStore).ownerScan = func(_ context.Context, s chat.OwnerScan) chat.OwnerScan {
		s.Complete, s.Skipped = false, []marotte.ChatID{"c-unreadable"}
		return s
	}

	_, err := h.runs.RunOwner(t.Context(), "wf_1")

	if !errors.Is(err, errRunOwnersUnread) {
		t.Errorf("RunOwner(wf_1) over an incomplete scan = %v, want errRunOwnersUnread: an unread chat may own the run", err)
	}
}

// The purge teardown deletes exactly the runs whose keep facts RunHolders puts on the purged chat.
func TestDeleteForSessions_DeletesExactlyTheRunsRunHoldersAttributesToTheChat(t *testing.T) {
	rows := purgeRuns(t,
		purgeRow("wf_own", "paused", "sess_launcher", time.Now()),
		purgeRow("wf_retired", "completed", "sess_retired", time.Now()),
		purgeRow("wf_other", "running", "sess_other", time.Now()),
		purgeRow("wf_none", "completed", "", time.Now()),
	)
	h, rig := newPurgeRigScan(t, rows, nil)
	chain := []string{"sess_retired", "sess_launcher"}
	owners := chat.OwnersOfChain("c-purged", chain)

	h.runs.deleteForSessions(t.Context(), "c-purged", chain, runStop{})

	var want []string
	for _, wf := range []struct{ id, parent string }{
		{"wf_own", "sess_launcher"}, {"wf_retired", "sess_retired"}, {"wf_other", "sess_other"}, {"wf_none", ""},
	} {
		if holders, _ := RunHolders([]string{wf.parent}, nil, owners); slices.Contains(holders, "c-purged") {
			want = append(want, wf.id)
		}
	}
	slices.Sort(want)
	if got := deletedRunIDs(rig); !slices.Equal(got, want) || len(want) != 2 {
		t.Errorf("deleteForSessions deleted %v, want %v (the runs attributed to the chat)", got, want)
	}
}

func TestRunsParentedBy_NamesLiveAndShownCandidates(t *testing.T) {
	rows := purgeRuns(t,
		purgeRow("wf_running", "running", "sess_s", time.Now()),
		purgeRow("wf_done", "completed", "sess_s", time.Now()),
		purgeRow("wf_elsewhere", "running", "sess_other", time.Now()),
	)
	h, _ := newPurgeRigScan(t, rows, nil)

	if _, _, err := h.runs.RunsParentedBy(t.Context(), "sess_s"); !errors.Is(err, errRunRecordsUnread) {
		t.Errorf("RunsParentedBy before runs/ was read = %v, want errRunRecordsUnread", err)
	}

	h.runs.recorded.set(map[string]string{"wf_recorded": "sess_s", "wf_recorded_other": "sess_other"})
	if err := h.runs.leaseStore().Put(t.Context(), &runlease.Lease{
		WorkflowID: "wf_leased", ParentSessionID: "sess_s", Origin: runlease.OriginAgent,
	}); err != nil {
		t.Fatalf("Setup: Put lease: %v", err)
	}
	live, all, err := h.runs.RunsParentedBy(t.Context(), "sess_s")
	if err != nil {
		t.Fatalf("RunsParentedBy = %v", err)
	}
	if want := []string{"wf_leased", "wf_recorded", "wf_running"}; !slices.Equal(live, want) {
		t.Errorf("live = %v, want %v", live, want)
	}
	if want := []string{"wf_done", "wf_leased", "wf_recorded", "wf_running"}; !slices.Equal(all, want) {
		t.Errorf("all = %v, want %v", all, want)
	}
}

func TestRunsParentedBy_AnUnreadLeaseFileIsAnError(t *testing.T) {
	h, _ := newPurgeRigScan(t, purgeRuns(t), nil)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, runlease.FileName), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("Setup: write runs.json: %v", err)
	}
	h.runs.leases, _ = runlease.NewStore(dir)
	h.runs.recorded.set(map[string]string{})

	if _, _, err := h.runs.RunsParentedBy(t.Context(), "sess_s"); !errors.Is(err, errRunLeasesUnread) {
		t.Errorf("RunsParentedBy with the lease file unread = %v, want errRunLeasesUnread", err)
	}
}
