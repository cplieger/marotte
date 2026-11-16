package chat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func loadSkillCall(id string) marotte.EntryToolCall {
	return marotte.EntryToolCall{
		ID: id, Title: "Tool Load", Kind: marotte.ToolKindOther, Status: marotte.ToolPending,
		ToolName: "tool_load", Input: json.RawMessage(`{"id":"skill::land"}`),
	}
}

var landLoaded = []marotte.LoadedResource{{ID: "skill::land", Type: marotte.LoadedSkill, Name: "land"}}

// servedTool decodes the page's one entry of kind into v, failing when the page holds none or several.
func servedTool(t *testing.T, entries []marotte.Entry, kind marotte.EntryKind, v any) {
	t.Helper()
	n := 0
	for i := range entries {
		if entries[i].Kind != kind {
			continue
		}
		n++
		if err := json.Unmarshal(entries[i].Payload, v); err != nil {
			t.Fatalf("decode the served %s: %v", kind, err)
		}
	}
	if n != 1 {
		t.Fatalf("the page serves %d %s entries, want 1", n, kind)
	}
}

// The page serves the title the card draws while the record keeps only what the agent sent.
func TestHandleChatPage_ServesTheCardTitle(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	turn := openPromptTurn(t, s, "c1", "m-1")
	call := loadSkillCall("tc1")
	if err := s.Append(t.Context(), "c1", entryOf(turn, "", call.ID, marotte.EntryKindToolCall, call)); err != nil {
		t.Fatalf("Setup: append tool_call: %v", err)
	}
	res := marotte.EntryToolResult{Status: marotte.ToolCompleted, Output: "done"}
	if err := s.Append(t.Context(), "c1", entryOf(turn, "", marotte.ToolResultID(call.ID), marotte.EntryKindToolResult, res)); err != nil {
		t.Fatalf("Setup: append tool_result: %v", err)
	}

	page := getPage(t, s, "c1", "")

	var gotCall marotte.EntryToolCall
	servedTool(t, page.Entries, marotte.EntryKindToolCall, &gotCall)
	if gotCall.CardTitle != "Loaded skill: land" || !slices.Equal(gotCall.Loaded, landLoaded) {
		t.Errorf("served call = %q %v, want %q %v", gotCall.CardTitle, gotCall.Loaded, "Loaded skill: land", landLoaded)
	}
	if gotCall.Title != "Tool Load" {
		t.Errorf("served call title = %q, want the wire title kept", gotCall.Title)
	}
	var gotRes marotte.EntryToolResult
	servedTool(t, page.Entries, marotte.EntryKindToolResult, &gotRes)
	if gotRes.CardTitle != "Loaded skill: land" || !slices.Equal(gotRes.Loaded, landLoaded) {
		t.Errorf("served result = %q %v, want the join's %q %v", gotRes.CardTitle, gotRes.Loaded, "Loaded skill: land", landLoaded)
	}
	disk, err := os.ReadFile(filepath.Join(dir, "c1", EntriesFileName))
	if err != nil {
		t.Fatalf("read the log: %v", err)
	}
	for _, field := range []string{`"card_title"`, `"loaded"`} {
		if strings.Contains(string(disk), field) {
			t.Errorf("the record holds %s, want it derived at serve time only:\n%s", field, disk)
		}
	}
}

// The title is derived before the preview bound, so a command the preview drops from the input still titles the card.
func TestHandleChatPage_TitlesACommandThePreviewCuts(t *testing.T) {
	command := "cat <<'EOF' > out.txt " + strings.Repeat("x", 6<<10)
	input, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	call := marotte.EntryToolCall{ID: "tc1", Title: "Run Command", Kind: marotte.ToolKindExecute, Status: marotte.ToolPending, Input: input}
	got, _ := windowedCall(t, call, settled("ok\n"))
	if !got.HasFull {
		t.Fatalf("Setup: the %d-byte command was not cut from the preview", len(command))
	}
	want := "cat <<'EOF' > out.txt " + strings.Repeat("x", 97) + "…"
	if got.CardTitle != want {
		t.Errorf("card_title = %q, want the command cut at 120 units, %q", got.CardTitle, want)
	}
}

// A repair read past the call still titles the result from the join, not from the result alone.
func TestHandleTurnRange_StampsAResultWhoseCallPrecedesAfter(t *testing.T) {
	s, _ := newTestStore(t)
	turn := openPromptTurn(t, s, "c1", "m-1")
	call := loadSkillCall("tc1")
	if err := s.Append(t.Context(), "c1", entryOf(turn, "", call.ID, marotte.EntryKindToolCall, call)); err != nil {
		t.Fatalf("Setup: append tool_call: %v", err)
	}
	res := marotte.EntryToolResult{Status: marotte.ToolCompleted}
	if err := s.Append(t.Context(), "c1", entryOf(turn, "", marotte.ToolResultID(call.ID), marotte.EntryKindToolResult, res)); err != nil {
		t.Fatalf("Setup: append tool_result: %v", err)
	}

	page := decodeTurnRange(t, getTurnRangeQuery(t, s, "c1", turn, "after=1"))

	wantShapes(t, page.Entries, []string{"2:/tool_result"}, "?after=1")
	var got marotte.EntryToolResult
	servedTool(t, page.Entries, marotte.EntryKindToolResult, &got)
	if got.CardTitle != "Loaded skill: land" || !slices.Equal(got.Loaded, landLoaded) {
		t.Errorf("served result = %q %v, want the join's %q %v", got.CardTitle, got.Loaded, "Loaded skill: land", landLoaded)
	}
}

// A call id can repeat across turns; the page serves each turn's call and result from that turn alone.
func TestHandleChatPage_TurnsSharingACallIDStayApart(t *testing.T) {
	s, _ := newTestStore(t)
	appendTool := func(turn string, call marotte.EntryToolCall) {
		t.Helper()
		if err := s.Append(t.Context(), "c1", entryOf(turn, "", call.ID, marotte.EntryKindToolCall, call)); err != nil {
			t.Fatalf("Setup: append tool_call: %v", err)
		}
		res := marotte.EntryToolResult{Status: marotte.ToolCompleted}
		if err := s.Append(t.Context(), "c1", entryOf(turn, "", marotte.ToolResultID(call.ID), marotte.EntryKindToolResult, res)); err != nil {
			t.Fatalf("Setup: append tool_result: %v", err)
		}
	}
	first := openPromptTurn(t, s, "c1", "m-1")
	appendTool(first, loadSkillCall("tc1"))
	closeTurn(t, s, "c1", first, marotte.TurnOutcomeCompleted)
	second := openPromptTurn(t, s, "c1", "m-2")
	appendTool(second, marotte.EntryToolCall{
		ID: "tc1", Title: "Run Command", Kind: marotte.ToolKindExecute, Status: marotte.ToolPending,
		Input: json.RawMessage(`{"command":"make lint"}`),
	})

	page := getPage(t, s, "c1", "")

	want := map[string]string{first: "Loaded skill: land", second: "make lint"}
	wire := map[string]string{first: "Tool Load", second: "Run Command"}
	seen := 0
	for _, e := range page.Entries {
		switch e.Kind {
		case marotte.EntryKindToolCall:
			var c marotte.EntryToolCall
			if err := json.Unmarshal(e.Payload, &c); err != nil {
				t.Fatalf("decode call: %v", err)
			}
			seen++
			if c.Title != wire[e.Turn] || c.CardTitle != want[e.Turn] {
				t.Errorf("turn %s call served title %q card %q, want %q and %q", e.Turn, c.Title, c.CardTitle, wire[e.Turn], want[e.Turn])
			}
		case marotte.EntryKindToolResult:
			var r marotte.EntryToolResult
			if err := json.Unmarshal(e.Payload, &r); err != nil {
				t.Fatalf("decode result: %v", err)
			}
			seen++
			if r.CardTitle != want[e.Turn] {
				t.Errorf("turn %s result served %q, want its own turn's join %q", e.Turn, r.CardTitle, want[e.Turn])
			}
		}
	}
	if seen != 4 {
		t.Fatalf("the page serves %d tool entries, want 4", seen)
	}
}

func TestRenderChatMarkdown_TitlesAToolByItsCardTitle(t *testing.T) {
	entries, _ := chatOf(openTurn("t-1", 1, nil).
		tool(marotte.EntryToolCall{
			ID: "tc1", Title: "Run Command", Kind: marotte.ToolKindExecute, Status: marotte.ToolPending,
			Input: json.RawMessage(`{"command":"go test ./..."}`),
		}, &marotte.EntryToolResult{Status: marotte.ToolCompleted}))

	md := renderChatMarkdown(&marotte.Chat{ID: "c1", Name: "Titled"}, entries)

	if want := "<summary>Tool: go test ./... — completed</summary>"; !strings.Contains(md, want) {
		t.Errorf("markdown missing %q\n---\n%s", want, md)
	}
}

func TestRenderChatMarkdown_OrphanResultTitlesFromItself(t *testing.T) {
	tf := openTurn("t-1", 1, nil)
	tf.add("", "tc9:result", marotte.EntryKindToolResult, marotte.EntryToolResult{
		Title: "Run Command", Kind: marotte.ToolKindExecute, Status: marotte.ToolCompleted,
		Input: json.RawMessage(`{"command":"make lint"}`),
	})
	entries, _ := chatOf(tf)

	md := renderChatMarkdown(&marotte.Chat{ID: "c1", Name: "Orphan"}, entries)

	if want := "<summary>Tool result: make lint — completed</summary>"; !strings.Contains(md, want) {
		t.Errorf("markdown missing %q\n---\n%s", want, md)
	}
}

func TestHandleExport_JSONCarriesTheCardTitle(t *testing.T) {
	s := storeWith(t, loadSkillCall("tc1"), &marotte.EntryToolResult{Status: marotte.ToolCompleted})

	req := httptest.NewRequest(http.MethodGet, "/api/chats/c1/export?format=json", nil)
	rec := httptest.NewRecorder()
	newRouter(s).handleOne(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Entries []marotte.Entry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v; body = %s", err, rec.Body.String())
	}
	var call marotte.EntryToolCall
	servedTool(t, body.Entries, marotte.EntryKindToolCall, &call)
	var res marotte.EntryToolResult
	servedTool(t, body.Entries, marotte.EntryKindToolResult, &res)
	if call.CardTitle != "Loaded skill: land" || res.CardTitle != "Loaded skill: land" {
		t.Errorf("exported card_title = %q / %q, want %q on both", call.CardTitle, res.CardTitle, "Loaded skill: land")
	}
	if !slices.Equal(call.Loaded, landLoaded) {
		t.Errorf("exported loaded = %v, want %v", call.Loaded, landLoaded)
	}
}
