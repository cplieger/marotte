package kirosession

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type holderFunc func(context.Context) ([]string, error)

func (f holderFunc) HeldSessions(ctx context.Context) ([]string, error) { return f(ctx) }

var _ Holder = holderFunc(nil)

func holding(ids ...string) Holder {
	return holderFunc(func(context.Context) ([]string, error) { return ids, nil })
}

func failing(err error) Holder {
	return holderFunc(func(context.Context) ([]string, error) { return nil, err })
}

const oldAge = 24 * time.Hour

func TestOrphanSweep_KeepsTheUnionOfItsHolders(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"sess_a", "sess_b", "sess_c", "sess_orphan"} {
		makeSession(t, root, "h", id, oldAge)
	}
	writeRunState(t, filepath.Join(root, "h2"), "wf_c", parentState("sess_c"))

	NewOrphanSweep(New(root, testWorkspace), Holders{
		Records: []Holder{holding("sess_a")},
		Live:    []Holder{holding("sess_b")},
	}).Once(t.Context())

	for _, id := range []string{"sess_a", "sess_b", "sess_c"} {
		if !exists(t, filepath.Join(root, "h", id)) {
			t.Errorf("%s was reaped, though a holder names it", id)
		}
	}
	if exists(t, filepath.Join(root, "h", "sess_orphan")) {
		t.Error("sess_orphan survived, though no holder names it")
	}
}

func TestOrphanSweep_AHolderThatCannotAnswerReapsNothing(t *testing.T) {
	cause := errors.New("a chat file could not be read")
	for _, tc := range []struct {
		name     string
		records  Holder
		live     Holder
		loop     bool
		wantSkip bool
	}{
		{name: "a record holder fails", records: failing(cause), live: holding(), wantSkip: true},
		{name: "a live holder fails", records: holding("sess_a"), live: failing(cause), wantSkip: true},
		{name: "a run state cannot be read", records: holding("sess_a"), live: holding(), loop: true, wantSkip: true},
		{name: "every holder answers", records: holding("sess_a"), live: holding()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			root := t.TempDir()
			makeSession(t, root, "h", "sess_a", oldAge)
			makeSession(t, root, "h", "sess_orphan", oldAge)
			want := cause.Error()
			if tc.loop {
				want = filepath.Join(runStateIn(filepath.Join(root, "h2"), "wf_loop"), runStateFileName)
				selfLoop(t, want)
			}

			NewOrphanSweep(New(root, testWorkspace), Holders{Records: []Holder{tc.records}, Live: []Holder{tc.live}}).
				Once(t.Context())

			if survived := exists(t, filepath.Join(root, "h", "sess_orphan")); survived != tc.wantSkip {
				t.Errorf("sess_orphan survived = %v, want %v", survived, tc.wantSkip)
			}
			skipped := strings.Contains(logs.String(), "skipping orphan sweep") && strings.Contains(logs.String(), want)
			if skipped != tc.wantSkip {
				t.Errorf("a skip warning naming %q logged = %v, want %v; logs:\n%s", want, skipped, tc.wantSkip, logs)
			}
		})
	}
}

func TestOrphanSweep_ACorruptRunStateHoldsNothing(t *testing.T) {
	logs := captureLogs(t)
	root := t.TempDir()
	makeSession(t, root, "h", "sess_anchor", oldAge)
	makeSession(t, root, "h", "sess_orphan", oldAge)
	bad := writeRunState(t, filepath.Join(root, "h"), "wf_bad", "not json")

	NewOrphanSweep(New(root, testWorkspace), Holders{Records: []Holder{holding("sess_anchor")}}).Once(t.Context())

	if exists(t, filepath.Join(root, "h", "sess_orphan")) {
		t.Error("sess_orphan survived: a corrupt run state stopped the round")
	}
	out := logs.String()
	if got := strings.Count(out, "a run's state file is corrupt"); got != 1 {
		t.Errorf("corrupt-state warnings = %d, want 1; logs:\n%s", got, out)
	}
	if !strings.Contains(out, bad) {
		t.Errorf("the corrupt-state warning does not name %s; logs:\n%s", bad, out)
	}
}

// The refusal guards a config dir and a session tree on different volumes; the live and run-state holders read
// $KIRO_HOME's side, so their answers cannot lift it.
func TestOrphanSweep_RefusesWhenNoRecordHoldsASession(t *testing.T) {
	logs := captureLogs(t)
	root := t.TempDir()
	for _, id := range []string{"sess_b", "sess_c", "sess_orphan"} {
		makeSession(t, root, "h", id, oldAge)
	}
	writeRunState(t, filepath.Join(root, "h"), "wf_c", parentState("sess_c"))

	NewOrphanSweep(New(root, testWorkspace), Holders{
		Records: []Holder{holding()},
		Live:    []Holder{holding("sess_b")},
	}).Once(t.Context())

	for _, id := range []string{"sess_b", "sess_c", "sess_orphan"} {
		if !exists(t, filepath.Join(root, "h", id)) {
			t.Errorf("%s was reaped while no chat record held a session", id)
		}
	}
	if !strings.Contains(logs.String(), "REFUSING orphan sweep") {
		t.Errorf("no refusal logged; logs:\n%s", logs)
	}
}

// The reaper roots at $KIRO_HOME, so a boot that never owns its config dir must never read a keep-list to reap by.
func TestOrphanSweepRun_WaitsForItsGate(t *testing.T) {
	counting := func(calls *atomic.Int32) Holder {
		return holderFunc(func(context.Context) ([]string, error) {
			calls.Add(1)
			return []string{"sess_live"}, nil
		})
	}
	t.Run("a gate that never closes reads nothing", func(t *testing.T) {
		var calls atomic.Int32
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		NewOrphanSweep(New(t.TempDir(), testWorkspace), Holders{Records: []Holder{counting(&calls)}}).
			Run(ctx, make(chan struct{}))

		if got := calls.Load(); got != 0 {
			t.Errorf("the keep-list was read %d time(s) behind a closed-off gate, want 0", got)
		}
	})
	t.Run("a closed gate releases the round", func(t *testing.T) {
		var calls atomic.Int32
		gate := make(chan struct{})
		close(gate)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			NewOrphanSweep(New(t.TempDir(), testWorkspace), Holders{Records: []Holder{counting(&calls)}}).Run(ctx, gate)
		}()
		deadline := time.Now().Add(5 * time.Second)
		for calls.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
		<-done
		if calls.Load() == 0 {
			t.Error("no round ran after the gate closed, so nothing reclaims KAS session state")
		}
	})
}

func TestOrphanSweepRun_ReturnsWhenItsContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		NewOrphanSweep(New(t.TempDir(), testWorkspace), Holders{}).Run(ctx, nil)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s of its context ending")
	}
}
