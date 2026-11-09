package composition

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/runlease"
)

const (
	sweepReapedLine = "kirosession: orphan sweep reaped sessions"
	sweepSkipLine   = "kirosession: skipping orphan sweep"
	sweepWitness    = "sess_orphan"
	sweepAnchor     = "sess_chat"
)

// sweepFixture is a config dir and a Kiro home laid down before a boot, at the paths bootSeam derives from root.
type sweepFixture struct {
	root        string
	workDir     string
	sessionsDir string
}

// newSweepFixture lays down a chat whose chain is sweepAnchor, so a round is never refused for an empty keep-list,
// and the unheld, old witness session a round reaps.
func newSweepFixture(t *testing.T) sweepFixture {
	t.Helper()
	root := seamRoot(t)
	f := sweepFixture{root: root, workDir: filepath.Join(root, "work"), sessionsDir: filepath.Join(root, "kiro", "sessions")}
	chats, err := chat.NewStore(filepath.Join(root, "chats"))
	if err != nil {
		t.Fatalf("Setup: chat store: %v", err)
	}
	seedChat(t, chats, "c-anchor", sweepAnchor)
	f.session(t, "ba", sweepAnchor)
	f.session(t, "ba", sweepWitness)
	return f
}

// session writes a KAS session directory in bucket that names the rig's workspace and no workflow, old enough to reap.
func (f sweepFixture) session(t *testing.T, bucket, id string) {
	t.Helper()
	dir := filepath.Join(f.sessionsDir, bucket, id)
	writeFixture(t, filepath.Join(dir, "session.json"), fmt.Sprintf(`{"workspacePaths":[%q]}`, f.workDir))
	old := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatalf("Setup: age %s: %v", dir, err)
	}
}

// runState writes KAS's state for run wf in bucket, as kiro-cli lays it down.
func (f sweepFixture) runState(t *testing.T, bucket, wf, status, parent string) {
	t.Helper()
	writeFixture(t, filepath.Join(f.sessionsDir, bucket, "workflows", wf, "workflow-state.json"),
		fmt.Sprintf(`{"workflowId":%q,"status":%q,"parentSessionId":%q}`, wf, status, parent))
}

func (f sweepFixture) exists(bucket, id string) bool {
	_, err := os.Stat(filepath.Join(f.sessionsDir, bucket, id))
	return err == nil
}

// bootAndSweep boots over the fixture and waits until a round has reaped the witness and finished.
func (f sweepFixture) bootAndSweep(t *testing.T, logs *seamLogs) {
	t.Helper()
	bootSeam(t, f.root, newSeamEngine(t), logs, seamOpts{})
	waitFor(t, "a session sweep round that reaped the witness", func() bool {
		return !f.exists("ba", sweepWitness) && logs.count(sweepReapedLine) > 0
	})
}

func TestSessionSweep_KeepsAPausedParentlessRunsHostSessionAcrossARestart(t *testing.T) {
	logs := captureSeamLogs(t)
	f := newSweepFixture(t)
	if err := openRunLeaseStore(f.root).Put(t.Context(), &runlease.Lease{
		WorkflowID: "wf_host", Origin: runlease.OriginManual, ParentSessionID: "sess_host",
		Host: &runlease.HostSecurity{Presets: []string{}},
	}); err != nil {
		t.Fatalf("Setup: lease: %v", err)
	}
	f.runState(t, "bb", "wf_host", "paused", "sess_host")
	f.session(t, "ba", "sess_host")

	f.bootAndSweep(t, logs)

	if !f.exists("ba", "sess_host") {
		t.Error("a paused run's host session was reaped, so answering, resuming or retrying it cannot load it")
	}
	if !f.exists("ba", sweepAnchor) {
		t.Error("the anchor chat's session was reaped")
	}
}

func TestSessionSweep_KeepsAFailedParentlessRunsHostSessionForRetry(t *testing.T) {
	logs := captureSeamLogs(t)
	f := newSweepFixture(t)
	f.runState(t, "bb", "wf_failed", "failed", "sess_host")
	f.session(t, "ba", "sess_host")

	f.bootAndSweep(t, logs)

	if !f.exists("ba", "sess_host") {
		t.Error("a failed run's host session was reaped, so Retry cannot load it")
	}
}

func TestSessionSweep_AnUnreadableRunStateReapsNothing(t *testing.T) {
	logs := captureSeamLogs(t)
	f := newSweepFixture(t)
	state := filepath.Join(f.sessionsDir, "bb", "workflows", "wf_loop", "workflow-state.json")
	if err := os.MkdirAll(filepath.Dir(state), 0o700); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if err := os.Symlink("workflow-state.json", state); err != nil {
		t.Fatalf("Setup: self-loop symlink: %v", err)
	}

	bootSeam(t, f.root, newSeamEngine(t), logs, seamOpts{})
	waitFor(t, "a session sweep round", func() bool {
		return !f.exists("ba", sweepWitness) || logs.has(sweepSkipLine)
	})

	if !f.exists("ba", sweepWitness) {
		t.Error("the sweep reaped a session while a run's state file could not be read")
	}
	if !logs.has(sweepSkipLine, state) {
		t.Errorf("no %q warning naming %s", sweepSkipLine, state)
	}
}
