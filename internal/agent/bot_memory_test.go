package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// botMemoryMessage is the warning a Bot whose memory file did not load shows.
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

// TestBotDoor_LoadsTheMemoryFileAsAContextFile pins that with memory on, each door adds the Bot's own file under
// KIRO_HOME to the session's context files, by force so a file not written yet loads once it is.
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

// TestBotDoor_AMemoryFileOutsideItsFolderIsNeverSent pins that a KIRO_HOME that is not absolute would let KAS resolve
// the file inside the workspace, so the door sends no path and warns.
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

// TestBotDoor_MemoryOffWithNoPathToRemoveWarns pins the load with memory off whose file cannot be named: no remove is
// sent, the chat is told nothing (its Bot has no memory), and the log says KAS may keep the entry.
func TestBotDoor_MemoryOffWithNoPathToRemoveWarns(t *testing.T) {
	logs := captureLogs(t)
	br := reporting(marotte.BotRoleID, marotte.DefaultModeID, marotte.BotRoleID)
	h, cs := botRuntime(t, br)
	seedModeChat(t, cs, "c1", true, true)
	setBotMemory(t, cs, "c1", false)
	t.Setenv("HOME", "relative-home")
	since := h.bus.fanout.Position().Head

	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("the Bot did not open: %v", err)
	}

	if n, p := contextCalls(br); n != 0 {
		t.Errorf("%d %s calls (last %v), want none", n, marotte.MethodSessionContext, p)
	}
	if got := memoryErrorsSince(t, h, since); len(got) != 0 {
		t.Errorf("memory_not_loaded %q with memory off", got)
	}
	var kept bool
	for line := range strings.Lines(logs.String()) {
		kept = kept || strings.Contains(line, `"level":"WARN"`) && strings.Contains(line, "may still hold its memory file") &&
			strings.Contains(line, `"chat_id":"c1"`)
	}
	if !kept {
		t.Errorf("no warning that the session may still hold its memory file; logs:\n%s", logs.String())
	}
}

// startingMemory is the text a new memory file starts with.
const startingMemory = "# Memory\n\nYour notes for future turns. Keep this file under 40,000 characters.\n"

// botMemoryHome is the test HOME's KIRO_HOME, created as kiro-cli and marotte's boot steering leave it, and c1's
// memory file under it.
func botMemoryHome(t *testing.T) (kiroHome, file string) {
	t.Helper()
	kiroHome = filepath.Join(homeOf(t), ".kiro")
	if err := os.MkdirAll(kiroHome, 0o755); err != nil {
		t.Fatal(err)
	}
	return kiroHome, filepath.Join(kiroHome, "bots", "c1", "memory.md")
}

func permOf(t *testing.T, path string) (os.FileMode, bool) {
	t.Helper()
	fi, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return 0, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm(), true
}

// TestBotDoor_MemoryOnCreatesTheFile pins that a started Bot with memory on opens with its directory (0700) and memory
// file (0600) in place, the starting text in a new file and the notes kept in one that exists.
func TestBotDoor_MemoryOnCreatesTheFile(t *testing.T) {
	for _, door := range doors {
		for _, notes := range []string{"", "- the backlog is at seven\n"} {
			t.Run(door.name+" with notes "+strconv.Quote(notes), func(t *testing.T) {
				h, cs := botRuntime(t, reporting(marotte.BotRoleID, marotte.DefaultModeID, marotte.BotRoleID))
				_, file := botMemoryHome(t)
				want := startingMemory
				if notes != "" {
					want = notes
					if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(file, []byte(notes), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				seedModeChat(t, cs, "c1", true, door.resumed)

				if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
					t.Fatalf("open the Bot: %v", err)
				}

				if got, err := os.ReadFile(file); err != nil || string(got) != want {
					t.Errorf("memory.md = %q, %v; want %q", got, err, want)
				}
				if mode, ok := permOf(t, file); !ok || mode != 0o600 {
					t.Errorf("memory.md mode = %o (exists %v), want 600", mode, ok)
				}
				if mode, ok := permOf(t, filepath.Dir(file)); !ok || mode != 0o700 {
					t.Errorf("the Bot's directory mode = %o (exists %v), want 700", mode, ok)
				}
			})
		}
	}
}

// TestBotDoor_NoMemoryCreatesNothingAndKeepsTheNotes pins that a Bot with memory off, a draft and a regular chat open
// with no memory file made, and memory off keeps the notes a Bot already wrote.
func TestBotDoor_NoMemoryCreatesNothingAndKeepsTheNotes(t *testing.T) {
	for _, door := range doors {
		for _, tc := range []struct {
			name string
			bot  *marotte.Bot
		}{
			{"a Bot with memory off", &marotte.Bot{Goal: "Answer the weekly questions.", StartedAt: 1}},
			{"a draft with memory on", &marotte.Bot{Goal: "Answer the weekly questions.", Memory: true}},
			{"a regular chat", nil},
		} {
			t.Run(door.name+" "+tc.name, func(t *testing.T) {
				h, cs := botRuntime(t, reporting(marotte.BotRoleID, marotte.DefaultModeID, marotte.BotRoleID))
				kiroHome, _ := botMemoryHome(t)
				seedModeChat(t, cs, "c1", tc.bot != nil && tc.bot.StartedAt != 0, door.resumed)
				if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
					c.Bot = tc.bot
					return true
				}); err != nil {
					t.Fatal(err)
				}

				if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
					t.Fatalf("open: %v", err)
				}

				if _, ok := permOf(t, filepath.Join(kiroHome, "bots")); ok {
					t.Error("bots/ was created for a chat with no memory")
				}
			})
		}
		t.Run(door.name+" memory off keeps the notes", func(t *testing.T) {
			h, cs := botRuntime(t, reporting(marotte.BotRoleID, marotte.DefaultModeID, marotte.BotRoleID))
			_, file := botMemoryHome(t)
			if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte("- the backlog is at seven\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			seedModeChat(t, cs, "c1", true, door.resumed)
			setBotMemory(t, cs, "c1", false)

			if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
				t.Fatalf("open the Bot: %v", err)
			}

			if got, err := os.ReadFile(file); err != nil || string(got) != "- the backlog is at seven\n" {
				t.Errorf("memory.md = %q, %v; want the notes kept", got, err)
			}
		})
	}
}

// TestBotDoor_ALinkedDirectoryIsNotWrittenThrough pins the link refusal at the door: a link where the Bot's directory
// goes is refused, nothing lands at its target, the session is not told to load what it points at (a loaded one, which
// may hold the file from an earlier open, is told to drop it), the chat is told the memory did not load, and the Bot
// still opens.
func TestBotDoor_ALinkedDirectoryIsNotWrittenThrough(t *testing.T) {
	for _, door := range doors {
		t.Run(door.name, func(t *testing.T) {
			br := reporting(marotte.BotRoleID, marotte.DefaultModeID, marotte.BotRoleID)
			h, cs := botRuntime(t, br)
			kiroHome, file := botMemoryHome(t)
			target := filepath.Join(kiroHome, "steering")
			if err := os.MkdirAll(target, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(kiroHome, "bots"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Dir(file)); err != nil {
				t.Fatal(err)
			}
			seedModeChat(t, cs, "c1", true, door.resumed)
			since := h.bus.fanout.Position().Head

			if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
				t.Fatalf("open the Bot: %v", err)
			}

			if entries, _ := os.ReadDir(target); len(entries) != 0 {
				t.Errorf("the link's target holds %v, want nothing written there", entries)
			}
			n, p := contextCalls(br)
			switch {
			case !door.resumed && n != 0:
				t.Errorf("%d %s calls on a new session, want none for a linked directory", n, marotte.MethodSessionContext)
			case door.resumed && (n != 1 || !jsonEqual(t, p, removeMemory(t, cs, file))):
				t.Errorf("%d calls, last %v; want one %v", n, p, removeMemory(t, cs, file))
			}
			if got := memoryErrorsSince(t, h, since); !slices.Equal(got, []string{botMemoryMessage}) {
				t.Errorf("memory_not_loaded messages = %q, want %q", got, []string{botMemoryMessage})
			}
		})
	}
}

// TestBotDoor_ALinkSwappedInBeforeAReloadIsDropped pins the reload half of the link refusal: KAS keeps the context
// files a session held, so a Bot opened on its real memory file, whose file is then swapped for a link, is told on the
// next load to drop the file and never to add it, and the link's target is left as it was.
func TestBotDoor_ALinkSwappedInBeforeAReloadIsDropped(t *testing.T) {
	logs := captureLogs(t)
	first := reporting(marotte.BotRoleID, marotte.DefaultModeID, marotte.BotRoleID)
	second := reporting(marotte.BotRoleID, marotte.DefaultModeID, marotte.BotRoleID)
	h, cs := botRuntime(t, first, second)
	kiroHome, file := botMemoryHome(t)
	seedModeChat(t, cs, "c1", true, true)

	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("open the Bot: %v", err)
	}
	if n, p := contextCalls(first); n != 1 || p["subcommand"] != "add" {
		t.Fatalf("the first open sent %d calls, last %v; want one add", n, p)
	}
	h.coord.closeBridge(t.Context(), "c1", marotte.TurnOutcomeInterrupted)
	joinInflight(t, h)

	target := filepath.Join(kiroHome, "elsewhere.md")
	if err := os.WriteFile(target, []byte("not the Bot's notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, file); err != nil {
		t.Fatal(err)
	}
	since := h.bus.fanout.Position().Head

	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("reopen the Bot: %v", err)
	}

	if n, p := contextCalls(second); n != 1 || !jsonEqual(t, p, removeMemory(t, cs, file)) {
		t.Errorf("the reload sent %d calls, last %v; want one %v", n, p, removeMemory(t, cs, file))
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "not the Bot's notes\n" {
		t.Errorf("the link's target = %q, %v; want it untouched", got, err)
	}
	if got := memoryErrorsSince(t, h, since); !slices.Equal(got, []string{botMemoryMessage}) {
		t.Errorf("memory_not_loaded messages = %q, want %q", got, []string{botMemoryMessage})
	}
	if strings.Contains(logs.String(), "kept a memory file") {
		t.Errorf("a remove KAS accepted was logged as kept; logs:\n%s", logs.String())
	}
}

// TestBotDoor_AFailedDropOfARefusedLinkIsLogged pins that a loaded session refusing to drop a memory file swapped for a
// link is tried twice and logged, beside the chat's warning that the memory did not load.
func TestBotDoor_AFailedDropOfARefusedLinkIsLogged(t *testing.T) {
	logs := captureLogs(t)
	br := reporting(marotte.BotRoleID, marotte.DefaultModeID, marotte.BotRoleID)
	br.setCallRPCErr(marotte.MethodSessionContext, &marotte.RPCError{Code: -32603, Message: "Internal error"})
	h, cs := botRuntime(t, br)
	kiroHome, file := botMemoryHome(t)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(kiroHome, "elsewhere.md"), file); err != nil {
		t.Fatal(err)
	}
	seedModeChat(t, cs, "c1", true, true)

	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("the Bot did not open: %v", err)
	}

	if n, p := contextCalls(br); n != 2 || p["subcommand"] != "remove" {
		t.Errorf("%d calls, last %v; want the remove tried twice", n, p)
	}
	var kept bool
	for line := range strings.Lines(logs.String()) {
		kept = kept || strings.Contains(line, `"level":"WARN"`) && strings.Contains(line, "kept a memory file") &&
			strings.Contains(line, `"chat_id":"c1"`)
	}
	if !kept {
		t.Errorf("no warning that the session kept the memory file; logs:\n%s", logs.String())
	}
}

// removeMemory is the context remove that drops c1's memory file from its current session.
func removeMemory(t *testing.T, cs *testChatStore, file string) map[string]any {
	t.Helper()
	return map[string]any{"sessionId": botSessionID(t, cs, "c1"), "subcommand": "remove", "path": file}
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
