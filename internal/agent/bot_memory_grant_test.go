package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

const botNotes = "# Memory\n\nThe weekly answers are due on Friday.\n"

// botGrantFixture is a KIRO_HOME under a test HOME holding Bot c1's directory and memory file, and a hub whose chat c1
// is a started Bot with memory on, running on sess_bot after sess_bot_old. c2 is another such Bot, c3 a regular chat,
// c4 a tangent forked from c1, and sess_step a step of a run c1 launched.
type botGrantFixture struct {
	h    *Runtime
	br   *respondingBridge
	home string
	dir  string
	file string
}

func newBotGrantFixture(t *testing.T) *botGrantFixture {
	t.Helper()
	tmp := canonDir(t, t.TempDir())
	t.Setenv("HOME", tmp)
	f := &botGrantFixture{home: filepath.Join(tmp, ".kiro")}
	f.dir = filepath.Join(f.home, "bots", "c1")
	f.file = filepath.Join(f.dir, "memory.md")
	for _, d := range []string{f.dir, filepath.Join(f.home, "bots", "c2")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "memory.md"), []byte(botNotes), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f.h, f.br = hubForFSTest(t, canonDir(t, t.TempDir()))
	f.edit(t, "c1", func(c *marotte.Chat) {
		c.Bot = &marotte.Bot{Goal: "Answer the weekly questions.", StartedAt: 1, Memory: true}
		c.ACPSessionID, c.PriorACPSessionIDs = "sess_bot", []string{"sess_bot_old"}
	})
	f.edit(t, "c2", func(c *marotte.Chat) {
		c.Name = "Other Bot"
		c.Bot = &marotte.Bot{Goal: "Watch the build.", StartedAt: 1, Memory: true}
		c.ACPSessionID = "sess_other_bot"
	})
	f.edit(t, "c3", func(c *marotte.Chat) { c.Name = "Regular"; c.ACPSessionID = "sess_chat" })
	f.edit(t, "c4", func(c *marotte.Chat) { c.Name = "Tangent"; c.Tangent = true; c.ACPSessionID = "sess_tangent" })
	f.h.router.seedSteps("wf_bot", []string{"sess_step"})
	return f
}

func (f *botGrantFixture) edit(t *testing.T, chatID marotte.ChatID, write func(c *marotte.Chat)) {
	t.Helper()
	if _, err := f.h.chatStore.Mutate(t.Context(), chatID, func(c *marotte.Chat, _ bool) bool {
		write(c)
		return true
	}); err != nil {
		t.Fatal(err)
	}
}

// do sends one fs request on chatID's routing, as the engine lane delivers it, and answers its result.
func (f *botGrantFixture) do(t *testing.T, chatID marotte.ChatID, method, session, path string, extra map[string]any) (any, error) {
	t.Helper()
	in := f.h.inbound
	handlers := map[string]func(context.Context, marotte.ChatID, acpResponder, *marotte.RPCResponse){
		marotte.MethodFSRead:      in.respondFSRead,
		marotte.MethodFSWrite:     in.respondFSWrite,
		methodKiroFSStat:          in.respondKiroFSStat,
		methodKiroFSReadDirectory: in.respondKiroFSReadDirectory,
		methodKiroFSDelete:        in.respondKiroFSDelete,
	}
	handlers[method](t.Context(), chatID, f.h.originOf("c1"), fsMsg(t, method, session, path, extra))
	<-f.br.done
	return f.br.response.result, f.br.response.err
}

func (f *botGrantFixture) read(t *testing.T, chatID marotte.ChatID, session, path string) (string, error) {
	t.Helper()
	res, err := f.do(t, chatID, marotte.MethodFSRead, session, path, nil)
	body, _ := res.(map[string]any)
	content, _ := body["content"].(string)
	return content, err
}

func (f *botGrantFixture) write(t *testing.T, chatID marotte.ChatID, session, path, content string) error {
	t.Helper()
	_, err := f.do(t, chatID, marotte.MethodFSWrite, session, path, map[string]any{"content": content})
	return err
}

// TestBotMemoryGrant_TheBotsOwnSessionsReadAndWriteItsDirectory pins that a session in the Bot's chain, current or
// prior, reaches its directory through every marotte file verb.
func TestBotMemoryGrant_TheBotsOwnSessionsReadAndWriteItsDirectory(t *testing.T) {
	for _, session := range []string{"sess_bot", "sess_bot_old"} {
		t.Run("a read from "+session, func(t *testing.T) {
			f := newBotGrantFixture(t)
			if got, err := f.read(t, "c1", session, f.file); err != nil || got != botNotes {
				t.Errorf("read of its memory file = %q, %v; want its notes", got, err)
			}
		})
		t.Run("a write from "+session, func(t *testing.T) {
			f := newBotGrantFixture(t)
			if err := f.write(t, "c1", session, f.file, "# Memory\n\nAnswered week 12.\n"); err != nil {
				t.Errorf("write of its memory file = %v, want nil", err)
			}
			if got, err := os.ReadFile(f.file); err != nil || string(got) != "# Memory\n\nAnswered week 12.\n" {
				t.Errorf("the memory file after the write = %q, %v; want the written notes", got, err)
			}
			if info, err := os.Stat(f.file); err != nil || info.Mode().Perm() != 0o600 {
				t.Errorf("the memory file's mode after the write = %v, %v; want 0600 kept", info.Mode().Perm(), err)
			}
		})
		t.Run("a stat from "+session, func(t *testing.T) {
			f := newBotGrantFixture(t)
			res, err := f.do(t, "c1", methodKiroFSStat, session, f.file, nil)
			if body, ok := res.(kiroStatBody); err != nil || !ok || body.Type != fsTypeFile || body.Size != int64(len(botNotes)) {
				t.Errorf("stat of its memory file = %+v, %v; want a file of %d bytes", res, err, len(botNotes))
			}
		})
		t.Run("a listing from "+session, func(t *testing.T) {
			f := newBotGrantFixture(t)
			res, err := f.do(t, "c1", methodKiroFSReadDirectory, session, f.dir, nil)
			body, ok := res.(kiroReadDirBody)
			if err != nil || !ok || !slices.Equal(body.Entries, []kiroDirEntry{{Name: "memory.md", Type: fsTypeFile}}) {
				t.Errorf("read_directory of its directory = %+v, %v; want memory.md", res, err)
			}
		})
	}
	t.Run("a new file in its directory, then its delete", func(t *testing.T) {
		f := newBotGrantFixture(t)
		scratch := filepath.Join(f.dir, "draft.md")
		if err := f.write(t, "c1", "sess_bot", scratch, "draft"); err != nil {
			t.Fatalf("write of a new file in its directory = %v, want nil", err)
		}
		if _, err := f.do(t, "c1", methodKiroFSDelete, "sess_bot", scratch, nil); err != nil {
			t.Errorf("delete of a file in its directory = %v, want nil", err)
		}
		if _, err := os.Lstat(scratch); !os.IsNotExist(err) {
			t.Errorf("the file after its delete: lstat err = %v, want not-exist", err)
		}
	})
	t.Run("a KIRO_HOME reached through a link", func(t *testing.T) {
		f := newBotGrantFixture(t)
		linked := filepath.Join(canonDir(t, t.TempDir()), "home")
		if err := os.Symlink(filepath.Dir(f.home), linked); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HOME", linked)
		if got, err := f.read(t, "c1", "sess_bot", filepath.Join(linked, ".kiro", "bots", "c1", "memory.md")); err != nil || got != botNotes {
			t.Errorf("read through a linked home = %q, %v; want its notes", got, err)
		}
		if got, err := f.read(t, "c1", "sess_bot", f.file); err != nil || got != botNotes {
			t.Errorf("read by the linked home's real location = %q, %v; want its notes", got, err)
		}
	})
}

// TestBotMemoryGrant_NoOtherSessionReachesTheDirectory pins that only a session in the started Bot's chain, with memory
// on, reaches the directory; every other request, read or write, is refused.
func TestBotMemoryGrant_NoOtherSessionReachesTheDirectory(t *testing.T) {
	for _, tc := range []struct {
		name    string
		chatID  marotte.ChatID
		session string
		setup   func(t *testing.T, f *botGrantFixture)
	}{
		{name: "a regular chat", chatID: "c3", session: "sess_chat"},
		{name: "another Bot", chatID: "c2", session: "sess_other_bot"},
		{name: "a step of a run the Bot launched", chatID: "c1", session: "sess_step"},
		{name: "a tangent forked from the Bot", chatID: "c4", session: "sess_tangent"},
		{name: "the observer pass", chatID: "", session: "sess_observer"},
		{name: "a request naming no session", chatID: "c1"},
		{name: "a session the Bot's chain does not hold", chatID: "c1", session: "sess_elsewhere"},
		{name: "a draft", chatID: "c1", session: "sess_bot", setup: func(t *testing.T, f *botGrantFixture) {
			f.edit(t, "c1", func(c *marotte.Chat) { c.Bot.StartedAt = 0 })
		}},
		{name: "a Bot with memory off", chatID: "c1", session: "sess_bot", setup: func(t *testing.T, f *botGrantFixture) {
			f.edit(t, "c1", func(c *marotte.Chat) { c.Bot.Memory = false })
		}},
		{name: "a chat whose Bot block is gone", chatID: "c1", session: "sess_bot", setup: func(t *testing.T, f *botGrantFixture) {
			f.edit(t, "c1", func(c *marotte.Chat) { c.Bot = nil })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBotGrantFixture(t)
			if tc.setup != nil {
				tc.setup(t, f)
			}
			if got, err := f.read(t, tc.chatID, tc.session, f.file); err == nil {
				t.Errorf("read of c1's memory file = %q, want refused", got)
			}
			if err := f.write(t, tc.chatID, tc.session, f.file, "planted"); err == nil {
				t.Error("write of c1's memory file = nil, want refused")
			}
			if res, err := f.do(t, tc.chatID, methodKiroFSStat, tc.session, f.file, nil); err == nil {
				t.Errorf("stat of c1's memory file = %+v, want refused", res)
			}
			if res, err := f.do(t, tc.chatID, methodKiroFSReadDirectory, tc.session, f.dir, nil); err == nil {
				t.Errorf("read_directory of c1's directory = %+v, want refused", res)
			}
			if _, err := f.do(t, tc.chatID, methodKiroFSDelete, tc.session, f.file, nil); err == nil {
				t.Error("delete of c1's memory file = nil, want refused")
			}
			if got, err := os.ReadFile(f.file); err != nil || string(got) != botNotes {
				t.Errorf("c1's memory file after the refused requests = %q, %v; want its notes", got, err)
			}
		})
	}
	t.Run("another Bot's directory from the Bot's own session", func(t *testing.T) {
		f := newBotGrantFixture(t)
		other := filepath.Join(f.home, "bots", "c2", "memory.md")
		if got, err := f.read(t, "c1", "sess_bot", other); err == nil {
			t.Errorf("read of c2's memory file from c1 = %q, want refused", got)
		}
		if err := f.write(t, "c1", "sess_bot", other, "planted"); err == nil {
			t.Error("write of c2's memory file from c1 = nil, want refused")
		}
	})
	t.Run("the bots directory above the Bot's own", func(t *testing.T) {
		f := newBotGrantFixture(t)
		if res, err := f.do(t, "c1", methodKiroFSReadDirectory, "sess_bot", filepath.Dir(f.dir), nil); err == nil {
			t.Errorf("read_directory of %s = %+v, want refused", filepath.Dir(f.dir), res)
		}
		if got, err := f.read(t, "c1", "sess_bot", f.dir+"/../c2/memory.md"); err == nil {
			t.Errorf("read of a dot-dot spelling into c2 = %q, want refused", got)
		}
	})
	t.Run("the Bot's directory itself cannot be deleted", func(t *testing.T) {
		f := newBotGrantFixture(t)
		if _, err := f.do(t, "c1", methodKiroFSDelete, "sess_bot", f.dir, nil); err == nil {
			t.Error("delete of the Bot's directory = nil, want refused")
		}
		if _, err := os.Stat(f.dir); err != nil {
			t.Errorf("the Bot's directory is gone: %v", err)
		}
	})
}

// TestBotMemoryGrant_ARoutingIdNamingNoChatIsRefusedQuietly pins that a request routed on an id no chat can hold (a
// parentless run's, the observer pass's) is refused without reading the chat store, so no false chat-store error is
// logged for an expected refusal.
func TestBotMemoryGrant_ARoutingIdNamingNoChatIsRefusedQuietly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		chatID marotte.ChatID
	}{
		{"a parentless run", runChatPrefix + "wf_bot"},
		{"the observer pass", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBotGrantFixture(t)
			logs := captureLogs(t)
			if got, err := f.read(t, tc.chatID, "sess_step", f.file); err == nil {
				t.Errorf("read of c1's memory file = %q, want refused", got)
			}
			if err := f.write(t, tc.chatID, "sess_step", f.file, "planted"); err == nil {
				t.Error("write of c1's memory file = nil, want refused")
			}
			for line := range strings.Lines(logs.String()) {
				if strings.Contains(line, `"level":"ERROR"`) {
					t.Errorf("an expected refusal logged an error: %s", line)
				}
			}
		})
	}
}

// TestBotMemoryGrant_ALinkAtTheDirectoryOrTheFileIsRefused pins that a Bot pointed at another tree, by a link at its
// directory, at the bots directory above it or at the file, reaches nothing.
func TestBotMemoryGrant_ALinkAtTheDirectoryOrTheFileIsRefused(t *testing.T) {
	plantOutside := func(t *testing.T) string {
		t.Helper()
		outside := canonDir(t, t.TempDir())
		if err := os.WriteFile(filepath.Join(outside, "memory.md"), []byte(outsideSecret), 0o600); err != nil {
			t.Fatal(err)
		}
		return outside
	}
	for _, tc := range []struct {
		name string
		link func(t *testing.T, f *botGrantFixture) (checked string)
	}{
		{"the Bot's directory linking elsewhere", func(t *testing.T, f *botGrantFixture) string {
			outside := plantOutside(t)
			if err := os.RemoveAll(f.dir); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, f.dir); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(outside, "memory.md")
		}},
		{"the bots directory linking elsewhere", func(t *testing.T, f *botGrantFixture) string {
			outside := plantOutside(t)
			bots := filepath.Dir(f.dir)
			if err := os.Mkdir(filepath.Join(outside, "c1"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(filepath.Join(outside, "memory.md"), filepath.Join(outside, "c1", "memory.md")); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(bots); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, bots); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(outside, "c1", "memory.md")
		}},
		{"the memory file linking out of the directory", func(t *testing.T, f *botGrantFixture) string {
			outside := plantOutside(t)
			if err := os.Remove(f.file); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(outside, "memory.md"), f.file); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(outside, "memory.md")
		}},
		{"the memory file linking to another file in the directory", func(t *testing.T, f *botGrantFixture) string {
			other := filepath.Join(f.dir, "kept.md")
			if err := os.WriteFile(other, []byte(outsideSecret), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(f.file); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("kept.md", f.file); err != nil {
				t.Fatal(err)
			}
			return other
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBotGrantFixture(t)
			checked := tc.link(t, f)
			if got, err := f.read(t, "c1", "sess_bot", f.file); err == nil {
				t.Errorf("read through the link = %q, want refused", got)
			}
			if err := f.write(t, "c1", "sess_bot", f.file, "clobber"); err == nil {
				t.Error("write through the link = nil, want refused")
			}
			if res, err := f.do(t, "c1", methodKiroFSStat, "sess_bot", f.file, nil); err == nil {
				t.Errorf("stat through the link = %+v, want refused", res)
			}
			if got, err := os.ReadFile(checked); err != nil || string(got) != outsideSecret {
				t.Errorf("the linked file after the refused requests = %q, %v; want it untouched", got, err)
			}
		})
	}
}

// TestBotMemoryGrant_AFileOfAnotherKindIsRefused pins the grant's kind check: in the Bot's directory only a directory,
// a regular file or an absent name is served, so a FIFO a shell planted there is never opened.
func TestBotMemoryGrant_AFileOfAnotherKindIsRefused(t *testing.T) {
	f := newBotGrantFixture(t)
	pipe := filepath.Join(f.dir, "pipe")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}
	// Held open read-write, so an open the grant wrongly admits does not block the test.
	held, err := os.OpenFile(pipe, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	if res, err := f.do(t, "c1", methodKiroFSStat, "sess_bot", pipe, nil); err == nil {
		t.Errorf("stat of a FIFO = %+v, want refused", res)
	}
	if err := f.write(t, "c1", "sess_bot", pipe, "planted"); err == nil {
		t.Error("write of a FIFO = nil, want refused")
	}
	if info, err := os.Lstat(pipe); err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("the FIFO after the refused requests = %v, %v; want it kept", info, err)
	}
}
