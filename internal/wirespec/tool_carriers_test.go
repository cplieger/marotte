package wirespec

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/go/wiregen/v2"
)

// A tool card's title and loaded list are served at read time by marotte.ToolCard, never stored, so every
// frame and typed reply that can carry a tool entry must be built through it. The scan finds each registered
// event payload or type whose fields reach one of toolShapes (an event whose payload is a shape counts too).
// A new carrier fails here until its name is a key of toolCarriers. The chat page, both turn ranges and the
// export are untyped replies the registry cannot see, pinned by TestHandleChatPage_ServesTheCardTitle,
// TestHandleTurnRange_StampsAResultWhoseCallPrecedesAfter, TestRunTurnRange_StampsAResultWhoseCallPrecedesAfter
// and TestHandleExport_JSONCarriesTheCardTitle.

// toolShapes are the marotte types a tool's card fields ride on.
var toolShapes = []string{"Entry", "EntryToolCall", "EntryToolResult", "ToolCall", "ToolProgressPayload"}

// toolCarriers is every registered carrier, by package-qualified name, with the site that stamps it and
// the tests that pin the stamp.
var toolCarriers = map[string]string{
	"marotte.EntryAppendedPayload": "translate appendedEntry: TestPublishAppended_StampsAToolCallCopy, " +
		"TestPublishSealed_StampsAResultFromTheCallItSettles, TestPublishSealed_TitlesACloseAbortAsTheLiveCardWas, " +
		"agent TestRunAppendAfterClosed_TitlesALateResultFromItsCall",
	"marotte.RunStepTranscript": "agent stepTranscript (ServeToolEntries): TestStepTranscript_ServesTheCardTitle, " +
		"TestStepTranscript_ServesAReplayedCallsCardTitle",
	"marotte.ToolProgressPayload": "translate deltaCard: TestToolProgress_CarriesTheCardTitleWhenItChanges",
	"marotte.TurnClosedPayload":   "carries only its turn_close (PublishAppended)",
	"marotte.TurnOpenedPayload":   "carries only its turn_open (PublishAppended)",
}

// shapeIndex is every struct type of the scanned packages by "<pkg path>.<name>", with the import
// path each of its files names an alias by.
type shapeIndex struct {
	structs map[string]*ast.StructType
	imports map[string]map[string]string
}

// modulePath is read off a registration, so a module rename cannot leave the scan reading nothing.
var modulePath = strings.TrimSuffix(marottePkgPath, "/internal/marotte")

func loadShapes(t *testing.T, pkgs []string) *shapeIndex {
	t.Helper()
	idx := &shapeIndex{structs: map[string]*ast.StructType{}, imports: map[string]map[string]string{}}
	fset := token.NewFileSet()
	for _, pkg := range pkgs {
		dir := filepath.Join("..", "..", strings.TrimPrefix(pkg, modulePath+"/"))
		files, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, f := range files {
			name := f.Name()
			if f.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse %s: %v", name, err)
			}
			aliases := map[string]string{}
			for _, im := range file.Imports {
				path, _ := strconv.Unquote(im.Path.Value)
				alias := path[strings.LastIndex(path, "/")+1:]
				if im.Name != nil {
					alias = im.Name.Name
				}
				aliases[alias] = path
			}
			for _, decl := range file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.TYPE {
					continue
				}
				for _, spec := range gen.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if st, isStruct := ts.Type.(*ast.StructType); ok && isStruct {
						key := pkg + "." + ts.Name.Name
						idx.structs[key], idx.imports[key] = st, aliases
					}
				}
			}
		}
	}
	return idx
}

// reaches reports whether struct key holds a tool shape in any field, through pointers, slices,
// arrays, maps and nested structs of the scanned packages.
func (idx *shapeIndex) reaches(key string, seen map[string]bool) bool {
	if seen[key] {
		return false
	}
	seen[key] = true
	st := idx.structs[key]
	if st == nil {
		return false
	}
	pkg := key[:strings.LastIndex(key, ".")]
	for _, f := range st.Fields.List {
		if ref, ok := idx.named(f.Type, pkg, idx.imports[key]); ok {
			if ref.pkg == marottePkgPath && slices.Contains(toolShapes, ref.name) {
				return true
			}
			if idx.reaches(ref.pkg+"."+ref.name, seen) {
				return true
			}
		}
	}
	return false
}

type typeName struct{ pkg, name string }

// named is the named type an expression's elements are, unwrapping pointers, slices, arrays and a
// map's value.
func (idx *shapeIndex) named(e ast.Expr, pkg string, aliases map[string]string) (typeName, bool) {
	switch x := e.(type) {
	case *ast.Ident:
		return typeName{pkg, x.Name}, true
	case *ast.SelectorExpr:
		if p, ok := x.X.(*ast.Ident); ok && aliases[p.Name] != "" {
			return typeName{aliases[p.Name], x.Sel.Name}, true
		}
	case *ast.StarExpr:
		return idx.named(x.X, pkg, aliases)
	case *ast.ArrayType:
		return idx.named(x.Elt, pkg, aliases)
	case *ast.MapType:
		return idx.named(x.Value, pkg, aliases)
	}
	return typeName{}, false
}

// registeredTypes is every registered type and event payload, once each.
func registeredTypes() []wiregen.Type {
	types := slices.Clone(Registry().Types)
	for _, e := range Registry().Events {
		if !slices.Contains(types, e.Data) {
			types = append(types, e.Data)
		}
	}
	return types
}

func TestRegistry_EveryToolEntryCarrierIsStamped(t *testing.T) {
	types := registeredTypes()
	var pkgs []string
	for _, wt := range types {
		if strings.HasPrefix(wt.PkgPath, modulePath+"/") && !slices.Contains(pkgs, wt.PkgPath) {
			pkgs = append(pkgs, wt.PkgPath)
		}
	}
	idx := loadShapes(t, pkgs)
	if idx.structs[marottePkgPath+".EntryToolCall"] == nil {
		t.Fatal("the scan found no marotte.EntryToolCall; the scan is broken, not the registry")
	}
	var frames []wiregen.Type
	for _, e := range Registry().Events {
		frames = append(frames, e.Data)
	}
	found := map[string]bool{}
	for _, wt := range types {
		key := wt.PkgPath + "." + wt.Name
		short := wt.PkgPath[strings.LastIndex(wt.PkgPath, "/")+1:] + "." + wt.Name
		isShapeFrame := wt.PkgPath == marottePkgPath && slices.Contains(toolShapes, wt.Name) && slices.Contains(frames, wt)
		if !isShapeFrame && !idx.reaches(key, map[string]bool{}) {
			continue
		}
		found[short] = true
		if _, ok := toolCarriers[short]; !ok {
			t.Errorf("%s carries a tool entry or frame but toolCarriers does not list it: build it through "+
				"marotte.StampToolEntry or marotte.ToolCard, pin the stamp with a test, and list it here "+
				"with that site and test", short)
		}
	}
	for name := range toolCarriers {
		if !found[name] {
			t.Errorf("toolCarriers lists %s, which no registered type reaching a tool shape is: drop it", name)
		}
	}
}
