package route_test

import (
	"net/http"
	"reflect"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/route"
)

type (
	req    struct{ A string }
	resp   struct{ B string }
	params struct {
		ID string `json:"id"`
	}
	query struct {
		Q string `json:"q,omitempty"`
	}
)

var nop = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

func TestSplitPattern(t *testing.T) {
	cases := []struct{ pattern, method, path string }{
		{"GET /api/runs/{id}", "GET", "/api/runs/{id}"},
		{"POST /api/sync", "POST", "/api/sync"},
		{"/api/settings", "", "/api/settings"},
		{"/", "", "/"},
		{"example.com/x", "", "example.com/x"},
		{"GET example.com/x", "GET", "example.com/x"},
	}
	for _, tc := range cases {
		t.Run(tc.pattern, func(t *testing.T) {
			m, p := route.SplitPattern(tc.pattern)
			if m != tc.method || p != tc.path {
				t.Errorf("SplitPattern(%q) = %q, %q; want %q, %q", tc.pattern, m, p, tc.method, tc.path)
			}
		})
	}
}

func TestJSON_TypesAndEmptyBodies(t *testing.T) {
	b := route.JSON[req, resp]("PUT /api/x", nop)
	if b.Kind != route.KindJSON || b.Pattern != "PUT /api/x" || b.Any || len(b.Methods) != 1 {
		t.Fatalf("JSON binding = %+v", b)
	}
	mt := b.Methods[0]
	if mt.Method != http.MethodPut || mt.Request != reflect.TypeFor[req]() || mt.Response != reflect.TypeFor[resp]() {
		t.Errorf("method types = %+v", mt)
	}
	empty := route.JSON[struct{}, struct{}]("POST /api/y", nop).Methods[0]
	if empty.Request != nil || empty.Response != nil {
		t.Errorf("struct{} bodies = %v, %v; want nil, nil", empty.Request, empty.Response)
	}
	if got := route.JSON[struct{}, []resp]("GET /api/z", nop).Methods[0].Response; got != reflect.TypeFor[[]resp]() {
		t.Errorf("slice response = %v", got)
	}
}

func TestModifiers_SetEveryMethod(t *testing.T) {
	h := route.Header{Name: "SSE-Client", Arg: "sseClient", Required: true, MinBytes: 1, MaxBytes: 64}
	b := route.WithHeaders(route.WithStatus(route.WithParams[params](route.WithQuery[query](
		route.JSON[struct{}, resp]("GET /api/x/{id}", nop),
	)), http.StatusAccepted), h)
	mt := b.Methods[0]
	if mt.Query != reflect.TypeFor[query]() || mt.Params != reflect.TypeFor[params]() ||
		mt.Status != http.StatusAccepted || !slices.Equal(mt.Headers, []route.Header{h}) {
		t.Errorf("modified method types = %+v", mt)
	}
	plain := route.Typed[struct{}, resp](http.MethodGet).WithQuery(reflect.TypeFor[query]()).
		WithParams(reflect.TypeFor[params]()).WithStatus(http.StatusOK).WithHeaders(h)
	if plain.Query != reflect.TypeFor[query]() || plain.Params != reflect.TypeFor[params]() || plain.Status != http.StatusOK {
		t.Errorf("MethodTypes modifiers = %+v", plain)
	}
}

func TestNonJSONKinds(t *testing.T) {
	cases := []struct {
		b       route.Binding
		kind    route.Kind
		methods []string
	}{
		{route.Raw("/metrics", nop), route.KindRaw, []string{"GET"}},
		{route.Raw("POST /api/file/upload", nop), route.KindRaw, []string{"POST"}},
		{route.Stream("/api/events", nop), route.KindEventStream, []string{"GET"}},
		{route.WebSocket("/api/shell/ws", nop), route.KindWebSocket, []string{"GET"}},
		{route.Redirect("GET /x", nop), route.KindRedirect, []string{"GET"}},
	}
	for _, tc := range cases {
		t.Run(tc.b.Pattern, func(t *testing.T) {
			var got []string
			for _, mt := range tc.b.Methods {
				got = append(got, mt.Method)
				if mt.Request != nil || mt.Response != nil {
					t.Errorf("%s carries a JSON body type: %+v", tc.b.Pattern, mt)
				}
			}
			if tc.b.Kind != tc.kind || !slices.Equal(got, tc.methods) {
				t.Errorf("binding = kind %v methods %v, want %v %v", tc.b.Kind, got, tc.kind, tc.methods)
			}
		})
	}
}

func TestAnyMethod_TheSevenMethods(t *testing.T) {
	b := route.AnyMethod("/api/", nop)
	if !b.Any || len(b.Methods) != 0 {
		t.Errorf("AnyMethod = %+v, want Any with no method types", b)
	}
	want := []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}
	if got := route.AnyMethods(); !slices.Equal(got, want) {
		t.Errorf("AnyMethods() = %v, want %v", got, want)
	}
}

func TestKeyScopes_EachOnce(t *testing.T) {
	scopes := route.KeyScopes()
	seen := map[string]bool{}
	for _, s := range scopes {
		if s == "" || seen[s] {
			t.Errorf("KeyScopes has an empty or repeated scope %q", s)
		}
		seen[s] = true
	}
	for _, s := range []string{"chats:read", "runs:read", "runs:answer", "runs:control", "tools", "metrics:read", "notify"} {
		if !seen[s] {
			t.Errorf("KeyScopes lacks %q", s)
		}
	}
}

func TestEntry_Key(t *testing.T) {
	e := route.Entry{Method: "GET", Path: "/api/x"}
	if got := e.Key(); got != (route.Key{Method: "GET", Path: "/api/x"}) {
		t.Errorf("Key() = %+v", got)
	}
}
