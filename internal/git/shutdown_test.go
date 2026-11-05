package git

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// forceRefreshAndLeave sends a `?fetch=1` read whose client has already gone, so the handler
// returns at once and leaves its scan running behind it.
func forceRefreshAndLeave(t *testing.T, h *Handler) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/git/status-all?fetch=1", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.handleStatusAll(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// A forced refresh whose client left keeps scanning; Shutdown must cancel that scan, wait for it
// to publish, and start none afterwards.
func TestHandlerShutdown_CancelsAndWaitsForAScanInFlight(t *testing.T) {
	workDir := t.TempDir()
	repoDir := filepath.Join(workDir, "myrepo")
	if err := os.Mkdir(repoDir, 0o750); err != nil {
		t.Fatal(err)
	}
	initFixtureRepo(t, repoDir)
	runGit(t, repoDir, "remote", "add", "origin", "ssh://example.invalid/r.git")
	// The fetch's ssh transport touches the marker and then holds the connection open until git
	// is killed: `cat` ends at the EOF git's death leaves on its stdin.
	marker := filepath.Join(t.TempDir(), "fetching")
	script := filepath.Join(t.TempDir(), "ssh")
	body := "#!/bin/sh\n: > '" + marker + "'\nexec cat 2>/dev/null\n"
	// Under ForkLock: a parallel test's fork could inherit the write fd and fail the exec with
	// ETXTBSY (https://go.dev/issue/22315).
	syscall.ForkLock.RLock()
	err := os.WriteFile(script, []byte(body), 0o700)
	syscall.ForkLock.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", script)
	t.Setenv("GIT_SSH_VARIANT", "simple")

	h := newTestHandler(t, workDir)
	// Leaves perRepoBudget as the only bound a scan Shutdown failed to cancel would end on.
	h.timeouts.Fetch = time.Minute

	forceRefreshAndLeave(t, h)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the scan's fetch never reached the transport")
		}
		time.Sleep(5 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(t.Context(), perRepoBudget/2)
	defer cancel()
	if err := h.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown with a fetch in flight: %v; the scan was not cancelled", err)
	}
	snap, running := h.statusCache.read(statusKeyFetch)
	if snap == nil || len(snap.repos) != 1 || snap.repos[0].Repo != "myrepo" {
		t.Errorf("snapshot after Shutdown = %+v, want the cancelled scan published before Shutdown returned", snap)
	}
	if running != nil {
		t.Error("a refresh is still in flight after Shutdown returned")
	}

	seeded := []allRepoStatus{repoRow("seeded")}
	seedSnapshot(&h.statusCache, statusKeyFetch, seeded, time.Now())
	forceRefreshAndLeave(t, h)
	if err := h.Shutdown(ctx); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
	if snap, _ := h.statusCache.read(statusKeyFetch); len(snap.repos) != 1 || snap.repos[0].Repo != "seeded" {
		t.Errorf("snapshot = %+v, want the seeded one: a scan ran after Shutdown", snap.repos)
	}
}
