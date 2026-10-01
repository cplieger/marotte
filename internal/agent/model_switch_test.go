package agent

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// --- switch_model ---

// switchesOf decodes every model_switched entry in entries, in file order.
func switchesOf(t *testing.T, entries []marotte.Entry) []marotte.EntryModelSwitched {
	t.Helper()
	var out []marotte.EntryModelSwitched
	for i := range entries {
		if entries[i].Kind != marotte.EntryKindModelSwitched {
			continue
		}
		var m marotte.EntryModelSwitched
		if err := json.Unmarshal(entries[i].Payload, &m); err != nil {
			t.Fatalf("decode model_switched %q: %v", entries[i].ID, err)
		}
		out = append(out, m)
	}
	return out
}

// awaitModel polls the record until it carries model with no pending pick, the
// shape the closer's dispatched apply leaves; the deadline fails closed.
func awaitModel(t *testing.T, cs *testChatStore, chatID marotte.ChatID, model string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, ok := cs.Get(t.Context(), chatID)
		if ok && c.Model == model && c.PendingModel == "" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	c, _ := cs.Get(t.Context(), chatID)
	t.Fatalf("model = %q pending_model = %q after 5s, want %q applied and the pick cleared", c.Model, c.PendingModel, model)
}

func TestSwitchModel_MissingChatID(t *testing.T) {
	h, _, _ := newTestHub()
	rec := postCmd(t, h, marotte.ClientCommand{Type: "switch_model"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
}

func TestSwitchModel_ChatNotFound(t *testing.T) {
	h, _, _ := newTestHub()
	rec := postCmd(t, h, marotte.ClientCommand{
		Type: "switch_model", ChatID: "nope",
	})
	if rec.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rec.Code)
	}
}

// An idle chat with no live bridge takes the pick on the record alone: model set,
// nothing pending, no entry (there is no session to switch), no bridge spawned, and
// the session id kept for the next OpenBridge to carry the model onto.
func TestSwitchModel_ANoBridgeChatTakesThePickOnTheRecord(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.ACPSessionID = "old-acp"
		c.Model = "m-old"
		return true
	})

	rec := postCmd(t, h, marotte.ClientCommand{
		Type: "switch_model", ChatID: "c1",
		Payload: json.RawMessage(`{"model":"m-new"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}

	c, _ := cs.Get(t.Context(), "c1")
	if c.Model != "m-new" || c.PendingModel != "" {
		t.Errorf("model = %q pending_model = %q, want m-new applied with nothing pending", c.Model, c.PendingModel)
	}
	if c.ACPSessionID != "old-acp" {
		t.Errorf("acp_session_id = %q, want old-acp preserved: the pick is not a session change", c.ACPSessionID)
	}
	if switches := switchesOf(t, logOf(t, cs, "c1")); len(switches) != 0 {
		t.Errorf("model_switched entries = %+v, want none: no session was switched", switches)
	}
	if sb := h.coord.Bridge("c1"); sb != nil {
		t.Error("a bridge was spawned for a pick on an idle bridgeless chat")
	}
}

func TestSwitchModel_WithModelOverride(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "claude-opus"
		c.ACPSessionID = "old-acp"
		return true
	})

	rec := postCmd(t, h, marotte.ClientCommand{
		Type: "switch_model", ChatID: "c1",
		Payload: json.RawMessage(`{"model":"claude-sonnet"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}

	c, _ := cs.Get(t.Context(), "c1")
	// The model on the chat should have changed from the override.
	if c.Model == "claude-opus" {
		t.Errorf("chat.Model = %q, want it to change from claude-opus", c.Model)
	}
}

// A switch on an idle chat with a live bridge lands in the session at once and is
// recorded between turns as one model_switched entry; the usage counters reset for
// the new model while the context_size, a property of the window, is preserved.
func TestSwitchModel_ALiveIdleChatRecordsTheSwitchAndResetsUsage(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "m-old"
		c.Usage = marotte.Usage{
			ContextSize: 200000, ContextPct: 80, Credits: 1.23,
		}
		return true
	})
	if _, err := h.coord.OpenBridge(t.Context(), "c1", "m-old"); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	// The spawn records the fake session's own model; the switch under test starts
	// from m-old, so the record is re-seeded after the spawn.
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Model = "m-old"; return true })

	rec := postCmd(t, h, marotte.ClientCommand{
		Type: "switch_model", ChatID: "c1",
		Payload: json.RawMessage(`{"model":"m-new"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}

	if switches := switchesOf(t, logOf(t, cs, "c1")); len(switches) != 1 || switches[0] != (marotte.EntryModelSwitched{From: "m-old", To: "m-new"}) {
		t.Errorf("model_switched entries = %+v, want exactly {m-old m-new}", switches)
	}
	c, _ := cs.Get(t.Context(), "c1")
	if c.Model != "m-new" || c.PendingModel != "" {
		t.Errorf("model = %q pending_model = %q, want m-new applied with nothing pending", c.Model, c.PendingModel)
	}
	if c.Usage.ContextSize != 200000 || c.Usage.ContextPct != 0 || c.Usage.Credits != 0 {
		t.Errorf("usage = %+v, want the counters reset and context_size 200000 preserved", c.Usage)
	}
}

// A pick resolving to the model already set is not a switch: it answers ok and
// writes nothing, so the usage counters stand, no entry lands and no bridge spawns.
// There is no bare restart; a wedged session is a fault to report, not to hide.
func TestSwitchModel_TheSameModelAnswersOKAndWritesNothing(t *testing.T) {
	h, cs, br := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.ACPSessionID = "old"
		c.Model = "m-same"
		c.Usage = marotte.Usage{
			ContextSize: 200000, ContextPct: 80, Credits: 1.23,
		}
		return true
	})

	for _, payload := range []json.RawMessage{nil, json.RawMessage(`{"model":"m-same"}`)} {
		rec := postCmd(t, h, marotte.ClientCommand{Type: "switch_model", ChatID: "c1", Payload: payload})
		if rec.Code != http.StatusOK {
			t.Fatalf("payload %s: code = %d, body = %s", payload, rec.Code, rec.Body.String())
		}
	}

	c, _ := cs.Get(t.Context(), "c1")
	if c.Model != "m-same" || c.PendingModel != "" {
		t.Errorf("model = %q pending_model = %q, want m-same with nothing pending", c.Model, c.PendingModel)
	}
	if c.Usage.Credits != 1.23 {
		t.Errorf("a non-switch reset the counters: %+v", c.Usage)
	}
	if entries := logOf(t, cs, "c1"); len(entries) != 0 {
		t.Errorf("entries = %+v, want none: a non-switch records nothing", entries)
	}
	if br.startCount() != 0 {
		t.Errorf("bridge starts = %d, want 0: a non-switch restarts nothing", br.startCount())
	}
}

// The model field is validated at the command boundary: a bad value returns 400
// without mutating chat state.
func TestSwitchModel_RejectsInvalidModel(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "m-old"
		return true
	})

	rec := postCmd(t, h, marotte.ClientCommand{
		Type: "switch_model", ChatID: "c1",
		Payload: json.RawMessage(`{"model":"bad<script>"}`),
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}

	c, _ := cs.Get(t.Context(), "c1")
	if c.Model != "m-old" {
		t.Errorf("chat.Model = %q, want unchanged m-old", c.Model)
	}
	if entries := logOf(t, cs, "c1"); len(entries) != 0 {
		t.Errorf("no entry should be persisted on validation failure, got %+v", entries)
	}
}

// Fast path: in-session model switch (set_config_option) succeeds, bridge stays alive.
func TestSwitchModel_FastPath_SetModelSucceeds(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "old-model"
		return true
	})
	// Create a bridge first so the fast path has something to call.
	sb, err := h.coord.OpenBridge(t.Context(), "c1", "old-model")
	if err != nil {
		t.Fatalf("getOrCreateBridge: %v", err)
	}
	fb := sb.bridge.(*fakeBridge)
	origSessionID := fb.SessionID()

	rec := postCmd(t, h, marotte.ClientCommand{
		Type: "switch_model", ChatID: "c1",
		Payload: json.RawMessage(`{"model":"new-model"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}

	// The bridge should still be the same instance (no restart).
	sb2 := h.coord.Bridge("c1")
	if sb2 == nil {
		t.Fatal("bridge gone after fast-path switch")
	}
	fb2 := sb2.bridge.(*fakeBridge)
	if fb2.SessionID() != origSessionID {
		t.Errorf("session id changed: %q → %q (bridge was restarted, fast path failed)",
			origSessionID, fb2.SessionID())
	}
	// The fake bridge should have received an in-session model switch
	// (v3 session/set_config_option, configId "model").
	fb2.mu.Lock()
	calls := append([]string(nil), fb2.calls...)
	fb2.mu.Unlock()
	found := false
	for _, c := range calls {
		if c == "session/set_config_option" {
			found = true
		}
	}
	if !found {
		t.Errorf("session/set_config_option not called on bridge; calls = %v", calls)
	}
	// Chat model should be updated.
	c, _ := cs.Get(t.Context(), "c1")
	if c.Model != "new-model" {
		t.Errorf("chat.Model = %q, want new-model", c.Model)
	}
}

// kiro-cli accepts a model id it cannot serve — set_config_option succeeds and only
// the SERVICE rejects it, mid-prompt, on every later turn — so the gate has to refuse
// before the id reaches the wire.
func TestSwitchModel_RefusesAModelTheAccountDoesNotServe(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "m-old"
		c.ServedModelIDs = []string{"m-old", "m-other"}
		return true
	})

	rec := postCmd(t, h, marotte.ClientCommand{
		Type: "switch_model", ChatID: "c1",
		Payload: json.RawMessage(`{"model":"m-unentitled"}`),
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("code = %d, want 409; body = %s", rec.Code, rec.Body.String())
	}

	// Nothing changed: the refusal must not persist the model, and must not tear
	// down or spawn a bridge on the way to failing.
	c, _ := cs.Get(t.Context(), "c1")
	if c.Model != "m-old" {
		t.Errorf("chat.Model = %q, want the previous model preserved", c.Model)
	}
	if sb := h.coord.Bridge("c1"); sb != nil {
		t.Error("a bridge was created for a refused switch")
	}
}

// Both fail-open cases, which are the ones that would turn this gate into an outage:
// a backend advertising no catalog must behave as it did before the gate existed.
func TestSwitchModel_AllowsWhenEntitlementIsUnknowable(t *testing.T) {
	cases := []struct {
		name   string
		served []string
	}{
		{"no advertised set at all", nil},
		{"an empty advertised set", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, cs, _ := newTestHub()
			_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
				c.Name = "A"
				c.Model = "m-old"
				c.ServedModelIDs = tc.served
				return true
			})
			rec := postCmd(t, h, marotte.ClientCommand{
				Type: "switch_model", ChatID: "c1",
				Payload: json.RawMessage(`{"model":"m-anything"}`),
			})
			if rec.Code != http.StatusOK {
				t.Fatalf("code = %d, want 200; body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// The picker's display list drops [Deprecated] and [Legacy] entries, so validating
// against it would refuse a model the account can still run — worse than the defect
// the gate prevents. The gate reads the unfiltered served set.
func TestSwitchModel_AllowsADeprecatedModelTheAccountStillServes(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "m-old"
		// The display catalog omits it; the served set does not. That divergence is
		// exactly what applyModelConfigOptionLocked produces.
		c.ServedModelIDs = []string{"m-old", "m-deprecated"}
		return true
	})

	rec := postCmd(t, h, marotte.ClientCommand{
		Type: "switch_model", ChatID: "c1",
		Payload: json.RawMessage(`{"model":"m-deprecated"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (a deprecated model is hidden, not unentitled); body = %s",
			rec.Code, rec.Body.String())
	}
}

// Which evidence the entitlement gate believes, in both directions:
// config_option_update refreshes the chat's recorded set after the session result,
// so the bridge's snapshot can only be older.
func TestSwitchModel_TheChatRecordOutranksTheLiveSession(t *testing.T) {
	cases := []struct {
		name     string
		recorded []string
		model    string
		wantCode int
	}{
		{
			name:     "the record admits a model it carries",
			recorded: []string{"m-old", "m-new"},
			model:    "m-new",
			wantCode: http.StatusOK,
		},
		{
			name:     "the record refuses a model it does not carry",
			recorded: []string{"m-old"},
			model:    "m-new",
			wantCode: http.StatusConflict,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, cs, br := newTestHub()
			if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
				c.Name = "A"
				c.Model = "m-old"
				c.ServedModelIDs = tc.recorded
				return true
			}); err != nil {
				t.Fatalf("seed the chat: %v", err)
			}
			h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})

			rec := postCmd(t, h, marotte.ClientCommand{
				Type: "switch_model", ChatID: "c1",
				Payload: json.RawMessage(`{"model":"` + tc.model + `"}`),
			})
			if rec.Code != tc.wantCode {
				t.Errorf("switch to %q with recorded=%v: code = %d, want %d; body = %s",
					tc.model, tc.recorded, rec.Code, tc.wantCode, rec.Body.String())
			}
		})
	}
}

// A swap the session refuses is reported and dropped: pending_model clears, model
// stands, the reader hears switch_failed, and the bridge is left alone. There is no
// restart fallback, which would cost the conversation's context to retry a pick.
func TestSwitchModel_ARefusedSwapClearsThePickAndReportsIt(t *testing.T) {
	h, cs, br := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "m-old"
		return true
	})
	if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	// The spawn records the fake session's own model; the refusal under test must
	// leave m-old standing, so the record is re-seeded after the spawn.
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Model = "m-old"; return true })
	origSessionID := br.SessionID()
	br.mu.Lock()
	br.setModelFailures = 1
	br.mu.Unlock()
	before := h.bus.fanout.Position().Head

	rec := postCmd(t, h, marotte.ClientCommand{
		Type: "switch_model", ChatID: "c1",
		Payload: json.RawMessage(`{"model":"m-new"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}

	c, _ := cs.Get(t.Context(), "c1")
	if c.Model != "m-old" || c.PendingModel != "" {
		t.Errorf("model = %q pending_model = %q, want m-old kept and the refused pick cleared", c.Model, c.PendingModel)
	}
	if switches := switchesOf(t, logOf(t, cs, "c1")); len(switches) != 0 {
		t.Errorf("model_switched entries = %+v, want none for a refused swap", switches)
	}
	var codes []marotte.ErrorCode
	for _, p := range errorPayloadsSince(t, h, before) {
		codes = append(codes, p.Code)
	}
	if len(codes) != 1 || codes[0] != marotte.ErrCodeSwitchFailed {
		t.Errorf("error codes = %v, want exactly [switch_failed]", codes)
	}
	if br.startCount() != 1 || br.SessionID() != origSessionID {
		t.Errorf("bridge starts = %d session = %q, want the one original bridge left alone (session %q)",
			br.startCount(), br.SessionID(), origSessionID)
	}
}

// A pick on a busy chat is parked as pending_model and applied by the close that
// makes the chat idle, so a switch never touches the running turn.
func TestSwitchModel_ABusyChatParksThePickUntilTheClose(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "m-old"
		return true
	})
	id, _ := h.stagePromptTurn(t, "c1")

	rec := postCmd(t, h, marotte.ClientCommand{
		Type: "switch_model", ChatID: "c1",
		Payload: json.RawMessage(`{"model":"m-new"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	c, _ := cs.Get(t.Context(), "c1")
	if c.Model != "m-old" || c.PendingModel != "m-new" {
		t.Fatalf("model = %q pending_model = %q mid-turn, want m-old with m-new parked", c.Model, c.PendingModel)
	}

	endTurn(t, h, "c1", id)
	awaitModel(t, cs, "c1", "m-new")
}

// A pick on a chat that has never run is a PREFERENCE: it persists on the record with
// no bridge, no session and no event row, so a later set_effort auto-persist cannot
// clobber it back.
func TestSwitchModel_PreSessionPickPersistsWithoutABridge(t *testing.T) {
	h, cs, br := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "m-old"
		// A tier chosen before the model pick was chosen under m-old; the pick
		// clears it so resolution falls to the new model's own default.
		c.Effort = "max"
		return true
	})

	rec := postCmd(t, h, marotte.ClientCommand{
		Type: "switch_model", ChatID: "c1",
		Payload: json.RawMessage(`{"model":"m-new"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}

	chat, ok := cs.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("chat gone after the pick")
	}
	if chat.Model != "m-new" {
		t.Errorf("chat.Model = %q, want m-new persisted on the record", chat.Model)
	}
	if chat.Effort != "" {
		t.Errorf("chat.Effort = %q, want cleared: the tier was chosen under m-old", chat.Effort)
	}
	if entries := logOf(t, cs, "c1"); len(entries) != 0 {
		t.Errorf("entries = %+v, want none: a pre-session pick is not a switch event", entries)
	}
	if opts := br.lastStartOpts(); opts != nil {
		t.Errorf("a bridge was spawned for a pre-session pick: StartOpts = %+v", opts)
	}
}

// A pick parked while the chat had no bridge (a process that restarted with it set, or
// a pick made under a prompt's reserved slot before the spawn) lands on the record at
// the next spawn, so the session opens on the pick with nothing left pending.
func TestOpenBridge_APendingPickLandsBeforeTheSessionOpens(t *testing.T) {
	h, cs, br := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "m-old"
		c.PendingModel = "m-new"
		c.Effort = "max"
		return true
	})

	if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}

	opts := br.lastStartOpts()
	if opts == nil {
		t.Fatal("no bridge spawned")
	}
	if opts.Model != "m-new" {
		t.Errorf("StartOpts.Model = %q, want m-new: the session must open on the parked pick", opts.Model)
	}
	// The record's model is the session's own report once it opens (the fake says
	// fake-model), so the pick's landing is read off the cleared pending field.
	c, _ := cs.Get(t.Context(), "c1")
	if c.PendingModel != "" {
		t.Errorf("pending_model = %q, want cleared by the spawn", c.PendingModel)
	}
	if c.Effort != "" {
		t.Errorf("effort = %q, want cleared: the tier was chosen under m-old", c.Effort)
	}
	if switches := switchesOf(t, logOf(t, cs, "c1")); len(switches) != 0 {
		t.Errorf("model_switched entries = %+v, want none: no session existed to switch", switches)
	}
}
