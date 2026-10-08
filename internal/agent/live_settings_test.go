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
	return &liveRecorder{recordingPush: &recordingPush{}, applied: push.ResolvePreferences(t.Context(), t.TempDir())}
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
	r.kasPath = filepath.Join(t.TempDir(), "mcp.json")
	store, err := mcp.New(t.Context(), configDir, nil, mcp.WithKASConfigPath(r.kasPath),
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
	contentCollection []string
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
			name: "content_collection", body: `{"content_collection_enabled":true}`,
			want: livePushesSeen{contentCollection: []string{marotte.ConfigValueContentCollectionEnabled}},
		},
		{name: "mcp_wait", body: `{"mcp_wait_for_ready":true}`, want: livePushesSeen{mcpWaits: []bool{true}}},
		{name: "debug_logs", body: `{"debug_logs":true}`, want: livePushesSeen{debug: []bool{true}}},
		{
			name: "notification_toggle", body: `{"notify_pr_status":true}`,
			want: livePushesSeen{prefs: []map[marotte.PushKind]bool{{
				marotte.PushKindAgentFinished: true, marotte.PushKindPermission: true,
				marotte.PushKindPRStatus: true, marotte.PushKindRunOutcome: true,
			}}},
		},
		{name: "an_unrelated_key", body: `{"fb_path":"/workspace/src"}`},
		{
			name: "values_resolving_as_before",
			body: `{"agent_ignore_files":[],"terminal_command_timeout_ms":500,"content_collection_enabled":false,` +
				`"mcp_wait_for_ready":false,"debug_logs":false,"notify_agent_finished":true,"notify_pr_status":false}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := newLiveRecorder(t)
			h, configDir := reopenFixture(t, rec.options()...)
			rec.openWithStore(t, configDir)
			first, err := h.coord.OpenBridge(t.Context(), "c1", "")
			if err != nil {
				t.Fatalf("OpenBridge: %v", err)
			}

			rewriteConfigByHand(t, configDir, tc.body)
			second, err := h.coord.OpenBridge(t.Context(), "c1", "")
			if err != nil {
				t.Fatalf("OpenBridge after the hand edit: %v", err)
			}

			if second != first {
				t.Fatalf("a hand edit of %s reopened the chat; a live setting is pushed into the running process", tc.body)
			}
			if got := pushesSeen(fakeOf(first), rec); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("after a hand edit to %s the next open pushed %+v, want %+v", tc.body, got, tc.want)
			}
		})
	}
}

func TestOpenBridge_ADocumentTurnedUnreadableIsReportedOnce(t *testing.T) {
	h, configDir := reopenFixture(t)
	first, err := h.coord.OpenBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	since := h.bus.fanout.Position().Head

	rewriteConfigByHand(t, configDir, "{not json")
	for range 2 {
		if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
			t.Fatalf("OpenBridge after the hand edit: %v", err)
		}
	}

	if frames := fakeOf(first).notified(marotte.MethodPolicyIgnoreFilesChanged); len(frames) != 0 {
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
		if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
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
		if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
			t.Fatalf("OpenBridge %d: %v", i+1, err)
		}
		if got := render.count(); got != 1 {
			t.Errorf("after open %d the MCP file was rendered %d times, want 1: KAS's file was never written", i+1, got)
		}
	}
}

func TestOpenBridge_ADocumentUnreadableAtBootIsReportedAtTheFirstOpenAndOnlyThen(t *testing.T) {
	configDir := t.TempDir()
	rewriteConfigByHand(t, configDir, "{not json")
	h := reopenFixtureIn(t, configDir, nil)
	since := h.bus.fanout.Position().Head

	for i := range 2 {
		if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
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
				"want 1: the first open spawns the process the report is about", i+1, reports)
		}
	}
	msgs := policyErrorMessages(h, since)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "started before the file is fixed enforces only "+settings.AgentIgnoreFloor) {
		t.Errorf("the boot-time report reads %q, want it to say a chat started meanwhile enforces only %s: "+
			"the chat it reports on was spawned with no previous list", msgs, settings.AgentIgnoreFloor)
	}
}

func TestOpenBridge_TheUnreadableReportFollowsTheDocumentTheNewProcessSpawnedOver(t *testing.T) {
	for _, tc := range []struct {
		name, atOpen, atSpawn string
		want                  int
	}{
		{name: "repaired_while_spawning", atOpen: "{not json", atSpawn: `{"agent_ignore_files":[".kiroignore"]}`, want: 0},
		{name: "broken_while_spawning", atOpen: `{"agent_ignore_files":[".kiroignore"]}`, atSpawn: "{not json", want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configDir := t.TempDir()
			rewriteConfigByHand(t, configDir, tc.atOpen)
			h := reopenFixtureIn(t, configDir, func(b *fakeBridge) {
				// Start may run off the test goroutine (the utility session), so this reports rather than aborts.
				b.onStart = func() {
					tmp := filepath.Join(configDir, "spawn.tmp")
					if err := os.WriteFile(tmp, []byte(tc.atSpawn), 0o600); err != nil {
						t.Errorf("Setup: write %s: %v", tmp, err)
						return
					}
					if err := os.Rename(tmp, filepath.Join(configDir, settings.Filename)); err != nil {
						t.Errorf("Setup: rename over config.json: %v", err)
					}
				}
			})
			since := h.bus.fanout.Position().Head

			if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
				t.Fatalf("OpenBridge: %v", err)
			}

			if got := len(policyErrorMessages(h, since)); got != tc.want {
				t.Errorf("config.json %q at the open and %q while the process spawned: %d policy errors, want %d",
					tc.atOpen, tc.atSpawn, got, tc.want)
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

func openChat(t *testing.T, h *Runtime, chatID marotte.ChatID) *sharedBridge {
	t.Helper()
	_, _ = h.chatStore.Mutate(t.Context(), chatID, func(c *marotte.Chat, _ bool) bool { c.Name = string(chatID); return true })
	sb, err := h.coord.OpenBridge(t.Context(), chatID, "")
	if err != nil {
		t.Fatalf("OpenBridge %s: %v", chatID, err)
	}
	return sb
}

func TestReadLiveSettings_AWriteBetweenTwoFieldReadsIsReadAsOneDocument(t *testing.T) {
	for _, tc := range []struct {
		name      string
		next      string
		readable  bool
		every     bool
		terminal  int
		debugLogs bool
	}{
		{name: "replaced_by_another_document", next: `{"terminal_command_timeout_ms":600000,"debug_logs":true}`, readable: true, terminal: 600000, debugLogs: true},
		{name: "truncated_mid_write", next: `{"debug_logs":`},
		{name: "replaced_at_every_read", next: `{"terminal_command_timeout_ms":600000,"debug_logs":true}`, every: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			rewriteConfigByHand(t, dir, `{"terminal_command_timeout_ms":300000,"debug_logs":false}`)
			var once sync.Once
			fields := func(ctx context.Context, dir string) liveSettings {
				read := readLiveFields(ctx, dir)
				if tc.every {
					rewriteConfigByHand(t, dir, tc.next)
				}
				once.Do(func() { rewriteConfigByHand(t, dir, tc.next) })
				return read
			}

			got := readLiveSettings(t.Context(), dir, fields)

			if got.readable != tc.readable {
				t.Fatalf("readLiveSettings over a write to %s between its field reads: readable = %t, want %t", tc.next, got.readable, tc.readable)
			}
			if tc.readable && (got.terminalTimeoutMs != tc.terminal || got.debugLogs != tc.debugLogs) {
				t.Errorf("readLiveSettings read timeout %d and debug %t, want %d and %t, both from %s",
					got.terminalTimeoutMs, got.debugLogs, tc.terminal, tc.debugLogs, tc.next)
			}
		})
	}
}

func TestOpenBridge_AnUnparseableDocumentPushesNothingAndTheFixedOnePushesItsValues(t *testing.T) {
	rec := newLiveRecorder(t)
	h, configDir := reopenFixture(t, rec.options()...)
	rec.openWithStore(t, configDir)
	rewriteConfigByHand(t, configDir, nonDefaultLiveValues)
	br := fakeOf(openChat(t, h, "c1"))
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
	if want := append(before.debug, false); !slices.Equal(got.debug, want) {
		t.Errorf("the open once config.json parsed set debug logs %v, want %v", got.debug, want)
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

	if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}

	if after, err := os.Stat(rec.kasPath); err != nil || !os.SameFile(before, after) {
		t.Errorf("a config.json broken between the sync's read and its render rewrote KAS's MCP file (stat err %v), want no write", err)
	}
	rec.mu.Lock()
	rec.beforeRender = nil
	rec.mu.Unlock()
	rewriteConfigByHand(t, configDir, `{"mcp_wait_for_ready":true}`)
	sb, err := h.coord.OpenBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge after the repair: %v", err)
	}
	if got := pushesSeen(fakeOf(sb), rec).mcpWaits; !slices.Equal(got, []bool{true}) {
		t.Errorf("renders after the repair = %v, want [true] at the next open", got)
	}
}
