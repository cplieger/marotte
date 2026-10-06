package wirespec

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/wiregen/v3"
)

// An SSE payload type missing from the registry gets no decoder, so the client hand-declares
// its shape and both halves still compile. The registry is walked from both ends: registered
// but unbound, and declared but unregistered.

// marottePkgPath is read off a registration, so a package rename cannot leave these tests
// scanning nothing.
var marottePkgPath = wiregen.TypeRef[marotte.ChatHeader]().PkgPath

// emptySignalPayloads are payload types deliberately unregistered: empty structs for pure
// invalidation events, with nothing to decode. TestPayloadExemptions_AreStillEmptyStructs
// fails once one gains a field.
var emptySignalPayloads = []string{
	"CompactionStartedPayload",
	"ForgesChangedPayload",
	"HooksChangedPayload",
	"MCPConfigChangedPayload",
	"MCPPoolChangedPayload",
	"PowersChangedPayload",
	"RecipesChangedPayload",
	"SettingsUpdatedPayload",
	"SlashCommandsChangedPayload",
	"SteeringIssuesChangedPayload",
	"SubjectChangedPayload",
}

// unboundDataPayloads are data-carrying payloads that are NOT registered: broadcast by
// production code with shapes hand-written in static-src/bus.ts and no runtime validation.
// Named here because an unnamed known gap looks like an unnoticed one. Shrinking this list
// is the fix; growing it requires a reason.
var unboundDataPayloads = []string{
	"ChatStatusPayload",
	"MCPPrewarmPayload",
	"ModeChangedPayload",
	"WorkingLabelPayload",
}

// pendingClientBindings are registered payload types (decoder generated) deliberately
// without an SSE binding yet, because binding would break the other language's build
// TODAY. Empty is its normal state; the two tests below hold every entry to that claim.
var pendingClientBindings = []string{}

// declaredMarottePayloads parses internal/marotte and returns every exported *Payload type
// with its struct field count. It reads SOURCE because the question is what a list does
// not mention, and parses every file directly: a loader would drop a build-tagged payload
// that is still on the wire where it builds.
func declaredMarottePayloads(t *testing.T) map[string]int {
	t.Helper()
	dir := filepath.Join("..", "marotte")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read internal/marotte: %v", err)
	}
	fset := token.NewFileSet()
	out := map[string]int{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || !ts.Name.IsExported() || !strings.HasSuffix(ts.Name.Name, "Payload") {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}
				out[ts.Name.Name] = st.Fields.NumFields()
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("parsed internal/marotte and found no exported *Payload types; the scan is broken, not the registry")
	}
	return out
}

// registeredMarottePayloads returns the names of the marotte.*Payload types in the
// registry.
func registeredMarottePayloads(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, wt := range Registry().Types {
		if wt.PkgPath == marottePkgPath && strings.HasSuffix(wt.Name, "Payload") {
			out = append(out, wt.Name)
		}
	}
	return out
}

// TestRegistry_EveryDeclaredPayloadIsRegisteredOrExempt pins that a new marotte.*Payload
// cannot reach the SSE wire without a registration or an exemption entry.
func TestRegistry_EveryDeclaredPayloadIsRegisteredOrExempt(t *testing.T) {
	declared := declaredMarottePayloads(t)
	registered := registeredMarottePayloads(t)

	for name := range declared {
		switch {
		case slices.Contains(registered, name):
		case slices.Contains(emptySignalPayloads, name):
		case slices.Contains(unboundDataPayloads, name):
		default:
			t.Errorf("marotte.%s is declared but has no wire registration.\n"+
				"Add wiregen.TypeRef[marotte.%s]() to wireTypes plus its {EventType, TypeName} entry in sseEvents,\n"+
				"or — if it is an empty invalidation signal with no field to decode — add it to emptySignalPayloads with that reason.",
				name, name)
		}
	}
}

// TestRegistry_EveryRegisteredPayloadHasAnSSEBinding walks the other direction: a payload
// in wireTypes with no sseEvents entry is a type generated for no decoder.
func TestRegistry_EveryRegisteredPayloadHasAnSSEBinding(t *testing.T) {
	r := Registry()
	bound := make([]string, 0, len(r.SSEEvents))
	for _, e := range r.SSEEvents {
		bound = append(bound, e.TypeName)
	}

	for _, name := range registeredMarottePayloads(t) {
		if slices.Contains(pendingClientBindings, name) {
			continue
		}
		if !slices.Contains(bound, name) {
			t.Errorf("marotte.%s is registered in wireTypes but bound to no SSE event.\n"+
				"Add its {EventType: \"…\", TypeName: %q} entry to sseEvents, or drop the registration.",
				name, name)
		}
	}
}

// TestPendingClientBindings_AreRegisteredAndUnbound holds that list to its reason: an
// unregistered entry belongs in unboundDataPayloads, a bound one has spent its exemption.
func TestPendingClientBindings_AreRegisteredAndUnbound(t *testing.T) {
	registered := registeredMarottePayloads(t)
	bound := make([]string, 0, len(Registry().SSEEvents))
	for _, e := range Registry().SSEEvents {
		bound = append(bound, e.TypeName)
	}
	for _, name := range pendingClientBindings {
		if !slices.Contains(registered, name) {
			t.Errorf("pendingClientBindings names marotte.%s, which is not registered in wireTypes.\n"+
				"This list is for a REGISTERED payload whose SSE binding is waiting on the client; "+
				"an unregistered one belongs in unboundDataPayloads.", name)
		}
		if slices.Contains(bound, name) {
			t.Errorf("pendingClientBindings names marotte.%s, but it IS bound now; drop the entry (the exemption is spent)", name)
		}
	}
}

// TestRegistry_EverySSEBindingNamesARegisteredType guards the typo: TypeName is a string.
func TestRegistry_EverySSEBindingNamesARegisteredType(t *testing.T) {
	r := Registry()
	names := make([]string, 0, len(r.Types))
	for _, wt := range r.Types {
		names = append(names, wt.Name)
	}

	for _, e := range r.SSEEvents {
		if !slices.Contains(names, e.TypeName) {
			t.Errorf("SSE event %q binds TypeName %q, which is not a registered type (typo, or the registration was removed)",
				e.EventType, e.TypeName)
		}
	}
}

// TestRegistry_NoDuplicateSSEEventTypes pins one decoder per event; a duplicate is a
// silent last-wins.
func TestRegistry_NoDuplicateSSEEventTypes(t *testing.T) {
	seen := map[string]string{}
	for _, e := range Registry().SSEEvents {
		if prev, dup := seen[e.EventType]; dup {
			t.Errorf("SSE event %q is bound twice: %q then %q (the generated registry keeps one)",
				e.EventType, prev, e.TypeName)
		}
		seen[e.EventType] = e.TypeName
	}
}

// TestPayloadExemptions_AreStillEmptyStructs fails once an exempt payload gains a field.
func TestPayloadExemptions_AreStillEmptyStructs(t *testing.T) {
	declared := declaredMarottePayloads(t)
	for _, name := range emptySignalPayloads {
		fields, ok := declared[name]
		if !ok {
			t.Errorf("emptySignalPayloads names marotte.%s, which no longer exists in internal/marotte; drop the entry", name)
			continue
		}
		if fields != 0 {
			t.Errorf("marotte.%s is exempt as an empty invalidation signal but now has %d field(s).\n"+
				"It carries data, so it needs a registration and an SSE binding; remove it from emptySignalPayloads.",
				name, fields)
		}
	}
}

// TestPayloadExemptions_AreNotStale removes an exemption whose type was registered or is
// gone, so the lists cannot hide a fixed gap.
func TestPayloadExemptions_AreNotStale(t *testing.T) {
	declared := declaredMarottePayloads(t)
	registered := registeredMarottePayloads(t)

	for _, list := range []struct {
		name    string
		entries []string
	}{
		{"emptySignalPayloads", emptySignalPayloads},
		{"unboundDataPayloads", unboundDataPayloads},
	} {
		for _, name := range list.entries {
			if _, ok := declared[name]; !ok {
				t.Errorf("%s names marotte.%s, which is not declared in internal/marotte any more; drop the entry",
					list.name, name)
			}
			if slices.Contains(registered, name) {
				t.Errorf("%s names marotte.%s, but it IS registered now; drop the entry (the exemption is spent)",
					list.name, name)
			}
		}
	}
}

// TestRegistry_DeclaresNoTypeOrDecoderMappings guards a symptomless defect: wiregen's
// TypeMappings and DecoderMappings key on the alias-resolved type, and from Go 1.27
// json.RawMessage is an ALIAS of jsontext.Value, so a "encoding/json.RawMessage" key
// silently stops matching. marotte registers no mapping, a property of the registry value.
// If a mapping is ever needed, register BOTH spellings.
func TestRegistry_DeclaresNoTypeOrDecoderMappings(t *testing.T) {
	r := Registry()
	if len(r.TypeMappings) != 0 {
		t.Errorf("Registry().TypeMappings has %d entries, want 0; a key naming a stdlib type "+
			"must carry both the alias spelling and the resolved one: %v", len(r.TypeMappings), r.TypeMappings)
	}
	if len(r.DecoderMappings) != 0 {
		t.Errorf("Registry().DecoderMappings has %d entries, want 0; same rule: %v",
			len(r.DecoderMappings), r.DecoderMappings)
	}
}
