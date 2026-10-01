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

// The run's per-turn range read: the repair a run pane runs on a `run_turn` stamp
// mismatch or a seq hole, addressed by TURN because that is what the stamp names.

// runTurnRangeFixture opens one step turn, seals two entries into it and answers the
// registry plus that turn's id. The text seals at seq 1 and the tool_call at seq 2, so
// the turn holds turn_open(0), text(1), tool_call(2).
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

// A CLOSED turn is the case design 13 property 8 arm 2 turns on: after a gap the digest
// answers `gone` for the stamp, the client reads this range, and what it needs back is the
// tail INCLUDING the turn_close plus an empty subject, which is what lets it drop the stamp
// and stop reading the step as live. Nothing about the function distinguishes an open turn
// from a closed one except which branch of the stamp loop matches, so without this case a
// later reader answering found=false from the `rec.open` miss turns the whole `gone` path
// into a 404 with every other case still green.
func TestRunTurnRange_ServesAClosedTurnsTailWithNoStamp(t *testing.T) {
	r, turn := runTurnRangeFixture(t)
	if _, closed, err := r.CloseNode(t.Context(), "wf1", "root/step", "completed", ""); err != nil || !closed {
		t.Fatalf("CloseNode = closed %v, err %v; want the step's turn closed", closed, err)
	}

	entries, open, stamps, found, err := r.turnRange(t.Context(), "wf1", turn, 2)
	if err != nil || !found {
		t.Fatalf("turnRange(from=2, the wire's after=1) on a closed turn = found %v, err %v; want the tail", found, err)
	}
	// THREE, not two: the close settles what it makes unsettleable, so the turn's unsettled
	// tool_call takes a synthesized tool_result{aborted} before the turn_close. The client's
	// range read adopts that result like any other entry, which is what stops a step's card
	// spinning for the tab's life after a gap.
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
	// One version AHEAD of the page is the defect this pins: the client would hold that
	// version and never ask again.
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

// runTurnRangeUnreadableFixture opens one turn, seals one entry into it and then
// overwrites the log's bytes IN PLACE at the same length, so the in-memory offset index
// still points inside the file and it is the DECODE that fails rather than the index:
// that is the shape a torn or corrupt log takes for a range read.
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

// A log this server cannot READ is its own fault, so the error travels and the route
// answers 500; only an id the log holds no turn for is the caller's answer. Folding the
// two made the route's 500 arm and its Warn unreachable, so a disk or decode fault
// reached the client as a 404 telling it to stop asking about a turn that is there.
func TestRunTurnRange_AnUnreadableLogAnswersTheError(t *testing.T) {
	r, turn := runTurnRangeUnreadableFixture(t)

	_, _, _, found, err := r.turnRange(t.Context(), "wf1", turn, 0)
	if err == nil || found {
		t.Fatalf("turnRange over an undecodable log = found %v, err %v; want the read error", found, err)
	}
}

// The run route's own half of that division, which nothing pinned while the error was
// folded away: the chat twin is driven end to end through its router in internal/chat,
// and this is the run side's.
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
	// Both lists travel as arrays rather than null, or the client's array readers
	// fail the whole page.
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

// stampRefs and stampVersions keep a failure readable: the stamps are pointers, so %v
// on the slice prints addresses.
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
