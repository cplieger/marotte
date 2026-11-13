package applogin

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/cplieger/access"
	"github.com/cplieger/access/account"
)

func bearerRequest(value string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if value != "" {
		r.Header.Set("Authorization", value)
	}
	return r
}

func TestAgentKey_VerifyOnlyItsOwnKey(t *testing.T) {
	var exported []string
	v, err := NewAgentKeyVerifier(func(k, val string) error {
		if k != AgentKeyEnv {
			t.Errorf("setenv key %q, want %s", k, AgentKeyEnv)
		}
		exported = append(exported, val)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(exported) != 1 {
		t.Fatalf("setenv called %d times, want 1", len(exported))
	}
	key := exported[0]
	p, ok, err := v.Verify(t.Context(), bearerRequest("Bearer "+key))
	if !ok || err != nil || p.Method != MethodAgent || p.User == nil || !p.User.Enabled {
		t.Fatalf("Verify(agent key) = (%+v, %t, %v), want the agent principal", p, ok, err)
	}
	sameLength := key[:len(key)-1] + string(rune('0'+(key[len(key)-1]-'0'+1)%10))
	for _, c := range []struct{ name, header string }{
		{"no_header", ""},
		{"basic", "Basic " + key},
		{"same_length_wrong", "Bearer " + sameLength},
		{"other_length_wrong", "Bearer " + key + "00"},
		{"empty", "Bearer "},
	} {
		if p, ok, err := v.Verify(t.Context(), bearerRequest(c.header)); ok || err != nil || p.User != nil {
			t.Errorf("%s: Verify = (%+v, %t, %v), want (zero, false, nil)", c.name, p, ok, err)
		}
	}
}

// TestAgentKey_OwnerKeyStillAuthenticates sends an owner key the way the agent key is sent; the
// agent verifier passes it on and the library's key verifier grants it.
func TestAgentKey_OwnerKeyStillAuthenticates(t *testing.T) {
	h := newHarness(t)
	si := h.setupOwner(t.Context())
	created, err := h.svc.CreateAPIKey(t.Context(), account.As(h.principal(si.Token)), si.User.ID, account.NewKey{Label: "k", Scopes: []string{"tools"}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := h.authn.Authenticate(bearerRequest("Bearer " + created.Plaintext.Reveal()))
	if err != nil || !p.IsAPIKey() || p.Method != access.MethodAPIKey {
		t.Errorf("an owner key = (%v, %v), want an API-key principal", p.Method, err)
	}
	p, err = h.authn.Authenticate(bearerRequest("Bearer " + h.agentKy))
	if err != nil || p.Method != MethodAgent {
		t.Errorf("the agent key = (%v, %v), want MethodAgent", p.Method, err)
	}
}

func TestAgentKey_FailingSetenvReturnsNoVerifier(t *testing.T) {
	boom := errors.New("test: setenv")
	v, err := NewAgentKeyVerifier(func(string, string) error { return boom })
	if !errors.Is(err, boom) || v != nil {
		t.Errorf("NewAgentKeyVerifier = (%v, %v), want no verifier and the setenv error", v, err)
	}
}

func TestAgentKey_VerifierHoldsNoSecret(t *testing.T) {
	var key string
	v, err := NewAgentKeyVerifier(func(_, val string) error { key = val; return nil })
	if err != nil {
		t.Fatal(err)
	}
	av := v.(*agentKeyVerifier)
	secrets, hashes := 0, 0
	var walk func(rv reflect.Value)
	walk = func(rv reflect.Value) {
		switch rv.Kind() {
		case reflect.Pointer:
			if !rv.IsNil() {
				walk(rv.Elem())
			}
		case reflect.Slice:
			for i := range rv.Len() {
				walk(rv.Index(i))
			}
		case reflect.String:
			if rv.String() == key {
				t.Error("a field holds the plaintext")
			}
		case reflect.Struct:
			switch rv.Type() {
			case secretType:
				if !rv.IsZero() {
					secrets++
				}
				return
			case hashType:
				if !rv.IsZero() {
					hashes++
				}
				return
			}
			for _, fv := range rv.Fields() {
				walk(fv)
			}
		}
	}
	walk(reflect.ValueOf(av).Elem())
	if secrets != 0 || hashes != 1 {
		t.Errorf("the verifier holds %d Secret and %d Hash values, want 0 and 1 (non-zero)", secrets, hashes)
	}
	if n := len(av.hash.Reveal()); n != 64 {
		t.Errorf("the held hash is %d bytes, want 64 hex", n)
	}
}

// TestAgentKey_RevealOnlyAtExport: agentkey.go imports no crypto/subtle and holds one Reveal,
// inside NewAgentKeyVerifier.
func TestAgentKey_RevealOnlyAtExport(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "agentkey.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range f.Imports {
		if strings.Trim(imp.Path.Value, `"`) == "crypto/subtle" {
			t.Error("agentkey.go imports crypto/subtle")
		}
	}
	var sites []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Reveal" {
				sites = append(sites, fn.Name.Name)
			}
			return true
		})
	}
	if len(sites) != 1 || sites[0] != "NewAgentKeyVerifier" {
		t.Errorf("Reveal sites in agentkey.go = %v, want [NewAgentKeyVerifier]", sites)
	}
}
