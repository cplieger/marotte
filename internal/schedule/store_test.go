package schedule

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
)

// TestNewStore_ReadsLegacyLastResult pins that a schedules.json written while the
// outcome was one prefix-coded string still shows each row's last outcome, and
// that the next write stores the status and reason fields only.
func TestNewStore_ReadsLegacyLastResult(t *testing.T) {
	const orphan = "The run stopped because the server restarted while it was running. Run it again, or wait for the next slot"
	legacy := `[
  {"id":"a","source":"bundled://a","spec":{"freq":"daily","hour":1,"minute":0},"enabled":true,"anchor":"2026-08-04T01:00:00Z","last_result":"started"},
  {"id":"b","source":"bundled://b","spec":{"freq":"daily","hour":1,"minute":0},"enabled":true,"anchor":"2026-08-04T01:00:00Z","last_result":"failed: needed approval for fs_write with nobody watching. Add a permission rule to allow it"},
  {"id":"c","source":"bundled://c","spec":{"freq":"daily","hour":1,"minute":0},"enabled":true,"anchor":"2026-08-04T01:00:00Z","last_result":"unknown: no terminal signal was seen and the run stayed absent for 6 hours"},
  {"id":"d","source":"bundled://d","spec":{"freq":"daily","hour":1,"minute":0},"enabled":true,"anchor":"2026-08-04T01:00:00Z","last_result":"` + orphan + `"},
  {"id":"e","source":"bundled://e","spec":{"freq":"daily","hour":1,"minute":0},"enabled":true,"anchor":"2026-08-04T01:00:00Z","last_result":"stopped: the server restarted while it was running — run it again, or wait for the next slot"}
]`
	dir := t.TempDir()
	if err := writeFile(dir, legacy); err != nil {
		t.Fatalf("seed: %v", err)
	}
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	want := map[string]Outcome{
		"bundled://a": {Status: statusStarted},
		"bundled://b": {Status: StatusFailed, Reason: "needed approval for fs_write with nobody watching. Add a permission rule to allow it"},
		"bundled://c": {Status: StatusUnknown, Reason: "no terminal signal was seen and the run stayed absent for 6 hours"},
		"bundled://d": {Status: StatusFailed, Reason: orphan},
		"bundled://e": {Status: StatusFailed, Reason: "stopped because the server restarted while it was running — run it again, or wait for the next slot"},
	}
	if got := len(st.List()); got != len(want) {
		t.Fatalf("NewStore(legacy) kept %d rows, want %d", got, len(want))
	}
	for _, e := range st.List() {
		got := Outcome{Status: e.LastStatus, Reason: e.LastReason}
		if got != want[e.Source] {
			t.Errorf("NewStore(legacy) entry %q outcome = %+v, want %+v", e.Source, got, want[e.Source])
		}
	}

	extra := Entry{Source: "bundled://z", Spec: launchrule.Spec{Freq: launchrule.FreqDaily, Hour: 3}, Enabled: true}
	if err := st.Put(t.Context(), &extra); err != nil {
		t.Fatalf("Put: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if strings.Contains(string(raw), "last_result") {
		t.Errorf("rewritten store still carries last_result:\n%s", raw)
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("decode rewritten store: %v", err)
	}
	for _, row := range rows {
		if row["source"] == "bundled://b" && (row["last_status"] != "failed" || row["last_reason"] == nil) {
			t.Errorf("rewritten row b = %v, want last_status failed and a last_reason", row)
		}
	}
}

// A schedule's model and effort survive a restart, so every later fire sends the same pick.
func TestStore_KeepsTheModelPickAcrossAReopen(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	e := Entry{Source: "bundled://x", Spec: launchrule.Spec{Freq: launchrule.FreqDaily, Hour: 3}, Enabled: true, Model: "opus", Effort: "high"}
	if err := st.Put(t.Context(), &e); err != nil {
		t.Fatalf("Put: %v", err)
	}
	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore (reopen): %v", err)
	}
	list := reopened.List()
	if len(list) != 1 || list[0].Model != "opus" || list[0].Effort != "high" {
		t.Errorf("reopened store = %+v, want the opus/high row", list)
	}
}

// The id is the schedule's URL path segment and its only handle for a delete: a source
// spelled as a path, a URL or with dot segments must still yield one canonical segment, the
// same one after a reopen, and a second Put of the same recipe must replace its row.
func TestStore_IDIsOneStableSegmentPerRecipe(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	sources := []string{
		"/workspace/.kiro/workflows/a.workflow.json",
		"/workspace/../workspace/./a.workflow.json",
		"bundled://a",
		"bundled://a ",
	}
	ids := map[string]string{}
	for _, src := range sources {
		e := Entry{Source: src, Spec: launchrule.Spec{Freq: launchrule.FreqDaily, Hour: 3}}
		if err := st.Put(t.Context(), &e); err != nil {
			t.Fatalf("Put(%q): %v", src, err)
		}
		id := e.ID()
		if id == "" || strings.ContainsAny(id, "/.%:?# ") {
			t.Errorf("ID(%q) = %q, want one canonical path segment", src, id)
		}
		if prev, dup := ids[id]; dup {
			t.Errorf("ID(%q) = ID(%q) = %q", src, prev, id)
		}
		ids[id] = src
	}
	again := Entry{Source: sources[0], Spec: launchrule.Spec{Freq: launchrule.FreqDaily, Hour: 5}}
	if err := st.Put(t.Context(), &again); err != nil {
		t.Fatalf("Put (edit): %v", err)
	}
	if got := len(st.List()); got != len(sources) {
		t.Errorf("after re-saving one recipe the store holds %d rows, want %d", got, len(sources))
	}

	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore (reopen): %v", err)
	}
	for _, e := range reopened.List() {
		if ids[e.ID()] != e.Source {
			t.Errorf("reopened row %q has id %q, which was minted for %q", e.Source, e.ID(), ids[e.ID()])
		}
	}
	if err := reopened.Delete(t.Context(), again.ID()); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, e := range reopened.List() {
		if e.Source == sources[0] {
			t.Errorf("Delete(%q) left the row for %q", again.ID(), sources[0])
		}
	}
}

// fired is a store holding one schedule whose last fire launched run wf-1.
func fired(t *testing.T) (*Store, Entry) {
	t.Helper()
	st := mustStore(t)
	e := Entry{Source: "bundled://x", Spec: launchrule.Spec{Freq: launchrule.FreqDaily, Hour: 2}, Enabled: true}
	if err := st.Put(t.Context(), &e); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := st.RecordStart(t.Context(), Run{ScheduleID: e.ID(), ScheduleLife: e.Life, WorkflowID: "wf-1"}); err != nil {
		t.Fatalf("RecordStart: %v", err)
	}
	return st, e
}

// An ending fills the row of its own run while it reads started, and nothing else: a failure already on
// the row (an unattended refusal, a bound stop) is the more useful reason, and a newer fire owns its row.
func TestStore_RecordEndingFillsOnlyItsOwnStartedRun(t *testing.T) {
	done := Outcome{Status: StatusCompleted}
	t.Run("its own started run", func(t *testing.T) {
		st, e := fired(t)
		if err := st.RecordEnding(t.Context(), Run{ScheduleID: e.ID(), ScheduleLife: e.Life, WorkflowID: "wf-1"}, done); err != nil {
			t.Fatalf("RecordEnding: %v", err)
		}
		if got := st.List()[0]; got.LastStatus != StatusCompleted {
			t.Errorf("row = (%q, %q), want completed", got.LastStatus, got.LastReason)
		}
	})
	t.Run("a failure already recorded", func(t *testing.T) {
		st, e := fired(t)
		run := Run{ScheduleID: e.ID(), ScheduleLife: e.Life, WorkflowID: "wf-1"}
		denial := Outcome{Status: StatusFailed, Reason: "needed approval for Write with nobody watching"}
		if err := st.RecordOutcome(t.Context(), run, denial); err != nil {
			t.Fatalf("RecordOutcome: %v", err)
		}
		if err := st.RecordEnding(t.Context(), run, done); err != nil {
			t.Fatalf("RecordEnding: %v", err)
		}
		if got := st.List()[0]; got.LastStatus != denial.Status || got.LastReason != denial.Reason {
			t.Errorf("row = (%q, %q), want the denial kept", got.LastStatus, got.LastReason)
		}
	})
	t.Run("another run", func(t *testing.T) {
		st, e := fired(t)
		if err := st.RecordEnding(t.Context(), Run{ScheduleID: e.ID(), ScheduleLife: e.Life, WorkflowID: "wf-0"}, done); err != nil {
			t.Fatalf("RecordEnding: %v", err)
		}
		if got := st.List()[0]; got.LastStatus != statusStarted {
			t.Errorf("row = (%q, %q), want wf-1's started kept", got.LastStatus, got.LastReason)
		}
	})
}

// A late outcome of a superseded run (a lease absent for hours, a sweep at boot) never lands on the row a
// newer fire owns.
func TestStore_RecordOutcomeIgnoresASupersededRun(t *testing.T) {
	st, e := fired(t)
	late := Outcome{Status: StatusUnknown, Reason: "no terminal signal"}
	if err := st.RecordOutcome(t.Context(), Run{ScheduleID: e.ID(), ScheduleLife: e.Life, WorkflowID: "wf-0"}, late); err != nil {
		t.Fatalf("RecordOutcome: %v", err)
	}
	if got := st.List()[0]; got.LastStatus != statusStarted {
		t.Errorf("row = (%q, %q), want wf-1's started kept", got.LastStatus, got.LastReason)
	}
}

// A run can end before its launch returns, so its ending reaches the row before the fire is recorded; the
// fire then advances the anchor and keeps that ending.
func TestStore_RecordFireKeepsAnEndingItsRunRecordedFirst(t *testing.T) {
	st, e := fired(t)
	run := Run{ScheduleID: e.ID(), ScheduleLife: e.Life, WorkflowID: "wf-1"}
	fast := Outcome{Status: StatusFailed, Reason: "step build failed"}
	if err := st.RecordEnding(t.Context(), run, fast); err != nil {
		t.Fatalf("RecordEnding: %v", err)
	}
	due := at(2026, time.August, 4, 2, 0)
	if err := st.recordFire(t.Context(), e.Row(), due, "wf-1", Outcome{Status: statusStarted}); err != nil {
		t.Fatalf("recordFire: %v", err)
	}
	got := st.List()[0]
	if got.LastStatus != fast.Status || got.LastReason != fast.Reason {
		t.Errorf("row = (%q, %q), want the run's own ending %+v", got.LastStatus, got.LastReason, fast)
	}
	if !got.Anchor.Equal(due) || !got.LastRunAt.Equal(due) {
		t.Errorf("anchor %v, last run %v, want both at the slot %v", got.Anchor, got.LastRunAt, due)
	}
}

// The run a row tracks survives a reopen and an edit, or every ending after a restart or a Save is dropped.
func TestStore_KeepsTheRowsRunAcrossAReopenAndAnEdit(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	e := Entry{Source: "bundled://x", Spec: launchrule.Spec{Freq: launchrule.FreqDaily, Hour: 2}, Enabled: true}
	if err := st.Put(t.Context(), &e); err != nil {
		t.Fatalf("Put: %v", err)
	}
	run := Run{ScheduleID: e.ID(), ScheduleLife: e.Life, WorkflowID: "wf-1"}
	if err := st.RecordStart(t.Context(), run); err != nil {
		t.Fatalf("RecordStart: %v", err)
	}
	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore (reopen): %v", err)
	}
	edit := Entry{Source: "bundled://x", Spec: launchrule.Spec{Freq: launchrule.FreqDaily, Hour: 4}, Enabled: true}
	if err := reopened.Put(t.Context(), &edit); err != nil {
		t.Fatalf("Put (edit): %v", err)
	}
	if err := reopened.RecordEnding(t.Context(), run, Outcome{Status: StatusCompleted}); err != nil {
		t.Fatalf("RecordEnding: %v", err)
	}
	if got := reopened.List()[0]; got.LastStatus != StatusCompleted {
		t.Errorf("row = (%q, %q), want completed", got.LastStatus, got.LastReason)
	}
}
