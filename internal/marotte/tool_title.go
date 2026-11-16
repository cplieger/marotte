package marotte

import (
	"encoding/json"
	"strings"
	"unicode/utf16"
)

// ToolCardTitle is the title a tool card draws, the find index files and the export prints.
// It is derived from the call's stored fields whenever the call is served and is never stored,
// so a rule change reaches every existing chat.
func ToolCardTitle(tc *ToolCall) string {
	if tc.MCP != nil {
		return strings.ReplaceAll(tc.MCP.ToolName, "_", " ")
	}
	if cmd, ok := commandTitle(tc); ok {
		return cmd
	}
	return titleText(tc.Title)
}

// placeholderShellTitle is KAS's execute_bash title when the model sent no description.
const placeholderShellTitle = "Run Command"

// titleUnits bounds a title built from input the model wrote (a command, a list of loaded ids).
const titleUnits = 120

// commandTitle is a description-less shell call's command on one line, never humanized; false
// when the call is not KAS's placeholder or carries no command.
func commandTitle(tc *ToolCall) (string, bool) {
	if tc.Kind != ToolKindExecute || tc.Title != placeholderShellTitle {
		return "", false
	}
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(tc.Input, &in) != nil {
		return "", false
	}
	line := strings.Join(strings.Fields(in.Command), " ")
	if line == "" {
		return "", false
	}
	return cutUnits(line, titleUnits), true
}

// titleText shows a model-written title as written, since KAS's shell titles are sentences full
// of paths and flags; only a bare tool id is humanized.
func titleText(raw string) string {
	t := strings.TrimPrefix(raw, "Running: ")
	if !isBareToolID(t) {
		return t
	}
	return strings.Map(func(r rune) rune {
		if r == '-' || r == '_' {
			return ' '
		}
		return r
	}, t)
}

func isBareToolID(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// cutUnits bounds s to limit UTF-16 units, the last an ellipsis, counted as the browser counts
// the drawn text. A rune that would straddle the cut is dropped whole.
func cutUnits(s string, limit int) string {
	if unitLen(s) <= limit {
		return s
	}
	n := 0
	for i, r := range s {
		w := utf16.RuneLen(r)
		if n+w > limit-1 {
			return s[:i] + "…"
		}
		n += w
	}
	return s
}

// unitLen is s's length in UTF-16 units; ranging a string yields only encodable runes (an
// invalid byte decodes to U+FFFD), so RuneLen is never -1 here.
func unitLen(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}
