package translate

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// summarizationInfo builds a session_info_update payload carrying a
// _meta.kiro.summarization block with the given status (and optional summary),
// matching the KAS v3 wire shape HandleSessionInfoUpdate decodes.
func summarizationInfo(t *testing.T, status, summary string) json.RawMessage {
	t.Helper()
	sum := map[string]any{"status": status}
	if summary != "" {
		sum["summary"] = map[string]any{"conversationSummary": summary}
	}
	return mustJSON(t, map[string]any{
		"_meta": map[string]any{"kiro": map[string]any{"summarization": sum}},
	})
}

func entriesOfKind(entries []marotte.Entry, kind marotte.EntryKind) []marotte.Entry {
	var got []marotte.Entry
	for _, e := range entries {
		if e.Kind == kind {
			got = append(got, e)
		}
	}
	return got
}

func compactionsOf(t *testing.T, entries []marotte.Entry) []marotte.EntryCompaction {
	t.Helper()
	var out []marotte.EntryCompaction
	for _, e := range entriesOfKind(entries, marotte.EntryKindCompaction) {
		var p marotte.EntryCompaction
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("decode compaction %q: %v", e.ID, err)
		}
		out = append(out, p)
	}
	return out
}

func compactionFailuresOf(t *testing.T, entries []marotte.Entry) []marotte.EntryCompactionFailed {
	t.Helper()
	var out []marotte.EntryCompactionFailed
	for _, e := range entriesOfKind(entries, marotte.EntryKindCompactionFailed) {
		var p marotte.EntryCompactionFailed
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("decode compaction_failed %q: %v", e.ID, err)
		}
		out = append(out, p)
	}
	return out
}

// entryKinds is the entries' kinds in seal order: the shape a mid-turn compaction is
// judged on, since where the summary sits is log position and nothing else.
func entryKinds(entries []marotte.Entry) []marotte.EntryKind {
	kinds := make([]marotte.EntryKind, 0, len(entries))
	for _, e := range entries {
		kinds = append(kinds, e.Kind)
	}
	return kinds
}

func errorPayloads(t *testing.T, events *[]marotte.ServerEvent) []marotte.ErrorPayload {
	t.Helper()
	var got []marotte.ErrorPayload
	for _, e := range *events {
		if e.Type != marotte.EventError {
			continue
		}
		p, ok := e.Payload.(marotte.ErrorPayload)
		if !ok {
			t.Fatalf("EventError payload type = %T, want marotte.ErrorPayload", e.Payload)
		}
		got = append(got, p)
	}
	return got
}

func countCompactionStarted(events *[]marotte.ServerEvent) int {
	n := 0
	for _, e := range *events {
		if e.Type == marotte.EventCompactionStarted {
			n++
		}
	}
	return n
}

// TestHandleV3Summarization_CanceledIsBenign pins that a KAS summarization
// "canceled"/"cancelled" reason is a quiet no-op: no compaction entry, no
// compaction_failed entry, no error banner, no host tell, nothing on the wire.
func TestHandleV3Summarization_CanceledIsBenign(t *testing.T) {
	for _, status := range []string{"canceled", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			deps, events, store := depsWithStore(t, "c1")
			tr := New(rolesOf(deps))

			tr.HandleSessionInfoUpdate(t.Context(), "c1", summarizationInfo(t, status, ""), FrameAttribution{})

			if got := deps.between["c1"]; len(got) != 0 {
				t.Errorf("between-turns entries = %v, want none (cancel is benign)", entryKinds(got))
			}
			if got := deps.chatEntries("c1"); len(got) != 0 {
				t.Errorf("turn entries = %v, want none (cancel is benign)", entryKinds(got))
			}
			if p := errorPayloads(t, events); len(p) != 0 {
				t.Errorf("EventError broadcasts = %+v, want none (cancel must not banner)", p)
			}
			if len(deps.compactionFailures) != 0 {
				t.Errorf("CompactionFailed calls = %+v, want none for %q", deps.compactionFailures, status)
			}
			if len(*events) != 0 {
				t.Errorf("broadcasts = %d, want 0 (cancel is a silent no-op)", len(*events))
			}
			if c, _ := store.Get(t.Context(), "c1"); c.CompactionWatermark != "" {
				t.Errorf("CompactionWatermark = %q, want empty (no compaction occurred)", c.CompactionWatermark)
			}
		})
	}
}

// TestHandleV3Summarization_SuccessCompletes pins the between-turns completion: the entry
// files after the newest turn's close and the watermark names it.
func TestHandleV3Summarization_SuccessCompletes(t *testing.T) {
	deps, events, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1", summarizationInfo(t, "success", "history summary"), FrameAttribution{})

	between := deps.between["c1"]
	if got := entryKinds(between); len(got) != 1 || got[0] != marotte.EntryKindCompaction {
		t.Fatalf("between-turns entries = %v, want just the compaction", got)
	}
	if got := compactionsOf(t, between); got[0].Summary != "history summary" {
		t.Errorf("compaction summary = %q, want %q", got[0].Summary, "history summary")
	}
	if got := deps.chatEntries("c1"); len(got) != 0 {
		t.Errorf("turn entries = %v, want none (nothing to seal between turns)", entryKinds(got))
	}
	wantID := marotte.CompactionEntryID([]byte("history summary"), 1)
	if between[0].ID != wantID {
		t.Errorf("compaction entry id = %q, want the summary-derived %q", between[0].ID, wantID)
	}
	c, _ := store.Get(t.Context(), "c1")
	if c.CompactionWatermark != wantID {
		t.Errorf("CompactionWatermark = %q, want the compaction entry's id %q", c.CompactionWatermark, wantID)
	}
	if !hasEntryAppended(events, marotte.EntryKindCompaction) {
		t.Errorf("no entry_appended{compaction} on the wire; events = %d", len(*events))
	}
	if len(deps.compactionFailures) != 0 {
		t.Errorf("CompactionFailed calls = %+v, want none on success", deps.compactionFailures)
	}
	if p := errorPayloads(t, events); len(p) != 0 {
		t.Errorf("EventError broadcasts = %+v, want none on success", p)
	}
}

// TestHandleV3Summarization_SealsTheLanesBeforeTheEntry pins a mid-turn compaction sealing
// every lane first, so it sits where it happened.
func TestHandleV3Summarization_SealsTheLanesBeforeTheEntry(t *testing.T) {
	deps, events, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	turn := deps.turns.chatTurn("c1")
	if _, err := turn.TextDelta(t.Context(), "", "say-1", "before the compaction"); err != nil {
		t.Fatalf("stage the open text: %v", err)
	}

	tr.HandleSessionInfoUpdate(t.Context(), "c1", summarizationInfo(t, "success", "history summary"), FrameAttribution{})

	sealed := deps.chatEntries("c1")
	want := []marotte.EntryKind{marotte.EntryKindText, marotte.EntryKindCompaction}
	if got := entryKinds(sealed); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("sealed entries = %v, want %v (the open text seals ahead of the summary)", got, want)
	}
	var text marotte.EntryText
	if err := json.Unmarshal(sealed[0].Payload, &text); err != nil || text.Text != "before the compaction" {
		t.Errorf("sealed text = %q (%v), want the pre-compaction prose", text.Text, err)
	}
	if deps.turns.closed(turn) {
		t.Error("the turn closed on a compaction; only the turn end closes it")
	}
	if open := turn.OpenEntries(); len(open) != 0 {
		t.Errorf("open entries after the seal = %d, want 0", len(open))
	}
	c, _ := store.Get(t.Context(), "c1")
	if c.CompactionWatermark != sealed[1].ID {
		t.Errorf("CompactionWatermark = %q, want the compaction entry's id %q", c.CompactionWatermark, sealed[1].ID)
	}
	if got := deps.between["c1"]; len(got) != 0 {
		t.Errorf("between-turns entries = %v, want none while a turn is open", entryKinds(got))
	}
	if !hasEntryAppended(events, marotte.EntryKindCompaction) {
		t.Errorf("no entry_appended{compaction} on the wire; events = %d", len(*events))
	}

	// The rest of the turn opens a fresh entry rather than re-extending the sealed one.
	if _, err := turn.TextDelta(t.Context(), "", "say-1", "after"); err != nil {
		t.Fatalf("stage the follow-on text: %v", err)
	}
	if open := turn.OpenEntries(); len(open) != 1 || open[0].Text != "after" {
		t.Errorf("open entries after the follow-on delta = %+v, want one holding %q", open, "after")
	}
	if got := deps.chatEntries("c1"); len(got) != 2 {
		t.Errorf("sealed entries after the follow-on delta = %d, want still 2", len(got))
	}
}

// A tool call still in flight does not hold the compaction back: its result is an
// entry of its own and lands in the turn after the summary.
func TestHandleV3Summarization_AToolCallInFlightStillLandsAfterTheEntry(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	turn := deps.turns.chatTurn("c1")
	if _, err := turn.ToolCall(t.Context(), "", &marotte.EntryToolCall{ID: "t-1", Status: marotte.ToolInProgress}); err != nil {
		t.Fatalf("stage the tool call: %v", err)
	}

	tr.HandleSessionInfoUpdate(t.Context(), "c1", summarizationInfo(t, "success", "history summary"), FrameAttribution{})
	if _, err := turn.ToolResult(t.Context(), "", "t-1", &marotte.EntryToolResult{Status: marotte.ToolCompleted}); err != nil {
		t.Fatalf("settle the tool call: %v", err)
	}

	want := []marotte.EntryKind{marotte.EntryKindToolCall, marotte.EntryKindCompaction, marotte.EntryKindToolResult}
	got := entryKinds(deps.chatEntries("c1"))
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("sealed entries = %v, want %v", got, want)
	}
}

// TestHandleV3Summarization_FailureLandsInTheTurnWithoutAWatermark pins a failed compaction
// as an entry of the turn with no watermark move.
func TestHandleV3Summarization_FailureLandsInTheTurnWithoutAWatermark(t *testing.T) {
	deps, events, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	turn := deps.turns.chatTurn("c1")
	if _, err := turn.TextDelta(t.Context(), "", "say-1", "mid-reply"); err != nil {
		t.Fatalf("stage the open text: %v", err)
	}

	tr.HandleSessionInfoUpdate(t.Context(), "c1", summarizationInfo(t, "error", ""), FrameAttribution{})

	want := []marotte.EntryKind{marotte.EntryKindText, marotte.EntryKindCompactionFailed}
	sealed := deps.chatEntries("c1")
	if got := entryKinds(sealed); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("sealed entries = %v, want %v", got, want)
	}
	if deps.turns.closed(turn) {
		t.Error("the turn closed on a failed compaction; only the turn end closes it")
	}
	if c, _ := store.Get(t.Context(), "c1"); c.CompactionWatermark != "" {
		t.Errorf("CompactionWatermark = %q, want empty (nothing was compacted)", c.CompactionWatermark)
	}
	if !hasEntryAppended(events, marotte.EntryKindCompactionFailed) {
		t.Errorf("no entry_appended{compaction_failed} on the wire; events = %d", len(*events))
	}
}

// TestHandleV3Summarization_GenuineErrorFails pins that a genuine "error" files the entry,
// broadcasts the banner and tells the host.
func TestHandleV3Summarization_GenuineErrorFails(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1", summarizationInfo(t, "error", ""), FrameAttribution{})

	failed := compactionFailuresOf(t, deps.between["c1"])
	if len(failed) != 1 {
		t.Fatalf("compaction_failed entries = %d, want 1", len(failed))
	}
	if failed[0].Reason != "error" {
		t.Errorf("compaction_failed reason = %q, want %q", failed[0].Reason, "error")
	}
	p := errorPayloads(t, events)
	if len(p) != 1 {
		t.Fatalf("EventError broadcasts = %d, want 1", len(p))
	}
	if p[0].Code != marotte.ErrCodeCompactionFailed {
		t.Errorf("error code = %q, want %q", p[0].Code, marotte.ErrCodeCompactionFailed)
	}
	if p[0].Message != "error" {
		t.Errorf("error message = %q, want %q", p[0].Message, "error")
	}
	if !p[0].TurnScoped {
		t.Error("the compaction error banner is not turn-scoped")
	}
	if len(deps.compactionFailures) != 1 || deps.compactionFailures[0].chatID != "c1" || deps.compactionFailures[0].detail != "error" {
		t.Errorf("CompactionFailed calls = %+v, want one call for c1 with detail error", deps.compactionFailures)
	}
}

// TestHandleV3Summarization_RunningStarts pins that a "running" reason
// broadcasts exactly one compaction_started signal and files no entry.
func TestHandleV3Summarization_RunningStarts(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1", summarizationInfo(t, "running", ""), FrameAttribution{})

	if n := countCompactionStarted(events); n != 1 {
		t.Errorf("compaction_started broadcasts = %d, want 1", n)
	}
	if len(deps.compactionFailures) != 0 {
		t.Errorf("CompactionFailed calls = %+v, want none while running", deps.compactionFailures)
	}
	if p := errorPayloads(t, events); len(p) != 0 {
		t.Errorf("EventError broadcasts = %+v, want none while running", p)
	}
	if got := deps.between["c1"]; len(got) != 0 {
		t.Errorf("between-turns entries = %v, want none while running", entryKinds(got))
	}
}

// TestHandleV3Summarization_ALandedCompactionIsSilent pins no log line on the ordinary path
// (the refusal's Warn is pinned in tombstone_drop_test.go).
func TestHandleV3Summarization_ALandedCompactionIsSilent(t *testing.T) {
	for _, status := range []string{"success", "error"} {
		t.Run(status, func(t *testing.T) {
			var logs bytes.Buffer
			t.Cleanup(captureSlog(&logs))
			deps, _, _ := depsWithStore(t, "c1")
			tr := New(rolesOf(deps))

			tr.HandleSessionInfoUpdate(t.Context(), "c1", summarizationInfo(t, status, "history summary"), FrameAttribution{})

			if got := logs.String(); strings.Contains(got, "level=WARN") || strings.Contains(got, "level=ERROR") {
				t.Errorf("a landed %q compaction logged:\n%s", status, got)
			}
		})
	}
}

func TestHandleCompactionFailed_BoundsAndSanitizesTheDetail(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	detail := strings.Repeat("x", maxCompactionDetailBytes) + "\nsecret tail"

	tr.HandleSessionInfoUpdate(t.Context(), "c1", summarizationInfo(t, detail, ""), FrameAttribution{})

	failed := compactionFailuresOf(t, deps.between["c1"])
	if len(failed) != 1 {
		t.Fatalf("compaction_failed entries = %d, want 1", len(failed))
	}
	if strings.Contains(failed[0].Reason, "\n") || strings.Contains(failed[0].Reason, "secret tail") {
		t.Errorf("bounded detail = %q, want one sanitized line without the tail", failed[0].Reason)
	}
	payloads := errorPayloads(t, events)
	if len(payloads) != 1 || payloads[0].Message != failed[0].Reason {
		t.Errorf("error payloads = %+v, want the filed bounded detail %q", payloads, failed[0].Reason)
	}
	if len(deps.compactionFailures) != 1 || deps.compactionFailures[0].detail != failed[0].Reason {
		t.Errorf("CompactionFailed calls = %+v, want the filed bounded detail %q", deps.compactionFailures, failed[0].Reason)
	}
}
