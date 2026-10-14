package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// With no resolver configured, KiroHome falls back to $HOME/.kiro on
// every call (no caching), so a t.Setenv("HOME", ...) is reflected.
func TestKiroHome_fallsBackToHomeDotKiro(t *testing.T) {
	prev := kiroHomeResolver
	kiroHomeResolver = nil // force the os.UserHomeDir fallback path
	t.Cleanup(func() { kiroHomeResolver = prev })

	t.Setenv("HOME", "/test-home")
	if got := KiroHome(); got != "/test-home/.kiro" {
		t.Errorf("KiroHome() = %q, want %q", got, "/test-home/.kiro")
	}
}

// When a resolver is configured, KiroHome returns its result and caches it
// via sync.Once, so the resolver runs exactly once across repeated calls.
func TestKiroHome_resolverResultCachedOnce(t *testing.T) {
	prevResolver := kiroHomeResolver
	prevHome := kiroHome
	t.Cleanup(func() {
		kiroHomeResolver = prevResolver
		kiroHome = prevHome
		kiroHomeOnce = sync.Once{}
	})

	calls := 0
	kiroHomeResolver = func() string {
		calls++
		return "/resolved/kiro"
	}
	kiroHomeOnce = sync.Once{} // reset so the resolver fires fresh

	if got := KiroHome(); got != "/resolved/kiro" {
		t.Fatalf("KiroHome() = %q, want %q", got, "/resolved/kiro")
	}
	if got := KiroHome(); got != "/resolved/kiro" {
		t.Fatalf("KiroHome() second call = %q, want %q", got, "/resolved/kiro")
	}
	if calls != 1 {
		t.Errorf("resolver invoked %d times, want 1 (sync.Once caching)", calls)
	}
}

// The path helpers join under KiroHome()'s "steering" / "settings" subdirectories, names
// kiro-cli and marotte must agree on.
func TestKiroPathHelpers(t *testing.T) {
	prev := kiroHomeResolver
	kiroHomeResolver = nil
	t.Cleanup(func() { kiroHomeResolver = prev })
	t.Setenv("HOME", "/cfg") // KiroHome() => /cfg/.kiro via the fallback

	if got, want := KiroSteeringPath("custom.md"), "/cfg/.kiro/steering/custom.md"; got != want {
		t.Errorf("KiroSteeringPath = %q, want %q", got, want)
	}
	if got, want := KiroSteeringPath("sub/file.md"), "/cfg/.kiro/steering/sub/file.md"; got != want {
		t.Errorf("KiroSteeringPath(nested) = %q, want %q", got, want)
	}
	if got, want := KiroSettingsPath("global.json"), "/cfg/.kiro/settings/global.json"; got != want {
		t.Errorf("KiroSettingsPath = %q, want %q", got, want)
	}
}

// KiroHomeDir refuses the relative fallback a writer would resolve against its cwd.
func TestKiroHomeDir_RefusesARelativeHome(t *testing.T) {
	prev := kiroHomeResolver
	kiroHomeResolver = nil
	t.Cleanup(func() { kiroHomeResolver = prev })

	t.Run("no home", func(t *testing.T) {
		t.Setenv("HOME", "")
		dir, err := KiroHomeDir()
		if !errors.Is(err, errNoKiroHome) || dir != "" {
			t.Errorf("KiroHomeDir() = %q, %v; want \"\", errNoKiroHome", dir, err)
		}
	})
	t.Run("home set", func(t *testing.T) {
		t.Setenv("HOME", "/cfg")
		dir, err := KiroHomeDir()
		if err != nil || dir != "/cfg/.kiro" {
			t.Errorf("KiroHomeDir() = %q, %v; want /cfg/.kiro, nil", dir, err)
		}
	})
}

// ResolveKiroHome follows KAS's rule, so marotte and the agent read one tree.
func TestResolveKiroHome_MatchesTheAgentsRule(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(outside, "home-link")
	if err := os.Symlink(home, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	fallback := filepath.Join(home, ".kiro")
	for _, tc := range []struct {
		name, kiroHome, want string
	}{
		{"unset", "", fallback},
		{"blank", "  ", fallback},
		{"relative", "state/kiro", fallback},
		{"filesystem root", "/", fallback},
		{"equals home", home, fallback},
		{"contains home", filepath.Dir(home), fallback},
		{"symlink to home", link, fallback},
		{"the default spelled out", fallback, fallback},
		{"inside home", filepath.Join(home, "kiro-state"), filepath.Join(home, "kiro-state")},
		{"outside home, not yet created", filepath.Join(outside, "kiro", "x"), filepath.Join(outside, "kiro", "x")},
		{"uncleaned", outside + "/kiro/../kiro2/", filepath.Join(outside, "kiro2")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveKiroHome(tc.kiroHome, home); got != tc.want {
				t.Errorf("ResolveKiroHome(%q, %q) = %q, want %q", tc.kiroHome, home, got, tc.want)
			}
		})
	}
}

// KAS's O9u tests path.relative with startsWith(".."), so a HOME whose relative path from the
// candidate only starts with those bytes ("..home") is outside it, and KAS honours the candidate.
func TestResolveKiroHome_DotDotNamedComponentIsOutside(t *testing.T) {
	base := t.TempDir()
	for _, tc := range []struct {
		name, home string
	}{
		{"first component", filepath.Join(base, "..home")},
		{"deeper component", filepath.Join(base, "..home", "..user")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveKiroHome(base, tc.home); got != base {
				t.Errorf("ResolveKiroHome(%q, %q) = %q, want %q", base, tc.home, got, base)
			}
		})
	}
}

// The not-yet-created tail keeps its order behind the resolved prefix: reversed, link/kiro/x
// would compare as real/x/kiro, which is home, and fall back where KAS accepts it.
func TestResolveKiroHome_KeepsTheMissingTailInOrder(t *testing.T) {
	base := t.TempDir()
	realDir := filepath.Join(base, "real")
	home := filepath.Join(realDir, "x", "kiro")
	if err := os.MkdirAll(home, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	candidate := filepath.Join(link, "kiro", "x")
	if got := ResolveKiroHome(candidate, home); got != candidate {
		t.Errorf("ResolveKiroHome(%q, %q) = %q, want %q", candidate, home, got, candidate)
	}
}
