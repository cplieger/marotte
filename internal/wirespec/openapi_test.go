package wirespec

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/go/wiregen/v2"
	"github.com/cplieger/marotte/internal/apitable"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/route"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// docsOpenAPI is the committed document the generator writes.
func docsOpenAPI(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "openapi.json"))
	if err != nil {
		t.Fatalf("read docs/openapi.json: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("docs/openapi.json: %v", err)
	}
	return doc
}

func TestOpenAPI_ValidatesAgainstOAS31(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "openapi.json"))
	if err != nil {
		t.Fatalf("read docs/openapi.json: %v", err)
	}
	schema, err := os.ReadFile(filepath.Join("testdata", "oas-3.1-schema.json"))
	if err != nil {
		t.Fatalf("read the OAS 3.1 schema: %v", err)
	}
	sv, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	const oasURL = "https://spec.openapis.org/oas/3.1/schema/2022-10-07"
	c := jsonschema.NewCompiler()
	if err := c.AddResource(oasURL, sv); err != nil {
		t.Fatalf("add schema: %v", err)
	}
	s, err := c.Compile(oasURL)
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	dv, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("document: %v", err)
	}
	if err := s.Validate(dv); err != nil {
		t.Errorf("docs/openapi.json is not a valid OpenAPI 3.1 document: %v", err)
	}
}

// wantSecurity is the security each access class is published with.
func wantSecurity(a route.Access) []any {
	switch a.Class {
	case route.ClassKeyScope:
		return []any{map[string]any{"session": []any{}}, map[string]any{"apiKey": []any{a.KeyScope}}}
	case route.ClassSession, route.ClassRecovery, route.ClassRecentAuth:
		return []any{map[string]any{"session": []any{}}}
	default:
		return []any{}
	}
}

var loopbackServers = []any{map[string]any{
	"url":         "http://127.0.0.1:{port}",
	"description": "reachable only from loopback inside the container",
	"variables":   map[string]any{"port": map[string]any{"default": "9847"}},
}}

func fixtureEntry(name, path string, a route.Access) route.Entry {
	a.Transport = route.TransportAny
	return route.Entry{
		Name: name, Method: http.MethodPost, Path: path, Kind: route.KindJSON,
		Response: reflect.TypeFor[httpreply.OKBody](), Access: a, Summary: name,
	}
}

func operation(t *testing.T, doc map[string]any, method, path string) map[string]any {
	t.Helper()
	paths, _ := doc["paths"].(map[string]any)
	item, _ := paths[path].(map[string]any)
	op, ok := item[strings.ToLower(method)].(map[string]any)
	if !ok {
		t.Fatalf("no operation %s %s in the document", method, path)
	}
	return op
}

func TestOpenAPI_SecurityMatchesAccess(t *testing.T) {
	fixtures := []struct {
		entry   route.Entry
		servers []any
		ext     map[string]any
	}{
		{
			entry: fixtureEntry("keyScoped", "/api/key-scoped", route.Access{Class: route.ClassKeyScope, KeyScope: "chats:read"}),
			ext:   map[string]any{"class": "key_scope", "key_scope": "chats:read"},
		},
		{
			entry: fixtureEntry("sessionOnly", "/api/session-only", route.Access{Class: route.ClassSession}),
			ext:   map[string]any{"class": "session"},
		},
		{
			entry: fixtureEntry("recentTen", "/api/recent-ten", route.Access{Class: route.ClassRecentAuth, RecentAuth: 10 * time.Minute}),
			ext:   map[string]any{"class": "recent_auth", "recent_auth": "10m"},
		},
		{
			entry: fixtureEntry("recentEight", "/api/recent-eight", route.Access{Class: route.ClassRecentAuth, RecentAuth: 8 * time.Hour}),
			ext:   map[string]any{"class": "recent_auth", "recent_auth": "8h"},
		},
		{
			entry: fixtureEntry("publicOne", "/api/public-one", route.Access{Class: route.ClassPublic}),
			ext:   map[string]any{"class": "public"},
		},
		{
			entry:   fixtureEntry("loopbackOne", "/api/loopback-one", route.Access{Class: route.ClassLoopbackOperator}),
			servers: loopbackServers, ext: map[string]any{"class": "loopback_operator"},
		},
	}
	entries := make([]route.Entry, 0, len(fixtures))
	for _, f := range fixtures {
		entries = append(entries, f.entry)
	}
	doc := generateOpenAPI(t, entries)
	for _, f := range fixtures {
		t.Run(f.entry.Name, func(t *testing.T) {
			op := operation(t, doc, f.entry.Method, f.entry.Path)
			if got := op["security"]; !reflect.DeepEqual(got, wantSecurity(f.entry.Access)) {
				t.Errorf("security = %v, want %v", got, wantSecurity(f.entry.Access))
			}
			if got, _ := op["servers"].([]any); !reflect.DeepEqual(got, f.servers) {
				t.Errorf("servers = %v, want %v", got, f.servers)
			}
			if got := op["x-marotte-access"]; !reflect.DeepEqual(got, f.ext) {
				t.Errorf("x-marotte-access = %v, want %v", got, f.ext)
			}
		})
	}

	committed := docsOpenAPI(t)
	byOp := map[string]route.Entry{}
	for _, e := range apitable.Entries() {
		byOp[strings.ToLower(e.Method)+" "+strings.TrimSuffix(e.Path, "{$}")] = e
	}
	paths, _ := committed["paths"].(map[string]any)
	ops := 0
	for path, item := range paths {
		for method, raw := range item.(map[string]any) {
			op, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			ops++
			e, ok := byOp[method+" "+path]
			if !ok {
				t.Errorf("%s %s: an operation with no table entry", method, path)
				continue
			}
			if got := op["security"]; !reflect.DeepEqual(got, wantSecurity(e.Access)) {
				t.Errorf("%s %s: security %v, want %v", method, path, got, wantSecurity(e.Access))
			}
			if e.Access.Class != route.ClassKeyScope && slices.ContainsFunc(asList(op["security"]), func(alt any) bool {
				_, bearer := alt.(map[string]any)["apiKey"]
				return bearer
			}) {
				t.Errorf("%s %s: a Bearer alternative on a %s operation", method, path, e.Access.Class)
			}
		}
	}
	if ops < 100 {
		t.Errorf("the document has %d operations, want the table's /api/ entries", ops)
	}
	if got := operation(t, committed, http.MethodGet, "/api/openapi.json")["security"]; !reflect.DeepEqual(got, []any{map[string]any{"session": []any{}}}) {
		t.Errorf("GET /api/openapi.json security = %v, want session only", got)
	}
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

// generateOpenAPI runs one generation pass over entries and returns its document.
func generateOpenAPI(t *testing.T, entries []route.Entry) map[string]any {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "openapi.json")
	if err := registryFor(entries, nil).Generate(t.Context(), wiregen.WithTSDir(dir), wiregen.WithTransportImport("../api-client.js"), wiregen.WithOpenAPIFiles(out)); err != nil {
		t.Fatalf("generate: %v", err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read the generated document: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("generated document: %v", err)
	}
	return doc
}
