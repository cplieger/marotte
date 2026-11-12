package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rpcerr"
)

// botRoleMessage is the Bot's own mode_not_applied text (design §7.1 "Holding the mode"): its pill has no mode menu,
// so "pick the mode again" is advice a Bot cannot follow.
const botRoleMessage = "This Bot's role did not load. Its next message retries."

// botRuntime is a runtime whose chat sessions are bridges, in open order, then fresh fakes. HOME is a test directory,
// so the role's paths sit under it.
func botRuntime(t *testing.T, bridges ...*fakeBridge) (*Runtime, *testChatStore) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	var mu sync.Mutex
	next := bridges
	factory := func() engineSession {
		mu.Lock()
		defer mu.Unlock()
		if len(next) == 0 {
			return newFakeBridge()
		}
		b := next[0]
		next = next[1:]
		return b
	}
	cs := newTestChatStore()
	h := newRuntimeOn(context.Background(), "/tmp/work", fakeEngineOf(factory, nil), cs)
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	settleBoot(t, h)
	return h, cs
}

// reporting is a fresh fake whose doors report mode current among modes.
func reporting(current string, modes ...string) *fakeBridge {
	b := newFakeBridge()
	b.currentMode = current
	for _, id := range modes {
		b.modes = append(b.modes, marotte.SessionMode{ID: id, Name: id, Source: "bundled"})
	}
	return b
}

// seedModeChat writes chatID: a started Bot when bot is set, else a regular chat on the default mode; resumed gives it
// a session to load.
func seedModeChat(t *testing.T, cs *testChatStore, chatID marotte.ChatID, bot, resumed bool) {
	t.Helper()
	if _, err := cs.Mutate(t.Context(), chatID, func(c *marotte.Chat, _ bool) bool {
		c.Name = "Weekly answers"
		c.CurrentModeID = marotte.DefaultModeID
		if bot {
			c.CurrentModeID = marotte.BotRoleID
			c.NameSetByUser = true
			c.Bot = &marotte.Bot{Goal: "Answer the weekly questions.", StartedAt: 1, Memory: true}
		}
		if resumed {
			c.RecordSession("sess_" + string(chatID))
		}
		return true
	}); err != nil {
		t.Fatalf("seed %s: %v", chatID, err)
	}
}

// setModeParams is every session/set_mode the bridge was asked, by its modeId.
func setModeParams(b *fakeBridge) []any {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []any
	for _, m := range b.calls {
		if m == marotte.MethodSetMode {
			out = append(out, b.lastParams[m]["modeId"])
		}
	}
	return out
}

func modeOf(t *testing.T, cs *testChatStore, chatID marotte.ChatID) string {
	t.Helper()
	c, ok := cs.Get(t.Context(), chatID)
	if !ok {
		t.Fatalf("chat %s is gone", chatID)
	}
	return c.CurrentModeID
}

// modeErrorsSince is the mode_not_applied messages broadcast after since.
func modeErrorsSince(t *testing.T, h *Runtime, since uint64) []string {
	t.Helper()
	var out []string
	for _, p := range errorPayloadsSince(t, h, since) {
		if p.Code == marotte.ErrCodeModeNotApplied {
			out = append(out, p.Message)
		}
	}
	return out
}

// TestApplyLoadedSessionFacts_HoldsTheBotRole pins the load's writer through AdoptMode: a Bot's record stays on its
// role whatever the load reports, and a regular chat (a Bot's tangent) never takes it.
func TestApplyLoadedSessionFacts_HoldsTheBotRole(t *testing.T) {
	cases := []struct {
		name     string
		chat     marotte.Chat
		reported string
		want     string
	}{
		{"a Bot loaded in another mode", marotte.Chat{CurrentModeID: marotte.BotRoleID, Bot: &marotte.Bot{StartedAt: 1}}, marotte.DefaultModeID, marotte.BotRoleID},
		{"a tangent loaded on the role", marotte.Chat{CurrentModeID: marotte.DefaultModeID}, marotte.BotRoleID, marotte.DefaultModeID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.chat
			_, _ = applyLoadedSessionFacts(&c, &fakeBridge{currentMode: tc.reported}, "")
			if c.CurrentModeID != tc.want {
				t.Errorf("CurrentModeID = %q, want %q", c.CurrentModeID, tc.want)
			}
		})
	}
}

// TestPersistNewSessionMetadata_HoldsTheBotRole pins the new session's writer: the Bot's record stays on its role,
// and the regular chat's "pick the mode again" report, which a Bot cannot act on, is not sent.
func TestPersistNewSessionMetadata_HoldsTheBotRole(t *testing.T) {
	h, cs := botRuntime(t)
	seedModeChat(t, cs, "c1", true, false)
	since := h.bus.fanout.Position().Head

	if err := h.coord.persistNewSessionMetadata(t.Context(), "c1", reporting(marotte.DefaultModeID), firstLevel{}); err != nil {
		t.Fatalf("persist: %v", err)
	}

	if got := modeOf(t, cs, "c1"); got != marotte.BotRoleID {
		t.Errorf("CurrentModeID = %q, want %q", got, marotte.BotRoleID)
	}
	for _, msg := range modeErrorsSince(t, h, since) {
		if strings.Contains(msg, "Pick the mode again") {
			t.Errorf("a Bot was told %q", msg)
		}
	}
}

// TestBotDoor_ReassertsTheRoleOnAMismatch pins design §7.1: a Bot's session reported in another mode is sent one
// session/set_mode to the role before it serves, on both doors, and one reported on the role is sent none.
func TestBotDoor_ReassertsTheRoleOnAMismatch(t *testing.T) {
	for _, door := range doors {
		for _, tc := range []struct {
			name     string
			reported string
			want     []any
		}{
			{"reported in another mode", marotte.DefaultModeID, []any{marotte.BotRoleID}},
			{"reported on its role", marotte.BotRoleID, nil},
		} {
			t.Run(door.name+" "+tc.name, func(t *testing.T) {
				br := reporting(tc.reported, marotte.DefaultModeID, marotte.BotRoleID)
				h, cs := botRuntime(t, br)
				seedModeChat(t, cs, "c1", true, door.resumed)
				since := h.bus.fanout.Position().Head

				if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
					t.Fatalf("open the Bot: %v", err)
				}

				if got := setModeParams(br); len(got) != len(tc.want) || (len(got) == 1 && got[0] != tc.want[0]) {
					t.Errorf("session/set_mode modeIds = %v, want %v", got, tc.want)
				}
				if got := modeOf(t, cs, "c1"); got != marotte.BotRoleID {
					t.Errorf("CurrentModeID = %q, want %q", got, marotte.BotRoleID)
				}
				if got := modeErrorsSince(t, h, since); len(got) != 0 {
					t.Errorf("mode_not_applied %v after the role was applied", got)
				}
			})
		}
	}
}

// TestBotDoor_ARefusedReassertFailsTheGeneration pins that a Bot never runs a turn without its role: KAS refusing
// the re-assert fails the open with the Bot's own message and leaves the record on the role.
func TestBotDoor_ARefusedReassertFailsTheGeneration(t *testing.T) {
	for _, door := range doors {
		t.Run(door.name, func(t *testing.T) {
			br := reporting(marotte.DefaultModeID, marotte.DefaultModeID)
			br.setCallRPCErr(marotte.MethodSetMode, &marotte.RPCError{Code: -32602, Message: "Unknown mode: marotte-bot"})
			h, cs := botRuntime(t, br)
			seedModeChat(t, cs, "c1", true, door.resumed)
			since := h.bus.fanout.Position().Head

			_, err := h.coord.openReleased(t.Context(), "c1", "")

			if err == nil {
				t.Error("the Bot's session opened off its role")
			}
			// The failed prompt's bridge_start_failed reason, which the send button shows.
			if got := rpcerr.Text(err); got != botRoleMessage {
				t.Errorf("the open's error reads %q, want %q", got, botRoleMessage)
			}
			if got := modeErrorsSince(t, h, since); len(got) != 1 || got[0] != botRoleMessage {
				t.Errorf("mode_not_applied messages = %q, want exactly %q", got, botRoleMessage)
			}
			if got := modeOf(t, cs, "c1"); got != marotte.BotRoleID {
				t.Errorf("CurrentModeID = %q, want %q", got, marotte.BotRoleID)
			}
			// The failed generation's session is not the keeper's: its idle edge would send and tell again.
			if keeperHoldsASession(h, "c1") {
				t.Error("the role keeper still holds the session whose door failed")
			}
		})
	}
}

// TestBotDoor_ARoleKASCouldNotFindFailsTheGeneration pins the not_found path: KAS falls back to the default mode and
// queues _kiro/customAgent/not_found for the role; the open fails as a refused re-assert does, with no fallback
// written and no "using vibe" error beside the Bot's message.
func TestBotDoor_ARoleKASCouldNotFindFailsTheGeneration(t *testing.T) {
	br := reporting(marotte.DefaultModeID, marotte.DefaultModeID)
	notFound, _ := json.Marshal(map[string]any{
		"sessionId": "sess_c1", "requestedAgent": marotte.BotRoleID, "fallbackAgent": marotte.DefaultModeID,
	})
	br.notifsOnStart = []*marotte.RPCResponse{{JSONRPC: "2.0", Method: methodV3CustomAgentNotFound, Params: notFound}}
	br.setCallRPCErr(marotte.MethodSetMode, &marotte.RPCError{Code: -32602, Message: "Unknown mode: marotte-bot"})
	h, cs := botRuntime(t, br)
	seedModeChat(t, cs, "c1", true, true)
	since := h.bus.fanout.Position().Head

	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err == nil {
		t.Error("the Bot's session opened on the fallback mode")
	}

	if got := modeOf(t, cs, "c1"); got != marotte.BotRoleID {
		t.Errorf("CurrentModeID = %q, want %q: the fallback was persisted", got, marotte.BotRoleID)
	}
	if got := modeErrorsSince(t, h, since); len(got) != 1 || got[0] != botRoleMessage {
		t.Errorf("mode_not_applied messages = %q, want exactly %q", got, botRoleMessage)
	}
	for _, p := range errorPayloadsSince(t, h, since) {
		if p.Code == marotte.ErrCodeAgentNotFound {
			t.Errorf("agent_not_found shown beside the Bot's message: %q", p.Message)
		}
	}
}

// TestBotDoor_AReassertKASAnswersWithNotFoundFailsTheGeneration pins KAS 3.1.0's refusal as the bot_role capture
// recorded it: session/set_mode to a role the session does not hold answers {} after sending
// _kiro/customAgent/not_found for it, so the door reads the refusal off the lane, not the reply.
func TestBotDoor_AReassertKASAnswersWithNotFoundFailsTheGeneration(t *testing.T) {
	for _, door := range doors {
		t.Run(door.name, func(t *testing.T) {
			br := reporting(marotte.DefaultModeID, marotte.DefaultModeID)
			br.notifsOnCall = map[string][]*marotte.RPCResponse{marotte.MethodSetMode: {capturedNotFound(t)}}
			br.setCallResult(marotte.MethodSetMode, json.RawMessage(`{}`))
			h, cs := botRuntime(t, br)
			seedModeChat(t, cs, "c1", true, door.resumed)
			since := h.bus.fanout.Position().Head

			if _, err := h.coord.openReleased(t.Context(), "c1", ""); err == nil {
				t.Error("the Bot's session opened on the fallback mode")
			}

			if got := modeErrorsSince(t, h, since); len(got) != 1 || got[0] != botRoleMessage {
				t.Errorf("mode_not_applied messages = %q, want exactly %q", got, botRoleMessage)
			}
			if got := modeOf(t, cs, "c1"); got != marotte.BotRoleID {
				t.Errorf("CurrentModeID = %q, want %q", got, marotte.BotRoleID)
			}
		})
	}
}

// TestBotDoor_OnlyAStartedBotsRecordOpensTheRoleDoor pins that the door reads Bot-ness off the record: a record naming
// the role with no bot block, or a draft, opens as a regular chat, with no set_mode, no context file and no error.
func TestBotDoor_OnlyAStartedBotsRecordOpensTheRoleDoor(t *testing.T) {
	for _, tc := range []struct {
		name string
		bot  *marotte.Bot
	}{
		{"a record with no bot block", nil},
		{"a draft", &marotte.Bot{Goal: "Not started yet.", Memory: true}},
	} {
		for _, door := range doors {
			t.Run(tc.name+" on "+door.name, func(t *testing.T) {
				br := reporting(marotte.DefaultModeID, marotte.DefaultModeID)
				h, cs := botRuntime(t, br)
				seedModeChat(t, cs, "c1", false, door.resumed)
				if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
					c.CurrentModeID = marotte.BotRoleID
					c.Bot = tc.bot
					return true
				}); err != nil {
					t.Fatal(err)
				}
				since := h.bus.fanout.Position().Head

				if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
					t.Fatalf("open: %v", err)
				}

				if got := setModeParams(br); len(got) != 0 {
					t.Errorf("session/set_mode modeIds = %v, want none", got)
				}
				if n, p := contextCalls(br); n != 0 {
					t.Errorf("%d %s calls (last %v), want none", n, marotte.MethodSessionContext, p)
				}
				if got := modeErrorsSince(t, h, since); len(got) != 0 {
					t.Errorf("mode_not_applied %q on a chat that is no started Bot", got)
				}
			})
		}
	}
}

// modeUpdateOnPrompt makes b's prompt turns report KAS moving the session through modes, as an agent's own switch does.
func modeUpdateOnPrompt(b *fakeBridge, modes ...string) {
	b.chunksOnCall = map[string][]string{marotte.MethodPrompt: {"done"}}
	b.setOnCall(func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method != marotte.MethodPrompt {
			return nil, nil, false
		}
		var frames []*marotte.RPCResponse
		for _, mode := range modes {
			update, _ := json.Marshal(map[string]any{"sessionUpdate": "current_mode_update", "currentModeId": mode})
			params, _ := json.Marshal(map[string]any{"sessionId": b.SessionID(), "update": json.RawMessage(update)})
			frames = append(frames, &marotte.RPCResponse{JSONRPC: "2.0", Method: marotte.MethodSessionUpdate, Params: params})
		}
		return json.RawMessage(`{"stopReason":"end_turn"}`), frames, true
	})
}

// TestBotTurn_AnAgentSwitchOffTheRoleIsReassertedOnceIdle pins design §7.1's role keeper: a current_mode_update that
// moves a Bot off its role is undone with one session/set_mode once its session is idle, and none is sent when the
// session ends the turn back on the role; a regular chat's switch is its own.
func TestBotTurn_AnAgentSwitchOffTheRoleIsReassertedOnceIdle(t *testing.T) {
	for _, tc := range []struct {
		name     string
		reported []string
		bot      bool
		want     []any
	}{
		{"a Bot", []string{"kiro_spec"}, true, []any{marotte.BotRoleID}},
		{"a Bot reported on its role", []string{marotte.BotRoleID}, true, nil},
		{"a Bot switched back to its role", []string{"kiro_spec", marotte.BotRoleID}, true, nil},
		{"a regular chat", []string{"kiro_spec"}, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := marotte.DefaultModeID
			if tc.bot {
				current = marotte.BotRoleID
			}
			br := reporting(current, marotte.DefaultModeID, "kiro_spec", marotte.BotRoleID)
			modeUpdateOnPrompt(br, tc.reported...)
			h, cs := botRuntime(t, br)
			seedModeChat(t, cs, "c1", tc.bot, false)

			runPrompt(t, h, cs, "c1", "m1")
			// A second turn that reports no mode passes the keeper's gate and idle edge once more, so a set_mode owed
			// or wrongly claimed is sent by its end.
			br.setOnCall(nil)
			runPrompt(t, h, cs, "c1", "m2")

			waitFor(t, func() bool { return len(setModeParams(br)) >= len(tc.want) })
			if got := setModeParams(br); !slices.Equal(got, tc.want) {
				t.Errorf("session/set_mode modeIds after the turns = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestBotTurn_AFailedReassertReloadsTheSessionAtTheNextMessage pins design §7.1 "A Bot never runs a turn without its
// role": a re-assert KAS refuses marks the resident session for reload, so the chat's next door closes it and reloads
// through session/load, whose hold restores the role or fails the open with the Bot's message.
func TestBotTurn_AFailedReassertReloadsTheSessionAtTheNextMessage(t *testing.T) {
	switched := func(t *testing.T, reload *fakeBridge) (*Runtime, *testChatStore, *fakeBridge) {
		t.Helper()
		br := reporting(marotte.BotRoleID, marotte.DefaultModeID, "kiro_spec", marotte.BotRoleID)
		modeUpdateOnPrompt(br, "kiro_spec")
		br.setCallErr(marotte.MethodSetMode, context.DeadlineExceeded)
		h, cs := botRuntime(t, br, reload)
		seedModeChat(t, cs, "c1", true, false)
		runPrompt(t, h, cs, "c1", "m1")
		waitFor(t, func() bool { return len(setModeParams(br)) == 1 && keeperIs(h, "c1", roleFailed) })
		waitNoHold(t, h, "c1")
		return h, cs, br
	}

	t.Run("the reload restores the role", func(t *testing.T) {
		reload := reporting("kiro_spec", marotte.DefaultModeID, "kiro_spec", marotte.BotRoleID)
		reload.chunksOnCall = map[string][]string{marotte.MethodPrompt: {"done"}}
		h, cs, br := switched(t, reload)
		sessionID := botSessionID(t, cs, "c1")

		runPrompt(t, h, cs, "c1", "m2")

		if !sessionClosed(br) {
			t.Error("the session left off the role was kept for the next message")
		}
		if n := countCalls(br, marotte.MethodPrompt); n != 1 {
			t.Errorf("%d prompts reached the session left off the role, want only the switching turn's", n)
		}
		if opts := reload.lastSessionOpts(); opts == nil || opts.SessionID != sessionID {
			t.Fatalf("the next message's door sent %+v, want a session/load of %q", opts, sessionID)
		}
		if got := setModeParams(reload); !slices.Equal(got, []any{marotte.BotRoleID}) {
			t.Errorf("session/set_mode modeIds on the reload = %v, want [%s]", got, marotte.BotRoleID)
		}
		if n := countCalls(reload, marotte.MethodPrompt); n != 1 {
			t.Errorf("%d prompts reached the reloaded session, want 1", n)
		}
	})

	t.Run("the reload is refused", func(t *testing.T) {
		reload := reporting("kiro_spec", marotte.DefaultModeID, "kiro_spec")
		reload.setCallRPCErr(marotte.MethodSetMode, &marotte.RPCError{Code: -32602, Message: "Unknown mode: marotte-bot"})
		h, _, br := switched(t, reload)

		_, err := h.coord.openReleased(t.Context(), "c1", "")

		if !sessionClosed(br) {
			t.Error("the session left off the role was kept for the next message")
		}
		if got := rpcerr.Text(err); got != botRoleMessage {
			t.Errorf("the next door's error reads %q, want %q", got, botRoleMessage)
		}
	})
}

// TestBotTurn_OnlyAStartedBotsSwitchIsReasserted pins that the role keeper holds only a started Bot's session: a
// draft's session never registered the role, so its agent's switch is its own.
func TestBotTurn_OnlyAStartedBotsSwitchIsReasserted(t *testing.T) {
	br := reporting(marotte.DefaultModeID, marotte.DefaultModeID, "kiro_spec")
	modeUpdateOnPrompt(br, "kiro_spec")
	h, cs := botRuntime(t, br)
	seedModeChat(t, cs, "c1", false, false)
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Bot = &marotte.Bot{Goal: "Not started yet."}
		return true
	}); err != nil {
		t.Fatal(err)
	}

	runPrompt(t, h, cs, "c1", "m1")

	if got := setModeParams(br); len(got) != 0 {
		t.Errorf("session/set_mode modeIds after a draft's switch = %v, want none", got)
	}
}

// waitNoHold waits until no hold on chatID's life refuses a close: runPrompt returns before the turn's close tail
// ends, and a door opened directly under that tail keeps the session.
func waitNoHold(t *testing.T, h *Runtime, chatID marotte.ChatID) {
	t.Helper()
	waitFor(t, func() bool {
		lc, ok := h.coord.turns.lookup(chatID)
		if !ok {
			return true
		}
		lc.mu.Lock()
		defer lc.mu.Unlock()
		return lc.blockerLocked(0) == holdKinds
	})
}

// TestPersistNewSessionMetadata_ADraftIsToldItsModeDidNotApply pins that only a started Bot's door reports its own
// mode (holdBotRole): a draft's new session reporting another mode than the one asked gets the regular toast.
func TestPersistNewSessionMetadata_ADraftIsToldItsModeDidNotApply(t *testing.T) {
	br := reporting("kiro_spec", marotte.DefaultModeID, "kiro_spec")
	h, cs := botRuntime(t, br)
	seedModeChat(t, cs, "c1", false, false)
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.CurrentModeID = marotte.BotRoleID
		c.Bot = &marotte.Bot{Goal: "Not started yet."}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	since := h.bus.fanout.Position().Head

	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("open: %v", err)
	}

	if got := modeErrorsSince(t, h, since); len(got) != 1 || got[0] == botRoleMessage {
		t.Errorf("mode_not_applied messages = %q, want the regular chat's one", got)
	}
}

// lastCall is the position of b's last call to method among all its calls, -1 for none.
func lastCall(b *fakeBridge, method string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, m := range slices.Backward(b.calls) {
		if m == method {
			return i
		}
	}
	return -1
}

// TestBotRole_ANotFoundBeforeTheSetModeIsNotItsRefusal pins that only a not_found KAS sends for the set_mode itself
// fails it: one replayed on the load, or folded during the turn, is stale once KAS accepts the role.
func TestBotRole_ANotFoundBeforeTheSetModeIsNotItsRefusal(t *testing.T) {
	t.Run("replayed on the load", func(t *testing.T) {
		br := reporting(marotte.DefaultModeID, marotte.DefaultModeID, marotte.BotRoleID)
		br.notifsOnStart = []*marotte.RPCResponse{capturedNotFound(t)}
		br.setCallResult(marotte.MethodSetMode, json.RawMessage(`{}`))
		h, cs := botRuntime(t, br)
		seedModeChat(t, cs, "c1", true, true)
		since := h.bus.fanout.Position().Head

		if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
			t.Fatalf("the Bot did not open: %v", err)
		}

		if got := setModeParams(br); !slices.Equal(got, []any{marotte.BotRoleID}) {
			t.Errorf("session/set_mode modeIds = %v, want [%s]", got, marotte.BotRoleID)
		}
		if got := modeErrorsSince(t, h, since); len(got) != 0 {
			t.Errorf("mode_not_applied %q after KAS accepted the role", got)
		}
	})

	t.Run("folded during the turn", func(t *testing.T) {
		br := reporting(marotte.BotRoleID, marotte.DefaultModeID, "kiro_spec", marotte.BotRoleID)
		notFound := capturedNotFound(t)
		modeUpdateOnPrompt(br, "kiro_spec")
		inner := br.onCall
		br.setOnCall(func(method string, params map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
			result, frames, ok := inner(method, params)
			if ok {
				frames = append([]*marotte.RPCResponse{notFound}, frames...)
			}
			return result, frames, ok
		})
		h, cs := botRuntime(t, br)
		seedModeChat(t, cs, "c1", true, false)

		runPrompt(t, h, cs, "c1", "m1")
		waitFor(t, func() bool { return len(setModeParams(br)) == 1 })
		waitFor(t, func() bool { return keeperIs(h, "c1", roleOn) })
		first := h.bridge.mgr.get("c1")

		if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
			t.Fatalf("the next door failed: %v", err)
		}

		if sessionClosed(br) || h.bridge.mgr.get("c1") != first {
			t.Error("an accepted re-assert was read as refused: the session was reloaded")
		}
	})
}

type capturedReport struct {
	Method         string                `json:"method"`
	CurrentModeID  string                `json:"currentModeId"`
	AvailableModes []marotte.SessionMode `json:"availableModes"`
}

// capturedReports are the door reports the bot_role capture recorded, in order: the regular chat's session/new, the
// Bot's session/new, then its reopens.
func capturedReports(t *testing.T) []capturedReport {
	t.Helper()
	var out []capturedReport
	for _, l := range loadKASCorpusLines(t, "bot_role") {
		if l.Dir != "report" {
			continue
		}
		var r capturedReport
		if err := json.Unmarshal(l.Frame, &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if len(out) < 3 {
		t.Fatalf("the bot_role capture holds %d door reports, want the regular chat's and the Bot's new and load", len(out))
	}
	for i, want := range []string{marotte.MethodSessionNew, marotte.MethodSessionNew, marotte.MethodSessionLoad} {
		if out[i].Method != want {
			t.Fatalf("the bot_role capture's door report %d is %s, want %s", i, out[i].Method, want)
		}
	}
	return out
}

// capturedNotFound is the _kiro/customAgent/not_found KAS sent for the role in the bot_role capture.
func capturedNotFound(t *testing.T) *marotte.RPCResponse {
	t.Helper()
	for _, l := range loadKASCorpusLines(t, "bot_role") {
		if l.Dir == "in" && l.frame(t).Method == methodV3CustomAgentNotFound {
			return &marotte.RPCResponse{JSONRPC: "2.0", Method: methodV3CustomAgentNotFound, Params: l.frame(t).Params}
		}
	}
	t.Fatal("the bot_role capture holds no not_found")
	return nil
}

func reportingCaptured(r capturedReport) *fakeBridge {
	b := newFakeBridge()
	b.currentMode = r.CurrentModeID
	b.modes = r.AvailableModes
	return b
}

func catalogVersion(h *Runtime) string {
	_, _, stamp := h.catalog.modesModelsStamped()
	if stamp == nil {
		return ""
	}
	return stamp.Version
}

// TestBotSession_TheRoleNeverReachesTheWorkspaceCatalog pins design §7.4 (P1b) through both writers, fed the modes
// KAS 3.1.0 reported in the bot_role capture: a Bot's session/new and its session/load list the role, and every chat's
// pill still lists no marotte-bot, and the catalog version a regular chat's open set does not move.
func TestBotSession_TheRoleNeverReachesTheWorkspaceCatalog(t *testing.T) {
	reports := capturedReports(t)
	for i, door := range doors {
		t.Run(door.name, func(t *testing.T) {
			regular := reportingCaptured(reports[0])
			bot := reportingCaptured(reports[1+i])
			h, cs := botRuntime(t, regular, bot)
			seedModeChat(t, cs, "c0", false, false)
			seedModeChat(t, cs, "c1", true, door.resumed)

			if _, err := h.coord.openReleased(t.Context(), "c0", ""); err != nil {
				t.Fatalf("open the regular chat: %v", err)
			}
			before := catalogVersion(h)
			if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
				t.Fatalf("open the Bot: %v", err)
			}

			for _, m := range serveConfigTemplate(t, h).Modes {
				if m.ID == marotte.BotRoleID {
					t.Errorf("GET /api/config-template lists %+v", m)
				}
			}
			if after := catalogVersion(h); after != before {
				t.Errorf("catalog version moved %q -> %q on a Bot's open", before, after)
			}
		})
	}
}
