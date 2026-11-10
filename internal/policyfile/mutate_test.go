package policyfile

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// swapAfterEdit replaces the seam between an attempt's edit and its compare for one test.
func swapAfterEdit(t *testing.T, fn func(path string)) {
	t.Helper()
	prev := afterEdit
	afterEdit = fn
	t.Cleanup(func() { afterEdit = prev })
}

// saveRules sets the rules of the file Load reads for path to f's, as a write from scratch would.
func saveRules(ctx context.Context, path string, f *File) error {
	return Mutate(ctx, path, func(cur *File) (bool, error) {
		cur.Rules = f.Rules
		return true, nil
	})
}

func ruleSet(rules []Rule) []string {
	out := make([]string, 0, len(rules))
	for i := range rules {
		out = append(out, signature(&rules[i]))
	}
	slices.Sort(out)
	return out
}

func loadRules(t *testing.T, path string) []Rule {
	t.Helper()
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return f.Rules
}

var (
	ruleAsk   = Rule{Capability: "shell", Effect: EffectAsk}
	ruleKAS   = Rule{Capability: "fs_read", Effect: EffectAllow, Match: []string{"/kas/**"}}
	ruleAdded = Rule{Capability: "web_fetch", Effect: EffectAllow}
)

// upsertCounting is an edit that adds rule and records each call's rules.
func upsertCounting(rule Rule, seen *[][]Rule) func(*File) (bool, error) {
	return func(f *File) (bool, error) {
		*seen = append(*seen, slices.Clone(f.Rules))
		return f.Upsert(&rule)
	}
}

func TestMutate_KeepsWhatAnotherWriterWroteAfterTheRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), filename)
	writeFixture(t, path, "rules:\n  - capability: shell\n    effect: ask\n")
	first := true
	swapAfterEdit(t, func(p string) {
		if !first {
			return
		}
		first = false
		writeFixture(t, p, "rules:\n  - capability: shell\n    effect: ask\n"+
			"  - capability: fs_read\n    effect: allow\n    match:\n      - /kas/**\n"+
			"workspaceTrusted: true\n")
	})
	var seen [][]Rule

	if err := Mutate(t.Context(), path, upsertCounting(ruleAdded, &seen)); err != nil {
		t.Fatalf("Mutate = %v, want nil", err)
	}

	if want := ruleSet([]Rule{ruleAsk, ruleKAS, ruleAdded}); !slices.Equal(ruleSet(loadRules(t, path)), want) {
		t.Errorf("rules after Mutate = %v, want the file's, the other writer's and the edit's %v", ruleSet(loadRules(t, path)), want)
	}
	if doc := readRaw(t, path); doc["workspaceTrusted"] != true {
		t.Errorf("workspaceTrusted = %#v, want the other writer's true kept", doc["workspaceTrusted"])
	}
	if len(seen) != 2 {
		t.Fatalf("the edit ran %d times, want 2 (the first attempt discarded)", len(seen))
	}
	if !slices.Equal(ruleSet(seen[1]), ruleSet([]Rule{ruleAsk, ruleKAS})) {
		t.Errorf("the second edit saw %v, want the other writer's rule in it", ruleSet(seen[1]))
	}
}

func TestMutate_GivesUpWithErrBusy(t *testing.T) {
	path := filepath.Join(t.TempDir(), filename)
	writeFixture(t, path, "rules: []\n")
	writes := 0
	var last string
	swapAfterEdit(t, func(p string) {
		writes++
		last = "rules:\n  - capability: cap-" + string(rune('a'+writes)) + "\n    effect: ask\n"
		writeFixture(t, p, last)
	})
	var seen [][]Rule

	err := Mutate(t.Context(), path, upsertCounting(ruleAdded, &seen))

	if !errors.Is(err, ErrBusy) {
		t.Errorf("Mutate = %v, want ErrBusy", err)
	}
	if len(seen) != 3 {
		t.Errorf("the edit ran %d times, want 3 attempts", len(seen))
	}
	if got, _ := os.ReadFile(path); string(got) != last {
		t.Errorf("the file holds %q, want only the other writer's last write %q", got, last)
	}
}

func TestMutate_ARemoveRetriedOverAnotherWriteKeepsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), filename)
	writeFixture(t, path, "rules:\n  - capability: shell\n    effect: ask\n")
	first := true
	swapAfterEdit(t, func(p string) {
		if first {
			first = false
			writeFixture(t, p, "rules:\n  - capability: shell\n    effect: ask\n"+
				"  - capability: fs_read\n    effect: allow\n    match:\n      - /kas/**\n")
		}
	})

	err := Mutate(t.Context(), path, func(f *File) (bool, error) { return f.Remove(&ruleAsk), nil })
	if err != nil {
		t.Fatalf("Mutate = %v, want nil", err)
	}
	if got, want := ruleSet(loadRules(t, path)), ruleSet([]Rule{ruleKAS}); !slices.Equal(got, want) {
		t.Errorf("rules = %v, want the other writer's rule kept and the removed one gone %v", got, want)
	}
}

func TestMutate_AFileWithADocumentEndIsWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), filename)
	writeFixture(t, path, "rules:\n  - capability: shell\n    effect: ask\n...\n")
	var seen [][]Rule

	if err := Mutate(t.Context(), path, upsertCounting(ruleAdded, &seen)); err != nil {
		t.Fatalf("Mutate = %v, want nil", err)
	}
	if len(seen) != 1 {
		t.Errorf("the edit ran %d times, want once (the attempt compared against the bytes it read)", len(seen))
	}
	if got, want := ruleSet(loadRules(t, path)), ruleSet([]Rule{ruleAsk, ruleAdded}); !slices.Equal(got, want) {
		t.Errorf("rules = %v, want %v", got, want)
	}
}

func TestMutate_NoChangeWritesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), filename)
	const body = "# by hand\nrules: [ {capability: shell, effect: ask} ]\n"
	writeFixture(t, path, body)
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	if err := Mutate(t.Context(), path, func(*File) (bool, error) { return false, nil }); err != nil {
		t.Fatalf("Mutate = %v, want nil", err)
	}

	if got, _ := os.ReadFile(path); !bytes.Equal(got, []byte(body)) {
		t.Errorf("the file holds %q, want it untouched", got)
	}
	if fi, err := os.Stat(path); err != nil || !fi.ModTime().Equal(old) {
		t.Errorf("mtime = %v (%v), want unchanged %v", fi.ModTime(), err, old)
	}
}

func TestMutate_AnUnreadableFileIsErrUnreadableAndUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), filename)
	const body = "rules: [\n"
	writeFixture(t, path, body)
	called := false

	err := Mutate(t.Context(), path, func(*File) (bool, error) { called = true; return true, nil })

	if !errors.Is(err, ErrUnreadable) {
		t.Errorf("Mutate = %v, want ErrUnreadable", err)
	}
	if called {
		t.Error("the edit ran over a file Load refuses")
	}
	if got, _ := os.ReadFile(path); string(got) != body {
		t.Errorf("the file holds %q, want it untouched", got)
	}
}

func TestMutate_AnEditErrorReturnsAsIsAndWritesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), filename)
	const body = "rules: []\n"
	writeFixture(t, path, body)
	refusal := errors.New("refused")

	err := Mutate(t.Context(), path, func(f *File) (bool, error) {
		f.Rules = append(f.Rules, ruleAdded)
		return true, refusal
	})

	if !errors.Is(err, refusal) || errors.Is(err, ErrUnreadable) || errors.Is(err, ErrBusy) {
		t.Errorf("Mutate = %v, want the edit's own error", err)
	}
	if got, _ := os.ReadFile(path); string(got) != body {
		t.Errorf("the file holds %q, want it untouched", got)
	}
}
