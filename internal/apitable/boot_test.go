package apitable_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/apitable"
	"github.com/cplieger/marotte/internal/route"
	"github.com/cplieger/marotte/internal/server"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func sealViolations(t *testing.T, err error) []string {
	t.Helper()
	var se *server.SealError
	if !errors.As(err, &se) {
		t.Fatalf("Routes() = %v, want a *server.SealError", err)
	}
	return se.Violations
}

func TestEndpoints_BootBindsEveryEntry(t *testing.T) {
	if _, err := composedServer(t, nil).Routes(); err != nil {
		t.Fatalf("the composed server refuses to seal: %v", err)
	}

	entries := apitable.Entries()
	pick := slices.IndexFunc(entries, func(e route.Entry) bool { return e.Method == http.MethodPost && e.Kind == route.KindJSON })
	if pick < 0 {
		t.Fatal("no POST JSON entry to drop")
	}
	dropped := entries[pick]
	cases := map[string]struct {
		entries []route.Entry
		want    string
	}{
		"an entry removed": {
			entries: slices.Delete(slices.Clone(entries), pick, pick+1),
			want:    dropped.Method + " " + dropped.Path,
		},
		"an entry with no binding": {
			entries: append(slices.Clone(entries), route.Entry{
				Name: "unboundProbe", Method: http.MethodPost, Path: "/api/unbound-probe", Kind: route.KindJSON,
				Access: route.Access{Class: route.ClassSession, Transport: route.TransportAny},
			}),
			want: "POST /api/unbound-probe",
		},
		"a response type that differs": {
			entries: func() []route.Entry {
				es := slices.Clone(entries)
				es[pick].Response = reflect.TypeFor[struct{ Probe int }]()
				return es
			}(),
			want: dropped.Method + " " + dropped.Path,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := composedServer(t, tc.entries).Routes()
			if vs := sealViolations(t, err); !slices.ContainsFunc(vs, func(v string) bool { return strings.Contains(v, tc.want) }) {
				t.Errorf("violations %q do not name %s", vs, tc.want)
			}
		})
	}
}

// TestRegistrar_UndeclaredMethodsNeverReachTheHandler binds a recording probe in place of every
// plain-path binding's handler: each of the seven methods it does not bind answers 405 with the
// bound Allow and never reaches the probe, and each bound method does.
func TestRegistrar_UndeclaredMethodsNeverReachTheHandler(t *testing.T) {
	var reached []string
	plain := 0
	mux := http.NewServeMux()
	g := server.NewRegistrar(mux, nil)
	bound := map[string][]string{}
	for _, b := range bindings(t) {
		if m, _ := route.SplitPattern(b.Pattern); m != "" || b.Any {
			continue
		}
		plain++
		for _, mt := range b.Methods {
			bound[b.Pattern] = append(bound[b.Pattern], mt.Method)
			if mt.Method == http.MethodGet {
				bound[b.Pattern] = append(bound[b.Pattern], http.MethodHead)
			}
		}
		pattern := b.Pattern
		b.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reached = append(reached, r.Method+" "+pattern)
			w.WriteHeader(http.StatusNoContent)
		})
		g.Bind(b)
	}
	if plain < 10 {
		t.Fatalf("found %d plain-path bindings, want the server's full set", plain)
	}
	for pattern, ms := range bound {
		for _, m := range route.AnyMethods() {
			reached = reached[:0]
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(m, concretePath(pattern), http.NoBody))
			if slices.Contains(ms, m) {
				if len(reached) != 1 {
					t.Errorf("%s %s: bound, but the probe saw %v", m, pattern, reached)
				}
				continue
			}
			if rec.Code != http.StatusMethodNotAllowed || len(reached) != 0 {
				t.Errorf("%s %s = %d, probe saw %v; want 405 before the handler", m, pattern, rec.Code, reached)
			}
			if got, want := rec.Header().Get("Allow"), strings.Join(ms, ", "); got != want {
				t.Errorf("%s %s: Allow = %q, want %q", m, pattern, got, want)
			}
		}
	}
}

// TestEndpoints_HEADAnswersAsGET serves HEAD, then GET, on every HEAD entry of the composed server except the
// catch-alls, the streams and the socket, and fails when their status or Content-Type differ, which catches a method
// switch that drops HEAD into an empty 200. Where this server answers the GET with an error a write arm would also
// give, or cannot reach the handler, the package tests pin that HEAD never writes:
// TestKnowledgeAndMemoryRoutes_HEADOnlyReads, TestHandleOne_HEADLeavesTheStoreUnchanged,
// TestFileRoute_HEADAnswersAsGET, TestRepoRoutes_HEADAnswersAsGET and TestMergeRoute_HEADReadsTheStateAndNeverMerges.
func TestEndpoints_HEADAnswersAsGET(t *testing.T) {
	mux, err := composedServer(t, nil).Routes()
	if err != nil {
		t.Fatalf("Routes() = %v", err)
	}
	n := 0
	for _, e := range apitable.Entries() {
		if e.Method != http.MethodHead || e.Any || e.Kind == route.KindEventStream || e.Kind == route.KindWebSocket {
			continue
		}
		n++
		path := concretePath(e.Path)
		head := serveLoopback(mux, http.MethodHead, path)
		get := serveLoopback(mux, http.MethodGet, path)
		if hct, gct := head.Header().Get("Content-Type"), get.Header().Get("Content-Type"); head.Code != get.Code || hct != gct {
			t.Errorf("HEAD %s = %d %q, want GET's %d %q", e.Path, head.Code, hct, get.Code, gct)
		}
	}
	if n < 50 {
		t.Fatalf("served %d HEAD twins, want the table's full set", n)
	}
}

// TestAPIFallback_ShadowsNoEntry addresses every /api/ entry that is not a catch-all and asserts the
// pattern ServeMux dispatches it to is its own, never the /api/ fallback.
func TestAPIFallback_ShadowsNoEntry(t *testing.T) {
	mux, err := composedServer(t, nil).Routes()
	if err != nil {
		t.Fatalf("Routes() = %v", err)
	}
	n := 0
	for _, e := range apitable.Entries() {
		if e.Any || !strings.HasPrefix(e.Path, "/api/") {
			continue
		}
		n++
		_, pattern := mux.Handler(httptest.NewRequest(e.Method, concretePath(e.Path), http.NoBody))
		if _, path := route.SplitPattern(pattern); path != e.Path {
			t.Errorf("%s %s dispatches to %q, want its own pattern", e.Method, e.Path, pattern)
		}
	}
	if n < 100 {
		t.Fatalf("addressed %d /api/ entries, want the table's full set", n)
	}
}

func TestOpenAPI_EmbeddedEqualsDocs(t *testing.T) {
	docs, err := os.ReadFile("../../docs/openapi.json")
	if err != nil {
		t.Fatalf("read docs/openapi.json: %v", err)
	}
	if !bytes.Equal(apitable.OpenAPI, docs) {
		t.Error("internal/apitable/openapi.json differs from docs/openapi.json; run go run ./cmd/wire-codegen")
	}
}

// responseSchema compiles the 2xx JSON schema of one operation of the embedded document.
func responseSchema(t *testing.T, c *jsonschema.Compiler, e route.Entry) *jsonschema.Schema {
	t.Helper()
	status := "200"
	if e.Status != 0 {
		status = strconv.Itoa(e.Status)
	}
	ptr := "openapi.json#/paths/" + escapePointer(openAPIPath(e.Path)) + "/" + strings.ToLower(e.Method) +
		"/responses/" + status + "/content/application~1json/schema"
	s, err := c.Compile(ptr)
	if err != nil {
		t.Fatalf("%s %s: compile %s: %v", e.Method, e.Path, ptr, err)
	}
	return s
}

// openAPIPath drops ServeMux's {$} anchor and trailing-wildcard dots, which OpenAPI paths do not carry.
func openAPIPath(p string) string {
	return strings.ReplaceAll(strings.TrimSuffix(p, "{$}"), "...}", "}")
}

func escapePointer(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

func TestOpenAPI_RealBodiesValidate(t *testing.T) {
	var doc any
	if err := json.Unmarshal(apitable.OpenAPI, &doc); err != nil {
		t.Fatalf("embedded document: %v", err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource("openapi.json", doc); err != nil {
		t.Fatalf("add document: %v", err)
	}
	mux, err := composedServer(t, nil).Routes()
	if err != nil {
		t.Fatalf("Routes() = %v", err)
	}
	validated := 0
	for _, e := range apitable.Entries() {
		if e.Method != http.MethodGet || e.Kind != route.KindJSON || e.Response == nil || !strings.HasPrefix(e.Path, "/api/") {
			continue
		}
		rec := serveLoopback(mux, http.MethodGet, concretePath(e.Path))
		if rec.Code < 200 || rec.Code > 299 {
			continue
		}
		var body any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Errorf("GET %s: 2xx body is not JSON: %v", e.Path, err)
			continue
		}
		if err := responseSchema(t, c, e).Validate(body); err != nil {
			t.Errorf("GET %s: body %s does not validate: %v", e.Path, rec.Body.String(), err)
		}
		validated++
	}
	if validated < 25 {
		t.Errorf("validated %d bodies, want the composed server's readable GETs", validated)
	}
}
