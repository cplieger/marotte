package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/route"
)

type (
	regBody  struct{ A string }
	regReply struct{ B string }
	regOther struct{ C string }
)

func jsonEntry(name, method, path string) route.Entry {
	return route.Entry{
		Name: name, Method: method, Path: path, Kind: route.KindJSON,
		Response: reflect.TypeFor[regReply](),
		Access:   route.Access{Class: route.ClassSession, Transport: route.TransportAny, Safe: method == http.MethodGet || method == http.MethodHead},
	}
}

// withTwin appends e's HEAD twin when e is a GET entry, as apitable.Entries does.
func withTwin(es ...route.Entry) []route.Entry {
	var out []route.Entry
	for _, e := range es {
		out = append(out, e)
		if e.Method == http.MethodGet {
			twin := e
			twin.Method = http.MethodHead
			out = append(out, twin)
		}
	}
	return out
}

func anyEntries(path string) []route.Entry {
	var out []route.Entry
	for _, m := range route.AnyMethods() {
		out = append(out, route.Entry{
			Name: "any" + m, Method: m, Path: path, Kind: route.KindRaw, Any: true,
			Access: route.Access{Class: route.ClassPublic, Transport: route.TransportAny, Safe: m == http.MethodGet || m == http.MethodHead},
		})
	}
	return out
}

func status(code int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) })
}

func violations(t *testing.T, err error) []string {
	t.Helper()
	var se *SealError
	if !errors.As(err, &se) {
		t.Fatalf("seal() = %v, want a *SealError", err)
	}
	return se.Violations
}

func hasViolation(vs []string, parts ...string) bool {
	return slices.ContainsFunc(vs, func(v string) bool {
		for _, p := range parts {
			if !strings.Contains(v, p) {
				return false
			}
		}
		return true
	})
}

func TestRegistrar_SealPassesWhenEveryEntryIsBound(t *testing.T) {
	entries := withTwin(jsonEntry("getX", http.MethodGet, "/api/x"), jsonEntry("putX", http.MethodPut, "/api/x"),
		jsonEntry("getRun", http.MethodGet, "/api/runs/{id}"))
	entries = append(entries, anyEntries("/api/")...)
	g := NewRegistrar(http.NewServeMux(), entries)
	g.Bind(route.Methods("/api/x", status(http.StatusOK),
		route.Typed[struct{}, regReply](http.MethodGet), route.Typed[struct{}, regReply](http.MethodPut)))
	g.Bind(route.JSON[struct{}, regReply]("GET /api/runs/{id}", status(http.StatusOK)))
	g.Bind(route.AnyMethod("/api/", status(http.StatusNotFound)))
	if err := g.seal(); err != nil {
		t.Fatalf("seal() = %v, want nil", err)
	}
}

func TestRegistrar_SealListsEveryViolation(t *testing.T) {
	entries := withTwin(jsonEntry("getX", http.MethodGet, "/api/x"), jsonEntry("getUnbound", http.MethodGet, "/api/unbound"),
		jsonEntry("getTyped", http.MethodGet, "/api/typed"))
	entries = append(entries, jsonEntry("postTwice", http.MethodPost, "/api/twice"))
	g := NewRegistrar(http.NewServeMux(), entries)
	g.Bind(route.JSON[struct{}, regReply]("GET /api/x", status(http.StatusOK)))
	g.Bind(route.JSON[struct{}, regReply]("GET /api/no-entry", status(http.StatusOK)))
	g.Bind(route.JSON[struct{}, regOther]("GET /api/typed", status(http.StatusOK)))
	g.Bind(route.JSON[struct{}, regReply]("POST /api/twice", status(http.StatusOK)))
	g.Bind(route.JSON[struct{}, regReply]("POST /api/twice", status(http.StatusOK)))
	g.Bind(route.JSON[struct{}, regReply]("GET example.com/api/host", status(http.StatusOK)))

	vs := violations(t, g.seal())
	for name, parts := range map[string][]string{
		"an unbound entry":    {"GET /api/unbound", "unbound"},
		"a binding no entry":  {"GET /api/no-entry", "no entry"},
		"a response mismatch": {"GET /api/typed", "Response"},
		"a second binding":    {"POST /api/twice", "bound twice"},
		"a host pattern":      {"example.com/api/host", "host"},
	} {
		if !hasViolation(vs, parts...) {
			t.Errorf("%s: no violation naming %q in %q", name, parts, vs)
		}
	}
}

func TestRegistrar_EveryComparedFieldIsAViolation(t *testing.T) {
	base := jsonEntry("postX", http.MethodPost, "/api/x/{id}")
	base.Params = reflect.TypeFor[struct {
		ID string `json:"id"`
	}]()
	base.Request = reflect.TypeFor[regBody]()
	base.Query = reflect.TypeFor[regOther]()
	base.Status = http.StatusAccepted
	base.Headers = []route.Header{{Name: "SSE-Client", Arg: "sseClient", Required: true, MaxBytes: 64}}
	bind := func() route.Binding {
		b := route.JSON[regBody, regReply]("POST /api/x/{id}", status(http.StatusAccepted))
		b = route.WithParams[struct {
			ID string `json:"id"`
		}](b)
		b = route.WithQuery[regOther](b)
		b = route.WithStatus(b, http.StatusAccepted)
		return route.WithHeaders(b, base.Headers...)
	}
	cases := map[string]func(*route.Entry){
		"Kind":     func(e *route.Entry) { e.Kind = route.KindRaw },
		"Params":   func(e *route.Entry) { e.Params = nil },
		"Request":  func(e *route.Entry) { e.Request = reflect.TypeFor[regOther]() },
		"Query":    func(e *route.Entry) { e.Query = nil },
		"Response": func(e *route.Entry) { e.Response = reflect.TypeFor[regOther]() },
		"Status":   func(e *route.Entry) { e.Status = http.StatusOK },
		"Headers":  func(e *route.Entry) { e.Headers = nil },
	}
	t.Run("matching", func(t *testing.T) {
		g := NewRegistrar(http.NewServeMux(), []route.Entry{base})
		g.Bind(bind())
		if err := g.seal(); err != nil {
			t.Errorf("seal() = %v, want nil", err)
		}
	})
	for field, mutate := range cases {
		t.Run(field, func(t *testing.T) {
			e := base
			mutate(&e)
			g := NewRegistrar(http.NewServeMux(), []route.Entry{e})
			g.Bind(bind())
			if vs := violations(t, g.seal()); !hasViolation(vs, "POST /api/x/{id}", field) {
				t.Errorf("violations %q name no %s mismatch", vs, field)
			}
		})
	}
}

func TestRegistrar_ACatchAllNeedsExactlyItsSevenEntries(t *testing.T) {
	short := anyEntries("/x/")[:6]
	g := NewRegistrar(http.NewServeMux(), short)
	g.Bind(route.AnyMethod("/x/", status(http.StatusOK)))
	if vs := violations(t, g.seal()); !hasViolation(vs, "OPTIONS /x/", "no entry") {
		t.Errorf("violations %q, want the missing OPTIONS entry named", vs)
	}
}

func TestRegistrar_BindAfterSealMountsNothing(t *testing.T) {
	mux := http.NewServeMux()
	g := NewRegistrar(mux, withTwin(jsonEntry("getLate", http.MethodGet, "/api/late")))
	if err := g.seal(); err == nil {
		t.Fatal("seal() = nil with an unbound entry")
	}
	g.Bind(route.JSON[struct{}, regReply]("GET /api/late", status(http.StatusTeapot)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/late", http.NoBody))
	if rec.Code == http.StatusTeapot {
		t.Error("a binding after seal was mounted")
	}
}

func TestRegistrar_PlainPathFilterAnswersBeforeTheHandler(t *testing.T) {
	calls := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(http.StatusOK) })
	mux := http.NewServeMux()
	g := NewRegistrar(mux, nil)
	g.Bind(route.Methods("/api/settings", h,
		route.Typed[struct{}, regReply](http.MethodGet), route.Typed[regBody, regReply](http.MethodPut)))

	for _, m := range route.AnyMethods() {
		t.Run(m, func(t *testing.T) {
			before := calls
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(m, "/api/settings", http.NoBody))
			bound := m == http.MethodGet || m == http.MethodHead || m == http.MethodPut
			if bound {
				if rec.Code != http.StatusOK || calls != before+1 {
					t.Errorf("%s = %d with %d handler calls, want the handler's 200", m, rec.Code, calls-before)
				}
				return
			}
			if rec.Code != http.StatusMethodNotAllowed || calls != before {
				t.Fatalf("%s = %d with %d handler calls, want 405 before the handler", m, rec.Code, calls-before)
			}
			if got := rec.Header().Get("Allow"); got != "GET, HEAD, PUT" {
				t.Errorf("Allow = %q, want %q", got, "GET, HEAD, PUT")
			}
			if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"method not allowed","code":"method_not_allowed"}` {
				t.Errorf("body = %s, want the coded method refusal", got)
			}
		})
	}
}

func TestRegistrar_ACatchAllServesEveryMethodUnfiltered(t *testing.T) {
	mux := http.NewServeMux()
	g := NewRegistrar(mux, nil)
	g.Bind(route.AnyMethod("/api/", status(http.StatusNotFound)))
	for _, m := range append(route.AnyMethods(), "TRACE") {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(m, "/api/typo", http.NoBody))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s /api/typo = %d, want the catch-all's own 404", m, rec.Code)
		}
	}
}

func TestRegistrar_EntryLookupIsTheDispatchedEntry(t *testing.T) {
	entries := withTwin(jsonEntry("getRun", http.MethodGet, "/api/runs/{id}"), jsonEntry("getX", http.MethodGet, "/api/x"))
	entries = append(entries, jsonEntry("putX", http.MethodPut, "/api/x"))
	entries = append(entries, anyEntries("/api/")...)
	mux := http.NewServeMux()
	g := NewRegistrar(mux, entries)
	g.Bind(route.JSON[struct{}, regReply]("GET /api/runs/{id}", status(http.StatusOK)))
	g.Bind(route.Methods("/api/x", status(http.StatusOK),
		route.Typed[struct{}, regReply](http.MethodGet), route.Typed[struct{}, regReply](http.MethodPut)))
	g.Bind(route.AnyMethod("/api/", status(http.StatusNotFound)))
	if err := g.seal(); err != nil {
		t.Fatalf("seal() = %v", err)
	}
	cases := []struct {
		method, path string
		want         route.Key
		ok           bool
	}{
		{http.MethodGet, "/api/runs/r1", route.Key{Method: "GET", Path: "/api/runs/{id}"}, true},
		{http.MethodHead, "/api/runs/r1", route.Key{Method: "HEAD", Path: "/api/runs/{id}"}, true},
		{http.MethodPost, "/api/runs/r1", route.Key{Method: "POST", Path: "/api/"}, true},
		{http.MethodPut, "/api/x", route.Key{Method: "PUT", Path: "/api/x"}, true},
		{http.MethodDelete, "/api/x", route.Key{}, false},
		{"TRACE", "/api/typo", route.Key{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			_, pattern := mux.Handler(httptest.NewRequest(tc.method, tc.path, http.NoBody))
			e, ok := g.entryOf(pattern, tc.method)
			if ok != tc.ok || (ok && e.Key() != tc.want) {
				t.Errorf("entryOf(%q, %s) = %+v %v, want %+v %v", pattern, tc.method, e.Key(), ok, tc.want, tc.ok)
			}
		})
	}
	if _, ok := g.entryOf("", http.MethodGet); ok {
		t.Error(`entryOf("") answered ok`)
	}
}
