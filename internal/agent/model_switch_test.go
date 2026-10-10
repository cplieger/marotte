package agent

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

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

// awaitModel polls until the record carries model with nothing pending, failing closed.
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

// An idle chat with no bridge takes the pick on the record alone: no entry, no spawn, session id kept.
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
	if sb := h.coord.bridgeFor("c1"); sb != nil {
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
	if c.Model == "claude-opus" {
		t.Errorf("chat.Model = %q, want it to change from claude-opus", c.Model)
	}
}

// An idle live chat switches at once, records one model_switched entry and resets usage; context_size is kept.
func TestSwitchModel_ALiveIdleChatRecordsTheSwitchAndResetsUsage(t *testing.T) {
	h, cs, _ := newTestHub()
	finishedTurn(t, h, "c1")
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "m-old"
		c.Usage = marotte.Usage{
			ContextSize: 200000, ContextPct: 80, Credits: 1.23,
		}
		return true
	})
	if _, err := h.coord.openBridge(t.Context(), "c1", "m-old"); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	// The spawn records the fake's model, so re-seed m-old after it.
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

// A closer's dispatch and the switch command can apply one pending pick at the same moment; the
// pick is applied and recorded once.
func TestApplyPendingModel_ConcurrentAppliersRecordOneSwitch(t *testing.T) {
	for range 50 {
		h, cs, _ := newTestHub()
		finishedTurn(t, h, "c1")
		if _, err := h.coord.openBridge(t.Context(), "c1", "m-old"); err != nil {
			t.Fatalf("OpenBridge: %v", err)
		}
		_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
			c.Model = "m-old"
			c.PendingModel = "m-new"
			return true
		})

		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() { h.applyPendingModel(t.Context(), "c1") })
		}
		wg.Wait()

		if switches := switchesOf(t, logOf(t, cs, "c1")); len(switches) != 1 {
			t.Fatalf("model_switched entries after 4 concurrent appliers = %+v, want exactly one", switches)
		}
	}
}

// The same model answers ok and writes nothing; there is no bare restart.
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

// A bad model is a 400 with no state change.
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

// The in-session switch (set_config_option) succeeds and the bridge stays alive.
func TestSwitchModel_FastPath_SetModelSucceeds(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "old-model"
		return true
	})
	sb, err := h.coord.openBridge(t.Context(), "c1", "old-model")
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

	// Same instance: no restart.
	sb2 := h.coord.bridgeFor("c1")
	if sb2 == nil {
		t.Fatal("bridge gone after fast-path switch")
	}
	fb2 := sb2.bridge.(*fakeBridge)
	if fb2.SessionID() != origSessionID {
		t.Errorf("session id changed: %q → %q (bridge was restarted, fast path failed)",
			origSessionID, fb2.SessionID())
	}
	// The fake received set_config_option with configId "model".
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
	c, _ := cs.Get(t.Context(), "c1")
	if c.Model != "new-model" {
		t.Errorf("chat.Model = %q, want new-model", c.Model)
	}
}

// kiro-cli accepts an unserved id and only the service rejects it mid-prompt, so the gate refuses first.
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

	// Nothing changed, and no bridge was torn down or spawned.
	c, _ := cs.Get(t.Context(), "c1")
	if c.Model != "m-old" {
		t.Errorf("chat.Model = %q, want the previous model preserved", c.Model)
	}
	if sb := h.coord.bridgeFor("c1"); sb != nil {
		t.Error("a bridge was created for a refused switch")
	}
}

// Both fail-open cases: no advertised catalog behaves as before the gate.
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

// The gate reads the unfiltered served set: the display list drops [Deprecated] and [Legacy].
func TestSwitchModel_AllowsADeprecatedModelTheAccountStillServes(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "m-old"
		// The display catalog omits it and the served set does not (applyModelConfigOptionLocked).
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

// The chat's recorded set outranks the bridge's older snapshot.
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

// A refused swap clears pending_model, keeps model, reports switch_failed and leaves the bridge alone.
func TestSwitchModel_ARefusedSwapClearsThePickAndReportsIt(t *testing.T) {
	h, cs, br := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "m-old"
		return true
	})
	if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	// The spawn records the fake's model, so re-seed m-old after it.
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

// A busy chat's pick is parked and applied by the close that idles it.
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

// A pick on a never-run chat is a preference persisted on the record alone, so a later set_effort auto-persist cannot clobber it.
func TestSwitchModel_PreSessionPickPersistsWithoutABridge(t *testing.T) {
	h, cs, br := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "m-old"
		// The tier was chosen under m-old; the pick clears it.
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

// A pick parked with no bridge lands at the next spawn, so the session opens on it.
func TestOpenBridge_APendingPickLandsBeforeTheSessionOpens(t *testing.T) {
	h, cs, br := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "m-old"
		c.PendingModel = "m-new"
		c.Effort = "max"
		return true
	})

	if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}

	opts := br.lastStartOpts()
	if opts == nil {
		t.Fatal("no bridge spawned")
	}
	if opts.Model != "m-new" {
		t.Errorf("StartOpts.Model = %q, want m-new: the session must open on the parked pick", opts.Model)
	}
	// The record's model is then the session's report, so the landing is read off the cleared pending field.
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
