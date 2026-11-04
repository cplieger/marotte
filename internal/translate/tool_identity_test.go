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
		"unloaded built-in via tool_call":    {},
		"unloaded MCP tool via tool_call":    {},
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
	mcp := &marotte.MCPToolIdentity{ServerName: "probe", ToolName: "run_workflow"}
	cases := map[string]struct {
		toolID, title string
		mcp           *marotte.MCPToolIdentity
		want          string
	}{
		"KAS's toolId wins":                {"disclose_context", "Load skill: x", nil, "disclose_context"},
		"a known display title":            {"", "Update Workflow", nil, "update_workflow"},
		"an unknown title names nothing":   {"", "Kiro Powers", nil, ""},
		"a model-composed title":           {"", "Update the plan", nil, ""},
		"an MCP call is never a built-in":  {"", "Run Workflow", mcp, ""},
		"an inherited name is not a title": {"", "constructor", nil, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := builtinToolName(tc.toolID, tc.title, tc.mcp); got != tc.want {
				t.Errorf("builtinToolName(%q, %q) = %q, want %q", tc.toolID, tc.title, got, tc.want)
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
