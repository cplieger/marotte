package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/kirosession"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/tabs"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/workflow"
)

// sweepRig is a runtime over workDir whose run logs live under cfgDir and whose KAS run state lives under sessionsDir.
type sweepRig struct {
	h           *Runtime
	rig         *deleteRig
	cfgDir      string
	sessionsDir string
	workDir     string
}

func newSweepRig(t *testing.T) *sweepRig {
	t.Helper()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("Setup: tabs.NewStore: %v", err)
	}
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	workDir := t.TempDir()
	h, rig := newEngineRigAt(t, workDir, map[string]json.RawMessage{methodKiroWorkflowList: purgeRuns(t)}, nil, nil, st,
		WithSessionReaper(kirosession.New(sessionsDir, workDir)))
	cfgDir := t.TempDir()
	h.runs.useLog(newRunLog(cfgDir))
	return &sweepRig{h: h, rig: rig, cfgDir: cfgDir, sessionsDir: sessionsDir, workDir: workDir}
}

// orphanLog writes a run log for wf whose last write was age ago.
func (s *sweepRig) orphanLog(t *testing.T, wf string, age time.Duration) {
	t.Helper()
	dir := filepath.Join(s.cfgDir, runLogDir, wf)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("Setup: mkdir %s: %v", dir, err)
	}
	file := filepath.Join(dir, "entries.jsonl")
	if err := os.WriteFile(file, []byte(`{"seq":1}`+"\n"), 0o600); err != nil {
		t.Fatalf("Setup: write %s: %v", file, err)
	}
	s.age(t, wf, age)
}

// age sets wf's log file and directory times to age ago.
func (s *sweepRig) age(t *testing.T, wf string, age time.Duration) {
	t.Helper()
	at := time.Now().Add(-age)
	dir := filepath.Join(s.cfgDir, runLogDir, wf)
	for _, p := range []string{filepath.Join(dir, "entries.jsonl"), dir} {
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatalf("Setup: chtimes %s: %v", p, err)
		}
	}
}

// kasRunState makes KAS's run-state directory for wf in bucket.
func (s *sweepRig) kasRunState(t *testing.T, bucket, wf string) {
	t.Helper()
	mkdirAll(t, filepath.Join(s.bucketRuns(bucket), wf))
}

// bucketRuns is KAS's run-state base in the session store's bucket.
func (s *sweepRig) bucketRuns(bucket string) string {
	return filepath.Join(s.sessionsDir, bucket, "workflows")
}

// workspaceRuns is KAS's run-state base inside the workspace.
func (s *sweepRig) workspaceRuns() string { return filepath.Join(s.workDir, ".kiro", "workflows") }

func mkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("Setup: mkdir %s: %v", dir, err)
	}
}

// serveList answers every workflow/list with reply, or fails it with err.
func (s *sweepRig) serveList(reply json.RawMessage, err error) {
	s.rig.setAnswer(func(method string, _ map[string]any) (json.RawMessage, bool, error) {
		if method != methodKiroWorkflowList {
			return nil, false, nil
		}
		return reply, true, err
	})
}

func (s *sweepRig) logs(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(s.cfgDir, runLogDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("list run logs: %v", err)
	}
	out := []string{}
	for _, e := range entries {
		out = append(out, e.Name())
	}
	slices.Sort(out)
	return out
}

const twentyDays = 20 * 24 * time.Hour

func TestSweepOrphanRunLogs_RemovesAnOrphanOlderThanRetention(t *testing.T) {
	s := newSweepRig(t)
	s.orphanLog(t, "wf_orphan", twentyDays)

	res := sweepOrphanLogs(t, s.h, fourteenDays)

	if got := s.logs(t); len(got) != 0 {
		t.Errorf("sweep(14d) left run logs %v, want none: wf_orphan's run is gone from the list and the KAS store", got)
	}
	if res.Purged != 1 {
		t.Errorf("sweep(14d).Purged = %d, want 1", res.Purged)
	}
}

func TestSweepOrphanRunLogs_KeepsALogWhoseRunIsListedOrOnDisk(t *testing.T) {
	for _, tc := range []struct {
		setup func(t *testing.T, s *sweepRig)
		name  string
	}{
		{
			name: "in the list",
			setup: func(t *testing.T, s *sweepRig) {
				t.Helper()
				s.serveList(purgeRuns(t, purgeRow("wf_kept", "completed", "sess_a", daysAgo(20))), nil)
			},
		},
		{
			name: "state on disk",
			setup: func(t *testing.T, s *sweepRig) {
				t.Helper()
				s.kasRunState(t, "wsbucket", "wf_kept")
			},
		},
		{
			name: "state in the workspace's run base",
			setup: func(t *testing.T, s *sweepRig) {
				t.Helper()
				mkdirAll(t, filepath.Join(s.workspaceRuns(), "wf_kept"))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSweepRig(t)
			s.orphanLog(t, "wf_kept", twentyDays)
			s.orphanLog(t, "wf_control", twentyDays)
			tc.setup(t, s)

			sweepOrphanLogs(t, s.h, fourteenDays)

			if got := s.logs(t); !slices.Equal(got, []string{"wf_kept"}) {
				t.Errorf("sweep(14d) left %v, want only [wf_kept]: its run still exists, wf_control's does not", got)
			}
		})
	}
}

func TestSweepOrphanRunLogs_AFailedOrPartialListRemovesNothing(t *testing.T) {
	for _, tc := range []struct {
		err   error
		name  string
		reply string
	}{
		{name: "list call failed", err: errors.New("engine unavailable")},
		{name: "no runs array", reply: `{}`},
		{name: "null runs", reply: `{"runs":null}`},
		{name: "row with no workflow id", reply: `{"runs":[{"workflowId":"","status":"completed"}]}`},
		{name: "undecodable reply", reply: `{"runs":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSweepRig(t)
			s.orphanLog(t, "wf_orphan", twentyDays)
			s.serveList(json.RawMessage(tc.reply), tc.err)

			res := sweepOrphanLogs(t, s.h, fourteenDays)

			if got := s.logs(t); !slices.Equal(got, []string{"wf_orphan"}) {
				t.Fatalf("sweep(14d) over a failed or partial list left %v, want [wf_orphan]: an incomplete list proves no run absent", got)
			}
			if res.Purged != 0 {
				t.Errorf("sweep(14d) over a failed or partial list Purged = %d, want 0", res.Purged)
			}

			s.serveList(purgeRuns(t), nil)
			sweepOrphanLogs(t, s.h, fourteenDays)

			if got := s.logs(t); len(got) != 0 {
				t.Errorf("sweep(14d) over a complete list left %v, want none", got)
			}
		})
	}
}

func TestSweepOrphanRunLogs_AnUnreadableKASStoreRemovesNothing(t *testing.T) {
	for _, tc := range []struct {
		runsDir func(s *sweepRig) string
		name    string
	}{
		{name: "a session store bucket", runsDir: func(s *sweepRig) string { return s.bucketRuns("wsbucket") }},
		{name: "the workspace's run base", runsDir: (*sweepRig).workspaceRuns},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSweepRig(t)
			s.orphanLog(t, "wf_orphan", twentyDays)
			runsDir := tc.runsDir(s)
			mkdirAll(t, filepath.Dir(runsDir))
			if err := os.WriteFile(runsDir, nil, 0o600); err != nil {
				t.Fatalf("Setup: write %s: %v", runsDir, err)
			}

			sweepOrphanLogs(t, s.h, fourteenDays)

			if got := s.logs(t); !slices.Equal(got, []string{"wf_orphan"}) {
				t.Fatalf("sweep(14d) with an unlistable run base left %v, want [wf_orphan]", got)
			}

			if err := os.Remove(runsDir); err != nil {
				t.Fatalf("Setup: remove %s: %v", runsDir, err)
			}
			mkdirAll(t, runsDir)
			sweepOrphanLogs(t, s.h, fourteenDays)

			if got := s.logs(t); len(got) != 0 {
				t.Errorf("sweep(14d) with a readable run base left %v, want none", got)
			}
		})
	}
}

func TestSweepOrphanRunLogs_AnUnreadableLeaseFileRemovesNothing(t *testing.T) {
	s := newSweepRig(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, runlease.FileName), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("Setup: write runs.json: %v", err)
	}
	s.h.runs.leases, _ = runlease.NewStore(dir)
	s.orphanLog(t, "wf_orphan", twentyDays)

	res := sweepOrphanLogs(t, s.h, fourteenDays)

	if got := s.logs(t); !slices.Equal(got, []string{"wf_orphan"}) {
		t.Errorf("sweep(14d) with the lease file unread left %v, want [wf_orphan]: an unread lease may name its run", got)
	}
	if res.Purged != 0 || res.Kept != 1 {
		t.Errorf("sweep(14d) with the lease file unread = purged %d kept %d, want purged 0 kept 1", res.Purged, res.Kept)
	}
}

// Whatever wakes the sweep sooner, a pass that removed logs holds the next removal runPurgePace off.
func TestSweepOrphanRunLogs_APassWithinThePaceRemovesNothing(t *testing.T) {
	s := newSweepRig(t)
	s.orphanLog(t, "wf_first", twentyDays)
	s.h.SweepOrphanRunLogs(t.Context(), fourteenDays)
	removed := time.Now()
	s.orphanLog(t, "wf_second", twentyDays)

	second := s.h.SweepOrphanRunLogs(t.Context(), fourteenDays)

	if got := s.logs(t); !slices.Equal(got, []string{"wf_second"}) {
		t.Fatalf("two back-to-back sweeps left %v, want [wf_second]: the second is inside the pace", got)
	}
	if second.Purged != 0 || second.Kept != 1 {
		t.Errorf("pass 2 = purged %d kept %d, want purged 0 kept 1", second.Purged, second.Kept)
	}
	if due := removed.Add(time.Minute); second.NextDeadline.After(due) || second.NextDeadline.Before(due.Add(-5*time.Second)) {
		t.Errorf("pass 2 NextDeadline = %v, want a minute after the first removal, about %v", second.NextDeadline, due)
	}

	sweepOrphanLogs(t, s.h, fourteenDays)

	if got := s.logs(t); len(got) != 0 {
		t.Errorf("a sweep after the pace left %v, want none", got)
	}
}

func TestSweepOrphanRunLogs_KeepsAYoungOrphan(t *testing.T) {
	t.Run("inside the window", func(t *testing.T) {
		s := newSweepRig(t)
		s.orphanLog(t, "wf_young", 2*24*time.Hour)
		s.orphanLog(t, "wf_control", twentyDays)

		sweepOrphanLogs(t, s.h, fourteenDays)

		if got := s.logs(t); !slices.Equal(got, []string{"wf_young"}) {
			t.Errorf("sweep(14d) left %v, want only [wf_young]: a 2-day-old log is inside retention", got)
		}
	})
	t.Run("inside the guard at retention 0", func(t *testing.T) {
		s := newSweepRig(t)
		s.orphanLog(t, "wf_fresh", time.Minute)
		s.orphanLog(t, "wf_control", time.Hour)

		sweepOrphanLogs(t, s.h, RunRetention{Enabled: true})

		if got := s.logs(t); !slices.Equal(got, []string{"wf_fresh"}) {
			t.Errorf("sweep(0) left %v, want only [wf_fresh]: a log a minute old may belong to a run KAS has not listed yet", got)
		}
	})
}

func TestSweepOrphanRunLogs_KeepForeverRemovesNothing(t *testing.T) {
	s := newSweepRig(t)
	s.orphanLog(t, "wf_orphan", 400*24*time.Hour)

	res := sweepOrphanLogs(t, s.h, RunRetention{Window: fourteenDays.Window})

	if got := s.logs(t); !slices.Equal(got, []string{"wf_orphan"}) {
		t.Fatalf("sweep under Keep forever left %v, want [wf_orphan]", got)
	}
	if res != (RunPurgeResult{}) {
		t.Errorf("sweep under Keep forever = %+v, want the zero result", res)
	}

	sweepOrphanLogs(t, s.h, fourteenDays)

	if got := s.logs(t); len(got) != 0 {
		t.Errorf("sweep(14d) after Keep forever left %v, want none", got)
	}
}

func TestSweepOrphanRunLogs_CapsAPassAndSkipsAPoisonItem(t *testing.T) {
	s := newSweepRig(t)
	s.orphanLog(t, "wf_poison", 40*24*time.Hour)
	failLogRemoval(t, s.h, "wf_poison")
	for i := range 52 {
		s.orphanLog(t, fmt.Sprintf("wf_orphan_%02d", i), twentyDays-time.Duration(i)*time.Minute)
	}

	before := time.Now()
	first := sweepOrphanLogs(t, s.h, fourteenDays)
	after := time.Now()

	if first.Purged != 49 || first.Errors != 1 {
		t.Errorf("pass 1 Purged, Errors = %d, %d, want 49, 1: fifty attempts, the oldest one failing", first.Purged, first.Errors)
	}
	if got := s.logs(t); len(got) != 4 || got[0] != "wf_orphan_49" || got[3] != "wf_poison" {
		t.Errorf("pass 1 left %v, want the three youngest orphans and wf_poison", got)
	}
	if first.NextDeadline.Before(before) || first.NextDeadline.After(after.Add(time.Minute)) {
		t.Errorf("pass 1 NextDeadline = %v, want within a minute of %v: three orphans wait", first.NextDeadline, before)
	}

	second := sweepOrphanLogs(t, s.h, fourteenDays)

	if second.Purged != 3 || second.Errors != 0 {
		t.Errorf("pass 2 Purged, Errors = %d, %d, want 3, 0: the poisoned log is not retried", second.Purged, second.Errors)
	}
	if got := s.logs(t); !slices.Equal(got, []string{"wf_poison"}) {
		t.Errorf("pass 2 left %v, want only [wf_poison]", got)
	}
}

func TestSweepOrphanRunLogs_KeepsALeasedOrOpenRunsLog(t *testing.T) {
	s := newSweepRig(t)
	if err := s.h.runs.leaseStore().Put(t.Context(), &runlease.Lease{WorkflowID: "wf_leased", Origin: runlease.OriginManual}); err != nil {
		t.Fatalf("Setup: lease wf_leased: %v", err)
	}
	s.orphanLog(t, "wf_leased", twentyDays)
	step := &translate.RunStep{RunID: "wf_open", NodePath: workflow.PathKey([]string{"wf_open", "review"}), SessionID: "step-session-a"}
	if _, _, err := s.h.runs.log.open(t.Context(), step, "c1"); err != nil {
		t.Fatalf("Setup: open wf_open's step: %v", err)
	}
	s.age(t, "wf_open", twentyDays)
	s.orphanLog(t, "wf_control", twentyDays)

	sweepOrphanLogs(t, s.h, fourteenDays)

	if got := s.logs(t); !slices.Equal(got, []string{"wf_leased", "wf_open"}) {
		t.Errorf("sweep(14d) left %v, want [wf_leased wf_open]: marotte still holds both runs", got)
	}
}
