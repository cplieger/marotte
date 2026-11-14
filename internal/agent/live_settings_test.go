package agent

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/mcp"
	"github.com/cplieger/marotte/internal/push"
	"github.com/cplieger/marotte/internal/settings"
)

type liveRecorder struct {
	*recordingPush
	store        *mcp.Store
	beforeRender func()
	applied      map[marotte.PushKind]bool
	kasPath      string
	mcpWaits     []bool
	debug        []bool
	prefs        []map[marotte.PushKind]bool
	mu           sync.Mutex
	debugOn      bool
}

func newLiveRecorder(t *testing.T) *liveRecorder {
	t.Helper()
	return &liveRecorder{recordingPush: &recordingPush{}, applied: push.ResolvePreferences(settings.Document{})}
}

func (r *liveRecorder) options() []Option {
	return []Option{WithKASMCPRenderer(r), WithDebugLogs(r.debugLogs, r.setDebug), WithPush(r)}
}

func (r *liveRecorder) RenderKASConfig(ctx context.Context) error {
	r.mu.Lock()
	hook := r.beforeRender
	r.mu.Unlock()
	if hook != nil {
		hook()
	}
	beforeWait, beforeKnown := r.store.RenderedWaitForReady()
	if err := r.store.RenderKASConfig(ctx); err != nil {
		return err
	}
	if wait, known := r.store.RenderedWaitForReady(); beforeKnown && known == beforeKnown && wait == beforeWait {
		return nil
	}
	raw, err := os.ReadFile(r.kasPath)
	if err != nil {
		return err
	}
	var doc struct {
		MCPServers map[string]struct {
			WaitForReady bool `json:"waitForReady"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	r.mu.Lock()
	r.mcpWaits = append(r.mcpWaits, doc.MCPServers["local"].WaitForReady)
	r.mu.Unlock()
	return nil
}

func (r *liveRecorder) RenderedWaitForReady() (waitForReady, known bool) {
	return r.store.RenderedWaitForReady()
}

func (r *liveRecorder) debugLogs() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.debugOn
}

func (r *liveRecorder) setDebug(on bool) {
	r.mu.Lock()
	r.debug = append(r.debug, on)
	r.debugOn = on
	r.mu.Unlock()
}

func (r *liveRecorder) SetPreferences(prefs map[marotte.PushKind]bool) {
	r.mu.Lock()
	r.prefs = append(r.prefs, prefs)
	r.applied = maps.Clone(prefs)
	r.mu.Unlock()
}

func (r *liveRecorder) Preferences() map[marotte.PushKind]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return maps.Clone(r.applied)
}

func (r *liveRecorder) openWithStore(t *testing.T, configDir string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	r.kasPath = filepath.Join(home, ".kiro", "settings", "mcp.json")
	store, err := mcp.New(t.Context(), configDir, nil,
		mcp.WithWaitForReady(func(ctx context.Context) (bool, bool) { return settings.MCPWaitForReady(ctx, configDir) }))
	if err != nil {
		t.Fatalf("Setup: mcp.New: %v", err)
	}
	if _, err := store.Create(t.Context(), &mcp.Server{Name: "local", Transport: mcp.TransportStdio, Command: "npx", Enabled: true}); err != nil {
		t.Fatalf("Setup: create an MCP server: %v", err)
	}
	r.store = store
}

type livePushesSeen struct {
	prefs             []map[marotte.PushKind]bool
	ignore            []string
	terminal          []string
	contentCollection []bool
	mcpWaits          []bool
	debug             []bool
}

func pushesSeen(br *fakeBridge, r *liveRecorder) livePushesSeen {
	r.mu.Lock()
	defer r.mu.Unlock()
	return livePushesSeen{
		ignore:            br.notified(marotte.MethodPolicyIgnoreFilesChanged),
		terminal:          br.notified(marotte.MethodTerminalSettingsChanged),
		contentCollection: contentCollectionWrites(br),
		mcpWaits:          r.mcpWaits,
		debug:             r.debug,
		prefs:             r.prefs,
	}
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Setup: marshal %v: %v", v, err)
	}
	return string(raw)
}

func TestOpenBridge_AHandEditPushesExactlyTheLiveSurfacesItMoved(t *testing.T) {
	ignoreFrame := jsonOf(t, map[string]any{marotte.ParamIgnoreFiles: []string{settings.AgentIgnoreFloor, ".gitignore"}})
	terminalFrame := jsonOf(t, marotte.TerminalSettingsParams(300000))
	for _, tc := range []struct {
		name string
		body string
		want livePushesSeen
	}{
		{name: "ignore_files", body: `{"agent_ignore_files":[".gitignore"]}`, want: livePushesSeen{ignore: []string{ignoreFrame}}},
		{name: "terminal_timeout", body: `{"terminal_command_timeout_ms":300000}`, want: livePushesSeen{terminal: []string{terminalFrame}}},
		{
			name: "content_collection", body: `{"content_collection_enabled":false}`,
			want: livePushesSeen{contentCollection: []bool{false}},
		},
		{name: "mcp_wait", body: `{"mcp_wait_for_ready":true}`, want: livePushesSeen{mcpWaits: []bool{true}}},
		{
			name: "notification_toggle", body: `{"notify_pr_status":true}`,
			want: livePushesSeen{prefs: []map[marotte.PushKind]bool{{
				marotte.PushKindAgentFinished: true, marotte.PushKindPermission: true,
				marotte.PushKindPRStatus: true, marotte.PushKindRunOutcome: true,
			}}},
		},
		{name: "an_unrelated_key", body: `{"last_merge_method":"squash"}`},
		{
			name: "values_resolving_as_before",
			body: `{"agent_ignore_files":[],"terminal_command_timeout_ms":500,"content_collection_enabled":true,` +
				`"mcp_wait_for_ready":false,"debug_logs":false,"notify_agent_finished":true,"notify_pr_status":false}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := newLiveRecorder(t)
			h, configDir := reopenFixture(t, rec.options()...)
			rec.openWithStore(t, configDir)
			first, err := h.coord.openReleased(t.Context(), "c1", "")
			if err != nil {
				t.Fatalf("OpenBridge: %v", err)
			}

			rewriteConfigByHand(t, configDir, tc.body)
			second, err := h.coord.openReleased(t.Context(), "c1", "")
			if err != nil {
				t.Fatalf("OpenBridge after the hand edit: %v", err)
			}

			if second != first {
				t.Fatalf("a hand edit of %s reopened the chat; a live setting is pushed into the running engine", tc.body)
			}
			if got := pushesSeen(fakeSourceOf(t, h).lane, rec); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("after a hand edit to %s the next open pushed %+v, want %+v", tc.body, got, tc.want)
			}
		})
	}
}

// A hand edit of debug_logs switches marotte's log level at the next open. It is also an engine restart reason
// (KAS reads its level at spawn), so whether that open finds the same chat depends on the restart's timing and is not
// asserted here.
func TestOpenBridge_AHandEditOfDebugLogsSwitchesTheLogLevel(t *testing.T) {
	rec := newLiveRecorder(t)
	h, configDir := reopenFixture(t, rec.options()...)
	rec.openWithStore(t, configDir)
	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	rewriteConfigByHand(t, configDir, `{"debug_logs":true}`)
	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge after the hand edit: %v", err)
	}
	rec.mu.Lock()
	debug := slices.Clone(rec.debug)
	rec.mu.Unlock()
	if !slices.Equal(debug, []bool{true}) {
		t.Errorf("after a hand edit of debug_logs the log level was set %v, want [true]", debug)
	}
}

func TestOpenBridge_ADocumentTurnedUnreadableIsReportedOnce(t *testing.T) {
	h, configDir := reopenFixture(t)
	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	lane := fakeSourceOf(t, h).lane
	before := len(lane.notified(marotte.MethodPolicyIgnoreFilesChanged))
	since := h.bus.fanout.Position().Head

	rewriteConfigByHand(t, configDir, "{not json")
	for range 2 {
		if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
			t.Fatalf("OpenBridge after the hand edit: %v", err)
		}
	}

	if frames := lane.notified(marotte.MethodPolicyIgnoreFilesChanged)[before:]; len(frames) != 0 {
		t.Errorf("an unreadable config.json pushed ignore lists %v; kiro-cli must keep enforcing the previous one", frames)
	}
	reports := 0
	for _, typ := range broadcastTypes(h, since) {
		if typ == marotte.EventPolicyError {
			reports++
		}
	}
	if reports != 1 {
		t.Errorf("two opens over an unreadable config.json broadcast %d policy errors, want 1", reports)
	}
}

func TestOpenBridge_ValuesTheDocumentHeldBeforeBootReachTheSurfacesThatNeverAppliedThem(t *testing.T) {
	configDir := t.TempDir()
	rewriteConfigByHand(t, configDir, `{"mcp_wait_for_ready":true,"debug_logs":true,"notify_pr_status":true}`)
	rec := newLiveRecorder(t)
	render := &fakeMCPRender{render: writesTrue}
	h := reopenFixtureIn(t, configDir, nil, WithKASMCPRenderer(render), WithDebugLogs(rec.debugLogs, rec.setDebug), WithPush(rec))

	for i := range 2 {
		if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
			t.Fatalf("OpenBridge %d: %v", i+1, err)
		}
		if got := render.count(); got != 1 {
			t.Errorf("after open %d the MCP file was rendered %d times, want 1: it was rendered with the wait off", i+1, got)
		}
		rec.mu.Lock()
		debug, prefs := slices.Clone(rec.debug), slices.Clone(rec.prefs)
		rec.mu.Unlock()
		if !slices.Equal(debug, []bool{true}) {
			t.Errorf("after open %d debug logs were set %v, want [true]: the log level was installed at info", i+1, debug)
		}
		if len(prefs) != 1 || !prefs[0][marotte.PushKindPRStatus] {
			t.Errorf("after open %d the notification toggles were pushed %v, want once with pr_status on", i+1, prefs)
		}
	}
}

func TestOpenBridge_AnMCPFileTheBootRenderFailedToWriteIsRenderedAtTheFirstOpen(t *testing.T) {
	render := &fakeMCPRender{unknown: true, render: func(context.Context, int) (bool, error) { return false, nil }}
	h, _ := reopenFixture(t, WithKASMCPRenderer(render))

	for i := range 2 {
		if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
			t.Fatalf("OpenBridge %d: %v", i+1, err)
		}
		if got := render.count(); got != 1 {
			t.Errorf("after open %d the MCP file was rendered %d times, want 1: KAS's file was never written", i+1, got)
		}
	}
}

// The engine starts at construction, so the report rides its spawn: counted from the ring's start.
func TestEngineSpawn_ADocumentUnreadableAtBootIsReportedOnce(t *testing.T) {
	configDir := t.TempDir()
	rewriteConfigByHand(t, configDir, "{not json")
	h := reopenFixtureIn(t, configDir, nil)
	const since = 0

	for i := range 2 {
		if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
			t.Fatalf("OpenBridge %d: %v", i+1, err)
		}
		reports := 0
		for _, typ := range broadcastTypes(h, since) {
			if typ == marotte.EventPolicyError {
				reports++
			}
		}
		if reports != 1 {
			t.Errorf("after open %d of a chat over a config.json unreadable since boot %d policy errors were broadcast, "+
				"want 1: the engine's spawn reports it, and no chat open repeats it", i+1, reports)
		}
	}
	msgs := policyErrorMessages(h, since)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "enforces only "+settings.AgentIgnoreFloor) {
		t.Errorf("the boot-time report reads %q, want it to say an engine started over it enforces only %s: "+
			"the engine it reports on started with no previous list", msgs, settings.AgentIgnoreFloor)
	}
}

func TestEngineSpawn_TheUnreadableReportFollowsTheDocumentTheEngineSpawnedOver(t *testing.T) {
	for _, tc := range []struct {
		name, atBoot, atSpawn string
		want                  int
	}{
		{name: "repaired_while_spawning", atBoot: "{not json", atSpawn: `{"agent_ignore_files":[".kiroignore"]}`, want: 0},
		{name: "broken_while_spawning", atBoot: `{"agent_ignore_files":[".kiroignore"]}`, atSpawn: "{not json", want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configDir := t.TempDir()
			rewriteConfigByHand(t, configDir, tc.atBoot)
			src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
			// The engine starts off the test goroutine, so this reports rather than aborts.
			src.onStart = func() {
				tmp := filepath.Join(configDir, "spawn.tmp")
				if err := os.WriteFile(tmp, []byte(tc.atSpawn), 0o600); err != nil {
					t.Errorf("Setup: write %s: %v", tmp, err)
					return
				}
				if err := os.Rename(tmp, filepath.Join(configDir, settings.Filename)); err != nil {
					t.Errorf("Setup: rename over config.json: %v", err)
				}
				for range 2 {
					if _, _, err := settings.Poll(context.Background(), configDir, func(context.Context, marotte.ServerEvent) {}); err != nil {
						t.Errorf("Setup: settings.Poll: %v", err)
					}
				}
			}
			cs := newTestChatStore()
			h := newRuntimeOn(context.Background(), t.TempDir(), src, cs, WithConfigDir(configDir))
			cs.wire(h)
			t.Cleanup(func() { shutdownHub(t, h) })
			since := h.bus.fanout.Position().Head

			openChat(t, h, "c1")

			if got := len(policyErrorMessages(h, since)); got != tc.want {
				t.Errorf("config.json %q at boot and %q while the engine spawned: %d policy errors, want %d",
					tc.atBoot, tc.atSpawn, got, tc.want)
			}
		})
	}
}

func policyErrorMessages(h *Runtime, since uint64) []string {
	var out []string
	for _, e := range h.bus.fanout.Snapshot() {
		var frame struct {
			Type    marotte.EventType          `json:"type"`
			Payload marotte.PolicyErrorPayload `json:"payload"`
		}
		if e.Offset <= since || json.Unmarshal(e.Event.Data, &frame) != nil || frame.Type != marotte.EventPolicyError {
			continue
		}
		for _, item := range frame.Payload.Errors {
			out = append(out, item.Message)
		}
	}
	return out
}

func openChat(t *testing.T, h *Runtime, chatID marotte.ChatID) {
	t.Helper()
	_, _ = h.chatStore.Mutate(t.Context(), chatID, func(c *marotte.Chat, _ bool) bool { c.Name = string(chatID); return true })
	if _, err := h.coord.openReleased(t.Context(), chatID, ""); err != nil {
		t.Fatalf("OpenBridge %s: %v", chatID, err)
	}
}

// A write landing while the readers decode is the next read's document: the readers decode the snapshot they were
// handed, whatever the file holds by then.
func TestReadLiveSettings_AWriteDuringTheReadersIsTheNextReadsDocument(t *testing.T) {
	for _, tc := range []struct {
		name         string
		next         string
		nextReadable bool
	}{
		{name: "replaced_by_another_document", next: `{"terminal_command_timeout_ms":600000,"debug_logs":true}`, nextReadable: true},
		{name: "truncated_mid_write", next: `{"debug_logs":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			rewriteConfigByHand(t, dir, `{"terminal_command_timeout_ms":300000,"debug_logs":false}`)
			fields := func(d settings.Document) liveSettings {
				rewriteConfigByHand(t, dir, tc.next)
				return readLiveFields(d)
			}

			got := readLiveSettings(t.Context(), dir, fields)

			if !got.readable || got.terminalTimeoutMs != 300000 || got.debugLogs {
				t.Errorf("the read during the write = readable %t, timeout %d, debug %t, want the first document's: readable, 300000, false",
					got.readable, got.terminalTimeoutMs, got.debugLogs)
			}
			next := readLiveSettings(t.Context(), dir, readLiveFields)
			if next.readable != tc.nextReadable {
				t.Fatalf("the next read over %s: readable = %t, want %t", tc.next, next.readable, tc.nextReadable)
			}
			if tc.nextReadable && (next.terminalTimeoutMs != 600000 || !next.debugLogs) {
				t.Errorf("the next read = timeout %d, debug %t, want %s's 600000 and true", next.terminalTimeoutMs, next.debugLogs, tc.next)
			}
		})
	}
}

func TestOpenBridge_AnUnparseableDocumentPushesNothingAndTheFixedOnePushesItsValues(t *testing.T) {
	rec := newLiveRecorder(t)
	h, configDir := reopenFixture(t, rec.options()...)
	rec.openWithStore(t, configDir)
	// No debug_logs in any document: moving it restarts the engine, which then reads the timeout at its spawn rather
	// than taking a frame on br.
	rewriteConfigByHand(t, configDir, `{"agent_ignore_files":[".gitignore"],"terminal_command_timeout_ms":300000,`+
		`"content_collection_enabled":true,"mcp_wait_for_ready":true,"notify_pr_status":true}`)
	openChat(t, h, "c1")
	br := fakeSourceOf(t, h).lane
	before := pushesSeen(br, rec)

	rewriteConfigByHand(t, configDir, `{"terminal_command_timeout_ms":`)
	openChat(t, h, "c1")
	if got := pushesSeen(br, rec); !reflect.DeepEqual(got, before) {
		t.Errorf("an open over an unparseable config.json pushed %+v after %+v, want nothing: its readers answer defaults", got, before)
	}

	rewriteConfigByHand(t, configDir, `{"terminal_command_timeout_ms":45000}`)
	openChat(t, h, "c1")
	got := pushesSeen(br, rec)
	if want := append(before.terminal, jsonOf(t, marotte.TerminalSettingsParams(45000))); !slices.Equal(got.terminal, want) {
		t.Errorf("the open once config.json parsed sent timeout frames %v, want %v", got.terminal, want)
	}
}

func TestOpenBridge_AConfigBrokenAfterTheSnapshotRendersNoMCPFileAndTheRepairRendersIt(t *testing.T) {
	rec := newLiveRecorder(t)
	h, configDir := reopenFixture(t, rec.options()...)
	rec.openWithStore(t, configDir)
	rewriteConfigByHand(t, configDir, `{"mcp_wait_for_ready":true}`)
	rec.mu.Lock()
	rec.beforeRender = func() { rewriteConfigByHand(t, configDir, "{half typed") }
	rec.mu.Unlock()
	before, err := os.Stat(rec.kasPath)
	if err != nil {
		t.Fatalf("Setup: stat KAS's MCP file: %v", err)
	}

	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}

	if after, err := os.Stat(rec.kasPath); err != nil || !os.SameFile(before, after) {
		t.Errorf("a config.json broken between the sync's read and its render rewrote KAS's MCP file (stat err %v), want no write", err)
	}
	rec.mu.Lock()
	rec.beforeRender = nil
	rec.mu.Unlock()
	rewriteConfigByHand(t, configDir, `{"mcp_wait_for_ready":true}`)
	sb, err := h.coord.openReleased(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge after the repair: %v", err)
	}
	if got := pushesSeen(fakeOf(sb), rec).mcpWaits; !slices.Equal(got, []bool{true}) {
		t.Errorf("renders after the repair = %v, want [true] at the next open", got)
	}
}
