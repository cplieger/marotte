package agent

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// An empty sessionID is the chat's own frame; a sessionID plus workflow marker is a step's.
func configOptionFrame(t *testing.T, running, sessionID string, workflow bool) *marotte.RPCResponse {
	t.Helper()
	update := map[string]any{
		"sessionUpdate": string(marotte.ACPUpdateConfigOption),
		"configOptions": []any{
			map[string]any{
				"id":           "effortLevel",
				"currentValue": running,
				"options": []any{
					map[string]any{"value": "high", "name": "high"},
					map[string]any{"value": "max", "name": "max"},
				},
			},
		},
	}
	if workflow {
		update["_meta"] = map[string]any{"kiro": map[string]any{
			"workflow": map[string]any{"workflowId": "wf1", "nodeId": "n1", "type": "step"},
		}}
	}
	params := mustJSON(t, map[string]any{
		"sessionId": sessionID,
		"update":    mustJSON(t, update),
	})
	return &marotte.RPCResponse{Method: marotte.MethodSessionUpdate, Params: params}
}

func healEffortFixture(t *testing.T, chose string) (*Runtime, *fakeBridge) {
	t.Helper()
	h, cs, br := newTestHub()
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.Effort = chose
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})
	return h, br
}

// settleHeals joins the repair goroutine through the runtime's shutdown, so "nothing was
// applied" is a fact rather than a race the test won.
func settleHeals(t *testing.T, h *Runtime) {
	t.Helper()
	shutdownHub(t, h)
}

// TestHealEffort_RepairsALevelTheSessionMovedOnItsOwn covers the spawning turn, which repairEffort cannot reach.
func TestHealEffort_RepairsALevelTheSessionMovedOnItsOwn(t *testing.T) {
	h, br := healEffortFixture(t, "max")

	h.handleSessionUpdate(t.Context(), "c1", configOptionFrame(t, "high", "", false))
	settleHeals(t, h)

	if got := br.lastEffort(); got != "max" {
		t.Errorf("effort applied = %q, want %q", got, "max")
	}
	if got := br.lastObservedEffort(); got != "high" {
		t.Errorf("observed level = %q, want %q — the bridge must be told what the session reports, or its differs-only cache skips the repair", got, "high")
	}
}

// TestHealEffort_RepairsOncePerBridge pins the latch; it is claimed synchronously, so spending it in the fixture proves the second frame did nothing.
func TestHealEffort_RepairsOncePerBridge(t *testing.T) {
	h, br := healEffortFixture(t, "max")
	sb := h.coord.bridgeFor("c1")
	if !sb.claimEffortHeal() {
		t.Fatal("fixture: the latch was already spent")
	}

	h.handleSessionUpdate(t.Context(), "c1", configOptionFrame(t, "high", "", false))
	settleHeals(t, h)

	if got := br.lastEffort(); got != "" {
		t.Errorf("applied %q on a spent latch; the repair must run once per bridge", got)
	}
	if got := br.lastObservedEffort(); got != "high" {
		t.Errorf("observed level = %q, want %q — the OBSERVATION is not latched, only the repair", got, "high")
	}
}

// TestHealEffort_SendsNothingWhenThereIsNothingToRepair covers the three distinct "nothing" cases.
func TestHealEffort_SendsNothingWhenThereIsNothingToRepair(t *testing.T) {
	tests := map[string]struct {
		chose   string
		running string
	}{
		"the session already runs at the chosen level": {chose: "max", running: "max"},
		"the chat chose nothing and has no seed":       {chose: "", running: "high"},
		"the session reports no level at all":          {chose: "max", running: ""},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			h, br := healEffortFixture(t, test.chose)

			h.handleSessionUpdate(t.Context(), "c1", configOptionFrame(t, test.running, "", false))
			settleHeals(t, h)

			if got := br.lastEffort(); got != "" {
				t.Errorf("applied %q, want no call", got)
			}
		})
	}
}

// TestHealEffort_IgnoresAWorkflowStepsFrame pins that a step's level never repairs the chat's.
func TestHealEffort_IgnoresAWorkflowStepsFrame(t *testing.T) {
	h, br := healEffortFixture(t, "max")

	h.handleSessionUpdate(t.Context(), "c1", configOptionFrame(t, "high", "sess-step", true))
	settleHeals(t, h)

	if got := br.lastEffort(); got != "" {
		t.Errorf("applied %q from a step's frame, want no call", got)
	}
	if got := br.lastObservedEffort(); got != "" {
		t.Errorf("observed %q from a step's frame; a step's level is not this session's", got)
	}
}

// modelEffortFrame is the chat's own config_option_update naming the running model and level.
func modelEffortFrame(t *testing.T, model, running string) *marotte.RPCResponse {
	t.Helper()
	update := map[string]any{
		"sessionUpdate": string(marotte.ACPUpdateConfigOption),
		"configOptions": []any{
			map[string]any{
				"id":           marotte.ConfigOptionModel,
				"currentValue": model,
				"options":      []any{map[string]any{"value": model, "name": model}},
			},
			map[string]any{
				"id":           marotte.ConfigOptionEffort,
				"currentValue": running,
				"options": []any{
					map[string]any{"value": "low", "name": "low"},
					map[string]any{"value": "medium", "name": "medium"},
					map[string]any{"value": "high", "name": "high"},
				},
			},
		},
	}
	params := mustJSON(t, map[string]any{"sessionId": "", "update": mustJSON(t, update)})
	return &marotte.RPCResponse{Method: marotte.MethodSessionUpdate, Params: params}
}

// effortDrift is a chat on fake-model opened while another chat's pick moves the seed under it:
// the seed is high at the chat's first session and medium afterwards.
type effortDrift struct {
	cs        *testChatStore
	dir       string
	mu        sync.Mutex
	failLoads bool
}

func newEffortDrift(t *testing.T) *effortDrift {
	t.Helper()
	d := &effortDrift{cs: newTestChatStore(), dir: t.TempDir()}
	writeEffortSeed(t, d.dir, map[string]string{"fake-model": "high"})
	if _, err := d.cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name, c.Model = "A", "fake-model"
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	return d
}

// boot starts a runtime over the drift's store, as marotte's start does; the caller shuts it down.
func (d *effortDrift) boot(t *testing.T) *Runtime {
	t.Helper()
	return d.bootOver(t, d.cs)
}

// bootOver is boot with store, which reads and writes the drift's own, in front of it.
func (d *effortDrift) bootOver(t *testing.T, store chatRecords) *Runtime {
	t.Helper()
	h := New(t.Context(), t.TempDir(), func() ACPBridge {
		b := newFakeBridge()
		d.mu.Lock()
		if d.failLoads {
			b.loadErr = errors.New("session/load refused")
		}
		d.mu.Unlock()
		return b
	}, store)
	h.runs.cancelRetryBase = time.Hour
	d.cs.wire(h)
	h.coord.lifecycle.configDir = d.dir
	return h
}

// firstSessionThenDrift opens the chat's first session at the seed, then moves the seed as another
// chat's pick does (recordEffortSeed).
func (d *effortDrift) firstSessionThenDrift(t *testing.T, h *Runtime) *sharedBridge {
	t.Helper()
	sb, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if opts := fakeOf(sb).lastStartOpts(); opts == nil || opts.SessionID != "" || opts.Effort != "high" {
		t.Fatalf("first session/new = %+v, want a new session at the seed, high", opts)
	}
	writeEffortSeed(t, d.dir, map[string]string{"fake-model": "medium"})
	return sb
}

// crash ends the chat's process on its own, the forward loop's ended_itself arm, and waits for
// the registry to drop it.
func crash(t *testing.T, h *Runtime) {
	t.Helper()
	fakeOf(h.coord.bridgeFor("c1")).Stop()
	deadline := time.Now().Add(5 * time.Second)
	for h.coord.bridgeFor("c1") != nil {
		if time.Now().After(deadline) {
			t.Fatal("the crashed bridge was still registered after 5s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func reopenStartOpts(t *testing.T, h *Runtime) *marotte.StartOpts {
	t.Helper()
	sb, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	opts := fakeOf(sb).lastStartOpts()
	if opts == nil {
		t.Fatal("the reopen started no session")
	}
	return opts
}

// A prompt on the open bridge re-asserts the level the chat runs at, not the seed another chat moved.
func TestEffortDrift_ThePromptRepairKeepsTheChatsLevel(t *testing.T) {
	d := newEffortDrift(t)
	h := d.boot(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	sb := d.firstSessionThenDrift(t, h)

	if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("reopen: %v", err)
	}

	if got := fakeOf(sb).lastEffort(); got != "high" {
		t.Errorf("repairEffort asserted %q on the open session, want high: another chat's pick moved this chat", got)
	}
}

// The respawn after a crash loads the session at the level it ran before the crash.
func TestEffortDrift_ACrashRespawnLoadsTheChatsLevel(t *testing.T) {
	d := newEffortDrift(t)
	h := d.boot(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	d.firstSessionThenDrift(t, h)
	crash(t, h)

	opts := reopenStartOpts(t, h)

	if opts.SessionID == "" {
		t.Fatalf("the respawn opened a new session, want a session/load: %+v", opts)
	}
	if opts.Effort != "high" {
		t.Errorf("session/load asserted effort %q, want high (the level the chat ran before the crash)", opts.Effort)
	}
}

// A marotte restart reads the record from disk; the first load of the new process keeps the level.
func TestEffortDrift_ARestartLoadsTheChatsLevel(t *testing.T) {
	d := newEffortDrift(t)
	before := d.boot(t)
	d.firstSessionThenDrift(t, before)
	shutdownHub(t, before)
	h := d.boot(t)
	t.Cleanup(func() { shutdownHub(t, h) })

	opts := reopenStartOpts(t, h)

	if opts.SessionID == "" || opts.Effort != "high" {
		t.Errorf("the first open after a restart = {SessionID %q, Effort %q}, want a session/load at high",
			opts.SessionID, opts.Effort)
	}
}

// The empty-turn retry detaches the session (refreshRetrySession); the new session keeps the level.
func TestEffortDrift_ARefreshedSessionKeepsTheChatsLevel(t *testing.T) {
	d := newEffortDrift(t)
	h := d.boot(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	d.firstSessionThenDrift(t, h)
	bridgeRole{coord: h.coord}.CloseBridge(t.Context(), "c1", marotte.TurnOutcomeInterrupted)
	if _, err := d.cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.RecordSession("")
		return true
	}); err != nil {
		t.Fatalf("detach the session: %v", err)
	}

	opts := reopenStartOpts(t, h)

	if opts.SessionID != "" || opts.Effort != "high" {
		t.Errorf("the refreshed session = {SessionID %q, Effort %q}, want a session/new at high",
			opts.SessionID, opts.Effort)
	}
}

// A load KAS refuses falls back to a new session, which keeps the level too.
func TestEffortDrift_ANewSessionAfterAFailedLoadKeepsTheChatsLevel(t *testing.T) {
	d := newEffortDrift(t)
	h := d.boot(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	d.firstSessionThenDrift(t, h)
	crash(t, h)
	d.mu.Lock()
	d.failLoads = true
	d.mu.Unlock()

	opts := reopenStartOpts(t, h)

	if opts.SessionID != "" || opts.Effort != "high" {
		t.Errorf("the fallback session = {SessionID %q, Effort %q}, want a session/new at high",
			opts.SessionID, opts.Effort)
	}
}

// recordRanWithoutLevel binds c1 to a session that reported running at high while its record holds
// no level, and moves the seed to medium.
func (d *effortDrift) recordRanWithoutLevel(t *testing.T, sessionID string) {
	t.Helper()
	writeEffortSeed(t, d.dir, map[string]string{"fake-model": "medium"})
	if _, err := d.cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Effort, c.EffortActive = "", "high"
		c.RecordSession(sessionID)
		return true
	}); err != nil {
		t.Fatalf("record the session: %v", err)
	}
}

// A record holding no level whose load KAS refuses opens the new session at the level the chat ran.
func TestEffortDrift_ANewSessionAfterAFailedLoadKeepsTheLevelTheChatRan(t *testing.T) {
	d := newEffortDrift(t)
	d.recordRanWithoutLevel(t, "sess-old")
	d.failLoads = true
	h := d.boot(t)
	t.Cleanup(func() { shutdownHub(t, h) })

	opts := reopenStartOpts(t, h)

	if opts.SessionID != "" || opts.Effort != "high" {
		t.Errorf("the fallback session = {SessionID %q, Effort %q}, want a session/new at high",
			opts.SessionID, opts.Effort)
	}
	if c, _ := d.cs.Get(t.Context(), "c1"); c.Effort != "high" {
		t.Errorf("record level after the fallback = %q, want high pinned", c.Effort)
	}
}

// The load of a record holding no level asserts none: KAS restores the level it stored itself.
func TestEffortDrift_ALoadOfARecordWithoutLevelAssertsNone(t *testing.T) {
	d := newEffortDrift(t)
	d.recordRanWithoutLevel(t, "sess-old")
	h := d.boot(t)
	t.Cleanup(func() { shutdownHub(t, h) })

	opts := reopenStartOpts(t, h)

	if opts.SessionID != "sess-old" || opts.Effort != "" {
		t.Errorf("the load = {SessionID %q, Effort %q}, want a session/load of sess-old asserting no level",
			opts.SessionID, opts.Effort)
	}
}

// A detached record holding no level opens its new session at the level the chat ran, not the seed.
func TestEffortDrift_ARefreshedSessionKeepsTheLevelTheChatRan(t *testing.T) {
	d := newEffortDrift(t)
	d.recordRanWithoutLevel(t, "")
	h := d.boot(t)
	t.Cleanup(func() { shutdownHub(t, h) })

	opts := reopenStartOpts(t, h)

	if opts.SessionID != "" || opts.Effort != "high" {
		t.Errorf("the refreshed session = {SessionID %q, Effort %q}, want a session/new at high",
			opts.SessionID, opts.Effort)
	}
	if c, _ := d.cs.Get(t.Context(), "c1"); c.Effort != "high" {
		t.Errorf("record level after the refresh = %q, want high pinned", c.Effort)
	}
}

// A session resumed into a new chat keeps the level KAS stored for it: the load sends none, the
// record adopts the reported level, and the next prompt holds the session to it, not to the seed.
func TestEffortDrift_AResumedSessionKeepsItsOwnLevel(t *testing.T) {
	d := newEffortDrift(t)
	if _, err := d.cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Model = ""
		c.RecordSession("sess-tui")
		return true
	}); err != nil {
		t.Fatalf("bind the resumed session: %v", err)
	}
	h := d.boot(t)

	opts := reopenStartOpts(t, h)
	if opts.SessionID != "sess-tui" || opts.Effort != "" {
		shutdownHub(t, h)
		t.Fatalf("the resume = {SessionID %q, Effort %q}, want a session/load of sess-tui asserting no level",
			opts.SessionID, opts.Effort)
	}
	br := fakeOf(h.coord.bridgeFor("c1"))
	h.handleSessionUpdate(t.Context(), "c1", modelEffortFrame(t, "fake-model", "low"))
	_, openErr := h.coord.openBridge(t.Context(), "c1", "")
	settleHeals(t, h)
	if openErr != nil {
		t.Fatalf("reopen: %v", openErr)
	}

	if c, _ := d.cs.Get(t.Context(), "c1"); c.Effort != "low" {
		t.Errorf("record level after the session reported low = %q, want low", c.Effort)
	}
	if got := br.lastEffort(); got != "low" {
		t.Errorf("effort asserted on the resumed session = %q, want low, the level KAS stored for it", got)
	}
}

// A bridgeless model pick pins the new model's first level, so the session that opens later sends
// it although the seed moved in between.
func TestEffortDrift_ABridgelessModelPickKeepsItsLevel(t *testing.T) {
	d := newEffortDrift(t)
	writeEffortSeed(t, d.dir, map[string]string{"m2": "low"})
	if _, err := d.cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.PendingModel = "m2"
		return true
	}); err != nil {
		t.Fatalf("park the pick: %v", err)
	}
	h := d.boot(t)
	t.Cleanup(func() { shutdownHub(t, h) })

	h.applyPendingModel(t.Context(), "c1")
	// Another length than low's: the settings cache keys on size and mtime, and an in-place
	// rewrite inside one mtime tick would read the old bytes.
	writeEffortSeed(t, d.dir, map[string]string{"m2": "medium"})
	opts := reopenStartOpts(t, h)

	if opts.Model != "m2" || opts.Effort != "low" {
		t.Errorf("the session after the pick = {Model %q, Effort %q}, want {m2, low}", opts.Model, opts.Effort)
	}
}

// A heal never re-asserts the seed: a chat with a session and no level adopts what KAS reports.
func TestHealEffort_AdoptsTheReportedLevelRatherThanTheSeed(t *testing.T) {
	h, br := healEffortFixture(t, "")
	dir := t.TempDir()
	writeEffortSeed(t, dir, map[string]string{"fake-model": "medium"})
	h.coord.lifecycle.configDir = dir
	if _, err := h.chatStore.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Model = "fake-model"
		c.RecordSession("sess-c1")
		return true
	}); err != nil {
		t.Fatalf("bind the session: %v", err)
	}

	h.handleSessionUpdate(t.Context(), "c1", configOptionFrame(t, "high", "", false))
	settleHeals(t, h)

	if got := br.lastEffort(); got != "" {
		t.Errorf("healed to %q, want no call: the seed is another chat's pick", got)
	}
	if c, _ := h.chatStore.Get(t.Context(), "c1"); c.Effort != "high" {
		t.Errorf("record level = %q, want high, the level the session runs", c.Effort)
	}
}

// Once the session runs the picked model the record still holds the old model's level until the
// switch persists; the switch asserts its own. A pick parked behind a turn is not a switch yet.
func TestHealEffort_LeavesAModelSwitchInFlightAlone(t *testing.T) {
	tests := map[string]struct {
		model    string
		wantHeal string
	}{
		"the session already runs the pick": {model: "m-new", wantHeal: ""},
		"the pick is parked behind a turn":  {model: "m-old", wantHeal: "max"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			h, br := healEffortFixture(t, "max")
			if _, err := h.chatStore.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
				c.Model, c.PendingModel = test.model, "m-new"
				return true
			}); err != nil {
				t.Fatalf("park the pick: %v", err)
			}
			sb := h.coord.bridgeFor("c1")

			h.handleSessionUpdate(t.Context(), "c1", configOptionFrame(t, "medium", "", false))
			settleHeals(t, h)

			if got := br.lastEffort(); got != test.wantHeal {
				t.Errorf("healed to %q, want %q", got, test.wantHeal)
			}
			if spent := !sb.claimEffortHeal(); spent != (test.wantHeal != "") {
				t.Errorf("heal latch spent = %v, want %v", spent, test.wantHeal != "")
			}
		})
	}
}

// catalogModel is the catalog entry KAS's model choice meta decodes to.
func catalogModel(t *testing.T, id, meta string) marotte.SessionModel {
	t.Helper()
	var m marotte.ModelChoiceMeta
	if err := json.Unmarshal([]byte(meta), &m); err != nil {
		t.Fatalf("decode %s: %v", meta, err)
	}
	return m.SessionModel(id, id, "")
}

// A switch onto a default-off model caps the asserted level by the target's thinking and tiers,
// never the old model's: fake-model's session reports thinking on and tiers topping out at medium.
// The record keeps the uncapped level for when thinking comes back on.
func TestApplyPendingModel_CapsTheSwitchLevelForThinkingOff(t *testing.T) {
	h, cs, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	dir := t.TempDir()
	writeEffortSeed(t, dir, map[string]string{"m2": "max"})
	h.coord.lifecycle.configDir = dir
	h.coord.catalog.SetModels([]marotte.SessionModel{
		catalogModel(t, "fake-model", `{"kiro":{"thinkingToggleable":true,"effortLevels":["low","medium","max"]}}`),
		catalogModel(t, "m2", `{"kiro":{"thinkingToggleable":true,"defaultThinkingEnabled":false,`+
			`"effortLevels":["low","medium","high","xhigh","max"]}}`),
	})
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name, c.Model, c.ThinkingActive = "A", "fake-model", marotte.ThinkingOn
		c.EffortLevels = []marotte.SessionEffortLevel{{ID: "low"}, {ID: "medium"}, {ID: "max"}}
		return true
	})
	if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("open: %v", err)
	}
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.PendingModel = "m2"
		return true
	})

	h.applyPendingModel(t.Context(), "c1")

	if got := br.lastEffort(); got != "high" {
		t.Errorf("the switch asserted %q, want high: m2's thinking is off and high is its top tier below xhigh", got)
	}
	if c, _ := cs.Get(t.Context(), "c1"); c.Model != "m2" || c.Effort != "max" {
		t.Errorf("header = {Model %q, Effort %q}, want {m2, max}", c.Model, c.Effort)
	}
}

// A chat's first session on a default-off model that has reported no tiers sends the catalog's
// thinking-off cap and pins the uncapped seed, and the respawn after a crash sends the cap again.
func TestEffortDrift_AFirstSessionOnADefaultOffModelSendsTheCapAndPinsTheSeed(t *testing.T) {
	d := newEffortDrift(t)
	writeEffortSeed(t, d.dir, map[string]string{"fake-model": "max"})
	h := d.boot(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	h.coord.catalog.SetModels([]marotte.SessionModel{
		catalogModel(t, "fake-model", `{"kiro":{"thinkingToggleable":true,"defaultThinkingEnabled":false,`+
			`"effortLevels":["low","medium","high","xhigh","max"]}}`),
	})

	first := reopenStartOpts(t, h)
	if first.SessionID != "" || first.Effort != "high" {
		t.Errorf("the first session = {SessionID %q, Effort %q}, want a session/new at high, max capped for thinking off",
			first.SessionID, first.Effort)
	}
	if c, _ := d.cs.Get(t.Context(), "c1"); c.Effort != "max" {
		t.Errorf("record level = %q, want max: the record keeps the uncapped level for thinking on", c.Effort)
	}
	crash(t, h)

	again := reopenStartOpts(t, h)
	if again.SessionID == "" || again.Effort != "high" {
		t.Errorf("the respawn = {SessionID %q, Effort %q}, want a session/load at high", again.SessionID, again.Effort)
	}
}

// modelOnlyFrame is the chat's own config_option_update for a model with no tiers: KAS omits the
// effortLevel option.
func modelOnlyFrame(t *testing.T, model string) *marotte.RPCResponse {
	t.Helper()
	update := map[string]any{
		"sessionUpdate": string(marotte.ACPUpdateConfigOption),
		"configOptions": []any{map[string]any{
			"id":           marotte.ConfigOptionModel,
			"currentValue": model,
			"options":      []any{map[string]any{"value": model, "name": model}},
		}},
	}
	params := mustJSON(t, map[string]any{"sessionId": "", "update": mustJSON(t, update)})
	return &marotte.RPCResponse{Method: marotte.MethodSessionUpdate, Params: params}
}

// pinAtHigh pins c1 at high on fake-model, the fake session's model, which last reported high.
func (d *effortDrift) pinAtHigh(t *testing.T) {
	t.Helper()
	if _, err := d.cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Effort, c.EffortActive = "high", "high"
		c.EffortLevels = []marotte.SessionEffortLevel{{ID: "low"}, {ID: "high"}}
		return true
	}); err != nil {
		t.Fatalf("pin the chat: %v", err)
	}
}

func parkPick(t *testing.T, d *effortDrift, model string) {
	t.Helper()
	if _, err := d.cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.PendingModel = model
		return true
	}); err != nil {
		t.Fatalf("park the pick: %v", err)
	}
}

// detach ends the bridge and the record's session, as refreshRetrySession does.
func detach(t *testing.T, d *effortDrift, h *Runtime) {
	t.Helper()
	bridgeRole{coord: h.coord}.CloseBridge(t.Context(), "c1", marotte.TurnOutcomeInterrupted)
	if _, err := d.cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.RecordSession("")
		return true
	}); err != nil {
		t.Fatalf("detach the session: %v", err)
	}
}

// The tier fake-model's session reported is fake-model's: after a move onto m-flat (listed with no
// tiers) or auto, the next session/new sends none and pins none, whichever door moved the chat and opens anew.
func TestModelMove_TheOldModelsTierReachesNoLaterSession(t *testing.T) {
	tests := map[string]func(t *testing.T, d *effortDrift, h *Runtime){
		"a bridgeless pick, then a refreshed session": func(t *testing.T, d *effortDrift, h *Runtime) {
			parkPick(t, d, "m-flat")
			h.applyPendingModel(t.Context(), "c1")
		},
		"a live switch, then a failed load's replacement": func(t *testing.T, d *effortDrift, h *Runtime) {
			if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
				t.Fatalf("open: %v", err)
			}
			parkPick(t, d, "m-flat")
			h.applyPendingModel(t.Context(), "c1")
			crash(t, h)
			d.mu.Lock()
			d.failLoads = true
			d.mu.Unlock()
		},
		"KAS repinning to auto, then a refreshed session": func(t *testing.T, d *effortDrift, h *Runtime) {
			if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
				t.Fatalf("open: %v", err)
			}
			h.handleSessionUpdate(t.Context(), "c1", modelOnlyFrame(t, marotte.ModelAuto))
			detach(t, d, h)
		},
	}
	for name, moveThenDoor := range tests {
		t.Run(name, func(t *testing.T) {
			d := newEffortDrift(t)
			d.pinAtHigh(t)
			h := d.boot(t)
			t.Cleanup(func() { shutdownHub(t, h) })
			h.coord.catalog.SetModels([]marotte.SessionModel{{ID: "m-flat"}})
			moveThenDoor(t, d, h)

			opts := reopenStartOpts(t, h)

			if opts.SessionID != "" || opts.Effort != "" {
				t.Errorf("the next session = {SessionID %q, Effort %q}, want a session/new sending no level",
					opts.SessionID, opts.Effort)
			}
			if c, _ := d.cs.Get(t.Context(), "c1"); c.Effort != "" {
				t.Errorf("record level = %q, want none: high was fake-model's", c.Effort)
			}
		})
	}
}

// A session that opened on another model than the record's (a withheld one's backend default) leaves
// none of the record's model's tier on the record.
func TestPersistNewSessionMetadata_AnotherModelDropsTheOldModelsTier(t *testing.T) {
	h, cs, br := newTestHub()
	br.mu.Lock()
	br.modelID = "m-default"
	br.mu.Unlock()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name, c.Model, c.Effort, c.EffortActive = "A", "m1", "high", "high"
		c.EffortLevels = []marotte.SessionEffortLevel{{ID: "low"}, {ID: "high"}}
		return true
	})

	if err := h.coord.persistNewSessionMetadata(t.Context(), "c1", br, firstLevel{}); err != nil {
		t.Fatalf("persist: %v", err)
	}

	c, _ := cs.Get(t.Context(), "c1")
	if c.Model != "m-default" || c.Effort != "" || c.EffortActive != "" || len(c.EffortLevels) != 0 {
		t.Errorf("record = {Model %q, Effort %q, EffortActive %q, EffortLevels %v}, want {m-default, none, none, none}",
			c.Model, c.Effort, c.EffortActive, c.EffortLevels)
	}
}
