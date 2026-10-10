package agent

// The removed Run-now surface, asserted absent: a compile error cannot catch a route
// re-registered or a method name reintroduced as a constant.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestHooksRoutes_HaveNoTriggerVerb pins the route table through the real mux: POST
// /api/hooks/{id}/trigger was Run-now's entry point.
func TestHooksRoutes_HaveNoTriggerVerb(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	(&Settings{}).registerHooksRoutes(mux)

	for _, tc := range []struct {
		name    string
		method  string
		path    string
		matched bool
	}{
		{"the list read survives", http.MethodGet, "/api/hooks", true},
		{"the enabled toggle survives", http.MethodPost, "/api/hooks/abc/enabled", true},
		{"Run now is gone", http.MethodPost, "/api/hooks/abc/trigger", false},
		{"and gone for every id", http.MethodPost, "/api/hooks/YS5qc29uI2hvb2stMA/trigger", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(tc.method, tc.path, nil)
			_, pattern := mux.Handler(r)
			if got := pattern != ""; got != tc.matched {
				t.Errorf("%s %s matched pattern %q; want matched=%v",
					tc.method, tc.path, pattern, tc.matched)
			}
		})
	}
}

// TestHookMethodConstants_OmitTheRunNowPair pins the wire vocabulary by reading declarations,
// so comments may still name executeHook while a const may not.
func TestHookMethodConstants_OmitTheRunNowPair(t *testing.T) {
	t.Parallel()
	const gone = "_kiro/hooks/triggerHook, _kiro/hooks/executeHook"

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the runtime package directory: %v", err)
	}
	fset := token.NewFileSet()
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, pErr := parser.ParseFile(fset, name, nil, 0)
		if pErr != nil {
			t.Fatalf("parse %s: %v", name, pErr)
		}
		scanned++
		for _, spec := range constStrings(file) {
			if spec.value == "_kiro/hooks/triggerHook" || spec.value == "_kiro/hooks/executeHook" {
				t.Errorf("%s declares %s = %q; the Run-now pair (%s) is deleted, "+
					"and declaring either name is how the shell path comes back",
					name, spec.name, spec.value, gone)
			}
		}
	}
	// A scan that read nothing would pass for the wrong reason.
	if scanned == 0 {
		t.Fatal("scanned no production sources; the guard is vacuous")
	}
}

type constString struct {
	name  string
	value string
}

func constStrings(file *ast.File) []constString {
	var out []constString
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				out = append(out, constString{name: name.Name, value: strings.Trim(lit.Value, `"`)})
			}
		}
	}
	return out
}
