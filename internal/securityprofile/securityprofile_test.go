package securityprofile

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/policyfile"
)

func storeOver(t *testing.T, live []marotte.PolicyRule) (*Store, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	s := &Store{
		List:      func(context.Context, string) ([]marotte.PolicyRule, error) { return live, nil },
		ConfigDir: t.TempDir(),
		WorkDir:   t.TempDir(),
	}
	path, err := policyfile.PathFor(policyfile.ScopeUser, policyfile.Roots{Home: home, WorkDir: s.WorkDir})
	if err != nil {
		t.Fatalf("Setup: user path: %v", err)
	}
	return s, path
}

func TestCustomize_SwitchesToCustomWithThePresetsInForce(t *testing.T) {
	s, userPath := storeOver(t, []marotte.PolicyRule{
		{Capability: "fs_read", Effect: "allow", Scope: "session", Source: "preset:read-workspace", Match: []string{"/w/**"}},
		{Capability: "shell", Effect: "allow", Scope: "session", Source: "consent"},
	})
	if got := s.Active(t.Context()); got != policyfile.DefaultProfile {
		t.Fatalf("Setup: Active = %q, want the default %q", got, policyfile.DefaultProfile)
	}

	if err := s.Customize(t.Context()); err != nil {
		t.Fatalf("Customize = %v", err)
	}

	if got := s.Active(t.Context()); got != policyfile.ProfileCustom {
		t.Errorf("Active after Customize = %q, want %q", got, policyfile.ProfileCustom)
	}
	f, err := policyfile.Load(userPath)
	if err != nil {
		t.Fatalf("load the user file: %v", err)
	}
	if len(f.Rules) != 1 || f.Rules[0].Capability != "fs_read" || !slices.Equal(f.Rules[0].Match, []string{"/w/**"}) {
		t.Errorf("user file rules = %+v, want only the preset's fs_read [/w/**] (a one-session consent is not the profile's)", f.Rules)
	}
}

// No preset rule in force cannot be told from a session not yet started.
func TestCustomize_RefusesWithoutALivePolicy(t *testing.T) {
	for _, tc := range []struct {
		list func(context.Context, string) ([]marotte.PolicyRule, error)
		name string
	}{
		{name: "no reader"},
		{name: "the reader fails", list: func(context.Context, string) ([]marotte.PolicyRule, error) {
			return nil, errors.New("bridge down")
		}},
		{name: "no preset rule in force", list: func(context.Context, string) ([]marotte.PolicyRule, error) {
			return []marotte.PolicyRule{{Capability: "fs_write", Effect: "ask", Scope: "kiro", Source: "kiro-scope"}}, nil
		}},
		{name: "a session still on another profile", list: func(context.Context, string) ([]marotte.PolicyRule, error) {
			return []marotte.PolicyRule{{Capability: "all", Effect: "allow", Scope: "session", Source: "preset:" + policyfile.PresetAllowAll}}, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, userPath := storeOver(t, nil)
			s.List = tc.list

			if err := s.Customize(t.Context()); !errors.Is(err, ErrNoLivePolicy) {
				t.Fatalf("Customize = %v, want ErrNoLivePolicy", err)
			}
			if got := s.Active(t.Context()); got == policyfile.ProfileCustom {
				t.Error("a refused Customize switched the profile to custom")
			}
			if f, err := policyfile.Load(userPath); err != nil || len(f.Rules) != 0 {
				t.Errorf("a refused Customize wrote the user file (rules %v, err %v)", f, err)
			}
		})
	}
}

// A second Customize would save over the rule KAS wrote after the first switch.
func TestEnsureCustom_ConcurrentCallersSwitchOnce(t *testing.T) {
	s, _ := storeOver(t, nil)
	var lists atomic.Int32
	second := make(chan struct{})
	s.List = func(context.Context, string) ([]marotte.PolicyRule, error) {
		if lists.Add(1) == 1 {
			// Hold the first switch open long enough for an unserialized second one to read too.
			select {
			case <-second:
			case <-time.After(300 * time.Millisecond):
			}
		} else {
			close(second)
		}
		return []marotte.PolicyRule{{Capability: "fs_read", Effect: "allow", Scope: "session", Source: "preset:read-workspace"}}, nil
	}

	var wg sync.WaitGroup
	results := make([]bool, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Go(func() { results[i], errs[i] = s.EnsureCustom(t.Context()) })
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("EnsureCustom #%d = %v", i, err)
		}
	}
	if got := lists.Load(); got != 1 || results[0] == results[1] {
		t.Errorf("two concurrent EnsureCustom read the live policy %d times and switched %v, want one read and one switch", got, results)
	}
	if switched, err := s.EnsureCustom(t.Context()); err != nil || switched {
		t.Errorf("EnsureCustom on custom = (%v, %v), want (false, nil)", switched, err)
	}
}

// The list stands in for a session that has not yet reopened on Custom.
func TestCustomize_AStaleRequestAfterTheSwitchLeavesTheUserFileAlone(t *testing.T) {
	s, userPath := storeOver(t, []marotte.PolicyRule{
		{Capability: "fs_read", Effect: "allow", Scope: "session", Source: "preset:read-workspace", Match: []string{"/w/**"}},
	})
	if switched, err := s.EnsureCustom(t.Context()); err != nil || !switched {
		t.Fatalf("Setup: EnsureCustom = (%v, %v), want (true, nil)", switched, err)
	}
	kasRule := policyfile.Rule{Capability: "shell", Effect: "allow", Match: []string{"head *"}}
	f, err := policyfile.Load(userPath)
	if err != nil {
		t.Fatalf("Setup: load the user file: %v", err)
	}
	if _, err := f.Upsert(&kasRule); err != nil {
		t.Fatalf("Setup: upsert: %v", err)
	}
	if err := policyfile.Save(t.Context(), userPath, f); err != nil {
		t.Fatalf("Setup: save KAS's rule: %v", err)
	}
	want, err := os.ReadFile(userPath)
	if err != nil {
		t.Fatalf("Setup: read the user file: %v", err)
	}

	if err := s.Customize(t.Context()); !errors.Is(err, ErrNoLivePolicy) {
		t.Errorf("Customize on custom = %v, want ErrNoLivePolicy", err)
	}

	if got, err := os.ReadFile(userPath); err != nil || !bytes.Equal(got, want) {
		t.Errorf("a stale Customize rewrote the user file (err %v):\n%s\nwant\n%s", err, got, want)
	}
}
