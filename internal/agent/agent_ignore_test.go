package agent

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

// notifyProbeBridge records the ignore-list notifications one bridge received.
type notifyProbeBridge struct {
	*fakeBridge

	mu   sync.Mutex
	sent [][]string
}

func newNotifyProbeBridge() *notifyProbeBridge {
	return &notifyProbeBridge{fakeBridge: newFakeBridge()}
}

func (b *notifyProbeBridge) Notify(_ context.Context, method string, params any) error {
	if method != marotte.MethodPolicyIgnoreFilesChanged {
		return nil
	}
	m, ok := params.(map[string]any)
	if !ok {
		return nil
	}
	files, ok := m[marotte.ParamIgnoreFiles].([]string)
	if !ok {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sent = append(b.sent, slices.Clone(files))
	return nil
}

// ignoreFrames returns the lists this bridge was sent, oldest first.
func (b *notifyProbeBridge) ignoreFrames() [][]string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.sent)
}

// twoLiveBridges stands up two open chat bridges and returns each one's probe.
func twoLiveBridges(t *testing.T, configDir string) (*Runtime, map[marotte.ChatID]*notifyProbeBridge) {
	t.Helper()
	var mu sync.Mutex
	made := []*notifyProbeBridge{}
	factory := func() ACPBridge {
		p := newNotifyProbeBridge()
		mu.Lock()
		made = append(made, p)
		mu.Unlock()
		return p
	}

	cs := newTestChatStore()
	h := New(context.Background(), t.TempDir(), factory, cs, WithConfigDir(configDir))
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })

	ctx := t.Context()
	probes := map[marotte.ChatID]*notifyProbeBridge{}
	for _, id := range []marotte.ChatID{"c1", "c2"} {
		if _, err := cs.Mutate(ctx, id, func(c *marotte.Chat, _ bool) bool { c.Name = string(id); return true }); err != nil {
			t.Fatalf("seed chat %s: %v", id, err)
		}
		mu.Lock()
		before := len(made)
		mu.Unlock()
		if _, err := h.coord.OpenBridge(ctx, id, ""); err != nil {
			t.Fatalf("OpenBridge %s: %v", id, err)
		}
		mu.Lock()
		fresh := made[before:]
		mu.Unlock()
		if len(fresh) != 1 {
			t.Fatalf("OpenBridge %s built %d bridges, want exactly 1", id, len(fresh))
		}
		probes[id] = fresh[0]
	}
	return h, probes
}

// TestSettingsWrite_PushesTheIgnoreListToEveryLiveBridge asserts each probe individually: an
// aggregate count passes a loop that sent one bridge two frames and the other none.
func TestSettingsWrite_PushesTheIgnoreListToEveryLiveBridge(t *testing.T) {
	dir := t.TempDir()
	h, probes := twoLiveBridges(t, dir)
	writeIgnoreFiles(t, dir, []string{".gitignore"})

	h.ReconcileSessionSettings(t.Context())

	want := []string{settings.AgentIgnoreFloor, ".gitignore"}
	for id, p := range probes {
		frames := p.ignoreFrames()
		if len(frames) != 1 {
			t.Errorf("chat %s received %d ignore-list frames, want 1", id, len(frames))
			continue
		}
		if !slices.Equal(frames[0], want) {
			t.Errorf("chat %s was sent %v, want %v: the floor leads and the user's entries follow", id, frames[0], want)
		}
	}
}

// TestSettingsWrite_SendsNoIgnoreListFromAnUnreadableDocument pins the absence of a
// frame: `{files: []}` would clear enforcement in KAS for every open chat.
func TestSettingsWrite_SendsNoIgnoreListFromAnUnreadableDocument(t *testing.T) {
	logs := captureLogs(t)
	dir := t.TempDir()
	writeIgnoreFiles(t, dir, []string{".gitignore"})
	h, probes := twoLiveBridges(t, dir)

	// Corrupt after the bridges are up, so each one already holds a list.
	if err := os.WriteFile(filepath.Join(dir, settings.Filename), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt settings: %v", err)
	}
	h.ReconcileSessionSettings(t.Context())

	for id, p := range probes {
		if frames := p.ignoreFrames(); len(frames) != 0 {
			t.Errorf("chat %s was sent %v from an unparseable document; kiro-cli must keep enforcing the previous list", id, frames)
		}
	}
	// Silence toward KAS must still be logged.
	if !strings.Contains(logs.String(), "agent ignore files not pushed") {
		t.Errorf("nothing recorded the refusal; the log is where an operator learns the document is broken: %s", logs.String())
	}
}

// TestSettingsWrite_TellsTheClientTheIgnoreListIsUnreadable pins the broadcast that
// shows the reader why their edit did nothing.
func TestSettingsWrite_TellsTheClientTheIgnoreListIsUnreadable(t *testing.T) {
	dir := t.TempDir()
	writeIgnoreFiles(t, dir, []string{".gitignore"})
	h, _ := twoLiveBridges(t, dir)

	if err := os.WriteFile(filepath.Join(dir, settings.Filename), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt settings: %v", err)
	}
	h.ReconcileSessionSettings(t.Context())

	var got []marotte.PolicyErrorItem
	for _, e := range h.bus.fanout.Snapshot() {
		var frame struct {
			Type    marotte.EventType          `json:"type"`
			Payload marotte.PolicyErrorPayload `json:"payload"`
		}
		if err := json.Unmarshal(e.Event.Data, &frame); err != nil {
			continue // another event kind whose payload is a different shape
		}
		if frame.Type == marotte.EventPolicyError {
			got = append(got, frame.Payload.Errors...)
		}
	}
	if len(got) != 1 {
		t.Fatalf("broadcast %d policy_error items, want exactly 1: %v", len(got), got)
	}
	if got[0].Source != settings.Filename {
		t.Errorf("policy error names source %q, want %q: the reader has to know WHICH document to fix", got[0].Source, settings.Filename)
	}
	if msg := got[0].Message; !strings.Contains(msg, "already running keep enforcing their previous list") ||
		!strings.Contains(msg, "enforces only "+settings.AgentIgnoreFloor) {
		t.Errorf("policy error message %q does not name both populations: running chats keep their list, a chat started "+
			"meanwhile enforces only %s", msg, settings.AgentIgnoreFloor)
	}
}

// TestSpawnIgnoreFiles_AnEmptyDocumentStillCarriesTheFloor pins the spawn's fail mode,
// deliberately the opposite of the fan-out's: a fresh connection has nothing to keep.
func TestSpawnIgnoreFiles_AnEmptyDocumentStillCarriesTheFloor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		body string
	}{
		{"no config.json at all", ""},
		{"a document that says nothing", "{}"},
		{"an unparseable document", "{not json"},
		{"the key set to an empty list", `{"agent_ignore_files":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if tc.body != "" {
				if err := os.WriteFile(filepath.Join(dir, settings.Filename), []byte(tc.body), 0o600); err != nil {
					t.Fatalf("write settings: %v", err)
				}
			}
			got := spawnIgnoreFiles(t.Context(), dir)
			if want := []string{settings.AgentIgnoreFloor}; !slices.Equal(got, want) {
				t.Errorf("spawnIgnoreFiles = %v, want %v", got, want)
			}
		})
	}
}

// TestStartOptsLiterals_AllCarryIgnoreFiles reads the SOURCE: a new spawn site omitting
// IgnoreFiles fails silently, since KAS reports nothing for a missing notification.
func TestStartOptsLiterals_AllCarryIgnoreFiles(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the runtime package directory: %v", err)
	}
	fset := token.NewFileSet()
	literals := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, pErr := parser.ParseFile(fset, name, nil, 0)
		if pErr != nil {
			t.Fatalf("parse %s: %v", name, pErr)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !isStartOptsType(lit.Type) {
				return true
			}
			literals++
			if !hasKey(lit, "IgnoreFiles") {
				t.Errorf("%s:%d builds a marotte.StartOpts with no IgnoreFiles; that bridge would enforce no ignore file at all, and nothing else about it would look wrong",
					name, fset.Position(lit.Pos()).Line)
			}
			return true
		})
	}
	// A floor rather than an exact count, so a scan that stopped finding sites fails instead of passing vacuously.
	if literals < 5 {
		t.Fatalf("found %d marotte.StartOpts literals, want at least 5; the scan is not reaching the spawn sites", literals)
	}
}

// isStartOptsType reports whether expr names marotte.StartOpts.
func isStartOptsType(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "StartOpts" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "marotte"
}

// hasKey reports whether a keyed composite literal sets the named field.
func hasKey(lit *ast.CompositeLit, field string) bool {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if key, ok := kv.Key.(*ast.Ident); ok && key.Name == field {
			return true
		}
	}
	return false
}
