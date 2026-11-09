package apitable_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/apitable"
	"github.com/cplieger/marotte/internal/route"
)

// getAudit is each GET route's effect audit: each row's path (a trailing "*" matches the subtree) and
// whether its handler has no effect. A GET entry without a row fails, as does one whose Safe differs.
var getAudit = map[string]bool{
	"/":                             true,
	"/healthz":                      true,
	"/healthz/":                     true,
	"/metrics":                      true,
	"/preview/":                     true,
	"/api":                          true,
	"/api/":                         true,
	"/api/version":                  true,
	"/api/kiro-settings":            true,
	"/api/chats":                    true,
	"/api/chats/search":             true,
	"/api/chats/{id}":               true,
	"/api/chats/{id}/turns":         true,
	"/api/chats/{id}/turns/{turn}":  true,
	"/api/chats/{id}/search":        true,
	"/api/chats/{id}/export":        true,
	"/api/chats/{id}/tools/{tool}":  true,
	"/api/chats/{id}/kiro-session":  true,
	"/api/chats/{id}/merges/{op}":   false,
	"/api/sessions":                 true,
	"/api/runs/{id}":                true,
	"/api/runs/{id}/controls":       true,
	"/api/runs/{id}/steps/{path}":   true,
	"/api/runs/{id}/turns/{turn}":   true,
	"/api/runs/live":                true,
	"/api/recipes":                  true,
	"/api/config-template":          true,
	"/api/slash-commands":           true,
	"/api/steering/issues":          true,
	"/api/governance":               true,
	"/api/hooks":                    true,
	"/api/memory":                   true,
	"/api/memory/{id}":              true,
	"/api/memory/sync":              true,
	"/api/knowledge":                true,
	"/api/launch-rules":             true,
	"/api/file-changes":             true,
	"/api/engine":                   true,
	"/api/config/standing":          true,
	"/api/powers":                   false,
	"/api/powers/{name}/servers":    true,
	"/api/settings":                 true,
	"/api/steering":                 true,
	"/api/tabs":                     true,
	"/api/workspace/kiro-docs":      true,
	"/api/specs/{dir}":              true,
	"/api/permissions":              true,
	"/api/openapi.json":             true,
	"/api/account/usage":            true,
	"/api/tools":                    true,
	"/api/tools/search":             true,
	"/api/tools/jobs":               true,
	"/api/tools/catalog":            true,
	"/api/tools/status":             true,
	"/api/git/repos":                true,
	"/api/git/log":                  true,
	"/api/git/branches":             true,
	"/api/git/branch-commits":       true,
	"/api/git/show":                 true,
	"/api/git/status":               false,
	"/api/git/status-all":           false,
	"/api/forges":                   true,
	"/api/forges/{id}":              true,
	"/api/forges/{id}/capabilities": true,
	"/api/forges/{id}/repos*":       true,
	"/api/forges/inventory":         true,
	"/api/mcp":                      true,
	"/api/mcp/{name}":               true,
	"/api/mcp/registry/search":      true,
	"/api/mcp/status":               true,
	"/api/mcp/pool":                 true,
	"/api/file":                     true,
	"/api/file/stat":                true,
	"/api/file/download":            true,
	"/api/files":                    true,
	"/api/files/search":             true,
	"/api/preview/stamp":            true,
	"/api/push/vapid-key":           true,
	"/api/whoami":                   true,
	"/api/login/options":            true,
	"/api/events":                   false,
	"/api/shell/ws":                 false,
	"/debug/pprof/":                 true,
	"/api/test/sse":                 true,
}

func auditRow(path string) (safe, ok bool) {
	if safe, ok := getAudit[path]; ok {
		return safe, true
	}
	for row, safe := range getAudit {
		if prefix, sub := strings.CutSuffix(row, "*"); sub && strings.HasPrefix(path, prefix) {
			return safe, true
		}
	}
	return false, false
}

var classes = []route.Class{
	route.ClassPublic, route.ClassSession, route.ClassKeyScope,
	route.ClassRecentAuth, route.ClassRecovery, route.ClassLoopbackOperator,
}

func TestEndpoints_EntriesAreComplete(t *testing.T) {
	entries := apitable.Entries()
	byKey := map[route.Key]route.Entry{}
	names := map[string]bool{}
	for _, e := range entries {
		if _, dup := byKey[e.Key()]; dup {
			t.Errorf("%s %s: two entries", e.Method, e.Path)
		}
		byKey[e.Key()] = e
		if e.Method != http.MethodHead {
			if e.Name == "" || names[e.Name] {
				t.Errorf("%s %s: name %q is empty or not unique", e.Method, e.Path, e.Name)
			}
			names[e.Name] = true
		}
	}
	for _, e := range entries {
		id := e.Method + " " + e.Path
		if e.Access.Class == "" || e.Access.Transport == 0 {
			t.Errorf("%s: Access %+v lacks a class or a transport", id, e.Access)
		}
		if e.Kind == 0 {
			t.Errorf("%s: no kind", id)
		}
		if e.Summary == "" {
			t.Errorf("%s: no summary", id)
		}
		if isSafeMethod(e.Method) {
			safe, ok := auditRow(e.Path)
			if !ok {
				t.Errorf("%s: no row in the GET audit", id)
			} else if e.Access.Safe != safe {
				t.Errorf("%s: Safe = %v, the audit says %v", id, e.Access.Safe, safe)
			}
		} else if e.Access.Safe {
			t.Errorf("%s: Safe on a method that is not GET or HEAD", id)
		}
		if e.Access.Class == route.ClassRecentAuth && e.Access.RecentAuth <= 0 {
			t.Errorf("%s: recent-auth entry with no window", id)
		}
		if e.Query != nil {
			checkQuery(t, id, e.Query)
		}
		if e.ContentType != "" && e.Kind != route.KindRaw {
			t.Errorf("%s: ContentType on a %v entry", id, e.Kind)
		}
		if e.AuthCodes != nil {
			t.Errorf("%s: AuthCodes before the library entries exist", id)
		}
		checkParams(t, id, e)
	}
	for _, e := range entries {
		if e.Method != http.MethodGet || e.Any {
			continue
		}
		twin, ok := byKey[route.Key{Method: http.MethodHead, Path: e.Path}]
		e.Method = http.MethodHead
		if !ok || !reflect.DeepEqual(twin, e) {
			t.Errorf("GET %s: HEAD twin %+v differs from its GET entry in more than the method", e.Path, twin)
		}
	}
}

func checkQuery(t *testing.T, id string, q reflect.Type) {
	t.Helper()
	if q.Kind() != reflect.Struct || q.Name() == "" {
		t.Errorf("%s: Query %v is not a named struct", id, q)
		return
	}
	for f := range q.Fields() {
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if tag == "" || tag == "-" {
			t.Errorf("%s: query field %s has no json key", id, f.Name)
		}
		switch f.Type.Kind() {
		case reflect.String, reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		default:
			t.Errorf("%s: query field %s is %v, not a string, bool or integer", id, f.Name, f.Type)
		}
	}
}

func checkParams(t *testing.T, id string, e route.Entry) {
	t.Helper()
	var wild []string
	for seg := range strings.SplitSeq(e.Path, "/") {
		if name, ok := strings.CutPrefix(seg, "{"); ok {
			name = strings.TrimSuffix(strings.TrimSuffix(name, "}"), "...")
			if name != "$" {
				wild = append(wild, name)
			}
		}
	}
	if (e.Params == nil) != (len(wild) == 0) {
		t.Errorf("%s: Params %v for wildcards %v", id, e.Params, wild)
		return
	}
	if e.Params == nil {
		return
	}
	var tags []string
	for f := range e.Params.Fields() {
		tag, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
		if strings.Contains(opts, "omitempty") || f.Type.Kind() != reflect.String {
			t.Errorf("%s: path param %s is optional or not a string", id, f.Name)
		}
		tags = append(tags, tag)
	}
	slices.Sort(tags)
	slices.Sort(wild)
	if !slices.Equal(tags, wild) {
		t.Errorf("%s: Params keys %v, path wildcards %v", id, tags, wild)
	}
}

func TestEndpoints_AccessRowsMapOntoAccess(t *testing.T) {
	scopes := route.KeyScopes()
	for _, e := range apitable.Entries() {
		id := e.Method + " " + e.Path
		a := e.Access
		if !slices.Contains(classes, a.Class) {
			t.Errorf("%s: class %q is none of the six", id, a.Class)
		}
		switch {
		case a.Class == route.ClassKeyScope && !slices.Contains(scopes, a.KeyScope):
			t.Errorf("%s: key_scope entry with scope %q outside route.KeyScopes()", id, a.KeyScope)
		case a.Class != route.ClassKeyScope && a.KeyScope != "":
			t.Errorf("%s: KeyScope %q on a %s entry", id, a.KeyScope, a.Class)
		}
		if a.Class != route.ClassRecentAuth && a.RecentAuth != 0 {
			t.Errorf("%s: RecentAuth %v on a %s entry", id, a.RecentAuth, a.Class)
		}
		if a.Class == route.ClassRecovery {
			t.Errorf("%s: a recovery entry before the access routes exist", id)
		}
	}
}

// TestEndpoints_ClassesFollowTheCensus pins the access class of a sample of every route class.
func TestEndpoints_ClassesFollowTheCensus(t *testing.T) {
	want := map[route.Key]route.Access{
		{Method: "GET", Path: "/metrics"}:                         {Class: route.ClassKeyScope, KeyScope: "metrics:read"},
		{Method: "POST", Path: "/api/sessions/{session}/notify"}:  {Class: route.ClassKeyScope, KeyScope: "notify"},
		{Method: "GET", Path: "/api/events"}:                      {Class: route.ClassKeyScope, KeyScope: "runs:control"},
		{Method: "GET", Path: "/api/chats"}:                       {Class: route.ClassKeyScope, KeyScope: "chats:read"},
		{Method: "GET", Path: "/api/sessions"}:                    {Class: route.ClassKeyScope, KeyScope: "chats:read"},
		{Method: "GET", Path: "/api/runs/{id}"}:                   {Class: route.ClassKeyScope, KeyScope: "runs:read"},
		{Method: "GET", Path: "/api/recipes"}:                     {Class: route.ClassKeyScope, KeyScope: "runs:read"},
		{Method: "POST", Path: "/api/runs/{id}/answer"}:           {Class: route.ClassKeyScope, KeyScope: "runs:answer"},
		{Method: "POST", Path: "/api/runs"}:                       {Class: route.ClassKeyScope, KeyScope: "runs:control"},
		{Method: "DELETE", Path: "/api/runs/{id}"}:                {Class: route.ClassKeyScope, KeyScope: "runs:control"},
		{Method: "POST", Path: "/api/runs/{id}/steps/{path}/end"}: {Class: route.ClassKeyScope, KeyScope: "runs:control"},
		{Method: "GET", Path: "/api/tools/status"}:                {Class: route.ClassKeyScope, KeyScope: "tools"},
		{Method: "GET", Path: "/api/tools"}:                       {Class: route.ClassKeyScope, KeyScope: "tools"},
		{Method: "POST", Path: "/api/login"}:                      {Class: route.ClassRecentAuth, RecentAuth: 10 * time.Minute},
		{Method: "POST", Path: "/api/logout"}:                     {Class: route.ClassRecentAuth, RecentAuth: 10 * time.Minute},
		{Method: "POST", Path: "/api/engine/restart"}:             {Class: route.ClassRecentAuth, RecentAuth: 10 * time.Minute},
		{Method: "GET", Path: "/api/shell/ws"}:                    {Class: route.ClassRecentAuth, RecentAuth: 8 * time.Hour},
		{Method: "POST", Path: "/api/shell/restart"}:              {Class: route.ClassSession},
		{Method: "POST", Path: "/api/kiro-cli/rescan"}:            {Class: route.ClassLoopbackOperator},
		{Method: "GET", Path: "/debug/pprof/"}:                    {Class: route.ClassLoopbackOperator},
		{Method: "GET", Path: "/healthz"}:                         {Class: route.ClassPublic},
		{Method: "POST", Path: "/healthz/"}:                       {Class: route.ClassPublic},
		{Method: "GET", Path: "/preview/"}:                        {Class: route.ClassPublic},
		{Method: "POST", Path: "/"}:                               {Class: route.ClassPublic},
		{Method: "POST", Path: "/api/"}:                           {Class: route.ClassSession},
		{Method: "GET", Path: "/api/openapi.json"}:                {Class: route.ClassSession},
		{Method: "POST", Path: "/api/command"}:                    {Class: route.ClassSession},
		{Method: "POST", Path: "/api/forges/login"}:               {Class: route.ClassSession},
		{Method: "PATCH", Path: "/api/settings"}:                  {Class: route.ClassSession},
	}
	got := map[route.Key]route.Access{}
	for _, e := range apitable.Entries() {
		got[e.Key()] = e.Access
	}
	for k, w := range want {
		g, ok := got[k]
		if !ok {
			t.Errorf("%s %s: no entry", k.Method, k.Path)
			continue
		}
		if g.Class != w.Class || g.KeyScope != w.KeyScope || g.RecentAuth != w.RecentAuth {
			t.Errorf("%s %s: access %s %q %v, want %s %q %v", k.Method, k.Path, g.Class, g.KeyScope, g.RecentAuth, w.Class, w.KeyScope, w.RecentAuth)
		}
	}
	for _, e := range apitable.Entries() {
		for _, single := range []string{"metrics:read", "notify"} {
			if e.Access.KeyScope == single && e.Path != "/metrics" && e.Path != "/api/sessions/{session}/notify" {
				t.Errorf("%s %s: the single-route scope %q on a second route", e.Method, e.Path, single)
			}
		}
	}
}

func TestEndpoints_AnyMethodMountsAreExactlyFive(t *testing.T) {
	methods := map[string][]string{}
	for _, e := range apitable.Entries() {
		if e.Any {
			methods[e.Path] = append(methods[e.Path], e.Method)
		}
	}
	want := []string{"/", "/api", "/api/", "/debug/pprof/", "/healthz/"}
	got := make([]string, 0, len(methods))
	for p, ms := range methods {
		got = append(got, p)
		slices.Sort(ms)
		seven := slices.Sorted(slices.Values(route.AnyMethods()))
		if !slices.Equal(ms, seven) {
			t.Errorf("catch-all %s has entries for %v, want the seven %v", p, ms, seven)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("catch-all mounts = %v, want %v", got, want)
	}
}

// commandTypes parses internal/marotte/commands.go for its CommandType constants.
func commandTypes(t *testing.T) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "../marotte/commands.go", nil, 0)
	if err != nil {
		t.Fatalf("parse commands.go: %v", err)
	}
	var out []string
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "CommandType" {
				continue
			}
			for _, v := range vs.Values {
				if lit, ok := v.(*ast.BasicLit); ok {
					s, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatalf("CommandType value %s: %v", lit.Value, err)
					}
					out = append(out, s)
				}
			}
		}
	}
	if len(out) < 30 {
		t.Fatalf("found %d CommandType constants, want the file's full set", len(out))
	}
	return out
}

func TestEndpoints_CommandUnionCoversEveryType(t *testing.T) {
	declared := commandTypes(t)
	seen := map[string]bool{}
	for _, m := range apitable.Commands() {
		name := string(m.Type)
		if seen[name] {
			t.Errorf("command %q has two members", name)
		}
		seen[name] = true
		if !slices.Contains(declared, name) {
			t.Errorf("member %q names no CommandType constant", name)
		}
		if m.Payload != nil && (m.Payload.Kind() != reflect.Struct || m.Payload.Name() == "") {
			t.Errorf("command %q payload %v is not a named struct", name, m.Payload)
		}
	}
	for _, c := range declared {
		if !seen[c] {
			t.Errorf("CommandType %q has no union member", c)
		}
	}
}
