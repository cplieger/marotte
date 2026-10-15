package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

const fiveTiersMeta = `{"kiro":{"hasEffort":true,"defaultEffortLevel":"medium","effortLevels":["low","medium","high","xhigh","max"]}}`

// switchFixture is a started chat on fake-model at medium with a live idle bridge, whose catalog
// offers fake-model and opus the five tiers, and whose seed for opus is medium: another chat's
// pick, which a switch must not read over the level this chat picked.
func switchFixture(t *testing.T) (*effortDrift, *Runtime) {
	t.Helper()
	return switchFixtureOver(t, func(cs *testChatStore) chatRecords { return cs })
}

// switchFixtureOver is switchFixture with the runtime reading the chat through over's store.
func switchFixtureOver(t *testing.T, over func(*testChatStore) chatRecords) (*effortDrift, *Runtime) {
	t.Helper()
	d := newEffortDrift(t)
	writeEffortSeed(t, d.dir, map[string]string{"fake-model": "medium", "opus": "medium"})
	h := d.bootOver(t, over(d.cs))
	t.Cleanup(func() { shutdownHub(t, h) })
	finishedTurn(t, h, "c1")
	if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("open: %v", err)
	}
	h.coord.catalog.SetModels([]marotte.SessionModel{
		catalogModel(t, "fake-model", fiveTiersMeta),
		catalogModel(t, "opus", fiveTiersMeta),
		catalogModel(t, "gpt", `{"kiro":{"hasEffort":true,"defaultEffortLevel":"medium","effortLevels":["low","medium","high"]}}`),
	})
	if _, err := d.cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Model, c.Effort = "fake-model", "medium"
		return true
	}); err != nil {
		t.Fatalf("seed the level: %v", err)
	}
	return d, h
}

func postOK(t *testing.T, h *Runtime, typ marotte.CommandType, payload string) {
	t.Helper()
	rec := postCmd(t, h, marotte.ClientCommand{Type: typ, ChatID: "c1", Payload: json.RawMessage(payload)})
	if rec.Code != http.StatusOK {
		t.Fatalf("%s %s: code = %d, body = %s", typ, payload, rec.Code, rec.Body.String())
	}
}

// switchEntries is the chat's model_switched rows after its finished turn.
func switchEntries(t *testing.T, d *effortDrift) []marotte.EntryModelSwitched {
	t.Helper()
	return switchesOf(t, logOf(t, d.cs, "c1"))
}

// The reported bug: the tier picked with the switch is what the switch asserts, what its one row
// names and what the record keeps, and the respawn after a crash loads the new session at it.
func TestSwitchModel_ATierPickedWithTheSwitchIsWhatTheNewSessionRuns(t *testing.T) {
	d, h := switchFixture(t)
	br := fakeOf(h.coord.bridgeFor("c1"))

	postOK(t, h, "switch_model", `{"model":"opus","effort":"high"}`)

	if got := br.lastEffort(); got != "high" {
		t.Errorf("the switch asserted %q, want high: the tier picked with it, not opus's seed", got)
	}
	want := []marotte.EntryModelSwitched{{From: "fake-model", To: "opus", Effort: "high"}}
	if got := switchEntries(t, d); len(got) != 1 || got[0] != want[0] {
		t.Errorf("model_switched rows = %+v, want exactly %+v", got, want)
	}
	c, _ := d.cs.Get(t.Context(), "c1")
	if c.Model != "opus" || c.Effort != "high" || c.PendingModel != "" || c.PendingEffort != "" {
		t.Errorf("header = {Model %q, Effort %q, PendingModel %q, PendingEffort %q}, want {opus, high, \"\", \"\"}",
			c.Model, c.Effort, c.PendingModel, c.PendingEffort)
	}
	if got := h.coord.effortSeedFor(t.Context(), "opus"); got != "high" {
		t.Errorf("opus's new-chat seed = %q, want high: the pick was made for opus", got)
	}

	crash(t, h)
	if opts := reopenStartOpts(t, h); opts.SessionID == "" || opts.Effort != "high" {
		t.Errorf("the respawn = {SessionID %q, Effort %q}, want a session/load at high", opts.SessionID, opts.Effort)
	}
}

// Pick high, then the model in a second gesture: the switch carries the chat's level, which the
// new model offers, rather than reading the new model's seed.
func TestSwitchModel_AModelSwitchCarriesTheLevelThePickSet(t *testing.T) {
	d, h := switchFixture(t)
	br := fakeOf(h.coord.bridgeFor("c1"))

	postOK(t, h, "set_effort", `{"level":"high"}`)
	postOK(t, h, "switch_model", `{"model":"opus"}`)

	if got := br.lastEffort(); got != "high" {
		t.Errorf("the switch asserted %q, want high", got)
	}
	got := switchEntries(t, d)
	if len(got) != 2 || got[1] != (marotte.EntryModelSwitched{From: "fake-model", To: "opus", Effort: "high"}) {
		t.Errorf("model_switched rows = %+v, want the effort row then {fake-model opus high}", got)
	}
	if c, _ := d.cs.Get(t.Context(), "c1"); c.Effort != "high" {
		t.Errorf("record level = %q, want high", c.Effort)
	}
	if seed := h.coord.effortSeedFor(t.Context(), "opus"); seed != "medium" {
		t.Errorf("opus's seed = %q, want medium untouched: a carried level is no pick for opus", seed)
	}
}

// A tier the new model lacks is never asserted: the switch takes the model's first level and its
// row names the tier that was not offered.
func TestSwitchModel_ATierTheNewModelLacksFallsBackAndTheRowSaysSo(t *testing.T) {
	d, h := switchFixture(t)
	br := fakeOf(h.coord.bridgeFor("c1"))

	postOK(t, h, "switch_model", `{"model":"gpt","effort":"max"}`)

	if got := br.lastEffort(); got != "medium" {
		t.Errorf("the switch asserted %q, want gpt's default, medium", got)
	}
	want := marotte.EntryModelSwitched{From: "fake-model", To: "gpt", Effort: "medium", Unoffered: "max"}
	if got := switchEntries(t, d); len(got) != 1 || got[0] != want {
		t.Errorf("model_switched rows = %+v, want exactly %+v", got, want)
	}
	if c, _ := d.cs.Get(t.Context(), "c1"); c.Effort != "medium" {
		t.Errorf("record level = %q, want medium: a tier gpt lacks is never stored for it", c.Effort)
	}
	if seed := h.coord.effortSeedFor(t.Context(), "gpt"); seed != "" {
		t.Errorf("gpt's seed = %q, want none: max never landed on gpt", seed)
	}
}

// A remembered seed the new model no longer offers is no fallback: the switch takes the model's
// default, so the session is never asked a tier its row calls not offered.
func TestSwitchModel_AStaleSeedTheNewModelLacksIsNotItsFallback(t *testing.T) {
	d, h := switchFixture(t)
	br := fakeOf(h.coord.bridgeFor("c1"))
	writeEffortSeed(t, d.dir, map[string]string{"gpt": "max"})

	postOK(t, h, "switch_model", `{"model":"gpt","effort":"max"}`)

	if got := br.lastEffort(); got != "medium" {
		t.Errorf("the switch asserted %q, want gpt's default, medium: its seed max is a tier gpt lacks", got)
	}
	want := marotte.EntryModelSwitched{From: "fake-model", To: "gpt", Effort: "medium", Unoffered: "max"}
	if got := switchEntries(t, d); len(got) != 1 || got[0] != want {
		t.Errorf("model_switched rows = %+v, want exactly %+v", got, want)
	}
	if c, _ := d.cs.Get(t.Context(), "c1"); c.Effort != "medium" {
		t.Errorf("record level = %q, want medium", c.Effort)
	}
}

// A pick before any session has reported the model keeps its tier: nothing in the catalog can
// refuse it, and the session the chat opens next runs at it.
func TestSwitchModel_APickTheCatalogCannotJudgeKeepsItsTier(t *testing.T) {
	d, h := switchFixture(t)
	bridgeRole{coord: h.coord}.CloseBridge(t.Context(), "c1", marotte.TurnOutcomeInterrupted)
	h.coord.catalog.SetModels([]marotte.SessionModel{catalogModel(t, "fake-model", fiveTiersMeta)})

	postOK(t, h, "switch_model", `{"model":"opus","effort":"xhigh"}`)

	if c, _ := d.cs.Get(t.Context(), "c1"); c.Model != "opus" || c.Effort != "xhigh" {
		t.Errorf("header = {Model %q, Effort %q}, want {opus, xhigh}: not opus's seed, medium", c.Model, c.Effort)
	}
	if opts := reopenStartOpts(t, h); opts.Effort != "xhigh" {
		t.Errorf("the next session asked effort %q, want xhigh", opts.Effort)
	}
}

// Mid-turn, a second commit naming the parked model and no tier keeps the parked tier, and the
// close lands it as the one row.
func TestSwitchModel_AModelOnlyRepickKeepsTheParkedTier(t *testing.T) {
	d, h := switchFixture(t)
	br := fakeOf(h.coord.bridgeFor("c1"))
	id, _ := h.stagePromptTurn(t, "c1")

	postOK(t, h, "switch_model", `{"model":"opus","effort":"high"}`)
	postOK(t, h, "switch_model", `{"model":"opus"}`)

	if c, _ := d.cs.Get(t.Context(), "c1"); c.PendingModel != "opus" || c.PendingEffort != "high" {
		t.Errorf("mid-turn pending = {%q, %q}, want {opus, high}", c.PendingModel, c.PendingEffort)
	}
	endTurn(t, h, "c1", id)
	awaitModel(t, d.cs, "c1", "opus")

	if got := br.lastEffort(); got != "high" {
		t.Errorf("the switch at the close asserted %q, want high", got)
	}
	want := marotte.EntryModelSwitched{From: "fake-model", To: "opus", Effort: "high"}
	if got := switchEntries(t, d); len(got) != 1 || got[0] != want {
		t.Errorf("model_switched rows = %+v, want exactly %+v", got, want)
	}
}

// Mid-turn, a second commit naming the parked model with the chat's own tier replaces the parked
// tier: the last pick wins, even one equal to the level the chat already holds.
func TestSwitchModel_ARepickBackToTheChatsTierReplacesTheParkedTier(t *testing.T) {
	d, h := switchFixture(t)
	br := fakeOf(h.coord.bridgeFor("c1"))
	id, _ := h.stagePromptTurn(t, "c1")

	postOK(t, h, "switch_model", `{"model":"opus","effort":"high"}`)
	postOK(t, h, "switch_model", `{"model":"opus","effort":"medium"}`)

	if c, _ := d.cs.Get(t.Context(), "c1"); c.PendingModel != "opus" || c.PendingEffort != "medium" {
		t.Errorf("mid-turn pending = {%q, %q}, want {opus, medium}", c.PendingModel, c.PendingEffort)
	}
	br.mu.Lock()
	br.effort = ""
	br.mu.Unlock()
	endTurn(t, h, "c1", id)
	awaitModel(t, d.cs, "c1", "opus")
	if got := br.lastEffort(); got != "medium" {
		t.Errorf("the switch at the close asserted %q, want medium", got)
	}
}

// Mid-turn, a commit naming another model replaces the pick whole: the tier parked with the
// first model does not ride the second.
func TestSwitchModel_ARepickOfAnotherModelDropsTheParkedTier(t *testing.T) {
	d, h := switchFixture(t)
	h.stagePromptTurn(t, "c1")

	postOK(t, h, "switch_model", `{"model":"opus","effort":"high"}`)
	postOK(t, h, "switch_model", `{"model":"gpt"}`)

	if c, _ := d.cs.Get(t.Context(), "c1"); c.PendingModel != "gpt" || c.PendingEffort != "" {
		t.Errorf("mid-turn pending = {%q, %q}, want {gpt, \"\"}", c.PendingModel, c.PendingEffort)
	}
}

// Mid-turn, the Off stop over a tier parked with a model drops that tier, and the one request turns
// thinking off when it is on: the close lands the model at the chat's level with thinking still
// off. A model-only re-pick would keep the tier and turn thinking back on.
func TestSwitchModel_OffOverAParkedTierDropsIt(t *testing.T) {
	const toggleable = `{"kiro":{"hasEffort":true,"thinkingToggleable":true,"effortLevels":["low","medium","high","xhigh","max"]}}`
	for name, thinkingWasOn := range map[string]bool{"Off turns thinking off": true, "thinking already off": false} {
		t.Run(name, func(t *testing.T) {
			d, h := switchFixture(t)
			br := fakeOf(h.coord.bridgeFor("c1"))
			h.coord.catalog.SetModels([]marotte.SessionModel{
				catalogModel(t, "fake-model", toggleable),
				catalogModel(t, "opus", toggleable),
			})
			if !thinkingWasOn {
				if _, err := d.cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
					c.Thinking = marotte.ThinkingOff
					return true
				}); err != nil {
					t.Fatalf("choose thinking off: %v", err)
				}
			}
			id, _ := h.stagePromptTurn(t, "c1")

			postOK(t, h, "switch_model", `{"model":"opus","effort":"high"}`)
			postOK(t, h, "switch_model", `{"model":"opus","effort":"thinking:off"}`)

			if c, _ := d.cs.Get(t.Context(), "c1"); c.PendingModel != "opus" || c.PendingEffort != "" || c.Thinking != marotte.ThinkingOff {
				t.Errorf("mid-turn header = {pending %q/%q, thinking %q}, want {opus, \"\", off}",
					c.PendingModel, c.PendingEffort, c.Thinking)
			}
			endTurn(t, h, "c1", id)
			awaitModel(t, d.cs, "c1", "opus")

			br.mu.Lock()
			thinking := br.thinking
			br.mu.Unlock()
			if thinking == marotte.ThinkingOn || br.lastEffort() != "medium" {
				t.Errorf("the switch asserted {thinking %q, effort %q}, want thinking left off at medium", thinking, br.lastEffort())
			}
			want := marotte.EntryModelSwitched{From: "fake-model", To: "opus", Effort: "medium"}
			if got := switchEntries(t, d); len(got) != 1 || got[0] != want {
				t.Errorf("model_switched rows = %+v, want exactly %+v", got, want)
			}
			if c, _ := d.cs.Get(t.Context(), "c1"); c.Thinking != marotte.ThinkingOff || c.Effort != "medium" {
				t.Errorf("header = {Thinking %q, Effort %q}, want {off, medium}", c.Thinking, c.Effort)
			}
			if seed := h.coord.effortSeedFor(t.Context(), "opus"); seed != "medium" {
				t.Errorf("opus's seed = %q, want medium untouched: the dropped high never landed", seed)
			}
		})
	}
}

// Mid-turn, a stop kept on the running model after another model's switch was parked cancels that
// switch in one request: the stop lands in place, the pair is cleared, and the close moves nothing,
// so the last gesture sticks.
func TestSwitchModel_AStopKeptOnTheRunningModelCancelsAParkedSwitch(t *testing.T) {
	const toggleable = `{"kiro":{"hasEffort":true,"thinkingToggleable":true,"effortLevels":["low","medium","high","xhigh","max"]}}`
	for name, tc := range map[string]struct {
		thinkingWasOff           bool
		effort                   string
		wantEffort, wantThinking string
		rows                     []marotte.EntryModelSwitched
	}{
		"a tier": {
			effort: "low", wantEffort: "low", wantThinking: "",
			rows: []marotte.EntryModelSwitched{{From: "fake-model", To: "fake-model", Effort: "low"}},
		},
		"the chat's own tier, thinking on": {
			effort: "medium", wantEffort: "medium", wantThinking: "",
		},
		"the chat's own tier, thinking off": {
			thinkingWasOff: true, effort: "medium",
			wantEffort: "medium", wantThinking: marotte.ThinkingOn,
		},
		"Off turning thinking off": {
			effort: "thinking:off", wantEffort: "medium", wantThinking: marotte.ThinkingOff,
		},
		"Off, thinking already off": {
			thinkingWasOff: true, effort: "thinking:off",
			wantEffort: "medium", wantThinking: marotte.ThinkingOff,
		},
	} {
		t.Run(name, func(t *testing.T) {
			d, h := switchFixture(t)
			h.coord.catalog.SetModels([]marotte.SessionModel{
				catalogModel(t, "fake-model", toggleable),
				catalogModel(t, "opus", toggleable),
			})
			if tc.thinkingWasOff {
				if _, err := d.cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
					c.Thinking = marotte.ThinkingOff
					return true
				}); err != nil {
					t.Fatalf("choose thinking off: %v", err)
				}
			}
			id, _ := h.stagePromptTurn(t, "c1")

			postOK(t, h, "switch_model", `{"model":"opus","effort":"high"}`)
			postOK(t, h, "switch_model", `{"model":"fake-model","effort":"`+tc.effort+`"}`)

			if c, _ := d.cs.Get(t.Context(), "c1"); c.PendingModel != "" || c.PendingEffort != "" ||
				c.Effort != tc.wantEffort || c.Thinking != tc.wantThinking {
				t.Errorf("mid-turn header = {pending %q/%q, effort %q, thinking %q}, want no pending at {%s, %q}: the stop lands at once and cancels opus",
					c.PendingModel, c.PendingEffort, c.Effort, c.Thinking, tc.wantEffort, tc.wantThinking)
			}
			endTurn(t, h, "c1", id)
			awaitTurnOver(t, h, "c1")
			h.applyPendingModel(t.Context(), "c1")

			c, _ := d.cs.Get(t.Context(), "c1")
			if c.Model != "fake-model" || c.Effort != tc.wantEffort || c.Thinking != tc.wantThinking {
				t.Errorf("header = {Model %q, Effort %q, Thinking %q}, want {fake-model, %s, %q}",
					c.Model, c.Effort, c.Thinking, tc.wantEffort, tc.wantThinking)
			}
			if got := switchEntries(t, d); !slices.Equal(got, tc.rows) {
				t.Errorf("model_switched rows = %+v, want %+v", got, tc.rows)
			}
			if seed := h.coord.effortSeedFor(t.Context(), "opus"); seed != "medium" {
				t.Errorf("opus's seed = %q, want medium untouched: the cancelled high never landed", seed)
			}
		})
	}
}

// awaitTurnOver polls until chatID has no live turn, failing closed.
func awaitTurnOver(t *testing.T, h *Runtime, chatID marotte.ChatID) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.coord.turns.live(chatID) {
		if time.Now().After(deadline) {
			t.Fatalf("%s: the turn is still live after its close", chatID)
		}
		time.Sleep(time.Millisecond)
	}
}

// holdKey marks a context whose chat reads and writes heldChatStore runs a hold's hooks after.
type holdKey struct{}

// storeHold runs afterRead once after the first read made on its context, and afterWrite once
// after the first write; a nil hook is skipped.
type storeHold struct {
	afterRead, afterWrite func()
	read, wrote           sync.Once
}

// heldChatStore is the drift's store, running a storeHold's hooks for a context that carries one.
type heldChatStore struct{ *testChatStore }

func (s heldChatStore) Get(ctx context.Context, id marotte.ChatID) (*marotte.Chat, bool) {
	c, ok := s.testChatStore.Get(ctx, id)
	if hold, held := ctx.Value(holdKey{}).(*storeHold); held && hold.afterRead != nil {
		hold.read.Do(hold.afterRead)
	}
	return c, ok
}

func (s heldChatStore) Mutate(ctx context.Context, id marotte.ChatID, fn func(c *marotte.Chat, exists bool) bool) (string, error) {
	v, err := s.testChatStore.Mutate(ctx, id, fn)
	if hold, held := ctx.Value(holdKey{}).(*storeHold); held && hold.afterWrite != nil {
		hold.wrote.Do(hold.afterWrite)
	}
	return v, err
}

func heldSwitchFixture(t *testing.T) (*effortDrift, *Runtime) {
	t.Helper()
	return switchFixtureOver(t, func(cs *testChatStore) chatRecords { return heldChatStore{cs} })
}

// admitHeld starts admitting p and returns once the admission has read the chat, held there; the
// returned func lets it go on and answers its error.
func admitHeld(t *testing.T, h *Runtime, p marotte.SwitchModelCommand) func() error {
	t.Helper()
	read, resume := make(chan struct{}), make(chan struct{})
	hold := &storeHold{afterRead: func() {
		close(read)
		<-resume
	}}
	answered := make(chan error, 1)
	go func() {
		answered <- h.admitSwitchModel(context.WithValue(t.Context(), holdKey{}, hold), "c1", p)
	}()
	select {
	case <-read:
	case err := <-answered:
		t.Fatalf("the admission answered %v before reading the chat", err)
	case <-time.After(5 * time.Second):
		t.Fatal("the admission never read the chat")
	}
	return func() error {
		close(resume)
		return <-answered
	}
}

// parkOnBridgelessChat closes the fixture's bridge and parks pair, as a pick made with no session.
func parkOnBridgelessChat(t *testing.T, d *effortDrift, h *Runtime, pair pendingPair) {
	t.Helper()
	bridgeRole{coord: h.coord}.CloseBridge(t.Context(), "c1", marotte.TurnOutcomeInterrupted)
	if _, err := d.cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.PendingModel, c.PendingEffort = pair.model, pair.effort
		return true
	}); err != nil {
		t.Fatalf("park the pair: %v", err)
	}
}

// A spawn's fold waits for a switch being admitted, so it lands the pair that switch parked, not
// the one parked before it. gpt lacks max, so the landing takes gpt's default.
func TestSwitchModel_TheSpawnsFoldWaitsForASwitchBeingAdmitted(t *testing.T) {
	d, h := heldSwitchFixture(t)
	parkOnBridgelessChat(t, d, h, pendingPair{model: "gpt", effort: "low"})
	finish := admitHeld(t, h, marotte.SwitchModelCommand{Model: "gpt", Effort: "max"})

	folded := make(chan struct{})
	go func() {
		h.coord.foldParkedPick(t.Context(), "c1")
		close(folded)
	}()
	// Room for a fold that does not wait to land the earlier pair.
	select {
	case <-folded:
	case <-time.After(100 * time.Millisecond):
	}
	if c, _ := d.cs.Get(t.Context(), "c1"); c.Model != "fake-model" || c.PendingModel != "gpt" || c.PendingEffort != "low" {
		t.Errorf("mid-admission header = {Model %q, PendingModel %q, PendingEffort %q}, want {fake-model, gpt, low}: "+
			"the fold landed between the admission's read and its park", c.Model, c.PendingModel, c.PendingEffort)
	}
	if err := finish(); err != nil {
		t.Fatalf("admission: %v", err)
	}
	<-folded

	if c, _ := d.cs.Get(t.Context(), "c1"); c.Model != "gpt" || c.Effort != "medium" || c.PendingModel != "" || c.PendingEffort != "" {
		t.Errorf("header = {Model %q, Effort %q, PendingModel %q, PendingEffort %q}, want {gpt, medium, \"\", \"\"}",
			c.Model, c.Effort, c.PendingModel, c.PendingEffort)
	}
}

// An admission applies the one decision it read: a model the session reports meanwhile, which no
// switch lock orders, does not turn the request into an OK that parks nothing.
func TestSwitchModel_AnAdmissionParksWhatItDecidedWhenTheModelMovesMeanwhile(t *testing.T) {
	d, h := heldSwitchFixture(t)
	parkOnBridgelessChat(t, d, h, pendingPair{model: "gpt", effort: "low"})
	finish := admitHeld(t, h, marotte.SwitchModelCommand{Model: "gpt", Effort: "max"})

	// config_option_update's move onto gpt at low.
	if _, err := d.cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.MoveToModel("gpt")
		c.Effort = "low"
		return true
	}); err != nil {
		t.Fatalf("move the model: %v", err)
	}
	if err := finish(); err != nil {
		t.Fatalf("admission: %v", err)
	}

	if c, _ := d.cs.Get(t.Context(), "c1"); c.PendingModel != "gpt" || c.PendingEffort != "max" {
		t.Errorf("parked = {%q, %q}, want {gpt, max}", c.PendingModel, c.PendingEffort)
	}
}

// A client walking away once the switch's stop has landed still gets its park: the stop never
// stands with the pair it cancelled still parked.
func TestSwitchModel_AStopThatLandedKeepsItsParkWhenTheClientWalksAway(t *testing.T) {
	d, h := heldSwitchFixture(t)
	if _, err := d.cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.PendingModel, c.PendingEffort = "opus", "high"
		return true
	}); err != nil {
		t.Fatalf("park the pair: %v", err)
	}
	ctx, walkAway := context.WithCancel(t.Context())
	defer walkAway()
	// The stop's own record write is the first on the request's context.
	ctx = context.WithValue(ctx, holdKey{}, &storeHold{afterWrite: walkAway})

	if err := h.admitSwitchModel(ctx, "c1", marotte.SwitchModelCommand{Model: "fake-model", Effort: "high"}); err != nil {
		t.Fatalf("admission: %v", err)
	}

	if c, _ := d.cs.Get(t.Context(), "c1"); c.Effort != "high" || c.PendingModel != "" || c.PendingEffort != "" {
		t.Errorf("header = {Effort %q, PendingModel %q, PendingEffort %q}, want {high, \"\", \"\"}",
			c.Effort, c.PendingModel, c.PendingEffort)
	}
}

// Mid-turn the tier waits with the model: nothing reaches the running session, and the close
// lands both as one row.
func TestSwitchModel_ABusyChatParksTheTierWithThePick(t *testing.T) {
	d, h := switchFixture(t)
	br := fakeOf(h.coord.bridgeFor("c1"))
	id, _ := h.stagePromptTurn(t, "c1")
	before := br.lastEffort()

	postOK(t, h, "switch_model", `{"model":"opus","effort":"high"}`)

	c, _ := d.cs.Get(t.Context(), "c1")
	if c.Model != "fake-model" || c.Effort != "medium" || c.PendingModel != "opus" || c.PendingEffort != "high" {
		t.Errorf("mid-turn header = {Model %q, Effort %q, PendingModel %q, PendingEffort %q}, want {fake-model, medium, opus, high}",
			c.Model, c.Effort, c.PendingModel, c.PendingEffort)
	}
	if got := br.lastEffort(); got != before {
		t.Errorf("the running session was asserted %q mid-turn, want nothing sent", got)
	}

	endTurn(t, h, "c1", id)
	awaitModel(t, d.cs, "c1", "opus")

	if got := br.lastEffort(); got != "high" {
		t.Errorf("the switch at the close asserted %q, want high", got)
	}
	want := marotte.EntryModelSwitched{From: "fake-model", To: "opus", Effort: "high"}
	if got := switchEntries(t, d); len(got) != 1 || got[0] != want {
		t.Errorf("model_switched rows = %+v, want exactly %+v", got, want)
	}
	if c, _ := d.cs.Get(t.Context(), "c1"); c.Effort != "high" || c.PendingEffort != "" {
		t.Errorf("header = {Effort %q, PendingEffort %q}, want {high, \"\"}", c.Effort, c.PendingEffort)
	}
}

// A tier picked with a model on a chat with no bridge lands on the record, and the session it
// opens next runs at it.
func TestSwitchModel_ABridgelessPickOpensItsSessionAtTheTier(t *testing.T) {
	d, h := switchFixture(t)
	bridgeRole{coord: h.coord}.CloseBridge(t.Context(), "c1", marotte.TurnOutcomeInterrupted)

	postOK(t, h, "switch_model", `{"model":"opus","effort":"xhigh"}`)

	if c, _ := d.cs.Get(t.Context(), "c1"); c.Model != "opus" || c.Effort != "xhigh" || c.PendingModel != "" {
		t.Errorf("header = {Model %q, Effort %q, PendingModel %q}, want {opus, xhigh, \"\"}", c.Model, c.Effort, c.PendingModel)
	}
	if opts := reopenStartOpts(t, h); opts.Effort != "xhigh" {
		t.Errorf("the next session asked effort %q, want xhigh", opts.Effort)
	}
}

// A pair parked with no bridge lands when the next session opens: the spawn folds it onto the
// record first, so the session opens on the model at the parked tier.
func TestSwitchModel_TheSpawnLandsAParkedPair(t *testing.T) {
	d, h := switchFixture(t)
	bridgeRole{coord: h.coord}.CloseBridge(t.Context(), "c1", marotte.TurnOutcomeInterrupted)
	if _, err := d.cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.PendingModel, c.PendingEffort = "opus", "xhigh"
		return true
	}); err != nil {
		t.Fatalf("park the pair: %v", err)
	}

	opts := reopenStartOpts(t, h)

	if opts.Model != "opus" || opts.Effort != "xhigh" {
		t.Errorf("the session = {Model %q, Effort %q}, want {opus, xhigh}", opts.Model, opts.Effort)
	}
	if c, _ := d.cs.Get(t.Context(), "c1"); c.Effort != "xhigh" || c.PendingModel != "" || c.PendingEffort != "" {
		t.Errorf("header = {Effort %q, PendingModel %q, PendingEffort %q}, want {xhigh, \"\", \"\"}",
			c.Effort, c.PendingModel, c.PendingEffort)
	}
}

// A switch-by-restart opens the target's first session at the chat's level when the target
// offers it, not at the target's seed.
func TestSwitchModel_ASwitchByRestartCarriesTheChatsLevel(t *testing.T) {
	d, h := switchFixture(t)
	bridgeRole{coord: h.coord}.CloseBridge(t.Context(), "c1", marotte.TurnOutcomeInterrupted)
	if _, err := d.cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Effort = "high"
		return true
	}); err != nil {
		t.Fatalf("seed the level: %v", err)
	}

	sb, err := h.coord.openBridge(t.Context(), "c1", "opus")
	if err != nil {
		t.Fatalf("open on opus: %v", err)
	}

	if opts := fakeOf(sb).lastStartOpts(); opts == nil || opts.Model != "opus" || opts.Effort != "high" {
		t.Errorf("the session = %+v, want one on opus at high, not opus's seed, medium", opts)
	}
}

// A tier picked with a switch turns thinking back on, as a set_effort pick does, so the session
// is asked the tier uncapped.
func TestSwitchModel_ATierPickedWithTheSwitchTurnsThinkingOn(t *testing.T) {
	d, h := switchFixture(t)
	br := fakeOf(h.coord.bridgeFor("c1"))
	h.coord.catalog.SetModels([]marotte.SessionModel{
		catalogModel(t, "fake-model", fiveTiersMeta),
		catalogModel(t, "opus", `{"kiro":{"hasEffort":true,"thinkingToggleable":true,"effortLevels":["low","medium","high","xhigh","max"]}}`),
	})
	if _, err := d.cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Thinking = marotte.ThinkingOff
		return true
	}); err != nil {
		t.Fatalf("choose thinking off: %v", err)
	}

	postOK(t, h, "switch_model", `{"model":"opus","effort":"max"}`)

	br.mu.Lock()
	thinking := br.thinking
	br.mu.Unlock()
	if thinking != marotte.ThinkingOn || br.lastEffort() != "max" {
		t.Errorf("the switch asserted {thinking %q, effort %q}, want {on, max}", thinking, br.lastEffort())
	}
	if c, _ := d.cs.Get(t.Context(), "c1"); c.Thinking != marotte.ThinkingOn || c.Effort != "max" {
		t.Errorf("header = {Thinking %q, Effort %q}, want {on, max}", c.Thinking, c.Effort)
	}
}

// Onto a model whose thinking stays off, the row names the tier the session is asked to run, the
// cap, while the record keeps the carried preference for when thinking comes back on.
func TestSwitchModel_TheRowNamesTheCappedTierAndTheRecordThePreference(t *testing.T) {
	d, h := switchFixture(t)
	br := fakeOf(h.coord.bridgeFor("c1"))
	h.coord.catalog.SetModels([]marotte.SessionModel{
		catalogModel(t, "fake-model", fiveTiersMeta),
		catalogModel(t, "opus", `{"kiro":{"hasEffort":true,"thinkingToggleable":true,"defaultThinkingEnabled":false,`+
			`"effortLevels":["low","medium","high","xhigh","max"]}}`),
	})
	if _, err := d.cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Effort = "max"
		return true
	}); err != nil {
		t.Fatalf("seed the level: %v", err)
	}

	postOK(t, h, "switch_model", `{"model":"opus"}`)

	if got := br.lastEffort(); got != "high" {
		t.Errorf("the switch asserted %q, want high: max capped while opus's thinking stays off", got)
	}
	want := marotte.EntryModelSwitched{From: "fake-model", To: "opus", Effort: "high"}
	if got := switchEntries(t, d); len(got) != 1 || got[0] != want {
		t.Errorf("model_switched rows = %+v, want exactly %+v", got, want)
	}
	if c, _ := d.cs.Get(t.Context(), "c1"); c.Effort != "max" {
		t.Errorf("record level = %q, want max: the cap is never written over the preference", c.Effort)
	}
}

// Two gestures before any session has reported the new model: the chat's level rides the pick,
// since nothing in the catalog can refuse it, and the next session runs at it.
func TestSwitchModel_ACarriedLevelRidesAPickTheCatalogCannotJudge(t *testing.T) {
	d, h := switchFixture(t)
	bridgeRole{coord: h.coord}.CloseBridge(t.Context(), "c1", marotte.TurnOutcomeInterrupted)
	h.coord.catalog.SetModels([]marotte.SessionModel{catalogModel(t, "fake-model", fiveTiersMeta)})

	postOK(t, h, "set_effort", `{"level":"high"}`)
	postOK(t, h, "switch_model", `{"model":"opus"}`)

	if c, _ := d.cs.Get(t.Context(), "c1"); c.Model != "opus" || c.Effort != "high" {
		t.Errorf("header = {Model %q, Effort %q}, want {opus, high}: not opus's seed, medium", c.Model, c.Effort)
	}
	if opts := reopenStartOpts(t, h); opts.Effort != "high" {
		t.Errorf("the next session asked effort %q, want high", opts.Effort)
	}
}

// A model listed with no tiers runs none: the switch asserts no effort, its row names the wanted
// tier as not offered, and a stale seed for it is never taken as its level.
func TestSwitchModel_ATierlessModelRunsNoTierAndTheRowSaysSo(t *testing.T) {
	for name, payload := range map[string]string{
		"a picked tier":  `{"model":"flat","effort":"high"}`,
		"a carried tier": `{"model":"flat"}`,
	} {
		t.Run(name, func(t *testing.T) {
			d, h := switchFixture(t)
			br := fakeOf(h.coord.bridgeFor("c1"))
			writeEffortSeed(t, d.dir, map[string]string{"flat": "xhigh"})
			h.coord.catalog.SetModels([]marotte.SessionModel{
				catalogModel(t, "fake-model", fiveTiersMeta),
				catalogModel(t, "flat", `{"kiro":{"hasEffort":false}}`),
			})
			if _, err := d.cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
				c.Effort = "high"
				return true
			}); err != nil {
				t.Fatalf("seed the level: %v", err)
			}
			br.mu.Lock()
			br.effort = ""
			br.mu.Unlock()

			postOK(t, h, "switch_model", payload)

			if got := br.lastEffort(); got != "" {
				t.Errorf("the switch asserted %q, want no effort: flat runs none", got)
			}
			want := marotte.EntryModelSwitched{From: "fake-model", To: "flat", Unoffered: "high"}
			if got := switchEntries(t, d); len(got) != 1 || got[0] != want {
				t.Errorf("model_switched rows = %+v, want exactly %+v", got, want)
			}
			if c, _ := d.cs.Get(t.Context(), "c1"); c.Model != "flat" || c.Effort != "" {
				t.Errorf("header = {Model %q, Effort %q}, want {flat, \"\"}", c.Model, c.Effort)
			}
		})
	}
}

// A switch retried after it landed (its first response lost) answers ok and writes nothing more,
// a tier the new model lacked included.
func TestSwitchModel_ARetryOfALandedSwitchIsOK(t *testing.T) {
	for name, tc := range map[string]struct {
		payload string
		row     marotte.EntryModelSwitched
	}{
		"an offered tier":   {`{"model":"opus","effort":"high"}`, marotte.EntryModelSwitched{From: "fake-model", To: "opus", Effort: "high"}},
		"an unoffered tier": {`{"model":"gpt","effort":"max"}`, marotte.EntryModelSwitched{From: "fake-model", To: "gpt", Effort: "medium", Unoffered: "max"}},
	} {
		t.Run(name, func(t *testing.T) {
			d, h := switchFixture(t)

			postOK(t, h, "switch_model", tc.payload)
			postOK(t, h, "switch_model", tc.payload)

			if got := switchEntries(t, d); len(got) != 1 || got[0] != tc.row {
				t.Errorf("model_switched rows = %+v, want exactly %+v", got, tc.row)
			}
		})
	}
}

// A tier the model in force lacks is refused, as is a malformed tier.
func TestSwitchModel_RefusesAnEffortItCannotCarry(t *testing.T) {
	d, h := switchFixture(t)
	if _, err := d.cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Model, c.Effort = "gpt", "low"
		return true
	}); err != nil {
		t.Fatalf("run gpt: %v", err)
	}
	for name, payload := range map[string]string{
		"a tier the model in force lacks": `{"model":"gpt","effort":"max"}`,
		"a malformed tier":                `{"model":"opus","effort":"not a tier"}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := postCmd(t, h, marotte.ClientCommand{Type: "switch_model", ChatID: "c1", Payload: json.RawMessage(payload)})
			if rec.Code != http.StatusBadRequest {
				t.Errorf("code = %d, want 400", rec.Code)
			}
		})
	}
}

// A tier named with the model in force is set in place, as set_effort sets it: its row and the
// record.
func TestSwitchModel_ATierOnTheModelInForceIsSetInPlace(t *testing.T) {
	d, h := switchFixture(t)

	postOK(t, h, "switch_model", `{"model":"fake-model","effort":"high"}`)

	if c, _ := d.cs.Get(t.Context(), "c1"); c.Model != "fake-model" || c.Effort != "high" || c.PendingModel != "" {
		t.Errorf("header = {Model %q, Effort %q, PendingModel %q}, want {fake-model, high, \"\"}", c.Model, c.Effort, c.PendingModel)
	}
	want := []marotte.EntryModelSwitched{{From: "fake-model", To: "fake-model", Effort: "high"}}
	if got := switchEntries(t, d); !slices.Equal(got, want) {
		t.Errorf("model_switched rows = %+v, want %+v", got, want)
	}
}

// A turn's close that lands while a cancelling switch is applying its stop waits for the whole
// request: the parked pair is cleared before the close can land it, so Off on the running model
// never ends on the parked model's tier with thinking back on.
func TestSwitchModel_ATurnCloseCannotLandBetweenAStopAndItsCancel(t *testing.T) {
	const toggleable = `{"kiro":{"hasEffort":true,"thinkingToggleable":true,"effortLevels":["low","medium","high","xhigh","max"]}}`
	d, h := switchFixture(t)
	br := fakeOf(h.coord.bridgeFor("c1"))
	h.coord.catalog.SetModels([]marotte.SessionModel{
		catalogModel(t, "fake-model", toggleable),
		catalogModel(t, "opus", toggleable),
	})
	id, _ := h.stagePromptTurn(t, "c1")
	postOK(t, h, "switch_model", `{"model":"opus","effort":"high"}`)

	// The stop's set_config_option holds the request between its stop and its park.
	release := make(chan struct{})
	br.mu.Lock()
	br.blockOn = map[string]chan struct{}{marotte.MethodSetConfigOption: release}
	br.mu.Unlock()
	configCalls := func() int {
		n := 0
		for _, m := range br.callLog() {
			if m == marotte.MethodSetConfigOption {
				n++
			}
		}
		return n
	}
	before := configCalls()
	answered := make(chan int, 1)
	go func() {
		rec := postCmd(t, h, marotte.ClientCommand{
			Type: "switch_model", ChatID: "c1", Payload: json.RawMessage(`{"model":"fake-model","effort":"thinking:off"}`),
		})
		answered <- rec.Code
	}()
	deadline := time.Now().Add(5 * time.Second)
	for configCalls() == before {
		if time.Now().After(deadline) {
			t.Fatal("the cancelling switch never applied its stop")
		}
		time.Sleep(time.Millisecond)
	}

	endTurn(t, h, "c1", id)
	awaitTurnOver(t, h, "c1")
	closed := make(chan struct{})
	go func() {
		h.applyPendingModel(t.Context(), "c1")
		close(closed)
	}()
	// Room for a close that does not wait to land the parked pair before the request goes on.
	select {
	case <-closed:
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if code := <-answered; code != http.StatusOK {
		t.Fatalf("the cancelling switch answered %d", code)
	}
	<-closed
	h.applyPendingModel(t.Context(), "c1")

	c, _ := d.cs.Get(t.Context(), "c1")
	if c.Model != "fake-model" || c.Effort != "medium" || c.Thinking != marotte.ThinkingOff || c.PendingModel != "" {
		t.Errorf("header = {Model %q, Effort %q, Thinking %q, PendingModel %q}, want {fake-model, medium, off, \"\"}",
			c.Model, c.Effort, c.Thinking, c.PendingModel)
	}
	if got := switchEntries(t, d); len(got) != 0 {
		t.Errorf("model_switched rows = %+v, want none: opus never landed", got)
	}
}
