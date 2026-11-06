package kascap

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// mcpServerSchemaRe is KAS's per-server mcp.json schema, `<x>=<zod>.object({...a,...b,...c,...d})`:
// one zod object spreading four shared member literals (common, stdio, remote, declaration). It is
// matched at each mcpServerSchemaLit site, with identRunBefore checking the `<x>=<zod>` lead.
var mcpServerSchemaRe = regexp.MustCompile(`^\.object\(\{\.\.\.([\w$]+),\.\.\.([\w$]+),\.\.\.([\w$]+),\.\.\.([\w$]+)\}\)`)

const mcpServerSchemaLit = ".object({..."

// identRunBefore reports whether src[:end] ends with `<ident bytes><sep><ident bytes>`.
func identRunBefore(src string, end int, sep byte) bool {
	i := end
	for i > 0 && isIdentByte(src[i-1], true) {
		i--
	}
	if i == end || i == 0 || src[i-1] != sep {
		return false
	}
	return i-1 > 0 && isIdentByte(src[i-2], true)
}

// mcpFileExclusions are the bundle's per-server keys kasServer deliberately has no field for.
var mcpFileExclusions = map[string]string{
	"cwd":         "no field; HealKASConfig reverts a hand-added one (kasServer doc)",
	"autoApprove": "declined: KAS ignores it, and marotte does not pre-approve MCP tools",
}

// extractMCPServerKeys returns the member names of the per-server schema's spread literals, in
// declaration order.
func extractMCPServerKeys(src string) ([]string, error) {
	var matches [][]string
	for i := 0; ; {
		j := strings.Index(src[i:], mcpServerSchemaLit)
		if j < 0 {
			break
		}
		at := i + j
		i = at + len(mcpServerSchemaLit)
		if m := mcpServerSchemaRe.FindStringSubmatch(src[at:]); m != nil && identRunBefore(src, at, '=') {
			matches = append(matches, m)
		}
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("per-server schema: found %d spread objects, want exactly 1", len(matches))
	}
	var keys []string
	for _, name := range matches[0][1:] {
		sites := literalSites(src, name+"={", true)
		if len(sites) != 1 {
			return nil, fmt.Errorf("spread %s: found %d `%s={` literals, want exactly 1", name, len(sites), name)
		}
		members, err := objectKeys(src, sites[0]+len(name)+1, nil)
		if err != nil {
			return nil, fmt.Errorf("spread %s: %w", name, err)
		}
		keys = append(keys, members...)
	}
	return keys, nil
}

// objectKeys reads the depth-1 `key:` names of the object literal opening at src[open]. A member
// it cannot name (a spread, a shorthand, a computed key computed cannot resolve) is an error, so a
// schema reshaped that way fails the census rather than hiding a key. computed maps the identifier
// of a `[x]:` member to its key; nil refuses every computed member.
func objectKeys(src string, open int, computed func(ident string) (string, error)) ([]string, error) {
	var keys []string
	depth := 0
	expectKey := false
	for i := open; i < len(src); i++ {
		c := src[i]
		switch {
		case c == '[' && expectKey && depth == 1 && computed != nil:
			ident := identAt(src, i+1)
			end := i + 1 + len(ident)
			if ident == "" || !strings.HasPrefix(src[end:], "]:") {
				return nil, fmt.Errorf("unnamed member at %d: %.40q", i, src[i:])
			}
			key, err := computed(ident)
			if err != nil {
				return nil, fmt.Errorf("computed member at %d: %w", i, err)
			}
			keys = append(keys, key)
			expectKey = false
			i = end + 1
		case c == '"' || c == '\'' || c == '`':
			end := closingQuote(src, i)
			if end < 0 {
				return nil, fmt.Errorf("unterminated string at %d", i)
			}
			if expectKey && depth == 1 {
				if c == '`' || end+1 >= len(src) || src[end+1] != ':' {
					return nil, fmt.Errorf("unnamed member at %d: %.40q", i, src[i:])
				}
				keys = append(keys, src[i+1:end])
				expectKey = false
			}
			i = end
		case c == '{' || c == '(' || c == '[':
			depth++
			expectKey = depth == 1
		case c == '}' || c == ')' || c == ']':
			depth--
			if depth == 0 {
				return keys, nil
			}
		case c == ',':
			expectKey = depth == 1
		case expectKey && depth == 1:
			j := i
			for j < len(src) && isIdentByte(src[j], j > i) {
				j++
			}
			if j == i || j >= len(src) || src[j] != ':' {
				return nil, fmt.Errorf("unnamed member at %d: %.40q", i, src[i:])
			}
			keys = append(keys, src[i:j])
			expectKey = false
			i = j
		}
	}
	return nil, fmt.Errorf("object at %d never closes", open)
}

// closingQuote returns the index of the quote closing the string literal opening at src[open], or -1.
func closingQuote(src string, open int) int {
	for j := open + 1; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++
		case src[open]:
			return j
		}
	}
	return -1
}

func isIdentByte(c byte, notFirst bool) bool {
	return c == '_' || c == '$' || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || (notFirst && '0' <= c && c <= '9')
}

// renderedKASKeys returns kasServer's json tag names, read from the declaration.
func renderedKASKeys(t *testing.T) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "../mcp/kasfile.go", nil, 0)
	if err != nil {
		t.Fatalf("parse kasServer declaration: %v", err)
	}
	var keys []string
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.TypeSpec)
		if !ok || spec.Name.Name != "kasServer" {
			return true
		}
		for _, field := range spec.Type.(*ast.StructType).Fields.List {
			if field.Tag == nil {
				continue
			}
			tag, unquoteErr := strconv.Unquote(field.Tag.Value)
			if unquoteErr != nil {
				t.Fatalf("decode kasServer tag %s: %v", field.Tag.Value, unquoteErr)
			}
			name, _, _ := strings.Cut(reflect.StructTag(tag).Get("json"), ",")
			keys = append(keys, name)
		}
		return false
	})
	if len(keys) == 0 {
		t.Fatal("kasServer declaration has no json-tagged fields")
	}
	return keys
}

func TestMCPFileSchemaExtractor(t *testing.T) {
	const src = `x=1;a$={env:T.record(T.string()).optional(),timeout:T.number().min(1),"waitForReady":T.boolean()},` +
		`b$={command:T.string().describe("a {brace}, and: comma"),args:T.array(T.object({inner:T.string()}))},` +
		`c$={url:T.string()},d$={type:T.literal('x\'y')};S=T.object({...a$,...b$,...c$,...d$})`
	got, err := extractMCPServerKeys(src)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if want := []string{"env", "timeout", "waitForReady", "command", "args", "url", "type"}; !slices.Equal(got, want) {
		t.Errorf("keys = %v, want %v", got, want)
	}
	if _, err := extractMCPServerKeys(`a={...b,c:1};S=T.object({...a,...a,...a,...a})`); err == nil {
		t.Error("a spread member was accepted, want an error naming it")
	}
}

func TestMCPFileSchemaKeysMatchRender(t *testing.T) {
	forEachBundle(t, func(t *testing.T, b bundle) {
		bundleKeys, err := extractMCPServerKeys(b.src)
		if err != nil {
			t.Fatalf("extract per-server keys: %v", err)
		}
		inBundle := make(map[string]bool, len(bundleKeys))
		for _, k := range bundleKeys {
			inBundle[k] = true
		}
		rendered := renderedKASKeys(t)
		for _, k := range bundleKeys {
			if _, excluded := mcpFileExclusions[k]; !excluded && !slices.Contains(rendered, k) {
				t.Errorf("KAS reads per-server key %q, which kasServer neither renders nor excludes: "+
					"add a field (HealKASConfig reverts a hand-added one) or an mcpFileExclusions row with the reason", k)
			}
		}
		for _, k := range rendered {
			if !inBundle[k] {
				t.Errorf("kasServer renders %q, which KAS's per-server schema no longer declares", k)
			}
		}
		for _, k := range slices.Sorted(maps.Keys(mcpFileExclusions)) {
			if !inBundle[k] || slices.Contains(rendered, k) {
				t.Errorf("mcpFileExclusions row %q is stale: the bundle declares it %t, kasServer renders it %t",
					k, inBundle[k], slices.Contains(rendered, k))
			}
		}
	})
}
