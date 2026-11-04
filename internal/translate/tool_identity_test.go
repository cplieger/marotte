package translate

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

const (
	toolIdentityFrames = "testdata/kas/tool_identity.jsonl"
	toolIdentityGolden = "testdata/kas/tool_identity.golden.json"
)

// toolIdentityRow is one recorded create frame. Source says where it was recorded: kas-wire is a
// verbatim ACP frame, chat-log a recorded call's title, kind and rawInput with the `_meta.kiro`
// KAS's handleToolAction stamps on that tool, kas-bundle a title taken from the pinned agent
// server where no call was recorded. Argument values are replaced; identity never reads them.
type toolIdentityRow struct {
	Name   string          `json:"name"`
	Source string          `json:"source"`
	Frame  json.RawMessage `json:"frame"`
}

// toolIdentityResult is one golden row: the tool_call entry the client is handed for that frame.
// static-src/tool-glyph.node.test.ts reads the same file.
type toolIdentityResult struct {
	Name string                `json:"name"`
	Call marotte.EntryToolCall `json:"call"`
}

func readToolIdentityRows(t *testing.T) []toolIdentityRow {
	t.Helper()
	f, err := os.Open(toolIdentityFrames)
	if err != nil {
		t.Fatalf("open %s: %v", toolIdentityFrames, err)
	}
	defer f.Close()
	var rows []toolIdentityRow
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var r toolIdentityRow
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("decode %s row %d: %v", toolIdentityFrames, len(rows)+1, err)
		}
		rows = append(rows, r)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read %s: %v", toolIdentityFrames, err)
	}
	return rows
}

func TestToolIdentity_RecordedFramesMatchTheGolden(t *testing.T) {
	var results []toolIdentityResult
	for _, r := range readToolIdentityRows(t) {
		var tc acpToolCallWire
		if err := json.Unmarshal(r.Frame, &tc); err != nil {
			t.Fatalf("decode frame %q: %v", r.Name, err)
		}
		tc.gate()
		call := toolCallFromWire(&tc, tc.Meta.Kiro.AgentSubtaskID, toolUpdateContent{}, 0)
		results = append(results, toolIdentityResult{Name: r.Name, Call: marotte.EntryToolCallOf(&call)})
	}
	got, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got = append(got, '\n')
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(filepath.Clean(toolIdentityGolden), got, 0o600); err != nil {
			t.Fatalf("write %s: %v", toolIdentityGolden, err)
		}
	}
	want, err := os.ReadFile(toolIdentityGolden)
	if err != nil {
		t.Fatalf("read %s (create it with UPDATE_GOLDEN=1 go test ./internal/translate/ -run TestToolIdentity): %v", toolIdentityGolden, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s is stale: run UPDATE_GOLDEN=1 go test ./internal/translate/ -run TestToolIdentity, review the diff, and rerun static-src/tool-glyph.node.test.ts:\n%s", toolIdentityGolden, got)
	}
}

func TestToolIdentity_RecordedFramesNameTheirTool(t *testing.T) {
	type want struct {
		tool string
		mcp  *marotte.MCPToolIdentity
	}
	wants := map[string]want{
		"read_file (wire)":                   {},
		"tool_load (wire)":                   {tool: "tool_load"},
		"MCP via tool_call (wire)":           {mcp: &marotte.MCPToolIdentity{ServerName: "capture", ToolName: "ask_value"}},
		"update_workflow via tool_call":      {tool: "update_workflow"},
		"update_workflow direct":             {tool: "update_workflow"},
		"inspect_workflow via tool_call":     {tool: "inspect_workflow"},
		"inspect_workflow direct":            {tool: "inspect_workflow"},
		"run_workflow":                       {tool: "run_workflow"},
		"send_message":                       {tool: "send_message"},
		"web_search direct":                  {tool: "web_search"},
		"web_search via tool_call":           {tool: "web_search"},
		"mailbox send_message via tool_call": {mcp: &marotte.MCPToolIdentity{ServerName: "mailbox", ToolName: "send_message"}},
		"mailbox fetch_inbox via tool_call":  {mcp: &marotte.MCPToolIdentity{ServerName: "mailbox", ToolName: "fetch_inbox"}},
		"unloaded built-in via tool_call":    {tool: "inspect_workflow"},
		"unloaded MCP tool via tool_call":    {mcp: &marotte.MCPToolIdentity{ServerName: "mailbox", ToolName: "reply_message"}},
		"tool_load":                          {tool: "tool_load"},
		"save_workflow_definition":           {tool: "save_workflow_definition"},
		"disclose_context skill":             {tool: "disclose_context"},
		"disclose_context steering":          {tool: "disclose_context"},
		"control process via tool_call":      {},
		"list_processes":                     {},
		"update_session_information":         {},
		"described shell call":               {},
		"read_file":                          {},
		"fs_write":                           {},
		"str_replace":                        {},
		"fs_append":                          {},
		"delete_file":                        {},
		"grep_search":                        {},
		"file_search":                        {},
		"list_directory":                     {},
		"web_fetch":                          {},
	}
	rows := readToolIdentityRows(t)
	if len(rows) != len(wants) {
		t.Errorf("%d recorded rows, %d expectations: every row needs one", len(rows), len(wants))
	}
	for _, r := range rows {
		t.Run(r.Name, func(t *testing.T) {
			w, ok := wants[r.Name]
			if !ok {
				t.Fatalf("no expectation for row %q", r.Name)
			}
			var tc acpToolCallWire
			if err := json.Unmarshal(r.Frame, &tc); err != nil {
				t.Fatalf("decode: %v", err)
			}
			call := toolCallFromWire(&tc, "", toolUpdateContent{}, 0)
			if call.ToolName != w.tool {
				t.Errorf("tool name = %q, want %q", call.ToolName, w.tool)
			}
			if !reflect.DeepEqual(call.MCP, w.mcp) {
				t.Errorf("mcp = %+v, want %+v", call.MCP, w.mcp)
			}
		})
	}
}

func TestMCPCallIdentity(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		server, title string
		want          *marotte.MCPToolIdentity
	}{
		"a mounted tool":                    {"probe", "@probe/echo_text", &marotte.MCPToolIdentity{ServerName: "probe", ToolName: "echo_text"}},
		"a Power route":                     {"srv", "@pw/srv/do_thing", &marotte.MCPToolIdentity{ServerName: "srv", ToolName: "do_thing"}},
		"a Power named like its server":     {"srv", "@srv/srv/do_thing", &marotte.MCPToolIdentity{ServerName: "srv", ToolName: "do_thing"}},
		"a tool name with a slash":          {"probe", "@probe/a/b", &marotte.MCPToolIdentity{ServerName: "probe", ToolName: "a/b"}},
		"no server name":                    {"", "@probe/echo_text", nil},
		"a stray server name on a built-in": {"probe", "Read File", nil},
		"a title naming another server":     {"probe", "@other/x", nil},
		"no tool half":                      {"probe", "@probe/", nil},
		"no @ prefix":                       {"probe", "probe/echo_text", nil},
		"a bidi override is defused": {
			"pro\u202ebe", "@pro\u202ebe/echo_text",
			&marotte.MCPToolIdentity{ServerName: "pro be", ToolName: "echo_text"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := mcpCallIdentity(tc.server, tc.title)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("mcpCallIdentity(%q, %q) = %+v, want %+v", tc.server, tc.title, got, tc.want)
			}
		})
	}
}

func TestBuiltinToolName(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		toolID, title, want string
	}{
		"KAS's toolId wins":                {"disclose_context", "Load skill: x", "disclose_context"},
		"a known display title":            {"", "Update Workflow", "update_workflow"},
		"an unknown title names nothing":   {"", "Kiro Powers", ""},
		"a model-composed title":           {"", "Update the plan", ""},
		"an inherited name is not a title": {"", "constructor", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := builtinToolName(tc.toolID, tc.title); got != tc.want {
				t.Errorf("builtinToolName(%q, %q) = %q, want %q", tc.toolID, tc.title, got, tc.want)
			}
		})
	}
}

func TestEnvelopeIdentity(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		rawInput string
		tool     string
		mcp      *marotte.MCPToolIdentity
	}{
		"a built-in":                        {`{"tool_id":"tool::inspect_workflow","arguments":{}}`, "inspect_workflow", nil},
		"the older built-in prefix":         {`{"tool_id":"builtin::update_workflow"}`, "update_workflow", nil},
		"an aliased built-in":               {`{"tool_id":"tool::remote_web_search"}`, "web_search", nil},
		"an undistinguished built-in":       {`{"tool_id":"tool::control_bash_process"}`, "", nil},
		"an inherited name":                 {`{"tool_id":"tool::constructor"}`, "", nil},
		"an inherited name, older prefix":   {`{"tool_id":"builtin::constructor"}`, "", nil},
		"an MCP tool":                       {`{"tool_id":"tool::mailbox::reply_message"}`, "", &marotte.MCPToolIdentity{ServerName: "mailbox", ToolName: "reply_message"}},
		"an MCP tool named like a built-in": {`{"tool_id":"tool::probe::run_workflow"}`, "", &marotte.MCPToolIdentity{ServerName: "probe", ToolName: "run_workflow"}},
		"a Power member":                    {`{"tool_id":"power::pw::tool::srv::do_thing"}`, "", &marotte.MCPToolIdentity{ServerName: "srv", ToolName: "do_thing"}},
		"a plugin member":                   {`{"tool_id":"plugin::pl::tool::srv::do_thing"}`, "", &marotte.MCPToolIdentity{ServerName: "srv", ToolName: "do_thing"}},
		"a Power member naming a built-in":  {`{"tool_id":"power::pw::tool::run_workflow"}`, "", nil},
		"a Power overview":                  {`{"tool_id":"power::pw::overview"}`, "", nil},
		"a bare Power":                      {`{"tool_id":"power::pw"}`, "", nil},
		"a skill":                           {`{"tool_id":"skill::release-notes"}`, "", nil},
		"no server half":                    {`{"tool_id":"tool::::reply_message"}`, "", nil},
		"no tool half":                      {`{"tool_id":"tool::mailbox::"}`, "", nil},
		"tool_load's id is not read":        {`{"id":"tool::inspect_workflow"}`, "", nil},
		"tool_load's tool_ids are not read": {`{"tool_ids":["tool::inspect_workflow"]}`, "", nil},
		"a non-string id":                   {`{"tool_id":7}`, "", nil},
		"no input":                          {``, "", nil},
		"a null input":                      {`null`, "", nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tool, mcp := envelopeIdentity(json.RawMessage(tc.rawInput))
			if tool != tc.tool {
				t.Errorf("tool = %q, want %q", tool, tc.tool)
			}
			if !reflect.DeepEqual(mcp, tc.mcp) {
				t.Errorf("mcp = %+v, want %+v", mcp, tc.mcp)
			}
		})
	}
}

func TestCallIdentity_Precedence(t *testing.T) {
	t.Parallel()
	envelope := json.RawMessage(`{"tool_id":"tool::probe::echo_text"}`)
	cases := map[string]struct {
		tc   acpToolCallWire
		tool string
		mcp  *marotte.MCPToolIdentity
	}{
		"an MCP title is never a built-in": {
			tc:  acpToolCallWire{Title: "@probe/run_workflow", Meta: acpKiroMeta{Kiro: acpKiroBlock{ServerName: "probe", ToolID: "run_workflow"}}},
			mcp: &marotte.MCPToolIdentity{ServerName: "probe", ToolName: "run_workflow"},
		},
		"a known title outranks the envelope": {
			tc:   acpToolCallWire{Title: "Inspect Workflow", RawInput: envelope},
			tool: "inspect_workflow",
		},
		"the envelope names what the title does not": {
			tc:  acpToolCallWire{Title: "Tool Call", RawInput: envelope},
			mcp: &marotte.MCPToolIdentity{ServerName: "probe", ToolName: "echo_text"},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tool, mcp := callIdentity(&c.tc)
			if tool != c.tool {
				t.Errorf("tool = %q, want %q", tool, c.tool)
			}
			if !reflect.DeepEqual(mcp, c.mcp) {
				t.Errorf("mcp = %+v, want %+v", mcp, c.mcp)
			}
		})
	}
}

// Updates are where KAS's raw-args error paths can carry a model-written serverName.
func TestToolCallUpdate_ServerNameIsNotAdopted(t *testing.T) {
	tr, _, deps, events, chatID := primeToolCall(t)
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"title":      "@x/readFile",
		"status":     "in_progress",
		"_meta":      map[string]any{"kiro": map[string]any{"serverName": "x"}},
	}), FrameAttribution{})
	if _, ok := lastToolCallUpdate(t, deps, events); !ok {
		t.Fatal("no tool_progress frame emitted")
	}
	open, ok := deps.turns.chats["c1"].OpenCallFor("tc-1")
	if !ok {
		t.Fatal("the turn holds no open tool call tc-1")
	}
	if open.Call.MCP != nil {
		t.Errorf("open call MCP after an update carrying serverName = %+v, want none", open.Call.MCP)
	}
}
