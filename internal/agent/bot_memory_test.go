package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// botMemoryMessage is the warning a Bot whose memory file did not load shows (design §7.1, "The memory file").
const botMemoryMessage = "This Bot's memory did not load, so it is working without its notes."

func contextCalls(b *fakeBridge) (n int, last map[string]any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, m := range b.calls {
		if m == marotte.MethodSessionContext {
			n++
		}
	}
	return n, b.lastParams[marotte.MethodSessionContext]
}

func memoryErrorsSince(t *testing.T, h *Runtime, since uint64) []string {
	t.Helper()
	var out []string
	for _, p := range errorPayloadsSince(t, h, since) {
		if p.Code == marotte.ErrCodeMemoryNotLoaded {
			out = append(out, p.Message)
		}
	}
	return out
}

func setBotMemory(t *testing.T, cs *testChatStore, chatID marotte.ChatID, on bool) {
	t.Helper()
	if _, err := cs.Mutate(t.Context(), chatID, func(c *marotte.Chat, _ bool) bool {
		c.Bot.Memory = on
		return true
	}); err != nil {
		t.Fatal(err)
	}
}

func botSessionID(t *testing.T, cs *testChatStore, chatID marotte.ChatID) string {
	t.Helper()
	c, ok := cs.Get(t.Context(), chatID)
	if !ok {
		t.Fatalf("chat %s is gone", chatID)
	}
	return c.ACPSessionID
}

// TestBotDoor_LoadsTheMemoryFileAsAContextFile pins design §7.1 "The memory file": with memory on, each door adds the
// Bot's own file under KIRO_HOME to the session's context files, by force so a file not written yet loads once it is.
func TestBotDoor_LoadsTheMemoryFileAsAContextFile(t *testing.T) {
	for _, door := range doors {
		t.Run(door.name, func(t *testing.T) {
			br := reporting(marotte.BotRoleID, marotte.DefaultModeID, marotte.BotRoleID)
			h, cs := botRuntime(t, br)
			seedModeChat(t, cs, "c1", true, door.resumed)
			since := h.bus.fanout.Position().Head

			if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
				t.Fatalf("open the Bot: %v", err)
			}

			n, p := contextCalls(br)
			if n != 1 {
				t.Fatalf("%d %s calls, want 1", n, marotte.MethodSessionContext)
			}
			home := filepath.Join(homeOf(t), ".kiro")
			want := map[string]any{
				"sessionId": botSessionID(t, cs, "c1"), "subcommand": "add",
				"path": filepath.Join(home, "bots", "c1", "memory.md"), "force": true,
			}
			if !jsonEqual(t, p, want) {
				t.Errorf("params = %v, want %v", p, want)
			}
			if got := memoryErrorsSince(t, h, since); len(got) != 0 {
				t.Errorf("memory_not_loaded %q after a loaded file", got)
			}
		})
	}
}

// TestBotDoor_MemoryOffDropsTheFileOnALoad pins the off half: KAS keeps a session's context files across a load, so a
// load with memory off removes the file, and a new session, which holds none, is sent nothing.
func TestBotDoor_MemoryOffDropsTheFileOnALoad(t *testing.T) {
	for _, door := range doors {
		t.Run(door.name, func(t *testing.T) {
			br := reporting(marotte.BotRoleID, marotte.DefaultModeID, marotte.BotRoleID)
			h, cs := botRuntime(t, br)
			seedModeChat(t, cs, "c1", true, door.resumed)
			setBotMemory(t, cs, "c1", false)

			if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
				t.Fatalf("open the Bot: %v", err)
			}

			n, p := contextCalls(br)
			if !door.resumed {
				if n != 0 {
					t.Errorf("%d %s calls on a new session with memory off, want none", n, marotte.MethodSessionContext)
				}
				return
			}
			home := filepath.Join(homeOf(t), ".kiro")
			want := map[string]any{
				"sessionId": botSessionID(t, cs, "c1"), "subcommand": "remove",
				"path": filepath.Join(home, "bots", "c1", "memory.md"),
			}
			if n != 1 || !jsonEqual(t, p, want) {
				t.Errorf("%d calls, last %v; want one %v", n, p, want)
			}
		})
	}
}

// TestChatDoor_OnlyAStartedBotTouchesContextFiles pins that the memory file never reaches another chat's session: a
// regular chat (a Bot's tangent among them) and a draft Bot send no context call on either door.
func TestChatDoor_OnlyAStartedBotTouchesContextFiles(t *testing.T) {
	for _, door := range doors {
		for _, kind := range []string{"a regular chat", "a draft Bot"} {
			t.Run(door.name+" "+kind, func(t *testing.T) {
				br := reporting(marotte.DefaultModeID, marotte.DefaultModeID)
				h, cs := botRuntime(t, br)
				seedModeChat(t, cs, "c1", false, door.resumed)
				if kind == "a draft Bot" {
					if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
						c.Bot = &marotte.Bot{Goal: "Answer the weekly questions.", Memory: true}
						return true
					}); err != nil {
						t.Fatal(err)
					}
				}

				if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
					t.Fatalf("open: %v", err)
				}

				if n, p := contextCalls(br); n != 0 {
					t.Errorf("%d %s calls (last %v), want none", n, marotte.MethodSessionContext, p)
				}
			})
		}
	}
}

// TestBotDoor_AMemoryFileThatWillNotLoadWarnsAndOpens pins the failure arm: the add is tried twice, and a second
// failure opens the Bot anyway and says in its chat that it runs without its notes.
func TestBotDoor_AMemoryFileThatWillNotLoadWarnsAndOpens(t *testing.T) {
	cases := []struct {
		name  string
		fail  func(b *fakeBridge, calls *int)
		calls int
		warns []string
	}{
		{"refused twice", func(b *fakeBridge, _ *int) {
			b.setCallRPCErr(marotte.MethodSessionContext, &marotte.RPCError{Code: -32603, Message: "Internal error"})
		}, 2, []string{botMemoryMessage}},
		{"answered unsuccessful twice", func(b *fakeBridge, _ *int) {
			b.setCallResult(marotte.MethodSessionContext, json.RawMessage(`{"success":false,"message":"Session sess_c1 not found","entries":[]}`))
		}, 2, []string{botMemoryMessage}},
		{"refused once", func(b *fakeBridge, calls *int) {
			var mu sync.Mutex
			b.onCallFails = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool, error) {
				if method != marotte.MethodSessionContext {
					return nil, nil, false, nil
				}
				mu.Lock()
				defer mu.Unlock()
				*calls++
				if *calls == 1 {
					return nil, nil, false, context.DeadlineExceeded
				}
				return json.RawMessage(`{"success":true,"message":"added"}`), nil, true, nil
			}
		}, 2, nil},
	}
	for _, door := range doors {
		for _, tc := range cases {
			t.Run(door.name+" "+tc.name, func(t *testing.T) {
				br := reporting(marotte.BotRoleID, marotte.DefaultModeID, marotte.BotRoleID)
				var seen int
				tc.fail(br, &seen)
				h, cs := botRuntime(t, br)
				seedModeChat(t, cs, "c1", true, door.resumed)
				since := h.bus.fanout.Position().Head

				if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
					t.Fatalf("the Bot did not open: %v", err)
				}

				if n, _ := contextCalls(br); n != tc.calls {
					t.Errorf("%d %s calls, want %d", n, marotte.MethodSessionContext, tc.calls)
				}
				if got := memoryErrorsSince(t, h, since); !slices.Equal(got, tc.warns) {
					t.Errorf("memory_not_loaded messages = %q, want %q", got, tc.warns)
				}
			})
		}
	}
}

// TestBotDoor_AFailedRemoveOpensWithNoWarning pins that memory off never warns: a remove that fails twice leaves the
// open going, and a Bot told it has no memory is told nothing else.
func TestBotDoor_AFailedRemoveOpensWithNoWarning(t *testing.T) {
	br := reporting(marotte.BotRoleID, marotte.DefaultModeID, marotte.BotRoleID)
	br.setCallRPCErr(marotte.MethodSessionContext, &marotte.RPCError{Code: -32603, Message: "Internal error"})
	h, cs := botRuntime(t, br)
	seedModeChat(t, cs, "c1", true, true)
	setBotMemory(t, cs, "c1", false)
	since := h.bus.fanout.Position().Head

	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("the Bot did not open: %v", err)
	}

	if n, _ := contextCalls(br); n != 2 {
		t.Errorf("%d %s calls, want 2", n, marotte.MethodSessionContext)
	}
	if got := memoryErrorsSince(t, h, since); len(got) != 0 {
		t.Errorf("memory_not_loaded %q with memory off", got)
	}
}

// TestBotDoor_AMemoryFileOutsideItsFolderIsNeverSent pins design §7.1's path rule: a KIRO_HOME that is not absolute
// would let KAS resolve the file inside the workspace, so the door sends no path and warns.
func TestBotDoor_AMemoryFileOutsideItsFolderIsNeverSent(t *testing.T) {
	br := reporting(marotte.BotRoleID, marotte.DefaultModeID, marotte.BotRoleID)
	h, cs := botRuntime(t, br)
	seedModeChat(t, cs, "c1", true, false)
	t.Setenv("HOME", "relative-home")
	since := h.bus.fanout.Position().Head

	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("the Bot did not open: %v", err)
	}

	if n, p := contextCalls(br); n != 0 {
		t.Errorf("%d %s calls (last %v), want none", n, marotte.MethodSessionContext, p)
	}
	if got := memoryErrorsSince(t, h, since); !slices.Equal(got, []string{botMemoryMessage}) {
		t.Errorf("memory_not_loaded messages = %q, want %q", got, botMemoryMessage)
	}
}

func homeOf(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	return home
}

func jsonEqual(t *testing.T, got, want map[string]any) bool {
	t.Helper()
	a, _ := json.Marshal(got)
	b, _ := json.Marshal(want)
	return string(a) == string(b)
}
