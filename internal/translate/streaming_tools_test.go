package translate

import (
	"bytes"
	"cmp"
	"encoding/json"
	"maps"
	"reflect"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/slogx/capture"
)

// lineRec is a recording lineRecorder so the diff gates are observable.
type lineRec struct {
	lastDiffs []marotte.ToolDiff
	calls     int
	// lastRecency is the eviction key the tracker was handed.
	lastRecency int
}

func (r *lineRec) RecordFromDiffs(_ marotte.ChatID, diffs []marotte.ToolDiff, recency int, _ string) {
	r.calls++
	r.lastDiffs = diffs
	r.lastRecency = recency
}

type lineDeps struct {
	*baseDeps
	rec *lineRec
	// primed is the tool call primeToolCall created: the delta oracle needs the value the
	// deltas fold ONTO, and its tool_call entry frame was cleared from the stream.
	primed marotte.ToolCall
}

func (d *lineDeps) RecordFromDiffs(chatID marotte.ChatID, diffs []marotte.ToolDiff, turn int, kind string) {
	d.rec.RecordFromDiffs(chatID, diffs, turn, kind)
}

type workDirDeps struct {
	*baseDeps
	workDir string
}

func (d *workDirDeps) WorkDir() string { return d.workDir }

// hookStatusDeps overrides IsHookStatusEnabled so the hooks.showStatus gate is
// exercisable in both states (baseDeps hard-codes false).
type hookStatusDeps struct {
	*baseDeps
	enabled bool
}

func (d *hookStatusDeps) IsHookStatusEnabled() bool { return d.enabled }

var (
	_ lineRecorder = (*lineRec)(nil)
	_ hostDouble   = (*lineDeps)(nil)
	_ hostDouble   = (*workDirDeps)(nil)
	_ hostDouble   = (*hookStatusDeps)(nil)
)

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

// stashCreatedThenClear records the tool call the create frames built, then empties the
// stream so a following update is observed in isolation.
func stashCreatedThenClear(t *testing.T, deps *lineDeps, events *[]marotte.ServerEvent) {
	t.Helper()
	deps.primed, _ = foldToolCallUpdates(t, marotte.ToolCall{}, events)
	*events = nil
}

// lastToolCallUpdate folds every tool_progress DELTA onto the call its tool_call entry
// carried, then the tool_result's settled value, and returns the whole. While the call is
// open the fold is cross-checked against the turn's in-flight value, so it is not the
// emitter's rules agreeing with themselves.
func lastToolCallUpdate(t *testing.T, deps *lineDeps, events *[]marotte.ServerEvent) (marotte.ToolCall, bool) {
	t.Helper()
	// Seeded from the create primeToolCall consumed; a tool_call entry still in the stream
	// overrides it.
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

func decodePayload[T any](t *testing.T, e *marotte.Entry) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(e.Payload, &v); err != nil {
		t.Fatalf("decode %s %q: %v", e.Kind, e.ID, err)
	}
	return v
}

// applyToolResult is the client's settle, in Go: every field is replaced.
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
	tc.Offload = r.Offload
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
	if d.Offload != nil {
		tc.Offload = d.Offload
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

// TestHandleToolCall_HookAskRenderedRegardlessOfStatusSetting pins that a hook's
// ask-permission card (kind "other", _meta.kiro.hookAsk) is NOT gated on hooks.showStatus:
// suppressing it also drops the follow-up carrying the user's answer.
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

// TestHandleToolCall_DiffGate pins that HandleToolCall records line changes only when the
// call carries at least one diff.
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
		// A zero recency would file every range under the bucket evicted first.
		first := rec.lastRecency
		if first <= 0 {
			t.Errorf("with diff: RecordFromDiffs recency = %d, want a positive wall-clock stamp", first)
		}
		// A second diffed call never records behind the first: the key is an ordering.
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

// TestToolCallUpdate_TerminalStatusEmitsWorkingLabel pins that a terminal status
// broadcasts a working_label (Thinking) event.
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

// TestToolCallUpdate_OutputAppendedWhenContentPresent pins that update content text is
// sanitized, newline-terminated and appended to Output.
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

// TestToolCallUpdate_LocationsGate pins that Locations are replaced only by a non-empty list.
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

// TestToolCallUpdate_DiffPresentRecordsAndAppends pins the ledger gate: a diff always lands
// on the card, and only a `completed` tool feeds the changed-file aggregate and tracker.
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
		// The write tool's catch emits a diff with a real path and the intended text, so a failed
		// write must not claim a change.
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

// TestRelPath pins relPath's workspace-root stripping: inside paths become relative slash
// paths; an empty workdir or an escaping path returns the input unchanged.
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
		// A first component that merely BEGINS with two dots is a name, not a traversal.
		{name: "DotDotPrefixedDirIsRelative", workDir: "/work", abs: "/work/..drafts/x.go", want: "..drafts/x.go"},
		{name: "ParentEscapeReturnsAbs", workDir: "/work", abs: "/x.go", want: "/x.go"},
		// filepath.Clean turns "file:///work/x.go" into the relative "file:/work/x.go", which
		// makes filepath.Rel error and leaks the raw URI.
		{name: "FileURIBecomesRelative", workDir: "/work", abs: "file:///work/sub/file.go", want: "sub/file.go"},
		{name: "FileURIIsPercentDecoded", workDir: "/work", abs: "file:///work/hello%20world.sh", want: "hello world.sh"},
		// Normalising FIRST keeps the outside branch from returning the URI spelling.
		{name: "FileURIOutsideWorkDirIsStillAPath", workDir: "/work", abs: "file:///elsewhere/x.go", want: "/elsewhere/x.go"},
		{name: "FileURIWithEmptyWorkDirIsStillAPath", workDir: "", abs: "file:///a/b.go", want: "/a/b.go"},
		{name: "LocalhostAuthorityIsAccepted", workDir: "/work", abs: "file://localhost/work/x.go", want: "x.go"},
		// A remote authority names a file this process cannot open, so it is left alone.
		{name: "RemoteAuthorityIsLeftAlone", workDir: "/work", abs: "file://host/share/x.go", want: "file://host/share/x.go"},
		{name: "NonFileSchemeIsLeftAlone", workDir: "/work", abs: "https://example.com/x.go", want: "https://example.com/x.go"},
		// "://" trips the cheap gate but parses to no scheme; Clean collapses the slashes.
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

// TestHandleToolCall_IsNewFileFlag pins that a call counts as a new-file creation only when
// it is BOTH an edit kind AND pending.
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

// TestToolCallUpdate_CheckpointFromWire drives two real kiro-cli shapes through the JSON
// decode, pinning the `_meta.kiro.checkpoint` nesting: a misplaced tag fails silently as
// "Rewind shows no diff". KAS sends NO `original` for a file it just created.
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

// TestToolCallUpdate_CheckpointMergeIsPerField pins that a later frame with a narrower key
// set cannot erase `original`, the pre-image a diff needs.
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

// TestToolCallUpdate_CheckpointAbsentStaysNil pins that a call touching no file grows no
// checkpoint, so most chat-file tool calls carry no `"checkpoint":{}`.
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

// An update's title and kind are applied when present; an omitted one keeps what the
// initial tool_call set (KAS sends both nullish on most updates).
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

// A call's lane is fixed at the create: an id arriving on an UPDATE is never adopted,
// because re-laning re-parents the card. A disagreeing update is folded in place and logged.
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

// TestToolCallUpdate_WorkflowIDFromRawOutput pins the one field read out of `rawOutput`:
// the run id `run_workflow` reports, the only link from the invocation to its run. Every
// case is a shape KAS really sends; none may panic or contaminate it.
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

// TestToolCallUpdate_WorkflowIDIsAdoptedOnce pins that a later frame cannot name a
// different run.
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

// TestToolCallUpdate_RefusedWorkflowUpdateReadsAsDeclined pins the fifth card outcome:
// `update_workflow` concludes Success while its `updated` field reports the refusal.
func TestToolCallUpdate_RefusedWorkflowUpdateReadsAsDeclined(t *testing.T) {
	t.Parallel()

	const reason = "Cannot update a completed workflow."
	cases := []struct {
		name         string
		raw          any
		wantDeclined bool
	}{
		{"a refused update", map[string]any{"updated": false, "message": reason}, true},
		// `updated` travels on every reply, so reading its PRESENCE would mark every edit refused.
		{"an applied update", map[string]any{"updated": true, "message": "Plan updated."}, false},
		// A queued update is taken (its signal lands at the turn's end).
		{"a queued update", map[string]any{"updated": true, "queued": true}, false},
		// Absent reads as taken, which keeps every OTHER tool untouched.
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
			// A second axis, never a status: `failed` would offer "Explain this error" for a tool
			// that ran correctly.
			if got.Status != marotte.ToolCompleted {
				t.Errorf("ToolCall.Status = %q, want %q (a refusal is still a completion)", got.Status, marotte.ToolCompleted)
			}
		})
	}
}

// TestToolCallUpdate_RefusedWorkflowSaveReadsAsDeclined pins the same outcome for
// `save_workflow_definition`, which concludes Success with `saved: false` on a refusal.
func TestToolCallUpdate_RefusedWorkflowSaveReadsAsDeclined(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		raw          any
		wantDeclined bool
	}{
		{"a refused save", map[string]any{"saved": false, "errorCount": 1}, true},
		// `saved` travels on every reply, so reading its PRESENCE would mark every save refused.
		{"a saved definition", map[string]any{"saved": true, "workflowRef": "wf-def:x"}, false},
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
			if got.Status != marotte.ToolCompleted {
				t.Errorf("ToolCall.Status = %q, want %q (a refusal is still a completion)", got.Status, marotte.ToolCompleted)
			}
		})
	}
}

// TestToolCallUpdate_DeclinedOnlyGradesASettledCall pins the status gate: an in-flight
// frame has reported nothing yet, whichever workflow tool's verdict it carries.
func TestToolCallUpdate_DeclinedOnlyGradesASettledCall(t *testing.T) {
	t.Parallel()
	for name, raw := range map[string]map[string]any{
		"update": {"updated": false, "message": "not yet"},
		"save":   {"saved": false, "errorCount": 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tr, _, deps, events, chatID := primeToolCall(t)
			tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
				"toolCallId": "tc-1",
				"status":     "in_progress",
				"rawOutput":  raw,
			}), FrameAttribution{})

			got, ok := lastToolCallUpdate(t, deps, events)
			if !ok {
				t.Fatal("no tool_progress or tool_result frame emitted")
			}
			if got.Declined {
				t.Errorf("ToolCall.Declined on an in_progress frame with rawOutput %v = true, want false", raw)
			}
		})
	}
}

// TestToolCallUpdate_DeclinedIsNeverCleared pins the one-way rule: a later frame carries
// no verdict, and reading its absence as "taken" would erase the refusal.
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

// TestToolCallUpdate_AnUpdateCallDoesNotAdoptTheWorkflowID pins that an `update_workflow`
// call, which echoes the run's `workflowId` while starting nothing, is not seated on the
// run's card, where its refusal would render nowhere. `updated` tells the two apart.
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
			// The launch carries no `updated`, so the call that owns the card is untouched.
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

// remoteJSONSchemaReason is verbatim from the 2.20.1 bundle: the reason reaching the card
// unaltered is the property under test.
const remoteJSONSchemaReason = "Cannot use this tool to write a Remote JSON Schema in Supervised mode. " +
	"Switch to Autopilot mode to allow this write."

// TestHandleToolCallUpdate_FailedTakesReasonFromRawOutput drives the guard's real frame:
// status failed, the reason as a bare string in rawOutput, and an empty-path diff. KAS's
// edit arm puts the reason in no content block.
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
	// A path-less diff contributes nothing, or the card would claim a change.
	if len(got.Diffs) != 0 {
		t.Errorf("ToolCall.Diffs = %+v, want none (a diff with no path names no file)", got.Diffs)
	}
	if changed := closedChangedFiles(t, deps.baseDeps, chatID); len(changed) != 0 {
		t.Errorf("ChangedFiles = %v, want empty (the write threw before it reached a path)", changed)
	}
}

// TestHandleToolCallUpdate_FailedKeepsExistingOutput pins the `Output == ""` half of the
// gate: a failed execute tool's own output is what the reader needs.
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

// TestHandleToolCallUpdate_AFailureAfterTheReadersStopSettlesAborted pins the stop grade: KAS
// settles a call its cancel stopped as `failed`, the reader's stop makes it `aborted`, and a
// completion or a failure with no stop keeps KAS's status.
func TestHandleToolCallUpdate_AFailureAfterTheReadersStopSettlesAborted(t *testing.T) {
	t.Parallel()
	const l6 = "This tool was interrupted before it reported a result, so it may or may not have taken effect."
	cases := []struct {
		name    string
		status  string
		want    marotte.ToolStatus
		stopped bool
	}{
		{name: "failed after the stop", status: "failed", stopped: true, want: marotte.ToolAborted},
		{name: "failed with no stop", status: "failed", want: marotte.ToolFailed},
		{name: "completed after the stop", status: "completed", stopped: true, want: marotte.ToolCompleted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			tr, _, deps, events, chatID := primeToolCall(t)
			deps.stopped = map[string]bool{deps.turns.chats[chatID].ID(): c.stopped}
			tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
				"toolCallId": "tc-1",
				"status":     c.status,
				"rawOutput":  l6,
				"content": []map[string]any{
					{"type": "content", "content": map[string]any{"type": "text", "text": l6}},
				},
			}), FrameAttribution{})

			got, ok := lastToolCallUpdate(t, deps, events)
			if !ok {
				t.Fatal("no tool_progress or tool_result frame emitted")
			}
			if got.Status != c.want {
				t.Errorf("ToolCall.Status for a %q frame (stopped=%v) = %q, want %q", c.status, c.stopped, got.Status, c.want)
			}
			if got.Output != l6+"\n" {
				t.Errorf("ToolCall.Output = %q, want KAS's sentence kept on the card", got.Output)
			}
		})
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

// The sanitizer deletes U+202E from the content copy only, so the match must be
// made on the unsanitized text and the extracted message sanitized after.
func TestHandleToolCallUpdate_StringifiedObjectMatchSurvivesSanitizing(t *testing.T) {
	output := map[string]any{"message": "started\u202e run", "workflowId": "wf_9"}
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
	if got.Output != "started run\n" {
		t.Errorf("ToolCall.Output = %q, want %q", got.Output, "started run\n")
	}
}

// KAS's MCP wrapper returns {response, imageBase64Urls} and stringifies it into
// the content block, so the card must show the tool's text, not the envelope.
func TestHandleToolCallUpdate_UnwrapsTheMCPResponseEnvelope(t *testing.T) {
	const text = "3 issues found:\n- a\n- b"
	cases := map[string]struct {
		rawOutput map[string]any
		content   string // "" sends the stringified rawOutput
		status    string
		want      string
	}{
		"a different content block stays canonical": {
			rawOutput: map[string]any{"response": "envelope text"},
			content:   `{"response":"what the content says"}`, status: "completed",
			want: `{"response":"what the content says"}` + "\n",
		},
		"success envelope": {
			rawOutput: map[string]any{"response": text, "imageBase64Urls": []any{}},
			status:    "completed", want: text + "\n",
		},
		"error envelope": {
			rawOutput: map[string]any{"response": "server said no"},
			status:    "failed", want: "server said no\n",
		},
		"another tool's response-bearing object stays as sent": {
			rawOutput: map[string]any{"response": "x", "succeeded": true},
			status:    "completed", want: `{"response":"x","succeeded":true}` + "\n",
		},
		"the response is trimmed": {
			rawOutput: map[string]any{"response": "  padded \n"},
			status:    "completed", want: "padded\n",
		},
		// The match must hold on the unsanitized text.
		"an envelope whose text the sanitizer changes": {
			rawOutput: map[string]any{"response": "left\u202eright", "imageBase64Urls": []any{}},
			status:    "completed", want: "leftright\n",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tr, _, deps, events, chatID := primeToolCall(t)
			stringified := cmp.Or(tc.content, string(mustJSON(t, tc.rawOutput)))
			tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
				"toolCallId": "tc-1",
				"status":     tc.status,
				"rawOutput":  tc.rawOutput,
				"content": []map[string]any{
					{"type": "content", "content": map[string]any{"type": "text", "text": stringified}},
				},
			}), FrameAttribution{})
			got, ok := lastToolCallUpdate(t, deps, events)
			if !ok {
				t.Fatal("no tool_progress or tool_result frame emitted")
			}
			if got.Output != tc.want {
				t.Errorf("ToolCall.Output for rawOutput %s = %q, want %q", stringified, got.Output, tc.want)
			}
		})
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

// TestRawOutputFailureText pins the decode: a bare string, an object's error or message,
// else "". Each negative row is a shape KAS sends that must never become output.
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

// A disclosure and a policy denial can arrive on an update. Each fills an empty slot only,
// so a later frame cannot replace the refusal a user is reading.
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

// TestParseToolUpdateContent_UnmodelledType pins that an undecoded content block renders
// nothing and logs the two Debug lines that make the drop findable.
// Serial (no t.Parallel): captureSlog swaps the process-wide slog default.
func TestParseToolUpdateContent_UnmodelledType(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	tr := New(rolesOf(deps))

	t.Run("an unknown type renders nothing and logs both lines", func(t *testing.T) {
		var logs bytes.Buffer
		t.Cleanup(captureSlog(&logs))

		got := tr.parseToolUpdateContent("tc-1", []acpToolCallContentBlock{
			{Type: "structuredContent"},
		})

		if got.output != "" || got.diffs != nil || got.terminalID != "" {
			t.Errorf("an unmodelled block produced output: %#v", got)
		}
		line := logs.String()
		for _, want := range []string{"unmodelled type", "structuredContent", "tc-1"} {
			if !strings.Contains(line, want) {
				t.Errorf("log %q missing %q", line, want)
			}
		}
		// The second line is the observable symptom a reader searches for.
		if !strings.Contains(line, "produced nothing to render") {
			t.Errorf("log %q missing the empty-render line", line)
		}
	})

	t.Run("a claim-only tool logs nothing", func(t *testing.T) {
		// No content blocks is the common case; logging here would bury the real line.
		var logs bytes.Buffer
		t.Cleanup(captureSlog(&logs))
		tr.parseToolUpdateContent("tc-2", nil)
		if logs.Len() != 0 {
			t.Errorf("a claim-only tool logged %q", logs.String())
		}
	})

	t.Run("a known type with an unmatched payload is not called unmodelled", func(t *testing.T) {
		// An empty-text block and a path-less diff are NORMAL frames, so the guard is on the TYPE
		// rather than on whether an arm matched.
		var logs bytes.Buffer
		t.Cleanup(captureSlog(&logs))
		tr.parseToolUpdateContent("tc-3", []acpToolCallContentBlock{
			{Type: contentTypeContent},
			{Type: contentTypeDiff},
			{Type: contentTypeTerminal},
		})
		line := logs.String()
		if strings.Contains(line, "unmodelled type") {
			t.Errorf("log %q calls a known type unmodelled", line)
		}
		// Content arrived and none of it reached the card: worth logging.
		if !strings.Contains(line, "produced nothing to render") {
			t.Errorf("log %q missing the empty-render line", line)
		}
	})

	t.Run("a block that renders logs nothing", func(t *testing.T) {
		var logs bytes.Buffer
		t.Cleanup(captureSlog(&logs))
		// acpToolCallContentBlock.Content is an anonymous struct, so a literal would restate it.
		blk := acpToolCallContentBlock{Type: contentTypeContent}
		blk.Content.Text = "hello"
		got := tr.parseToolUpdateContent("tc-4", []acpToolCallContentBlock{blk})
		if got.output == "" {
			t.Fatal("a content block with text produced no output")
		}
		if logs.Len() != 0 {
			t.Errorf("a rendering block logged %q", logs.String())
		}
	})
}

// TestKnownToolContentType lists the closed set rather than deriving it, which would agree
// with any switch.
func TestKnownToolContentType(t *testing.T) {
	for _, known := range []string{contentTypeContent, contentTypeDiff, contentTypeTerminal} {
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

// TestToolCallTitle_IsTreatedAtTheDecodeDoor pins the treatment where the title ENTERS, so
// its four sinks (chat file, tool card, working pill, connect snapshot) are covered once.
// The ASCII titles the client classifies on are byte-identical under the treatment.
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
				&acpToolCallWire{ToolCallID: "tc", Title: tc.in},
				"", toolUpdateContent{}, 0,
			)
			if got.Title != tc.want {
				t.Errorf("toolCallFromWire(title %q).Title = %q, want %q", tc.in, got.Title, tc.want)
			}
		})
	}
}

// TestToolCallTitle_IsBounded pins the cap: nothing else bounds the title on the connect
// snapshot or the working pill.
func TestToolCallTitle_IsBounded(t *testing.T) {
	long := strings.Repeat("x", 4096)
	got := toolCallFromWire(&acpToolCallWire{ToolCallID: "tc", Title: long}, "", toolUpdateContent{}, 0)
	// The preset's "..." sits OUTSIDE the cap.
	if maxLen := maxDisplayTextBytes + len("..."); len(got.Title) > maxLen {
		t.Errorf("title length = %d, want at most %d bytes", len(got.Title), maxLen)
	}
	if !strings.HasSuffix(got.Title, "...") {
		t.Errorf("a truncated title must say so; got %q", got.Title[max(0, len(got.Title)-16):])
	}
}

func TestToolCallUpdate_OffloadAdoptedFromTheSettlingFrame(t *testing.T) {
	tr, _, deps, events, chatID := primeToolCall(t)
	path := "/config/home/.kiro/sessions/ab12/sess_1/tool-outputs/execute_bash-0a1b2c3d.txt"
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "completed",
		"_meta": map[string]any{"kiro": map[string]any{
			"outputTransformation": map[string]any{"kind": "offloaded", "absFilePath": path, "totalChars": 48213},
		}},
	}), FrameAttribution{})

	got, ok := lastToolCallUpdate(t, deps, events)
	if !ok {
		t.Fatal("no tool_progress or tool_result frame emitted")
	}
	if got.Offload == nil || got.Offload.Path != path || got.Offload.TotalChars != 48213 {
		t.Errorf("ToolCall.Offload = %+v, want path %q and 48213 chars", got.Offload, path)
	}
}

func TestOffloadFrom_RefusesWhatIsNotAnOffloadedFile(t *testing.T) {
	for name, in := range map[string]*acpOutputTransformation{
		"absent":        nil,
		"other_kind":    {Kind: "clipped", AbsFilePath: "/a/b.txt", TotalChars: 1},
		"relative_path": {Kind: "offloaded", AbsFilePath: "b.txt", TotalChars: 1},
		"negative_size": {Kind: "offloaded", AbsFilePath: "/a/b.txt", TotalChars: -1},
	} {
		if got := offloadFrom(in); got != nil {
			t.Errorf("offloadFrom(%s) = %+v, want nil", name, got)
		}
	}
}
