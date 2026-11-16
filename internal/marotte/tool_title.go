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
	if d := tc.Disclosed; d != nil {
		if d.Type == string(LoadedSteering) {
			return "Loaded steering: " + d.DisplayName
		}
		return "Loaded skill: " + d.DisplayName
	}
	if loaded := ToolLoaded(tc); loaded != nil {
		return loadedTitle(loaded)
	}
	if tc.MCP != nil {
		return strings.ReplaceAll(tc.MCP.ToolName, "_", " ")
	}
	if cmd, ok := commandTitle(tc); ok {
		return cmd
	}
	return titleText(tc.Title)
}

// LoadedType is what one id a load call named is.
type LoadedType string

// The LoadedType values, one per kind of thing KAS's resource-id grammar names.
const (
	LoadedMCP      LoadedType = "mcp"
	LoadedTool     LoadedType = "tool"
	LoadedSkill    LoadedType = "skill"
	LoadedSteering LoadedType = "steering"
	LoadedOverview LoadedType = "overview"
	// LoadedUnknown is an id outside KAS's resource-id grammar; its Name is the id.
	LoadedUnknown LoadedType = "unknown"
)

// LoadedResource is one resource a load call loaded.
type LoadedResource struct {
	// ID is the id as the call sent it, or `<type>::<display_name>` for a disclose_context that
	// sent none and is known only by its disclosed block.
	ID   string     `json:"id"`
	Type LoadedType `json:"type"`
	// Name is the tool, skill, document or Power name.
	Name string `json:"name"`
	// Server is a LoadedMCP resource's server, else empty.
	Server string `json:"server"`
}

// ToolLoaded is what a load call loaded, derived alongside ToolCardTitle; nil for a call that
// is not a load or names nothing yet.
func ToolLoaded(tc *ToolCall) []LoadedResource {
	if loaded := loadedIDs(tc); loaded != nil {
		return loaded
	}
	if d := tc.Disclosed; d != nil && (d.Type == string(LoadedSkill) || d.Type == string(LoadedSteering)) {
		return []LoadedResource{{ID: d.Type + "::" + d.DisplayName, Type: LoadedType(d.Type), Name: d.DisplayName}}
	}
	return nil
}

// loadTools load a resource by id; load_resource replaces the other two under KAS's
// consolidate_disclosures experiment.
var loadTools = map[string]bool{"tool_load": true, "disclose_context": true, "load_resource": true}

// loadedIDs reads a load call's `id`, else an older call's `tool_ids`. A present `id` decides
// alone, so an empty one loads nothing whatever tool_ids holds.
func loadedIDs(tc *ToolCall) []LoadedResource {
	if !loadTools[tc.ToolName] {
		return nil
	}
	var in map[string]any
	if json.Unmarshal(tc.Input, &in) != nil {
		return nil
	}
	if id, ok := in["id"].(string); ok {
		if id == "" {
			return nil
		}
		return []LoadedResource{parseResourceID(id)}
	}
	many, _ := in["tool_ids"].([]any)
	var out []LoadedResource
	for _, v := range many {
		if id, ok := v.(string); ok && id != "" {
			out = append(out, parseToolLoadID(id))
		}
	}
	return out
}

// parseResourceID reads one KAS resource id: `tool::<server>::<tool>`, `tool::<name>`, the older
// `builtin::<name>`, `skill::<name>`, `steering::<name>`, `power::<name>::overview`, and a Power
// or plugin member, `<power|plugin>::<name>::` before one of the others. As KAS reads them, a
// skill or steering name runs to the id's end, `::` included, and a Power or plugin name ends at
// its first `::`, so a member of a name holding one parses unknown.
func parseResourceID(id string) LoadedResource {
	parts := strings.Split(id, "::")
	if len(parts) == 3 && parts[0] == "power" && parts[1] != "" && parts[2] == "overview" {
		return LoadedResource{ID: id, Type: LoadedOverview, Name: parts[1]}
	}
	if (parts[0] == "power" || parts[0] == "plugin") && len(parts) >= 3 && parts[1] != "" {
		parts = parts[2:]
	}
	if r, ok := parseMemberID(id, parts); ok {
		return r
	}
	return LoadedResource{ID: id, Type: LoadedUnknown, Name: id}
}

// parseMemberID reads the id's parts after any Power or plugin prefix; false outside the grammar.
func parseMemberID(id string, parts []string) (LoadedResource, bool) {
	head := parts[0]
	switch {
	case (head == string(LoadedSkill) || head == string(LoadedSteering)) && len(parts) > 1 && parts[1] != "":
		return LoadedResource{ID: id, Type: LoadedType(head), Name: strings.Join(parts[1:], "::")}, true
	case len(parts) == 3 && head == "tool" && parts[1] != "" && parts[2] != "":
		return LoadedResource{ID: id, Type: LoadedMCP, Name: parts[2], Server: parts[1]}, true
	case len(parts) == 2 && parts[1] != "" && (head == "tool" || head == "builtin"):
		return LoadedResource{ID: id, Type: LoadedTool, Name: parts[1]}, true
	}
	return LoadedResource{}, false
}

// parseToolLoadID reads one of an older tool_load's `tool_ids`, whose schema is
// `builtin::<tool>` or `<server>::<tool>`: the catalogue grammar, else a two-part id is that
// server's MCP tool.
func parseToolLoadID(id string) LoadedResource {
	r := parseResourceID(id)
	server, name, ok := strings.Cut(id, "::")
	if r.Type == LoadedUnknown && ok && server != "" && name != "" && !strings.Contains(name, "::") {
		return LoadedResource{ID: id, Type: LoadedMCP, Name: name, Server: server}
	}
	return r
}

func loadedTitle(loaded []LoadedResource) string {
	labels := make([]string, len(loaded))
	for i, r := range loaded {
		switch r.Type {
		case LoadedMCP:
			labels[i] = r.Server + " " + r.Name
		case LoadedOverview:
			labels[i] = "power overview: " + r.Name
		case LoadedUnknown:
			labels[i] = r.ID
		default:
			labels[i] = string(r.Type) + ": " + r.Name
		}
	}
	return cutUnits("Loaded "+strings.Join(labels, ", "), titleUnits)
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

// SettledToolCall is a call as its card draws it: the call's stored fields with its result's
// title, kind, input and disclosed over them, the fields ToolCardTitle reads. A nil call is a
// result whose call is not at hand.
func SettledToolCall(call *EntryToolCall, res *EntryToolResult) ToolCall {
	var tc ToolCall
	if call != nil {
		tc = ToolCallOfEntry(call)
	}
	if res == nil {
		return tc
	}
	if res.Title != "" {
		tc.Title = res.Title
	}
	if res.Kind != "" {
		tc.Kind = res.Kind
	}
	if len(res.Input) > 0 {
		tc.Input = res.Input
	}
	if res.Disclosed != nil {
		tc.Disclosed = res.Disclosed
	}
	return tc
}

// StampToolCall sets a served call's CardTitle and Loaded.
func StampToolCall(c *EntryToolCall) {
	tc := ToolCallOfEntry(c)
	c.CardTitle, c.Loaded = ToolCardTitle(&tc), ToolLoaded(&tc)
}

// StampToolResult sets a served result's CardTitle and Loaded from its join with call, which
// is nil when the call is not at hand.
func StampToolResult(r *EntryToolResult, call *EntryToolCall) {
	tc := SettledToolCall(call, r)
	r.CardTitle, r.Loaded = ToolCardTitle(&tc), ToolLoaded(&tc)
}

// ServeToolEntries stamps every tool_call and tool_result in entries, rewriting their payloads
// in place, so entries must be the caller's own copy. A result's call is looked up in entries,
// then in context; an entry that does not decode, or derives nothing, is left as stored.
func ServeToolEntries(entries, context []Entry) {
	calls := ToolCallsByID(context, entries)
	for i := range entries {
		e := &entries[i]
		switch e.Kind {
		case EntryKindToolCall:
			if c, ok := calls[e.ID]; ok {
				stamped := *c
				StampToolCall(&stamped)
				reencode(e, &stamped, stamped.CardTitle, stamped.Loaded)
			}
		case EntryKindToolResult:
			var r EntryToolResult
			if json.Unmarshal(e.Payload, &r) == nil {
				StampToolResult(&r, calls[ToolCallIDOfResult(e.ID)])
				reencode(e, &r, r.CardTitle, r.Loaded)
			}
		}
	}
}

// ToolCallsByID decodes every tool_call in the lists by entry id, a later list's call replacing an earlier's.
func ToolCallsByID(lists ...[]Entry) map[string]*EntryToolCall {
	calls := make(map[string]*EntryToolCall)
	for _, src := range lists {
		for i := range src {
			if src[i].Kind != EntryKindToolCall {
				continue
			}
			var c EntryToolCall
			if json.Unmarshal(src[i].Payload, &c) == nil {
				calls[src[i].ID] = &c
			}
		}
	}
	return calls
}

func reencode(e *Entry, payload any, title string, loaded []LoadedResource) {
	if title == "" && loaded == nil {
		return
	}
	if raw, err := json.Marshal(payload); err == nil {
		e.Payload = raw
	}
}
