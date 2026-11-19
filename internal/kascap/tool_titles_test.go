package kascap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const toolIdentitySource = "../translate/tool_identity.go"

// parseToolIdentity reads the translator's source, so the translator exports nothing for these
// tests: its single-name string consts, and its single-name values by name.
func parseToolIdentity(t *testing.T) (consts map[string]string, values map[string]ast.Expr) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), toolIdentitySource, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", toolIdentitySource, err)
	}
	consts, values = map[string]string{}, map[string]ast.Expr{}
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || len(spec.Values) != 1 {
			return true
		}
		if lit, ok := spec.Values[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if s, err := strconv.Unquote(lit.Value); err == nil {
				consts[spec.Names[0].Name] = s
			}
		}
		values[spec.Names[0].Name] = spec.Values[0]
		return true
	})
	return consts, values
}

// declaredStringConst is the value of the translator's string const named name.
func declaredStringConst(t *testing.T, name string) string {
	t.Helper()
	consts, _ := parseToolIdentity(t)
	s, ok := consts[name]
	if !ok || s == "" {
		t.Fatalf("%s: no string const named %s; the declaration moved", toolIdentitySource, name)
	}
	return s
}

// declaredToolMap reads the translator's string-to-string map literal named name. A key or value
// may be a string const declared in the same file.
func declaredToolMap(t *testing.T, name string) map[string]string {
	t.Helper()
	consts, values := parseToolIdentity(t)
	literal, _ := values[name].(*ast.CompositeLit)
	if literal == nil {
		t.Fatalf("%s: no map literal named %s; the declaration moved", toolIdentitySource, name)
	}
	str := func(e ast.Expr) string {
		switch e := e.(type) {
		case *ast.BasicLit:
			if e.Kind == token.STRING {
				if s, err := strconv.Unquote(e.Value); err == nil {
					return s
				}
			}
		case *ast.Ident:
			if s, ok := consts[e.Name]; ok {
				return s
			}
		}
		t.Fatalf("%s: %s holds an entry that is neither a string literal nor a string const", toolIdentitySource, name)
		return ""
	}
	out := map[string]string{}
	for _, elt := range literal.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			t.Fatalf("%s: %s is not a keyed map literal", toolIdentitySource, name)
		}
		out[str(kv.Key)] = str(kv.Value)
	}
	if len(out) == 0 {
		t.Fatalf("%s: %s is empty", toolIdentitySource, name)
	}
	return out
}

// toolIDRoutedNameRe finds KAS's keep-names call for the membership function over the set of action types it
// stamps `_meta.kiro.toolId` for; the call names the minified function, whose body names the set.
var toolIDRoutedNameRe = regexp.MustCompile(`[\w$]+\(([\w$]+),"isToolIdRoutedActionType"\)`)

// toolIDMetaSet answers the members of that set, discovered through the keep-names literal and its
// membership function, each discovery unique; ok is false when any step finds none or several.
func toolIDMetaSet(src string) (members []string, ok bool) {
	// The two class-led patterns run through findAround on their literal; the function step opens on one.
	only := func(re *regexp.Regexp, lit string) []string {
		var m [][]int
		if lit == "" {
			m = re.FindAllStringSubmatchIndex(src, -1)
		} else {
			m = findAround(re, lit, src)
		}
		if len(m) != 1 {
			return nil
		}
		return submatches(src, m)
	}
	fn := only(toolIDRoutedNameRe, `,"isToolIdRoutedActionType")`)
	if fn == nil {
		return nil, false
	}
	set := only(regexp.MustCompile(`function `+regexp.QuoteMeta(fn[1])+`\(t\)\{return ([\w$]+)\.has\(t\)\}`), "")
	if set == nil {
		return nil, false
	}
	list := only(regexp.MustCompile(`[,;\s]`+regexp.QuoteMeta(set[1])+`=new Set\(\[((?:"[a-z_]+",?)+)\]\)`),
		set[1]+`=new Set([`)
	if list == nil {
		return nil, false
	}
	return strings.Split(strings.ReplaceAll(list[1], `"`, ""), ","), true
}

// The pinned agent server titles a built-in from its tool class (`static id="x";static title="Y"`)
// or definition object (`{id:"x",title:"Y"`), else falls back to the bare id when it carries no
// `remote_` or `subagent_` prefix. Each row of the translator's table must be one of those.
func TestBuiltinToolTitlesMatchTheBundle(t *testing.T) {
	forEachBundle(t, func(t *testing.T, b bundle) {
		for title, id := range declaredToolMap(t, "builtinToolByTitle") {
			class := `static id="` + id + `";static title="` + title + `"`
			object := `{id:"` + id + `",title:"` + title + `"`
			bare := title == id && !strings.HasPrefix(id, "remote_") && !strings.HasPrefix(id, "subagent_") &&
				strings.Contains(b.src, `"`+id+`"`) && !strings.Contains(b.src, `static id="`+id+`"`)
			if !strings.Contains(b.src, class) && !strings.Contains(b.src, object) && !bare {
				t.Errorf("%s: no tool titled %q with id %q; fix builtinToolByTitle in internal/translate/tool_identity.go", b.name, title, id)
			}
		}
	})
}

// The translator reads a remote_<name> id with server remote as a built-in. That holds while KAS's
// remote wrapper declares exactly that prefix and server and builds user MCP ids as mcp_<...>.
func TestRemoteWrapperIdentityMatchesTheBundle(t *testing.T) {
	prefix := declaredStringConst(t, "remoteWrapperIDPrefix")
	server := declaredStringConst(t, "remoteWrapperServer")
	forEachBundle(t, func(t *testing.T, b bundle) {
		if wrapper := `static idPrefix="` + prefix + `";static serverName="` + server + `"`; !strings.Contains(b.src, wrapper) {
			t.Errorf("%s: no remote wrapper declaring %s; fix remoteWrapperIDPrefix and remoteWrapperServer in internal/translate/tool_identity.go", b.name, wrapper)
		}
		if !strings.Contains(b.src, "`mcp_${") {
			t.Errorf("%s: no mcp_ tool id builder; a user MCP id may now collide with %s<name>", b.name, prefix)
		}
	})
}

// builtinToolName prefers `_meta.kiro.toolId`, and keeps a title table only because KAS stamps it on
// so few tools. When the set grows, key on it and shrink builtinToolByTitle.
func TestToolIDMetaIsStampedOnTheKnownTools(t *testing.T) {
	forEachBundle(t, func(t *testing.T, b bundle) {
		got, ok := toolIDMetaSet(b.src)
		if !ok {
			t.Fatalf("%s: no unique toolId action-type set behind isToolIdRoutedActionType; re-anchor this test", b.name)
		}
		want := []string{"disclose_context", "create_diagram"}
		if !slices.Equal(got, want) {
			t.Errorf("%s: KAS stamps toolId on %v, want %v", b.name, got, want)
		}
	})
}

func TestToolIDMetaSetExtractor(t *testing.T) {
	adjacent := `var HWt="...",h1l=new Set(["disclose_context","create_diagram"]);function lOe(t){return h1l.has(t)}` +
		`a(lOe,"isToolIdRoutedActionType");function zWt(t,e){if(t!=="disclose_context")return}`
	hoisted := `function Dq(t){return bel.has(t)}function bGt(t,e){if(t!=="disclose_context")return}` +
		`var wGt,bel,A6=_(()=>{wGt="...",bel=new Set(["disclose_context","create_diagram"]);a(Dq,"isToolIdRoutedActionType");})`
	want := []string{"disclose_context", "create_diagram"}
	for name, src := range map[string]string{"set beside its function": adjacent, "set hoisted into a lazy module": hoisted} {
		if got, ok := toolIDMetaSet(src); !ok || !slices.Equal(got, want) {
			t.Errorf("%s: toolIDMetaSet = %v, %v; want %v", name, got, ok, want)
		}
	}
	if got, ok := toolIDMetaSet(adjacent + hoisted); ok {
		t.Errorf("two keep-names calls: toolIDMetaSet = %v, want no unique discovery", got)
	}
}
