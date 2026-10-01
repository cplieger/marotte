package agent

// What marotte owes the agent-ignore feature now that KAS enforces it: the LIST
// reaches every bridge, and it reaches them at every door.
//
// The fail mode asserted below is that an unreadable document sends NOTHING,
// leaving KAS enforcing what it was last told rather than a guess or a clear.

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

// notifyProbeBridge records the ignore-list notifications one bridge received, so
// a fan-out can be asserted per bridge rather than in aggregate. It embeds the
// shared fake, whose Notify records nothing.
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

// twoLiveBridges stands a runtime up with two open chat bridges and returns the
// probe for each, so a fan-out assertion can name the bridge that missed out.
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
	h.mcpRegistry.SignalReady()
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

// TestPushAgentIgnoreFiles_ReachesEveryLiveBridge is the fan-out half of the PATCH
// path: the value is CONNECTION-scope in KAS, so a saved edit has to be pushed per
// bridge or it reaches only the chats opened after the save.
//
// Both probes are asserted individually. A test that counted frames in aggregate
// would pass on a fan-out that sent one bridge two frames and the other none,
// which is the shape a mistaken loop produces.
func TestPushAgentIgnoreFiles_ReachesEveryLiveBridge(t *testing.T) {
	dir := t.TempDir()
	writeIgnoreFiles(t, dir, []string{".gitignore"})
	h, probes := twoLiveBridges(t, dir)

	h.PushAgentIgnoreFiles(t.Context())

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

// TestPushAgentIgnoreFiles_SendsNothingWhenTheDocumentIsUnreadable asserts the
// ABSENCE of a frame rather than its contents, because both alternatives are worse
// than silence: a list assembled from a document that could not be parsed is a
// guess, and `{files: []}` CLEARS enforcement in KAS outright, so an unreadable
// config.json would disable the floor for every open chat at once. Every live
// bridge already holds a list KAS is enforcing, so leaving it alone is the only
// answer that cannot lose ground.
func TestPushAgentIgnoreFiles_SendsNothingWhenTheDocumentIsUnreadable(t *testing.T) {
	logs := captureLogs(t)
	dir := t.TempDir()
	writeIgnoreFiles(t, dir, []string{".gitignore"})
	h, probes := twoLiveBridges(t, dir)

	// Corrupt AFTER the bridges are up, so each one is a bridge holding a list.
	if err := os.WriteFile(filepath.Join(dir, settings.Filename), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt settings: %v", err)
	}
	h.PushAgentIgnoreFiles(t.Context())

	for id, p := range probes {
		if frames := p.ignoreFrames(); len(frames) != 0 {
			t.Errorf("chat %s was sent %v from an unparseable document; kiro-cli must keep enforcing the previous list", id, frames)
		}
	}
	// The operator half: silence toward KAS must not be silence toward the log, or
	// a broken document looks exactly like a document that changed nothing.
	if !strings.Contains(logs.String(), "agent ignore files not pushed") {
		t.Errorf("nothing recorded the refusal; the log is where an operator learns the document is broken: %s", logs.String())
	}
}

// TestPushAgentIgnoreFiles_TellsTheClientTheDocumentIsUnreadable is the other half
// of the refusal: silence toward KAS must not be silence toward the READER either.
// The log reaches an operator reading the container; the broadcast is what puts the
// reason on the screen of whoever is about to wonder why their edit did nothing.
//
// The seam is the runtime's own broadcaster rather than a connected SSE client,
// because the frame is what the runtime owes and the transport is the sse
// library's to test.
func TestPushAgentIgnoreFiles_TellsTheClientTheDocumentIsUnreadable(t *testing.T) {
	dir := t.TempDir()
	writeIgnoreFiles(t, dir, []string{".gitignore"})
	h, _ := twoLiveBridges(t, dir)

	if err := os.WriteFile(filepath.Join(dir, settings.Filename), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt settings: %v", err)
	}
	h.PushAgentIgnoreFiles(t.Context())

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
	if !strings.Contains(got[0].Message, "kiro-cli keeps enforcing the previous one") {
		t.Errorf("policy error message %q does not say the previous list is still in force; a reader who assumes the list was cleared would edit the wrong thing", got[0].Message)
	}
}

// TestSpawnIgnoreFiles_AnEmptyDocumentStillCarriesTheFloor pins the door's own
// fail mode, which is the opposite of the fan-out's and deliberately so.
//
// A fresh connection has no prior value for KAS to keep, so sending nothing would
// leave `.kiroignore` unenforced for that session's whole life. There is nothing
// to lose ground against, so the floor is the safe answer here where silence is
// the safe answer there.
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

// TestStartOptsLiterals_AllCarryIgnoreFiles reads the SOURCE, because no
// behavioural test can cover a door nobody has written yet. One resolver serves
// five spawn sites (chat new, chat load, the utility session, both run bridges),
// and a bridge that omits it enforces `.kiroignore` for nothing — silently, since
// KAS answers a missing notification with no error and the chat works in every
// other respect. A sixth site added later is exactly the regression this catches;
// a fan-out or handshake test cannot.
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
	// Five production spawn sites today. Asserted as a FLOOR rather than an exact
	// count so adding a sixth is a green test plus a real assertion on it, while a
	// scan that stopped finding them fails here instead of passing vacuously.
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
