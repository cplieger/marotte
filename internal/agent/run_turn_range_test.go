package agent

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// The run's per-turn range read, addressed by turn as the `run_turn` stamp names it.

// runTurnRangeFixture opens one step turn holding turn_open(0), text(1), tool_call(2) and returns it.
func runTurnRangeFixture(t *testing.T) (*runLog, string) {
	t.Helper()
	r := newRunLog(t.TempDir())
	ctx := t.Context()
	turn, _, err := r.Open(ctx, "wf1", "root/step", "sess-a", "c-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := turn.TextDelta(ctx, "", "say-1", "hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := turn.ToolCall(ctx, "", &marotte.EntryToolCall{ID: "call-1"}); err != nil {
		t.Fatal(err)
	}
	return r, turn.ID()
}

func TestRunTurnRange_AnswersTheTailAboveAfterAndStampsTheSeqItServed(t *testing.T) {
	r, turn := runTurnRangeFixture(t)

	entries, _, stamps, found, err := r.turnRange(t.Context(), "wf1", turn, 2)
	if err != nil || !found {
		t.Fatalf("turnRange(from=2, the wire's after=1) = found %v, err %v; want the tail", found, err)
	}
	if len(entries) != 1 || entries[0].Seq != 2 || entries[0].Kind != marotte.EntryKindToolCall {
		t.Fatalf("turnRange(from=2, the wire's after=1) entries = %v, want the tool_call at seq 2", kinds(entries))
	}
	if len(stamps) != 1 {
		t.Fatalf("stamp refs = %v, want one run_turn stamp for the open turn", stampRefs(stamps))
	}
	if want := "wf1/" + turn; stamps[0].Ref != want {
		t.Fatalf("stamp ref = %q, want %q", stamps[0].Ref, want)
	}
	if want := turnVersion(turn, 2); stamps[0].Version != want {
		t.Fatalf("stamp version = %q, want the newest SERVED seq %q", stamps[0].Version, want)
	}
}

// A closed turn: after a gap the digest says `gone`, and this read must return
// the tail with its turn_close and an empty subject so the client stops treating the step as live.
func TestRunTurnRange_ServesAClosedTurnsTailWithNoStamp(t *testing.T) {
	r, turn := runTurnRangeFixture(t)
	if _, closed, err := r.CloseNode(t.Context(), "wf1", "root/step", "completed", ""); err != nil || !closed {
		t.Fatalf("CloseNode = closed %v, err %v; want the step's turn closed", closed, err)
	}

	entries, open, stamps, found, err := r.turnRange(t.Context(), "wf1", turn, 2)
	if err != nil || !found {
		t.Fatalf("turnRange(from=2, the wire's after=1) on a closed turn = found %v, err %v; want the tail", found, err)
	}
	// Three: the close synthesizes a tool_result{aborted} for the unsettled tool_call, stopping the card spinning.
	if len(entries) != 3 || entries[0].Kind != marotte.EntryKindToolCall ||
		entries[1].Kind != marotte.EntryKindToolResult || entries[2].Kind != marotte.EntryKindTurnClose {
		t.Fatalf("entries = %v, want the tool_call, its synthesized result, then the turn_close", kinds(entries))
	}
	if len(open) != 0 {
		t.Fatalf("open tails = %d, want none: a closed turn holds no lane open", len(open))
	}
	if len(stamps) != 0 {
		t.Fatalf("stamp refs = %v, want none: the ref is gone with the turn", stampRefs(stamps))
	}
}

func TestRunTurnRange_AnEmptyTailStampsTheSeqTheCallerHeld(t *testing.T) {
	r, turn := runTurnRangeFixture(t)

	entries, _, stamps, found, err := r.turnRange(t.Context(), "wf1", turn, 3)
	if err != nil || !found {
		t.Fatalf("turnRange(from=3, the wire's after=2) = found %v, err %v; want an empty tail", found, err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %v, want none above the newest seq", kinds(entries))
	}
	// A version ahead of the page would stop the client asking.
	if want := turnVersion(turn, 2); len(stamps) != 1 || stamps[0].Version != want {
		t.Fatalf("stamp versions = %v, want one at %q", stampVersions(stamps), want)
	}
}

func TestRunTurnRange_AnAbsentAfterAsksForTheWholeTurn(t *testing.T) {
	r, turn := runTurnRangeFixture(t)

	entries, _, _, found, err := r.turnRange(t.Context(), "wf1", turn, 0)
	if err != nil || !found {
		t.Fatalf("turnRange(from=0, the whole turn) = found %v, err %v", found, err)
	}
	if len(entries) != 3 || entries[0].Kind != marotte.EntryKindTurnOpen || entries[0].Seq != 0 {
		t.Fatalf("turnRange(from=0, the whole turn) = %v, want the turn_open at seq 0 and both seals", kinds(entries))
	}
}

func TestRunTurnRange_AParallelRunsOtherOpenTurnIsNeitherServedNorStamped(t *testing.T) {
	r, first := runTurnRangeFixture(t)
	ctx := t.Context()
	second, _, err := r.Open(ctx, "wf1", "root/other", "sess-b", "c-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.TextDelta(ctx, "", "say-2", "other"); err != nil {
		t.Fatal(err)
	}

	entries, _, stamps, found, err := r.turnRange(ctx, "wf1", first, 0)
	if err != nil || !found {
		t.Fatalf("turnRange(first) = found %v, err %v", found, err)
	}
	for i := range entries {
		if entries[i].Turn != first {
			t.Fatalf("entries carry turn %q, want only %q", entries[i].Turn, first)
		}
	}
	if len(stamps) != 1 || stamps[0].Ref != "wf1/"+first {
		t.Fatalf("stamp refs = %v, want %q alone", stampRefs(stamps), "wf1/"+first)
	}
}

// runTurnRangeUnreadableFixture overwrites the log in place at the same length, so the decode fails rather than the index.
func runTurnRangeUnreadableFixture(t *testing.T) (*runLog, string) {
	t.Helper()
	dir := t.TempDir()
	r := newRunLog(dir)
	ctx := t.Context()
	turn, _, err := r.Open(ctx, "wf1", "root/step", "sess-a", "c-1")
	if err != nil {
		t.Fatalf("Setup: Open: %v", err)
	}
	if _, err := turn.TextDelta(ctx, "", "say-1", "hello"); err != nil {
		t.Fatalf("Setup: TextDelta: %v", err)
	}
	path := filepath.Join(dir, runLogDir, "wf1", "entries.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Setup: read the log: %v", err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), len(raw)), 0o600); err != nil {
		t.Fatalf("Setup: corrupt the log: %v", err)
	}
	return r, turn.ID()
}

// An unreadable log is a 500; only a missing turn is the caller's 404.
func TestRunTurnRange_AnUnreadableLogAnswersTheError(t *testing.T) {
	r, turn := runTurnRangeUnreadableFixture(t)

	_, _, _, found, err := r.turnRange(t.Context(), "wf1", turn, 0)
	if err == nil || found {
		t.Fatalf("turnRange over an undecodable log = found %v, err %v; want the read error", found, err)
	}
}

// The run route's half of that split; internal/chat drives the chat twin.
func TestRunTurnRangeRoute_500sALogItCannotRead(t *testing.T) {
	r, turn := runTurnRangeUnreadableFixture(t)
	rr := &runRoutes{runs: &Runs{log: r}}

	req := httptest.NewRequest(http.MethodGet, "/api/runs/wf1/turns/"+turn, nil)
	req.SetPathValue("id", "wf1")
	req.SetPathValue("turn", turn)
	rec := httptest.NewRecorder()
	rr.handleTurnRange(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("undecodable log status = %d, want 500; body %s", rec.Code, rec.Body.String())
	}
}

func TestRunTurnRange_AnUnknownTurnAndAnUnknownRunAreBothNotFound(t *testing.T) {
	r, _ := runTurnRangeFixture(t)

	if _, _, _, found, err := r.turnRange(t.Context(), "wf1", "t-nothing-minted", 0); found || err != nil {
		t.Fatalf("turnRange(unknown turn) = found %v, err %v; want not found", found, err)
	}
	if _, _, _, found, err := r.turnRange(t.Context(), "wf-none", "t-1", 0); found || err != nil {
		t.Fatalf("turnRange(unknown run) = found %v, err %v; want not found", found, err)
	}
}

func TestRunTurnRangeRoute_ServesTheTailAnd404sAnUnknownTurn(t *testing.T) {
	r, turn := runTurnRangeFixture(t)
	rr := &runRoutes{runs: &Runs{log: r}}

	get := func(id, turnID, query string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/runs/"+id+"/turns/"+turnID+query, nil)
		req.SetPathValue("id", id)
		req.SetPathValue("turn", turnID)
		rec := httptest.NewRecorder()
		rr.handleTurnRange(rec, req)
		return rec
	}

	rec := get("wf1", turn, "?after=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Entries     []marotte.Entry         `json:"entries"`
		OpenEntries []marotte.OpenEntry     `json:"open_entries"`
		Subject     []*marotte.SubjectStamp `json:"subject"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode the reply: %v", err)
	}
	if len(got.Entries) != 1 || got.Entries[0].Seq != 2 {
		t.Fatalf("entries = %v, want the tail above seq 1", kinds(got.Entries))
	}
	if len(got.Subject) != 1 {
		t.Fatalf("subject refs = %v, want the turn's run_turn stamp", stampRefs(got.Subject))
	}
	// Arrays, never null.
	if raw := rec.Body.String(); !strings.Contains(raw, `"open_entries":[`) {
		t.Fatalf("body = %s, want open_entries as an array", raw)
	}

	if code := get("wf1", "t-nothing-minted", "").Code; code != http.StatusNotFound {
		t.Fatalf("unknown turn status = %d, want 404", code)
	}
	if code := get("wf1", turn, "?after=nope").Code; code != http.StatusBadRequest {
		t.Fatalf("unparseable after status = %d, want 400", code)
	}
	if code := get("", turn, "").Code; code != http.StatusBadRequest {
		t.Fatalf("missing workflow id status = %d, want 400", code)
	}
}

// stampRefs and stampVersions print the pointed-to values.
func stampRefs(s []*marotte.SubjectStamp) []string {
	out := make([]string, len(s))
	for i := range s {
		out[i] = s[i].Ref
	}
	return out
}

func stampVersions(s []*marotte.SubjectStamp) []string {
	out := make([]string, len(s))
	for i := range s {
		out[i] = s[i].Version
	}
	return out
}
