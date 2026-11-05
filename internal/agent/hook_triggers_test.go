package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

// fakeHookList answers _kiro/hooks/list from a swappable table and counts the calls.
type fakeHookList struct {
	err   error
	hooks []kasHook
	calls int
	mu    sync.Mutex
}

func (f *fakeHookList) list(context.Context) ([]kasHook, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.hooks, f.err
}

func (f *fakeHookList) set(hooks []kasHook, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hooks, f.err = hooks, err
}

func (f *fakeHookList) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func listedHook(id, trigger string) kasHook {
	return kasHook{ID: id, Meta: kasHookMeta{Trigger: trigger}}
}

// newInlineHookTriggers runs every refresh on the caller's goroutine, so a test reads its effect at once.
func newInlineHookTriggers(f *fakeHookList) *hookTriggers {
	return newHookTriggers(context.Background(), f.list, func(fn func()) bool { fn(); return true })
}

func wantTrigger(t *testing.T, c *hookTriggers, id, want string) {
	t.Helper()
	got, ok := c.HookTrigger(id)
	if want == "" {
		if ok {
			t.Errorf("HookTrigger(%q) = %q, want a miss", id, got)
		}
		return
	}
	if !ok || got != want {
		t.Errorf("HookTrigger(%q) = (%q, %v), want (%q, true)", id, got, ok, want)
	}
}

func TestHookTriggers_ListBecomesCanonicalMap(t *testing.T) {
	f := &fakeHookList{hooks: []kasHook{
		listedHook("/w/.kiro/hooks/guard.json#hook-0", "PreToolUse"),
		listedHook("/w/.kiro/hooks/stop.json#hook-0", "agentStop"),
		listedHook("/w/.kiro/hooks/odd.json#hook-0", "bogus"),
	}}
	c := newInlineHookTriggers(f)
	c.refresh()
	wantTrigger(t, c, "/w/.kiro/hooks/guard.json#hook-0", "PreToolUse")
	wantTrigger(t, c, "/w/.kiro/hooks/stop.json#hook-0", "Stop")
	wantTrigger(t, c, "/w/.kiro/hooks/odd.json#hook-0", "")
}

func TestHookTriggers_MissRefreshesOnceUntilTheNextChange(t *testing.T) {
	f := &fakeHookList{hooks: []kasHook{listedHook("a#hook-0", "PreToolUse")}}
	c := newInlineHookTriggers(f)
	// The first frame's lookup misses, since nothing was listed yet, and starts the list.
	wantTrigger(t, c, "a#hook-0", "")
	wantTrigger(t, c, "a#hook-0", "PreToolUse")
	wantTrigger(t, c, "kiro_default#hook-1", "")
	wantTrigger(t, c, "kiro_default#hook-1", "")
	if n := f.count(); n != 1 {
		t.Errorf("list calls = %d, want 1: a miss after a fresh list must not list again", n)
	}
}

func TestHookTriggers_DidChangeReplacesTheList(t *testing.T) {
	f := &fakeHookList{hooks: []kasHook{listedHook("old#hook-0", "PreToolUse")}}
	c := newInlineHookTriggers(f)
	c.refresh()
	f.set([]kasHook{listedHook("new#hook-0", "PostToolUse")}, nil)
	c.changed(json.RawMessage(`{"sessionId":"s1","hooks":[{"id":"kiro_default#hook-1","_meta":{"trigger":"PreToolUse"}}]}`))
	wantTrigger(t, c, "old#hook-0", "")
	wantTrigger(t, c, "new#hook-0", "PostToolUse")
	// A session's projection carries its agent-profile hooks, which the workspace list does not.
	wantTrigger(t, c, "kiro_default#hook-1", "PreToolUse")
}

func TestHookTriggers_FailedListKeepsTheLastMap(t *testing.T) {
	f := &fakeHookList{hooks: []kasHook{listedHook("a#hook-0", "PreToolUse")}}
	c := newInlineHookTriggers(f)
	c.refresh()
	f.set(nil, errors.New("engine gone"))
	c.changed(json.RawMessage(`{"sessionId":"s1","hooks":[]}`))
	wantTrigger(t, c, "a#hook-0", "PreToolUse")
}

func TestHookTriggers_MalformedDidChangeStillRelists(t *testing.T) {
	f := &fakeHookList{}
	c := newInlineHookTriggers(f)
	c.refresh()
	f.set([]kasHook{listedHook("a#hook-0", "PreToolUse")}, nil)
	c.changed(json.RawMessage(`{"hooks":"nope"}`))
	wantTrigger(t, c, "a#hook-0", "PreToolUse")
}

func TestHookTriggers_FailedFirstListRelistsOnTheNextMiss(t *testing.T) {
	f := &fakeHookList{err: errors.New("engine starting")}
	c := newInlineHookTriggers(f)
	c.refresh()
	f.set([]kasHook{listedHook("a#hook-0", "PreToolUse")}, nil)
	// No list has succeeded, so the miss lists again and the next frame resolves.
	wantTrigger(t, c, "a#hook-0", "")
	wantTrigger(t, c, "a#hook-0", "PreToolUse")
	if n := f.count(); n != 2 {
		t.Errorf("list calls = %d, want 2", n)
	}
}

// The failure log says what the next frame does: before any good list it lists again, after one it keeps the map.
func TestHookTriggers_FailureLogNamesWhatTheNextFrameDoes(t *testing.T) {
	const relists, keeps = "the next hook frame lists again", "keep the last known triggers"
	f := &fakeHookList{err: errors.New("engine starting")}
	c := newInlineHookTriggers(f)
	logs := captureLogs(t)
	c.refresh()
	if got := logs.String(); !strings.Contains(got, relists) || strings.Contains(got, keeps) {
		t.Errorf("first failure logged %q, want only %q", got, relists)
	}
	f.set([]kasHook{listedHook("a#hook-0", "PreToolUse")}, nil)
	c.refresh()
	f.set(nil, errors.New("engine gone"))
	logs = captureLogs(t)
	c.refresh()
	if got := logs.String(); !strings.Contains(got, keeps) || strings.Contains(got, relists) {
		t.Errorf("failure after a good list logged %q, want only %q", got, keeps)
	}
	// onEngineReady relists with no hook change, so the kept path's retry names both changes.
	const until, stale = "until the next change (engine ready or a hook change)", "until the hooks change"
	if got := logs.String(); !strings.Contains(got, until) || strings.Contains(got, stale) {
		t.Errorf("failure after a good list logged %q, want %q and not %q", got, until, stale)
	}
}
