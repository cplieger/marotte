package chat

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// allowedBareN is every production signature in this package that may take a
// parameter spelled `n`, with what that `n` counts. R1 deletes the second
// COORDINATE system, so the rule is narrower than "no counts": a page size and a
// byte cap are values, an ENTRY count crossing a signature is a coordinate the log
// already spells as seq. A site added here needs a reason a reader can check.
var allowedBareN = map[string]string{
	"WithEntryFileCap": "a BYTE cap on the whole log, not a count of anything in it",
	"WithChatFileCap":  "a BYTE cap on the chat file, not a count of anything in it",
	"nthIndex":         "counts occurrences of one byte in a string",
	"lastNthIndex":     "counts occurrences of one byte in a string",
	"ordinalAsInt":     "converts ONE ordinal, so the parameter is that ordinal",
}

var integerTypes = map[string]bool{
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
}

func TestNoBareCountParameter(t *testing.T) {
	names, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	var files []string
	for _, e := range names {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files = append(files, name)
	}
	if len(files) == 0 {
		t.Fatal("no production files parsed, so this gate measured nothing")
	}
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			for _, field := range fn.Type.Params.List {
				id, ok := field.Type.(*ast.Ident)
				if !ok || !integerTypes[id.Name] {
					continue
				}
				for _, param := range field.Names {
					if param.Name != "n" {
						continue
					}
					if _, allowed := allowedBareN[fn.Name.Name]; allowed {
						continue
					}
					pos := fset.Position(param.Pos())
					t.Errorf("%s:%d: %s takes a bare `n %s`; name what it counts, or allowlist it with a reason",
						filepath.Base(pos.Filename), pos.Line, fn.Name.Name, id.Name)
				}
			}
		}
	}
}

func TestAllowedBareNSitesExist(t *testing.T) {
	names, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	declared := map[string]bool{}
	for _, e := range names {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok {
				declared[fn.Name.Name] = true
			}
		}
	}
	for name := range allowedBareN {
		if !declared[name] {
			t.Errorf("%s is allowlisted and no longer exists, so the entry is stale", name)
		}
	}
}
