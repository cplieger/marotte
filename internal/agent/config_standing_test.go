package agent

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// standingReply is a getState answer holding one displaced agent read from each file in uris.
func standingReply(uris ...string) json.RawMessage {
	items := make([]map[string]any, 0, len(uris))
	for _, uri := range uris {
		items = append(items, map[string]any{
			"layer":    "workspace",
			"standing": map[string]any{"kind": "displaced", "decidedBy": "user-local"},
			"readFrom": map[string]any{"uri": "file://" + uri},
			"value":    map[string]any{"name": "reviewer"},
		})
	}
	raw, _ := json.Marshal(map[string]any{
		"layers":    []map[string]any{{"id": "user-local", "name": "Local Settings (~/.kiro)"}},
		"resources": []map[string]any{{"type": "agent", "items": items}},
	})
	return raw
}

// standingFixture is a configStanding whose fetch answers reply and records the roots it was asked for.
type standingFixture struct {
	c          *configStanding
	lt         *lifetime
	reply      json.RawMessage
	broadcasts atomic.Int32
	fetches    atomic.Int32
	mu         sync.Mutex
}

func newStandingFixture(t *testing.T) *standingFixture {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	f := &standingFixture{lt: &lifetime{shutdownCtx: ctx, shutdownCancel: cancel, done: make(chan struct{})}}
	f.c = newConfigStanding(f.lt, func(context.Context) (json.RawMessage, error) {
		f.fetches.Add(1)
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.reply, nil
	}, func(context.Context) { f.broadcasts.Add(1) })
	t.Cleanup(func() { cancel(); f.lt.inflight.Wait() })
	return f
}

func (f *standingFixture) answer(raw json.RawMessage) {
	f.mu.Lock()
	f.reply = raw
	f.mu.Unlock()
}

func (f *standingFixture) refetch() {
	f.c.refetchSoon()
	f.lt.inflight.Wait()
}

func TestConfigStanding_RefetchesOnPushAndBroadcastsOnChange(t *testing.T) {
	f := newStandingFixture(t)
	f.answer(standingReply("/work/.kiro/agents/reviewer.json"))
	f.refetch()
	if n := f.broadcasts.Load(); n != 1 {
		t.Fatalf("broadcasts after the first standing = %d, want 1", n)
	}
	f.refetch()
	if n := f.broadcasts.Load(); n != 1 {
		t.Errorf("broadcasts after an unchanged refetch = %d, want still 1", n)
	}
	f.answer(standingReply())
	f.refetch()
	if n := f.broadcasts.Load(); n != 2 {
		t.Errorf("broadcasts after the item became active = %d, want 2", n)
	}
	if got := f.c.response().Standing; len(got) != 0 {
		t.Errorf("standing = %+v, want none", got)
	}
}

func TestConfigStanding_RootsAreWorkDirOnly(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	h.standing.refetch(t.Context())
	br.mu.Lock()
	p := br.lastParams[methodKiroConfigurationGet]
	br.mu.Unlock()
	roots, _ := json.Marshal(p["roots"])
	want, _ := json.Marshal([]string{h.lifecycle.workDir})
	if string(roots) != string(want) || p["observer"] != "workspace" {
		t.Errorf("getState params = %v, want a workspace observer of only the work dir %s", p, want)
	}
}

// Same-named agents in two repositories: the standing names the file it was read from, so only that row is badged.
func TestConfigStanding_AgentStandingJoinsOnlyItsOwnRow(t *testing.T) {
	f := newStandingFixture(t)
	f.answer(standingReply("/work/repo-a/.kiro/agents/reviewer.json"))
	f.refetch()
	got := f.c.response().Standing
	if len(got) != 1 || got[0].Key != "work/repo-a/.kiro/agents/reviewer.json" {
		t.Errorf("standing = %+v, want one row keyed by repo-a's file only", got)
	}
	for _, s := range got {
		if s.Key == "work/repo-b/.kiro/agents/reviewer.json" || s.Key == "reviewer" {
			t.Errorf("standing joined %q: an agent's standing joins its own file, never a name", s.Key)
		}
	}
}

// agentLoadErrorFrame is _kiro/customAgent/config_error for path.
func agentLoadErrorFrame(path string) *marotte.RPCResponse {
	params, _ := json.Marshal(map[string]any{"path": path, "reasonCode": "invalid_json", "error": "bad json"})
	return &marotte.RPCResponse{Method: methodV3CustomAgentConfigErr, Params: params}
}

func TestConfigStanding_AgentLoadErrorClearsWhenTheFileChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reviewer.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := newStandingFixture(t)
	f.c.noteAgentFrame(agentLoadErrorFrame(path))
	key := path[1:]
	if _, ok := f.c.response().LoadErrors[key]; !ok {
		t.Fatalf("load errors = %v, want %s", f.c.response().LoadErrors, key)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.c.response().LoadErrors[key]; ok {
		t.Error("the load error survived its file changing")
	}
}

func TestAgentLoadErrors_KeyIsTheDocsRowPath(t *testing.T) {
	f := newStandingFixture(t)
	f.c.lstat = func(string) (fs.FileInfo, error) { return nil, fs.ErrNotExist }
	f.c.noteAgentFrame(agentLoadErrorFrame("/work/repo/.kiro/agents/reviewer.json"))
	f.c.mu.Lock()
	_, ok := f.c.errors["work/repo/.kiro/agents/reviewer.json"]
	f.c.mu.Unlock()
	if !ok {
		t.Errorf("load errors = %v, want the key a Docs row's Path carries", f.c.errors)
	}
	f.c.noteAgentFrame(agentLoadErrorFrame("relative/reviewer.json"))
	f.c.mu.Lock()
	n := len(f.c.errors)
	f.c.mu.Unlock()
	if n != 1 {
		t.Errorf("load errors = %d after a relative path, want it dropped", n)
	}
}

func TestAgentLoadErrors_ClearOnlyTheChangedFile(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f := newStandingFixture(t)
	f.c.noteAgentFrame(agentLoadErrorFrame(a))
	f.c.noteAgentFrame(agentLoadErrorFrame(b))
	if err := os.WriteFile(a, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := slices.Sorted(func(yield func(string) bool) {
		for k := range f.c.response().LoadErrors {
			if !yield(k) {
				return
			}
		}
	})
	if !slices.Equal(got, []string{b[1:]}) {
		t.Errorf("load errors after a.json changed = %v, want only b.json's", got)
	}
}
