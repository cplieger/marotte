package kascap

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// sessionSchemaPick is the one derivation of KAS's session/new, session/load and session/fork
// _meta.kiro schema, `<x>=<schema>.pick({settings:!0})`, which names the mangled schema.
const sessionSchemaPick = ".pick({settings:!0})"

// sessionSchemaKeys returns the member names of KAS's session _meta.kiro schema
// (`<schema>=<zod>.object({...}).passthrough()`), resolving a computed `[x]:` member through x's one
// string assignment. KAS reads these keys off the parsed block (`n?.<key>`), a shape the
// capability readers do not follow.
func sessionSchemaKeys(src string) ([]string, error) {
	var picks []string
	for _, at := range indexesOf(src, sessionSchemaPick) {
		if name, start := identBefore(src, at); name != "" && start > 0 && src[start-1] == '=' {
			picks = append(picks, name)
		}
	}
	if len(picks) != 1 {
		return nil, fmt.Errorf("session schema: found %d `<x>=<schema>%s` sites %v, want exactly 1", len(picks), sessionSchemaPick, picks)
	}
	schema := picks[0]
	var opens []int
	for _, at := range literalSites(src, schema+"=", false) {
		rest := src[at+len(schema)+1:]
		if zod := identAt(rest, 0); zod != "" && strings.HasPrefix(rest[len(zod):], ".object({") {
			opens = append(opens, at+len(schema)+1+len(zod)+len(".object("))
		}
	}
	if len(opens) != 1 {
		return nil, fmt.Errorf("session schema %s: found %d `%s=<zod>.object({` declarations, want exactly 1", schema, len(opens), schema)
	}
	return objectKeys(src, opens[0], func(ident string) (string, error) { return stringBinding(src, ident, opens[0]) })
}

// stringBinding returns the string literal assigned to ident in scope at use, failing unless there is exactly
// one. The minifier reuses a short name across function scopes, and a name declared inside a scope that closes
// before use is a different variable. A bare assignment there is counted, because esbuild's lazy init assigns
// the outer var; a local that is redeclared or is a parameter then fails as two bindings.
func stringBinding(src, ident string, use int) (string, error) {
	var vals []string
	for _, at := range literalSites(src, ident+`="`, false) {
		open := at + len(ident) + 1
		end := closingQuote(src, open)
		if end > 0 && (at > use || !closesBefore(src, end+1, use) || bareAssignment(src, at)) {
			vals = append(vals, src[open+1:end])
		}
	}
	if len(vals) != 1 {
		return "", fmt.Errorf("%s: found %d string assignments %v in scope, want exactly 1", ident, len(vals), vals)
	}
	return vals[0], nil
}

// bareAssignment reports whether the assignment at at is a statement of its own, not part of a var, let or const
// chain, scanning back over the chain's earlier `name=value,` pairs. An escaped quote on the way back means the
// site is text inside a string literal, no assignment at all.
func bareAssignment(src string, at int) bool {
	depth := 0
	for i := at - 1; i >= 0; i-- {
		switch src[i] {
		case '"', '\'', '`':
			if i > 0 && src[i-1] == '\\' {
				return false
			}
			i = jsStringStart(src, i)
		case ')', ']', '}':
			depth++
		case '(', '[', '{':
			if depth--; depth < 0 {
				return true
			}
		case ';':
			if depth == 0 {
				return true
			}
		case ' ':
			if depth == 0 && declarationKeywordBefore(src, i) {
				return false
			}
		}
	}
	return true
}

// declarationKeywordBefore reports whether var, let or const ends at src[end], as a word of its own.
func declarationKeywordBefore(src string, end int) bool {
	for _, kw := range []string{"var", "let", "const"} {
		if start := end - len(kw); start >= 0 && src[start:end] == kw &&
			(start == 0 || !isIdentByte(src[start-1], true) && src[start-1] != '.') {
			return true
		}
	}
	return false
}

// jsStringStart returns the index of the quote opening the string that closes at i.
func jsStringStart(src string, i int) int {
	q := src[i]
	for j := i - 1; j >= 0; j-- {
		if src[j] != q {
			continue
		}
		escapes := 0
		for k := j - 1; k >= 0 && src[k] == '\\'; k-- {
			escapes++
		}
		if escapes%2 == 0 {
			return j
		}
	}
	return 0
}

// closesBefore reports whether a block enclosing from closes before end; strings are skipped.
func closesBefore(src string, from, end int) bool {
	depth := 0
	for i := from; i < end; i++ {
		switch src[i] {
		case '"', '\'', '`':
			i = jsStringEnd(src, i)
		case '{', '[':
			depth++
		case '}', ']':
			if depth--; depth < 0 {
				return true
			}
		}
	}
	return false
}

// sessionKeysSentElsewhere are the session-schema keys marotte sends from outside this table, each
// with the file whose quoted literal sends it.
var sessionKeysSentElsewhere = map[string]string{
	"modelId":       "../bridge/session_door.go",
	"effortLevel":   "../bridge/session_door.go",
	"modeId":        "../bridge/session_door.go",
	"steering":      "../bridge/session_door.go",
	"createdReason": "../command/fork.go",
}

// declaredSessionKeys are the session-schema keys the table or another sender accounts for.
func declaredSessionKeys() map[string]bool {
	keys := map[string]bool{settingsKey: true}
	for _, row := range table {
		if row.door == doorSession && !inSettings(row.resolver) {
			keys[row.key] = true
		}
	}
	for key := range sessionKeysSentElsewhere {
		keys[key] = true
	}
	return keys
}

// TestSessionKeysSentElsewhereAreSent keeps sessionKeysSentElsewhere from declaring a key no sender
// writes any more, which would hide the key from the census.
func TestSessionKeysSentElsewhereAreSent(t *testing.T) {
	for key, path := range sessionKeysSentElsewhere {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		writes, err := writesMapKey(raw, key)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		if !writes {
			t.Errorf("%s writes no map entry %q; drop the sessionKeysSentElsewhere row or point it at the sender", path, key)
		}
	}
}

// writesMapKey reports whether the Go source writes key into a map, as a composite-literal key or an assignment's
// index, spelled as the string literal or as a constant bound to it. A comment, a log attribute or a struct tag
// naming the key is no write.
func writesMapKey(src []byte, key string) (bool, error) {
	f, err := parser.ParseFile(token.NewFileSet(), "", src, 0)
	if err != nil {
		return false, err
	}
	bound := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		if spec, ok := n.(*ast.ValueSpec); ok {
			for i, v := range spec.Values {
				if lit, ok := v.(*ast.BasicLit); ok && i < len(spec.Names) && lit.Value == strconv.Quote(key) {
					bound[spec.Names[i].Name] = true
				}
			}
		}
		return true
	})
	isKey := func(e ast.Expr) bool {
		switch e := e.(type) {
		case *ast.BasicLit:
			return e.Value == strconv.Quote(key)
		case *ast.Ident:
			return bound[e.Name]
		}
		return false
	}
	writes := false
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CompositeLit:
			if _, ok := n.Type.(*ast.MapType); ok {
				for _, elt := range n.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok && isKey(kv.Key) {
						writes = true
					}
				}
			}
		case *ast.AssignStmt:
			for _, lhs := range n.Lhs {
				if ix, ok := lhs.(*ast.IndexExpr); ok && isKey(ix.Index) {
					writes = true
				}
			}
		}
		return !writes
	})
	return writes, nil
}

func TestWritesMapKey(t *testing.T) {
	cases := map[string]struct {
		src  string
		want bool
	}{
		"map literal key":            {`var m = map[string]any{"modelId": 1}`, true},
		"index through a constant":   {`const k = "modelId"; func f(m map[string]any) { m[k] = 1 }`, true},
		"index with the literal":     {`func f(m map[string]any) { m["modelId"] = 1 }`, true},
		"comment only":               {`// sends "modelId": the model` + "\nvar x = 1", false},
		"log attribute":              {`func f() { slog.Info("x", "modelId", 1) }`, false},
		"struct tag":                 {"type T struct { M string `json:\"modelId\"` }", false},
		"decode by index":            {`func f(m map[string]any) any { return m["modelId"] }`, false},
		"another key's map literal":  {`var m = map[string]any{"modeId": 1}`, false},
		"constant bound, never used": {`const k = "modelId"`, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := writesMapKey([]byte("package p\n"+tc.src), "modelId")
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got != tc.want {
				t.Errorf("writesMapKey = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSessionSchemaKeysExtractor(t *testing.T) {
	const src = `Wx="overrideSessionId",ab=T.object({modeId:T.string().optional(),[Wx]:T.string().optional(),` +
		`filter:T.object({inner:T.enum(["a","b"])}).optional(),"quoted":T.boolean(),` +
		`env:T.record(T.string()).optional().catch(()=>{b.debug("x, y: z")})}).passthrough(),Pk=ab.pick({settings:!0})`
	got, err := sessionSchemaKeys(src)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if want := []string{"modeId", "overrideSessionId", "filter", "quoted", "env"}; !slices.Equal(got, want) {
		t.Errorf("keys = %v, want %v", got, want)
	}
	shadowed := `function g(){var Wx="elk.layout"}` + src
	if got, err := sessionSchemaKeys(shadowed); err != nil || !slices.Contains(got, "overrideSessionId") {
		t.Errorf("a reused name bound in a closed scope: keys = %v, err = %v; want overrideSessionId", got, err)
	}
	chained := `function g(){let a={b:"}"},Wx="elk.layout"}` + src
	if got, err := sessionSchemaKeys(chained); err != nil || !slices.Contains(got, "overrideSessionId") {
		t.Errorf("a reused name declared later in a closed scope's chain: keys = %v, err = %v; want overrideSessionId", got, err)
	}
	lazy := `var Wx;var initWx=E(()=>{Wx="overrideSessionId"});` + strings.Replace(src, `Wx="overrideSessionId",`, ``, 1)
	if got, err := sessionSchemaKeys(lazy); err != nil || !slices.Contains(got, "overrideSessionId") {
		t.Errorf("a lazy init assigning the outer name: keys = %v, err = %v; want overrideSessionId", got, err)
	}
	for name, bad := range map[string]string{
		"two pick sites":               src + `,Qk=cd.pick({settings:!0})`,
		"no declaration":               `Pk=ab.pick({settings:!0})`,
		"unbound computed key":         strings.Replace(src, `Wx="overrideSessionId",`, ``, 1),
		"computed key bound twice":     src + `;Wx="other"`,
		"bound only in a closed scope": strings.Replace(src, `Wx="overrideSessionId",`, `f=function(){var Wx="other"},`, 1),
	} {
		if _, err := sessionSchemaKeys(bad); err == nil {
			t.Errorf("%s: extracted keys, want an error", name)
		}
	}
}
