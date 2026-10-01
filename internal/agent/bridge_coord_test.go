package agent

// Tests for bridge_coord.go: override application, the in-session model switch and
// its record, registry teardown on the last bridge, the turn-close push, and the
// silent successes.

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/kirosession"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/marotte/internal/translate"
)

// --- helpers ---

// recordingStartBridge records the StartOpts passed to Start, else a fakeBridge.
type recordingStartBridge struct {
	*fakeBridge
	lastStart marotte.StartOpts
	recMu     sync.Mutex
}

func newRecordingStartBridge() *recordingStartBridge {
	return &recordingStartBridge{fakeBridge: newFakeBridge()}
}

func (b *recordingStartBridge) Start(ctx context.Context, opts *marotte.StartOpts) error {
	b.recMu.Lock()
	b.lastStart = *opts
	b.recMu.Unlock()
	return b.fakeBridge.Start(ctx, opts)
}

func (b *recordingStartBridge) startOpts() marotte.StartOpts {
	b.recMu.Lock()
	defer b.recMu.Unlock()
	return b.lastStart
}

func newRecordingStartHub(t *testing.T) (*Runtime, *testChatStore, *recordingStartBridge) {
	t.Helper()
	cs := newTestChatStore()
	rb := newRecordingStartBridge()
	h := New(t.Context(), "/tmp/rec-start", func() ACPBridge { return rb }, cs)
	cs.wire(h)
	h.mcpRegistry.SignalReady()
	return h, cs, rb
}

// recordingPush records the body of each Send on a channel, plus the subject of
// the most recent one, read only after a body arrives so the field is ordered.
type recordingPush struct {
	sends chan string
	// reloads counts ReloadPreferences calls, for the SSE reconnect rule. Atomic
	// because the handler that calls it may not be on the test's goroutine.
	reloads atomic.Int32
	// noSubs flips HasSubscribers to false for the drop path. The zero value keeps a
	// subscriber present, so every fixture that predates it is unchanged.
	noSubs  atomic.Bool
	subject marotte.PushSubject
}

func (p *recordingPush) RegisterRoutes(*http.ServeMux)            {}
func (p *recordingPush) Subscribe(marotte.PushSubscription)       {}
func (p *recordingPush) Unsubscribe(string)                       {}
func (p *recordingPush) HasSubscribers() bool                     { return !p.noSubs.Load() }
func (p *recordingPush) SetPreferences(map[marotte.PushKind]bool) {}
func (p *recordingPush) ReloadPreferences(context.Context)        { p.reloads.Add(1) }
func (p *recordingPush) Close()                                   {}
func (p *recordingPush) Retract(marotte.PushSubject)              {}
func (p *recordingPush) Send(_ context.Context, _, body string, _ marotte.PushKind, subject marotte.PushSubject) {
	p.subject = subject
	select {
	case p.sends <- body:
	default:
	}
}

// --- OpenBridge overrides + persisted model ---

// On a fresh session/new path the override model wins over the chat's stored value,
// and the persisted model is copied from the started bridge's ModelID.
func TestGetOrCreateBridge_AppliesOverrides(t *testing.T) {
	h, cs, rb := newRecordingStartHub(t)
	ctx := t.Context()
	_, _ = cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "m-chat"
		return true // no ACPSessionID -> fresh session/new path
	})

	if _, err := h.coord.OpenBridge(ctx, "c1", "model-override"); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}

	opts := rb.startOpts()
	if opts.Model != "model-override" {
		t.Errorf("StartOpts.Model = %q, want %q (override must beat chat.Model)", opts.Model, "model-override")
	}

	c, _ := cs.Get(ctx, "c1")
	if c.Model != "fake-model" {
		t.Errorf("persisted chat.Model = %q, want %q (bridge model must be copied into the chat)", c.Model, "fake-model")
	}
}

// A RESUME carries the chat's supervised choice on its own door. KAS's fork copies
// no autopilot, so a supervised chat's tangent would otherwise load into autopilot
// while its record still reads supervised — writes applied with nobody asked.
func TestGetOrCreateBridge_CarriesSupervisedOntoTheLoadDoor(t *testing.T) {
	// A fresh bridge per spawn, and the load's door is found by its session id: the
	// rehydrate sweep this load fires starts the utility bridge on the same factory,
	// and a shared recorder would report THAT door — no session, not supervised — as
	// the load's.
	var mu sync.Mutex
	var spawned []*recordingStartBridge
	cs := newTestChatStore()
	h := New(t.Context(), t.TempDir(), func() ACPBridge {
		rb := newRecordingStartBridge()
		mu.Lock()
		spawned = append(spawned, rb)
		mu.Unlock()
		return rb
	}, cs)
	cs.wire(h)
	h.mcpRegistry.SignalReady()

	ctx := t.Context()
	const acpSession = "sess_forked"
	if _, err := cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.SupervisedMode = true
		c.RecordSession(acpSession) // -> the session/load path
		return true
	}); err != nil {
		t.Fatalf("seed the chat: %v", err)
	}

	if _, err := h.coord.OpenBridge(ctx, "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}

	var opts marotte.StartOpts
	var found bool
	mu.Lock()
	for _, rb := range spawned {
		if o := rb.startOpts(); o.SessionID == acpSession {
			opts, found = o, true
		}
	}
	mu.Unlock()
	if !found {
		// Without a Start naming the stored session there was no load to assert on,
		// so every check below would pass or fail for an unrelated reason.
		t.Fatalf("no bridge was started with SessionID %q, so the resume never happened", acpSession)
	}
	if !opts.Supervised {
		t.Error("the resume's StartOpts.Supervised = false although the chat is supervised, " +
			"so KAS runs the loaded session in autopilot and its writes are applied unreviewed")
	}
}

// --- ApplyModelSwitch ---

// A successful in-session SetModel returns true, and the chat's reasoning-effort
// level is re-applied after the swap. The re-apply is the load-bearing half: KAS
// reconciles the session's effortLevel against the NEW model's tier list, so a chat
// at max dropped to the new default while the record and the pill still read max.
func TestApplyModelSwitch_SucceedsAndReAppliesEffort(t *testing.T) {
	h, cs, br := newTestHub()
	ctx := t.Context()
	_, _ = cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; c.Model = "m-old"; return true })
	if _, err := h.coord.OpenBridge(ctx, "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}

	if got := h.coord.ApplyModelSwitch(ctx, "c1", "m-new", "max"); got != true {
		t.Errorf("ApplyModelSwitch(success) = %v, want true", got)
	}
	if got := br.lastEffort(); got != "max" {
		t.Errorf("effort re-applied after the swap = %q, want %q; KAS resets the level inside the model swap", got, "max")
	}
}

// A switch never touches a turn: the session survives the swap, so a turn that was
// going to finish still finishes with its accumulator intact and nothing closes it.
// A busy chat's switch is parked on the header as pending_model and applied by the
// closer instead, so reaching this path mid-turn (a second device, a client draining
// its queue) must not displace the reply already on every screen.
func TestApplyModelSwitch_TouchesNoOpenTurn(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	_, _ = cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; c.Model = "m-old"; return true })
	if _, err := h.coord.OpenBridge(ctx, "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	id, log := streamingPromptTurn(t, h, "c1", "the user's own reply, still streaming")

	if got := h.coord.ApplyModelSwitch(ctx, "c1", "m-new", ""); !got {
		t.Fatalf("ApplyModelSwitch = %v, want true", got)
	}

	if after := h.liveTurn("c1"); after != log {
		t.Errorf("open turn after the swap = %p, want the prompt's own %s (%p) still open", after, id, log)
	}
	if closes := closesOf(t, logOf(t, cs, "c1")); len(closes) != 0 {
		t.Errorf("the log holds %d turn_close after the swap, want 0: a switch closes no turn", len(closes))
	}
}

// A chat that has chosen no level sends no effort call: there is nothing to
// re-assert, and the service's own reconciliation is the right answer.
func TestApplyModelSwitch_NoEffortChoiceSendsNoEffortCall(t *testing.T) {
	h, cs, br := newTestHub()
	ctx := t.Context()
	_, _ = cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; c.Model = "m-old"; return true })
	if _, err := h.coord.OpenBridge(ctx, "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}

	if got := h.coord.ApplyModelSwitch(ctx, "c1", "m-new", ""); got != true {
		t.Errorf("ApplyModelSwitch(success) = %v, want true", got)
	}
	if got := br.lastEffort(); got != "" {
		t.Errorf("effort applied = %q, want none for a chat that chose no level", got)
	}
}

// --- repairEffort: the level KAS changed on its own ---

// A prompt on an ALREADY-OPEN bridge re-asserts the chat's level, the only
// checkpoint that catches a level KAS moved without marotte asking:
// pinSessionModelId settling an unset model on the first prompt, or a switch made
// from the Kiro IDE or TUI on a shared session. Neither is a marotte action.
func TestOpenBridge_RepairsTheEffortOnAnOpenBridge(t *testing.T) {
	h, cs, br := newTestHub()
	ctx := t.Context()
	_, _ = cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.Effort = "max"
		return true
	})
	if _, err := h.coord.OpenBridge(ctx, "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	// Stand in for KAS moving the level underneath marotte.
	br.mu.Lock()
	br.effort = "high"
	br.mu.Unlock()

	if _, err := h.coord.OpenBridge(ctx, "c1", ""); err != nil {
		t.Fatalf("OpenBridge (reopen): %v", err)
	}

	if got := br.lastEffort(); got != "max" {
		t.Errorf("effort after a prompt on the open bridge = %q, want %q", got, "max")
	}
}

// A chat that has chosen no level, and has no seed to follow, asks for nothing: a
// call would only re-impose a level nobody picked.
func TestOpenBridge_RepairsNothingWithoutAChoice(t *testing.T) {
	h, cs, br := newTestHub()
	ctx := t.Context()
	_, _ = cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	if _, err := h.coord.OpenBridge(ctx, "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	br.mu.Lock()
	br.effort = ""
	br.mu.Unlock()

	if _, err := h.coord.OpenBridge(ctx, "c1", ""); err != nil {
		t.Fatalf("OpenBridge (reopen): %v", err)
	}

	if got := br.lastEffort(); got != "" {
		t.Errorf("effort applied = %q, want none for a chat with no choice and no seed", got)
	}
}

// --- effortFor ---

// writeEffortSeed writes a config.json carrying only the per-model effort seed.
func writeEffortSeed(t *testing.T, dir string, seed map[string]string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{settings.KeyLastEffortByModel: seed})
	if err != nil {
		t.Fatalf("marshal the seed map: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), body, 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
}

// effortFor prefers the chat's own choice, falls back to the level remembered for
// the chat's OWN model, and refuses a level too malformed to be a tier id. Shape
// only: the tier vocabulary is per model and KAS's to judge, so a well-formed
// unknown seed flows. No catalog is loaded here, so the model-default rung
// contributes nothing and every miss reads as "send none".
func TestEffortFor_PrefersTheChatThenTheSeed(t *testing.T) {
	tests := map[string]struct {
		chatEffort string
		chatModel  string
		seed       map[string]string
		want       string
	}{
		"chat choice wins over the seed":        {chatEffort: "max", chatModel: "m1", seed: map[string]string{"m1": "low"}, want: "max"},
		"seed answers for an unset chat":        {chatEffort: "", chatModel: "m1", seed: map[string]string{"m1": "xhigh"}, want: "xhigh"},
		"no choice and no seed sends none":      {chatEffort: "", chatModel: "m1", want: ""},
		"a malformed seed level is refused":     {chatEffort: "", chatModel: "m1", seed: map[string]string{"m1": "TURBO"}, want: ""},
		"a well-formed unknown level flows":     {chatEffort: "", chatModel: "m1", seed: map[string]string{"m1": "none"}, want: "none"},
		"another model's entry is not this one": {chatEffort: "", chatModel: "m2", seed: map[string]string{"m1": "max"}, want: ""},
		"a modelless chat never takes a seed":   {chatEffort: "", chatModel: "", seed: map[string]string{"m1": "max"}, want: ""},
		"the choice survives a seed miss":       {chatEffort: "high", chatModel: "m2", seed: map[string]string{"m1": "max"}, want: "high"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if test.seed != nil {
				writeEffortSeed(t, dir, test.seed)
			}
			h, _, _ := newTestHub()
			h.coord.lifecycle.configDir = dir

			got := h.coord.effortFor(t.Context(), &marotte.Chat{ID: "c1", Effort: test.chatEffort, Model: test.chatModel})

			if got != test.want {
				t.Errorf("effortFor(chat=%q, model=%q, last_effort_by_model=%v) = %q, want %q",
					test.chatEffort, test.chatModel, test.seed, got, test.want)
			}
		})
	}
}

// A pick on one model must not retract another model's remembered level. This is
// the whole reason the seed is a map: with one slot for the app, resolving either
// chat answered "" for the other, so no chat's drift could be repaired.
func TestEffortFor_OneModelsPickDoesNotRetractAnother(t *testing.T) {
	dir := t.TempDir()
	writeEffortSeed(t, dir, map[string]string{"m1": "max", "m2": "low"})
	h, _, _ := newTestHub()
	h.coord.lifecycle.configDir = dir

	for model, want := range map[string]string{"m1": "max", "m2": "low"} {
		if got := h.coord.effortSeedFor(t.Context(), model); got != want {
			t.Errorf("effortSeedFor(%q) = %q, want %q — each model keeps its own remembered level", model, got, want)
		}
		if got := h.coord.effortFor(t.Context(), &marotte.Chat{ID: "c-" + model, Model: model}); got != want {
			t.Errorf("effortFor(chat on %q) = %q, want %q — each model keeps its own remembered level", model, got, want)
		}
	}
}

// A chat that has chosen nothing and whose model has no remembered level resolves
// that MODEL's catalog default, not "". Both repair paths return on an empty level,
// so without this rung a drifted chat has no level to be corrected against.
func TestEffortFor_FallsBackToTheModelsCatalogDefault(t *testing.T) {
	h, _, _ := newTestHub()
	h.coord.lifecycle.configDir = t.TempDir()
	h.coord.catalog.SetModels([]marotte.SessionModel{{ID: "m1", DefaultEffortLevel: "high"}})

	got := h.coord.effortFor(t.Context(), &marotte.Chat{ID: "c1", Model: "m1"})

	if got != "high" {
		t.Errorf("effortFor(chat on m1, no choice and no seed) = %q, want high — the model's own default_effort_level is the last rung", got)
	}
}

// EffortForSwitch resolves against the TARGET model: the level remembered for that
// model, else the target's own default from the WORKSPACE catalog.
func TestEffortForSwitch_SeedThenModelDefault(t *testing.T) {
	catalog := []marotte.SessionModel{
		{ID: "m1", DefaultEffortLevel: "high"},
		{ID: "m2", DefaultEffortLevel: "medium"},
	}
	tests := map[string]struct {
		seed   map[string]string
		target string
		want   string
	}{
		"the target's own entry wins":            {seed: map[string]string{"m2": "max"}, target: "m2", want: "max"},
		"another model's entry yields a default": {seed: map[string]string{"m1": "max"}, target: "m2", want: "medium"},
		"no seed yields the target's default":    {target: "m1", want: "high"},
		"unknown target yields nothing":          {target: "m9", want: ""},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if test.seed != nil {
				writeEffortSeed(t, dir, test.seed)
			}
			h, _, _ := newTestHub()
			h.coord.lifecycle.configDir = dir
			h.coord.catalog.SetModels(catalog)

			got := h.coord.EffortForSwitch(t.Context(), test.target)

			if got != test.want {
				t.Errorf("EffortForSwitch(target=%q, last_effort_by_model=%v) = %q, want %q — the chat's own choice must never leak into a switch",
					test.target, test.seed, got, test.want)
			}
		})
	}
}

// The seed is a fallback, never a write: resolving it must not stamp the level
// onto the chat record, or that chat stops following the setting forever.
func TestEffortFor_DoesNotWriteTheSeedOntoTheChat(t *testing.T) {
	dir := t.TempDir()
	writeEffortSeed(t, dir, map[string]string{"m1": "max"})
	h, _, _ := newTestHub()
	h.coord.lifecycle.configDir = dir
	chat := &marotte.Chat{ID: "c1", Model: "m1"}

	if got := h.coord.effortFor(t.Context(), chat); got != "max" {
		t.Fatalf("effortFor = %q, want max", got)
	}
	if chat.Effort != "" {
		t.Errorf("chat.Effort = %q, want it left empty; the seed is resolved per spawn, not persisted", chat.Effort)
	}
}

// --- Forward clears the registry only when the last bridge exits ---

// When the forwarded bridge is the last one, Forward clears the MCP
// registry; when another bridge remains registered, it must not.
func TestForward_ClearsRegistryOnlyWhenLastBridge(t *testing.T) {
	seed := func(h *Runtime) {
		h.mcpRegistry.mu.Lock()
		h.mcpRegistry.servers["srv"] = &mcpServerRuntime{Name: "srv", State: mcpStateConnected}
		h.mcpRegistry.mu.Unlock()
	}

	t.Run("clears_when_no_bridges_remain", func(t *testing.T) {
		h, _, br := newTestHub()
		seed(h)
		br.Stop() // close notifCh so Forward's range exits immediately
		h.coord.Forward("nochat", br)
		if n := len(h.mcpRegistry.Snapshot()); n != 0 {
			t.Errorf("registry size = %d, want 0 (no bridges left must clearAll)", n)
		}
	})

	t.Run("keeps_when_a_bridge_remains", func(t *testing.T) {
		h, _, _ := newTestHub()
		seed(h)
		// A bridge that stays registered so count() stays >= 1.
		h.bridge.mgr.orInsert("keep")
		other := newFakeBridge()
		other.Stop()
		h.coord.Forward("other", other)
		if n := len(h.mcpRegistry.Snapshot()); n != 1 {
			t.Errorf("registry size = %d, want 1 (a remaining bridge must NOT clearAll)", n)
		}
	})
}

// KAS reviews a whole turn at once, so there is no per-turn trust gate to test.

// A non-cancelled turn fires the "Agent finished" push.
func TestSettleTurnOnResponse_NonCancelledFiresPush(t *testing.T) {
	cs := newTestChatStore()
	fp := &recordingPush{sends: make(chan string, 4)}
	h := New(t.Context(), "/tmp/push", func() ACPBridge { return newFakeBridge() }, cs, WithPush(fp))
	cs.wire(h)
	h.mcpRegistry.SignalReady()
	ctx := t.Context()

	id, log := h.stagePromptTurn(t, "c1")
	sayText(t, log)
	resp := &marotte.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "end_turn"})}
	h.SettleTurnOnResponse(ctx, "c1", id, 0, resp)

	select {
	case body := <-fp.sends:
		if body != "Agent finished" {
			t.Errorf("push body = %q, want %q", body, "Agent finished")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no push sent for a non-cancelled turn")
	}
}

// --- success paths must not emit an error log ---

// A settled turn whose seal and close both land logs no error: the log is where an
// operator looks for a failed persist, so a line there on every ordinary turn buries
// the real one.
func TestSettleTurnOnResponse_NoErrorLogOnSuccess(t *testing.T) {
	h, _, _ := newTestHub()
	ctx := t.Context()
	id, _ := streamingPromptTurn(t, h, "c1", "the reply")

	logs := captureLogs(t)
	resp := &marotte.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "cancelled"})}
	h.SettleTurnOnResponse(ctx, "c1", id, 0, resp)

	if got := logs.String(); strings.Contains(got, `"level":"ERROR"`) {
		t.Errorf("an ERROR line on a turn that settled cleanly: %s", got)
	}
}

// PersistModelSwitch logs nothing when the entry append and the header write both
// succeed.
func TestPersistModelSwitch_NoErrorLogOnSuccess(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	_, _ = cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; c.Model = "m-old"; return true })

	logs := captureLogs(t)
	h.coord.PersistModelSwitch(ctx, "c1", marotte.EntryModelSwitched{From: "m-old", To: "m-new"}, 1234)
	if got := logs.String(); strings.Contains(got, "switch_model:") {
		t.Errorf("unexpected switch_model error log on success: %s", got)
	}
}

// A landed switch is one model_switched entry between turns plus a header that
// takes the pick: pending_model cleared (or every later close re-applies the switch
// and resets the counters again), the old model's tier dropped, usage reset to the
// new context size.
func TestPersistModelSwitch_RecordsTheSwitchAndClearsThePendingPick(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	_, _ = cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "m-old"
		c.PendingModel = "m-new"
		c.Effort = "max"
		c.Usage = marotte.Usage{ContextSize: 100, Credits: 7}
		return true
	})

	h.coord.PersistModelSwitch(ctx, "c1",
		marotte.EntryModelSwitched{From: "m-old", To: "m-new", Effort: "high"}, 1234)

	entries := logOf(t, cs, "c1")
	switches := switchesOf(t, entries)
	// The tier travels on the entry: the banner names it, and the header clears its
	// own copy two assertions below, so the entry is the only record of it.
	if len(switches) != 1 || switches[0] != (marotte.EntryModelSwitched{From: "m-old", To: "m-new", Effort: "high"}) {
		t.Errorf("model_switched entries = %+v, want one {From: m-old, To: m-new, Effort: high}", switches)
	}
	if closes := closesOf(t, entries); len(closes) != 0 {
		t.Errorf("the log holds %d turn_close, want 0: the switch opened and closed no turn", len(closes))
	}

	c, ok := cs.Get(ctx, "c1")
	if !ok {
		t.Fatal("the chat vanished")
	}
	if c.Model != "m-new" || c.PendingModel != "" || c.Effort != "" {
		t.Errorf("header after the switch = {Model %q, PendingModel %q, Effort %q}, want {m-new, \"\", \"\"}",
			c.Model, c.PendingModel, c.Effort)
	}
	if c.Usage.ContextSize != 1234 || c.Usage.Credits != 0 {
		t.Errorf("Usage after the switch = %+v, want the counters reset and ContextSize 1234", c.Usage)
	}
}

// --- PersistEffortChange: the effort-only banner ---

// An effort change picked BETWEEN turns is appended on its own, and its From == To
// is what tells the renderer it was not a model switch.
func TestPersistEffortChange_AppendsTheTierBetweenTurns(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	_, _ = cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; c.Model = "opus-5"; return true })

	h.coord.PersistEffortChange(ctx, "c1", "opus-5", marotte.EffortMax)

	switches := switchesOf(t, logOf(t, cs, "c1"))
	want := marotte.EntryModelSwitched{From: "opus-5", To: "opus-5", Effort: "max"}
	if len(switches) != 1 || switches[0] != want {
		t.Errorf("model_switched entries = %+v, want one %+v", switches, want)
	}
}

// The same pick DURING a turn folds into that turn instead, so a tier changed
// mid-reply lands at the point it happened rather than after the turn closes.
func TestPersistEffortChange_FoldsTheTierIntoAnOpenTurn(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	_, _ = cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; c.Model = "opus-5"; return true })
	turnID, _ := streamingPromptTurn(t, h, "c1", "the reply")

	h.coord.PersistEffortChange(ctx, "c1", "opus-5", marotte.EffortHigh)

	entries := logOf(t, cs, "c1")
	switches := switchesOf(t, entries)
	want := marotte.EntryModelSwitched{From: "opus-5", To: "opus-5", Effort: "high"}
	if len(switches) != 1 || switches[0] != want {
		t.Fatalf("model_switched entries = %+v, want one %+v", switches, want)
	}
	for i := range entries {
		if entries[i].Kind == marotte.EntryKindModelSwitched && entries[i].Turn != turnID {
			t.Errorf("the entry sits in turn %q, want the open turn %q", entries[i].Turn, turnID)
		}
	}
}

// A chat with no model writes nothing: the renderer reads an empty To as
// `Context reset`, which is neither true here nor recoverable, and CmdSetEffort
// auto-creates a record so this is the ordinary state of a pick before the first
// prompt.
func TestPersistEffortChange_AChatWithNoModelWritesNothing(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	_, _ = cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })

	h.coord.PersistEffortChange(ctx, "c1", "", marotte.EffortHigh)

	if switches := switchesOf(t, logOf(t, cs, "c1")); len(switches) != 0 {
		t.Errorf("model_switched entries = %+v, want none for a modelless chat", switches)
	}
}

// --- adoptKASTitle: the bottom of the chat-naming precedence ---

// TestAdoptKASTitle pins all four arms of the guard. Every refusal here is a bug
// that compiles cleanly: adopting KAS's "New Session" placeholder makes the chat
// non-default-named, which then rejects the real title that arrives later, and
// adopting over an existing name clobbers a label that outranks this channel.
func TestAdoptKASTitle(t *testing.T) {
	cases := []struct {
		name  string
		start string
		title string
		want  string
	}{
		{
			name:  "adopts a real title onto a default-named chat",
			start: marotte.DefaultChatName,
			title: "Marotte conversational surface",
			want:  "Marotte conversational surface",
		},
		{
			name:  "refuses KAS's own placeholder",
			start: marotte.DefaultChatName,
			title: translate.KASDefaultSessionTitle,
			want:  marotte.DefaultChatName,
		},
		{
			name:  "refuses an empty title",
			start: marotte.DefaultChatName,
			title: "",
			want:  marotte.DefaultChatName,
		},
		{
			name:  "never overwrites a first-prompt label",
			start: "fix the reaper so it stops eating live sessions",
			title: "Reaper fix",
			want:  "fix the reaper so it stops eating live sessions",
		},
		{
			name:  "never overwrites an agent-authored focus title",
			start: "Reaper live-session exemption",
			title: translate.KASDefaultSessionTitle,
			want:  "Reaper live-session exemption",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &marotte.Chat{Name: tc.start}
			adoptKASTitle(c, tc.title)
			if c.Name != tc.want {
				t.Errorf("adoptKASTitle(%q, %q) left name %q, want %q",
					tc.start, tc.title, c.Name, tc.want)
			}
		})
	}
}

// This rung reads what KAS STORED, so it gets the SAME door treatment the live focus
// channel gets — sanitizer, bound, rune cap and shape rules — and the cases below are
// one per part of it. A stored title is not the safer input: KAS keeps its own session
// title independently of marotte's chat name, nothing bounded or sanitized it on the
// way in, a session titled by a pre-gate build re-offers that string on every resume,
// and a rename from the IDE or the TUI can put one there at any time. It is also the
// worse door, because a resume names a chat whose record was recreated and is
// therefore default-named, which is exactly the state this rung adopts into.
func TestAdoptKASTitle_AppliesTheWholeDoorTreatment(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  string
	}{
		{
			// Verbatim from the live volume's poisoned chat record.
			name:  "a_stored_model_refusal",
			title: "I need more context to generate a title. Could you share the user's first mes...",
			want:  marotte.DefaultChatName,
		},
		{
			name:  "a_stored_truncation_of_the_first_prompt",
			title: "Safari on Mac throws this console error for marotte: [Error] ResizeObserver l...",
			want:  marotte.DefaultChatName,
		},
		{
			// Nothing on the wire bounds this field and a stored title is not
			// Ete-capped, so the OUTCOME is what this pins: an arbitrarily long
			// string never reaches Chat.Name. Two rules refuse it independently —
			// the sanitizer's bound leaves a "..." marker the truncation rule
			// catches, and 515 runes is over the cap either way — so retuning one
			// of them cannot open it. The log test below is what pins the bound.
			name:  "an_unbounded_stored_title",
			title: strings.Repeat("x", 700),
			want:  marotte.DefaultChatName,
		},
		{
			// The sanitizer, and the reason it runs before the rules rather than
			// after them: the stored form is what gets compared and kept.
			name:  "a_stored_title_carrying_ansi_and_a_newline",
			title: "\x1b[31mRelease\x1b[0m\ncheck",
			want:  "Release check",
		},
		{
			name:  "a_real_stored_title_is_still_adopted",
			title: "Fix ResizeObserver Error In Safari",
			want:  "Fix ResizeObserver Error In Safari",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &marotte.Chat{Name: marotte.DefaultChatName}
			adoptKASTitle(c, tc.title)
			if c.Name != tc.want {
				t.Errorf("adoptKASTitle(%q) left name %q, want %q", tc.title, c.Name, tc.want)
			}
		})
	}
}

// The refusal line is the one place a title marotte did NOT adopt still reaches an
// operator, and it is the same untrusted string: nothing on the wire bounds the field
// and a stored title is not Ete-capped. So the line carries the SANITIZED form plus the
// rule that fired. Logging the raw argument instead is a one-word edit that puts
// unbounded control-bearing text into the log store, and nothing else would notice.
func TestAdoptKASTitle_LogsTheSanitizedTitleWithItsReason(t *testing.T) {
	logs := captureLogs(t)
	stored := "\x1b[31m" + strings.Repeat("x", 700) + "\nmore"

	adoptKASTitle(&marotte.Chat{Name: marotte.DefaultChatName}, stored)

	var rec struct {
		Title  string `json:"title"`
		Reason string `json:"reason"`
	}
	line := strings.TrimSpace(logs.String())
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("adoptKASTitle logged %q, want one JSON record: %v", line, err)
	}
	if rec.Reason == "" {
		t.Errorf("adoptKASTitle logged reason %q, want the rule that fired", rec.Reason)
	}
	if strings.ContainsAny(rec.Title, "\x1b\n") {
		t.Errorf("adoptKASTitle logged title %q, want it sanitized of ANSI and newlines", rec.Title)
	}
	if len(rec.Title) >= len(stored) {
		t.Errorf("adoptKASTitle logged %d title bytes for a %d-byte stored title, want it bounded",
			len(rec.Title), len(stored))
	}
}

// --- sweepSessionsOnce: the keep-list is chat-referenced UNION live ---

// testReaperWorkDir is the workspace root the reaper fixtures are built for: both
// the runtime's workDir and the root every fixture session claims in its own
// session.json, because the reaper reaps only for the workspace it was built with.
const testReaperWorkDir = "/tmp/work"

// writeSessionRecord writes the session.json the reaper reads to decide whether a
// session belongs to its workspace. A fixture without one is DOUBT, which the
// reaper answers by retaining — correct in production and vacuous in a reap test.
func writeSessionRecord(t *testing.T, sessionDir, workspaceRoot string) {
	t.Helper()
	body := `{"workspacePaths":["` + workspaceRoot + `"]}`
	if err := os.WriteFile(filepath.Join(sessionDir, "session.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write session record in %s: %v", sessionDir, err)
	}
}

// TestSweepSessionsOnce_KeepListCompleteness pins doubt-retains at the sweep boundary against
// a real orphan on disk: a partial keep-list means some chat's sessions are missing from it,
// so sweeping anyway deletes them, where not sweeping only postpones reclaiming disk. The
// control arm proves the orphan really was reapable. Its keep-list names a session that EXISTS
// on disk rather than being empty, because an empty keep-list is refused outright by the
// reaper — using it as the control would make this test assert the opposite of that guard.
func TestSweepSessionsOnce_KeepListCompleteness(t *testing.T) {
	cases := []struct {
		name        string
		complete    bool
		wantSurvive bool
	}{
		{name: "incomplete keep-list spares the orphan", complete: false, wantSurvive: true},
		{name: "complete keep-list reaps it (control)", complete: true, wantSurvive: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sessionsDir := t.TempDir()
			old := time.Now().Add(-24 * time.Hour)
			// An orphan old enough to clear the reaper's create-race guard, plus
			// a referenced sibling so the keep-list is non-empty and the sweep is
			// discriminating rather than refusing.
			orphan := filepath.Join(sessionsDir, "hash01", "sess_orphan")
			kept := filepath.Join(sessionsDir, "hash01", "sess_ref")
			for _, p := range []string{orphan, kept} {
				if err := os.MkdirAll(p, 0o700); err != nil {
					t.Fatalf("mkdir %s: %v", p, err)
				}
				// The reaper reads each candidate's own workspacePaths and skips
				// anything that does not name the workspace it was built for, so a
				// fixture without this record is retained whatever the keep-list
				// says — which would make the control arm pass for the wrong reason.
				writeSessionRecord(t, p, testReaperWorkDir)
				if err := os.Chtimes(p, old, old); err != nil {
					t.Fatalf("chtimes %s: %v", p, err)
				}
			}

			// Wire the reaper at CONSTRUCTION, not after: New starts
			// sweepSessionsLoop, which reads these fields, so assigning them
			// afterwards is a data race (caught by -race, not by plain go test).
			cs := newTestChatStore()
			h := New(t.Context(), testReaperWorkDir, func() ACPBridge { return newFakeBridge() }, cs,
				WithSessionReaper(
					kirosession.New(sessionsDir, testReaperWorkDir),
					func(context.Context) (map[string]struct{}, bool) {
						return map[string]struct{}{"sess_ref": {}}, tc.complete
					},
				))
			cs.wire(h)
			t.Cleanup(func() { shutdownHub(t, h) })

			h.sweepSessionsOnce()

			_, err := os.Stat(orphan)
			survived := err == nil
			if survived != tc.wantSurvive {
				t.Errorf("orphan survived = %v, want %v", survived, tc.wantSurvive)
			}
			if _, kErr := os.Stat(kept); kErr != nil {
				t.Errorf("referenced session was reaped: %v", kErr)
			}
		})
	}
}

// TestLiveSessionIDs_CoversEveryBridge pins that the exemption is general: any
// bridge holding a session no chat references would otherwise have its on-disk
// state deleted from under it once the session ages past the 10-minute guard,
// which is a create-race cushion and not a liveness test.
func TestLiveSessionIDs_CoversEveryBridge(t *testing.T) {
	// newTestHub's factory hands back ONE shared fake so tests can inspect it;
	// this test needs bridges with distinct session ids, so build the runtime with
	// a per-spawn factory instead.
	cs := newTestChatStore()
	h := New(t.Context(), testReaperWorkDir, func() ACPBridge { return newFakeBridge() }, cs)
	cs.wire(h)

	setSession := func(chatID marotte.ChatID, sessionID string) {
		t.Helper()
		sb, _ := h.bridge.mgr.orInsert(chatID)
		fb, ok := sb.bridge.(*fakeBridge)
		if !ok {
			t.Fatalf("bridge for %s is not a *fakeBridge", chatID)
		}
		fb.mu.Lock()
		fb.sessionID = sessionID
		fb.mu.Unlock()
	}

	setSession("chatA", "sess_chatA")
	setSession("chatB", "sess_chatB")
	// A bridge that has not started a session yet contributes nothing.
	setSession("chatC", "")

	got := h.liveSessionIDs()
	slices.Sort(got)
	want := []string{"sess_chatA", "sess_chatB"}
	if !slices.Equal(got, want) {
		t.Errorf("liveSessionIDs() = %v, want %v", got, want)
	}
}

// TestApplyLoadedSessionFacts_KeepsWhatTheResultOmitted pins the resume half of
// the mode contract: a fact the load result did not carry must not be written. A
// resumed bridge is freshly constructed, so it answers the zero value for anything
// absent, and writing those zeros wiped what the chat file had carried since its
// previous session. The CATALOGS are not written here — Catalog owns that rule.
func TestApplyLoadedSessionFacts_KeepsWhatTheResultOmitted(t *testing.T) {
	cases := map[string]struct {
		mode     string
		wantMode string
	}{
		"a silent result changes nothing":       {mode: "", wantMode: "spec"},
		"what the result DOES carry is written": {mode: "vibe", wantMode: "vibe"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := &marotte.Chat{Name: "A", CurrentModeID: "spec"}
			br := &fakeBridge{currentMode: tc.mode}

			applyLoadedSessionFacts(c, br, "")

			if c.CurrentModeID != tc.wantMode {
				t.Errorf("CurrentModeID = %q, want %q", c.CurrentModeID, tc.wantMode)
			}
		})
	}
}

// TestApplyLoadedSessionFacts_KeepsContextThresholds pins the same keep-on-absent
// contract one layer up: a resumed bridge is freshly constructed, so it answers 0 for a
// threshold the load result omitted, and writing that zero would replace a pair the chat
// file has carried since its previous session.
func TestApplyLoadedSessionFacts_KeepsContextThresholds(t *testing.T) {
	cases := map[string]struct {
		summarization, truncation         float64
		wantSummarization, wantTruncation float64
	}{
		"a silent result keeps both":         {wantSummarization: 80, wantTruncation: 95},
		"what the result carries is written": {summarization: 85, truncation: 97, wantSummarization: 85, wantTruncation: 97},
		"one carried member keeps the other": {summarization: 85, wantSummarization: 85, wantTruncation: 95},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := &marotte.Chat{Name: "A"}
			c.Usage.SummarizationThresholdPct = 80
			c.Usage.TruncationThresholdPct = 95
			br := &fakeBridge{summarizationPct: tc.summarization, truncationPct: tc.truncation}

			applyLoadedSessionFacts(c, br, "")

			if c.Usage.SummarizationThresholdPct != tc.wantSummarization {
				t.Errorf("SummarizationThresholdPct = %v, want %v",
					c.Usage.SummarizationThresholdPct, tc.wantSummarization)
			}
			if c.Usage.TruncationThresholdPct != tc.wantTruncation {
				t.Errorf("TruncationThresholdPct = %v, want %v",
					c.Usage.TruncationThresholdPct, tc.wantTruncation)
			}
		})
	}
}

// TestPersistNewSessionMetadata_ReportsAModeThatWasNotApplied pins the visibility half of the
// mode contract. applyInitialMode warns and continues when session/set_mode is refused, so the
// session runs the engine's default, and persistNewSessionMetadata then writes the ACTUAL mode
// onto the chat — right, because the pill must not claim a role the agent is not running under,
// but also the only record of the request. So one transient refusal permanently converts a chat
// pinned to "spec" into a default-mode chat: at the next spawn the ids match, so the guard
// skips the retry and nothing says why.
func TestPersistNewSessionMetadata_ReportsAModeThatWasNotApplied(t *testing.T) {
	cases := []struct {
		name       string
		requested  string
		actual     string
		wantReport bool
	}{
		{"a refused mode is reported", "spec", "vibe", true},
		{"the applied mode is not reported", "spec", "spec", false},
		{"a chat that asked for nothing is not reported", "", "vibe", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, cs, br := newTestHub()
			br.mu.Lock()
			br.currentMode = tc.actual
			br.mu.Unlock()
			_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
				c.Name = "A"
				c.CurrentModeID = tc.requested
				return true
			})

			since := h.bus.fanout.Position().Head
			h.coord.persistNewSessionMetadata(t.Context(), "c1", br)

			// The record always holds the mode the session is really in.
			c, _ := cs.Get(t.Context(), "c1")
			if c.CurrentModeID != tc.actual {
				t.Errorf("chat.CurrentModeID = %q, want the actual mode %q", c.CurrentModeID, tc.actual)
			}

			var reported bool
			for _, p := range errorPayloadsSince(t, h, since) {
				if p.Code != marotte.ErrCodeModeNotApplied {
					continue
				}
				reported = true
				if !strings.Contains(p.Message, tc.requested) {
					t.Errorf("message %q does not name the requested mode %q", p.Message, tc.requested)
				}
			}
			if reported != tc.wantReport {
				t.Errorf("mode_not_applied reported = %v, want %v", reported, tc.wantReport)
			}
		})
	}
}

// TestSpawnBridge_ReportsSupervisedThatWasNotApplied pins the session door's half of the
// supervised fail-open. applySupervised is best-effort inside Start — it logs at ERROR and
// continues — so a session that refuses `autopilot: off` used to open the chat UNSUPERVISED
// while the record, ChatHeader.supervised_mode and every client's checkbox still said
// supervised. The mode path already reported its own divergence; this one reached the user
// nowhere, on the one setting whose whole job is to stop a write landing unreviewed.
//
// The third case is why the report cannot key on the bridge's flag alone: false also means
// nobody asked, so a chat in autopilot would report a refusal on every spawn.
func TestSpawnBridge_ReportsSupervisedThatWasNotApplied(t *testing.T) {
	cases := []struct {
		name        string
		supervised  bool
		assertFails bool
		wantReport  bool
	}{
		{"a refused assert is reported", true, true, true},
		{"an accepted assert is not reported", true, false, false},
		{"a chat that asked for nothing is not reported", false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := newTestChatStore()
			br := newFakeBridge()
			br.mu.Lock()
			br.supervisedAssertFails = tc.assertFails
			br.mu.Unlock()
			h := New(t.Context(), "/tmp/work", func() ACPBridge { return br }, cs)
			cs.wire(h)
			h.mcpRegistry.SignalReady()
			_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
				c.Name = "A"
				c.SupervisedMode = tc.supervised
				return true
			})

			since := h.bus.fanout.Position().Head
			if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
				t.Fatalf("OpenBridge: %v", err)
			}

			// The chat keeps the REQUEST, unlike the mode path, which resets the record
			// to what the session took: supervised is the safer intent to remember, so
			// the next spawn re-asserts and this event is a report rather than the only
			// chance to act.
			c, _ := cs.Get(t.Context(), "c1")
			if c.SupervisedMode != tc.supervised {
				t.Errorf("chat.SupervisedMode = %v, want the request %v kept for the next spawn",
					c.SupervisedMode, tc.supervised)
			}

			var reported bool
			for _, p := range errorPayloadsSince(t, h, since) {
				if p.Code == marotte.ErrCodeSupervisedNotApplied {
					reported = true
				}
			}
			if reported != tc.wantReport {
				t.Errorf("supervised_not_applied reported = %v, want %v; a silent refusal leaves the "+
					"chat writing unreviewed with the checkbox still ticked", reported, tc.wantReport)
			}
		})
	}
}

// Closing a chat must NOT reap its durable KAS session; deleting one must. Sharing the delete
// path breaks the contract twice: the chat record survives with nothing left to
// `session/load`, and the History page — which lists KAS's sessions, not marotte's chat files
// — can only ever show chats that are still open. The delete arm is the control: without it, a
// close-preserves assertion would also pass if the reaper were simply unwired.
func TestChatTeardown_CloseKeepsSessionDeleteReapsIt(t *testing.T) {
	cases := []struct {
		name        string
		teardown    func(h *Runtime, ctx context.Context, id marotte.ChatID)
		wantSurvive bool
	}{
		{
			name:        "close keeps the session on disk",
			teardown:    func(h *Runtime, ctx context.Context, id marotte.ChatID) { h.CloseChatState(ctx, id) },
			wantSurvive: true,
		},
		{
			name:        "delete reaps it (control)",
			teardown:    func(h *Runtime, ctx context.Context, id marotte.ChatID) { h.DeleteChatState(ctx, id) },
			wantSurvive: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sessionsDir := t.TempDir()
			sessDir := filepath.Join(sessionsDir, "hash01", "sess_owned")
			if err := os.MkdirAll(sessDir, 0o700); err != nil {
				t.Fatalf("mkdir session: %v", err)
			}
			writeSessionRecord(t, sessDir, testReaperWorkDir)

			cs := newTestChatStore()
			h := New(t.Context(), testReaperWorkDir, func() ACPBridge { return newFakeBridge() }, cs,
				WithSessionReaper(
					kirosession.New(sessionsDir, testReaperWorkDir),
					func(context.Context) (map[string]struct{}, bool) {
						return map[string]struct{}{"sess_owned": {}}, true
					},
				))
			cs.wire(h)
			t.Cleanup(func() { shutdownHub(t, h) })

			ctx := t.Context()
			if _, err := cs.Mutate(ctx, "c-owner", func(c *marotte.Chat, _ bool) bool {
				c.Name = "owner"
				c.RecordSession("sess_owned")
				return true
			}); err != nil {
				t.Fatalf("seed chat: %v", err)
			}

			tc.teardown(h, ctx, "c-owner")

			_, err := os.Stat(sessDir)
			survived := err == nil
			if survived != tc.wantSurvive {
				t.Errorf("session survived = %v, want %v", survived, tc.wantSurvive)
			}
		})
	}
}

// TestChatTeardown_DeleteByChainReapsWithoutTheRecord is the close escalation's
// grade: the record is already deleted when the teardown runs, so the reap is
// driven from the chain captured before the commit. The record-reading grade is the
// control — on a recordless chat it must leave the session.
func TestChatTeardown_DeleteByChainReapsWithoutTheRecord(t *testing.T) {
	cases := []struct {
		name        string
		teardown    func(h *Runtime, ctx context.Context, id marotte.ChatID)
		wantSurvive bool
	}{
		{
			name: "the captured chain reaps with the record gone",
			teardown: func(h *Runtime, ctx context.Context, id marotte.ChatID) {
				h.DeleteChatStateByChain(ctx, id, []string{"sess_owned"})
			},
			wantSurvive: false,
		},
		{
			name:        "the record-reading grade no-ops without one (control)",
			teardown:    func(h *Runtime, ctx context.Context, id marotte.ChatID) { h.DeleteChatState(ctx, id) },
			wantSurvive: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sessionsDir := t.TempDir()
			sessDir := filepath.Join(sessionsDir, "hash01", "sess_owned")
			if err := os.MkdirAll(sessDir, 0o700); err != nil {
				t.Fatalf("mkdir session: %v", err)
			}
			writeSessionRecord(t, sessDir, testReaperWorkDir)

			cs := newTestChatStore()
			h := New(t.Context(), testReaperWorkDir, func() ACPBridge { return newFakeBridge() }, cs,
				WithSessionReaper(
					kirosession.New(sessionsDir, testReaperWorkDir),
					func(context.Context) (map[string]struct{}, bool) {
						return map[string]struct{}{"sess_owned": {}}, true
					},
				))
			cs.wire(h)
			t.Cleanup(func() { shutdownHub(t, h) })

			// NO chat record: the escalation deleted it inside the close commit.
			tc.teardown(h, t.Context(), "c-doomed")

			_, err := os.Stat(sessDir)
			survived := err == nil
			if survived != tc.wantSurvive {
				t.Errorf("session survived = %v, want %v", survived, tc.wantSurvive)
			}
		})
	}
}

// TestSessionLoad_HealsTheChatsRestartPausedRuns is the recovery model for agent-launched
// runs, and the reason there is no Resume button anywhere. A restart kills a chat's bridge,
// which KAS reconciles by PAUSING the runs that bridge launched; the user's next message
// respawns it and this sweep makes the run heal with the chat. The sweep runs OFF the spawn
// path deliberately — the prompt must not wait behind a run-list round trip — so the resume is
// awaited rather than assumed, and the wait fails closed.
func TestSessionLoad_HealsTheChatsRestartPausedRuns(t *testing.T) {
	h, cs, br := newTestHub()
	const chatID marotte.ChatID = "c1"
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList: kasRuns(t, map[string]any{
			"workflowId": "wf_1", "status": "paused", "parentSessionId": "sess_owned",
		}),
		methodKiroWorkflowInspect: inspectPaused(t, "wf_1", stalePauseReason),
		methodKiroWorkflowResume:  json.RawMessage(`{}`),
	}
	if _, err := cs.Mutate(t.Context(), chatID, func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.RecordSession("sess_owned")
		return true
	}); err != nil {
		t.Fatalf("seed the chat: %v", err)
	}

	if _, err := h.coord.OpenBridge(t.Context(), chatID, ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}

	stop := time.Now().Add(5 * time.Second)
	for !slices.Contains(br.callLog(), methodKiroWorkflowResume) {
		if time.Now().After(stop) {
			t.Fatalf("a rehydrated session never resumed the run a restart paused; calls were %v",
				br.callLog())
		}
		time.Sleep(time.Millisecond)
	}
}

// TurnFoldTarget reads the chat store only when it has to OPEN a turn, not on every
// folded frame. The open reads the header for the model the turn_open records; a fold
// is a registry lookup, and it runs per streamed delta and per tool frame on the only
// consumer of a 256-slot channel, contending with every persist on the same chat. No
// benchmark sees it: the translate benchmarks' fold target is a fake.
func TestTurnFoldTarget_ReadsTheChatOnlyWhenItOpensATurn(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	const chatID marotte.ChatID = "c1"
	_, _ = cs.Mutate(ctx, chatID, func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })

	// The first frame has no turn to fold into, so it opens one and pays for the facts.
	first := h.coord.TurnFoldTarget(ctx, chatID)
	if first == nil {
		t.Fatal("TurnFoldTarget opened no turn, so there is no fold path to measure")
	}
	before := cs.Gets.Load()

	for range 20 {
		if got := h.coord.TurnFoldTarget(ctx, chatID); got != first {
			t.Fatalf("a folded frame's target = %p, want the open turn %p", got, first)
		}
	}

	if got := cs.Gets.Load(); got != before {
		t.Errorf("chat reads = %d after 20 folded frames, want %d: the fold path reads the "+
			"chat header per frame", got-before, 0)
	}
}

// OwnTurn is the fold for a frame that may join a turn but must never start one: on
// an idle chat it answers nothing, opens nothing, and reads nothing.
func TestOwnTurn_DoesNotOpenATurn(t *testing.T) {
	h, cs, _ := newTestHub()
	before := cs.Gets.Load()

	if log, ok := h.coord.OwnTurn("c1"); ok || log != nil {
		t.Errorf("OwnTurn(no open turn) = (%v, %t), want (nil, false)", log, ok)
	}
	if h.coord.turns.live("c1") {
		t.Error("OwnTurn opened a turn")
	}
	if got := cs.Gets.Load(); got != before {
		t.Errorf("chat reads = %d, want %d", got, before)
	}
}

func TestApplyLoadedSessionFacts_RefreshesTheEntitlementSet(t *testing.T) {
	cases := map[string]struct {
		catalog []marotte.SessionModel
		want    []string
	}{
		"absent keeps the seed": {want: []string{"seed"}},
		"present replaces the seed": {
			catalog: []marotte.SessionModel{{ID: "old", Description: "[Deprecated]"}, {ID: "new"}},
			want:    []string{"old", "new"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			chat := &marotte.Chat{ServedModelIDs: []string{"seed"}}
			applyLoadedSessionFacts(chat, &fakeBridge{catalog: tc.catalog}, "")
			if !slices.Equal(chat.ServedModelIDs, tc.want) {
				t.Errorf("ServedModelIDs = %v, want %v", chat.ServedModelIDs, tc.want)
			}
		})
	}
}
