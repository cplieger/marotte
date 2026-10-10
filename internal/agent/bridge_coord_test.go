package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/kirosession"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/notice"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/marotte/internal/translate"
)

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
	return h, cs, rb
}

// recordingPush records each Send's body on a channel, plus the latest subject and chat
// name, read only after a body arrives.
type recordingPush struct {
	sends chan string
	// noSubs flips HasSubscribers to false; the zero value keeps a subscriber.
	noSubs  atomic.Bool
	subject marotte.PushSubject
	title   string
	// retracted records each Retract call's subject, in order.
	retracted []marotte.PushSubject
	mu        sync.Mutex
}

func (p *recordingPush) HasSubscribers() bool                   { return !p.noSubs.Load() }
func (*recordingPush) SetPreferences(map[marotte.PushKind]bool) {}
func (*recordingPush) Preferences() map[marotte.PushKind]bool   { return nil }
func (*recordingPush) Close()                                   {}
func (p *recordingPush) Retract(subj marotte.PushSubject) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.retracted = append(p.retracted, subj)
}

func (p *recordingPush) Send(_ context.Context, n *marotte.NotificationPayload) {
	p.subject = n.PushSubject
	p.title = n.Title
	select {
	case p.sends <- n.Body:
	default:
	}
}

// On session/new the override model wins over the stored value, and the persisted model comes from the started bridge.
func TestGetOrCreateBridge_AppliesOverrides(t *testing.T) {
	h, cs, rb := newRecordingStartHub(t)
	ctx := t.Context()
	_, _ = cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "m-chat"
		return true // no ACPSessionID -> fresh session/new path
	})

	if _, err := h.coord.openBridge(ctx, "c1", "model-override"); err != nil {
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

// A resume carries the chat's supervised choice on its door: KAS's fork copies no autopilot.
func TestGetOrCreateBridge_CarriesSupervisedOntoTheLoadDoor(t *testing.T) {
	// A fresh bridge per spawn, found by session id: the rehydrate sweep starts the utility bridge on the same factory.
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

	if _, err := h.coord.openBridge(ctx, "c1", ""); err != nil {
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
		// Without a Start naming the stored session there is no load to assert on.
		t.Fatalf("no bridge was started with SessionID %q, so the resume never happened", acpSession)
	}
	if !opts.Supervised {
		t.Error("the resume's StartOpts.Supervised = false although the chat is supervised, " +
			"so KAS runs the loaded session in autopilot and its writes are applied unreviewed")
	}
}

// A successful SetModel returns true and re-applies the effort level, which KAS reconciles against the new model's tiers.
func TestApplyModelSwitch_SucceedsAndReAppliesEffort(t *testing.T) {
	h, cs, br := newTestHub()
	ctx := t.Context()
	_, _ = cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; c.Model = "m-old"; return true })
	if _, err := h.coord.openBridge(ctx, "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}

	if got := h.coord.applyModelSwitch(ctx, "c1", "m-new", "max"); got != true {
		t.Errorf("ApplyModelSwitch(success) = %v, want true", got)
	}
	if got := br.lastEffort(); got != "max" {
		t.Errorf("effort re-applied after the swap = %q, want %q; KAS resets the level inside the model swap", got, "max")
	}
}

// A switch never touches a turn: the session survives, and the reply on every screen stays.
func TestApplyModelSwitch_TouchesNoOpenTurn(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	_, _ = cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; c.Model = "m-old"; return true })
	if _, err := h.coord.openBridge(ctx, "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	id, log := streamingPromptTurn(t, h, "c1", "the user's own reply, still streaming")

	if got := h.coord.applyModelSwitch(ctx, "c1", "m-new", ""); !got {
		t.Fatalf("ApplyModelSwitch = %v, want true", got)
	}

	if after := h.liveTurn("c1"); after != log {
		t.Errorf("open turn after the swap = %p, want the prompt's own %s (%p) still open", after, id, log)
	}
	if closes := closesOf(t, logOf(t, cs, "c1")); len(closes) != 0 {
		t.Errorf("the log holds %d turn_close after the swap, want 0: a switch closes no turn", len(closes))
	}
}

// A chat with no chosen level sends no effort call.
func TestApplyModelSwitch_NoEffortChoiceSendsNoEffortCall(t *testing.T) {
	h, cs, br := newTestHub()
	ctx := t.Context()
	_, _ = cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; c.Model = "m-old"; return true })
	if _, err := h.coord.openBridge(ctx, "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}

	if got := h.coord.applyModelSwitch(ctx, "c1", "m-new", ""); got != true {
		t.Errorf("ApplyModelSwitch(success) = %v, want true", got)
	}
	if got := br.lastEffort(); got != "" {
		t.Errorf("effort applied = %q, want none for a chat that chose no level", got)
	}
}

// A prompt on an already-open bridge re-asserts the level KAS moved on its own.
func TestOpenBridge_RepairsTheEffortOnAnOpenBridge(t *testing.T) {
	h, cs, br := newTestHub()
	ctx := t.Context()
	_, _ = cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.Effort = "max"
		return true
	})
	if _, err := h.coord.openBridge(ctx, "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	// Stand in for KAS moving the level.
	br.mu.Lock()
	br.effort = "high"
	br.mu.Unlock()

	if _, err := h.coord.openBridge(ctx, "c1", ""); err != nil {
		t.Fatalf("OpenBridge (reopen): %v", err)
	}

	if got := br.lastEffort(); got != "max" {
		t.Errorf("effort after a prompt on the open bridge = %q, want %q", got, "max")
	}
}

// No choice and no seed asks for nothing.
func TestOpenBridge_RepairsNothingWithoutAChoice(t *testing.T) {
	h, cs, br := newTestHub()
	ctx := t.Context()
	_, _ = cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	if _, err := h.coord.openBridge(ctx, "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	br.mu.Lock()
	br.effort = ""
	br.mu.Unlock()

	if _, err := h.coord.openBridge(ctx, "c1", ""); err != nil {
		t.Fatalf("OpenBridge (reopen): %v", err)
	}

	if got := br.lastEffort(); got != "" {
		t.Errorf("effort applied = %q, want none for a chat with no choice and no seed", got)
	}
}

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

// effortFor prefers the chat's choice, then the seed for its own model, and refuses a
// malformed level; a well-formed unknown seed flows. No catalog is loaded here.
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

// A pick on one model must not retract another model's remembered level.
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

// With no choice and no seed, effortFor resolves the model's catalog default, not "".
func TestEffortFor_FallsBackToTheModelsCatalogDefault(t *testing.T) {
	h, _, _ := newTestHub()
	h.coord.lifecycle.configDir = t.TempDir()
	h.coord.catalog.SetModels([]marotte.SessionModel{{ID: "m1", DefaultEffortLevel: "high"}})

	got := h.coord.effortFor(t.Context(), &marotte.Chat{ID: "c1", Model: "m1"})

	if got != "high" {
		t.Errorf("effortFor(chat on m1, no choice and no seed) = %q, want high — the model's own default_effort_level is the last rung", got)
	}
}

// A default-off model with thinking never chosen has its session level capped as KAS caps it.
func TestSessionEffort_CapsAHighTierOnADefaultOffModel(t *testing.T) {
	five := []marotte.SessionEffortLevel{{ID: "low"}, {ID: "medium"}, {ID: "high"}, {ID: "xhigh"}, {ID: "max"}}
	tests := []struct {
		name       string
		thinking   string
		defaultOff bool
		want       string
	}{
		{name: "default-off model, no choice", defaultOff: true, want: "high"},
		{name: "default-off model, thinking chosen on", thinking: marotte.ThinkingOn, defaultOff: true, want: "max"},
		{name: "default-on model, no choice", want: "max"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := newTestHub()
			h.coord.lifecycle.configDir = t.TempDir()
			h.coord.catalog.SetModels([]marotte.SessionModel{{ID: "m1", ThinkingToggleable: true, ThinkingDefaultOff: tc.defaultOff}})
			rec := &marotte.Chat{ID: "c1", Model: "m1", Effort: "max", Thinking: tc.thinking, EffortLevels: five}

			if got := h.coord.sessionEffort(t.Context(), rec); got != tc.want {
				t.Errorf("sessionEffort(Effort max, Thinking %q, defaultOff %v) = %q, want %q",
					tc.thinking, tc.defaultOff, got, tc.want)
			}
		})
	}
}

// EffortForSwitch resolves against the target model: its seed, else its workspace catalog default.
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

			got := h.coord.effortForSwitch(t.Context(), test.target)

			if got != test.want {
				t.Errorf("EffortForSwitch(target=%q, last_effort_by_model=%v) = %q, want %q — the chat's own choice must never leak into a switch",
					test.target, test.seed, got, test.want)
			}
		})
	}
}

// The seed is a fallback, never written onto the chat record.
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

// Forward clears the MCP registry only when its bridge is the last one.
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
		if n := len(h.mcpRegistry.snapshot()); n != 0 {
			t.Errorf("registry size = %d, want 0 (no bridges left must clearAll)", n)
		}
	})

	t.Run("keeps_when_a_bridge_remains", func(t *testing.T) {
		h, _, _ := newTestHub()
		seed(h)
		// Stays registered so count() stays >= 1.
		h.bridge.mgr.orInsert("keep")
		other := newFakeBridge()
		other.Stop()
		h.coord.Forward("other", other)
		if n := len(h.mcpRegistry.snapshot()); n != 1 {
			t.Errorf("registry size = %d, want 1 (a remaining bridge must NOT clearAll)", n)
		}
	})
}

// A non-cancelled turn fires the "Agent finished" push.
func TestSettleTurnOnResponse_NonCancelledFiresPush(t *testing.T) {
	cs := newTestChatStore()
	fp := &recordingPush{sends: make(chan string, 4)}
	h := New(t.Context(), "/tmp/push", func() ACPBridge { return newFakeBridge() }, cs, WithPush(fp))
	cs.wire(h)
	ctx := t.Context()

	id, log := h.stagePromptTurn(t, "c1")
	sayText(t, log)
	resp := &marotte.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "end_turn"})}
	h.SettleTurnOnResponse(ctx, "c1", id, 0, resp)

	select {
	case body := <-fp.sends:
		if body != "Response complete" {
			t.Errorf("push body = %q, want %q", body, "Response complete")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no push sent for a non-cancelled turn")
	}
}

// A notification is titled by the tab its click opens: the chat's name from its record, the
// tab strip's default for a chat with none, and the run's label for a run.
func TestNoticeTarget_TitlesTheTabTheClickOpens(t *testing.T) {
	cs := newTestChatStore()
	fp := &recordingPush{sends: make(chan string, 4)}
	h := New(t.Context(), t.TempDir(), func() ACPBridge { return newFakeBridge() }, cs, WithPush(fp))
	cs.wire(h)
	cs.seed(t, "c1", func(c *marotte.Chat) { c.Name = "Fix the parser" })

	for _, tc := range []struct {
		chatID    marotte.ChatID
		runID     string
		want      string
		wantSubjs marotte.PushSubject
	}{
		{"c1", "", "Fix the parser", marotte.ChatSubject("c1")},
		{"gone", "", marotte.DefaultChatName, marotte.ChatSubject("gone")},
		{"c1", "wf1", "Workflow run", marotte.RunSubject("wf1")},
	} {
		target := h.coord.NoticeTarget(t.Context(), tc.chatID, tc.runID)
		n := notice.TurnFinished(target, "")
		h.coord.Notify(t.Context(), tc.chatID, &n)
		select {
		case <-fp.sends:
		case <-time.After(2 * time.Second):
			t.Fatalf("no push sent for %q/%q", tc.chatID, tc.runID)
		}
		if fp.title != tc.want || fp.subject != tc.wantSubjs {
			t.Errorf("NoticeTarget(%q, %q) pushed title %q under %+v, want %q under %+v",
				tc.chatID, tc.runID, fp.title, fp.subject, tc.want, tc.wantSubjs)
		}
	}
}

// A settled turn whose seal and close land logs no error.
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

// PersistModelSwitch logs nothing when the entry append and header write both succeed.
func TestPersistModelSwitch_NoErrorLogOnSuccess(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	finishedTurn(t, h, "c1")
	_, _ = cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; c.Model = "m-old"; return true })

	logs := captureLogs(t)
	h.coord.persistModelSwitch(ctx, "c1", marotte.EntryModelSwitched{From: "m-old", To: "m-new"}, 1234)
	if got := logs.String(); strings.Contains(got, "switch_model:") {
		t.Errorf("unexpected switch_model error log on success: %s", got)
	}
	if switches := switchesOf(t, logOf(t, cs, "c1")); len(switches) != 1 {
		t.Errorf("model_switched entries = %+v, want the one this switch appended", switches)
	}
}

// A landed switch is one model_switched entry plus a header taking the pick: pending_model
// cleared, old tier dropped, usage reset to the new context size.
func TestPersistModelSwitch_RecordsTheSwitchAndClearsThePendingPick(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	finishedTurn(t, h, "c1")
	_, _ = cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "m-old"
		c.PendingModel = "m-new"
		c.Effort = "max"
		c.Usage = marotte.Usage{ContextSize: 100, Credits: 7}
		return true
	})

	h.coord.persistModelSwitch(ctx, "c1",
		marotte.EntryModelSwitched{From: "m-old", To: "m-new", Effort: "high"}, 1234)

	entries := logOf(t, cs, "c1")
	switches := switchesOf(t, entries)
	// The tier travels on the entry, the only record of it once the header clears its copy.
	if len(switches) != 1 || switches[0] != (marotte.EntryModelSwitched{From: "m-old", To: "m-new", Effort: "high"}) {
		t.Errorf("model_switched entries = %+v, want one {From: m-old, To: m-new, Effort: high}", switches)
	}
	if closes := closesOf(t, entries); len(closes) != 1 {
		t.Errorf("the log holds %d turn_close, want the finished turn's 1: the switch opened and closed no turn", len(closes))
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

// Picked between turns, the effort change is appended alone; From == To marks it as not a model switch.
func TestPersistEffortChange_AppendsTheTierBetweenTurns(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	_, _ = cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; c.Model = "opus-5"; return true })
	finishedTurn(t, h, "c1")

	h.coord.PersistEffortChange(ctx, "c1", "opus-5", marotte.EffortMax)

	switches := switchesOf(t, logOf(t, cs, "c1"))
	want := marotte.EntryModelSwitched{From: "opus-5", To: "opus-5", Effort: "max"}
	if len(switches) != 1 || switches[0] != want {
		t.Errorf("model_switched entries = %+v, want one %+v", switches, want)
	}
}

// When a started chat's log holds no turn to append after, the event turn minted to carry the change is closed on
// disk and on the wire: a client reads a turn with no turn_close as running, so the dot would pulse.
func TestPersistEffortChange_OnAnEmptyLogAnnouncesAClosedCarrier(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	_, _ = cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "opus-5"
		c.TurnCount = 1
		return true
	})

	h.coord.PersistEffortChange(ctx, "c1", "opus-5", marotte.EffortMedium)

	var kinds []string
	for _, e := range logOf(t, cs, "c1") {
		kinds = append(kinds, string(e.Kind))
	}
	if want := "turn_open,turn_close,model_switched"; strings.Join(kinds, ",") != want {
		t.Errorf("log kinds = %v, want %s", kinds, want)
	}
	var wire []string
	for _, typ := range extractTypes(t, bufferedSince(h, 0)) {
		switch marotte.EventType(typ) {
		case marotte.EventTurnOpened, marotte.EventTurnClosed, marotte.EventEntryAppended:
			wire = append(wire, typ)
		}
	}
	if want := "turn_opened,turn_closed,entry_appended"; strings.Join(wire, ",") != want {
		t.Errorf("announced %v, want %s", wire, want)
	}
}

// Picked during a turn, it folds into that turn.
func TestPersistEffortChange_FoldsTheTierIntoAnOpenTurn(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	_, _ = cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; c.Model = "opus-5"; return true })
	turnID, _ := streamingPromptTurn(t, h, "c1", "the reply")

	h.coord.PersistEffortChange(ctx, "c1", "opus-5", "high")

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

// A chat with no model writes nothing: an empty To renders as `Context reset`.
func TestPersistEffortChange_AChatWithNoModelWritesNothing(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	_, _ = cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })

	h.coord.PersistEffortChange(ctx, "c1", "", "high")

	if switches := switchesOf(t, logOf(t, cs, "c1")); len(switches) != 0 {
		t.Errorf("model_switched entries = %+v, want none for a modelless chat", switches)
	}
}

func finishedTurn(t *testing.T, h *Runtime, chatID marotte.ChatID) {
	t.Helper()
	id, _ := h.stagePromptTurn(t, chatID)
	endTurn(t, h, chatID, id)
	if c, ok := h.chatStore.Get(t.Context(), chatID); !ok || c.TurnCount != 1 {
		t.Fatalf("after one finished turn the header reads %+v, want turn_count 1", c)
	}
}

func TestPersistSwitches_RecordNothingBeforeTheFirstPrompt(t *testing.T) {
	tests := map[string]func(h *Runtime){
		"effort": func(h *Runtime) {
			h.coord.PersistEffortChange(t.Context(), "c1", "opus-5", "high")
		},
		"mode": func(h *Runtime) {
			h.coord.PersistModeSwitch(t.Context(), "c1",
				marotte.EntryModeSwitched{From: "", To: "spec", Source: marotte.ModeSwitchSourceUser})
		},
		"model": func(h *Runtime) {
			h.coord.persistModelSwitch(t.Context(), "c1", marotte.EntryModelSwitched{From: "opus-5", To: "sonnet-5"}, 1234)
		},
	}
	for name, persist := range tests {
		t.Run(name, func(t *testing.T) {
			h, cs, _ := newTestHub()
			_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; c.Model = "opus-5"; return true })

			persist(h)

			if got := logOf(t, cs, "c1"); len(got) != 0 {
				t.Errorf("log after a %s switch on an unstarted chat = %+v, want no entry", name, got)
			}
			if c, _ := cs.Get(t.Context(), "c1"); c.TurnCount != 0 {
				t.Errorf("turn_count = %d after a %s switch on an unstarted chat, want 0", c.TurnCount, name)
			}
		})
	}
}

func TestPersistModelSwitch_UnstartedChatTakesThePickOnTheHeader(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "opus-5"
		c.PendingModel = "sonnet-5"
		return true
	})

	h.coord.persistModelSwitch(t.Context(), "c1", marotte.EntryModelSwitched{From: "opus-5", To: "sonnet-5"}, 1234)

	c, _ := cs.Get(t.Context(), "c1")
	if c.Model != "sonnet-5" || c.PendingModel != "" || c.Usage.ContextSize != 1234 {
		t.Errorf("header = {Model %q, PendingModel %q, ContextSize %d}, want {sonnet-5, \"\", 1234}",
			c.Model, c.PendingModel, c.Usage.ContextSize)
	}
}

// TestAdoptKASTitle pins all four arms: adopting "New Session" or overwriting an existing name both compile cleanly.
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

// A stored KAS title gets the same door treatment as the live focus channel, one case per part:
// it was never sanitized, and a resume names a default-named chat.
func TestAdoptKASTitle_AppliesTheWholeDoorTreatment(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  string
	}{
		{
			// Verbatim from a real poisoned chat record.
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
			// Pins the outcome: an unbounded title never reaches Chat.Name. Two rules refuse it
			// independently; the log test pins the bound.
			name:  "an_unbounded_stored_title",
			title: strings.Repeat("x", 700),
			want:  marotte.DefaultChatName,
		},
		{
			// The sanitizer runs before the rules: the stored form is what gets compared and kept.
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

// The refusal line carries the SANITIZED title plus the rule that fired; logging the raw
// argument puts unbounded control text into the log.
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

// testReaperWorkDir is both the runtime's workDir and the root every fixture session claims:
// the reaper reaps only for its own workspace.
const testReaperWorkDir = "/tmp/work"

// Without one the reaper retains (doubt).
func writeSessionRecord(t *testing.T, sessionDir, workspaceRoot string) {
	t.Helper()
	body := `{"workspacePaths":["` + workspaceRoot + `"]}`
	if err := os.WriteFile(filepath.Join(sessionDir, "session.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write session record in %s: %v", sessionDir, err)
	}
}

// TestSweepSessionsOnce_KeepListCompleteness pins doubt-retains against a real orphan: a
// partial keep-list must not sweep. The control's keep-list names an existing session,
// since the reaper refuses an empty one outright.
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
			// Old enough to clear the create-race guard, plus a referenced sibling so the keep-list is non-empty.
			orphan := filepath.Join(sessionsDir, "hash01", "sess_orphan")
			kept := filepath.Join(sessionsDir, "hash01", "sess_ref")
			for _, p := range []string{orphan, kept} {
				if err := os.MkdirAll(p, 0o700); err != nil {
					t.Fatalf("mkdir %s: %v", p, err)
				}
				// Without this record the reaper retains whatever the keep-list says.
				writeSessionRecord(t, p, testReaperWorkDir)
				if err := os.Chtimes(p, old, old); err != nil {
					t.Fatalf("chtimes %s: %v", p, err)
				}
			}

			// Wired at construction: New starts sweepSessionsLoop, so later assignment races (-race).
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

// TestLiveSessionIDs_CoversEveryBridge pins that every live bridge's session is exempt; the
// 10-minute guard is a create-race cushion, not a liveness test.
func TestLiveSessionIDs_CoversEveryBridge(t *testing.T) {
	// A per-spawn factory: newTestHub's shares one fake, and this needs distinct session ids.
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
	// A bridge with no session yet contributes nothing.
	setSession("chatC", "")

	got := h.liveSessionIDs()
	slices.Sort(got)
	want := []string{"sess_chatA", "sess_chatB"}
	if !slices.Equal(got, want) {
		t.Errorf("liveSessionIDs() = %v, want %v", got, want)
	}
}

// TestApplyLoadedSessionFacts_KeepsWhatTheResultOmitted pins that a fact the load omitted is
// not written: a fresh bridge answers zero values. catalog owns the catalogs' rule.
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

			_, _ = applyLoadedSessionFacts(c, br, "")

			if c.CurrentModeID != tc.wantMode {
				t.Errorf("CurrentModeID = %q, want %q", c.CurrentModeID, tc.wantMode)
			}
		})
	}
}

// TestApplyLoadedSessionFacts_KeepsSummarizationThreshold pins the same keep-on-absent rule for the threshold.
func TestApplyLoadedSessionFacts_KeepsSummarizationThreshold(t *testing.T) {
	cases := map[string]struct {
		summarization, want float64
	}{
		"a silent result keeps the stored value": {want: 80},
		"what the result carries is written":     {summarization: 85, want: 85},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := &marotte.Chat{Name: "A"}
			c.Usage.SummarizationThresholdPct = 80
			br := &fakeBridge{summarizationPct: tc.summarization}

			_, _ = applyLoadedSessionFacts(c, br, "")

			if c.Usage.SummarizationThresholdPct != tc.want {
				t.Errorf("SummarizationThresholdPct = %v, want %v",
					c.Usage.SummarizationThresholdPct, tc.want)
			}
		})
	}
}

// TestPersistNewSessionMetadata_ReportsAModeThatWasNotApplied pins the report: the record
// takes the actual mode, so an unreported refusal silently converts a pinned chat.
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

// TestSpawnBridge_ReportsSupervisedThatWasNotApplied pins that a refused `autopilot: off`
// reaches the user. The third case is why the report cannot key on the bridge flag alone.
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
			_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
				c.Name = "A"
				c.SupervisedMode = tc.supervised
				return true
			})

			since := h.bus.fanout.Position().Head
			if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
				t.Fatalf("OpenBridge: %v", err)
			}

			// The chat keeps the request: supervised is the safer intent, so the next spawn re-asserts.
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

// TestChatTeardown_CloseKeepsSessionDeleteReapsIt pins that close keeps the KAS session and
// delete reaps it; the delete arm proves the reaper is wired.
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
			name: "delete reaps it (control)",
			teardown: func(h *Runtime, ctx context.Context, id marotte.ChatID) {
				h.DeleteChatStateByChain(ctx, id, []string{"sess_owned"}, command.RunStopChatDeleted)
			},
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

// TestChatTeardown_DeleteByChainReapsWithoutTheRecord pins a reap driven from the chain
// captured before the record went.
func TestChatTeardown_DeleteByChainReapsWithoutTheRecord(t *testing.T) {
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

	// No chat record: every delete removes it before the teardown.
	h.DeleteChatStateByChain(t.Context(), "c-doomed", []string{"sess_owned"}, command.RunStopTabClosed)

	if _, err := os.Stat(sessDir); err == nil {
		t.Errorf("session %s survived a by-chain delete with the record gone, want it reaped", sessDir)
	}
}

// TestSessionLoad_HealsTheChatsRestartPausedRuns pins that a resumed chat heals the runs KAS
// paused when its bridge died. The sweep is off the spawn path, so it is awaited.
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

	if _, err := h.coord.openBridge(t.Context(), chatID, ""); err != nil {
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

// TurnFoldTarget reads the chat store only to open a turn: a fold runs per delta on a
// 256-slot channel's only consumer. No benchmark sees it.
func TestTurnFoldTarget_ReadsTheChatOnlyWhenItOpensATurn(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	const chatID marotte.ChatID = "c1"
	_, _ = cs.Mutate(ctx, chatID, func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })

	// The first frame opens a turn and pays for the facts.
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

// OwnTurn on an idle chat answers, opens and reads nothing.
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
			_, _ = applyLoadedSessionFacts(chat, &fakeBridge{catalog: tc.catalog}, "")
			if !slices.Equal(chat.ServedModelIDs, tc.want) {
				t.Errorf("ServedModelIDs = %v, want %v", chat.ServedModelIDs, tc.want)
			}
		})
	}
}

// A saved model is judged at load: absent from the catalogue it is neither sent nor kept; an empty catalogue decides nothing.
func TestLoad_ASavedModelIsJudgedAgainstTheServedCatalogue(t *testing.T) {
	cases := map[string]struct {
		served    []string
		catalog   []marotte.SessionModel
		wantSent  string
		wantModel string
		wantEff   string
	}{
		"absent from a non-empty catalogue: cleared and not sent": {
			served: []string{"m-other"}, catalog: []marotte.SessionModel{{ID: "m-other"}},
		},
		"present in the catalogue: sent and kept": {
			served: []string{"m-saved"}, catalog: []marotte.SessionModel{{ID: "m-saved"}},
			wantSent: "m-saved", wantModel: "m-saved", wantEff: "max",
		},
		"empty catalogue: kept and not cleared": {
			wantSent: "m-saved", wantModel: "m-saved", wantEff: "max",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h, _, _ := newTestHub()
			rec := &marotte.Chat{Name: "A", Model: "m-saved", Effort: "max", ServedModelIDs: tc.served}

			sent, _, _, _ := h.coord.sessionChoices(t.Context(), "c1", rec, "")
			_, _ = applyLoadedSessionFacts(rec, &fakeBridge{catalog: tc.catalog}, "")

			if sent != tc.wantSent {
				t.Errorf("sessionChoices model = %q, want %q", sent, tc.wantSent)
			}
			if rec.Model != tc.wantModel || rec.Effort != tc.wantEff {
				t.Errorf("after load: model = %q, effort = %q; want %q, %q",
					rec.Model, rec.Effort, tc.wantModel, tc.wantEff)
			}
		})
	}
}

func TestOpenBridge_RecordsAWithheldModelAsAnUnavailableSwitch(t *testing.T) {
	h, cs, rb := newRecordingStartHub(t)
	ctx := t.Context()
	_, _ = cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name, c.Model, c.Effort, c.ServedModelIDs = "A", "m-gone", "max", []string{"m-other"}
		c.TurnCount = 1
		return true
	})

	if _, err := h.coord.openBridge(ctx, "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}

	if got := rb.startOpts().Model; got != "" {
		t.Errorf("StartOpts.Model = %q, want the unserved model withheld", got)
	}
	want := []marotte.EntryModelSwitched{{From: "m-gone", To: "fake-model", Reason: marotte.ModelSwitchReasonUnavailable}}
	if got := switchesOf(t, logOf(t, cs, "c1")); !slices.Equal(got, want) {
		t.Errorf("model_switched entries = %+v, want %+v", got, want)
	}
	if c, _ := cs.Get(ctx, "c1"); c.Model != "fake-model" || c.Effort != "" {
		t.Errorf("header = {Model %q, Effort %q}, want {fake-model, \"\"}", c.Model, c.Effort)
	}
}

func TestOpenBridge_RecordsAModelTheLoadDropped(t *testing.T) {
	h, cs, rb := newRecordingStartHub(t)
	rb.catalog = []marotte.SessionModel{{ID: "fake-model"}}
	ctx := t.Context()
	_, _ = cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name, c.Model = "A", "m-gone"
		c.RecordSession("sess_saved")
		c.TurnCount = 1
		return true
	})

	if _, err := h.coord.openBridge(ctx, "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}

	want := []marotte.EntryModelSwitched{{From: "m-gone", To: "fake-model", Reason: marotte.ModelSwitchReasonUnavailable}}
	if got := switchesOf(t, logOf(t, cs, "c1")); !slices.Equal(got, want) {
		t.Errorf("model_switched entries = %+v, want %+v", got, want)
	}
	if c, _ := cs.Get(ctx, "c1"); c.Model != "fake-model" {
		t.Errorf("header Model = %q, want the running model", c.Model)
	}
}

func TestOpenBridge_ServedOrFailedSpawnRecordsNoUnavailableSwitch(t *testing.T) {
	t.Run("served", func(t *testing.T) {
		h, cs, _ := newRecordingStartHub(t)
		ctx := t.Context()
		_, _ = cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool {
			c.Name, c.Model, c.ServedModelIDs = "A", "fake-model", []string{"fake-model"}
			return true
		})
		if _, err := h.coord.openBridge(ctx, "c1", ""); err != nil {
			t.Fatalf("OpenBridge: %v", err)
		}
		if got := switchesOf(t, logOf(t, cs, "c1")); len(got) != 0 {
			t.Errorf("model_switched entries = %+v, want none", got)
		}
	})
	t.Run("failed_spawn", func(t *testing.T) {
		h, cs, rb := newRecordingStartHub(t)
		rb.startErr = errors.New("spawn refused")
		ctx := t.Context()
		_, _ = cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool {
			c.Name, c.Model, c.Effort, c.ServedModelIDs = "A", "m-gone", "max", []string{"m-other"}
			return true
		})
		if _, err := h.coord.openBridge(ctx, "c1", ""); err == nil {
			t.Fatal("OpenBridge = nil, want the spawn's error")
		}
		if got := switchesOf(t, logOf(t, cs, "c1")); len(got) != 0 {
			t.Errorf("model_switched entries = %+v, want none", got)
		}
		if c, _ := cs.Get(ctx, "c1"); c.Model != "m-gone" || c.Effort != "max" {
			t.Errorf("header = {Model %q, Effort %q}, want {m-gone, max} back for the next spawn to withhold and record",
				c.Model, c.Effort)
		}
	})
}

func TestOpenBridge_AnUnservedModelTheSessionRunsKeepsItsSelection(t *testing.T) {
	cases := map[string]func(c *marotte.Chat){
		"withheld at the door": func(c *marotte.Chat) { c.ServedModelIDs = []string{"m-other"} },
		"dropped by the load":  func(c *marotte.Chat) { c.RecordSession("sess_saved") },
	}
	for name, seed := range cases {
		t.Run(name, func(t *testing.T) {
			h, cs, rb := newRecordingStartHub(t)
			rb.catalog = []marotte.SessionModel{{ID: "m-other"}}
			rb.modelID = "m-gone"
			ctx := t.Context()
			_, _ = cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool {
				c.Name, c.Model, c.Effort = "A", "m-gone", "max"
				seed(c)
				return true
			})

			if _, err := h.coord.openBridge(ctx, "c1", ""); err != nil {
				t.Fatalf("OpenBridge: %v", err)
			}

			if got := switchesOf(t, logOf(t, cs, "c1")); len(got) != 0 {
				t.Errorf("model_switched entries = %+v, want none", got)
			}
			if c, _ := cs.Get(ctx, "c1"); c.Model != "m-gone" || c.Effort != "max" {
				t.Errorf("header = {Model %q, Effort %q}, want {m-gone, max}", c.Model, c.Effort)
			}
		})
	}
}

func TestReconcileUserName(t *testing.T) {
	long := strings.Repeat("n", 100)
	cases := []struct {
		name       string
		chat       marotte.Chat
		stored     string
		setByUser  bool
		wantName   string
		wantUser   bool
		wantRename string
	}{
		{"unlatched successor gets the user's name", marotte.Chat{Name: "Mine", NameSetByUser: true}, "New Session", false, "Mine", true, "Mine"},
		{"already latched sends nothing", marotte.Chat{Name: "Mine", NameSetByUser: true}, "Mine", true, "Mine", true, ""},
		{"a long name compares against KAS's capped copy", marotte.Chat{Name: long, NameSetByUser: true}, marotte.KASStoredTitle(long), true, long, true, ""},
		{"a different user title elsewhere loses to the record", marotte.Chat{Name: "Mine", NameSetByUser: true}, "From TUI", true, "Mine", true, "Mine"},
		{"a TUI rename is adopted without the shape rules", marotte.Chat{Name: marotte.DefaultChatName}, "Fix Dr. Smith import", true, "Fix Dr. Smith import", true, ""},
		{"an unlatched title of the same shape is still refused", marotte.Chat{Name: marotte.DefaultChatName}, "Fix Dr. Smith import", false, marotte.DefaultChatName, false, ""},
		{"the placeholder is never adopted", marotte.Chat{Name: marotte.DefaultChatName}, "New Session", true, marotte.DefaultChatName, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.chat
			got := reconcileUserName(&c, tc.stored, tc.setByUser)
			if got != tc.wantRename || c.Name != tc.wantName || c.NameSetByUser != tc.wantUser {
				t.Errorf("reconcileUserName(%q, %v) = %q, chat {%q, %v}; want %q, {%q, %v}",
					tc.stored, tc.setByUser, got, c.Name, c.NameSetByUser, tc.wantRename, tc.wantName, tc.wantUser)
			}
		})
	}
}

// A successor session of a user-named chat starts unlatched, so the door renames it; an agent-named chat's is left alone.
func TestPersistNewSessionMetadata_RenamesASuccessorOfAUserNamedChat(t *testing.T) {
	for _, userNamed := range []bool{true, false} {
		h, cs, br := newTestHub()
		br.mu.Lock()
		br.sessionTitle = "New Session"
		br.mu.Unlock()
		_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
			c.Name = "Mine"
			c.NameSetByUser = userNamed
			return true
		})

		h.coord.persistNewSessionMetadata(t.Context(), "c1", br)

		br.mu.Lock()
		params, renamed := br.lastParams[marotte.MethodSessionRename]
		br.mu.Unlock()
		if renamed != userNamed {
			t.Errorf("user-named=%v: rename sent = %v, want %v", userNamed, renamed, userNamed)
		}
		if userNamed && params["title"] != "Mine" {
			t.Errorf("rename title = %v, want Mine", params["title"])
		}
		if c, _ := cs.Get(t.Context(), "c1"); c.Name != "Mine" {
			t.Errorf("user-named=%v: chat name = %q, want Mine", userNamed, c.Name)
		}
	}
}
