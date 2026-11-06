package kascap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
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

// authRefusalMessages are the texts KAS's credential-refusal classes pass to super. They route the
// discovery rather than the class names, so a renamed class is still found and must then be named
// by marotte's auth grade.
var authRefusalMessages = []string{
	"Authentication token has expired",
	"Authentication token is invalid",
	"Auth refresh callback failed: ",
	"Sign in to Kiro to send a prompt.",
}

// missingTokenSetRe matches the Set literal of KAS's missing-token codes, anchored on one member.
var missingTokenSetRe = regexp.MustCompile(`new Set\(\[([^\]]*"MALFORMED_TOKEN"[^\]]*)\]\)`)

// jsClass is one minified `<id>=class extends <base>{...}` declaration. name is its own `name="X"`
// field, "" when it inherits one; head is the body, "" when it does not close within classHeadReach.
type jsClass struct{ id, base, name, head string }

const (
	classExtends   = "=class extends "
	classHeadReach = 64 << 10
)

// jsClasses returns every `<id>=class extends <base>{` declaration in src.
func jsClasses(src string) []jsClass {
	var out []jsClass
	for at := strings.Index(src, classExtends); at >= 0; {
		next := at + len(classExtends)
		id, _ := identBefore(src, at)
		base := identAt(src, next)
		if open := next + len(base); id != "" && base != "" && open < len(src) && src[open] == '{' {
			head := jsBracketed(src[:min(len(src), open+classHeadReach)], open)
			out = append(out, jsClass{id: id, base: base, name: classNameField(head), head: head})
		}
		i := strings.Index(src[next:], classExtends)
		if i < 0 {
			break
		}
		at = next + i
	}
	return out
}

// classNameField returns the `name="X"` member at depth 1 of a class body, after `{`, `}` or `;`.
func classNameField(body string) string {
	const field = `name="`
	depth := 0
	for i := 0; i < len(body); i++ {
		switch c := body[i]; c {
		case '"', '\'', '`':
			i = jsStringEnd(body, i)
		case '{', '[', '(':
			depth++
		case '}', ']', ')':
			depth--
		default:
			if depth == 1 && strings.ContainsRune("{};", rune(body[i-1])) && strings.HasPrefix(body[i:], field) {
				if end := strings.IndexByte(body[i+len(field):], '"'); end >= 0 {
					return body[i+len(field) : i+len(field)+end]
				}
			}
		}
	}
	return ""
}

// bundleAuthClasses returns the names of the classes passing an authRefusalMessages text to super,
// and of every class extending one at any depth that names itself.
func bundleAuthClasses(t *testing.T, src string) []string {
	t.Helper()
	classes := jsClasses(src)
	auth := make(map[string]bool)
	for _, msg := range authRefusalMessages {
		var found []jsClass
		for _, c := range classes {
			if strings.Contains(c.head, "super(") && strings.Contains(c.head, msg) {
				found = append(found, c)
			}
		}
		if len(found) != 1 || found[0].name == "" {
			t.Fatalf("found %d classes passing %q to super in the bundle, want exactly 1 naming itself; re-anchor on KAS's auth errors",
				len(found), msg)
		}
		auth[found[0].id] = true
	}
	for grew := true; grew; {
		grew = false
		for _, c := range classes {
			if !auth[c.id] && auth[c.base] {
				auth[c.id], grew = true, true
			}
		}
	}
	seen := make(map[string]bool)
	for _, c := range classes {
		if auth[c.id] && c.name != "" {
			seen[c.name] = true
		}
	}
	return slices.Sorted(maps.Keys(seen))
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
		before := src[:locs[0][0]]
		sites := literalSites(before, member+`="`, false)
		value := -1
		if len(sites) > 0 {
			value = sites[len(sites)-1] + len(member) + 2
		}
		end := -1
		if value >= 0 {
			end = strings.IndexByte(before[value:], '"')
		}
		if end < 0 {
			t.Fatalf("missing-token Set member %q is neither a string nor a variable assigned one before the Set", member)
		}
		codes = append(codes, before[value:value+end])
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

// The fixture is the 3.0.0 and 3.1.0 bundles' own declarations (3.1.0 adds a keep-names static block),
// plus a renamed refresh class, a second-level subclass, a subclass inheriting its name and a
// non-auth sibling.
func TestAuthSurfaceExtractors(t *testing.T) {
	const src = "Jdt=class extends js{name=\"IllegalSessionPlacementError\";constructor(e){super(-32603,`Illegal session placement: ${e}`)}}," +
		`Q2=class extends Do{static{a(this,"TokenExpiredError")}name="TokenExpiredError";constructor(e){super(-32e3,e??"Authentication token has expired")}},` +
		"Xp=class extends js{name=\"TokenInvalidError\";constructor(e){super(-32e3,`Authentication token is invalid${e?`: ${e}`:\"\"}`)}}," +
		`X2=class extends Xp{name="CredentialAbsentError"},Q$=class extends X2{name="DeepAbsentError"},Z9=class extends Q${},` +
		"_se=class extends js{name=\"RenamedRefreshError\";constructor(e){super(-32e3,`Auth refresh callback failed: ${e}`)}}," +
		`rSt=class extends Do{static{a(this,"SignInRequiredError")}name="SignInRequiredError";errorCategory="error";data;` +
		`constructor(){super(-32e3,"Sign in to Kiro to send a prompt."),this.data={errorType:this.name}}};` +
		`var JRi,lcc;JRi="MISSING_TOKEN",lcc=new Set([JRi,"MALFORMED_TOKEN","INVALID_AUTH"])`
	if got, want := bundleAuthClasses(t, src), []string{
		"CredentialAbsentError", "DeepAbsentError", "RenamedRefreshError", "SignInRequiredError", "TokenExpiredError", "TokenInvalidError",
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
