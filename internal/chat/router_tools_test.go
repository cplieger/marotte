package chat

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// storeWith returns a store holding one chat whose turn carries the tool_call and, if res is non-nil, its
// tool_result, appended through the store so its bound runs first.
func storeWith(t *testing.T, call marotte.EntryToolCall, res *marotte.EntryToolResult) *Store {
	t.Helper()
	s, _ := newTestStore(t)
	turn := openPromptTurn(t, s, "c1", "m-1")
	if err := s.Append(t.Context(), "c1", entryOf(turn, "", call.ID, marotte.EntryKindToolCall, call)); err != nil {
		t.Fatalf("Setup: append tool_call: %v", err)
	}
	if res != nil {
		if err := s.Append(t.Context(), "c1", entryOf(turn, "", marotte.ToolResultID(call.ID), marotte.EntryKindToolResult, *res)); err != nil {
			t.Fatalf("Setup: append tool_result: %v", err)
		}
	}
	return s
}

// windowedCall serves the newest page and returns its one tool call's previewed halves.
func windowedCall(t *testing.T, call marotte.EntryToolCall, res *marotte.EntryToolResult) (marotte.EntryToolCall, marotte.EntryToolResult) {
	t.Helper()
	page := getPage(t, storeWith(t, call, res), "c1", "")
	var gotCall marotte.EntryToolCall
	var gotRes marotte.EntryToolResult
	calls, results := 0, 0
	for i := range page.Entries {
		switch page.Entries[i].Kind {
		case marotte.EntryKindToolCall:
			calls++
			if err := json.Unmarshal(page.Entries[i].Payload, &gotCall); err != nil {
				t.Fatalf("Setup: decode tool_call: %v", err)
			}
		case marotte.EntryKindToolResult:
			results++
			if err := json.Unmarshal(page.Entries[i].Payload, &gotRes); err != nil {
				t.Fatalf("Setup: decode tool_result: %v", err)
			}
		default:
		}
	}
	if calls != 1 || (res != nil) != (results == 1) {
		t.Fatalf("Setup: want one tool_call and %d tool_result, got %d and %d", boolToInt(res != nil), calls, results)
	}
	return gotCall, gotRes
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// settled is a completed tool_result carrying output.
func settled(output string) *marotte.EntryToolResult {
	return &marotte.EntryToolResult{Status: marotte.ToolCompleted, Output: output}
}

// TestTranscript_SmallToolCallIsSentWhole pins that most calls are small, and a second round trip for them would cost more
// than the ladder saves.
func TestTranscript_SmallToolCallIsSentWhole(t *testing.T) {
	in := marotte.EntryToolCall{ID: "tc1", Title: "Execute", Kind: marotte.ToolKindExecute, Input: json.RawMessage(`{"command":"ls"}`)}
	call, res := windowedCall(t, in, settled("all done\n"))
	if call.HasFull || res.HasFull {
		t.Errorf("has_full = %v/%v, want false for a %d-byte output", call.HasFull, res.HasFull, len(res.Output))
	}
	if res.Output != "all done\n" {
		t.Errorf("output = %q, want %q", res.Output, "all done\n")
	}
	if string(call.Input) != string(in.Input) {
		t.Errorf("input = %s, want %s", call.Input, in.Input)
	}
	if res.OutputBytes != 0 {
		t.Errorf("output_bytes = %d, want 0 when the value is whole", res.OutputBytes)
	}
}

// TestTranscript_BigOutputIsWindowedFromBothEnds pins that the last lines say how a command ended, so a prefix loses the error.
func TestTranscript_BigOutputIsWindowedFromBothEnds(t *testing.T) {
	var b strings.Builder
	for i := range 400 {
		if i == 0 {
			b.WriteString("FIRST\n")
			continue
		}
		if i == 399 {
			b.WriteString("LAST\n")
			continue
		}
		b.WriteString(strings.Repeat("m", 60) + "\n")
	}
	full := b.String()
	_, got := windowedCall(t, marotte.EntryToolCall{ID: "tc1", Title: "Execute", Kind: marotte.ToolKindExecute}, settled(full))

	if !got.HasFull {
		t.Fatal("has_full = false, want true for a windowed output")
	}
	// output_bytes is what the reveal fetches, the persisted length after the store's bound; the original is on
	// Truncated only.
	if got.Truncated == nil || got.Truncated.OutputBytes != len(full) {
		t.Errorf("truncated = %+v, want OutputBytes = %d (the length before the store cut it)",
			got.Truncated, len(full))
	}
	if got.OutputBytes >= len(full) || got.OutputBytes <= len(got.Output) {
		t.Errorf("output_bytes = %d, want the persisted length: above the previewed %d and below the original %d",
			got.OutputBytes, len(got.Output), len(full))
	}
	if len(got.Output) >= len(full) {
		t.Errorf("output is %d bytes, want fewer than the full %d", len(got.Output), len(full))
	}
	if !strings.HasPrefix(got.Output, "FIRST\n") {
		t.Errorf("output does not start with the first line: %q", got.Output[:min(40, len(got.Output))])
	}
	if !strings.HasSuffix(got.Output, "LAST\n") {
		t.Error("output does not end with the last line, so a failing command's error is lost")
	}
}

// TestTranscript_OneEnormousLineIsStillBounded pins that a line budget alone misses single multi-megabyte messages, which real
// chats hold (9.1 MB, 3.8 MB).
func TestTranscript_OneEnormousLineIsStillBounded(t *testing.T) {
	full := strings.Repeat("y", 200_000)
	_, got := windowedCall(t, marotte.EntryToolCall{ID: "tc1", Title: "Execute", Kind: marotte.ToolKindExecute}, settled(full))
	if !got.HasFull {
		t.Fatal("has_full = false, want true")
	}
	if len(got.Output) > previewBudget.outputBytes+1 {
		t.Errorf("output is %d bytes, want at most %d — a single line walked through the budget",
			len(got.Output), previewBudget.outputBytes+1)
	}
}

// TestTranscript_PreviewedOutputCarriesNoSpans pins that spans are absolute UTF-16 offsets into the whole output, so a
// windowed output ships plain and the bulk brings them back.
func TestTranscript_PreviewedOutputCarriesNoSpans(t *testing.T) {
	_, got := windowedCall(t, marotte.EntryToolCall{ID: "tc1", Title: "Execute", Kind: marotte.ToolKindExecute},
		&marotte.EntryToolResult{
			Status: marotte.ToolCompleted, Output: strings.Repeat("z", 20_000),
			OutputSpans: []marotte.TextSpan{{Start: 0, End: 5, Attrs: 1}},
		})
	if len(got.OutputSpans) != 0 {
		t.Errorf("output_spans = %v, want none on a windowed output", got.OutputSpans)
	}
}

// TestTranscript_OversizeDiffIsDroppedWholesale pins that a diff is a before/after pair the client diffs, so half a pair shows
// an edit nobody made.
func TestTranscript_OversizeDiffIsDroppedWholesale(t *testing.T) {
	// Between the two budgets, so the preview drops it (the store's drop is TestStoreBound_OversizeDiffIsDroppedNotTruncated).
	half := previewBudget.diffBytes
	_, got := windowedCall(t, marotte.EntryToolCall{ID: "tc1", Title: "Edit", Kind: marotte.ToolKindEdit},
		&marotte.EntryToolResult{Status: marotte.ToolCompleted, Diffs: []marotte.ToolDiff{
			{Path: "small.go", OldText: "a", NewText: "b"},
			{Path: "big.go", OldText: strings.Repeat("o", half), NewText: strings.Repeat("n", half)},
		}})
	if !got.HasFull {
		t.Fatal("has_full = false, want true")
	}
	if got.Truncated != nil {
		t.Errorf("truncated = %+v, want nil: the store kept both diffs", got.Truncated)
	}
	if len(got.Diffs) != 1 || got.Diffs[0].Path != "small.go" {
		t.Errorf("diffs = %v, want only the one that fit, whole", got.Diffs)
	}
}

// TestTranscript_InputKeepsItsSmallMembers pins that the card's claim line reads the small members while the bulk is one
// member.
func TestTranscript_InputKeepsItsSmallMembers(t *testing.T) {
	in, err := json.Marshal(map[string]any{
		"path":        "internal/app/main.go",
		"explanation": "rewrite the entry point",
		"text":        strings.Repeat("L", 30_000),
	})
	if err != nil {
		t.Fatalf("Setup: marshal input: %v", err)
	}
	got, _ := windowedCall(t, marotte.EntryToolCall{ID: "tc1", Title: "Write", Kind: marotte.ToolKindWrite, Input: in}, nil)
	// Over both budgets: the store dropped `text` first. Either way the claim line stays.
	if got.Truncated == nil || got.Truncated.InputBytes != len(in) {
		t.Errorf("truncated = %+v, want InputBytes = %d", got.Truncated, len(in))
	}
	var kept map[string]any
	if err := json.Unmarshal(got.Input, &kept); err != nil {
		t.Fatalf("preview input is not an object: %s", got.Input)
	}
	if kept["path"] != "internal/app/main.go" {
		t.Errorf("path = %v, want it kept — the claim line is built from it", kept["path"])
	}
	if kept["explanation"] != "rewrite the entry point" {
		t.Errorf("explanation = %v, want it kept", kept["explanation"])
	}
	if _, ok := kept["text"]; ok {
		t.Error("the oversized `text` member survived the preview")
	}
}

// The per-member cap does not bound the object: forty 3 KiB members passed whole while `has_full` claimed the input
// entire. The largest members go first, so the claim line's small ones survive.
func TestTranscript_AWideInputIsBoundedInAggregate(t *testing.T) {
	members := map[string]any{
		"path":    "internal/app/main.go",
		"command": "go build ./...",
	}
	// Each member fits its cap; together they are far over.
	for i := range 40 {
		members["blob"+strconv.Itoa(i)] = strings.Repeat("B", 3_000)
	}
	in, err := json.Marshal(members)
	if err != nil {
		t.Fatalf("Setup: marshal input: %v", err)
	}
	if len(in) <= previewBudget.inputTotal {
		t.Fatalf("Setup: fixture is %d bytes, needs to exceed the %d-byte object budget "+
			"or this test asserts nothing", len(in), previewBudget.inputTotal)
	}

	got, _ := windowedCall(t, marotte.EntryToolCall{ID: "tc1", Title: "Write", Kind: marotte.ToolKindWrite, Input: in}, nil)

	if !got.HasFull {
		t.Fatal("has_full = false for an input the transcript did not carry whole")
	}
	// The budget bounds the marshalled bytes, quotes, colons and commas included.
	if len(got.Input) > previewBudget.inputTotal {
		t.Errorf("preview input marshals to %d bytes, over the %d-byte budget by %d: the "+
			"aggregate cut charged the members' contents and not the JSON around them",
			len(got.Input), previewBudget.inputTotal, len(got.Input)-previewBudget.inputTotal)
	}
	var kept map[string]any
	if err := json.Unmarshal(got.Input, &kept); err != nil {
		t.Fatalf("preview input is not an object: %s", got.Input)
	}
	// The claim line's members are the smallest, so the last to go.
	if kept["path"] != "internal/app/main.go" {
		t.Errorf("path = %v, want it kept: the aggregate cut took a claim-line member "+
			"before the blobs", kept["path"])
	}
	if kept["command"] != "go build ./..." {
		t.Errorf("command = %v, want it kept", kept["command"])
	}
}

// The aggregate budget bounds the marshalled object, not the members' contents: with many tiny members JSON syntax
// adds four bytes each, which the fat-member fixture hides in its granularity.
func TestTranscript_TheAggregateBudgetChargesJSONsOwnSyntax(t *testing.T) {
	// Enough one-byte values that a contents-only count thinks the object fits.
	const members = 4_000
	obj := make(map[string]any, members)
	for i := range members {
		obj["k"+strconv.Itoa(i)] = 1
	}
	in, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("Setup: marshal input: %v", err)
	}

	got, _ := windowedCall(t, marotte.EntryToolCall{ID: "tc1", Title: "Write", Kind: marotte.ToolKindWrite, Input: in}, nil)

	if !got.HasFull {
		t.Fatal("has_full = false for an input the transcript did not carry whole")
	}
	if len(got.Input) > previewBudget.inputTotal {
		t.Errorf("preview input marshals to %d bytes, over the %d-byte budget by %d: the "+
			"aggregate cut charged the members' contents and not the quotes, colons and "+
			"commas around them", len(got.Input), previewBudget.inputTotal,
			len(got.Input)-previewBudget.inputTotal)
	}
	// Not satisfied by dropping everything: a claim line's worth must fit.
	var kept map[string]any
	if err := json.Unmarshal(got.Input, &kept); err != nil {
		t.Fatalf("preview input is not an object: %s", got.Input)
	}
	if len(kept) < 100 {
		t.Errorf("kept %d of %d members in a %d-byte budget, want most of what fits: the "+
			"charge per member is too high, not too low", len(kept), members,
			previewBudget.inputTotal)
	}
}

// The cut is deterministic, or map order would make a card's fields change between reloads.
func TestTranscript_TheAggregateCutIsTheSameEveryTime(t *testing.T) {
	members := make(map[string]json.RawMessage, 40)
	for i := range 40 {
		// Same size, so only the tie-break orders them.
		members["m"+strconv.Itoa(i)] = json.RawMessage(`"` + strings.Repeat("B", 3_000) + `"`)
	}
	first := ""
	for range 8 {
		kept := maps.Clone(members)
		if !trimInputToTotal(kept, 40*3_010, previewBudget) {
			t.Fatal("Setup: the fixture did not exceed the object budget")
		}
		names := slices.Sorted(maps.Keys(kept))
		joined := strings.Join(names, ",")
		if first == "" {
			first = joined
		}
		if joined != first {
			t.Fatalf("kept %v, want the same members as the first pass (%v): the cut "+
				"depends on map iteration order", names, first)
		}
	}
}

// TestToolBulk_ServesTheWholeCall is the ladder's other half.
func TestToolBulk_ServesTheWholeCall(t *testing.T) {
	// Between the budgets: the store keeps it whole, the preview cuts it.
	full := strings.Repeat("q", previewBudget.outputBytes+1_000)
	in := json.RawMessage(`{"command":"build"}`)
	s := storeWith(t, marotte.EntryToolCall{ID: "tc1", Title: "Execute", Kind: marotte.ToolKindExecute, Input: in},
		&marotte.EntryToolResult{
			Status: marotte.ToolCompleted, Output: full,
			OutputSpans: []marotte.TextSpan{{Start: 0, End: 3, Attrs: 2}},
			Diffs:       []marotte.ToolDiff{{Path: "a.go", NewText: "x"}},
		})

	req := httptest.NewRequest(http.MethodGet, "/api/chats/c1/tools/tc1", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got marotte.ToolCallBulk
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != "tc1" {
		t.Errorf("id = %q, want %q", got.ID, "tc1")
	}
	if got.Output != full {
		t.Errorf("output is %d bytes, want the whole %d", len(got.Output), len(full))
	}
	if string(got.Input) != string(in) {
		t.Errorf("input = %s, want %s", got.Input, in)
	}
	if len(got.OutputSpans) != 1 {
		t.Errorf("output_spans = %v, want the one the preview dropped", got.OutputSpans)
	}
	if len(got.Diffs) != 1 {
		t.Errorf("diffs = %v, want one", got.Diffs)
	}
}

// TestToolBulk_Rejections pins that an unknown call is a miss, a malformed id a bad request.
func TestToolBulk_Rejections(t *testing.T) {
	s := storeWith(t, marotte.EntryToolCall{ID: "tc1", Title: "Execute", Kind: marotte.ToolKindExecute}, settled("ok"))
	cases := []struct {
		name string
		path string
		want int
	}{
		{"an unknown tool call is a miss", "/api/chats/c1/tools/nope", http.StatusNotFound},
		{"an unknown chat is a miss", "/api/chats/c9/tools/tc1", http.StatusNotFound},
		{
			"a tool id outside the safe character set is refused",
			"/api/chats/c1/tools/a%20b", http.StatusBadRequest,
		},
		{"an empty tool id is refused", "/api/chats/c1/tools/", http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, c.path, nil)
			rec := httptest.NewRecorder()
			NewRouter(s).handleOne(rec, req)
			if rec.Code != c.want {
				t.Errorf("GET %s = %d, want %d (body %s)", c.path, rec.Code, c.want, rec.Body.String())
			}
		})
	}
}

// TestToolBulk_RejectsNonGet keeps the sub-resource read-only.
func TestToolBulk_RejectsNonGet(t *testing.T) {
	s := storeWith(t, marotte.EntryToolCall{ID: "tc1", Title: "Execute", Kind: marotte.ToolKindExecute}, settled("ok"))
	req := httptest.NewRequest(http.MethodPost, "/api/chats/c1/tools/tc1", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

// escapedValue is a JSON string value of n copies of c, unescaped. Built by hand: json.Marshal and the store both
// escape, which hid an accounting that measured raw bytes.
func escapedValue(c byte, n int) json.RawMessage {
	return json.RawMessage(`"` + strings.Repeat(string(c), n) + `"`)
}

// The aggregate budget charges what escaping costs: `<`, `>` and `&` become six-byte `\u00xx`, so a third of the
// budget raw marshals to twice it. Ordinary inputs (HTML or JSX, `&&` in a command) hit this.
func TestPreviewInput_TheAggregateBudgetChargesWhatEscapingCosts(t *testing.T) {
	// Each member fits its cap escaped (3,602 of 4,096); the ten fit the budget raw (6 of 16 KiB) and marshal to 36 KiB.
	obj := map[string]json.RawMessage{"path": json.RawMessage(`"internal/app/page.tsx"`)}
	for i := range 10 {
		obj["chunk"+strconv.Itoa(i)] = escapedValue('<', 600)
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("Setup: marshal input: %v", err)
	}
	// json.Marshal escaped the fixture, so pass the unescaped spelling another writer would produce.
	unescaped := unescapeUnicode(t, raw)
	if len(unescaped) <= previewBudget.inputMember {
		t.Fatalf("Setup: fixture is %d bytes, needs to exceed %d or previewInput returns "+
			"early and this test asserts nothing", len(unescaped), previewBudget.inputMember)
	}
	if len(unescaped) > previewBudget.inputTotal {
		t.Fatalf("Setup: fixture is %d raw bytes, over the %d-byte object budget already, "+
			"so a raw accounting would trim it too", len(unescaped), previewBudget.inputTotal)
	}

	got, cut := boundInput(unescaped, previewBudget)
	if !cut {
		t.Fatal("cut = false for an input that marshals to over twice the object budget")
	}
	if len(got) > previewBudget.inputTotal {
		t.Errorf("preview input marshals to %d bytes, over the %d-byte budget by %d: the "+
			"aggregate cut charged the values' raw bytes and not the escapes encoding/json "+
			"writes for them", len(got), previewBudget.inputTotal,
			len(got)-previewBudget.inputTotal)
	}
	var kept map[string]any
	if err := json.Unmarshal(got, &kept); err != nil {
		t.Fatalf("preview input is not an object: %s", got)
	}
	if kept["path"] != "internal/app/page.tsx" {
		t.Errorf("path = %v, want it kept: the aggregate cut took a claim-line member "+
			"before the chunks", kept["path"])
	}
}

// The per-member cap charges escaping too, measured against an unescaping member of the same raw size.
func TestPreviewInput_TheMemberCapChargesWhatEscapingCosts(t *testing.T) {
	obj := map[string]json.RawMessage{
		"path": json.RawMessage(`"internal/app/page.tsx"`),
		// 1,002 raw bytes, 6,002 marshalled: over the 4 KiB cap.
		"escaped": escapedValue('&', 1_000),
		// 3,102 either way: under it, though larger raw.
		"plain": escapedValue('x', 3_100),
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("Setup: marshal input: %v", err)
	}
	unescaped := unescapeUnicode(t, raw)
	if len(unescaped) <= previewBudget.inputMember {
		t.Fatalf("Setup: fixture is %d bytes, needs to exceed %d or previewInput returns "+
			"early", len(unescaped), previewBudget.inputMember)
	}

	got, cut := boundInput(unescaped, previewBudget)
	if !cut {
		t.Fatal("cut = false for an object holding a member that marshals to 6 KiB")
	}
	var kept map[string]any
	if err := json.Unmarshal(got, &kept); err != nil {
		t.Fatalf("preview input is not an object: %s", got)
	}
	if _, ok := kept["escaped"]; ok {
		t.Errorf("the `escaped` member survived: it is 1,002 raw bytes and 6,002 "+
			"marshalled, so the cap measured the wrong one and one member alone can "+
			"marshal to %d bytes", 6*previewBudget.inputMember)
	}
	if kept["plain"] != strings.Repeat("x", 3_100) {
		t.Error("the `plain` member was dropped: it is LARGER raw than the escaped one " +
			"and under the cap marshalled, so the cap is cutting on the wrong measure")
	}
}

// The early-out gate charges escaping too: 4,010 raw bytes of `<` marshal to about 24 KiB. The production caller's
// values are already escaped, so this pins the budgets agreeing, not a live leak.
func TestPreviewInput_TheEarlyOutGateChargesWhatEscapingCosts(t *testing.T) {
	// One 4,000-`<` member: 24,006 bytes marshalled, 4,010 raw, inside the member cap, so the gate decides.
	unescaped := json.RawMessage(`{"text":"` + strings.Repeat("<", 4_000) + `"}`)
	if len(unescaped) > previewBudget.inputMember {
		t.Fatalf("Setup: fixture is %d raw bytes, over the %d-byte cap already, so the "+
			"raw gate would have cut it and this test asserts nothing",
			len(unescaped), previewBudget.inputMember)
	}
	wire := inputWireBytes(unescaped)
	if len(wire) <= previewBudget.inputTotal {
		t.Fatalf("Setup: fixture marshals to %d bytes, inside the %d-byte object budget, "+
			"so nothing is over budget", len(wire), previewBudget.inputTotal)
	}

	got, cut := boundInput(unescaped, previewBudget)
	if !cut {
		t.Fatalf("cut = false for an input that marshals to %d bytes: the gate measured "+
			"the %d raw bytes and returned before either budget ran",
			len(wire), len(unescaped))
	}
	if len(got) > previewBudget.inputTotal {
		t.Errorf("preview input marshals to %d bytes, over the %d-byte budget",
			len(got), previewBudget.inputTotal)
	}
}

// An already-escaped input within budget passes unchanged, so the gate can convert before measuring.
func TestPreviewInput_AnAlreadyEscapedInputWithinBudgetIsUntouched(t *testing.T) {
	// 400 escaped `<`, about 2,411 wire bytes, under the 4,096-byte gate (the setup guard enforces it); escapes must not
	// count twice.
	obj := map[string]string{"text": strings.Repeat("<", 400)}
	wire, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("Setup: marshal: %v", err)
	}
	if len(wire) > previewBudget.inputMember {
		t.Fatalf("Setup: fixture is %d wire bytes, over the %d-byte gate, so it cannot "+
			"exercise the pass-through", len(wire), previewBudget.inputMember)
	}

	if got, cut := boundInput(wire, previewBudget); cut || got != nil {
		t.Errorf("previewInput cut an input of %d wire bytes (got %q): a value already in "+
			"its wire form is idempotent under the conversion", len(wire), got)
	}
}

// unescapeUnicode turns json.Marshal's `\u00xx` escapes back into raw bytes for fixtures.
func unescapeUnicode(t *testing.T, raw json.RawMessage) json.RawMessage {
	t.Helper()
	s := string(raw)
	for _, c := range []byte{'<', '>', '&'} {
		s = strings.ReplaceAll(s, fmt.Sprintf(`\u%04x`, c), string(c))
	}
	out := json.RawMessage(s)
	if !json.Valid(out) {
		t.Fatalf("Setup: unescaping produced invalid JSON: %s", out)
	}
	return out
}

// TestPreviewEntry_LeavesASmallEntryAlone pins copy-on-write: a small entry costs one decode and no re-encode.
func TestPreviewEntry_LeavesASmallEntryAlone(t *testing.T) {
	e := entryOf("t-1", "", "tc1:result", marotte.EntryKindToolResult,
		marotte.EntryToolResult{Status: marotte.ToolCompleted, Output: "two lines\nhere\n"})
	got := *e
	previewEntry(&got)
	if &got.Payload[0] != &e.Payload[0] {
		t.Error("previewEntry re-encoded an entry that needed no cutting")
	}
}

// TestPreviewOutput_CutsOnARuneBoundary pins that a mid-rune cut would show a replacement glyph.
func TestPreviewOutput_CutsOnARuneBoundary(t *testing.T) {
	// One long line of 3-byte runes, so every byte cut is mid-rune.
	full := strings.Repeat("\u4e16", 20_000)
	got, cut := boundOutput(full, previewBudget)
	if !cut {
		t.Fatal("cut = false, want true")
	}
	for _, r := range got {
		if r == '\uFFFD' {
			t.Fatalf("the preview holds a replacement rune: %q", got[:min(40, len(got))])
		}
	}
}
