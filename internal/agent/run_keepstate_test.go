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
	"github.com/cplieger/marotte/internal/launchrule"
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

const testEventKey = "github.com/o/r/run/7/success"

// triggeredMarkRig is newMarkRig whose wf_1 holds rule-1's triggered lease, launched from c-launcher's session.
func triggeredMarkRig(t *testing.T, fc *fakeConfirmer) *Runtime {
	t.Helper()
	h, _ := newMarkRig(t, nil)
	h.runs.confirmer = fc
	h.runs.grantLease(t.Context(), "wf_1", "publish", eventLaunch(), hostSec())
	return h
}

// The run's first run_start confirms its event once; a resume's run_start of the same life confirms nothing more.
func TestRunStartConfirmsTriggeredEvent(t *testing.T) {
	fc := &fakeConfirmer{live: map[string]string{testEventKey: "wf_1"}}
	h := triggeredMarkRig(t, fc)

	startRun(t, h, "", "wf_1", "sess_launcher")
	startRun(t, h, "", "wf_1", "sess_launcher")

	want := "ConfirmEvent rule-1 " + testEventKey + " wf_1"
	if got := fc.recorded(); !slices.Equal(got, []string{want}) {
		t.Errorf("confirmer calls = %q, want exactly [%q]", got, want)
	}
	if got := fc.launchedKeys(); !slices.Equal(got, []string{testEventKey}) {
		t.Errorf("launched = %q, want the event's key", got)
	}
}

// Only a triggered lease names an event: every other origin's run_start reaches no confirmer. A retry under the kept
// triggered lease begins a new life, so its run_start is handed over again for the confirmer to decide.
func TestRunStartWithoutRuleConfirmsNothing(t *testing.T) {
	for name, o := range map[string]launchOrigin{
		"manual":    manualLaunch(),
		"scheduled": scheduledLaunch(launchrule.Row{ID: "sched-1"}, time.Now().Add(time.Hour)),
	} {
		t.Run(name, func(t *testing.T) {
			fc := &fakeConfirmer{}
			h, _ := newMarkRig(t, nil)
			h.runs.confirmer = fc
			h.runs.grantLease(t.Context(), "wf_1", "publish", o, hostSec())

			startRun(t, h, "", "wf_1", "sess_launcher")

			if got := fc.recorded(); len(got) != 0 {
				t.Errorf("confirmer calls = %q, want none", got)
			}
		})
	}
	t.Run("granted by run_start", func(t *testing.T) {
		fc := &fakeConfirmer{}
		h, _ := newMarkRig(t, nil)
		h.runs.confirmer = fc

		startRun(t, h, "", "wf_1", "sess_launcher")

		if _, held := h.runs.lease("wf_1"); !held {
			t.Fatal("Setup: run_start granted no lease")
		}
		if got := fc.recorded(); len(got) != 0 {
			t.Errorf("confirmer calls = %q, want none", got)
		}
	})
	t.Run("retry under the kept triggered lease", func(t *testing.T) {
		fc := &fakeConfirmer{}
		h := triggeredMarkRig(t, fc)
		startRun(t, h, "", "wf_1", "sess_launcher")

		h.runs.restartLife(t.Context(), "wf_1")
		startRun(t, h, "", "wf_1", "sess_launcher")

		if n := fc.count("ConfirmEvent rule-1 " + testEventKey + " wf_1"); n != 2 {
			t.Errorf("ConfirmEvent calls = %d, want 2: the retried run's run_start was not handed over", n)
		}
	})
}

// A run_start and an outcome of a run whose rule was deleted write no state file for the rule.
func TestConfirmForDeletedRuleWritesNothing(t *testing.T) {
	dir := t.TempDir()
	st, err := launchrule.NewStore(dir)
	if err != nil {
		t.Fatalf("launchrule.NewStore: %v", err)
	}
	rule := putClockRule(t, st, "bundled://publish", launchrule.Spec{Freq: launchrule.FreqDaily, Hour: 2}, true)
	if err := st.Delete(t.Context(), rule.ID); err != nil {
		t.Fatalf("Setup: Delete: %v", err)
	}
	stateFile := filepath.Join(dir, "launch-state", rule.ID+".json")
	h, _ := newMarkRig(t, nil)
	h.runs.launchRules = st
	h.runs.confirmer = &fakeConfirmer{}
	o := triggeredLaunch(rule.ID, testEventKey, "github.com/o/r/run/7", "run 7")
	h.runs.grantLease(t.Context(), "wf_1", "publish", o, hostSec())

	startRun(t, h, "", "wf_1", "sess_launcher")
	h.runs.observeComplete(t.Context(), "", runNotif(methodWFRunComplete, map[string]any{
		"workflowId": "wf_1", "status": "completed",
	}))

	if _, err := os.Stat(stateFile); !os.IsNotExist(err) {
		t.Errorf("state file of the deleted rule: Stat err = %v, want not exist", err)
	}
	if rules := st.Clocks(); len(rules) != 0 {
		t.Errorf("rules after the run = %+v, want none: the deleted rule came back", rules)
	}
}

// run_start hands the frame's own run to the confirmer, which confirms only the live attempt naming it: a run whose
// attempt was abandoned, or whose event's live attempt is another run, leaves the key queued.
func TestRunStartOfAbandonedAttemptConfirmsNothing(t *testing.T) {
	for name, live := range map[string]map[string]string{
		"abandoned":          {},
		"another run's live": {testEventKey: "wf_2"},
	} {
		t.Run(name, func(t *testing.T) {
			fc := &fakeConfirmer{live: live}
			h := triggeredMarkRig(t, fc)

			startRun(t, h, "", "wf_1", "sess_launcher")

			if n := fc.count("ConfirmEvent rule-1 " + testEventKey + " wf_1"); n != 1 {
				t.Errorf("ConfirmEvent for wf_1 called %d times, want 1 naming the frame's run", n)
			}
			if got := fc.launchedKeys(); len(got) != 0 {
				t.Errorf("launched = %q, want the key still queued", got)
			}
		})
	}
}

// A boot-held attempt's run_start that arrives once the runtime exists, with no trigger runner yet, still confirms:
// the confirmer is wired at construction and the lease is loaded from disk.
func TestRunStartBeforeRunnerConstructedConfirms(t *testing.T) {
	dir := t.TempDir()
	seed, err := runlease.NewStore(dir)
	if err != nil {
		t.Fatalf("runlease.NewStore: %v", err)
	}
	seedRuns := &Runs{chats: newTestChatStore(), leases: seed}
	if !seedRuns.grantLease(t.Context(), "wf_1", "publish", eventLaunch(), hostSec()) {
		t.Fatal("Setup: the triggered lease was not persisted")
	}
	loaded, err := runlease.NewStore(dir)
	if err != nil {
		t.Fatalf("reopen runlease.NewStore: %v", err)
	}
	fc := &fakeConfirmer{bootHeld: map[string]bool{"wf_1": true}, live: map[string]string{testEventKey: "wf_1"}}
	h, _ := newMarkRig(t, nil)
	h.runs.mu.Lock()
	h.runs.leases = loaded
	h.runs.confirmer = fc
	h.runs.mu.Unlock()

	startRun(t, h, "", "wf_1", "sess_launcher")

	if got := fc.launchedKeys(); !slices.Equal(got, []string{testEventKey}) {
		t.Errorf("launched = %q, want the boot-held attempt's key confirmed", got)
	}
}
