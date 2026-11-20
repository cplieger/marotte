package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/policyfile"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/settings"
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

// failedRunInspect is KAS's inspect reply for one failed run launched from session.
func failedRunInspect(workflowID, session string) json.RawMessage {
	return json.RawMessage(`{"workflowId":"` + workflowID + `","state":{"workflowId":"` + workflowID + `",` +
		`"workflowName":"publish","status":"failed","parentSessionId":"` + session + `"}}`)
}

// launchFailedParentless launches wf_1 parentless on h and fails it, releasing its lease, and answers its host session.
func launchFailedParentless(t *testing.T, h *Runtime, br *fakeBridge) string {
	t.Helper()
	launchableRecipe(br, "wf_1")
	if _, _, _, err := h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{}, nil); err != nil {
		t.Fatalf("Setup: Launch = %v", err)
	}
	l, held := h.runs.lease("wf_1")
	if !held || l.ParentSessionID == "" {
		t.Fatalf("Setup: the launch left lease %+v (held %v), want one naming its host session", l, held)
	}
	h.runs.observeComplete(t.Context(), marotte.RunHostChatID("wf_1"), runCompleteFrame(t, "wf_1", "failed"))
	if _, held := h.runs.lease("wf_1"); held {
		t.Fatal("Setup: the failed run still holds its lease")
	}
	return l.ParentSessionID
}

// A failed parentless run retries while a chat file is unreadable, after a restart lost everything in memory: no chat
// can have launched it, so the unread chat hides no launcher.
func TestRetry_AFailedParentlessRunRetriesAcrossARestartWhileAChatFileIsUnreadable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h, _, br := newTestHub(WithConfigDir(dir))
	host := launchFailedParentless(t, h, br)
	shutdownHub(t, h)

	h, cs, br := newTestHub(WithConfigDir(dir))
	t.Cleanup(func() { shutdownHub(t, h) })
	unreadableChat(t, cs, "c9")
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowRetry: json.RawMessage(`{}`)}

	if _, err := h.runs.retry(t.Context(), "wf_1", h.runs.affordance("wf_1", launchedHead("failed", host, "publish"))); err != nil {
		t.Fatalf("Retry of a parentless run with a chat file unreadable = %v, want it retried", err)
	}
	if !slices.Contains(br.callLog(), methodKiroWorkflowRetry) {
		t.Error("the retry verb never reached KAS")
	}
}

// A run whose launching chat is the unreadable one is refused 503, saying why: the retry would host it elsewhere.
func TestRetry_AChatLaunchedRunWhoseLauncherIsUnreadableIsRefused(t *testing.T) {
	t.Parallel()
	h, cs, br := newTestHub(WithConfigDir(t.TempDir()))
	t.Cleanup(func() { shutdownHub(t, h) })
	unreadableChat(t, cs, "c9")
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowRetry: json.RawMessage(`{}`)}

	_, err := h.runs.retry(t.Context(), "wf_1", h.runs.affordance("wf_1", launchedHead("failed", "sess_c9", "publish")))
	if code, _, _ := command.StatusOf(err); code != http.StatusServiceUnavailable || !errors.Is(err, errRetryChatUnread) {
		t.Fatalf("Retry with the launcher unreadable = %v (status %d), want 503 errRetryChatUnread", err, code)
	}
	if slices.Contains(br.callLog(), methodKiroWorkflowRetry) {
		t.Error("the retry verb was sent for a run whose launcher could not be read")
	}
}

// A run whose launching chat is being deleted is refused 409, whatever the scan read.
func TestRetry_ARunWhoseLaunchingChatIsInTeardownIsRefused(t *testing.T) {
	t.Parallel()
	h, cs, br := newTestHub(WithConfigDir(t.TempDir()))
	t.Cleanup(func() { shutdownHub(t, h) })
	cs.tearDown("sess_c9")
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowRetry: json.RawMessage(`{}`)}

	_, err := h.runs.retry(t.Context(), "wf_1", h.runs.affordance("wf_1", launchedHead("failed", "sess_c9", "publish")))
	if code, _, _ := command.StatusOf(err); code != http.StatusConflict || !errors.Is(err, errRetryChatPurging) {
		t.Fatalf("Retry with the launching chat in teardown = %v (status %d), want 409 errRetryChatPurging", err, code)
	}
}

// A failed parentless run is answered parentless while a chat file is unreadable, after a restart.
func TestRunOwner_AFailedParentlessRunIsParentlessWhileAChatFileIsUnreadable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h, _, br := newTestHub(WithConfigDir(dir))
	host := launchFailedParentless(t, h, br)
	shutdownHub(t, h)

	h, cs, br := newTestHub(WithConfigDir(dir))
	t.Cleanup(func() { shutdownHub(t, h) })
	unreadableChat(t, cs, "c9")
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowInspect: failedRunInspect("wf_1", host)}

	got, err := h.runs.RunOwner(t.Context(), "wf_1")
	if err != nil || got.Verdict != command.RunParentless {
		t.Fatalf("RunOwner = (%+v, %v), want RunParentless", got, err)
	}
}

// A parentless run's run_start keeps no unread chat as a doubt holder: none of them can own its host session.
func TestObserveStart_AParentlessRunKeepsNoUnreadChat(t *testing.T) {
	t.Parallel()
	h, cs, br := newTestHub(WithConfigDir(t.TempDir()))
	t.Cleanup(func() { shutdownHub(t, h) })
	launchableRecipe(br, "wf_1")
	if _, _, _, err := h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{}, nil); err != nil {
		t.Fatalf("Setup: Launch = %v", err)
	}
	l, _ := h.runs.lease("wf_1")
	unreadableChat(t, cs, "c9")

	h.runs.observeStart(t.Context(), marotte.RunHostChatID("wf_1"), runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_1", "workflowName": "publish", "parentSessionId": l.ParentSessionID,
	}))

	l, _ = h.runs.lease("wf_1")
	if slices.Contains(l.KeepChats, "c9") {
		t.Errorf("KeepChats = %v: the unreadable chat is kept for a run it cannot have launched", l.KeepChats)
	}
}

// Deleting a parentless run drops its host record, so nothing outlives the run to vouch for its session.
func TestDelete_DropsAParentlessRunsHostRecord(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	h, cs, br := newTestHub(WithConfigDir(t.TempDir()))
	t.Cleanup(func() { shutdownHub(t, h) })
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowRetry: json.RawMessage(`{}`)}
	if _, err := h.runs.retry(t.Context(), "wf_1", h.runs.affordance("wf_1", launchedHead("failed", "sess_c9", "publish"))); err != nil {
		t.Fatalf("Setup: Retry with the launcher deleted = %v", err)
	}
	h.runs.observeComplete(t.Context(), marotte.RunHostChatID("wf_1"), runCompleteFrame(t, "wf_1", "failed"))
	if _, held := h.runs.lease("wf_1"); held {
		t.Fatal("Setup: the failed retry still holds its lease")
	}
	unreadableChat(t, cs, "c9")

	_, err := h.runs.retry(t.Context(), "wf_1", h.runs.affordance("wf_1", launchedHead("failed", "sess_c9", "publish")))
	if code, _, _ := command.StatusOf(err); code != http.StatusServiceUnavailable || !errors.Is(err, errRetryChatUnread) {
		t.Fatalf("Second retry with a chat unreadable = %v (status %d), want 503 errRetryChatUnread", err, code)
	}
	if n := countCalls(br, methodKiroWorkflowRetry); n != 1 {
		t.Errorf("the retry verb was sent %d times, want once: the refused retry reached KAS", n)
	}
}

// Reattributing an unattributed parentless run over an incomplete scan keeps no unread chat and clears its mark.
func TestReattributeRuns_AParentlessRunKeepsNoUnreadChat(t *testing.T) {
	t.Parallel()
	h, cs, br := newTestHub(WithConfigDir(t.TempDir()))
	t.Cleanup(func() { shutdownHub(t, h) })
	launchableRecipe(br, "wf_1")
	if _, _, _, err := h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{}, nil); err != nil {
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
	t.Parallel()
	h, cs, br := newTestHub(WithConfigDir(t.TempDir()))
	t.Cleanup(func() { shutdownHub(t, h) })
	launchableRecipe(br, "wf_1")
	if _, _, _, err := h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{}, nil); err != nil {
		t.Fatalf("Setup: Launch = %v", err)
	}
	l, _ := h.runs.lease("wf_1")
	cs.ownerScan = func(context.Context, chat.OwnerScan) chat.OwnerScan { return chat.OwnerScan{} }

	h.runs.observeStart(t.Context(), marotte.RunHostChatID("wf_1"), runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_1", "workflowName": "publish", "parentSessionId": l.ParentSessionID,
	}))

	if l, _ = h.runs.lease("wf_1"); l.Unattributed {
		t.Error("a parentless run carries an unattributed mark for a chats directory that could not be listed")
	}
}

// RunHostedBy names the run whose recorded host a session is, which the claim check refuses to bind into a chat, and
// names none for any other session, past a run directory that holds no host record.
func TestRunHostedBy_NamesTheParentlessRunASessionHosts(t *testing.T) {
	t.Parallel()
	h, _, br := newTestHub(WithConfigDir(t.TempDir()))
	t.Cleanup(func() { shutdownHub(t, h) })
	host := launchFailedParentless(t, h, br)
	if err := os.Mkdir(filepath.Join(h.runs.log.root, "wf_2"), 0o700); err != nil {
		t.Fatalf("Setup: %s", err)
	}

	if wf, hosted, err := h.runs.RunHostedBy(t.Context(), host); err != nil || !hosted || wf != "wf_1" {
		t.Errorf("RunHostedBy(host) = %q, %v, %v, want wf_1", wf, hosted, err)
	}
	if wf, hosted, err := h.runs.RunHostedBy(t.Context(), "sess_other"); err != nil || hosted || wf != "" {
		t.Errorf("RunHostedBy(another session) = %q, %v, %v, want none", wf, hosted, err)
	}
}

// A launch's host session is hosted from its mint, before workflow/new names the run and long before its record is
// written, and a launch that fails leaves it hosting nothing.
func TestRunHostedBy_ALaunchHostsItsSessionFromItsMint(t *testing.T) {
	t.Parallel()
	t.Run("before workflow/new", func(t *testing.T) {
		h, _, br := newTestHub(WithConfigDir(t.TempDir()))
		t.Cleanup(func() { shutdownHub(t, h) })
		launchableRecipe(br, "wf_1")
		var wf string
		var hosted bool
		var err error
		br.setOnCall(func(method string, params map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
			if method == methodKiroWorkflowNew {
				session, _ := params["parentSessionId"].(string)
				wf, hosted, err = h.runs.RunHostedBy(t.Context(), session)
			}
			return nil, nil, false
		})

		if _, _, _, lErr := h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{}, nil); lErr != nil {
			t.Fatalf("Setup: Launch = %v", lErr)
		}
		if err != nil || !hosted || wf != "" {
			t.Errorf("RunHostedBy(host) during workflow/new = %q, %v, %v, want hosted by an unnamed run", wf, hosted, err)
		}
	})
	t.Run("after a failed launch", func(t *testing.T) {
		h, _, br := newTestHub(WithConfigDir(t.TempDir()))
		t.Cleanup(func() { shutdownHub(t, h) })
		launchableRecipe(br, "wf_1")
		var session string
		br.mu.Lock()
		br.onCallFails = func(method string, params map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool, error) {
			if method != methodKiroWorkflowNew {
				return nil, nil, false, nil
			}
			session, _ = params["parentSessionId"].(string)
			return nil, nil, false, errors.New("workflow/new refused")
		}
		br.mu.Unlock()

		if _, _, _, err := h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{}, nil); err == nil {
			t.Fatal("Setup: Launch = nil, want workflow/new's failure")
		}
		if session == "" {
			t.Fatal("Setup: workflow/new named no parent session")
		}
		if wf, hosted, err := h.runs.RunHostedBy(t.Context(), session); err != nil || hosted {
			t.Errorf("RunHostedBy(host) after a failed launch = %q, %v, %v, want none", wf, hosted, err)
		}
	})
}

// An unlistable runs/ directory is an error, never "hosts nothing", which would let a host session be bound.
func TestRunHostedBy_AnUnlistableRunsDirectoryIsAnError(t *testing.T) {
	t.Parallel()
	h, _, _ := newTestHub(WithConfigDir(t.TempDir()))
	t.Cleanup(func() { shutdownHub(t, h) })
	if err := os.RemoveAll(h.runs.log.root); err != nil {
		t.Fatalf("Setup: %s", err)
	}
	if err := os.WriteFile(h.runs.log.root, nil, 0o600); err != nil {
		t.Fatalf("Setup: %s", err)
	}

	if _, _, err := h.runs.RunHostedBy(t.Context(), "sess_any"); err == nil {
		t.Error("RunHostedBy over an unlistable runs/ = nil error, want the listing failure")
	}
}

// A host record that exists but cannot be read is an error for RunHostedBy and proves nothing for parentlessHost:
// answering "hosts nothing" would let a chat bind the host session the record names once it reads again.
func TestRunHostedBy_AnUnreadableHostRecordIsAnError(t *testing.T) {
	t.Parallel()
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

			if wf, hosted, err := h.runs.RunHostedBy(t.Context(), host); err == nil {
				t.Errorf("RunHostedBy(host) over an unreadable record = %q, %v, nil, want the read failure", wf, hosted)
			}
			if h.runs.parentlessHost(t.Context(), "wf_1", host) {
				t.Error("parentlessHost over an unreadable record = true, want false")
			}
		})
	}
}

// An unleased run marotte launched parentless loads its launching session over an incomplete scan, with the launch's
// security, never refused 500 as a run whose launching chat is unknown: its host record proves no chat launched it.
func TestEnsureResident_AnUnleasedParentlessLaunchLoadsWhileAChatFileIsUnreadable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h, _, br := newTestHub(WithConfigDir(dir))
	host := launchFailedParentless(t, h, br)
	shutdownHub(t, h)

	h, cs, br := newTestHub(WithConfigDir(dir))
	t.Cleanup(func() { shutdownHub(t, h) })
	unreadableChat(t, cs, "c9")
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowInspect: inspectLaunched(t, "wf_1", marotte.RunStatusFailed, host, "publish"),
	}

	_, release, err := h.runs.ensureResident(t.Context(), "wf_1")
	release()
	if err != nil {
		t.Fatalf("ensureResident = %v, want the launching session loaded", err)
	}
	if so := br.lastSessionOpts(); so == nil || so.SessionID != host || so.Presets == nil {
		t.Errorf("the load was %+v, want the host session %s with the launch's presets", so, host)
	}
}

// A parentless run's retry after its terminal frame released its lease keeps the launch's host session and security
// profile, so a profile changed since the launch never widens or narrows a retried run's permissions.
func TestRetry_AParentlessRunKeepsItsLaunchProfileAfterItsLeaseIsReleased(t *testing.T) {
	t.Parallel()
	launched, _ := policyfile.ProfileFor(policyfile.ProfileUnrestricted)
	now, _ := policyfile.ProfileFor(policyfile.ProfileGuarded)
	if slices.Equal(launched.Presets, now.Presets) {
		t.Fatal("Setup: the two profiles carry the same presets, so this proves nothing")
	}
	dir := t.TempDir()
	writeSetting(t, dir, settings.KeySecurityProfile, policyfile.ProfileUnrestricted)
	h, _, br := newTestHub(WithConfigDir(dir))
	host := launchFailedParentless(t, h, br)
	shutdownHub(t, h)
	// The restart leaves the host session to reload, with whatever presets the retry gives it.
	writeSetting(t, dir, settings.KeySecurityProfile, policyfile.ProfileGuarded)
	h, _, br = newTestHub(WithConfigDir(dir))
	t.Cleanup(func() { shutdownHub(t, h) })
	br.setCallResult(methodKiroWorkflowRetry, retryReply(t, "wf_1", "running", "n1"))

	if _, err := h.runs.retry(t.Context(), "wf_1", h.runs.affordance("wf_1", launchedHead("failed", host, "publish"))); err != nil {
		t.Fatalf("Retry = %v", err)
	}

	l, held := h.runs.lease("wf_1")
	if !held || l.ParentSessionID != host {
		t.Fatalf("lease = %+v held %v, want one naming the launch's host session %s", l, held, host)
	}
	if presets, err := l.HostPresets(); err != nil || !slices.Equal(presets, launched.Presets) {
		t.Errorf("the retry's lease presets = %v, %v, want the launch's %v", presets, err, launched.Presets)
	}
	if so := br.lastSessionOpts(); so == nil || so.SessionID != host || !slices.Equal(so.Presets, launched.Presets) {
		t.Errorf("the retry loaded %+v, want the host session %s with the launch's presets %v", so, host, launched.Presets)
	}
}

// A run marotte launched parentless whose launch security is missing from disk is refused 409 with that cause,
// sending nothing: any profile would be a guess at its permissions, and its launching session itself can be opened.
func TestHandleRetry_AParentlessRunWithNoRecordedLaunchProfileIsRefusedForIt(t *testing.T) {
	t.Parallel()
	h, _, br := newTestHub(WithConfigDir(t.TempDir()))
	t.Cleanup(func() { shutdownHub(t, h) })
	host := launchFailedParentless(t, h, br)
	if err := os.Remove(filepath.Join(h.runs.log.root, "wf_1", hostSecurityFileName)); err != nil {
		t.Fatalf("Setup: %s", err)
	}
	br.setCallResult(methodKiroWorkflowInspect, inspectLaunched(t, "wf_1", marotte.RunStatusFailed, host, "publish"))
	br.setCallResult(methodKiroWorkflowRetry, retryReply(t, "wf_1", "running", "n1"))

	rec := httptest.NewRecorder()
	h.runRoutes.handleRetry(rec, retryReq("wf_1"))

	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), runlease.ErrNoHostSecurity.Error()) {
		t.Fatalf("POST retry = %d %s, want 409 %q", rec.Code, rec.Body.String(), runlease.ErrNoHostSecurity)
	}
	if body := rec.Body.String(); !strings.Contains(body, "Delete still works.") || strings.Contains(strings.ToLower(body), "cancel") {
		t.Errorf("the body = %s, want it to name delete and not cancel, which a failed run does not offer", body)
	}
	if br.called(methodKiroWorkflowRetry) {
		t.Error("the retry verb was sent for a run whose launch profile is unknown")
	}
	if _, held := h.runs.lease("wf_1"); held {
		t.Error("the refused retry left a lease")
	}
}
