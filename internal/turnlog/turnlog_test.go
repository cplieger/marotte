package turnlog

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// recorder is a Sink that assigns seq per turn and keeps every line, so a test can read
// back the seal order.
type recorder struct {
	t       *testing.T
	seq     map[string]uint64
	fail    error
	entries []marotte.Entry
}

// seal fails the test on an operation error and answers what it sealed; a method because
// Go cannot spread two return values into a call that also passes t.
func (r *recorder) seal(sealed []Sealed, err error) []Sealed {
	r.t.Helper()
	if err != nil {
		r.t.Fatalf("accumulator operation: %v", err)
	}
	return sealed
}

func (r *recorder) Append(_ context.Context, e *marotte.Entry) error {
	if r.fail != nil {
		return r.fail
	}
	e.Seq = r.seq[e.Turn]
	r.seq[e.Turn]++
	e.Ts = int64(len(r.entries) + 1)
	r.entries = append(r.entries, *e)
	return nil
}

// shape is one entry as "<lane>/<kind>", the two envelope fields every sealing rule
// is about.
func (r *recorder) shape() []string {
	out := make([]string, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e.Lane+"/"+string(e.Kind))
	}
	return out
}

func (r *recorder) ids() []string {
	out := make([]string, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e.ID)
	}
	return out
}

func (r *recorder) text(k int) string {
	var payload marotte.EntryText
	if err := json.Unmarshal(r.entries[k].Payload, &payload); err != nil {
		return "<unparseable: " + err.Error() + ">"
	}
	return payload.Text
}

func open(t *testing.T) (*Turn, *recorder) {
	t.Helper()
	r := &recorder{t: t, seq: make(map[string]uint64)}
	return Open("t-1", r), r
}

func equalShape(t *testing.T, got, want []string, what string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("%s sealed %v, want %v", what, got, want)
	}
}

// The sealing table, one case per row, asserting WHICH entries sealed in what order: when
// any entry takes a seq, no earlier entry in the same lane is still open.
func TestSealingTable(t *testing.T) {
	ctx := t.Context()

	t.Run("a text delta with the same say id extends the open entry", func(t *testing.T) {
		turn, rec := open(t)
		rec.seal(turn.TextDelta(ctx, "", "s1-say", "Hel"))
		rec.seal(turn.TextDelta(ctx, "", "s1-say", "lo"))
		if len(rec.entries) != 0 {
			t.Fatalf("an extended entry sealed %v, want nothing", rec.shape())
		}
		if got := turn.OpenEntries(); len(got) != 1 || got[0].Text != "Hello" || got[0].N != 2 {
			t.Errorf("open entry is %+v, want Hello over 2 deltas", got)
		}
	})

	t.Run("a text delta with a NEW say id seals the open entry", func(t *testing.T) {
		turn, rec := open(t)
		rec.seal(turn.TextDelta(ctx, "", "s1-say", "first"))
		rec.seal(turn.TextDelta(ctx, "", "s2-say", "second"))
		equalShape(t, rec.shape(), []string{"/text"}, "a new say id")
		if got := rec.ids(); got[0] != "s1-say" {
			t.Errorf("sealed entry id %q, want the say id s1-say", got[0])
		}
	})

	t.Run("a thinking delta seals a text entry in the same lane", func(t *testing.T) {
		turn, rec := open(t)
		rec.seal(turn.TextDelta(ctx, "", "s1-say", "prose"))
		rec.seal(turn.ThinkingDelta(ctx, "", "r1-say", "thought"))
		equalShape(t, rec.shape(), []string{"/text"}, "a kind switch")
		if got := turn.OpenEntries(); got[0].Kind != marotte.EntryKindThinking {
			t.Errorf("open entry kind %q, want thinking", got[0].Kind)
		}
	})

	t.Run("a tool_call seals its own lane only", func(t *testing.T) {
		turn, rec := open(t)
		rec.seal(turn.TextDelta(ctx, "", "s1-say", "parent"))
		rec.seal(turn.TextDelta(ctx, "d1", "s2-say", "delegate"))
		rec.seal(turn.ToolCall(ctx, "", &marotte.EntryToolCall{ID: "call-1"}))
		equalShape(t, rec.shape(), []string{"/text", "/tool_call"}, "a laned append")
		if got := turn.OpenEntries(); len(got) != 1 || got[0].Lane != "d1" {
			t.Errorf("open entries are %+v, want the delegate's alone", got)
		}
	})

	t.Run("a hook card is a tool_call in lane \"\"", func(t *testing.T) {
		turn, rec := open(t)
		rec.seal(turn.TextDelta(ctx, "", "s1-say", "prose"))
		rec.seal(turn.ToolCall(ctx, "", &marotte.EntryToolCall{ID: "hook-1", Kind: marotte.ToolKindHook}))
		equalShape(t, rec.shape(), []string{"/text", "/tool_call"}, "a hook card")
	})

	t.Run("an invocation is filed in the ISSUER's lane with the delegate uuid", func(t *testing.T) {
		turn, rec := open(t)
		rec.seal(turn.TextDelta(ctx, "", "s1-say", "before"))
		rec.seal(turn.Invocation(ctx, "", "delegate-uuid", &marotte.EntryToolCall{ID: "act-1"}))
		rec.seal(turn.TextDelta(ctx, "", "s2-say", "after"))
		equalShape(t, rec.shape(), []string{"/text", "/tool_call"}, "an invocation")
		var call marotte.EntryToolCall
		if err := json.Unmarshal(rec.entries[1].Payload, &call); err != nil {
			t.Fatalf("parse invocation payload: %v", err)
		}
		if call.AgentSubtaskID != "delegate-uuid" {
			t.Errorf("agent_subtask_id is %q, want the delegate uuid", call.AgentSubtaskID)
		}
		if rec.entries[1].Lane != "" {
			t.Errorf("invocation lane is %q, want the issuer's \"\"", rec.entries[1].Lane)
		}
	})

	t.Run("a tool_result lands in its CALL's lane, not the update's", func(t *testing.T) {
		turn, rec := open(t)
		rec.seal(turn.ToolCall(ctx, "d1", &marotte.EntryToolCall{ID: "call-1"}))
		rec.seal(turn.ToolResult(ctx, "", "call-1", &marotte.EntryToolResult{Status: marotte.ToolCompleted}))
		equalShape(t, rec.shape(), []string{"d1/tool_call", "d1/tool_result"}, "a disagreeing update")
		if got := rec.ids()[1]; got != "call-1:result" {
			t.Errorf("result entry id %q, want call-1:result", got)
		}
	})

	t.Run("a steer_ack lands in the chunk's own lane", func(t *testing.T) {
		turn, rec := open(t)
		rec.seal(turn.TextDelta(ctx, "d1", "s1-say", "prose"))
		rec.seal(turn.SteerAck(ctx, "d1", "steer-7", "did it"))
		equalShape(t, rec.shape(), []string{"d1/text", "d1/steer_ack"}, "an ack")
		if got := rec.ids()[1]; got != "steer-7:ack" {
			t.Errorf("ack entry id %q, want steer-7:ack", got)
		}
	})

	t.Run("a steer seals EVERY lane", func(t *testing.T) {
		turn, rec := open(t)
		rec.seal(turn.TextDelta(ctx, "", "s1-say", "parent"))
		rec.seal(turn.TextDelta(ctx, "d1", "s2-say", "delegate"))
		rec.seal(turn.Steer(ctx, "steer-1", &marotte.EntrySteer{Text: "also do X"}))
		equalShape(t, rec.shape(), []string{"/text", "d1/text", "/steer"}, "a steer")
		if got := turn.OpenEntries(); len(got) != 0 {
			t.Errorf("open entries after a steer are %+v, want none", got)
		}
	})

	t.Run("a turn_bind follows the lane-less rule with no exception", func(t *testing.T) {
		turn, rec := open(t)
		rec.seal(turn.TextDelta(ctx, "d1", "s1-say", "delegate"))
		rec.seal(turn.TurnBind(ctx, marotte.EntryTurnBind{KASMessageID: "kas-1"}))
		equalShape(t, rec.shape(), []string{"d1/text", "/turn_bind"}, "a turn_bind")
	})

	t.Run("a plan equal to the newest plan seals nothing", func(t *testing.T) {
		turn, rec := open(t)
		plan := marotte.EntryPlan{Entries: []marotte.PlanEntry{{Content: "step"}}}
		rec.seal(turn.Plan(ctx, plan))
		rec.seal(turn.TextDelta(ctx, "", "s1-say", "prose"))
		rec.seal(turn.Plan(ctx, plan))
		equalShape(t, rec.shape(), []string{"/plan"}, "a duplicate plan")
		if got := turn.OpenEntries(); len(got) != 1 {
			t.Errorf("a dropped plan sealed the open entry: %+v", got)
		}
	})

	t.Run("a plan whose entries differ seals every lane", func(t *testing.T) {
		turn, rec := open(t)
		rec.seal(turn.Plan(ctx, marotte.EntryPlan{Entries: []marotte.PlanEntry{{Content: "a"}}}))
		rec.seal(turn.TextDelta(ctx, "", "s1-say", "prose"))
		rec.seal(turn.Plan(ctx, marotte.EntryPlan{Entries: []marotte.PlanEntry{{Content: "b"}}}))
		equalShape(t, rec.shape(), []string{"/plan", "/text", "/plan"}, "a changed plan")
	})

	t.Run("a compaction, a failure and a safety block each seal every lane", func(t *testing.T) {
		turn, rec := open(t)
		rec.seal(turn.TextDelta(ctx, "", "s1-say", "a"))
		rec.seal(turn.Compaction(ctx, "summary", 0))
		rec.seal(turn.TextDelta(ctx, "", "s2-say", "b"))
		rec.seal(turn.CompactionFailed(ctx, "too big"))
		rec.seal(turn.TextDelta(ctx, "", "s3-say", "c"))
		rec.seal(turn.SafetyBlocked(ctx, []string{"p1"}))
		rec.seal(turn.TextDelta(ctx, "", "s4-say", "d"))
		rec.seal(turn.ModelSwitched(ctx, marotte.EntryModelSwitched{From: "old", To: "new"}))
		equalShape(t, rec.shape(), []string{
			"/text", "/compaction", "/text", "/compaction_failed",
			"/text", "/safety_blocked", "/text", "/model_switched",
		}, "the lane-less kinds")
	})

	t.Run("a delegate's text is not sealed by another lane's card", func(t *testing.T) {
		turn, rec := open(t)
		rec.seal(turn.TextDelta(ctx, "d1", "s1-say", "one "))
		rec.seal(turn.ToolCall(ctx, "", &marotte.EntryToolCall{ID: "call-1"}))
		rec.seal(turn.TextDelta(ctx, "d1", "s1-say", "two"))
		equalShape(t, rec.shape(), []string{"/tool_call"}, "a foreign lane's card")
		if got := turn.OpenEntries(); got[0].Text != "one two" {
			t.Errorf("delegate text is %q, want the whole paragraph", got[0].Text)
		}
	})
}

// A compaction id is derived from the summary bytes, so the live entry and its
// replayed twin carry one id.
func TestCompactionEntryIDIsDerivedFromTheSummary(t *testing.T) {
	turn, rec := open(t)
	rec.seal(turn.Compaction(t.Context(), "the summary", 0))
	want := marotte.CompactionEntryID([]byte("the summary"), 0)
	if got := rec.ids()[0]; got != want {
		t.Errorf("compaction entry id %q, want %q", got, want)
	}
}

// A say a seal split gives its later segments <say>#k, so the two sides of the merge
// agree on the segment ids.
func TestASplitSayTakesASegmentSuffix(t *testing.T) {
	ctx := t.Context()
	turn, rec := open(t)
	rec.seal(turn.TextDelta(ctx, "", "s1-say", "before "))
	rec.seal(turn.Plan(ctx, marotte.EntryPlan{Entries: []marotte.PlanEntry{{Content: "a"}}}))
	rec.seal(turn.TextDelta(ctx, "", "s1-say", "after"))
	rec.seal(turn.Steer(ctx, "steer-1", &marotte.EntrySteer{}))
	want := []string{"s1-say", "s1-say#2"}
	got := []string{rec.ids()[0], rec.ids()[2]}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("split say ids are %v, want %v", got, want)
	}
	if marotte.SayIDOf("s1-say#2") != "s1-say" {
		t.Error("SayIDOf does not invert the segment suffix this rule mints")
	}
}

// The carry rule at a seal: a carry bearing the committing prefix is DROPPED; anything
// shorter becomes the sealed entry's last delta.
func TestSteerCarryIsSettledAtTheSeal(t *testing.T) {
	ctx := t.Context()

	t.Run("a prefix-bearing carry is dropped before the card", func(t *testing.T) {
		turn, rec := open(t)
		rec.seal(turn.TextDelta(ctx, "", "s1-say", "done. "))
		turn.SetSteerCarry("", "s1-say", SteerAckPrefix+"7: partial")
		rec.seal(turn.ToolCall(ctx, "", &marotte.EntryToolCall{ID: "call-1"}))
		equalShape(t, rec.shape(), []string{"/text", "/tool_call"}, "a dropped carry")
		if got := rec.text(0); got != "done. " {
			t.Errorf("sealed text is %q, want the unclosed marker dropped", got)
		}
		if rec.entries[0].Seq >= rec.entries[1].Seq {
			t.Error("the text entry did not seal before the card")
		}
	})

	t.Run("a prose carry becomes the sealed entry's last delta", func(t *testing.T) {
		turn, rec := open(t)
		rec.seal(turn.TextDelta(ctx, "", "s1-say", "see "))
		turn.SetSteerCarry("", "s1-say", "[note")
		sealed := rec.seal(turn.ToolCall(ctx, "", &marotte.EntryToolCall{ID: "call-1"}))
		if got := rec.text(0); got != "see [note" {
			t.Errorf("sealed text is %q, want the released carry appended", got)
		}
		if sealed[0].Delta != "[note" {
			t.Errorf("released delta is %q, want the carry, so entry_delta can precede entry_sealed", sealed[0].Delta)
		}
	})

	t.Run("a released carry with nothing open opens an entry and seals it", func(t *testing.T) {
		turn, rec := open(t)
		turn.SetSteerCarry("d1", "s9-say", "[tail")
		rec.seal(turn.ToolCall(ctx, "d1", &marotte.EntryToolCall{ID: "call-1"}))
		equalShape(t, rec.shape(), []string{"d1/text", "d1/tool_call"}, "a carry with nothing open")
		if got := rec.text(0); got != "[tail" {
			t.Errorf("opened-and-sealed text is %q, want the carry", got)
		}
		if got := rec.ids()[0]; got != "s9-say" {
			t.Errorf("entry id %q, want the say the carry came from", got)
		}
	})

	t.Run("the carry is readable back so the filter can re-join it", func(t *testing.T) {
		turn, _ := open(t)
		turn.SetSteerCarry("", "s1-say", "[STE")
		if got := turn.Carry(""); got != "[STE" {
			t.Errorf("Carry is %q, want the withheld text", got)
		}
		turn.SetSteerCarry("", "s1-say", "")
		if got := turn.Carry(""); got != "" {
			t.Errorf("Carry after a clear is %q, want empty", got)
		}
	})
}

// SealLane ends ONE lane's prose with nothing to append, so metadata (a refusal) splits the
// prose without a barrier entry.
func TestSealLaneEndsOneLanesProse(t *testing.T) {
	ctx := t.Context()

	t.Run("it seals the named lane and leaves the others open", func(t *testing.T) {
		turn, rec := open(t)
		rec.seal(turn.TextDelta(ctx, "", "s1-say", "parent "))
		rec.seal(turn.TextDelta(ctx, "d1", "s2-say", "delegate"))
		if _, err := turn.SealLane(ctx, "d1"); err != nil {
			t.Fatalf("SealLane: %v", err)
		}
		equalShape(t, rec.shape(), []string{"d1/text"}, "a lane seal")
		// The parent lane kept accumulating, so its entry seals only at the close.
		rec.seal(turn.TextDelta(ctx, "", "s1-say", "prose"))
		rec.seal(turn.Close(ctx, marotte.TurnConclusion{Outcome: marotte.TurnOutcomeCompleted}))
		equalShape(t, rec.shape(), []string{"d1/text", "/text", "/turn_close"}, "a lane seal then a close")
		if got := rec.text(1); got != "parent prose" {
			t.Errorf("the parent lane sealed as %q, want the two deltas joined", got)
		}
	})

	t.Run("a later delta in the sealed lane opens a fresh entry", func(t *testing.T) {
		turn, rec := open(t)
		rec.seal(turn.TextDelta(ctx, "", "s1-say", "before"))
		if _, err := turn.SealLane(ctx, ""); err != nil {
			t.Fatalf("SealLane: %v", err)
		}
		rec.seal(turn.TextDelta(ctx, "", "s1-say", "after"))
		rec.seal(turn.Close(ctx, marotte.TurnConclusion{Outcome: marotte.TurnOutcomeCompleted}))
		equalShape(t, rec.shape(), []string{"/text", "/text", "/turn_close"}, "a delta after a lane seal")
		if got, want := rec.text(0), "before"; got != want {
			t.Errorf("the first entry is %q, want %q", got, want)
		}
		if got, want := rec.text(1), "after"; got != want {
			t.Errorf("the second entry is %q, want %q", got, want)
		}
	})

	t.Run("it settles the lane's steer carry", func(t *testing.T) {
		turn, rec := open(t)
		rec.seal(turn.TextDelta(ctx, "", "s1-say", "see "))
		turn.SetSteerCarry("", "s1-say", "[note")
		if _, err := turn.SealLane(ctx, ""); err != nil {
			t.Fatalf("SealLane: %v", err)
		}
		equalShape(t, rec.shape(), []string{"/text"}, "a lane seal with a carry")
		if len(rec.entries) == 0 {
			t.Fatal("SealLane sealed nothing, so the carry was never released")
		}
		if got := rec.text(0); got != "see [note" {
			t.Errorf("sealed text is %q, want the released carry appended", got)
		}
		if got := turn.Carry(""); got != "" {
			t.Errorf("Carry after the seal is %q, want empty", got)
		}
	})

	t.Run("it is a no-op on a lane with nothing open", func(t *testing.T) {
		turn, rec := open(t)
		sealed, err := turn.SealLane(ctx, "d9")
		if err != nil {
			t.Fatalf("SealLane: %v", err)
		}
		if len(sealed) != 0 || len(rec.entries) != 0 {
			t.Errorf("SealLane on an idle lane sealed %d entries, want none", len(rec.entries))
		}
	})

	t.Run("it is refused after the close", func(t *testing.T) {
		turn, rec := open(t)
		rec.seal(turn.Close(ctx, marotte.TurnConclusion{Outcome: marotte.TurnOutcomeCompleted}))
		if _, err := turn.SealLane(ctx, ""); !errors.Is(err, ErrClosed) {
			t.Errorf("SealLane after a close returned %v, want ErrClosed", err)
		}
	})
}

// A close seals every lane, aborts every unsettled call in its own lane, then writes the
// turn_close, the aggregate's carrier.
func TestCloseSealsAbortsThenWritesTheAggregate(t *testing.T) {
	ctx := t.Context()
	turn, rec := open(t)
	turn.Meter(1.5, 100)
	turn.Meter(0.5, 50)
	turn.SetModel("opus")
	turn.SetModel("sonnet")
	turn.ChangedFile("a.go", 3, 1, false)
	turn.ChangedFile("a.go", 2, 0, false)
	turn.AddCodeReferences(marotte.CodeReference{LicenseName: "MIT"}, marotte.CodeReference{LicenseName: "MIT"})
	turn.SetRefusal(&marotte.RefusalInfo{Category: "policy"})

	rec.seal(turn.ToolCall(ctx, "", &marotte.EntryToolCall{ID: "settled"}))
	rec.seal(turn.ToolResult(ctx, "", "settled", &marotte.EntryToolResult{Status: marotte.ToolCompleted}))
	rec.seal(turn.ToolCall(ctx, "d1", &marotte.EntryToolCall{ID: "hanging"}))
	rec.seal(turn.TextDelta(ctx, "", "s1-say", "tail"))
	rec.seal(turn.Close(ctx, marotte.TurnConclusion{
		Outcome: marotte.TurnOutcomeCompleted, RawStop: marotte.StopReasonEndTurn,
		Reason: "", Truncated: true,
	}))

	equalShape(t, rec.shape(), []string{
		"/tool_call", "/tool_result", "d1/tool_call", "/text",
		"d1/tool_result", "/turn_close",
	}, "a close")

	var aborted marotte.EntryToolResult
	if err := json.Unmarshal(rec.entries[4].Payload, &aborted); err != nil {
		t.Fatalf("parse aborted result: %v", err)
	}
	if aborted.Status != marotte.ToolAborted {
		t.Errorf("the unsettled call settled as %q, want aborted", aborted.Status)
	}
	var footer marotte.EntryTurnClose
	if err := json.Unmarshal(rec.entries[5].Payload, &footer); err != nil {
		t.Fatalf("parse turn_close: %v", err)
	}
	switch {
	case footer.Credits != 2:
		t.Errorf("credits are %v, want the two frames summed", footer.Credits)
	case footer.ElapsedMs != 150:
		t.Errorf("elapsed_ms is %v, want the two frames summed", footer.ElapsedMs)
	case footer.Model != "opus":
		t.Errorf("model is %q, want the first latched", footer.Model)
	case footer.ChangedFiles["a.go"].LinesAdded != 5:
		t.Errorf("a.go added %d lines, want both edits summed", footer.ChangedFiles["a.go"].LinesAdded)
	case len(footer.CodeReferences) != 1:
		t.Errorf("code references are %v, want the duplicate deduped", footer.CodeReferences)
	case footer.Refusal == nil || footer.Refusal.Category != "policy":
		t.Errorf("refusal is %+v, want the latched one", footer.Refusal)
	case !footer.Truncated:
		t.Error("truncated is false, want the conclusion's value")
	case footer.Outcome != marotte.TurnOutcomeCompleted:
		t.Errorf("outcome is %q, want completed", footer.Outcome)
	case footer.StopReasonRaw != string(marotte.StopReasonEndTurn):
		t.Errorf("stop_reason_raw is %q, want end_turn", footer.StopReasonRaw)
	}
}

// The changed-files aggregate is keyed by path: an empty path is skipped, a repeated path
// sums, and the create flag is the FIRST edit's.
func TestChangedFileAggregatesByPath(t *testing.T) {
	type edit struct {
		path           string
		added, removed int
		isNew          bool
	}
	cases := []struct {
		name  string
		edits []edit
		want  map[string]marotte.FileChange
	}{
		{name: "empty_path_skipped", edits: []edit{{"", 3, 1, true}}, want: map[string]marotte.FileChange{}},
		{name: "one_edit", edits: []edit{{"a.go", 3, 1, false}}, want: map[string]marotte.FileChange{
			"a.go": {LinesAdded: 3, LinesRemoved: 1},
		}},
		{name: "repeated_path_sums", edits: []edit{{"a.go", 3, 1, false}, {"a.go", 2, 4, false}}, want: map[string]marotte.FileChange{
			"a.go": {LinesAdded: 5, LinesRemoved: 5},
		}},
		{name: "create_then_edit_stays_created", edits: []edit{{"n.go", 10, 0, true}, {"n.go", 1, 1, false}}, want: map[string]marotte.FileChange{
			"n.go": {LinesAdded: 11, LinesRemoved: 1, IsNewFile: true},
		}},
		{name: "edit_then_create_is_an_edit", edits: []edit{{"e.go", 1, 1, false}, {"e.go", 2, 0, true}}, want: map[string]marotte.FileChange{
			"e.go": {LinesAdded: 3, LinesRemoved: 1},
		}},
		{name: "two_paths_stay_apart", edits: []edit{{"a.go", 1, 0, false}, {"b.go", 0, 2, true}}, want: map[string]marotte.FileChange{
			"a.go": {LinesAdded: 1}, "b.go": {LinesRemoved: 2, IsNewFile: true},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			turn, rec := open(t)
			for _, e := range tc.edits {
				turn.ChangedFile(e.path, e.added, e.removed, e.isNew)
			}
			rec.seal(turn.Close(t.Context(), marotte.TurnConclusion{
				Outcome: marotte.TurnOutcomeCompleted, RawStop: marotte.StopReasonEndTurn,
			}))
			var footer marotte.EntryTurnClose
			if err := json.Unmarshal(rec.entries[len(rec.entries)-1].Payload, &footer); err != nil {
				t.Fatalf("parse turn_close: %v", err)
			}
			if len(footer.ChangedFiles) != len(tc.want) {
				t.Fatalf("ChangedFiles has %d paths, want %d: %v", len(footer.ChangedFiles), len(tc.want), footer.ChangedFiles)
			}
			for path, want := range tc.want {
				got := footer.ChangedFiles[path]
				if got == nil || *got != want {
					t.Errorf("ChangedFiles[%q] = %+v, want %+v", path, got, want)
				}
			}
		})
	}
}

// An empty turn closes as two entries and nothing else: the turn_open the store
// wrote and the turn_close carrying outcome: empty.
func TestAnEmptyTurnClosesWithNothingBetween(t *testing.T) {
	turn, rec := open(t)
	rec.seal(turn.Close(t.Context(), marotte.TurnConclusion{Outcome: marotte.TurnOutcomeEmpty}))
	equalShape(t, rec.shape(), []string{"/turn_close"}, "an empty turn")
	var footer marotte.EntryTurnClose
	if err := json.Unmarshal(rec.entries[0].Payload, &footer); err != nil {
		t.Fatalf("parse turn_close: %v", err)
	}
	if footer.Outcome != marotte.TurnOutcomeEmpty {
		t.Errorf("outcome is %q, want empty", footer.Outcome)
	}
}

// A second closer writes nothing: the claim on the turn decides which one appends,
// and a loser must not double the aggregate.
func TestASecondCloseIsRefused(t *testing.T) {
	ctx := t.Context()
	turn, rec := open(t)
	rec.seal(turn.Close(ctx, marotte.TurnConclusion{Outcome: marotte.TurnOutcomeCompleted}))
	if _, err := turn.Close(ctx, marotte.TurnConclusion{Outcome: marotte.TurnOutcomeCancelled}); !errors.Is(err, ErrClosed) {
		t.Fatalf("a second close answered %v, want ErrClosed", err)
	}
	if _, err := turn.TextDelta(ctx, "", "s1-say", "late"); !errors.Is(err, ErrClosed) {
		t.Fatalf("a late delta answered %v, want ErrClosed", err)
	}
	if len(rec.entries) != 1 {
		t.Errorf("a refused turn wrote %v, want the one turn_close", rec.shape())
	}
}

// A sink failure is reported to the caller and nothing is handed back for a
// broadcast: the client learns of an entry only through the log.
func TestASinkFailureIsReported(t *testing.T) {
	ctx := t.Context()
	turn, rec := open(t)
	rec.seal(turn.TextDelta(ctx, "", "s1-say", "prose"))
	rec.fail = errors.New("disk is gone")
	sealed, err := turn.ToolCall(ctx, "", &marotte.EntryToolCall{ID: "call-1"})
	if err == nil {
		t.Fatal("a failed sink append reported success")
	}
	if len(sealed) != 0 {
		t.Errorf("a failed append handed back %d entries to broadcast, want none", len(sealed))
	}
}

// An unknown stop reason (KAS may add one without notice) reaches the turn_close
// verbatim beside a derived outcome, so the chat stays readable.
func TestCloseCarriesAnUnknownRawStopReasonVerbatim(t *testing.T) {
	turn, rec := open(t)
	rec.seal(turn.Close(t.Context(), marotte.ConcludeStopReason("pause_turn")))

	var footer marotte.EntryTurnClose
	if err := json.Unmarshal(rec.entries[len(rec.entries)-1].Payload, &footer); err != nil {
		t.Fatalf("parse turn_close: %v", err)
	}
	if footer.StopReasonRaw != "pause_turn" {
		t.Errorf("stop_reason_raw = %q, want the upstream's own word pause_turn", footer.StopReasonRaw)
	}
	if footer.Outcome != marotte.TurnOutcomeUnknown {
		t.Errorf("outcome = %q, want unknown: the closed enum is derived, never the raw word", footer.Outcome)
	}
	var wire map[string]any
	if err := json.Unmarshal(rec.entries[len(rec.entries)-1].Payload, &wire); err != nil {
		t.Fatalf("parse turn_close as a map: %v", err)
	}
	if got, ok := wire["stop_reason_raw"].(string); !ok || got != "pause_turn" {
		t.Errorf("stop_reason_raw on the wire = %#v, want the string pause_turn", wire["stop_reason_raw"])
	}
}

func TestTurnBind_ClosesBoundOnlyOnceTheBindIsOnDisk(t *testing.T) {
	ctx := t.Context()
	turn, rec := open(t)
	rec.fail = errors.New("disk full")
	if _, err := turn.TurnBind(ctx, marotte.EntryTurnBind{KASMessageID: "kas-1"}); err == nil {
		t.Fatal("TurnBind on a failing sink = nil, want its error")
	}
	select {
	case <-turn.Bound():
		t.Fatal("Bound() closed after a bind that never reached the sink")
	default:
	}
	rec.fail = nil
	rec.seal(turn.TurnBind(ctx, marotte.EntryTurnBind{KASMessageID: "kas-1"}))
	rec.seal(turn.TurnBind(ctx, marotte.EntryTurnBind{KASMessageID: "kas-2"}))
	select {
	case <-turn.Bound():
	default:
		t.Error("Bound() still open after a bind reached the sink")
	}
}
