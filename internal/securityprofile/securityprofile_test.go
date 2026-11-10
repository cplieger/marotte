package securityprofile

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
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
	path, err := policyfile.PathFor(policyfile.ScopeUser, policyfile.Roots{KiroHome: filepath.Join(home, ".kiro"), WorkDir: s.WorkDir})
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

// Customize from Delegated keeps the classifier: the approve-for-me preset's evaluate rule is copied
// into the user file rather than dropped as a rule the file cannot hold.
func TestCustomize_CopiesEvaluateRule(t *testing.T) {
	s, userPath := storeOver(t, []marotte.PolicyRule{
		{Capability: "fs_write", Effect: "allow", Scope: "session", Source: "preset:edit-workspace", Match: []string{"/w/**"}},
		{Capability: "all", Effect: "evaluate", Scope: "session", Source: "preset:approve-for-me"},
	})
	if err := s.Select(t.Context(), policyfile.ProfileDelegated); err != nil {
		t.Fatalf("Setup: Select(delegated) = %v", err)
	}
	if err := s.Customize(t.Context()); err != nil {
		t.Fatalf("Customize = %v", err)
	}
	f, err := policyfile.Load(userPath)
	if err != nil {
		t.Fatalf("load the user file: %v", err)
	}
	if !slices.ContainsFunc(f.Rules, func(r policyfile.Rule) bool { return r.Capability == "all" && r.Effect == "evaluate" }) {
		t.Errorf("user file rules = %+v, want the all evaluate rule kept", f.Rules)
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
	if err := policyfile.Mutate(t.Context(), userPath, func(f *policyfile.File) (bool, error) { return f.Upsert(&kasRule) }); err != nil {
		t.Fatalf("Setup: write KAS's rule: %v", err)
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

// The rollback after a failed persist writes back the whole file, not just its rules: a
// `policies:` reference dropped here would narrow grants with no message.
func TestCustomize_AFailedPersistRestoresTheUserFileWithItsOtherKeys(t *testing.T) {
	s, userPath := storeOver(t, []marotte.PolicyRule{
		{Capability: "fs_read", Effect: "allow", Scope: "session", Source: "preset:read-workspace", Match: []string{"/w/**"}},
	})
	if err := os.MkdirAll(filepath.Dir(userPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userPath, []byte("policies:\n  - dev-shell\nrules:\n  - capability: shell\n    effect: ask\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.ConfigDir, "config.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := s.Customize(t.Context())
	if err == nil || errors.Is(err, ErrRestoreFailed) {
		t.Fatalf("Customize = %v, want the persist error with the file restored", err)
	}

	got, err := os.ReadFile(userPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte("dev-shell")) {
		t.Errorf("restored user file lost its policies key:\n%s", got)
	}
	f, err := policyfile.Load(userPath)
	if err != nil || len(f.Rules) != 1 || f.Rules[0].Capability != "shell" {
		t.Errorf("restored rules = %+v (err %v), want only the original shell rule", f, err)
	}
}

// swapSeam replaces a package seam for one test.
func swapSeam[T any](t *testing.T, seam *T, with T) {
	t.Helper()
	prev := *seam
	*seam = with
	t.Cleanup(func() { *seam = prev })
}

// writeUserFile stages the user permissions file's text.
func writeUserFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func capabilities(t *testing.T, path string) []string {
	t.Helper()
	f, err := policyfile.Load(path)
	if err != nil {
		t.Fatalf("load the user file: %v", err)
	}
	out := make([]string, 0, len(f.Rules))
	for i := range f.Rules {
		out = append(out, f.Rules[i].Capability)
	}
	slices.Sort(out)
	return out
}

func TestCustomize_AFailedPersistKeepsARuleWrittenMeanwhile(t *testing.T) {
	s, userPath := storeOver(t, []marotte.PolicyRule{
		{Capability: "fs_read", Effect: "allow", Scope: "session", Source: "preset:read-workspace", Match: []string{"/w/**"}},
	})
	writeUserFile(t, userPath, "rules:\n  - capability: shell\n    effect: ask\n")
	persistFailed := errors.New("persist failed")
	swapSeam(t, &persistCustom, func(*Store, context.Context, string) error {
		kas := policyfile.Rule{Capability: "web_fetch", Effect: "allow"}
		if err := policyfile.Mutate(t.Context(), userPath, func(f *policyfile.File) (bool, error) { return f.Upsert(&kas) }); err != nil {
			t.Errorf("write KAS's rule: %v", err)
		}
		return persistFailed
	})

	err := s.Customize(t.Context())

	if !errors.Is(err, persistFailed) || errors.Is(err, ErrRestoreFailed) {
		t.Errorf("Customize = %v, want the persist error with the copied rules taken back out", err)
	}
	if got, want := capabilities(t, userPath), []string{"shell", "web_fetch"}; !slices.Equal(got, want) {
		t.Errorf("the user file holds %v, want the earlier rule and KAS's %v and no copied preset rule", got, want)
	}
}

func TestSeedEdit_RecordsOnlyTheLastAttemptsAdditions(t *testing.T) {
	seededRule := policyfile.Rule{Capability: "fs_read", Effect: "allow", Match: []string{"/w/**"}}
	other := policyfile.Rule{Capability: "web_search", Effect: "allow"}
	var added []policyfile.Rule
	edit := seedEdit([]policyfile.Rule{seededRule, other}, &added)

	if changed, err := edit(&policyfile.File{Rules: []policyfile.Rule{}}); err != nil || !changed {
		t.Fatalf("the first attempt = (%v, %v), want (true, nil)", changed, err)
	}
	changed, err := edit(&policyfile.File{Rules: []policyfile.Rule{seededRule}})

	if err != nil || !changed {
		t.Errorf("the retried attempt = (%v, %v), want (true, nil)", changed, err)
	}
	if len(added) != 1 || added[0].Capability != other.Capability {
		t.Errorf("added = %+v, want only %s, the one rule the retried attempt added", added, other.Capability)
	}
}

func TestCustomize_ABusyFileReturnsErrBusyAndSwitchesNothing(t *testing.T) {
	s, userPath := storeOver(t, []marotte.PolicyRule{
		{Capability: "fs_read", Effect: "allow", Scope: "session", Source: "preset:read-workspace", Match: []string{"/w/**"}},
	})
	if err := s.Select(t.Context(), policyfile.ProfileReadOnly); err != nil {
		t.Fatalf("Setup: Select = %v", err)
	}
	const body = "rules:\n  - capability: shell\n    effect: ask\n"
	writeUserFile(t, userPath, body)
	swapSeam(t, &mutatePolicy, func(context.Context, string, func(*policyfile.File) (bool, error)) error {
		return policyfile.ErrBusy
	})

	err := s.Customize(t.Context())

	if !errors.Is(err, policyfile.ErrBusy) {
		t.Errorf("Customize = %v, want an error matching policyfile.ErrBusy", err)
	}
	if got := s.Active(t.Context()); got != policyfile.ProfileReadOnly {
		t.Errorf("Active = %q, want the profile unchanged %q", got, policyfile.ProfileReadOnly)
	}
	if got, _ := os.ReadFile(userPath); string(got) != body {
		t.Errorf("the user file holds %q, want it untouched", got)
	}
}
