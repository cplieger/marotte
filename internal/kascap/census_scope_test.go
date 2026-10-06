package kascap

import (
	"slices"
	"testing"
)

// A later `r` parameter outside the binding's block is a different variable,
// the 2.26.1 _kiro/memory/* handler table shape. The template literal before
// the binding also pins that a `${` brace is skipped.
func TestInitScopeKeys_IgnoresAShadowingParameterPastTheBindingsBlock(t *testing.T) {
	src := "class A{async initialize(t){w.info(`${x}`);let r=t.clientCapabilities?._meta?.kiro;r?.hooks;}" +
		"h={\"_kiro/memory/list\":t=>this.c(t,r=>Promise.resolve(r.list(t)))}}"
	got := initScopeKeys(t, src)
	if want := []string{"hooks"}; !slices.Equal(got, want) {
		t.Errorf("initScopeKeys(%q) = %v, want %v", src, got, want)
	}
}

// Two binding sites, the 2.27.0 shape: each contributes its own reads.
func TestInitScopeKeys_ReadsEveryBindingSite(t *testing.T) {
	src := "function c(e){let t=e.clientCapabilities?._meta?.kiro;return t.configurationState===!0}" +
		"class A{async initialize(t){let r=t.clientCapabilities?._meta?.kiro;r?.hooks;}}"
	got := initScopeKeys(t, src)
	if want := []string{"configurationState", "hooks"}; !slices.Equal(got, want) {
		t.Errorf("initScopeKeys(%q) = %v, want %v", src, got, want)
	}
}

// A key read through the generic computed-member resolver, the 2.27.0
// backgroundExecution shape, with and without a fallback argument.
func TestComputedResolverKeys_ReadsTheLiteralKeyAtEachCallSite(t *testing.T) {
	src := `function VZe(e,t,r){return e.data[t]?.enabled??r}` +
		`let C=VZe(m,"backgroundExecution"),u=VZe(a,'other',r?.other);`
	got := computedResolverKeys(t, src)
	if want := []string{"backgroundExecution", "other"}; !slices.Equal(got, want) {
		t.Errorf("computedResolverKeys(%q) = %v, want %v", src, got, want)
	}
}
