package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// unreadableChat leaves a chat directory whose header does not decode, so every owner scan is incomplete.
func unreadableChat(t *testing.T, cs *testChatStore, chatID string) {
	t.Helper()
	dir := filepath.Join(cs.Dir(), chatID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("Setup: %s", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "chat.json"), []byte("{"), 0o600); err != nil {
		t.Fatalf("Setup: %s", err)
	}
}

// failedRunList is KAS's inventory naming one failed run launched from session.
func failedRunList(workflowID, session string) json.RawMessage {
	return json.RawMessage(`{"runs":[{"workflowId":"` + workflowID + `","name":"publish","workflowName":"publish",` +
		`"status":"failed","parentSessionId":"` + session + `"}]}`)
}

// launchFailedParentless launches wf_1 parentless on h and fails it, releasing its lease, and answers its host session.
func launchFailedParentless(t *testing.T, h *Runtime, br *fakeBridge) string {
	t.Helper()
	launchableRecipe(br, "wf_1")
	if _, _, _, err := h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{}); err != nil {
		t.Fatalf("Setup: Launch = %v", err)
	}
	l, held := h.runs.lease("wf_1")
	if !held || l.ParentSessionID == "" {
		t.Fatalf("Setup: the launch left lease %+v (held %v), want one naming its host session", l, held)
	}
	h.runs.observeComplete(t.Context(), runChatID("wf_1"), runCompleteFrame(t, "wf_1", "failed"))
	if _, held := h.runs.lease("wf_1"); held {
		t.Fatal("Setup: the failed run still holds its lease")
	}
	return l.ParentSessionID
}

// A failed parentless run retries while a chat file is unreadable, after a restart lost everything in memory: no chat
// can have launched it, so the unread chat hides no launcher.
func TestRetry_AFailedParentlessRunRetriesAcrossARestartWhileAChatFileIsUnreadable(t *testing.T) {
	dir := t.TempDir()
	h, _, br := newTestHub(WithConfigDir(dir))
	host := launchFailedParentless(t, h, br)
	shutdownHub(t, h)

	h, cs, br := newTestHub(WithConfigDir(dir))
	t.Cleanup(func() { shutdownHub(t, h) })
	unreadableChat(t, cs, "c9")
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowRetry: json.RawMessage(`{}`),
		methodKiroWorkflowList:  failedRunList("wf_1", host),
	}

	if _, err := h.runs.retry(t.Context(), "wf_1", h.runs.affordance("wf_1", &runHead{Status: "failed"})); err != nil {
		t.Fatalf("Retry of a parentless run with a chat file unreadable = %v, want it retried", err)
	}
	if !slices.Contains(br.callLog(), methodKiroWorkflowRetry) {
		t.Error("the retry verb never reached KAS")
	}
}

// A run whose launching chat is the unreadable one is refused 503, saying why: the retry would host it elsewhere.
func TestRetry_AChatLaunchedRunWhoseLauncherIsUnreadableIsRefused(t *testing.T) {
	h, cs, br := newTestHub(WithConfigDir(t.TempDir()))
	t.Cleanup(func() { shutdownHub(t, h) })
	unreadableChat(t, cs, "c9")
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowRetry: json.RawMessage(`{}`),
		methodKiroWorkflowList:  failedRunList("wf_1", "sess_c9"),
	}

	_, err := h.runs.retry(t.Context(), "wf_1", h.runs.affordance("wf_1", &runHead{Status: "failed"}))
	if code, _, _ := command.StatusOf(err); code != http.StatusServiceUnavailable || !errors.Is(err, errRetryChatUnread) {
		t.Fatalf("Retry with the launcher unreadable = %v (status %d), want 503 errRetryChatUnread", err, code)
	}
	if slices.Contains(br.callLog(), methodKiroWorkflowRetry) {
		t.Error("the retry verb was sent for a run whose launcher could not be read")
	}
}

// A run whose launching chat is being deleted is refused 409, whatever the scan read.
func TestRetry_ARunWhoseLaunchingChatIsInTeardownIsRefused(t *testing.T) {
	h, cs, br := newTestHub(WithConfigDir(t.TempDir()))
	t.Cleanup(func() { shutdownHub(t, h) })
	cs.tearDown("sess_c9")
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowRetry: json.RawMessage(`{}`),
		methodKiroWorkflowList:  failedRunList("wf_1", "sess_c9"),
	}

	_, err := h.runs.retry(t.Context(), "wf_1", h.runs.affordance("wf_1", &runHead{Status: "failed"}))
	if code, _, _ := command.StatusOf(err); code != http.StatusConflict || !errors.Is(err, errRetryChatPurging) {
		t.Fatalf("Retry with the launching chat in teardown = %v (status %d), want 409 errRetryChatPurging", err, code)
	}
}

// A failed parentless run is answered parentless while a chat file is unreadable, after a restart.
func TestRunOwner_AFailedParentlessRunIsParentlessWhileAChatFileIsUnreadable(t *testing.T) {
	dir := t.TempDir()
	h, _, br := newTestHub(WithConfigDir(dir))
	host := launchFailedParentless(t, h, br)
	shutdownHub(t, h)

	h, cs, br := newTestHub(WithConfigDir(dir))
	t.Cleanup(func() { shutdownHub(t, h) })
	unreadableChat(t, cs, "c9")
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowList: failedRunList("wf_1", host)}

	got, err := h.runs.RunOwner(t.Context(), "wf_1")
	if err != nil || got.Verdict != command.RunParentless {
		t.Fatalf("RunOwner = (%+v, %v), want RunParentless", got, err)
	}
}

// A parentless run's run_start keeps no unread chat as a doubt holder: none of them can own its host session.
func TestObserveStart_AParentlessRunKeepsNoUnreadChat(t *testing.T) {
	h, cs, br := newTestHub(WithConfigDir(t.TempDir()))
	t.Cleanup(func() { shutdownHub(t, h) })
	launchableRecipe(br, "wf_1")
	if _, _, _, err := h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{}); err != nil {
		t.Fatalf("Setup: Launch = %v", err)
	}
	l, _ := h.runs.lease("wf_1")
	unreadableChat(t, cs, "c9")

	h.runs.observeStart(t.Context(), runChatID("wf_1"), runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_1", "workflowName": "publish", "parentSessionId": l.ParentSessionID,
	}))

	l, _ = h.runs.lease("wf_1")
	if slices.Contains(l.KeepChats, "c9") {
		t.Errorf("KeepChats = %v: the unreadable chat is kept for a run it cannot have launched", l.KeepChats)
	}
}

// Deleting a parentless run drops its host record, so nothing outlives the run to vouch for its session.
func TestDelete_DropsAParentlessRunsHostRecord(t *testing.T) {
	h, _, br := newTestHub(WithConfigDir(t.TempDir()))
	t.Cleanup(func() { shutdownHub(t, h) })
	host := launchFailedParentless(t, h, br)
	if !h.runs.parentlessHost(t.Context(), "wf_1", host) {
		t.Fatal("Setup: the launch recorded no host session")
	}
	br.setCallResult(methodKiroWorkflowDelete, json.RawMessage(`{}`))

	if err := h.runs.delete(t.Context(), "wf_1"); err != nil {
		t.Fatalf("Delete = %v", err)
	}
	if h.runs.parentlessHost(t.Context(), "wf_1", host) {
		t.Error("the deleted run's host session is still recorded")
	}
	if dir, _ := h.runs.log.dir("wf_1"); dirExists(dir) {
		t.Errorf("the deleted run's directory %s is still on disk", dir)
	}
}

func dirExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// A retry records no host: a chat-launched run retried once over a complete scan after its launcher was deleted is
// still refused 503 when a later retry cannot read a chat, since a chat may have resumed its session since.
func TestRetry_ARetriedChatLaunchedRunIsStillRefusedWhileAChatFileIsUnreadable(t *testing.T) {
	h, cs, br := newTestHub(WithConfigDir(t.TempDir()))
	t.Cleanup(func() { shutdownHub(t, h) })
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowRetry: json.RawMessage(`{}`),
		methodKiroWorkflowList:  failedRunList("wf_1", "sess_c9"),
	}
	if _, err := h.runs.retry(t.Context(), "wf_1", h.runs.affordance("wf_1", &runHead{Status: "failed"})); err != nil {
		t.Fatalf("Setup: Retry with the launcher deleted = %v", err)
	}
	h.runs.observeComplete(t.Context(), runChatID("wf_1"), runCompleteFrame(t, "wf_1", "failed"))
	if _, held := h.runs.lease("wf_1"); held {
		t.Fatal("Setup: the failed retry still holds its lease")
	}
	unreadableChat(t, cs, "c9")

	_, err := h.runs.retry(t.Context(), "wf_1", h.runs.affordance("wf_1", &runHead{Status: "failed"}))
	if code, _, _ := command.StatusOf(err); code != http.StatusServiceUnavailable || !errors.Is(err, errRetryChatUnread) {
		t.Fatalf("Second retry with a chat unreadable = %v (status %d), want 503 errRetryChatUnread", err, code)
	}
	if n := countCalls(br, methodKiroWorkflowRetry); n != 1 {
		t.Errorf("the retry verb was sent %d times, want once: the refused retry reached KAS", n)
	}
}

// Reattributing an unattributed parentless run over an incomplete scan keeps no unread chat and clears its mark.
func TestReattributeRuns_AParentlessRunKeepsNoUnreadChat(t *testing.T) {
	h, cs, br := newTestHub(WithConfigDir(t.TempDir()))
	t.Cleanup(func() { shutdownHub(t, h) })
	launchableRecipe(br, "wf_1")
	if _, _, _, err := h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{}); err != nil {
		t.Fatalf("Setup: Launch = %v", err)
	}
	if err := h.runs.leaseStore().SetUnattributed(t.Context(), "wf_1", true); err != nil {
		t.Fatalf("Setup: SetUnattributed = %v", err)
	}
	unreadableChat(t, cs, "c9")

	h.runs.reattributeRuns(t.Context())

	l, _ := h.runs.lease("wf_1")
	if slices.Contains(l.KeepChats, "c9") {
		t.Errorf("KeepChats = %v: the unreadable chat is kept for a run it cannot have launched", l.KeepChats)
	}
	if l.Unattributed {
		t.Error("the reattributed run still carries its unattributed mark")
	}
}

// A parentless run's run_start over an unlistable chats directory is attributed: no chat can own its host session.
func TestObserveStart_AParentlessRunIsAttributedWhenTheChatsDirectoryIsUnlistable(t *testing.T) {
	h, cs, br := newTestHub(WithConfigDir(t.TempDir()))
	t.Cleanup(func() { shutdownHub(t, h) })
	launchableRecipe(br, "wf_1")
	if _, _, _, err := h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{}); err != nil {
		t.Fatalf("Setup: Launch = %v", err)
	}
	l, _ := h.runs.lease("wf_1")
	cs.ownerScan = func(context.Context, chat.OwnerScan) chat.OwnerScan { return chat.OwnerScan{} }

	h.runs.observeStart(t.Context(), runChatID("wf_1"), runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_1", "workflowName": "publish", "parentSessionId": l.ParentSessionID,
	}))

	if l, _ = h.runs.lease("wf_1"); l.Unattributed {
		t.Error("a parentless run carries an unattributed mark for a chats directory that could not be listed")
	}
}

// RunHostedBy names the run whose recorded host a session is, which the claim check refuses to bind into a chat, and
// names none for any other session.
func TestRunHostedBy_NamesTheParentlessRunASessionHosts(t *testing.T) {
	h, _, br := newTestHub(WithConfigDir(t.TempDir()))
	t.Cleanup(func() { shutdownHub(t, h) })
	host := launchFailedParentless(t, h, br)

	if wf, err := h.runs.RunHostedBy(t.Context(), host); err != nil || wf != "wf_1" {
		t.Errorf("RunHostedBy(host) = %q, %v, want wf_1", wf, err)
	}
	if wf, err := h.runs.RunHostedBy(t.Context(), "sess_other"); err != nil || wf != "" {
		t.Errorf("RunHostedBy(another session) = %q, %v, want none", wf, err)
	}
}

// An unlistable runs/ directory is an error, never "hosts nothing", which would let a host session be bound.
func TestRunHostedBy_AnUnlistableRunsDirectoryIsAnError(t *testing.T) {
	h, _, _ := newTestHub(WithConfigDir(t.TempDir()))
	t.Cleanup(func() { shutdownHub(t, h) })
	if err := os.RemoveAll(h.runs.log.root); err != nil {
		t.Fatalf("Setup: %s", err)
	}
	if err := os.WriteFile(h.runs.log.root, nil, 0o600); err != nil {
		t.Fatalf("Setup: %s", err)
	}

	if _, err := h.runs.RunHostedBy(t.Context(), "sess_any"); err == nil {
		t.Error("RunHostedBy over an unlistable runs/ = nil error, want the listing failure")
	}
}

// A host record that exists but cannot be read is an error for RunHostedBy and proves nothing for parentlessHost:
// answering "hosts nothing" would let a chat bind the host session the record names once it reads again.
func TestRunHostedBy_AnUnreadableHostRecordIsAnError(t *testing.T) {
	for name, plant := range map[string]func(path string) error{
		"a directory": func(path string) error { return os.Mkdir(path, 0o700) },
		"an oversized file": func(path string) error {
			return os.WriteFile(path, []byte(strings.Repeat("s", maxHostFileBytes+44)), 0o600)
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub(WithConfigDir(t.TempDir()))
			t.Cleanup(func() { shutdownHub(t, h) })
			host := launchFailedParentless(t, h, br)
			path := filepath.Join(h.runs.log.root, "wf_1", hostFileName)
			if err := os.Remove(path); err != nil {
				t.Fatalf("Setup: %s", err)
			}
			if err := plant(path); err != nil {
				t.Fatalf("Setup: %s", err)
			}

			if wf, err := h.runs.RunHostedBy(t.Context(), host); err == nil {
				t.Errorf("RunHostedBy(host) over an unreadable record = %q, nil, want the read failure", wf)
			}
			if h.runs.parentlessHost(t.Context(), "wf_1", host) {
				t.Error("parentlessHost over an unreadable record = true, want false")
			}
		})
	}
}
