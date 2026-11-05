package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/go/slogx/capture"
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

// policyErrors is every policy_error item h broadcast.
func policyErrors(h *Runtime) []marotte.PolicyErrorItem {
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
	return got
}

// The broadcast shows the reader why their edit did nothing, and what an engine started before the fix enforces.
func TestSettingsWrite_TellsTheClientWhatARestartWouldEnforce(t *testing.T) {
	const keeps = "keeps enforcing its previous list"
	last := "enforces the last list marotte read"
	floor := "enforces only " + settings.AgentIgnoreFloor
	t.Run("an_unparseable_document_after_a_readable_list", func(t *testing.T) {
		dir := t.TempDir()
		writeIgnoreFiles(t, dir, []string{".gitignore"})
		h, _ := liveEngine(t, dir)
		rewriteConfigByHand(t, dir, "{not json")
		h.ReconcileSessionSettings(t.Context())

		got := policyErrors(h)
		if len(got) != 1 {
			t.Fatalf("broadcast %d policy_error items, want exactly 1: %v", len(got), got)
		}
		if got[0].Source != settings.Filename {
			t.Errorf("policy error names source %q, want %q: the reader has to know WHICH document to fix", got[0].Source, settings.Filename)
		}
		if msg := got[0].Message; !strings.Contains(msg, keeps) || !strings.Contains(msg, last) {
			t.Errorf("policy error message %q, want it to say the engine keeps its list and a restart starts on the last list read", msg)
		}
	})
	t.Run("a_list_that_does_not_decode", func(t *testing.T) {
		dir := t.TempDir()
		h, _ := liveEngine(t, dir)
		rewriteConfigByHand(t, dir, `{"agent_ignore_files":7}`)
		h.ReconcileSessionSettings(t.Context())

		got := policyErrors(h)
		if len(got) != 1 || !strings.Contains(got[0].Message, keeps) || !strings.Contains(got[0].Message, floor) {
			t.Errorf("policy errors = %v, want one saying a restart enforces only %s", got, settings.AgentIgnoreFloor)
		}
	})
	t.Run("unparseable_then_mistyped_reports_each_answer", func(t *testing.T) {
		dir := t.TempDir()
		writeIgnoreFiles(t, dir, []string{".gitignore"})
		h, _ := liveEngine(t, dir)
		rewriteConfigByHand(t, dir, "{not json")
		h.ReconcileSessionSettings(t.Context())
		h.ReconcileSessionSettings(t.Context())
		rewriteConfigByHand(t, dir, `{"agent_ignore_files":7}`)
		h.ReconcileSessionSettings(t.Context())

		got := policyErrors(h)
		if len(got) != 2 || !strings.Contains(got[0].Message, last) || !strings.Contains(got[1].Message, floor) {
			t.Errorf("policy errors = %v, want two: the last list, then only %s", got, settings.AgentIgnoreFloor)
		}
	})
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
			var l lastReadable
			doc, _ := l.read(t.Context(), dir, readConfigDocument)
			if got, want := doc.engine.ignoreFiles, []string{settings.AgentIgnoreFloor}; !slices.Equal(got, want) {
				t.Errorf("the spawn's ignore files = %v, want %v", got, want)
			}
		})
	}
}

// spawnIgnoreRuntime is a runtime over a config directory holding body at boot, with no engine started.
func spawnIgnoreRuntime(t *testing.T, body string) (*Runtime, string) {
	t.Helper()
	dir := writeSettings(t, body)
	h := newRuntimeOn(context.Background(), t.TempDir(), fakeEngineOf(func() engineSession { return newFakeBridge() }, nil),
		newTestChatStore(), WithConfigDir(dir))
	t.Cleanup(func() { shutdownHub(t, h) })
	// The boot's own observer door reads the same settings and logs the same Warns, so it ends before a test captures.
	settleBoot(t, h)
	return h, dir
}

// The floor Warn fires only when the floor is what the spawn really sends. Serial: slog's default is process-global.
func TestSpawnIgnoreFiles_LogsTheFloorOnlyWhenTheFloorIsSent(t *testing.T) {
	const neverRead = "agent ignore files: config.json cannot be read and none was read since marotte started; this spawn enforces only the floor"
	const invalid = "agent ignore files: the stored list does not decode; this spawn enforces only the floor"
	floor := []string{settings.AgentIgnoreFloor}
	t.Run("never_readable", func(t *testing.T) {
		h, _ := spawnIgnoreRuntime(t, "{not json")
		rec := capture.Default(t)
		if got := h.spawnIgnoreFiles(t.Context()); !slices.Equal(got, floor) {
			t.Errorf("spawnIgnoreFiles = %v, want %v", got, floor)
		}
		if n := rec.CountExact(neverRead); n != 1 {
			t.Errorf("never-read floor Warn logged %d times, want 1: %v", n, rec.Messages())
		}
	})
	t.Run("readable_then_broken", func(t *testing.T) {
		h, dir := spawnIgnoreRuntime(t, `{"agent_ignore_files":[".gitignore"]}`)
		rewriteConfigByHand(t, dir, "{not json")
		rec := capture.Default(t)
		if got, want := h.spawnIgnoreFiles(t.Context()), []string{settings.AgentIgnoreFloor, ".gitignore"}; !slices.Equal(got, want) {
			t.Errorf("spawnIgnoreFiles = %v, want the last readable list %v", got, want)
		}
		if n := rec.CountExact(neverRead) + rec.CountExact(invalid); n != 0 {
			t.Errorf("a floor Warn logged %d times for a spawn that sends the last readable list: %v", n, rec.Messages())
		}
	})
	t.Run("a_list_that_does_not_decode", func(t *testing.T) {
		h, _ := spawnIgnoreRuntime(t, `{"agent_ignore_files":7}`)
		rec := capture.Default(t)
		if got := h.spawnIgnoreFiles(t.Context()); !slices.Equal(got, floor) {
			t.Errorf("spawnIgnoreFiles = %v, want %v", got, floor)
		}
		if n := rec.CountExact(invalid); n != 1 {
			t.Errorf("invalid-list Warn logged %d times, want 1: %v", n, rec.Messages())
		}
	})
}

// The one engine build carries the ignore-list resolver, read at send time with the floor leading.
func TestResolveEngine_CarriesTheIgnoreListResolver(t *testing.T) {
	dir := t.TempDir()
	writeIgnoreFiles(t, dir, []string{".gitignore"})
	h := newRuntimeOn(context.Background(), t.TempDir(), fakeEngineOf(func() engineSession { return newFakeBridge() }, nil),
		newTestChatStore(), WithConfigDir(dir))
	t.Cleanup(func() { shutdownHub(t, h) })

	opts := h.resolveEngine(t.Context(), "/fake/kiro-cli").opts
	if opts.IgnoreFiles == nil {
		t.Fatal("EngineOpts.IgnoreFiles = nil, so the engine starts enforcing nothing")
	}
	writeIgnoreFiles(t, dir, []string{".dockerignore"})
	if got, want := opts.IgnoreFiles(t.Context()), []string{settings.AgentIgnoreFloor, ".dockerignore"}; !slices.Equal(got, want) {
		t.Errorf("IgnoreFiles() after a save = %v, want %v read at send time", got, want)
	}
}

// A session door over a config.json never readable since boot grants no additional directory and says why.
// Serial: slog's default is process-global.
func TestSessionDirs_ANeverReadableDocumentGrantsNothingAndLogs(t *testing.T) {
	h, _ := spawnIgnoreRuntime(t, "{not json")
	rec := capture.Default(t)
	if got := h.readSessionSettings(t.Context()).AdditionalDirectories; len(got) != 0 {
		t.Errorf("a door over a never-readable config.json granted %v, want none", got)
	}
	if n := rec.CountExact("additional directories not granted to agents: config unreadable"); n != 1 {
		t.Errorf("the unreadable Warn logged %d times, want 1: %v", n, rec.Messages())
	}
}
