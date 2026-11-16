package marotte

import (
	"encoding/json"
	"strings"
	"testing"
)

func assertCardTitle(t *testing.T, tc *ToolCall, want string) {
	t.Helper()
	if got := ToolCardTitle(tc); got != want {
		t.Errorf("ToolCardTitle(title %q, kind %q) = %q, want %q", tc.Title, tc.Kind, got, want)
	}
}

// KAS titles a described shell call with the model's own sentence, full of paths and flags,
// so only a bare tool id is humanized.
func TestToolCardTitle_TitleText(t *testing.T) {
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
func TestToolCardTitle_Command(t *testing.T) {
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
func TestToolCardTitle_MCP(t *testing.T) {
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
