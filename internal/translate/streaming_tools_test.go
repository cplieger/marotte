package translate

import (
	"bytes"
	"encoding/json"
	"maps"
	"reflect"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/slogx/capture"
)

// lineRec is a recording LineRecorder capturing RecordFromDiffs
// invocations so the diff gates in HandleToolCall / HandleToolCallUpdate
// are observable.
type lineRec struct {
	lastDiffs []marotte.ToolDiff
	calls     int
	// lastRecency is the eviction key the tracker was handed, a wall-clock stamp
	// that orders a chat's touched files oldest-first.
	lastRecency int
}

func (r *lineRec) RecordFromDiffs(_ marotte.ChatID, diffs []marotte.ToolDiff, recency int, _ string) {
	r.calls++
	r.lastDiffs = diffs
	r.lastRecency = recency
}

// lineDeps wraps baseDeps and records the line-tracking calls.
type lineDeps struct {
	*baseDeps
	rec *lineRec
	// primed is the tool call primeToolCall created, kept because it clears the
	// event stream afterwards: the delta oracle needs the value the deltas fold
	// ONTO, and the tool_call entry frame that carried it is gone by then.
	primed marotte.ToolCall
}

func (d *lineDeps) RecordFromDiffs(chatID marotte.ChatID, diffs []marotte.ToolDiff, turn int, kind string) {
	d.rec.RecordFromDiffs(chatID, diffs, turn, kind)
}

// workDirDeps wraps baseDeps and overrides WorkDir for relPath tests.
type workDirDeps struct {
	*baseDeps
	workDir string
}

func (d *workDirDeps) WorkDir() string { return d.workDir }

// hookStatusDeps wraps baseDeps and overrides IsHookStatusEnabled so the
// hooks.showStatus gate is exercisable in both states (baseDeps hard-codes false).
type hookStatusDeps struct {
	*baseDeps
	enabled bool
}

func (d *hookStatusDeps) IsHookStatusEnabled() bool { return d.enabled }

var (
	_ LineRecorder = (*lineRec)(nil)
	_ hostDouble   = (*lineDeps)(nil)
	_ hostDouble   = (*workDirDeps)(nil)
	_ hostDouble   = (*hookStatusDeps)(nil)
)

// newLineCaptureDeps builds an event-capturing baseDeps with a recording
// LineTracker spliced in.
func newLineCaptureDeps() (*lineDeps, *lineRec, *[]marotte.ServerEvent) {
	base, events := newEventCaptureDeps()
	rec := &lineRec{}
	return &lineDeps{baseDeps: base, rec: rec}, rec, events
}

// primeToolCall registers one in-flight tool call "tc-1" on an event-capturing
// translator, then clears the captured events so the next update is seen in isolation.
func primeToolCall(t *testing.T) (*Translator, *lineRec, *lineDeps, *[]marotte.ServerEvent, marotte.ChatID) {
	t.Helper()
	deps, rec, events := newLineCaptureDeps()
	tr := New(rolesOf(deps))
	chatID := marotte.ChatID("c1")
	tr.HandleToolCall(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"title":      "readFile",
		"kind":       "read",
		"status":     "pending",
	}), FrameAttribution{})
	stashCreatedThenClear(t, deps, events)
	rec.calls = 0
	rec.lastDiffs = nil
	rec.lastRecency = 0
	return tr, rec, deps, events, chatID
}

// stashCreatedThenClear records the tool call the create frames built and then
// empties the stream, so a following update is observed in isolation while the
// delta oracle still knows the value those deltas fold onto.
func stashCreatedThenClear(t *testing.T, deps *lineDeps, events *[]marotte.ServerEvent) {
	t.Helper()
	deps.primed, _ = foldToolCallUpdates(t, marotte.ToolCall{}, events)
	*events = nil
}

// lastToolCallUpdate folds every tool_progress DELTA onto the call its tool_call
// entry carried, then the tool_result entry's settled value once the call settled,
// and returns the reconstructed whole, because no single frame carries it. While
// the call is open the fold is the ORACLE for the delta shape — toolProgress's
// inverse, cross-checked against the turn's own in-flight value so it is not the
// emitter's rules agreeing with themselves. A settled call has left the turn, and
// its tool_result entry is the whole.
func lastToolCallUpdate(t *testing.T, deps *lineDeps, events *[]marotte.ServerEvent) (marotte.ToolCall, bool) {
	t.Helper()
	// Seeded from the create primeToolCall consumed. A tool_call entry still in the
	// stream overrides it, so a test that primes its own call needs no seed.
	folded, ok := foldToolCallUpdates(t, deps.primed, events)
	if !ok {
		return marotte.ToolCall{}, false
	}
	turn := deps.turns.chats["c1"]
	if turn == nil {
		t.Fatalf("chat c1 has no turn, so the tool call %q cannot be checked", folded.ID)
	}
	open, live := turn.OpenCallFor(folded.ID)
	if folded.Status.Terminal() {
		if live {
			t.Fatalf("tool call %q settled on the wire but the turn still holds it open", folded.ID)
		}
		return folded, true
	}
	if !live {
		t.Fatalf("the turn holds no open tool call %q, so the delta stream cannot be checked", folded.ID)
	}
	if !reflect.DeepEqual(folded, open.Call) {
		t.Fatalf("the delta stream reconstructs\n  %+v\nbut the turn holds\n  %+v\n"+
			"— a field the fold changed is missing from the wire", folded, open.Call)
	}
	return folded, true
}

// foldToolCallUpdates replays the stream: the tool_call entry's whole ToolCall, every
// later tool_progress delta for it in order, and the tool_result entry that settled it.
func foldToolCallUpdates(t *testing.T, seed marotte.ToolCall, events *[]marotte.ServerEvent) (marotte.ToolCall, bool) {
	t.Helper()
	out := seed
	sawUpdate := false
	for _, e := range *events {
		switch p := e.Payload.(type) {
		case marotte.EntryAppendedPayload:
			switch p.Entry.Kind {
			case marotte.EntryKindToolCall:
				call := decodePayload[marotte.EntryToolCall](t, &p.Entry)
				out = marotte.ToolCallOfEntry(&call)
			case marotte.EntryKindToolResult:
				sawUpdate = true
				applyToolResult(&out, decodePayload[marotte.EntryToolResult](t, &p.Entry))
			}
		case marotte.ToolProgressPayload:
			sawUpdate = true
			applyToolCallDelta(&out, p)
		}
	}
	return out, sawUpdate
}

// decodePayload is the entry's payload as T.
func decodePayload[T any](t *testing.T, e *marotte.Entry) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(e.Payload, &v); err != nil {
		t.Fatalf("decode %s %q: %v", e.Kind, e.ID, err)
	}
	return v
}

// applyToolResult is the client's settle, in Go: the result carries every field the
// progress frames folded as it stands at the settle, so each one is replaced.
func applyToolResult(tc *marotte.ToolCall, r marotte.EntryToolResult) {
	tc.Title = r.Title
	tc.Kind = r.Kind
	tc.Status = r.Status
	tc.Output = r.Output
	tc.TerminalID = r.TerminalID
	tc.WorkflowID = r.WorkflowID
	tc.OutputSpans = r.OutputSpans
	tc.Diffs = r.Diffs
	tc.Locations = r.Locations
	tc.DurationMs = r.DurationMs
	tc.Checkpoint = r.Checkpoint
	tc.Disclosed = r.Disclosed
	tc.Denial = r.Denial
	tc.Truncated = r.Truncated
	tc.OutputBytes = r.OutputBytes
	tc.HasFull = r.HasFull
	tc.Declined = r.Declined
}

// applyToolCallDelta is the client's fold, in Go: the inverse of toolProgress.
// An absent field means unchanged.
func applyToolCallDelta(tc *marotte.ToolCall, d marotte.ToolProgressPayload) {
	tc.ID = d.ToolCallID
	if d.Title != "" {
		tc.Title = d.Title
	}
	if d.Kind != "" {
		tc.Kind = d.Kind
	}
	if d.Status != "" {
		tc.Status = d.Status
	}
	switch {
	case d.OutputReplace:
		tc.Output = d.OutputDelta
	case d.OutputDelta != "":
		tc.Output += d.OutputDelta
	}
	if d.OutputSpans != nil {
		tc.OutputSpans = d.OutputSpans
	}
	if len(d.DiffsAppended) > 0 {
		tc.Diffs = append(tc.Diffs, d.DiffsAppended...)
	}
	if d.Locations != nil {
		tc.Locations = d.Locations
	}
	if d.DurationMs != 0 {
		tc.DurationMs = d.DurationMs
	}
	if d.TerminalID != "" {
		tc.TerminalID = d.TerminalID
	}
	if d.AgentSubtaskID != "" {
		tc.AgentSubtaskID = d.AgentSubtaskID
	}
	if d.WorkflowID != "" {
		tc.WorkflowID = d.WorkflowID
	}
	if d.Checkpoint != nil {
		tc.Checkpoint = d.Checkpoint
	}
	if d.Disclosed != nil {
		tc.Disclosed = d.Disclosed
	}
	if d.Denial != nil {
		tc.Denial = d.Denial
	}
	if d.Declined {
		tc.Declined = true
	}
}

func hasWorkingLabel(events *[]marotte.ServerEvent) bool {
	for _, e := range *events {
		if e.Type == marotte.EventWorkingLabel {
			return true
		}
	}
	return false
}

// closedChangedFiles closes the chat's turn and returns the changed-files aggregate
// its turn_close carries, the only read of what the tool calls tracked.
func closedChangedFiles(t *testing.T, deps *baseDeps, chatID marotte.ChatID) map[string]*marotte.FileChange {
	t.Helper()
	turn := deps.turns.chats[chatID]
	if turn == nil {
		t.Fatalf("chat %q has no turn to close", chatID)
	}
	if _, err := turn.Close(t.Context(), marotte.TurnConclusion{Outcome: marotte.TurnOutcomeCompleted}); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return turnCloseOf(t, deps.chatEntries(chatID)).ChangedFiles
}

// TestHandleToolCall_HookAskRenderedRegardlessOfStatusSetting: a pre-tool-use hook's
// ask-permission gate arrives as a kind:"other" call tagged _meta.kiro.hookAsk, because
// v3's zToolKind has no "hook". Its card is NOT gated on hooks.showStatus: suppressing
// it also dropped the follow-up carrying the user's answer, so a hook needing approval
// always reaches the transcript.
func TestHandleToolCall_HookAskRenderedRegardlessOfStatusSetting(t *testing.T) {
	hookAsk := map[string]any{
		"toolCallId": "hook-ask-1",
		"title":      "Run hook",
		"kind":       "other",
		"status":     "pending",
		"_meta": map[string]any{"kiro": map[string]any{
			"hookAsk": map[string]any{"kind": "pre-tool-use", "toolName": "fs_write", "reason": "guard"},
		}},
	}

	t.Run("ShownWhenStatusDisabled", func(t *testing.T) {
		base, events := newEventCaptureDeps()
		deps := &hookStatusDeps{baseDeps: base, enabled: false}
		tr := New(rolesOf(deps))
		chatID := marotte.ChatID("c1")
		tr.HandleToolCall(t.Context(), chatID, mustJSON(t, hookAsk), FrameAttribution{})
		if !hasEntryAppended(events, marotte.EntryKindToolCall) {
			t.Error("hook-ask tool call appended no tool_call entry with hooks.showStatus off; want shown (the ask is ungated)")
		}
		if n := len(toolCallsOf(t, base.chatEntries(chatID))); n != 1 {
			t.Errorf("sealed tool_call entries = %d, want 1 (the ask must be sealed so its answer update lands)", n)
		}
	})

	t.Run("ShownWhenStatusEnabled", func(t *testing.T) {
		base, events := newEventCaptureDeps()
		deps := &hookStatusDeps{baseDeps: base, enabled: true}
		tr := New(rolesOf(deps))
		tr.HandleToolCall(t.Context(), marotte.ChatID("c1"), mustJSON(t, hookAsk), FrameAttribution{})
		if !hasEntryAppended(events, marotte.EntryKindToolCall) {
			t.Error("hook-ask tool call suppressed while hooks.showStatus on; want shown")
		}
	})

	t.Run("NonHookAskShownWhenStatusDisabled", func(t *testing.T) {
		base, events := newEventCaptureDeps()
		deps := &hookStatusDeps{baseDeps: base, enabled: false}
		tr := New(rolesOf(deps))
		tr.HandleToolCall(t.Context(), marotte.ChatID("c1"), mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"title":      "readFile",
			"kind":       "read",
			"status":     "pending",
		}), FrameAttribution{})
		if !hasEntryAppended(events, marotte.EntryKindToolCall) {
			t.Error("normal tool call suppressed with hooks.showStatus off; want shown (only hook-ask cards are gated)")
		}
	})
}

// TestHandleToolCall_DiffGate pins that HandleToolCall records line
// changes through the LineTracker only when the call carries at least
// one diff; a diff-free call never touches the tracker.
func TestHandleToolCall_DiffGate(t *testing.T) {
	t.Run("WithDiffRecordsLineChanges", func(t *testing.T) {
		deps, rec, _ := newLineCaptureDeps()
		tr := New(rolesOf(deps))
		tr.HandleToolCall(t.Context(), marotte.ChatID("c1"), mustJSON(t, map[string]any{
			"toolCallId": "tc-diff",
			"title":      "writeFile",
			"kind":       "edit",
			"status":     "pending",
			"content": []map[string]any{
				{"type": "diff", "path": "x.go", "oldText": "a", "newText": "b"},
			},
		}), FrameAttribution{})
		if rec.calls != 1 {
			t.Errorf("with diff: RecordFromDiffs calls = %d, want 1", rec.calls)
		}
		// The recency is the tracker's eviction key, so a zero files every range
		// under one bucket the tracker evicts first.
		first := rec.lastRecency
		if first <= 0 {
			t.Errorf("with diff: RecordFromDiffs recency = %d, want a positive wall-clock stamp", first)
		}
		// A second diffed call in the same chat never records behind the first,
		// which is what makes the key an ordering rather than a constant.
		tr.HandleToolCall(t.Context(), marotte.ChatID("c1"), mustJSON(t, map[string]any{
			"toolCallId": "tc-diff-2",
			"title":      "writeFile",
			"kind":       "edit",
			"status":     "pending",
			"content": []map[string]any{
				{"type": "diff", "path": "y.go", "oldText": "a", "newText": "b"},
			},
		}), FrameAttribution{})
		if rec.lastRecency < first {
			t.Errorf("second diffed call: RecordFromDiffs recency = %d, want at least the first call's %d", rec.lastRecency, first)
		}
	})
	t.Run("WithoutDiffSkipsLineTracker", func(t *testing.T) {
		deps, rec, _ := newLineCaptureDeps()
		tr := New(rolesOf(deps))
		tr.HandleToolCall(t.Context(), marotte.ChatID("c1"), mustJSON(t, map[string]any{
			"toolCallId": "tc-nodiff",
			"title":      "readFile",
			"kind":       "read",
			"status":     "pending",
		}), FrameAttribution{})
		if rec.calls != 0 {
			t.Errorf("without diff: RecordFromDiffs calls = %d, want 0", rec.calls)
		}
	})
}

// TestToolCallUpdate_StatusApplied pins that a non-empty status in an
// update overwrites the in-flight tool call's status.
func TestToolCallUpdate_StatusApplied(t *testing.T) {
	tr, _, deps, events, chatID := primeToolCall(t)
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "completed",
	}), FrameAttribution{})
	tc, ok := lastToolCallUpdate(t, deps, events)
	if !ok {
		t.Fatal("no tool_progress or tool_result frame emitted")
	}
	if tc.Status != marotte.ToolCompleted {
		t.Errorf("ToolCall.Status = %q, want %q (non-empty status must be applied)", tc.Status, marotte.ToolCompleted)
	}
}

// TestToolCallUpdate_TerminalStatusEmitsWorkingLabel pins that reaching
// a terminal status (completed or failed) broadcasts a working_label
// (Thinking) event.
func TestToolCallUpdate_TerminalStatusEmitsWorkingLabel(t *testing.T) {
	tests := []struct {
		name   string
		status string
	}{
		{name: "Completed", status: "completed"},
		{name: "Failed", status: "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, _, _, events, chatID := primeToolCall(t)
			tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
				"toolCallId": "tc-1",
				"status":     tt.status,
			}), FrameAttribution{})
			if !hasWorkingLabel(events) {
				t.Errorf("status=%q: no working_label event emitted, want one (terminal status must emit it)", tt.status)
			}
		})
	}
}

// TestToolCallUpdate_OutputAppendedWhenContentPresent pins that content
// text in an update is sanitized, newline-terminated, and appended to
// the tool call's Output.
func TestToolCallUpdate_OutputAppendedWhenContentPresent(t *testing.T) {
	tr, _, deps, events, chatID := primeToolCall(t)
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "in_progress",
		"content": []map[string]any{
			{"type": "content", "content": map[string]any{"text": "hello"}},
		},
	}), FrameAttribution{})
	tc, ok := lastToolCallUpdate(t, deps, events)
	if !ok {
		t.Fatal("no tool_progress or tool_result frame emitted")
	}
	if tc.Output != "hello\n" {
		t.Errorf("ToolCall.Output = %q, want %q (output must be appended when content present)", tc.Output, "hello\n")
	}
}

// TestToolCallUpdate_LocationsGate pins that Locations are replaced only
// when the update carries a non-empty list; an empty list leaves the
// existing Locations (nil here) untouched.
func TestToolCallUpdate_LocationsGate(t *testing.T) {
	t.Run("LocationsSetWhenPresent", func(t *testing.T) {
		tr, _, deps, events, chatID := primeToolCall(t)
		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "in_progress",
			"locations":  []map[string]any{{"path": "f.go", "line": 5}},
		}), FrameAttribution{})
		tc, ok := lastToolCallUpdate(t, deps, events)
		if !ok {
			t.Fatal("no tool_progress or tool_result frame emitted")
		}
		if len(tc.Locations) != 1 || tc.Locations[0].Path != "f.go" {
			t.Errorf("ToolCall.Locations = %+v, want one location path=f.go", tc.Locations)
		}
	})
	t.Run("EmptyLocationsNotAssigned", func(t *testing.T) {
		tr, _, deps, events, chatID := primeToolCall(t)
		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "in_progress",
			"locations":  []map[string]any{}, // decodes to a non-nil empty slice
		}), FrameAttribution{})
		tc, ok := lastToolCallUpdate(t, deps, events)
		if !ok {
			t.Fatal("no tool_progress or tool_result frame emitted")
		}
		if tc.Locations != nil {
			t.Errorf("ToolCall.Locations = %+v (len %d), want nil (empty locations must not be assigned)", tc.Locations, len(tc.Locations))
		}
	})
}

// TestToolCallUpdate_NoDiffSkipsLineTracker pins that an update carrying
// no diffs does not invoke the LineTracker.
func TestToolCallUpdate_NoDiffSkipsLineTracker(t *testing.T) {
	tr, rec, deps, events, chatID := primeToolCall(t)
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "in_progress",
	}), FrameAttribution{})
	if _, ok := lastToolCallUpdate(t, deps, events); !ok {
		t.Fatal("no tool_progress or tool_result frame emitted")
	}
	if rec.calls != 0 {
		t.Errorf("update without diffs: RecordFromDiffs calls = %d, want 0", rec.calls)
	}
}

// TestToolCallUpdate_DiffPresentRecordsAndAppends pins the ledger gate: a diff
// always lands on the card, and only a `completed` tool feeds the changed-file
// aggregate and the line tracker. Dropping the status check makes the in-progress
// case record, which is what counted a streaming write's partial diffs.
func TestToolCallUpdate_DiffPresentRecordsAndAppends(t *testing.T) {
	t.Run("InProgressAppendsWithoutRecording", func(t *testing.T) {
		tr, rec, deps, _, chatID := primeToolCall(t)
		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "in_progress",
			"content": []map[string]any{
				{"type": "diff", "path": "a.go", "oldText": "x", "newText": "y\n"},
			},
		}), FrameAttribution{})
		open, ok := deps.turns.chats[chatID].OpenCallFor("tc-1")
		if !ok {
			t.Fatal("in_progress: the turn no longer holds tc-1 open; a non-terminal update must not settle the call")
		}
		if got := len(open.Call.Diffs); got != 1 {
			t.Fatalf("in_progress: open call Diffs len = %d, want 1 (diff must be appended when present)", got)
		}
		if got := open.Call.Diffs[0].Path; got != "a.go" {
			t.Errorf("in_progress: Diffs[0].Path = %q, want %q", got, "a.go")
		}
		if rec.calls != 0 {
			t.Errorf("in_progress: RecordFromDiffs calls = %d, want 0 (only a completed tool changed a file)", rec.calls)
		}
		if _, ok := closedChangedFiles(t, deps.baseDeps, chatID)["a.go"]; ok {
			t.Error("in_progress: ChangedFiles[a.go] present, want absent (nothing has reached disk yet)")
		}
	})
	t.Run("CompletedRecordsOnce", func(t *testing.T) {
		tr, rec, deps, _, chatID := primeToolCall(t)
		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "completed",
			"content": []map[string]any{
				{"type": "diff", "path": "a.go", "oldText": "x\n", "newText": "y\n"},
			},
		}), FrameAttribution{})
		results := toolResultsOf(t, deps.chatEntries(chatID))
		if len(results) != 1 {
			t.Fatalf("completed: tool_result entries = %d, want 1 (a terminal update settles the call)", len(results))
		}
		if got := len(results[0].Diffs); got != 1 {
			t.Fatalf("completed: tool_result Diffs len = %d, want 1", got)
		}
		if rec.calls != 1 {
			t.Errorf("completed: RecordFromDiffs calls = %d, want 1 (must record when the tool succeeded)", rec.calls)
		}
		if got := len(rec.lastDiffs); got != 1 {
			t.Errorf("completed: RecordFromDiffs received %d diffs, want 1", got)
		}
		fc, ok := closedChangedFiles(t, deps.baseDeps, chatID)["a.go"]
		if !ok {
			t.Fatal("completed: ChangedFiles[a.go] missing; the diff was not tracked")
		}
		if fc.LinesAdded != 1 {
			t.Errorf("completed: LinesAdded = %d, want 1 (counted once, not once per frame)", fc.LinesAdded)
		}
	})
	t.Run("FailedDoesNotEnterTheLedger", func(t *testing.T) {
		// The write tool's catch emits a diff with a real path and the text it meant
		// to write, so a failed write claimed a file changed when nothing did.
		tr, rec, deps, _, chatID := primeToolCall(t)
		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "failed",
			"content": []map[string]any{
				{"type": "diff", "path": "locked.go", "oldText": "", "newText": "package x\n"},
			},
		}), FrameAttribution{})
		results := toolResultsOf(t, deps.chatEntries(chatID))
		if len(results) != 1 {
			t.Fatalf("failed: tool_result entries = %d, want 1 (a terminal update settles the call)", len(results))
		}
		if got := len(results[0].Diffs); got != 1 {
			t.Fatalf("failed: tool_result Diffs len = %d, want 1 (the card still shows what it tried)", got)
		}
		if rec.calls != 0 {
			t.Errorf("failed: RecordFromDiffs calls = %d, want 0", rec.calls)
		}
		if _, ok := closedChangedFiles(t, deps.baseDeps, chatID)["locked.go"]; ok {
			t.Error("failed: ChangedFiles[locked.go] present, want absent (the write failed)")
		}
	})
}

// TestRelPath pins relPath's workspace-root stripping: paths inside the
// workdir become workdir-relative slash paths, while an empty workdir or
// a path that escapes the root returns the absolute path unchanged.
func TestRelPath(t *testing.T) {
	tests := []struct {
		name    string
		workDir string
		abs     string
		want    string
	}{
		{name: "StripsRootPrefix", workDir: "/work", abs: "/work/sub/file.go", want: "sub/file.go"},
		{name: "OutsideWorkDirReturnsAbs", workDir: "/work", abs: "/elsewhere/x.go", want: "/elsewhere/x.go"},
		{name: "EmptyWorkDirReturnsAbs", workDir: "", abs: "/a/b.go", want: "/a/b.go"},
		// A first component that merely BEGINS with two dots is a directory
		// name, not a traversal: the escape test is separator-precise
		// (pathinside.RelEscapes), so this stays relative instead of leaking
		// the absolute path to the client.
		{name: "DotDotPrefixedDirIsRelative", workDir: "/work", abs: "/work/..drafts/x.go", want: "..drafts/x.go"},
		{name: "ParentEscapeReturnsAbs", workDir: "/work", abs: "/x.go", want: "/x.go"},
		// KAS sends some tool-call paths as file:// URIs, and every consumer treats
		// the value as a path, so the URI must be gone by the time it leaves here.
		// filepath.Clean turns "file:///work/x.go" into the RELATIVE "file:/work/x.go",
		// which makes filepath.Rel error and passes the raw URI through to consumers.
		{name: "FileURIBecomesRelative", workDir: "/work", abs: "file:///work/sub/file.go", want: "sub/file.go"},
		{name: "FileURIIsPercentDecoded", workDir: "/work", abs: "file:///work/hello%20world.sh", want: "hello world.sh"},
		// Normalising FIRST is what keeps the outside-the-workspace branch from
		// returning the spelling this function exists to remove.
		{name: "FileURIOutsideWorkDirIsStillAPath", workDir: "/work", abs: "file:///elsewhere/x.go", want: "/elsewhere/x.go"},
		{name: "FileURIWithEmptyWorkDirIsStillAPath", workDir: "", abs: "file:///a/b.go", want: "/a/b.go"},
		{name: "LocalhostAuthorityIsAccepted", workDir: "/work", abs: "file://localhost/work/x.go", want: "x.go"},
		// A remote authority names a file this process cannot open, so it is
		// left alone rather than rewritten into a local path that would then be
		// resolved against the local filesystem.
		{name: "RemoteAuthorityIsLeftAlone", workDir: "/work", abs: "file://host/share/x.go", want: "file://host/share/x.go"},
		{name: "NonFileSchemeIsLeftAlone", workDir: "/work", abs: "https://example.com/x.go", want: "https://example.com/x.go"},
		// A filename may legitimately contain "://", which trips the cheap gate but
		// parses to NO scheme, so it comes back through as a path; the duplicate
		// slashes collapse because filepath.Clean does that to every path here.
		{name: "PathContainingSchemeSeparator", workDir: "/work", abs: "/work/weird:///name.go", want: "weird:/name.go"},
		// An unparseable reference is returned as-is rather than mangled.
		{name: "MalformedURIIsLeftAlone", workDir: "/work", abs: "file://%zz/x.go", want: "file://%zz/x.go"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := &workDirDeps{baseDeps: newBaseDeps(), workDir: tt.workDir}
			tr := New(rolesOf(deps))
			if got := tr.relPath(tt.abs); got != tt.want {
				t.Errorf("relPath(%q) [workDir=%q] = %q, want %q", tt.abs, tt.workDir, got, tt.want)
			}
		})
	}
}

// TestHandleToolCall_IsNewFileFlag pins the isNew computation feeding TrackFileChanges:
// a call counts as a new-file creation only when it is BOTH an edit kind AND pending,
// observable on buf.ChangedFiles[path].IsNewFile.
func TestHandleToolCall_IsNewFileFlag(t *testing.T) {
	t.Run("PendingEditMarksNewFile", func(t *testing.T) {
		deps, _, _ := newLineCaptureDeps()
		tr := New(rolesOf(deps))
		chatID := marotte.ChatID("c1")
		tr.HandleToolCall(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-new",
			"title":      "writeFile",
			"kind":       "edit",
			"status":     "pending",
			"content": []map[string]any{
				{"type": "diff", "path": "new.go", "oldText": "", "newText": "package x\n"},
			},
		}), FrameAttribution{})
		fc, ok := closedChangedFiles(t, deps.baseDeps, chatID)["new.go"]
		if !ok {
			t.Fatal("ChangedFiles[new.go] missing; the diff was not tracked")
		}
		if !fc.IsNewFile {
			t.Errorf("IsNewFile = false, want true (a pending edit must be marked a new-file creation)")
		}
	})
	t.Run("CompletedEditIsNotNewFile", func(t *testing.T) {
		deps, _, _ := newLineCaptureDeps()
		tr := New(rolesOf(deps))
		chatID := marotte.ChatID("c2")
		tr.HandleToolCall(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-existing",
			"title":      "writeFile",
			"kind":       "edit",
			"status":     "completed",
			"content": []map[string]any{
				{"type": "diff", "path": "existing.go", "oldText": "a\n", "newText": "b\n"},
			},
		}), FrameAttribution{})
		fc, ok := closedChangedFiles(t, deps.baseDeps, chatID)["existing.go"]
		if !ok {
			t.Fatal("ChangedFiles[existing.go] missing; the diff was not tracked")
		}
		if fc.IsNewFile {
			t.Errorf("IsNewFile = true, want false (a completed edit is not a new-file creation)")
		}
	})
}

// --- _meta.kiro.checkpoint: KAS's snapshot mapping ---

// TestToolCallUpdate_CheckpointFromWire drives two shapes a real kiro-cli emits through
// the actual JSON decode, so the `_meta.kiro.checkpoint` NESTING is pinned and not just
// the merge logic: a misplaced struct tag compiles cleanly, yields nothing, and the
// symptom is "Rewind shows no diff" with nothing in any log. The create case is why the
// table exists — KAS sends NO `original` for a file it just created.
func TestToolCallUpdate_CheckpointFromWire(t *testing.T) {
	const (
		origURI = "kiro-snapshot-v2://sess_51d58124:5c1bae6d/?originalPath%3Dexisting.txt"
		modURI  = "kiro-snapshot-v2://sess_51d58124:952e8e1f/?originalPath%3Dexisting.txt"
		local   = "file:///tmp/ws/existing.txt"
	)
	tests := []struct {
		name       string
		checkpoint map[string]any
		want       marotte.ToolCheckpoint
	}{
		{
			name:       "overwriting an existing file carries all three",
			checkpoint: map[string]any{"original": origURI, "modified": modURI, "local": local},
			want:       marotte.ToolCheckpoint{Original: origURI, Modified: modURI, Local: local},
		},
		{
			name:       "creating a file has no pre-image",
			checkpoint: map[string]any{"modified": modURI, "local": local},
			want:       marotte.ToolCheckpoint{Modified: modURI, Local: local},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, _, deps, events, chatID := primeToolCall(t)
			tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
				"toolCallId": "tc-1",
				"status":     "completed",
				"_meta":      map[string]any{"kiro": map[string]any{"checkpoint": tt.checkpoint}},
			}), FrameAttribution{})
			tc, ok := lastToolCallUpdate(t, deps, events)
			if !ok {
				t.Fatal("no tool_progress or tool_result frame emitted")
			}
			if tc.Checkpoint == nil {
				t.Fatalf("ToolCall.Checkpoint = nil, want %+v (is the _meta.kiro.checkpoint tag path right?)", tt.want)
			}
			if *tc.Checkpoint != tt.want {
				t.Errorf("ToolCall.Checkpoint = %+v, want %+v", *tc.Checkpoint, tt.want)
			}
		})
	}
}

// TestToolCallUpdate_CheckpointMergeIsPerField pins that a later frame with a narrower
// key set cannot erase a value an earlier one supplied. The key set genuinely varies
// frame to frame for one tool call, so a wholesale struct replacement drops `original`
// and takes the pre-image — the only thing a diff needs — with it.
func TestToolCallUpdate_CheckpointMergeIsPerField(t *testing.T) {
	tr, _, deps, events, chatID := primeToolCall(t)
	send := func(cp map[string]any) {
		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"_meta":      map[string]any{"kiro": map[string]any{"checkpoint": cp}},
		}), FrameAttribution{})
	}
	send(map[string]any{"original": "orig-uri", "modified": "mod-uri", "local": "local-uri"})
	send(map[string]any{"modified": "mod-uri-2"})

	tc, ok := lastToolCallUpdate(t, deps, events)
	if !ok {
		t.Fatal("no tool_progress or tool_result frame emitted")
	}
	want := marotte.ToolCheckpoint{Original: "orig-uri", Modified: "mod-uri-2", Local: "local-uri"}
	if tc.Checkpoint == nil || *tc.Checkpoint != want {
		t.Errorf("ToolCall.Checkpoint = %+v, want %+v (a narrower frame must refine, not replace)", tc.Checkpoint, want)
	}
}

// TestToolCallUpdate_CheckpointAbsentStaysNil pins that a tool call which
// touched no file grows no checkpoint. ~95% of tool calls are in this case,
// so allocating an empty struct here would put a useless `"checkpoint":{}`
// on almost every tool call in every chat file on disk.
func TestToolCallUpdate_CheckpointAbsentStaysNil(t *testing.T) {
	tests := []struct {
		name string
		meta map[string]any
	}{
		{name: "no _meta at all", meta: nil},
		{name: "kiro meta without a checkpoint", meta: map[string]any{"kiro": map[string]any{"kind": "other"}}},
		{name: "an explicitly empty checkpoint", meta: map[string]any{"kiro": map[string]any{"checkpoint": map[string]any{}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, _, deps, events, chatID := primeToolCall(t)
			frame := map[string]any{"toolCallId": "tc-1", "status": "completed"}
			if tt.meta != nil {
				frame["_meta"] = tt.meta
			}
			tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, frame), FrameAttribution{})
			tc, ok := lastToolCallUpdate(t, deps, events)
			if !ok {
				t.Fatal("no tool_progress or tool_result frame emitted")
			}
			if tc.Checkpoint != nil {
				t.Errorf("ToolCall.Checkpoint = %+v, want nil (no file was written)", *tc.Checkpoint)
			}
		})
	}
}

// A mid-flight update may refine the card's title and kind, and KAS sends both
// nullish on most updates. So a value the update carries is applied and a value
// it omits keeps what the initial tool_call set — treating absence as an
// instruction blanks the label of a card the user is watching.
func TestToolCallUpdate_TitleAndKindAppliedOnlyWhenPresent(t *testing.T) {
	tests := []struct {
		name      string
		update    map[string]any
		wantTitle string
		wantKind  marotte.ToolKind
	}{
		{
			name:      "the_update_refines_both",
			update:    map[string]any{"title": "readFile(config.yaml)", "kind": "edit"},
			wantTitle: "readFile(config.yaml)",
			wantKind:  marotte.ToolKind("edit"),
		},
		{
			name:      "the_update_omits_both",
			update:    map[string]any{},
			wantTitle: "readFile",
			wantKind:  marotte.ToolKind("read"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr, _, deps, events, chatID := primeToolCall(t)
			update := map[string]any{"toolCallId": "tc-1", "status": "completed"}
			maps.Copy(update, tc.update)

			tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, update), FrameAttribution{})

			got, ok := lastToolCallUpdate(t, deps, events)
			if !ok {
				t.Fatal("no tool_progress or tool_result frame emitted")
			}
			if got.Title != tc.wantTitle {
				t.Errorf("ToolCall.Title after an update %v = %q, want %q", tc.update, got.Title, tc.wantTitle)
			}
			if got.Kind != tc.wantKind {
				t.Errorf("ToolCall.Kind after an update %v = %q, want %q", tc.update, got.Kind, tc.wantKind)
			}
		})
	}
}

// A call's lane is fixed at the create (the invocation rule): an id arriving on an
// UPDATE is never adopted, because re-laning a call mid-flight re-parents its card,
// and a step's work reaches the run's log by attribution rather than by an id on
// a frame. A disagreeing update is folded where the call is and logged, so the
// wire drift is visible without moving anything.
func TestToolCallUpdate_ALaneOnAnUpdateIsNeverAdopted(t *testing.T) {
	const msg = "tool_call_update lane disagrees with its call's lane; folding where the call is"

	t.Run("an_id_the_create_did_not_carry_is_logged_not_adopted", func(t *testing.T) {
		rec := capture.Default(t)
		tr, _, deps, events, chatID := primeToolCall(t)

		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "completed",
			"_meta":      map[string]any{"kiro": map[string]any{"agentSubtaskId": "uuid-plain"}},
		}), FrameAttribution{})

		got, ok := lastToolCallUpdate(t, deps, events)
		if !ok {
			t.Fatal("no tool_progress or tool_result frame emitted")
		}
		if got.AgentSubtaskID != "" {
			t.Errorf("ToolCall.AgentSubtaskID after an update carrying an id = %q, want the create's empty lane", got.AgentSubtaskID)
		}
		results := deps.chatEntries(chatID)
		if len(results) == 0 || results[len(results)-1].Kind != marotte.EntryKindToolResult || results[len(results)-1].Lane != "" {
			t.Errorf("tool_result entry = %+v, want it filed in the call's own lane (empty)", results)
		}
		if rec.CountExact(msg) != 1 || !rec.HasAttr(msg, "update_lane", "uuid-plain") {
			t.Errorf("got %d %q lines naming update_lane=uuid-plain, want 1", rec.CountExact(msg), msg)
		}
	})

	t.Run("a_held_id_survives_a_later_frame", func(t *testing.T) {
		deps, _, events := newLineCaptureDeps()
		tr := New(rolesOf(deps))
		chatID := marotte.ChatID("c1")
		tr.HandleToolCall(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"title":      "readFile",
			"kind":       "read",
			"status":     "pending",
			"_meta":      map[string]any{"kiro": map[string]any{"agentSubtaskId": "uuid-first"}},
		}), FrameAttribution{})
		stashCreatedThenClear(t, deps, events)

		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "completed",
			"_meta":      map[string]any{"kiro": map[string]any{"agentSubtaskId": "uuid-second"}},
		}), FrameAttribution{})

		got, ok := lastToolCallUpdate(t, deps, events)
		if !ok {
			t.Fatal("no tool_progress or tool_result frame emitted")
		}
		if got.AgentSubtaskID != "uuid-first" {
			t.Errorf("ToolCall.AgentSubtaskID after a second id arrived = %q, want %q (the lane is the create's)",
				got.AgentSubtaskID, "uuid-first")
		}
	})
}

// TestToolCallUpdate_WorkflowIDFromRawOutput pins the one field this client reads out of
// `rawOutput`, which KAS types as `unknown`. `run_workflow` reports the id of the run it
// created there, and it is the ONLY structural link from the invocation to its run, so
// without it the transcript cannot render a run's steps inside the call that launched
// them. Every case is a shape KAS really sends, and none may panic or contaminate it.
func TestToolCallUpdate_WorkflowIDFromRawOutput(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  any
		want string
	}{
		{"run_workflow's own shape", map[string]any{
			"message":    "Workflow 'wf_9' started successfully. Status: running.",
			"workflowId": "wf_9",
			"status":     "running",
		}, "wf_9"},
		{"an object carrying no id", map[string]any{"message": "done"}, ""},
		{"a bare string, which most tools send", "some output", ""},
		{"a number", 42, ""},
		{"an array", []any{"a", "b"}, ""},
		{"null", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			tr, _, deps, events, chatID := primeToolCall(t)
			tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
				"toolCallId": "tc-1",
				"status":     "completed",
				"rawOutput":  c.raw,
			}), FrameAttribution{})

			got, ok := lastToolCallUpdate(t, deps, events)
			if !ok {
				t.Fatal("no tool_progress or tool_result frame emitted")
			}
			if got.WorkflowID != c.want {
				t.Errorf("ToolCall.WorkflowID from rawOutput %v = %q, want %q", c.raw, got.WorkflowID, c.want)
			}
		})
	}
}

// TestToolCallUpdate_WorkflowIDIsAdoptedOnce mirrors the late-adoption rule the
// subtask id follows: KAS reports the run on the terminal update, and no later
// frame for the same call can name a different run.
func TestToolCallUpdate_WorkflowIDIsAdoptedOnce(t *testing.T) {
	t.Parallel()
	tr, _, deps, events, chatID := primeToolCall(t)

	for _, id := range []string{"wf_first", "wf_second"} {
		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "completed",
			"rawOutput":  map[string]any{"workflowId": id},
		}), FrameAttribution{})
	}

	got, ok := lastToolCallUpdate(t, deps, events)
	if !ok {
		t.Fatal("no tool_progress or tool_result frame emitted")
	}
	if got.WorkflowID != "wf_first" {
		t.Errorf("ToolCall.WorkflowID after a second id arrived = %q, want %q", got.WorkflowID, "wf_first")
	}
}

// TestToolCallUpdate_RefusedWorkflowUpdateReadsAsDeclined pins the fifth card outcome.
// `update_workflow` concludes Success while its own payload reports the refusal, so the
// status alone reads the refusal as a clean completion — the one field that carries the
// fact is `updated`, and this is the stated exception to reading outcome from a status.
func TestToolCallUpdate_RefusedWorkflowUpdateReadsAsDeclined(t *testing.T) {
	t.Parallel()

	const reason = "Cannot update a completed workflow."
	cases := []struct {
		name         string
		raw          any
		wantDeclined bool
	}{
		{"a refused update", map[string]any{"updated": false, "message": reason}, true},
		// The applied case is what makes the field a verdict rather than a marker: it
		// travels on every update_workflow reply, so reading its PRESENCE would mark
		// every successful plan edit as a refusal.
		{"an applied update", map[string]any{"updated": true, "message": "Plan updated."}, false},
		// A queued update is TAKEN — its signal lands at the current turn's end — so it
		// is not a refusal either.
		{"a queued update", map[string]any{"updated": true, "queued": true}, false},
		// Absent means the tool made no claim, which reads as taken. This is the arm
		// that keeps every OTHER tool untouched: none of them carries the key.
		{"an absent verdict", map[string]any{"workflowId": "wf_9", "status": "running"}, false},
		{"a bare string, which most tools send", "some output", false},
		{"null", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			tr, _, deps, events, chatID := primeToolCall(t)
			tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
				"toolCallId": "tc-1",
				"status":     "completed",
				"rawOutput":  c.raw,
			}), FrameAttribution{})

			got, ok := lastToolCallUpdate(t, deps, events)
			if !ok {
				t.Fatal("no tool_progress or tool_result frame emitted")
			}
			if got.Declined != c.wantDeclined {
				t.Errorf("ToolCall.Declined from rawOutput %v = %v, want %v", c.raw, got.Declined, c.wantDeclined)
			}
			// The mark is a SECOND axis, never a status: `failed` would offer the reader
			// an "Explain this error" button over a tool that ran correctly.
			if got.Status != marotte.ToolCompleted {
				t.Errorf("ToolCall.Status = %q, want %q (a refusal is still a completion)", got.Status, marotte.ToolCompleted)
			}
		})
	}
}

// TestToolCallUpdate_DeclinedOnlyGradesASettledCall pins the status gate. A refusal is
// something the tool REPORTED, so an in-flight frame carrying the field has not reported
// anything yet, and marking one would put the refusal outcome on a running card.
func TestToolCallUpdate_DeclinedOnlyGradesASettledCall(t *testing.T) {
	t.Parallel()
	tr, _, deps, events, chatID := primeToolCall(t)
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "in_progress",
		"rawOutput":  map[string]any{"updated": false, "message": "not yet"},
	}), FrameAttribution{})

	got, ok := lastToolCallUpdate(t, deps, events)
	if !ok {
		t.Fatal("no tool_progress or tool_result frame emitted")
	}
	if got.Declined {
		t.Error("ToolCall.Declined on an in_progress frame = true, want false")
	}
}

// TestToolCallUpdate_DeclinedIsNeverCleared pins the one-way rule. A later frame for the
// same call carries no verdict, and reading its absence as "taken" would erase the
// refusal the terminal frame reported.
func TestToolCallUpdate_DeclinedIsNeverCleared(t *testing.T) {
	t.Parallel()
	tr, _, deps, events, chatID := primeToolCall(t)
	for _, raw := range []any{
		map[string]any{"updated": false, "message": "refused"},
		map[string]any{"workflowId": "wf_9"},
	} {
		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "completed",
			"rawOutput":  raw,
		}), FrameAttribution{})
	}

	got, ok := lastToolCallUpdate(t, deps, events)
	if !ok {
		t.Fatal("no tool_progress or tool_result frame emitted")
	}
	if !got.Declined {
		t.Error("ToolCall.Declined after a verdict-free frame followed the refusal = false, want true")
	}
}

// TestToolCallUpdate_AnUpdateCallDoesNotAdoptTheWorkflowID is the D1 server half.
// `update_workflow` echoes the run's own `workflowId` while starting nothing, so
// adopting it makes the transcript read that call as the one that STARTED the run: its
// block is seated on the run's card and its refusal renders nowhere at all. The `updated`
// key is the only thing on the wire that tells the two calls apart.
func TestToolCallUpdate_AnUpdateCallDoesNotAdoptTheWorkflowID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{
			"a refused update echoing the run id",
			map[string]any{"workflowId": "wf_9", "updated": false, "message": "refused"},
			"",
		},
		{
			"an applied update echoing the run id",
			map[string]any{"workflowId": "wf_9", "updated": true, "message": "Plan updated."},
			"",
		},
		{
			// The launch's own rawOutput carries no `updated`, so the one call that
			// SHOULD own the card is untouched by the exclusion.
			"the launch, whose payload carries no verdict",
			map[string]any{"workflowId": "wf_9", "status": "running"},
			"wf_9",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			tr, _, deps, events, chatID := primeToolCall(t)
			tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
				"toolCallId": "tc-1",
				"status":     "completed",
				"rawOutput":  c.raw,
			}), FrameAttribution{})

			got, ok := lastToolCallUpdate(t, deps, events)
			if !ok {
				t.Fatal("no tool_progress or tool_result frame emitted")
			}
			if got.WorkflowID != c.want {
				t.Errorf("ToolCall.WorkflowID from rawOutput %v = %q, want %q", c.raw, got.WorkflowID, c.want)
			}
		})
	}
}

// The message KAS's write tool throws when a remote-$schema JSON write is refused
// outside Autopilot. Verbatim from the 2.20.1 bundle, because the reason reaching
// the card unaltered is the property under test.
const remoteJSONSchemaReason = "Cannot use this tool to write a Remote JSON Schema in Supervised mode. " +
	"Switch to Autopilot mode to allow this write."

// TestHandleToolCallUpdate_FailedTakesReasonFromRawOutput drives the frame the guard
// really produces: status failed, the reason as a bare JSON string in rawOutput, and a
// diff block with an empty path because the throw beat resolveFile. KAS's edit arm puts
// the reason in no content block, so rawOutput is the only channel it travels on.
func TestHandleToolCallUpdate_FailedTakesReasonFromRawOutput(t *testing.T) {
	tr, _, deps, events, chatID := primeToolCall(t)
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "failed",
		"rawOutput":  remoteJSONSchemaReason,
		"content": []map[string]any{
			{"type": "diff", "path": "", "newText": "{\"$schema\":\"https://example.test/s.json\"}\n"},
		},
	}), FrameAttribution{})

	got, ok := lastToolCallUpdate(t, deps, events)
	if !ok {
		t.Fatal("no tool_progress or tool_result frame emitted")
	}
	if got.Output != remoteJSONSchemaReason {
		t.Errorf("ToolCall.Output on a failed edit = %q, want the reason %q", got.Output, remoteJSONSchemaReason)
	}
	// The same frame pins that a path-less diff still contributes nothing: it is
	// the whole content set here, so if it rendered the card would claim a change.
	if len(got.Diffs) != 0 {
		t.Errorf("ToolCall.Diffs = %+v, want none (a diff with no path names no file)", got.Diffs)
	}
	if changed := closedChangedFiles(t, deps.baseDeps, chatID); len(changed) != 0 {
		t.Errorf("ChangedFiles = %v, want empty (the write threw before it reached a path)", changed)
	}
}

// TestHandleToolCallUpdate_FailedKeepsExistingOutput pins the `Output == ""` half
// of the gate. A failed execute tool has printed its own output, and that is what
// the reader needs; dropping the guard would append KAS's error text to it or
// overwrite it.
func TestHandleToolCallUpdate_FailedKeepsExistingOutput(t *testing.T) {
	tr, _, deps, events, chatID := primeToolCall(t)
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "failed",
		"rawOutput":  "Command exited with status 1",
		"content": []map[string]any{
			{"type": "content", "content": map[string]any{"type": "text", "text": "FAIL: 2 tests failed"}},
		},
	}), FrameAttribution{})

	got, ok := lastToolCallUpdate(t, deps, events)
	if !ok {
		t.Fatal("no tool_progress or tool_result frame emitted")
	}
	if got.Output != "FAIL: 2 tests failed\n" {
		t.Errorf("ToolCall.Output = %q, want the command's own output alone", got.Output)
	}
}

// Whether migrated tools still emit a bare string in rawOutput is unmeasured.
// When a content block accompanies one, the content copy remains canonical.
func TestHandleToolCallUpdate_CompletedStringRawOutputKeepsContent(t *testing.T) {
	tr, _, deps, events, chatID := primeToolCall(t)
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "completed",
		"rawOutput":  "wrote 3 lines",
		"content": []map[string]any{
			{"type": "content", "content": map[string]any{"type": "text", "text": "wrote 3 lines"}},
		},
	}), FrameAttribution{})

	got, ok := lastToolCallUpdate(t, deps, events)
	if !ok {
		t.Fatal("no tool_progress or tool_result frame emitted")
	}
	if got.Output != "wrote 3 lines\n" {
		t.Errorf("ToolCall.Output with string rawOutput and content = %q, want one content copy", got.Output)
	}
}

func TestHandleToolCallUpdate_CompletedTakesMessageFromStringifiedObjectOutput(t *testing.T) {
	const message = "Workflow 'wf_9' started successfully. Status: running."
	output := map[string]any{
		"message":    message,
		"workflowId": "wf_9",
		"status":     "running",
	}
	tr, _, deps, events, chatID := primeToolCall(t)
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "completed",
		"rawOutput":  output,
		"content": []map[string]any{
			{"type": "content", "content": map[string]any{"type": "text", "text": string(mustJSON(t, output))}},
		},
	}), FrameAttribution{})

	got, ok := lastToolCallUpdate(t, deps, events)
	if !ok {
		t.Fatal("no tool_progress or tool_result frame emitted")
	}
	if got.Output != message+"\n" {
		t.Errorf("ToolCall.Output from a stringified object = %q, want %q", got.Output, message+"\n")
	}
}

func TestHandleToolCallUpdate_CompletedKeepsDifferentContentOverObjectMessage(t *testing.T) {
	tr, _, deps, events, chatID := primeToolCall(t)
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "completed",
		"rawOutput": map[string]any{
			"message": "internal summary",
			"result":  42,
		},
		"content": []map[string]any{
			{"type": "content", "content": map[string]any{"type": "text", "text": "ordinary tool output"}},
		},
	}), FrameAttribution{})

	got, ok := lastToolCallUpdate(t, deps, events)
	if !ok {
		t.Fatal("no tool_progress or tool_result frame emitted")
	}
	if got.Output != "ordinary tool output\n" {
		t.Errorf("ToolCall.Output with distinct content and object rawOutput = %q, want the content block", got.Output)
	}
}

// TestRawOutputFailureText is the decode on its own: a bare string, then an
// object's error or message, and "" for everything else. The negative rows are
// what keep the read narrow — each is a shape KAS really sends on some tool, and
// none of them may become a card's output.
func TestRawOutputFailureText(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"a bare string, which is what the write tool sends", `"lock is held by another process"`, "lock is held by another process"},
		{"an object with error", `{"error":"ENOSPC: no space left on device"}`, "ENOSPC: no space left on device"},
		{"an object with message", `{"message":"policy refused the write"}`, "policy refused the write"},
		{"error wins over message", `{"error":"the cause","message":"the summary"}`, "the cause"},
		{"a whitespace-only string", `"   "`, ""},
		{"an empty string", `""`, ""},
		{"a number", `42`, ""},
		{"an array", `["a","b"]`, ""},
		{"an object with a non-string error", `{"error":{"code":13}}`, ""},
		{"run_workflow's success object", `{"workflowId":"wf_9","status":"running"}`, ""},
		{"malformed json", `{`, ""},
		{"absent", ``, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := rawOutputFailureText(json.RawMessage(c.raw)); got != c.want {
				t.Errorf("rawOutputFailureText(%s) = %q, want %q", c.raw, got, c.want)
			}
		})
	}
}

// A disclosure and a policy denial are both decided when the call is ATTEMPTED,
// so either can arrive on an update rather than the create. Each is adopted
// into an empty slot only: overwriting would let a later frame replace the
// refusal a user is reading with a narrower one.
func TestToolCallUpdate_DisclosureAndDenialAdoptedLate(t *testing.T) {
	t.Run("a_disclosure_on_the_update_is_adopted", func(t *testing.T) {
		tr, _, deps, events, chatID := primeToolCall(t)

		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "completed",
			"_meta": map[string]any{"kiro": map[string]any{
				"disclosedContext": map[string]any{
					"type": "skill", "displayName": "deploy-app", "uri": "file:///skills/deploy-app.md",
				},
			}},
		}), FrameAttribution{})

		got, ok := lastToolCallUpdate(t, deps, events)
		if !ok {
			t.Fatal("no tool_progress or tool_result frame emitted")
		}
		if got.Disclosed == nil {
			t.Fatalf("ToolCall.Disclosed after an update carrying disclosedContext = nil, want the skill")
		}
		if got.Disclosed.DisplayName != "deploy-app" || got.Disclosed.Type != "skill" {
			t.Errorf("ToolCall.Disclosed = %+v, want type %q name %q", *got.Disclosed, "skill", "deploy-app")
		}
	})

	t.Run("a_denial_on_the_update_is_adopted", func(t *testing.T) {
		tr, _, deps, events, chatID := primeToolCall(t)

		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "failed",
			"_meta": map[string]any{"kiro": map[string]any{
				"policyDenial": map[string]any{
					"capability": "fs.write", "resource": "/etc/passwd", "scope": "workspace", "source": "policy.json",
				},
			}},
		}), FrameAttribution{})

		got, ok := lastToolCallUpdate(t, deps, events)
		if !ok {
			t.Fatal("no tool_progress or tool_result frame emitted")
		}
		if got.Denial == nil {
			t.Fatalf("ToolCall.Denial after an update carrying policyDenial = nil, want the refusal")
		}
		if got.Denial.Capability != "fs.write" || got.Denial.Resource != "/etc/passwd" {
			t.Errorf("ToolCall.Denial = %+v, want capability %q resource %q", *got.Denial, "fs.write", "/etc/passwd")
		}
	})

	t.Run("values_held_from_the_create_survive_a_later_frame", func(t *testing.T) {
		deps, _, events := newLineCaptureDeps()
		tr := New(rolesOf(deps))
		chatID := marotte.ChatID("c1")
		tr.HandleToolCall(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"title":      "disclose_context",
			"kind":       "other",
			"status":     "pending",
			"_meta": map[string]any{"kiro": map[string]any{
				"disclosedContext": map[string]any{"type": "skill", "displayName": "first", "uri": "file:///a.md"},
				"policyDenial":     map[string]any{"capability": "fs.write", "resource": "/first"},
			}},
		}), FrameAttribution{})
		stashCreatedThenClear(t, deps, events)

		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "completed",
			"_meta": map[string]any{"kiro": map[string]any{
				"disclosedContext": map[string]any{"type": "steering", "displayName": "second", "uri": "file:///b.md"},
				"policyDenial":     map[string]any{"capability": "shell.exec", "resource": "/second"},
			}},
		}), FrameAttribution{})

		got, ok := lastToolCallUpdate(t, deps, events)
		if !ok {
			t.Fatal("no tool_progress or tool_result frame emitted")
		}
		if got.Disclosed == nil || got.Disclosed.DisplayName != "first" {
			t.Errorf("ToolCall.Disclosed after a second disclosure arrived = %+v, want the one held from the create (%q)",
				got.Disclosed, "first")
		}
		if got.Denial == nil || got.Denial.Resource != "/first" {
			t.Errorf("ToolCall.Denial after a second denial arrived = %+v, want the one held from the create (%q)",
				got.Denial, "/first")
		}
	})
}

// TestParseToolUpdateContent_UnmodelledType pins the BEHAVIOUR for a content block
// marotte does not decode — nothing rendered — and the two Debug lines that make the
// drop findable, since the symptom is otherwise a claim-only card with an empty details
// region and no signal anywhere. This is the surface kiro-cli's structuredContent lands
// on, deliberately not adopted while there is no renderer behind it.
// Serial (no t.Parallel): captureSlog swaps the process-wide slog default.
func TestParseToolUpdateContent_UnmodelledType(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	tr := New(rolesOf(deps))

	t.Run("an unknown type renders nothing and logs both lines", func(t *testing.T) {
		var logs bytes.Buffer
		t.Cleanup(captureSlog(&logs))

		got := tr.parseToolUpdateContent("tc-1", []ACPToolCallContentBlock{
			{Type: "structuredContent"},
		})

		// Behaviour first: the block is dropped, which is what it did before the
		// logging and what this test would catch a silent adoption of.
		if got.output != "" || got.diffs != nil || got.terminalID != "" {
			t.Errorf("an unmodelled block produced output: %#v", got)
		}
		line := logs.String()
		for _, want := range []string{"unmodelled type", "structuredContent", "tc-1"} {
			if !strings.Contains(line, want) {
				t.Errorf("log %q missing %q", line, want)
			}
		}
		// The second line is the observable SYMPTOM rather than the cause, and it
		// is what a reader searches for after seeing an empty card.
		if !strings.Contains(line, "produced nothing to render") {
			t.Errorf("log %q missing the empty-render line", line)
		}
	})

	t.Run("a claim-only tool logs nothing", func(t *testing.T) {
		// No content blocks at all is the ~95% case (read, delete, think). Logging
		// here would put a line on almost every tool call and bury the real one.
		var logs bytes.Buffer
		t.Cleanup(captureSlog(&logs))
		tr.parseToolUpdateContent("tc-2", nil)
		if logs.Len() != 0 {
			t.Errorf("a claim-only tool logged %q", logs.String())
		}
	})

	t.Run("a known type with an unmatched payload is not called unmodelled", func(t *testing.T) {
		// An empty-text content block and a diff with no path are NORMAL frames,
		// not gaps in what marotte decodes. A bare `default` arm would report both
		// as unmodelled types, which is the noise that would make the real line
		// unfindable — so the guard is on the TYPE, not on whether an arm matched.
		var logs bytes.Buffer
		t.Cleanup(captureSlog(&logs))
		tr.parseToolUpdateContent("tc-3", []ACPToolCallContentBlock{
			{Type: ContentTypeContent},
			{Type: ContentTypeDiff},
			{Type: ContentTypeTerminal},
		})
		line := logs.String()
		if strings.Contains(line, "unmodelled type") {
			t.Errorf("log %q calls a known type unmodelled", line)
		}
		// The empty-render line DOES fire, and should: content arrived and none of
		// it reached the card, which is the state worth knowing about however the
		// blocks were shaped.
		if !strings.Contains(line, "produced nothing to render") {
			t.Errorf("log %q missing the empty-render line", line)
		}
	})

	t.Run("a block that renders logs nothing", func(t *testing.T) {
		var logs bytes.Buffer
		t.Cleanup(captureSlog(&logs))
		// Field assignment rather than a composite literal: ACPToolCallContentBlock
		// declares Content as an ANONYMOUS struct, so a literal would have to
		// restate its type inline.
		blk := ACPToolCallContentBlock{Type: ContentTypeContent}
		blk.Content.Text = "hello"
		got := tr.parseToolUpdateContent("tc-4", []ACPToolCallContentBlock{blk})
		if got.output == "" {
			t.Fatal("a content block with text produced no output")
		}
		if logs.Len() != 0 {
			t.Errorf("a rendering block logged %q", logs.String())
		}
	})
}

// TestKnownToolContentType is the closed set the diagnostic above keys on, listed
// rather than derived: a derived expectation would agree with any switch,
// including one that had quietly stopped covering a member.
func TestKnownToolContentType(t *testing.T) {
	for _, known := range []string{ContentTypeContent, ContentTypeDiff, ContentTypeTerminal} {
		if !knownToolContentType(known) {
			t.Errorf("knownToolContentType(%q) = false, want true", known)
		}
	}
	for _, unknown := range []string{"structuredContent", "", "Content", "resource_link"} {
		if knownToolContentType(unknown) {
			t.Errorf("knownToolContentType(%q) = true, want false", unknown)
		}
	}
}

// TestToolCallTitle_IsTreatedAtTheDecodeDoor pins the treatment where the title
// ENTERS, so all four of its sinks are covered by one call: the persisted chat
// file, the client's tool card, WorkingLabelForKind's prompt-bar working pill,
// and the connect snapshot (which caps blocks and tool outputs and has no Title
// cap of its own).
//
// At the door rather than at each emit site, for the reason the focus DESCRIPTION
// already establishes: nothing parses or compares this value for anything but
// display, so there is no raw-for-compute need, and four emit sites are four
// places to forget. It also makes the tool card agree with the permission card,
// which has treated its own copy of the same string all along — that asymmetry
// inside one package is what marked this as a gap rather than a policy.
//
// The two ASCII titles the CLIENT classifies on ("Orchestrate Sub-agent", the
// "Sub-agent:" prefix) are byte-identical under this treatment, which is what
// makes it safe: the preset only rewrites the rune classes those titles cannot
// contain.
func TestToolCallTitle_IsTreatedAtTheDecodeDoor(t *testing.T) {
	cases := map[string]struct {
		in   string
		want string
	}{
		"bidi override is defused rather than deleted": {
			in:   "Run \u202ednuof-emaN- ecapskrow/ fr- mr\u202c",
			want: "Run  dnuof-emaN- ecapskrow/ fr- mr ",
		},
		"a newline cannot split a single-line surface": {
			in:   "Write file\nrm -rf /",
			want: "Write file rm -rf /",
		},
		"an ANSI introducer's ESC becomes a space": {
			in:   "Read \u001b]0;pwn\u0007file",
			want: "Read  ]0;pwn file",
		},
		"the pipeline driver's title is byte-identical": {
			in:   "Orchestrate Sub-agent",
			want: "Orchestrate Sub-agent",
		},
		"a delegate invocation's title is byte-identical": {
			in:   "Sub-agent: context-gatherer",
			want: "Sub-agent: context-gatherer",
		},
		"a CJK title is byte-identical": {
			in:   "ファイルを読む",
			want: "ファイルを読む",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := toolCallFromWire(
				&ACPToolCallWire{ToolCallID: "tc", Title: tc.in},
				"", toolUpdateContent{}, 0,
			)
			if got.Title != tc.want {
				t.Errorf("toolCallFromWire(title %q).Title = %q, want %q", tc.in, got.Title, tc.want)
			}
		})
	}
}

// TestToolCallTitle_IsBounded pins the other half. An unbounded title pushes the
// working pill and the tool card off their layout, and rides the connect snapshot
// where nothing else caps it.
func TestToolCallTitle_IsBounded(t *testing.T) {
	long := strings.Repeat("x", 4096)
	got := toolCallFromWire(&ACPToolCallWire{ToolCallID: "tc", Title: long}, "", toolUpdateContent{}, 0)
	// The preset carries its "..." marker OUTSIDE the cap, so a truncated value is
	// maxDisplayTextBytes+3 bytes.
	if maxLen := maxDisplayTextBytes + len("..."); len(got.Title) > maxLen {
		t.Errorf("title length = %d, want at most %d bytes", len(got.Title), maxLen)
	}
	if !strings.HasSuffix(got.Title, "...") {
		t.Errorf("a truncated title must say so; got %q", got.Title[max(0, len(got.Title)-16):])
	}
}
