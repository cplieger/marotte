package chat

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// promptTurn opens a prompt-class turn carrying text and attachment paths and
// answers its turn id.
func promptTurn(t *testing.T, s *Store, id marotte.ChatID, promptID, text string, paths ...string) string {
	t.Helper()
	prompt := &marotte.EntryPrompt{ID: promptID, Text: text}
	for _, p := range paths {
		prompt.Attachments = append(prompt.Attachments, marotte.Attachment{Path: p, Name: p})
	}
	opened, err := s.OpenTurn(t.Context(), id, &TurnSpec{Source: marotte.TurnOpenNamePrompt, Prompt: prompt},
		func(c *marotte.Chat) { c.Name = string(id) })
	if err != nil {
		t.Fatalf("OpenTurn(%s): %v", promptID, err)
	}
	return opened.Turn
}

// bindTurn appends the turn_bind KAS's user_message_id_assigned frame produces.
func bindTurn(t *testing.T, s *Store, id marotte.ChatID, turn, kasID, session string) {
	t.Helper()
	e := entryOf(turn, "", turn+":bind", marotte.EntryKindTurnBind, marotte.EntryTurnBind{KASMessageID: kasID, SessionID: session})
	if err := s.Append(t.Context(), id, e); err != nil {
		t.Fatalf("Append(turn_bind): %v", err)
	}
}

// compactAt appends a compaction entry into turn and answers its id.
func compactAt(t *testing.T, s *Store, id marotte.ChatID, turn, summary string, emptyOrdinal int) string {
	t.Helper()
	cid := marotte.CompactionEntryID([]byte(summary), emptyOrdinal)
	e := entryOf(turn, "", cid, marotte.EntryKindCompaction, marotte.EntryCompaction{Summary: summary})
	if err := s.Append(t.Context(), id, e); err != nil {
		t.Fatalf("Append(compaction): %v", err)
	}
	return cid
}

func recordSession(t *testing.T, s *Store, id marotte.ChatID, session string) {
	t.Helper()
	if _, err := s.Mutate(t.Context(), id, func(c *marotte.Chat, _ bool) bool {
		c.RecordSession(session)
		return true
	}); err != nil {
		t.Fatalf("Mutate(RecordSession %s): %v", session, err)
	}
}

func TestStore_PromptAttachmentPaths_StopsAtTheWatermark(t *testing.T) {
	s, _ := newTestStore(t)
	const id marotte.ChatID = "c1"
	t1 := promptTurn(t, s, id, "u1", "first", "a.png", "b.png")
	closeTurn(t, s, id, t1, marotte.TurnOutcomeCompleted)
	t2 := promptTurn(t, s, id, "u2", "second", "c.png")
	watermark := compactAt(t, s, id, t2, "the summary", 0)
	closeTurn(t, s, id, t2, marotte.TurnOutcomeCompleted)
	t3 := promptTurn(t, s, id, "u3", "third", "d.png")
	closeTurn(t, s, id, t3, marotte.TurnOutcomeCompleted)

	all, err := s.PromptAttachmentPaths(t.Context(), id, "")
	if err != nil {
		t.Fatalf("PromptAttachmentPaths(\"\"): %v", err)
	}
	if want := []string{"a.png", "b.png", "c.png", "d.png"}; !slices.Equal(all, want) {
		t.Errorf("PromptAttachmentPaths(\"\") = %q, want %q", all, want)
	}

	after, err := s.PromptAttachmentPaths(t.Context(), id, watermark)
	if err != nil {
		t.Fatalf("PromptAttachmentPaths(%s): %v", watermark, err)
	}
	if want := []string{"d.png"}; !slices.Equal(after, want) {
		t.Errorf("PromptAttachmentPaths(watermark) = %q, want %q (only the prompts after the compaction)", after, want)
	}

	unknown, err := s.PromptAttachmentPaths(t.Context(), id, "compaction-nope")
	if err != nil {
		t.Fatalf("PromptAttachmentPaths(unknown): %v", err)
	}
	if !slices.Equal(unknown, all) {
		t.Errorf("PromptAttachmentPaths(unknown watermark) = %q, want the whole log %q", unknown, all)
	}
}

func TestStore_PromptTexts_InTurnOrder(t *testing.T) {
	s, _ := newTestStore(t)
	const id marotte.ChatID = "c1"
	t1 := promptTurn(t, s, id, "u1", "first")
	closeTurn(t, s, id, t1, marotte.TurnOutcomeCompleted)
	opened, err := s.OpenTurn(t.Context(), id, &TurnSpec{Source: marotte.TurnOpenNameWireTurnStart}, nil)
	if err != nil {
		t.Fatalf("OpenTurn(wire): %v", err)
	}
	closeTurn(t, s, id, opened.Turn, marotte.TurnOutcomeCompleted)
	t3 := promptTurn(t, s, id, "u3", "third")
	closeTurn(t, s, id, t3, marotte.TurnOutcomeCompleted)

	texts, err := s.PromptTexts(t.Context(), id)
	if err != nil {
		t.Fatalf("PromptTexts: %v", err)
	}
	if want := []string{"first", "third"}; !slices.Equal(texts, want) {
		t.Errorf("PromptTexts = %q, want %q (a promptless turn contributes nothing)", texts, want)
	}
}

func TestStore_RewindTarget_ResolvesThePromptsTurnAndItsBind(t *testing.T) {
	s, _ := newTestStore(t)
	const id marotte.ChatID = "c1"
	recordSession(t, s, id, "sess-1")
	t1 := promptTurn(t, s, id, "u1", "first")
	bindTurn(t, s, id, t1, "kas-1", "sess-1")
	closeTurn(t, s, id, t1, marotte.TurnOutcomeCompleted)
	t2 := promptTurn(t, s, id, "u2", "second")
	closeTurn(t, s, id, t2, marotte.TurnOutcomeCompleted)

	got, found, err := s.RewindTarget(t.Context(), id, "u1")
	if err != nil || !found {
		t.Fatalf("RewindTarget(u1) = found %v, err %v; want found", found, err)
	}
	if want := (marotte.RewindTarget{Turn: t1, KASMessageID: "kas-1"}); !reflect.DeepEqual(got, want) {
		t.Errorf("RewindTarget(u1) = %+v, want %+v", got, want)
	}

	got, found, err = s.RewindTarget(t.Context(), id, "u2")
	if err != nil || !found {
		t.Fatalf("RewindTarget(u2) = found %v, err %v; want found", found, err)
	}
	if want := (marotte.RewindTarget{Turn: t2}); !reflect.DeepEqual(got, want) {
		t.Errorf("RewindTarget(u2) = %+v, want %+v (a turn with no bind has no KAS id)", got, want)
	}
}

// The cut un-says the runs its entries launched: a run launched in the target turn or
// after it is named, one launched before the target turn is not, and a run is named
// once however many tool entries carry its id.
func TestStore_RewindTarget_NamesTheRunsTheCutLaunched(t *testing.T) {
	s, _ := newTestStore(t)
	const id marotte.ChatID = "c1"
	recordSession(t, s, id, "sess-1")
	t1 := promptTurn(t, s, id, "u1", "first")
	launchRun(t, s, id, t1, "call-1", "wf-before")
	closeTurn(t, s, id, t1, marotte.TurnOutcomeCompleted)
	t2 := promptTurn(t, s, id, "u2", "second")
	launchRun(t, s, id, t2, "call-2", "wf-inside")
	closeTurn(t, s, id, t2, marotte.TurnOutcomeCompleted)

	got, found, err := s.RewindTarget(t.Context(), id, "u2")
	if err != nil || !found {
		t.Fatalf("RewindTarget(u2) = found %v, err %v; want found", found, err)
	}
	if want := []string{"wf-inside"}; !slices.Equal(got.LaunchedRuns, want) {
		t.Errorf("RewindTarget(u2).LaunchedRuns = %v, want %v: only the run the cut un-says", got.LaunchedRuns, want)
	}

	got, found, err = s.RewindTarget(t.Context(), id, "u1")
	if err != nil || !found {
		t.Fatalf("RewindTarget(u1) = found %v, err %v; want found", found, err)
	}
	if want := []string{"wf-before", "wf-inside"}; !slices.Equal(got.LaunchedRuns, want) {
		t.Errorf("RewindTarget(u1).LaunchedRuns = %v, want %v: a cut at the first turn un-says both", got.LaunchedRuns, want)
	}
}

// run_workflow and inspect_workflow answer identically on the wire, so a run launched
// before the cut and inspected inside it is mentioned on both sides; the cut un-says
// the inspection, never the launch.
func TestStore_RewindTarget_ARunReMentionedAfterTheCutIsNotNamed(t *testing.T) {
	s, _ := newTestStore(t)
	const id marotte.ChatID = "c1"
	recordSession(t, s, id, "sess-1")
	t1 := promptTurn(t, s, id, "u1", "start the review")
	launchRun(t, s, id, t1, "call-1", "wf-before")
	closeTurn(t, s, id, t1, marotte.TurnOutcomeCompleted)
	t2 := promptTurn(t, s, id, "u2", "how is it going")
	launchRun(t, s, id, t2, "call-2", "wf-before")
	closeTurn(t, s, id, t2, marotte.TurnOutcomeCompleted)

	got, found, err := s.RewindTarget(t.Context(), id, "u2")
	if err != nil || !found {
		t.Fatalf("RewindTarget(u2) = found %v, err %v; want found", found, err)
	}
	if len(got.LaunchedRuns) != 0 {
		t.Errorf("RewindTarget(u2).LaunchedRuns = %v, want none: wf-before was launched in turn 1 and only inspected in turn 2", got.LaunchedRuns)
	}
}

// A closed turn can span the revert point: its tool_call sits before the target's
// turn_open and the tool_result carrying the workflow id lands after it. The launch is
// where the call was issued, and the revert's window takes whole turns, so that turn
// survives with its tail and its run stops nothing.
func TestStore_RewindTarget_AStraddlersLaunchIsBeforeTheCut(t *testing.T) {
	s, _ := newTestStore(t)
	const id marotte.ChatID = "c1"
	recordSession(t, s, id, "sess-1")
	opened, err := s.OpenTurn(t.Context(), id, &TurnSpec{Source: marotte.TurnOpenNameWireTurnStart}, func(c *marotte.Chat) { c.Name = string(id) })
	if err != nil {
		t.Fatalf("OpenTurn(wire): %v", err)
	}
	tA := opened.Turn
	call := entryOf(tA, "", "call-1", marotte.EntryKindToolCall, marotte.EntryToolCall{ID: "call-1", Title: "Run Workflow"})
	if err := s.Append(t.Context(), id, call); err != nil {
		t.Fatalf("Append(tool_call): %v", err)
	}
	t2 := promptTurn(t, s, id, "u2", "second")
	result := entryOf(tA, "", marotte.ToolResultID("call-1"), marotte.EntryKindToolResult,
		marotte.EntryToolResult{Status: marotte.ToolCompleted, WorkflowID: "wf-straddle"})
	if err := s.Append(t.Context(), id, result); err != nil {
		t.Fatalf("Append(tool_result): %v", err)
	}
	closeTurn(t, s, id, tA, marotte.TurnOutcomeCompleted)
	launchRun(t, s, id, t2, "call-2", "wf-inside")
	closeTurn(t, s, id, t2, marotte.TurnOutcomeCompleted)

	got, found, err := s.RewindTarget(t.Context(), id, "u2")
	if err != nil || !found {
		t.Fatalf("RewindTarget(u2) = found %v, err %v; want found", found, err)
	}
	if want := []string{"wf-inside"}; !slices.Equal(got.LaunchedRuns, want) {
		t.Errorf("RewindTarget(u2).LaunchedRuns = %v, want %v: wf-straddle's tool_call precedes the cut", got.LaunchedRuns, want)
	}
}

// launchRun appends the tool_call and tool_result of a run_workflow call, the result
// carrying the workflow id the way the live path records it off rawOutput.
func launchRun(t *testing.T, s *Store, id marotte.ChatID, turn, callID, workflowID string) {
	t.Helper()
	call := entryOf(turn, "", callID, marotte.EntryKindToolCall, marotte.EntryToolCall{ID: callID, Title: "Run Workflow"})
	if err := s.Append(t.Context(), id, call); err != nil {
		t.Fatalf("Append(tool_call): %v", err)
	}
	result := entryOf(turn, "", marotte.ToolResultID(callID), marotte.EntryKindToolResult,
		marotte.EntryToolResult{Status: marotte.ToolCompleted, WorkflowID: workflowID})
	if err := s.Append(t.Context(), id, result); err != nil {
		t.Fatalf("Append(tool_result): %v", err)
	}
}

func TestStore_RewindTarget_OnlyATurnOpensPromptResolves(t *testing.T) {
	s, _ := newTestStore(t)
	const id marotte.ChatID = "c1"
	t1 := promptTurn(t, s, id, "u1", "first")
	bindTurn(t, s, id, t1, "kas-1", "")
	text := entryOf(t1, "", "a1", marotte.EntryKindText, marotte.EntryText{Text: "reply"})
	if err := s.Append(t.Context(), id, text); err != nil {
		t.Fatalf("Append(text): %v", err)
	}
	closeTurn(t, s, id, t1, marotte.TurnOutcomeCompleted)

	for _, promptID := range []string{"nope", "a1", t1, t1 + ":bind", "kas-1", ""} {
		got, found, err := s.RewindTarget(t.Context(), id, promptID)
		if err != nil {
			t.Fatalf("RewindTarget(%q): %v", promptID, err)
		}
		if found || !reflect.DeepEqual(got, marotte.RewindTarget{}) {
			t.Errorf("RewindTarget(%q) = %+v found=%v, want not found", promptID, got, found)
		}
	}
}

func TestStore_RewindTarget_ABindFromARetiredSessionYieldsNoKASID(t *testing.T) {
	s, _ := newTestStore(t)
	const id marotte.ChatID = "c1"
	recordSession(t, s, id, "sess-1")
	t1 := promptTurn(t, s, id, "u1", "first")
	bindTurn(t, s, id, t1, "kas-1", "sess-1")
	closeTurn(t, s, id, t1, marotte.TurnOutcomeCompleted)
	recordSession(t, s, id, "sess-2")
	t2 := promptTurn(t, s, id, "u2", "second")
	bindTurn(t, s, id, t2, "kas-2", "sess-2")
	closeTurn(t, s, id, t2, marotte.TurnOutcomeCompleted)

	got, found, err := s.RewindTarget(t.Context(), id, "u1")
	if err != nil || !found {
		t.Fatalf("RewindTarget(u1) = found %v, err %v; want found", found, err)
	}
	if want := (marotte.RewindTarget{Turn: t1}); !reflect.DeepEqual(got, want) {
		t.Errorf("RewindTarget(u1) = %+v, want %+v (kas-1 was minted by sess-1, which is retired)", got, want)
	}

	got, found, err = s.RewindTarget(t.Context(), id, "u2")
	if err != nil || !found {
		t.Fatalf("RewindTarget(u2) = found %v, err %v; want found", found, err)
	}
	if want := (marotte.RewindTarget{Turn: t2, KASMessageID: "kas-2"}); !reflect.DeepEqual(got, want) {
		t.Errorf("RewindTarget(u2) = %+v, want %+v (the current session's bind stands)", got, want)
	}
}

// retryTurn opens the empty-turn retry, which re-sends promptID on the session
// the recovery just recorded.
func retryTurn(t *testing.T, s *Store, id marotte.ChatID, promptID, text string) string {
	t.Helper()
	opened, err := s.OpenTurn(t.Context(), id, &TurnSpec{Source: marotte.TurnOpenNameEmptyRetry, Prompt: &marotte.EntryPrompt{ID: promptID, Text: text}}, nil)
	if err != nil {
		t.Fatalf("OpenTurn(retry %s): %v", promptID, err)
	}
	return opened.Turn
}

func TestStore_RewindTarget_AnEmptyRetryCutsAtTheFirstAttemptWithTheRetrysBind(t *testing.T) {
	s, _ := newTestStore(t)
	const id marotte.ChatID = "c1"
	recordSession(t, s, id, "sess-1")
	t1 := promptTurn(t, s, id, "u1", "first")
	bindTurn(t, s, id, t1, "kas-1", "sess-1")
	closeTurn(t, s, id, t1, marotte.TurnOutcomeEmpty)
	recordSession(t, s, id, "sess-2")
	t2 := retryTurn(t, s, id, "u1", "first")
	bindTurn(t, s, id, t2, "kas-2", "sess-2")
	closeTurn(t, s, id, t2, marotte.TurnOutcomeCompleted)

	got, found, err := s.RewindTarget(t.Context(), id, "u1")
	if err != nil || !found {
		t.Fatalf("RewindTarget(u1) = found %v, err %v; want found", found, err)
	}
	if want := (marotte.RewindTarget{Turn: t1, KASMessageID: "kas-2"}); !reflect.DeepEqual(got, want) {
		t.Errorf("RewindTarget(u1) = %+v, want %+v (the cut drops both attempts; kas-2 is the one bind on the current session)", got, want)
	}
}

func TestStore_RewindTarget_AnUnboundFirstAttemptStillLeadsTheCut(t *testing.T) {
	s, _ := newTestStore(t)
	const id marotte.ChatID = "c1"
	recordSession(t, s, id, "sess-1")
	t1 := promptTurn(t, s, id, "u1", "first")
	closeTurn(t, s, id, t1, marotte.TurnOutcomeEmpty)
	t2 := retryTurn(t, s, id, "u1", "first")
	bindTurn(t, s, id, t2, "kas-2", "sess-1")
	closeTurn(t, s, id, t2, marotte.TurnOutcomeCompleted)

	got, found, err := s.RewindTarget(t.Context(), id, "u1")
	if err != nil || !found {
		t.Fatalf("RewindTarget(u1) = found %v, err %v; want found", found, err)
	}
	if want := (marotte.RewindTarget{Turn: t1, KASMessageID: "kas-2"}); !reflect.DeepEqual(got, want) {
		t.Errorf("RewindTarget(u1) = %+v, want %+v (a later attempt never moves the cut past the first)", got, want)
	}
}

func TestStore_RewindTarget_NoHeaderIsChatNotFound(t *testing.T) {
	s, _ := newTestStore(t)
	_, found, err := s.RewindTarget(t.Context(), "c-missing", "u1")
	if found || err == nil {
		t.Errorf("RewindTarget(no header) = found %v, err %v; want an error and not found", found, err)
	}
}

func TestStore_EmptyCompactions_CountsTheEmptySummariesAlone(t *testing.T) {
	s, _ := newTestStore(t)
	const id marotte.ChatID = "c1"
	t1 := promptTurn(t, s, id, "u1", "first")
	compactAt(t, s, id, t1, "a real summary", 0)
	compactAt(t, s, id, t1, "", 1)
	compactAt(t, s, id, t1, "", 2)
	closeTurn(t, s, id, t1, marotte.TurnOutcomeCompleted)

	n, err := s.EmptyCompactions(t.Context(), id)
	if err != nil {
		t.Fatalf("EmptyCompactions: %v", err)
	}
	if n != 2 {
		t.Errorf("EmptyCompactions = %d, want 2", n)
	}
}

func TestStore_TurnCount_ReadsTheHeader(t *testing.T) {
	s, _ := newTestStore(t)
	const id marotte.ChatID = "c1"
	if n, ok := s.TurnCount(t.Context(), id); ok || n != 0 {
		t.Errorf("TurnCount(no header) = %d, %v; want 0, false", n, ok)
	}
	t1 := promptTurn(t, s, id, "u1", "first")
	closeTurn(t, s, id, t1, marotte.TurnOutcomeCompleted)
	promptTurn(t, s, id, "u2", "second")
	if n, ok := s.TurnCount(t.Context(), id); !ok || n != 0 {
		t.Errorf("TurnCount(before WriteCounters) = %d, %v; want 0, true (the header caches the count, the log does not write it)", n, ok)
	}
	if err := s.WriteCounters(t.Context(), id); err != nil {
		t.Fatalf("WriteCounters: %v", err)
	}

	n, ok := s.TurnCount(t.Context(), id)
	if !ok || n != 2 {
		t.Errorf("TurnCount = %d, %v; want 2, true (turn_count is the newest turn_open.n, closed or not)", n, ok)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if n, ok := s.TurnCount(ctx, id); ok || n != 0 {
		t.Errorf("TurnCount(dead ctx) = %d, %v; want 0, false", n, ok)
	}
}
