package agent

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

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
