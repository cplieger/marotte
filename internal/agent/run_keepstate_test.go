package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/chat/archive"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
)

// newMarkRig is a runtime over a real chat store whose c-launcher holds sess_launcher.
func newMarkRig(t *testing.T, rpcErrs map[string]*marotte.RPCError) (*Runtime, *testChatStore) {
	t.Helper()
	h, _ := newEngineRig(t, map[string]json.RawMessage{
		methodKiroWorkflowList:   purgeRuns(t),
		methodKiroWorkflowCancel: json.RawMessage(`{}`),
	}, rpcErrs, nil)
	cs := h.chatStore.(*testChatStore)
	cs.seed(t, "c-launcher", func(c *marotte.Chat) { c.RecordSession("sess_launcher") })
	return h, cs
}

func startRun(t *testing.T, h *Runtime, lane marotte.ChatID, wf, parent string) {
	t.Helper()
	h.runs.observeStart(t.Context(), lane, runNotif(methodWFRunStart, map[string]any{
		"workflowId": wf, "workflowName": "r", "parentSessionId": parent,
	}))
}

func holdsKey(t *testing.T, cs *testChatStore, id marotte.ChatID, key string) bool {
	t.Helper()
	c, found, err := cs.Lookup(t.Context(), id)
	if err != nil || !found {
		t.Fatalf("Lookup(%s) = found %v, %v", id, found, err)
	}
	_, ok := c.Keep[key]
	return ok
}

const runKey = "run:wf_1"

func TestStartRunMarks_HoldsTheFactAndRecordsItsHolders(t *testing.T) {
	h, cs := newMarkRig(t, nil)
	cs.seed(t, "c-other", nil)
	if err := h.runs.leaseStore().Put(t.Context(), &runlease.Lease{
		WorkflowID: "wf_1", Origin: runlease.OriginAgent, KeepChats: []string{"c-other"},
	}); err != nil {
		t.Fatal(err)
	}
	startRun(t, h, "", "wf_1", "sess_launcher")
	if !holdsKey(t, cs, "c-launcher", runKey) {
		t.Error("run_start wrote no run fact on the chat owning its parent session")
	}
	l, _ := h.runs.lease("wf_1")
	if !slices.Equal(l.KeepChats, []string{"c-launcher", "c-other"}) {
		t.Errorf("KeepChats = %v, want the union [c-launcher c-other]; a resume's start removed a holder", l.KeepChats)
	}
}

func TestStartRunMarks_AnUnlistableChatsDirectoryMarksTheRunUntilReattributed(t *testing.T) {
	h, cs := newMarkRig(t, nil)
	unlisted := true
	cs.ownerScan = func(_ context.Context, s chat.OwnerScan) chat.OwnerScan {
		if unlisted {
			return chat.OwnerScan{}
		}
		return s
	}
	startRun(t, h, "", "wf_1", "sess_launcher")
	if l, _ := h.runs.lease("wf_1"); !l.Unattributed {
		t.Fatal("a run whose launcher could not be named carries no unattributed mark")
	}
	if v, _ := cs.PurgeIdle(t.Context(), "c-launcher", time.Now().Add(time.Hour)); v.Outcome != archive.PurgeKept {
		t.Errorf("PurgeIdle with an unattributed run = %v, want kept", v.Outcome)
	}
	unlisted = false
	h.runs.reattributeRuns(t.Context())
	l, _ := h.runs.lease("wf_1")
	if l.Unattributed || !slices.Contains(l.KeepChats, "c-launcher") || !holdsKey(t, cs, "c-launcher", runKey) {
		t.Errorf("after ReattributeRuns: lease %+v, fact %v; want attributed to c-launcher",
			l, holdsKey(t, cs, "c-launcher", runKey))
	}
}

func TestRecordedRuns_ListsValidRunDirectories(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"wf_a", "wf_b", ".hidden"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "stray"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	rs := &Runs{log: &runLog{root: root}}
	got, err := rs.RecordedRuns(t.Context())
	if err != nil || !slices.Equal(got, []string{"wf_a", "wf_b"}) {
		t.Errorf("RecordedRuns = %v, %v, want [wf_a wf_b]", got, err)
	}
	rs.log.root = filepath.Join(root, "missing")
	if got, err := rs.RecordedRuns(t.Context()); err != nil || got != nil {
		t.Errorf("RecordedRuns of a missing root = %v, %v, want none", got, err)
	}
}

func markToken(t *testing.T, cs *testChatStore, id marotte.ChatID) string {
	t.Helper()
	c, found, err := cs.Lookup(t.Context(), id)
	if err != nil || !found {
		t.Fatalf("Lookup(%s) = found %v, %v", id, found, err)
	}
	return c.Keep[runKey].Token
}

// Progress is a sign of life: it rewrites the run's mark once the last write is RunMarkRefresh old, and not before.
func TestRunMadeProgress_RewritesTheMarkAtMostOncePerRefresh(t *testing.T) {
	h, cs := newMarkRig(t, nil)
	startRun(t, h, "", "wf_1", "sess_launcher")
	started := markToken(t, cs, "c-launcher")

	h.runs.RunMadeProgress("wf_1")
	if got := markToken(t, cs, "c-launcher"); got != started {
		t.Error("progress inside the refresh interval rewrote the mark")
	}
	h.runs.marks.written("wf_1", time.Now().Add(-chat.RunMarkRefresh))
	h.runs.RunMadeProgress("wf_1")
	if got := markToken(t, cs, "c-launcher"); got == started {
		t.Error("progress past the refresh interval left the mark as the start wrote it")
	}
}

// An unattributed run whose lease ends before the chats can be named is still attributed later, so its mark is
// written and the purge stops keeping every chat for it.
func TestReattributeRuns_AttributesARunWhoseLeaseEndedUnattributed(t *testing.T) {
	h, cs := newMarkRig(t, nil)
	cs.seed(t, "c-idle", nil)
	unlisted := true
	cs.ownerScan = func(_ context.Context, s chat.OwnerScan) chat.OwnerScan {
		if unlisted {
			return chat.OwnerScan{}
		}
		return s
	}
	startRun(t, h, "", "wf_1", "sess_launcher")
	h.runs.releaseLease(t.Context(), "wf_1")
	unlisted = false

	h.runs.reattributeRuns(t.Context())

	if !holdsKey(t, cs, "c-launcher", runKey) {
		t.Error("the ended run's launcher holds no mark after the reattribution")
	}
	if v, err := cs.PurgeIdle(t.Context(), "c-idle", time.Now().Add(time.Hour)); err != nil || v.Outcome != archive.PurgePurged {
		t.Errorf("PurgeIdle(c-idle) = %+v, %v; want purged: no unattributed run is left", v, err)
	}
}
