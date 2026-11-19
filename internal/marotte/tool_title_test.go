package marotte

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func assertCardTitle(t *testing.T, tc *ToolCall, want string) {
	t.Helper()
	if got, _ := ToolCard(tc); got != want {
		t.Errorf("ToolCard(title %q, kind %q) title = %q, want %q", tc.Title, tc.Kind, got, want)
	}
}

// KAS titles a described shell call with the model's own sentence, full of paths and flags,
// so only a bare tool id is humanized.
func TestToolCard_TitleText(t *testing.T) {
	cases := []struct{ name, title, want string }{
		{"snake id", "web_search", "web search"},
		{"longer snake id", "remote_web_search", "remote web search"},
		{"running prefix on an id", "Running: read_files", "read files"},
		{"running prefix on prose", "Running: Check vet output", "Check vet output"},
		{"prose with a path", "Run tests in ./internal/foo_bar", "Run tests in ./internal/foo_bar"},
		{"prose with a flag", "Run go test with --dry-run", "Run go test with --dry-run"},
		{"hyphenated file", "Check marotte-acp.md", "Check marotte-acp.md"},
		{"dotted file", "foo_bar.go", "foo_bar.go"},
		{"placeholder on another kind", "Run Command", "Run Command"},
		{"hook title", "Hook fired: lint-on-save", "Hook fired: lint-on-save"},
		{"bare running prefix", "Running: ", ""},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertCardTitle(t, &ToolCall{Title: c.title, Kind: ToolKindOther}, c.want)
		})
	}
}

// cutUnits counts UTF-16 code units, as the browser measures the text the card draws.
func TestCutUnits(t *testing.T) {
	const astral = "\U0001F600" // two UTF-16 units
	cases := []struct {
		name, in, want string
	}{
		{"under the limit", "abc", "abc"},
		{"exactly the limit", strings.Repeat("a", 120), strings.Repeat("a", 120)},
		{"one unit over", strings.Repeat("a", 121), strings.Repeat("a", 119) + "…"},
		{"astral fits whole before the cut", strings.Repeat("a", 117) + astral + "bb", strings.Repeat("a", 117) + astral + "…"},
		{"astral straddles the cut", strings.Repeat("a", 118) + astral + "b", strings.Repeat("a", 118) + "…"},
		{"astral as the last unit pair at the limit", strings.Repeat("a", 118) + astral, strings.Repeat("a", 118) + astral},
		{"multibyte BMP counts one unit", strings.Repeat("é", 120), strings.Repeat("é", 120)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cutUnits(c.in, 120); got != c.want {
				t.Errorf("cutUnits(%q, 120) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// KAS sends the static "Run Command" whenever the model wrote no description, which told a
// reader nothing, so such a card is titled by its command.
func TestToolCard_Command(t *testing.T) {
	heredoc := `{"command":"cat <<'EOF' > out.txt\n` + strings.Repeat("x", 300) + `\nEOF\n"}`
	cases := []struct {
		name, title string
		kind        ToolKind
		input       string
		want        string
	}{
		{"placeholder with a command", "Run Command", ToolKindExecute, `{"command":"go test ./..."}`, "go test ./..."},
		{"hyphens and underscores stay", "Run Command", ToolKindExecute, `{"command":"go test -run Test_parse ./internal/x-y"}`, "go test -run Test_parse ./internal/x-y"},
		{"whitespace collapses and trims", "Run Command", ToolKindExecute, `{"command":"  make\n\tlint  "}`, "make lint"},
		{"long command is cut", "Run Command", ToolKindExecute, `{"command":"` + strings.Repeat("a", 200) + `"}`, strings.Repeat("a", 119) + "…"},
		{"heredoc collapses to one cut line", "Run Command", ToolKindExecute, heredoc, "cat <<'EOF' > out.txt " + strings.Repeat("x", 97) + "…"},
		{"described title wins", "Check vet output", ToolKindExecute, `{"command":"go vet ./..."}`, "Check vet output"},
		{"described prose stays verbatim", "Run go test with --dry-run in ./internal/foo_bar", ToolKindExecute, `{"command":"go test --dry-run ./internal/foo_bar"}`, "Run go test with --dry-run in ./internal/foo_bar"},
		{"placeholder on a non-execute kind", "Run Command", ToolKindOther, `{"command":"go vet ./..."}`, "Run Command"},
		{"no input", "Run Command", ToolKindExecute, "", "Run Command"},
		{"null input", "Run Command", ToolKindExecute, `null`, "Run Command"},
		{"string input", "Run Command", ToolKindExecute, `"go vet"`, "Run Command"},
		{"non-string command", "Run Command", ToolKindExecute, `{"command":7}`, "Run Command"},
		{"blank command", "Run Command", ToolKindExecute, `{"command":" \n "}`, "Run Command"},
		{"hook title", "Hook fired: lint-on-save", ToolKindHook, "", "Hook fired: lint-on-save"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tc := &ToolCall{Title: c.title, Kind: c.kind}
			if c.input != "" {
				tc.Input = json.RawMessage(c.input)
			}
			assertCardTitle(t, tc, c.want)
		})
	}
}

// An MCP card is titled by the tool's own name, whatever title KAS sent.
func TestToolCard_MCP(t *testing.T) {
	cases := []struct{ tool, want string }{
		{"create_issue", "create issue"},
		{"list_buckets", "list buckets"},
		{"tool", "tool"},
		{"", ""},
		{"a_b_c", "a b c"},
		{"_leading", " leading"},
		{"trailing_", "trailing "},
		{"fetch-inbox", "fetch-inbox"},
	}
	for _, c := range cases {
		t.Run(c.tool, func(t *testing.T) {
			tc := &ToolCall{
				Title: "Running: github_create_issue",
				Kind:  ToolKindOther,
				MCP:   &MCPToolIdentity{ServerName: "my_server", ToolName: c.tool},
			}
			assertCardTitle(t, tc, c.want)
		})
	}
}

func loadCall(toolName, input string) *ToolCall {
	tc := &ToolCall{Title: "Load Resource", Kind: ToolKindOther, ToolName: toolName}
	if input != "" {
		tc.Input = json.RawMessage(input)
	}
	return tc
}

func assertLoaded(t *testing.T, tc *ToolCall, want []LoadedResource) {
	t.Helper()
	_, got := ToolCard(tc)
	if !slices.Equal(got, want) {
		t.Errorf("ToolCard(%s %s) loaded = %+v, want %+v", tc.ToolName, tc.Input, got, want)
	}
}

// KAS's resource-id grammar, one case per prefix the load tools accept, plus ids outside it.
func TestToolCard_LoadedResourceID(t *testing.T) {
	cases := []struct {
		id        string
		typ       LoadedType
		name, srv string
	}{
		{"tool::agent-mail::fetch_inbox", loadedMCP, "fetch_inbox", "agent-mail"},
		{"tool::inspect_workflow", loadedTool, "inspect_workflow", ""},
		{"builtin::inspect_workflow", loadedTool, "inspect_workflow", ""},
		{"skill::land", LoadedSkill, "land", ""},
		{"steering::code-style", loadedSteering, "code-style", ""},
		{"power::aws::overview", loadedOverview, "aws", ""},
		{"power::aws::tool::docs::search", loadedMCP, "search", "docs"},
		{"power::aws::steering::guide", loadedSteering, "guide", ""},
		{"plugin::kit::skill::release", LoadedSkill, "release", ""},
		{"plugin::kit::tool::srv::do", loadedMCP, "do", "srv"},
		{"tool::mailbox::", loadedUnknown, "tool::mailbox::", ""},
		{"tool::::x", loadedUnknown, "tool::::x", ""},
		{"power::aws", loadedUnknown, "power::aws", ""},
		{"plugin::::skill::x", loadedUnknown, "plugin::::skill::x", ""},
		{"memory::x", loadedUnknown, "memory::x", ""},
		{"skill::team::land", LoadedSkill, "team::land", ""},
		{"steering::web::css", loadedSteering, "web::css", ""},
		{"skill::", loadedUnknown, "skill::", ""},
		{"power::aws::skill::a::b", LoadedSkill, "a::b", ""},
		{"plugin::kit::steering::a::b", loadedSteering, "a::b", ""},
		{"power::a::b::skill::x", loadedUnknown, "power::a::b::skill::x", ""},
		{"plugin::a::b::steering::x", loadedUnknown, "plugin::a::b::steering::x", ""},
		// load_resource's id is a catalogue id, where a bare two-part id is no tool.
		{"agent-mail::fetch_inbox", loadedUnknown, "agent-mail::fetch_inbox", ""},
		{"tool", loadedUnknown, "tool", ""},
	}
	for _, c := range cases {
		t.Run(strings.ReplaceAll(c.id, "/", "_"), func(t *testing.T) {
			in, _ := json.Marshal(map[string]string{"id": c.id})
			assertLoaded(t, loadCall("load_resource", string(in)),
				[]LoadedResource{{ID: c.id, Type: c.typ, Name: c.name, Server: c.srv}})
		})
	}
}

func TestToolCard_LoadedCalls(t *testing.T) {
	skill := func(id, name string) LoadedResource {
		return LoadedResource{ID: id, Type: LoadedSkill, Name: name}
	}
	t.Run("tool_ids skip non-strings and empties", func(t *testing.T) {
		assertLoaded(t, loadCall("tool_load", `{"tool_ids":["skill::a",7,"","tool::b",null]}`),
			[]LoadedResource{skill("skill::a", "a"), {ID: "tool::b", Type: loadedTool, Name: "b"}})
	})
	// tool_load's tool_ids schema: `builtin::<tool_name>` or `<server_name>::<tool_name>`.
	t.Run("tool_ids server pairs are MCP tools", func(t *testing.T) {
		assertLoaded(t, loadCall("tool_load",
			`{"tool_ids":["agent-mail::fetch_inbox","agent-mail::send_message","builtin::control_process"]}`),
			[]LoadedResource{
				{ID: "agent-mail::fetch_inbox", Type: loadedMCP, Name: "fetch_inbox", Server: "agent-mail"},
				{ID: "agent-mail::send_message", Type: loadedMCP, Name: "send_message", Server: "agent-mail"},
				{ID: "builtin::control_process", Type: loadedTool, Name: "control_process"},
			})
	})
	t.Run("a non-string id falls to tool_ids", func(t *testing.T) {
		assertLoaded(t, loadCall("tool_load", `{"id":7,"tool_ids":["skill::a"]}`),
			[]LoadedResource{skill("skill::a", "a")})
	})
	t.Run("a null id falls to tool_ids", func(t *testing.T) {
		assertLoaded(t, loadCall("tool_load", `{"id":null,"tool_ids":["skill::a"]}`),
			[]LoadedResource{skill("skill::a", "a")})
	})
	t.Run("an empty id loads nothing, whatever tool_ids holds", func(t *testing.T) {
		assertLoaded(t, loadCall("tool_load", `{"id":"","tool_ids":["skill::a"]}`), nil)
	})
	// KAS 3.3.0 calls disclose_context itself, with the id load_resource takes.
	t.Run("disclose_context id skill", func(t *testing.T) {
		assertLoaded(t, loadCall("disclose_context", `{"id":"skill::sync-clones"}`),
			[]LoadedResource{skill("skill::sync-clones", "sync-clones")})
	})
	t.Run("disclose_context id steering", func(t *testing.T) {
		assertLoaded(t, loadCall("disclose_context", `{"id":"steering::git-commits"}`),
			[]LoadedResource{{ID: "steering::git-commits", Type: loadedSteering, Name: "git-commits"}})
	})
	for _, c := range []struct{ name, tool, input string }{
		{"not a load tool", "read_file", `{"id":"skill::a"}`},
		{"no input", "load_resource", ""},
		{"an empty object", "load_resource", `{}`},
		{"empty tool_ids", "tool_load", `{"tool_ids":[]}`},
		{"tool_ids not an array", "tool_load", `{"tool_ids":"skill::a"}`},
		{"input not an object", "load_resource", `["skill::a"]`},
		{"an older disclose_context with only a name", "disclose_context", `{"name":"release-notes"}`},
	} {
		t.Run(c.name, func(t *testing.T) { assertLoaded(t, loadCall(c.tool, c.input), nil) })
	}
}

// An older disclose_context sends `{name}`; the document's kind arrives only with KAS's
// disclosedContext.
func TestToolCard_LoadedDisclosed(t *testing.T) {
	disclosed := func(typ, name string) *ToolDisclosed {
		return &ToolDisclosed{Type: typ, DisplayName: name, URI: "file:///x/" + name}
	}
	t.Run("skill", func(t *testing.T) {
		tc := loadCall("disclose_context", `{"name":"land"}`)
		tc.Disclosed = disclosed("skill", "land")
		assertLoaded(t, tc, []LoadedResource{{ID: "skill::land", Type: LoadedSkill, Name: "land"}})
	})
	t.Run("steering", func(t *testing.T) {
		tc := loadCall("disclose_context", `{"name":"marotte"}`)
		tc.Disclosed = disclosed("steering", "marotte")
		assertLoaded(t, tc, []LoadedResource{{ID: "steering::marotte", Type: loadedSteering, Name: "marotte"}})
	})
	t.Run("a type KAS does not define loads nothing", func(t *testing.T) {
		tc := loadCall("", `{"name":"x"}`)
		tc.Disclosed = disclosed("memory", "x")
		assertLoaded(t, tc, nil)
	})
	t.Run("an id the call sent wins", func(t *testing.T) {
		tc := loadCall("disclose_context", `{"id":"skill::land"}`)
		tc.Disclosed = disclosed("skill", "Land")
		assertLoaded(t, tc, []LoadedResource{{ID: "skill::land", Type: LoadedSkill, Name: "land"}})
	})
}

// A load card is titled by what it loaded, not the tool that loaded it.
func TestToolCard_LoadClaim(t *testing.T) {
	cases := []struct{ id, want string }{
		{"tool::agent-mail::fetch_inbox", "Loaded agent-mail fetch_inbox"},
		{"tool::inspect_workflow", "Loaded tool: inspect_workflow"},
		{"skill::land", "Loaded skill: land"},
		{"steering::code-style", "Loaded steering: code-style"},
		{"power::aws::overview", "Loaded power overview: aws"},
		{"power::aws::tool::docs::search_docs", "Loaded docs search_docs"},
		{"plugin::kit::skill::release", "Loaded skill: release"},
		{"plugin::kit::steering::style", "Loaded steering: style"},
		{"memory::x", "Loaded memory::x"},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			in, _ := json.Marshal(map[string]string{"id": c.id})
			assertCardTitle(t, loadCall("load_resource", string(in)), c.want)
		})
	}
	t.Run("several tool_ids joined", func(t *testing.T) {
		assertCardTitle(t, loadCall("tool_load", `{"tool_ids":["builtin::grep","srv::fetch"]}`),
			"Loaded tool: grep, srv fetch")
	})
	t.Run("a long claim is cut at 120 units", func(t *testing.T) {
		ids := make([]string, 12)
		for i := range ids {
			ids[i] = "skill::release-notes"
		}
		in, _ := json.Marshal(map[string][]string{"tool_ids": ids})
		full := "Loaded " + strings.TrimSuffix(strings.Repeat("skill: release-notes, ", 12), ", ")
		assertCardTitle(t, loadCall("tool_load", string(in)), full[:119]+"…")
	})
	t.Run("a load naming nothing keeps its title", func(t *testing.T) {
		assertCardTitle(t, loadCall("load_resource", `{}`), "Load Resource")
	})
}

// A disclose_context card names the document KAS disclosed, by its display name, whatever id
// the call sent.
func TestToolCard_Disclosed(t *testing.T) {
	cases := []struct{ name, typ, display, want string }{
		{"skill", "skill", "Land", "Loaded skill: Land"},
		{"steering", "steering", "Git commits", "Loaded steering: Git commits"},
		{"a type KAS does not define reads as a skill", "memory", "x", "Loaded skill: x"},
		{"a long name is not cut", "skill", strings.Repeat("n", 130), "Loaded skill: " + strings.Repeat("n", 130)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tc := loadCall("disclose_context", `{"id":"skill::land"}`)
			tc.Disclosed = &ToolDisclosed{Type: c.typ, DisplayName: c.display, URI: "file:///x"}
			assertCardTitle(t, tc, c.want)
		})
	}
}

func TestToolCard_ArmOrder(t *testing.T) {
	shell := `{"command":"go test ./..."}`
	t.Run("disclosed beats loaded", func(t *testing.T) {
		tc := loadCall("load_resource", `{"id":"tool::inspect_workflow"}`)
		tc.Disclosed = &ToolDisclosed{Type: "steering", DisplayName: "marotte"}
		assertCardTitle(t, tc, "Loaded steering: marotte")
	})
	t.Run("loaded beats mcp", func(t *testing.T) {
		tc := loadCall("tool_load", `{"tool_ids":["srv::fetch"]}`)
		tc.MCP = &MCPToolIdentity{ServerName: "srv", ToolName: "tool_load"}
		assertCardTitle(t, tc, "Loaded srv fetch")
	})
	t.Run("mcp beats command", func(t *testing.T) {
		tc := &ToolCall{
			Title: placeholderShellTitle,
			Kind:  ToolKindExecute,
			Input: json.RawMessage(shell),
			MCP:   &MCPToolIdentity{ServerName: "srv", ToolName: "run_shell"},
		}
		assertCardTitle(t, tc, "run shell")
	})
	t.Run("command beats title", func(t *testing.T) {
		tc := &ToolCall{Title: placeholderShellTitle, Kind: ToolKindExecute, Input: json.RawMessage(shell)}
		assertCardTitle(t, tc, "go test ./...")
	})
	t.Run("a disclose_context id with no disclosed block", func(t *testing.T) {
		tc := loadCall("disclose_context", `{"id":"skill::x"}`)
		tc.Title = "Disclose Context"
		assertCardTitle(t, tc, "Loaded skill: x")
	})
}

func toolEntry(t *testing.T, id string, kind EntryKind, payload any) Entry {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return Entry{ID: id, Turn: "t1", Kind: kind, Payload: raw}
}

func servedCall(t *testing.T, e Entry) EntryToolCall {
	t.Helper()
	var c EntryToolCall
	if err := json.Unmarshal(e.Payload, &c); err != nil {
		t.Fatalf("decode %s: %v", e.ID, err)
	}
	return c
}

func servedResult(t *testing.T, e Entry) EntryToolResult {
	t.Helper()
	var r EntryToolResult
	if err := json.Unmarshal(e.Payload, &r); err != nil {
		t.Fatalf("decode %s: %v", e.ID, err)
	}
	return r
}

func TestServeToolEntries_StampsCallsAndJoinsResults(t *testing.T) {
	entries := []Entry{
		toolEntry(t, "tc-load", EntryKindToolCall, EntryToolCall{
			ID: "tc-load", Title: "Tool Load", Kind: ToolKindOther, Status: ToolPending,
			ToolName: "tool_load", Input: json.RawMessage(`{"id":"skill::land"}`),
		}),
		toolEntry(t, "tc-disclose", EntryKindToolCall, EntryToolCall{
			ID: "tc-disclose", Title: "Disclose Context", Kind: ToolKindOther, Status: ToolPending,
			ToolName: "disclose_context", Input: json.RawMessage(`{"name":"release-notes"}`),
		}),
		toolEntry(t, "tc-load:result", EntryKindToolResult, EntryToolResult{Title: "Tool Load", Status: ToolCompleted}),
		toolEntry(t, "tc-disclose:result", EntryKindToolResult, EntryToolResult{
			Title: "Disclose Context", Status: ToolCompleted,
			Disclosed: &ToolDisclosed{Type: "skill", DisplayName: "release-notes"},
		}),
	}
	ServeToolEntries(entries, nil)

	land := []LoadedResource{{ID: "skill::land", Type: LoadedSkill, Name: "land"}}
	if c := servedCall(t, entries[0]); c.CardTitle != "Loaded skill: land" || !slices.Equal(c.Loaded, land) {
		t.Errorf("load call served %q %+v, want %q %+v", c.CardTitle, c.Loaded, "Loaded skill: land", land)
	}
	if c := servedCall(t, entries[1]); c.CardTitle != "Disclose Context" || c.Loaded != nil {
		t.Errorf("disclose call served %q %+v, want its own title and nothing loaded", c.CardTitle, c.Loaded)
	}
	if r := servedResult(t, entries[2]); r.CardTitle != "Loaded skill: land" || !slices.Equal(r.Loaded, land) {
		t.Errorf("load result served %q %+v, want its call's join %q %+v", r.CardTitle, r.Loaded, "Loaded skill: land", land)
	}
	notes := []LoadedResource{{ID: "skill::release-notes", Type: LoadedSkill, Name: "release-notes"}}
	if r := servedResult(t, entries[3]); r.CardTitle != "Loaded skill: release-notes" || !slices.Equal(r.Loaded, notes) {
		t.Errorf("disclose result served %q %+v, want %q %+v", r.CardTitle, r.Loaded, "Loaded skill: release-notes", notes)
	}
}

func TestServeToolEntries_ContextSuppliesTheCall(t *testing.T) {
	context := []Entry{toolEntry(t, "tc-1", EntryKindToolCall, EntryToolCall{
		ID: "tc-1", Title: "Run Command", Kind: ToolKindExecute, Status: ToolPending,
		Input: json.RawMessage(`{"command":"go test ./..."}`),
	})}
	stored := toolEntry(t, "tc-1:result", EntryKindToolResult, EntryToolResult{Title: "Run Command", Status: ToolCompleted})
	entries := []Entry{stored}
	ServeToolEntries(entries, context)
	if r := servedResult(t, entries[0]); r.CardTitle != "go test ./..." {
		t.Errorf("result served %q, want the context call's command", r.CardTitle)
	}
	if c := servedCall(t, context[0]); c.CardTitle != "" {
		t.Errorf("context call stamped %q, want it untouched", c.CardTitle)
	}

	alone := []Entry{stored}
	ServeToolEntries(alone, nil)
	if r := servedResult(t, alone[0]); r.CardTitle != "Run Command" {
		t.Errorf("result with no call served %q, want its own title", r.CardTitle)
	}
}

// A call id can repeat across turns; each entry keeps its own payload and a result joins only
// its own turn's call.
func TestServeToolEntries_TurnsSharingACallIDStayApart(t *testing.T) {
	inTurn := func(e Entry, turn string) Entry { e.Turn = turn; return e }
	entries := []Entry{
		inTurn(toolEntry(t, "tc-1", EntryKindToolCall, EntryToolCall{
			ID: "tc-1", Title: "Tool Load", Kind: ToolKindOther, Status: ToolPending,
			ToolName: "tool_load", Input: json.RawMessage(`{"id":"skill::land"}`),
		}), "t1"),
		inTurn(toolEntry(t, "tc-1:result", EntryKindToolResult, EntryToolResult{Status: ToolCompleted}), "t1"),
		inTurn(toolEntry(t, "tc-1", EntryKindToolCall, EntryToolCall{
			ID: "tc-1", Title: "Run Command", Kind: ToolKindExecute, Status: ToolPending,
			Input: json.RawMessage(`{"command":"make lint"}`),
		}), "t2"),
		inTurn(toolEntry(t, "tc-1:result", EntryKindToolResult, EntryToolResult{Status: ToolCompleted}), "t2"),
	}
	ServeToolEntries(entries, nil)

	if c := servedCall(t, entries[0]); c.Title != "Tool Load" || c.CardTitle != "Loaded skill: land" {
		t.Errorf("t1 call served title %q card %q, want its own %q and %q", c.Title, c.CardTitle, "Tool Load", "Loaded skill: land")
	}
	if r := servedResult(t, entries[1]); r.CardTitle != "Loaded skill: land" {
		t.Errorf("t1 result served %q, want its own turn's join %q", r.CardTitle, "Loaded skill: land")
	}
	if c := servedCall(t, entries[2]); c.Title != "Run Command" || c.CardTitle != "make lint" {
		t.Errorf("t2 call served title %q card %q, want %q and %q", c.Title, c.CardTitle, "Run Command", "make lint")
	}
	if r := servedResult(t, entries[3]); r.CardTitle != "make lint" {
		t.Errorf("t2 result served %q, want %q", r.CardTitle, "make lint")
	}
}

func TestServeToolEntries_LeavesOtherKindsByteEqual(t *testing.T) {
	text := toolEntry(t, "x-1", EntryKindText, EntryText{Text: "Run Command"})
	broken := Entry{ID: "tc-2", Turn: "t1", Kind: EntryKindToolCall, Payload: json.RawMessage(`{"id":`)}
	untitled := toolEntry(t, "tc-3", EntryKindToolCall, EntryToolCall{ID: "tc-3", Status: ToolPending})
	entries := []Entry{text, broken, untitled}
	ServeToolEntries(entries, nil)
	for i, want := range []Entry{text, broken, untitled} {
		if string(entries[i].Payload) != string(want.Payload) {
			t.Errorf("entry %s payload = %s, want it byte-equal %s", want.ID, entries[i].Payload, want.Payload)
		}
	}
}

func TestSettledToolCall_ResultOverlaysTheTitleFields(t *testing.T) {
	call := &EntryToolCall{
		ID: "tc-1", Title: "Run Command", Kind: ToolKindExecute, ToolName: "execute_bash",
		Input: json.RawMessage(`{}`),
	}
	res := &EntryToolResult{
		Title: "Run Command", Kind: ToolKindExecute, Input: json.RawMessage(`{"command":"ls"}`),
		Disclosed: &ToolDisclosed{Type: "skill", DisplayName: "x"},
	}
	got := SettledToolCall(call, res)
	if got.ID != "tc-1" || got.ToolName != "execute_bash" || string(got.Input) != `{"command":"ls"}` || got.Disclosed != res.Disclosed {
		t.Errorf("SettledToolCall = %+v, want the call's identity under the result's input and disclosed", got)
	}
	kept := SettledToolCall(call, &EntryToolResult{Status: ToolCompleted})
	if kept.Title != "Run Command" || kept.Kind != ToolKindExecute || string(kept.Input) != `{}` {
		t.Errorf("SettledToolCall with an empty result = %+v, want the call's title, kind and input", kept)
	}
	if alone := SettledToolCall(nil, res); alone.Title != "Run Command" || alone.ToolName != "" {
		t.Errorf("SettledToolCall(nil, res) = %+v, want the result's fields alone", alone)
	}
}

// The converters build the stored record, which must never carry a derived title.
func TestEntryToolCallOf_NeverCarriesCardTitle(t *testing.T) {
	tc := ToolCall{ID: "tc-1", CardTitle: "drawn", Loaded: []LoadedResource{{ID: "skill::x"}}}
	if c := EntryToolCallOf(&tc); c.CardTitle != "" || c.Loaded != nil {
		t.Errorf("EntryToolCallOf copied %q %+v", c.CardTitle, c.Loaded)
	}
	e := EntryToolCall{ID: "tc-1", CardTitle: "drawn", Loaded: []LoadedResource{{ID: "skill::x"}}}
	if back := ToolCallOfEntry(&e); back.CardTitle != "" || back.Loaded != nil {
		t.Errorf("ToolCallOfEntry copied %q %+v", back.CardTitle, back.Loaded)
	}
}

func TestEntryToolResultOf_NeverCarriesCardTitle(t *testing.T) {
	tc := ToolCall{ID: "tc-1", CardTitle: "drawn", Loaded: []LoadedResource{{ID: "skill::x"}}}
	if r := EntryToolResultOf(&tc); r.CardTitle != "" || r.Loaded != nil {
		t.Errorf("EntryToolResultOf copied %q %+v", r.CardTitle, r.Loaded)
	}
}

func TestToolCallIDOfResult(t *testing.T) {
	if got := ToolCallIDOfResult(ToolResultID("tc-1")); got != "tc-1" {
		t.Errorf("ToolCallIDOfResult(ToolResultID(tc-1)) = %q", got)
	}
}
