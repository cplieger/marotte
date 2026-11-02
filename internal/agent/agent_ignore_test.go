package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

// ignoreFrames is every ignore list the engine lane was sent, oldest first.
func ignoreFrames(t *testing.T, lane *fakeBridge) [][]string {
	t.Helper()
	var out [][]string
	for _, raw := range lane.notified(marotte.MethodPolicyIgnoreFilesChanged) {
		var p struct {
			Files []string `json:"files"`
		}
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatalf("decode an ignore-list frame %s: %v", raw, err)
		}
		out = append(out, p.Files)
	}
	return out
}

// liveEngine is a runtime with two open chats over one engine; the ignore frames sent before it returns are dropped.
func liveEngine(t *testing.T, configDir string) (*Runtime, *fakeBridge) {
	t.Helper()
	lane := newFakeBridge()
	cs := newTestChatStore()
	h := newRuntimeOn(context.Background(), t.TempDir(), fakeEngineOf(func() engineSession { return newFakeBridge() }, lane), cs,
		WithConfigDir(configDir))
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	for _, id := range []marotte.ChatID{"c1", "c2"} {
		openChat(t, h, id)
	}
	lane.mu.Lock()
	lane.notifies, lane.notifyParams = nil, nil
	lane.mu.Unlock()
	return h, lane
}

// A settings write sends the ignore list once, on the engine, however many sessions are open.
func TestSettingsWrite_SendsTheIgnoreListOnceOnTheEngine(t *testing.T) {
	dir := t.TempDir()
	h, lane := liveEngine(t, dir)
	writeIgnoreFiles(t, dir, []string{".gitignore"})

	h.ReconcileSessionSettings(t.Context())

	want := []string{settings.AgentIgnoreFloor, ".gitignore"}
	frames := ignoreFrames(t, lane)
	if len(frames) != 1 || !slices.Equal(frames[0], want) {
		t.Errorf("engine ignore-list frames = %v, want exactly [%v]: the floor leads and the user's entries follow", frames, want)
	}
}

// TestSettingsWrite_SendsNoIgnoreListFromAnUnreadableDocument pins the absence of a
// frame: `{files: []}` would clear enforcement in KAS for every session.
func TestSettingsWrite_SendsNoIgnoreListFromAnUnreadableDocument(t *testing.T) {
	logs := captureLogs(t)
	dir := t.TempDir()
	writeIgnoreFiles(t, dir, []string{".gitignore"})
	h, lane := liveEngine(t, dir)

	// Corrupt after the engine is up, so it already holds a list.
	if err := os.WriteFile(filepath.Join(dir, settings.Filename), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt settings: %v", err)
	}
	h.ReconcileSessionSettings(t.Context())

	if frames := ignoreFrames(t, lane); len(frames) != 0 {
		t.Errorf("the engine was sent %v from an unparseable document; kiro-cli must keep enforcing the previous list", frames)
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
	h, _ := liveEngine(t, dir)

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
	if msg := got[0].Message; !strings.Contains(msg, "keeps enforcing its previous list") ||
		!strings.Contains(msg, "enforces only "+settings.AgentIgnoreFloor) {
		t.Errorf("policy error message %q does not name both outcomes: the running engine keeps its list, an engine "+
			"restarted meanwhile enforces only %s", msg, settings.AgentIgnoreFloor)
	}
}

// TestSpawnIgnoreFiles_AnEmptyDocumentStillCarriesTheFloor pins the engine spawn's fail mode,
// deliberately the opposite of the live push's: a fresh connection has nothing to keep.
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

// The one engine build carries the ignore-list resolver, read at send time with the floor leading.
func TestResolveEngine_CarriesTheIgnoreListResolver(t *testing.T) {
	dir := t.TempDir()
	writeIgnoreFiles(t, dir, []string{".gitignore"})
	h := newRuntimeOn(context.Background(), t.TempDir(), fakeEngineOf(func() engineSession { return newFakeBridge() }, nil),
		newTestChatStore(), WithConfigDir(dir))
	t.Cleanup(func() { shutdownHub(t, h) })

	opts := h.resolveEngine(t.Context(), "/fake/kiro-cli-chat").opts
	if opts.IgnoreFiles == nil {
		t.Fatal("EngineOpts.IgnoreFiles = nil, so the engine starts enforcing nothing")
	}
	writeIgnoreFiles(t, dir, []string{".dockerignore"})
	if got, want := opts.IgnoreFiles(t.Context()), []string{settings.AgentIgnoreFloor, ".dockerignore"}; !slices.Equal(got, want) {
		t.Errorf("IgnoreFiles() after a save = %v, want %v read at send time", got, want)
	}
}
