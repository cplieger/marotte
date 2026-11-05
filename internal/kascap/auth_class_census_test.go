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

const (
	authNamesSource    = "../command/prompt.go"
	knownClassesSource = "../rpcerr/rpcerr.go"
)

// authBaseClasses are KAS's credential-refusal classes; every subclass of one refuses a credential too.
var authBaseClasses = []string{"TokenExpiredError", "TokenInvalidError", "AuthRefreshFailedError"}

// jsClassRe matches a minified class declaration naming itself: `<id>=class extends <base>{name="X"`.
var jsClassRe = regexp.MustCompile(`(` + jsIdent + `)=class extends (` + jsIdent + `)\{name="([^"]+)"`)

// missingTokenSetRe matches the Set literal of KAS's missing-token codes, anchored on one member.
var missingTokenSetRe = regexp.MustCompile(`new Set\(\[([^\]]*"MALFORMED_TOKEN"[^\]]*)\]\)`)

// bundleAuthClasses returns the names of the auth base classes and of every class extending one, at any depth.
func bundleAuthClasses(t *testing.T, src string) []string {
	t.Helper()
	type decl struct{ base, name string }
	decls := make(map[string]decl)
	for _, m := range jsClassRe.FindAllStringSubmatch(src, -1) {
		decls[m[1]] = decl{base: m[2], name: m[3]}
	}
	auth := make(map[string]bool)
	for _, base := range authBaseClasses {
		var ids []string
		for id, d := range decls {
			if d.name == base {
				ids = append(ids, id)
			}
		}
		if len(ids) != 1 {
			t.Fatalf("found %d classes named %q in the bundle, want exactly 1; re-anchor on KAS's auth errors", len(ids), base)
		}
		auth[ids[0]] = true
	}
	for grew := true; grew; {
		grew = false
		for id, d := range decls {
			if !auth[id] && auth[d.base] {
				auth[id], grew = true, true
			}
		}
	}
	var names []string
	for id := range auth {
		names = append(names, decls[id].name)
	}
	slices.Sort(names)
	return names
}

// bundleMissingTokenCodes returns the members of the missing-token Set, resolving a member that names a variable
// to the string the bundle assigns it before the Set.
func bundleMissingTokenCodes(t *testing.T, src string) []string {
	t.Helper()
	locs := missingTokenSetRe.FindAllStringSubmatchIndex(src, -1)
	if len(locs) != 1 {
		t.Fatalf("found %d missing-token Sets holding \"MALFORMED_TOKEN\", want exactly 1; re-anchor on KAS's token codes", len(locs))
	}
	var codes []string
	for member := range strings.SplitSeq(src[locs[0][2]:locs[0][3]], ",") {
		if code, err := strconv.Unquote(member); err == nil {
			codes = append(codes, code)
			continue
		}
		assign := regexp.MustCompile(`(?:^|[^A-Za-z0-9_$.])` + regexp.QuoteMeta(member) + `="([^"]*)"`)
		found := assign.FindAllStringSubmatch(src[:locs[0][0]], -1)
		if len(found) == 0 {
			t.Fatalf("missing-token Set member %q is neither a string nor a variable assigned one before the Set", member)
		}
		codes = append(codes, found[len(found)-1][1])
	}
	return codes
}

// declaredStrings reads the string-literal slice var name from the Go source at path, which kascap cannot import.
func declaredStrings(t *testing.T, path, name string) []string {
	t.Helper()
	var out []string
	inspectValue(t, path, name, func(lit *ast.CompositeLit, _ map[string]string) {
		for _, e := range lit.Elts {
			out = append(out, stringLit(t, path, e, nil))
		}
	})
	return out
}

// declaredMapKeys reads the keys of map var name from the Go source at path, resolving a key that names a string
// constant declared in the same file.
func declaredMapKeys(t *testing.T, path, name string) map[string]bool {
	t.Helper()
	out := make(map[string]bool)
	inspectValue(t, path, name, func(lit *ast.CompositeLit, consts map[string]string) {
		for _, e := range lit.Elts {
			kv, ok := e.(*ast.KeyValueExpr)
			if !ok {
				t.Fatalf("%s: %s holds an element that is not key: value", path, name)
			}
			out[stringLit(t, path, kv.Key, consts)] = true
		}
	})
	return out
}

func inspectValue(t *testing.T, path, name string, read func(*ast.CompositeLit, map[string]string)) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	consts := make(map[string]string)
	var value *ast.CompositeLit
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok || len(spec.Names) != len(spec.Values) {
			return true
		}
		for i, n := range spec.Names {
			if lit, ok := spec.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				consts[n.Name], _ = strconv.Unquote(lit.Value)
			}
			if n.Name == name {
				value, _ = spec.Values[i].(*ast.CompositeLit)
			}
		}
		return true
	})
	if value == nil || len(value.Elts) == 0 {
		t.Fatalf("%s: found no %s literal with elements; the declaration moved", path, name)
	}
	read(value, consts)
}

func stringLit(t *testing.T, path string, e ast.Expr, consts map[string]string) string {
	t.Helper()
	switch v := e.(type) {
	case *ast.BasicLit:
		if s, err := strconv.Unquote(v.Value); err == nil && v.Kind == token.STRING {
			return s
		}
	case *ast.Ident:
		if s, ok := consts[v.Name]; ok {
			return s
		}
	}
	t.Fatalf("%s: an element is neither a string literal nor a string constant of the file", path)
	return ""
}

// The fixture is the 2.37 bundle's own declarations, plus a second-level subclass and a non-auth sibling.
func TestAuthSurfaceExtractors(t *testing.T) {
	const src = `Jdt=class extends js{name="IllegalSessionPlacementError";constructor(e){}},` +
		`J2=class extends js{name="TokenExpiredError";constructor(e){super(-32e3,e)}},` +
		"Xp=class extends js{name=\"TokenInvalidError\";constructor(e){super(-32e3,`invalid${e}`)}}," +
		`X2=class extends Xp{name="CredentialAbsentError"},Q$=class extends X2{name="DeepAbsentError"},` +
		`_se=class extends js{name="AuthRefreshFailedError";constructor(e){}};` +
		`var JRi,lcc;JRi="MISSING_TOKEN",lcc=new Set([JRi,"MALFORMED_TOKEN","INVALID_AUTH"])`
	if got, want := bundleAuthClasses(t, src), []string{
		"AuthRefreshFailedError", "CredentialAbsentError", "DeepAbsentError", "TokenExpiredError", "TokenInvalidError",
	}; !slices.Equal(got, want) {
		t.Errorf("bundleAuthClasses = %v, want %v", got, want)
	}
	if got, want := bundleMissingTokenCodes(t, src), []string{"MISSING_TOKEN", "MALFORMED_TOKEN", "INVALID_AUTH"}; !slices.Equal(got, want) {
		t.Errorf("bundleMissingTokenCodes = %v, want %v", got, want)
	}
}

// TestAuthClassesMatchBundle fails when KAS adds a credential-refusal class or token code that the command
// package's auth grade does not list (the prompt then fails with no Sign in route and health stays green), or a
// class the turn-info panel's engine-error row would drop.
func TestAuthClassesMatchBundle(t *testing.T) {
	names := declaredStrings(t, authNamesSource, "authErrorNames")
	known := declaredMapKeys(t, knownClassesSource, "knownClasses")
	forEachBundle(t, func(t *testing.T, b bundle) {
		for _, class := range bundleAuthClasses(t, b.src) {
			if !slices.Contains(names, class) {
				t.Errorf("KAS auth class %q is missing from authErrorNames (%s)", class, authNamesSource)
			}
			if !known[class] {
				t.Errorf("KAS auth class %q is missing from knownClasses (%s)", class, knownClassesSource)
			}
		}
		for _, code := range bundleMissingTokenCodes(t, b.src) {
			if !slices.Contains(names, code) {
				t.Errorf("KAS missing-token code %q is missing from authErrorNames (%s)", code, authNamesSource)
			}
		}
	})
}
