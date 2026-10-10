package command

// The shape pin: a mechanical guard against an aggregate dependency interface growing back in
// internal/command or internal/translate.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	// maxEmbeds is the most interfaces one interface may embed: Bridge's 3, the seams of one
	// concrete type.
	maxEmbeds = 3
	// maxMethods is the widest transitive method surface allowed: Bridge's 12, the real per-chat
	// ACP bridge.
	maxMethods = 12
)

type ifaceDecl struct {
	pkgDir   string
	file     string
	name     string
	embeds   []string
	direct   int
	external int
}

// TestNoFatDependencyAggregate fails if any interface in command or translate
// exceeds either shape bound.
func TestNoFatDependencyAggregate(t *testing.T) {
	decls := collectInterfaces(t, ".", "../translate")
	if len(decls) == 0 {
		t.Fatal("parsed zero interfaces; the pin is not reading any sources")
	}

	byName := make(map[string]ifaceDecl, len(decls))
	for _, d := range decls {
		byName[d.pkgDir+"."+d.name] = d
	}

	for _, d := range decls {
		if len(d.embeds) > maxEmbeds {
			t.Errorf("%s: interface %s embeds %d interfaces (%s), max %d — an interface aggregating that many roles is a DI container, and each consumer should name only the roles it uses",
				d.file, d.name, len(d.embeds), strings.Join(d.embeds, ", "), maxEmbeds)
		}
		if n := transitiveMethods(byName, d, map[string]bool{}); n > maxMethods {
			t.Errorf("%s: interface %s declares %d methods transitively, max %d — split it by role at its consumers",
				d.file, d.name, n, maxMethods)
		}
	}
}

// transitiveMethods counts d's own methods plus those of every embedded
// interface declared in the same package. An embedded interface from another
// package counts as 1, which is the floor rather than the truth: the packages
// under this pin only embed same-package interfaces, so a cross-package embed
// appearing here is itself worth a look.
func transitiveMethods(byName map[string]ifaceDecl, d ifaceDecl, seen map[string]bool) int {
	key := d.pkgDir + "." + d.name
	if seen[key] {
		return 0
	}
	seen[key] = true
	total := d.direct + d.external
	for _, e := range d.embeds {
		if sub, ok := byName[d.pkgDir+"."+e]; ok {
			total += transitiveMethods(byName, sub, seen)
		}
	}
	return total
}

func collectInterfaces(t *testing.T, dirs ...string) []ifaceDecl {
	t.Helper()
	var out []ifaceDecl
	for _, dir := range dirs {
		fset := token.NewFileSet()
		// ReadDir + ParseFile rather than the deprecated parser.ParseDir; go/packages would
		// type-check a whole program for a syntax question.
		ents, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, ent := range ents {
			name := ent.Name()
			if ent.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				ts, ok := n.(*ast.TypeSpec)
				if !ok {
					return true
				}
				it, ok := ts.Type.(*ast.InterfaceType)
				if !ok {
					return true
				}
				out = append(out, describe(dir, name, ts.Name.Name, it))
				return true
			})
		}
	}
	return out
}

func describe(dir, file, name string, it *ast.InterfaceType) ifaceDecl {
	d := ifaceDecl{pkgDir: dir, file: filepath.Join(dir, file), name: name}
	for _, f := range it.Methods.List {
		if len(f.Names) > 0 {
			d.direct++
			continue
		}
		switch e := f.Type.(type) {
		case *ast.Ident:
			d.embeds = append(d.embeds, e.Name)
		case *ast.SelectorExpr:
			d.embeds = append(d.embeds, exprName(e))
			d.external++
		default:
			d.direct++
		}
	}
	return d
}

func exprName(e *ast.SelectorExpr) string {
	if x, ok := e.X.(*ast.Ident); ok {
		return x.Name + "." + e.Sel.Name
	}
	return e.Sel.Name
}
