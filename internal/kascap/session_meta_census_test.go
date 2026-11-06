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
	return objectKeys(src, opens[0], func(ident string) (string, error) { return stringBinding(src, ident) })
}

// stringBinding returns the string literal assigned to ident, failing unless there is exactly one.
func stringBinding(src, ident string) (string, error) {
	var vals []string
	for _, at := range literalSites(src, ident+`="`, false) {
		open := at + len(ident) + 1
		if end := closingQuote(src, open); end > 0 {
			vals = append(vals, src[open+1:end])
		}
	}
	if len(vals) != 1 {
		return "", fmt.Errorf("%s: found %d string assignments %v, want exactly 1", ident, len(vals), vals)
	}
	return vals[0], nil
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
	for name, bad := range map[string]string{
		"two pick sites":           src + `,Qk=cd.pick({settings:!0})`,
		"no declaration":           `Pk=ab.pick({settings:!0})`,
		"unbound computed key":     strings.Replace(src, `Wx="overrideSessionId",`, ``, 1),
		"computed key bound twice": src + `;Wx="other"`,
	} {
		if _, err := sessionSchemaKeys(bad); err == nil {
			t.Errorf("%s: extracted keys, want an error", name)
		}
	}
}
