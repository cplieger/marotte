package chat

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/marotte/internal/marotte"
)

// withinBudget runs fn on its own goroutine and fails the test if it has not returned
// inside budget: the defect these tests pin HANGS rather than failing, because os.Open on a
// FIFO blocks in open(2) until a writer appears and no context deadline can rescue it. The
// goroutine is abandoned on expiry — it is parked in the kernel and nothing can reclaim it.
func withinBudget(t *testing.T, budget time.Duration, fn func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(budget):
		t.Fatalf("still blocked after %v: the read followed a non-regular file into open(2)", budget)
		return nil
	}
}

// mkfifoChat plants a FIFO where a chat id's header belongs and returns the id.
func mkfifoChat(t *testing.T, dir string) marotte.ChatID {
	t.Helper()
	if _, err := os.Stat("/dev/null"); err != nil {
		t.Skip("no unix device nodes")
	}
	id := marotte.ChatID("m-fifo0000-aaaa")
	if !chatIDPattern(id) {
		t.Fatalf("fixture id %q is not a valid chat id", id)
	}
	if err := os.MkdirAll(filepath.Join(dir, string(id)), 0o700); err != nil {
		t.Fatalf("chat dir: %v", err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, string(id), headerFileName), 0o600); err != nil {
		t.Skipf("mkfifo unsupported here: %v", err)
	}
	return id
}

// A FIFO at <chats>/<valid-chat-id>/chat.json is a one-command permanent wedge of every chat
// read, and the /config volume is reachable both by the operator and by the agent's shell.
func TestGet_RefusesAFifoInsteadOfBlockingForever(t *testing.T) {
	s, _ := newTestStore(t)
	id := mkfifoChat(t, s.dir)

	err := withinBudget(t, 3*time.Second, func() error {
		_, err := s.load(t.Context(), id)
		return err
	})
	if !errors.Is(err, atomicfile.ErrNotRegular) {
		t.Errorf("load over a FIFO = %v, want atomicfile.ErrNotRegular", err)
	}
	// And the public read reports absence rather than propagating the wedge.
	if _, ok := withinBudgetGet(t, s, id); ok {
		t.Error("Get returned ok for a FIFO planted at a chat file name")
	}
}

func withinBudgetGet(t *testing.T, s *Store, id marotte.ChatID) (*marotte.Chat, bool) {
	t.Helper()
	type res struct {
		c  *marotte.Chat
		ok bool
	}
	out := make(chan res, 1)
	go func() {
		c, ok := s.Get(t.Context(), id)
		out <- res{c, ok}
	}()
	select {
	case r := <-out:
		return r.c, r.ok
	case <-time.After(3 * time.Second):
		t.Fatal("Get still blocked after 3s over a FIFO")
		return nil, false
	}
}

// List reads with 8 workers inside one singleflight slot, so a blocking open wedges every
// concurrent GET /api/chats behind it, and the completeness flag is what makes the session
// sweep fail closed over the file it could not read.
func TestList_SurvivesAFifoAndReportsTheScanIncomplete(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.Mutate(t.Context(), "good", func(c *marotte.Chat, _ bool) bool {
		c.Name = "readable"
		return true
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	mkfifoChat(t, s.dir)

	type out struct {
		headers  []marotte.ChatHeader
		complete bool
	}
	ch := make(chan out, 1)
	go func() {
		h, c := s.listWithCompleteness(t.Context())
		ch <- out{h, c}
	}()
	select {
	case got := <-ch:
		if len(got.headers) != 1 || got.headers[0].ID != "good" {
			t.Errorf("headers = %+v, want just the readable chat", got.headers)
		}
		if got.complete {
			t.Error("complete = true, want false: a chat that exists was not read, " +
				"so the session keep-list derived from this must not be trusted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("List still blocked after 5s: one FIFO wedged the whole scan")
	}
}

// A link at <chats>/<id>/chat.json makes another file's bytes reachable through the
// chat read, the header projection and both search paths.
func TestOpenChatFile_RefusesASymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte(`{"id":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, headerFileName)
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
	f, _, err := openChatFile(link, "chat link")
	if err == nil {
		f.Close()
		t.Error("openChatFile followed a symlink at a chat header name")
	}
}
