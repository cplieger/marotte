// The ONE YAML front-matter parser for `.kiro/**/*.md`, shared by internal/server's REST
// scanners and the environment.md generator. Not a YAML library: only the subset `.kiro`
// uses (flat/block/quoted scalars, flow/block sequences); a malformed header degrades to
// empty fields rather than an error.

package steering

import (
	"strings"
)

// FrontMatterReadCap bounds how much of a document is read to find its front-matter, so an
// untrusted repo cannot OOM the container. One definition for every caller.
const FrontMatterReadCap = 64 << 10

// FrontMatter is every field `.kiro` documents carry that a reader surfaces; an absent field
// is the zero value.
type FrontMatter struct {
	// Name is the `name` key: a skill's or agent's declared name, which may
	// differ from its filename.
	Name string
	// Description is the `description` key, block scalars included.
	Description string
	// Inclusion is `inclusion`, validated to always|fileMatch|manual|auto with
	// anything unrecognised folded to always.
	//
	// The default is the STEERING default, so a caller classifying something
	// else must consult HasInclusion before believing it.
	Inclusion string
	// FileMatch is `fileMatchPattern`, meaningful when Inclusion is fileMatch.
	FileMatch string
	// Model is an agent's `model`.
	Model string
	// Tools is an agent's `tools` sequence, flow (`[a, b]`) or block (`- a`).
	Tools []string
	// HasInclusion reports whether the document DECLARED an inclusion key. "always" is the
	// steering default and a fabrication for a skill, yet a skill declaring `manual` or `auto`
	// does become a slash command: forward a declared mode, invent nothing.
	HasInclusion bool
	// SteeringOverride reports whether `steering_override` is present and
	// truthy — a skill that replaces the steering set is worth spotting.
	SteeringOverride bool
}

// Parse extracts the front-matter of a `.kiro` markdown document.
//
// Tolerates a leading UTF-8 BOM and CRLF line endings, so an editor-saved
// document does not silently lose its classification. Pure: no I/O.
func Parse(data []byte) FrontMatter {
	fm := FrontMatter{Inclusion: inclusionAlways}
	body, ok := frontmatterBody(data)
	if !ok {
		return fm
	}
	for _, f := range parseFields(body) {
		applyField(&fm, f)
	}
	return fm
}

type field struct {
	key   string
	value string
	list  []string
}

// Split out so Parse stays flat.
func applyField(fm *FrontMatter, f field) {
	switch f.key {
	case "name":
		fm.Name = f.value
	case "description":
		fm.Description = f.value
	case "inclusion":
		fm.Inclusion = normalizeInclusion(f.value)
		fm.HasInclusion = true
	case "fileMatchPattern":
		fm.FileMatch = f.value
	case "model":
		fm.Model = f.value
	case "tools":
		fm.Tools = f.list
	case "steering_override":
		fm.SteeringOverride = isTruthy(f.value)
	}
}

// A block scalar or block sequence owns every more-indented line after its key, which is why this
// is not a per-line Cut.
func parseFields(body string) []field {
	lines := strings.Split(body, "\n")
	var out []field
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if isSkippableLine(line) || leadingSpaces(line) > 0 {
			// An indented line here was consumed by a block handler, or is malformed.
			continue
		}
		rawKey, rawVal, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := field{key: strings.TrimSpace(rawKey)}
		val := strings.TrimSpace(rawVal)
		switch {
		case isBlockScalarIndicator(val):
			f.value, i = readBlockScalar(lines, i+1)
		case val == "":
			// A block sequence, or an empty value (readBlockSequence returns i unchanged).
			f.list, i = readBlockSequence(lines, i+1)
		case strings.HasPrefix(val, "["):
			f.list = parseFlowSequence(val)
			f.value = unquote(val)
		default:
			f.value = unquote(val)
		}
		out = append(out, f)
	}
	return out
}

// isSkippableLine reports whether a front-matter line carries no data: blank,
// or a full-line comment.
func isSkippableLine(line string) bool {
	t := strings.TrimSpace(line)
	return t == "" || strings.HasPrefix(t, "#")
}

// isBlockScalarIndicator reports whether a value is a YAML block-scalar header:
// `>` or `|`, optionally with a chomping (`-`/`+`) or explicit-indent digit.
// The value itself lives on the following indented lines.
func isBlockScalarIndicator(val string) bool {
	if val == "" || (val[0] != '>' && val[0] != '|') {
		return false
	}
	// Only indicator characters may follow; anything else (e.g. `>foo`) is not
	// a block scalar header.
	for _, r := range val[1:] {
		if r != '-' && r != '+' && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// readBlockScalar folds the indented lines from `from` into one string and returns the index
// of the LAST line consumed. Folding is `>`-style for every indicator: every consumer renders
// a single line.
func readBlockScalar(lines []string, from int) (value string, lastIdx int) {
	var parts []string
	i := from
	for ; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			// A blank line inside or just after the block is safe to consume.
			continue
		}
		if leadingSpaces(line) == 0 {
			break // a new top-level key
		}
		parts = append(parts, strings.TrimSpace(line))
	}
	return strings.Join(parts, " "), i - 1
}

// readBlockSequence reads `- item` lines starting at `from`, returning the items
// and the index of the last line consumed. Returns (nil, from-1) when the next
// content line is not a sequence entry, leaving the caller's cursor untouched.
func readBlockSequence(lines []string, from int) (items []string, lastIdx int) {
	i := from
	for ; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			continue
		}
		t := strings.TrimSpace(line)
		if leadingSpaces(line) == 0 || !strings.HasPrefix(t, "- ") {
			break
		}
		items = append(items, unquote(strings.TrimSpace(strings.TrimPrefix(t, "- "))))
	}
	if len(items) == 0 {
		return nil, from - 1
	}
	return items, i - 1
}

// Values containing a comma inside quotes are not supported — no `.kiro` document uses one, and
// guessing would be worse than the simple split.
func parseFlowSequence(val string) []string {
	inner := strings.TrimSuffix(strings.TrimPrefix(val, "["), "]")
	if strings.TrimSpace(inner) == "" {
		return nil
	}
	parts := strings.Split(inner, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := unquote(strings.TrimSpace(p)); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// leadingSpaces counts the indentation of a line, treating a tab as one level.
func leadingSpaces(line string) int {
	n := 0
	for _, r := range line {
		if r != ' ' && r != '\t' {
			break
		}
		n++
	}
	return n
}

// unquote strips one matching pair of surrounding single or double quotes.
// Deliberately not an escape-sequence decoder: `.kiro` front-matter uses quotes
// to protect a leading `*` or a colon, never to encode one.
func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// isTruthy reads a YAML boolean the permissive way YAML 1.1 does, because a
// hand-authored `steering_override: yes` should not read as false.
func isTruthy(v string) bool {
	switch strings.ToLower(v) {
	case "true", "yes", "on", "1":
		return true
	default:
		return false
	}
}

// normalizeText strips a leading UTF-8 BOM and folds every line ending (a lone "\r" too) to
// "\n". Parse and FirstHeading share it, so they agree on what a line is.
func normalizeText(data []byte) string {
	s := strings.TrimPrefix(string(data), "\ufeff")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// FirstHeading returns the text of a document's first markdown H1, or "": the fallback
// label for a document with no front-matter. A front-matter block is skipped.
func FirstHeading(data []byte) string {
	content := normalizeText(data)
	if _, after, ok := strings.Cut(content, "\n---"); ok && strings.HasPrefix(content, "---\n") {
		content = after
	}
	for line := range strings.SplitSeq(content, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "# "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}
