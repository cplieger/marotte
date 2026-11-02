package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/chat/archive"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
)

// newLifeRig is a runtime over a real chat store whose c-launcher holds sess_launcher.
func newLifeRig(t *testing.T, rpcErrs map[string]*marotte.RPCError) (*Runtime, *testChatStore) {
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

func TestStartLife_HoldsTheFactAndRecordsItsHolders(t *testing.T) {
	h, cs := newLifeRig(t, nil)
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

func TestStartLife_AnUnlistableChatsDirectoryMarksTheRunUntilReattributed(t *testing.T) {
	h, cs := newLifeRig(t, nil)
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
	h.runs.ReattributeRuns(t.Context())
	l, _ := h.runs.lease("wf_1")
	if l.Unattributed || !slices.Contains(l.KeepChats, "c-launcher") || !holdsKey(t, cs, "c-launcher", runKey) {
		t.Errorf("after ReattributeRuns: lease %+v, fact %v; want attributed to c-launcher",
			l, holdsKey(t, cs, "c-launcher", runKey))
	}
}

func TestReleaseLease_EachEnd(t *testing.T) {
	cases := []struct {
		name      string
		end       runEnd
		moveStart bool
		wantLease bool
		wantFact  bool
	}{
		{name: "terminal releases lease and fact", end: endTerminal},
		{name: "cancelled releases lease and fact", end: endCancelled},
		{name: "deleted releases lease and fact", end: endDeleted},
		{name: "a rollback drops only the lease", end: endRolledBack, wantFact: true},
		{name: "a rollback after a run_start keeps the lease", end: endRolledBack, moveStart: true, wantLease: true, wantFact: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, cs := newLifeRig(t, nil)
			startRun(t, h, "", "wf_1", "sess_launcher")
			life := h.runs.lifeOf("wf_1")
			if tc.moveStart {
				startRun(t, h, "", "wf_1", "sess_launcher")
			}
			h.runs.releaseLease(t.Context(), "wf_1", tc.end, life)
			if _, held := h.runs.lease("wf_1"); held != tc.wantLease {
				t.Errorf("lease held = %v, want %v", held, tc.wantLease)
			}
			if got := holdsKey(t, cs, "c-launcher", runKey); got != tc.wantFact {
				t.Errorf("fact held = %v, want %v", got, tc.wantFact)
			}
			_, ev := tc.end.evidence()
			if got := h.runs.lives.endedThisBoot("wf_1"); got != ev {
				t.Errorf("ended mark = %v, want %v", got, ev)
			}
		})
	}
}

// G5-1: a terminal frame for life 1 whose release lands after a retry granted life 2 leaves life 2 alone.
func TestReleaseLease_AnOlderLifesEndKeepsTheRetrysLife(t *testing.T) {
	h, cs := newLifeRig(t, nil)
	startRun(t, h, "", "wf_1", "sess_launcher")
	life1 := h.runs.lifeOf("wf_1")
	start, err := h.runs.beginRetryLife(t.Context(), "wf_1", &runOrigin{parent: runParent{chat: "c-launcher", session: "sess_launcher"}})
	if err != nil {
		t.Fatalf("beginRetryLife: %v", err)
	}
	if got := h.runs.releaseLease(t.Context(), "wf_1", endTerminal, life1); got != runlease.ReleaseSuperseded {
		t.Errorf("life 1's release = %v, want ReleaseSuperseded", got)
	}
	if l, held := h.runs.lease("wf_1"); !held || l.Life != start.life.token {
		t.Errorf("lease = %+v held %v, want life 2 (%s)", l, held, start.life.token)
	}
	if !holdsKey(t, cs, "c-launcher", runKey) {
		t.Error("life 1's end released life 2's keep fact")
	}
	if h.runs.lives.endedThisBoot("wf_1") {
		t.Error("life 1's end marked the retried run ended")
	}
}

func TestReleaseLease_WithNoLeaseReleasesEveryRecordHoldingTheFact(t *testing.T) {
	h, cs := newLifeRig(t, nil)
	if _, err := cs.Hold(t.Context(), "c-launcher", runKey, chat.KeepDurable); err != nil {
		t.Fatal(err)
	}
	h.runs.releaseLease(t.Context(), "wf_1", endTerminal, h.runs.lifeOf("wf_1"))
	if holdsKey(t, cs, "c-launcher", runKey) {
		t.Error("a lease-less terminal end left the run fact on its holder")
	}
}

func TestBeginRetryLife_RefusesAGoneHolderAndRollsBackItsClaims(t *testing.T) {
	h, cs := newLifeRig(t, nil)
	cs.ownerScan = func(_ context.Context, s chat.OwnerScan) chat.OwnerScan {
		s.Owners["sess_launcher"] = []marotte.ChatID{"c-launcher", "c-zgone"}
		return s
	}
	_, err := h.runs.beginRetryLife(t.Context(), "wf_1", &runOrigin{parent: runParent{chat: "c-launcher", session: "sess_launcher"}})
	if code, _, _ := command.StatusOf(err); code != http.StatusConflict || !errors.Is(err, errRetryChatGone) {
		t.Fatalf("retry over a deleted holder = %v, want 409 errRetryChatGone", err)
	}
	if holdsKey(t, cs, "c-launcher", runKey) {
		t.Error("a refused retry left its earlier claim on c-launcher")
	}
	if _, held := h.runs.lease("wf_1"); held {
		t.Error("a refused retry minted a lease")
	}
}

func TestAbandonRetry_RollsBackOnlyOnceNoLeaseHolds(t *testing.T) {
	h, cs := newLifeRig(t, nil)
	start, err := h.runs.beginRetryLife(t.Context(), "wf_1", &runOrigin{parent: runParent{chat: "c-launcher", session: "sess_launcher"}})
	if err != nil {
		t.Fatal(err)
	}
	h.runs.abandonRetry(t.Context(), "wf_1", &start, false)
	if !holdsKey(t, cs, "c-launcher", runKey) {
		t.Error("a retry that kept its lease rolled back the fact under it")
	}
	h.runs.abandonRetry(t.Context(), "wf_1", &start, true)
	if _, held := h.runs.lease("wf_1"); held || holdsKey(t, cs, "c-launcher", runKey) {
		t.Error("a failed retry left its minted lease or its own claim")
	}
}

func TestDeleteAs_NotFoundIsTakenOnlyForTheUsersDelete(t *testing.T) {
	notFound := map[string]*marotte.RPCError{methodKiroWorkflowDelete: {
		Code: -32603, Message: "Internal error", Data: json.RawMessage(`{"details":"workflow not found"}`),
	}}
	for _, tc := range []struct {
		name      string
		answer    notFoundAnswer
		wantErr   bool
		wantLease bool
	}{
		{name: "the user's delete", answer: notFoundTaken},
		{name: "an automatic delete", answer: notFoundRefused, wantErr: true, wantLease: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, cs := newLifeRig(t, notFound)
			startRun(t, h, "", "wf_1", "sess_launcher")
			err := h.runs.deleteAs(t.Context(), "wf_1", userStop(""), tc.answer)
			if (err != nil) != tc.wantErr {
				t.Fatalf("deleteAs = %v, want error %v", err, tc.wantErr)
			}
			if _, held := h.runs.lease("wf_1"); held != tc.wantLease {
				t.Errorf("lease held = %v, want %v", held, tc.wantLease)
			}
			if got := holdsKey(t, cs, "c-launcher", runKey); got != tc.wantLease {
				t.Errorf("fact held = %v, want %v", got, tc.wantLease)
			}
		})
	}
}

// G5-2: a rewind past a run kiro-cli lost lands, and the run's lease and fact stay.
func TestCancelRun_NotFoundHasNothingToStop(t *testing.T) {
	h, cs := newLifeRig(t, map[string]*marotte.RPCError{methodKiroWorkflowCancel: {
		Code: -32603, Message: "Internal error", Data: json.RawMessage(`{"details":"workflow not found"}`),
	}})
	startRun(t, h, "", "wf_1", "sess_launcher")
	if err := h.runs.CancelRun(t.Context(), "wf_1"); err != nil {
		t.Fatalf("CancelRun of a run kiro-cli lost = %v, want nil", err)
	}
	if _, held := h.runs.lease("wf_1"); !held || !holdsKey(t, cs, "c-launcher", runKey) {
		t.Error("a not-found cancel released the run's lease or keep fact")
	}
}

func TestChatHoldsPresentRun_SkipsOnlyARunPastTheAbsenceBudget(t *testing.T) {
	leases := []runlease.Lease{{WorkflowID: "wf_1", ChatID: "c1"}, {WorkflowID: "wf_2", ChatID: "c2"}}
	past := func(wf string) bool { return wf == "wf_1" }
	if chatHoldsPresentRun(slices.Clone(leases), "c1", past) {
		t.Error("a run past the absence budget still pins its chat's idle bridge")
	}
	if !chatHoldsPresentRun(slices.Clone(leases), "c2", past) {
		t.Error("a present run stopped pinning its chat")
	}
	if !chatHoldsLiveRun(leases, "c1") {
		t.Error("chatHoldsLiveRun stopped matching a lease kept past absence")
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
