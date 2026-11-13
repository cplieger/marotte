package agent

import (
	"fmt"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
)

// stepReadLog opens `steps` step turns of `perStep` sealed entries each on run wf1 and leaves the
// last one open. It answers the path of the first step.
func stepReadLog(t *testing.T, r *runLog, steps, perStep int) string {
	t.Helper()
	ctx := t.Context()
	for i := range steps {
		path := fmt.Sprintf("root/lane-%d:build", i)
		turn, _, err := r.open(ctx, &translate.RunStep{RunID: "wf1", NodePath: path, SessionID: fmt.Sprintf("sess-%d", i)}, "c-1")
		if err != nil {
			t.Fatalf("Setup: open %s: %v", path, err)
		}
		for j := range perStep - 1 {
			if _, err := turn.ModelSwitched(ctx, &marotte.EntryModelSwitched{From: fmt.Sprintf("m-%d", j), To: fmt.Sprintf("m-%d", j+1)}); err != nil {
				t.Fatalf("Setup: append to %s: %v", path, err)
			}
		}
	}
	return "root/lane-0:build"
}

// A step read decodes the step's turns, not the run's log: its cost is the same on a run of one
// step and on a run of 200.
func TestRunLog_AStepReadCostsTheStepNotTheRun(t *testing.T) {
	small := newRunLog(t.TempDir())
	path := stepReadLog(t, small, 1, 40)
	large := newRunLog(t.TempDir())
	stepReadLog(t, large, 200, 40)
	ctx := t.Context()
	read := func(r *runLog) func() {
		return func() {
			entries, _, _, found, err := r.stepTurns(ctx, "wf1", path)
			if err != nil || !found || len(entries) != 40 {
				t.Fatalf("stepTurns = %d entries, found %v, err %v; want the step's 40", len(entries), found, err)
			}
		}
	}

	one := testing.AllocsPerRun(3, read(small))
	many := testing.AllocsPerRun(3, read(large))

	if many > one*2 {
		t.Fatalf("a step read allocates %.0f times on a 200-step run against %.0f on a 1-step run; want the step's cost on both", many, one)
	}
}

// A live frame of the run is never held behind a step read's decode: the decode runs outside the
// run log's lock and the entry log's.
func TestRunLog_AnAppendLandsWhileAStepReadDecodes(t *testing.T) {
	r := newRunLog(t.TempDir())
	path := stepReadLog(t, r, 3, 200)
	live := r.turn("wf1", "root/lane-2:build")
	ctx := t.Context()
	appended := make(chan error, 1)
	reached := false
	stepReadHook = func() {
		reached = true
		go func() {
			_, err := live.ModelSwitched(ctx, &marotte.EntryModelSwitched{From: "a", To: "b"})
			appended <- err
		}()
		select {
		case err := <-appended:
			if err != nil {
				t.Errorf("append during the decode: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("an append waited on a step read's decode")
		}
	}
	t.Cleanup(func() { stepReadHook = nil })

	entries, _, _, found, err := r.stepTurns(ctx, "wf1", path)

	if !reached {
		t.Fatal("the step read never decoded outside its locks")
	}
	if err != nil || !found || len(entries) != 200 {
		t.Fatalf("stepTurns = %d entries, found %v, err %v; want the step's 200", len(entries), found, err)
	}
}

// A rewrite that replaces the file between the read's snapshot and its decode leaves the answer
// the snapshot's: the read holds the old file open and decodes sealed bytes no rewrite touches.
func TestRunLog_AStepReadAnswersItsSnapshotAcrossARewrite(t *testing.T) {
	r := newRunLog(t.TempDir())
	path := stepReadLog(t, r, 2, 30)
	ctx := t.Context()
	l, err := r.log(ctx, "wf1")
	if err != nil || l == nil {
		t.Fatalf("Setup: log: %v", err)
	}
	all, err := l.All()
	if err != nil {
		t.Fatalf("Setup: read: %v", err)
	}
	var kept []marotte.Entry
	for _, e := range all {
		if e.Turn != all[0].Turn {
			kept = append(kept, e)
		}
	}
	reached := false
	stepReadHook = func() {
		reached = true
		if err := l.Rewrite(ctx, kept); err != nil {
			t.Errorf("rewrite during the decode: %v", err)
		}
	}
	t.Cleanup(func() { stepReadHook = nil })

	entries, _, _, found, err := r.stepTurns(ctx, "wf1", path)

	if !reached {
		t.Fatal("the step read never decoded outside its locks")
	}
	if err != nil || !found {
		t.Fatalf("stepTurns = found %v, err %v; want the snapshot's step", found, err)
	}
	if len(entries) != 30 || entries[0].Turn != all[0].Turn {
		t.Fatalf("stepTurns = %d entries of turn %q, want the snapshot's 30 of %q", len(entries), entries[0].Turn, all[0].Turn)
	}
}
