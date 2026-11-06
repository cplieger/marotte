package kascap

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

// modelCallBase is the static name of KAS's model-call error base. A turn that breaks on a model call
// carries its subclass's name as the display_error errorType; tool errors extend its parent and stay in
// the tool result, so they never name a broken turn.
const modelCallBase = `static{a(this,"KiroQError")}`

// bundleModelCallClasses returns the names of the classes under modelCallBase, at any depth, that name
// themselves and write their own reader sentence.
func bundleModelCallClasses(t *testing.T, src string) []string {
	t.Helper()
	classes := jsClasses(src)
	under := make(map[string]bool)
	for _, c := range classes {
		if strings.Contains(c.head, modelCallBase) {
			under[c.id] = true
		}
	}
	if len(under) != 1 {
		t.Fatalf("found %d classes declaring %s, want exactly 1; re-anchor on KAS's model-call error base", len(under), modelCallBase)
	}
	for grew := true; grew; {
		grew = false
		for _, c := range classes {
			if !under[c.id] && under[c.base] {
				under[c.id], grew = true, true
			}
		}
	}
	seen := make(map[string]bool)
	for _, c := range classes {
		readable := strings.Contains(c.head, "userFacingSessionErrorMessage") || strings.Contains(c.head, "userMessage")
		if under[c.id] && c.name != "" && readable {
			seen[c.name] = true
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

func TestModelCallClassExtractor(t *testing.T) {
	const src = `Wi=class extends Error{static{a(this,"KiroError")}},` +
		`oo=class extends Wi{static{a(this,"KiroQError")}requestId;withRequestId(e){return e}},` +
		`epe=class extends oo{static{a(this,"NetworkUnreachableError")}name="NetworkUnreachableError";get userFacingSessionErrorMessage(){return"x"}},` +
		`Ul=class extends oo{name="UsageLimitReachedError";userMessage;constructor(e){super(e)}},` +
		`Dp=class extends epe{name="DeepNetworkError";get userFacingSessionErrorMessage(){return"y"}},` +
		`Qn=class extends oo{name="SilentError"},Qi=class extends epe{get userFacingSessionErrorMessage(){return"z"}},` +
		`Sx=class extends Wi{name="StringNotFoundError";get userFacingSessionErrorMessage(){return"tool"}}`
	got := bundleModelCallClasses(t, src)
	want := []string{"DeepNetworkError", "NetworkUnreachableError", "UsageLimitReachedError"}
	if !slices.Equal(got, want) {
		t.Errorf("bundleModelCallClasses = %v, want %v", got, want)
	}
}

// TestModelCallClassesAreKnown fails when KAS adds a model-call error class that knownClasses does not
// list: the turn-info panel's engine-error row would then be blank for the turn it broke.
func TestModelCallClassesAreKnown(t *testing.T) {
	known := declaredMapKeys(t, knownClassesSource, "knownClasses")
	forEachBundle(t, func(t *testing.T, b bundle) {
		for _, class := range bundleModelCallClasses(t, b.src) {
			if !known[class] {
				t.Errorf("KAS model-call error class %q is missing from knownClasses (%s); add it", class, knownClassesSource)
			}
		}
	})
}
