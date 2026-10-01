package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// fakeBroadcaster captures broadcasts for assertions. Access is guarded
// by mu so concurrent appends from parallel Append goroutines don't race
// the slice header.
type fakeBroadcaster struct {
	events []marotte.ServerEvent
	count  atomic.Int32
	mu     sync.Mutex
}

var _ broadcaster = (*fakeBroadcaster)(nil)

func (f *fakeBroadcaster) Broadcast(_ context.Context, e marotte.ServerEvent) {
	f.mu.Lock()
	f.events = append(f.events, e)
	f.mu.Unlock()
	f.count.Add(1)
}

// snapshot returns a copy of the captured events under the mutex.
// Tests should use this instead of reading f.events directly so a
// future asynchronous broadcaster doesn't race a bare slice read.
func (f *fakeBroadcaster) snapshot() []marotte.ServerEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.events)
}

// reset clears the event log under the mutex. Tests that want to
// observe only post-setup broadcasts should use this instead of a
// bare `f.events = nil` assignment.
func (f *fakeBroadcaster) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = nil
}

func newTestStore(t *testing.T) (*Store, *fakeBroadcaster) {
	t.Helper()
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	b := &fakeBroadcaster{}
	WithBroadcaster(b)(s)
	return s, b
}

// ageChat backdates a chat so it is eligible for purge, writing BOTH its
// UpdatedAt and its mtime. Purge ages from the chat's own UpdatedAt — its last
// activity — so aging the mtime alone does not make an entry purgeable; mtime is
// written too because it is the fallback for a chat that cannot be read.
func ageChat(t *testing.T, s *Store, id string, ago time.Duration) {
	t.Helper()
	path := filepath.Join(s.dir, id, headerFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read chat %s: %v", id, err)
	}
	var c marotte.Chat
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("unmarshal chat %s: %v", id, err)
	}
	old := time.Now().Add(-ago)
	c.UpdatedAt = old.UnixMilli()
	out, err := json.MarshalIndent(&c, "", "  ")
	if err != nil {
		t.Fatalf("marshal chat %s: %v", id, err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatalf("write chat %s: %v", id, err)
	}
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("chtimes chat %s: %v", id, err)
	}
}

// badChatIDs is the canonical set of invalid chat identifiers used
// across all RejectsBadChatID / InvalidChatIDRejected tests. Adding a
// new invalid pattern here automatically covers every method.
var badChatIDs = []marotte.ChatID{"", "a/b", "..", "a\x00b", "a b", marotte.ChatID(strings.Repeat("x", 200))}

// assertRejectsBadChatIDs iterates badChatIDs and asserts that fn
// returns a non-nil error for each. Use in table-driven subtests to
// eliminate duplicated bad-id slices across store method tests.
func assertRejectsBadChatIDs(t *testing.T, fn func(id marotte.ChatID) error) {
	t.Helper()
	for _, bad := range badChatIDs {
		if err := fn(bad); err == nil {
			t.Errorf("accepted bad id %q", bad)
		}
	}
}

// --- Create + Get ---

func TestMutate_CreatesChatAndBroadcasts(t *testing.T) {
	s, b := newTestStore(t)
	_, err := s.Mutate(t.Context(), "c1", func(c *marotte.Chat, exists bool) bool {
		if exists {
			t.Error("exists = true on fresh chat")
		}
		c.Name = "Hello"
		c.Model = "claude"
		return true
	})
	if err != nil {
		t.Fatalf("Mutate error = %v", err)
	}
	got, ok := s.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("Get returned false for created chat")
	}
	if got.Name != "Hello" || got.Model != "claude" {
		t.Errorf("fields: %+v", got)
	}
	if got.CreatedAt == 0 || got.UpdatedAt == 0 {
		t.Error("timestamps not set")
	}
	if len(b.events) != 1 || b.events[0].Type != "chat_created" {
		t.Errorf("broadcasts: %+v", b.events)
	}
}

func TestMutate_UpdatesChatAndBroadcasts(t *testing.T) {
	s, b := newTestStore(t)
	_, _ = s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	// Second Mutate should broadcast chat_updated.
	_, _ = s.Mutate(t.Context(), "c1", func(c *marotte.Chat, exists bool) bool {
		if !exists {
			t.Error("exists = false on existing chat")
		}
		c.Name = "B"
		return true
	})
	got, _ := s.Get(t.Context(), "c1")
	if got.Name != "B" {
		t.Errorf("name = %q", got.Name)
	}
	if len(b.events) != 2 || b.events[1].Type != "chat_updated" {
		t.Errorf("second event should be chat_updated: %+v", b.events)
	}
}

func TestMutate_AbortDoesNotBroadcast(t *testing.T) {
	s, b := newTestStore(t)
	_, err := s.Mutate(t.Context(), "c1", func(*marotte.Chat, bool) bool { return false })
	if err != nil {
		t.Fatalf("Mutate error = %v", err)
	}
	if _, ok := s.Get(t.Context(), "c1"); ok {
		t.Error("chat created despite abort")
	}
	if len(b.events) != 0 {
		t.Errorf("broadcast despite abort: %+v", b.events)
	}
}

func TestMutate_RejectsBadChatID(t *testing.T) {
	s, _ := newTestStore(t)
	assertRejectsBadChatIDs(t, func(id marotte.ChatID) error {
		_, err := s.Mutate(t.Context(), id, func(*marotte.Chat, bool) bool { return true })
		return err
	})
}

// --- Broadcaster contract ---

// BroadcasterContractTest holds any Broadcaster implementation to two semantics
// under concurrent Broadcast calls: a single writer's events stay in submission
// order, and Broadcast never blocks indefinitely.
func BroadcasterContractTest(t *testing.T, newBroadcaster func() broadcaster) {
	t.Helper()

	t.Run("ConcurrentBroadcastsDoNotBlock", func(t *testing.T) {
		b := newBroadcaster()
		const N = 100
		done := make(chan struct{})
		go func() {
			var wg sync.WaitGroup
			for i := range N {
				wg.Go(func() {
					b.Broadcast(t.Context(), marotte.ServerEvent{
						Type:   "test_event",
						ChatID: marotte.ChatID(fmt.Sprintf("c%d", i)),
					})
				})
			}
			wg.Wait()
			close(done)
		}()
		select {
		case <-done:
			// All N concurrent broadcasts completed without blocking.
		case <-time.After(5 * time.Second):
			t.Fatal("Broadcast blocked: N concurrent calls did not complete within 5s")
		}
	})

	t.Run("SingleWriterOrderPreserved", func(t *testing.T) {
		b := newBroadcaster()
		const N = 200
		for i := range N {
			b.Broadcast(t.Context(), marotte.ServerEvent{
				Type:   "order_test",
				ChatID: marotte.ChatID(fmt.Sprintf("%d", i)),
			})
		}
		// Verify ordering via the concrete type's snapshot if available.
		type snapshotter interface {
			snapshot() []marotte.ServerEvent
		}
		if s, ok := b.(snapshotter); ok {
			evs := s.snapshot()
			if len(evs) != N {
				t.Fatalf("len(events) = %d, want %d", len(evs), N)
			}
			for i, e := range evs {
				want := marotte.ChatID(fmt.Sprintf("%d", i))
				if e.ChatID != want {
					t.Errorf("event[%d].ChatID = %q, want %q (order violated)", i, e.ChatID, want)
					break
				}
			}
		}
	})
}

func TestFakeBroadcaster_ContractCompliance(t *testing.T) {
	BroadcasterContractTest(t, func() broadcaster {
		return &fakeBroadcaster{}
	})
}

// --- chatIDPattern ---

func TestChatIDPattern(t *testing.T) {
	valid := []string{
		"abc",
		"ABC",
		"01HXYZ",                               // ULID-like
		"550e8400-e29b-41d4-a716-446655440000", // UUID
		"chat-1716000000000",                   // legacy chat-<ms>
		"a_b",                                  // underscore
		"a-b",                                  // hyphen
		strings.Repeat("x", 128),               // max length
	}
	for _, id := range valid {
		if !chatIDPattern(marotte.ChatID(id)) {
			t.Errorf("chatIDPattern(%q) = false, want true", id)
		}
	}

	invalid := []string{
		"",                       // empty
		"a/b",                    // slash
		"..",                     // traversal
		"a.b",                    // dot
		"a b",                    // space
		"a\x00b",                 // null byte
		"a\nb",                   // newline
		strings.Repeat("x", 129), // over max length
	}
	for _, id := range invalid {
		if chatIDPattern(marotte.ChatID(id)) {
			t.Errorf("chatIDPattern(%q) = true, want false", id)
		}
	}
}

func TestGet_MissingChat(t *testing.T) {
	s, _ := newTestStore(t)
	if _, ok := s.Get(t.Context(), "nonexistent"); ok {
		t.Error("Get returned true for missing chat")
	}
}

// --- List ---

// TestList_SortsByUpdatedAtDesc runs in a synctest bubble, so the gap between the
// two timestamps is exact rather than a real-clock nudge that can collide on a
// fast machine.
//
// The store's real filesystem work is fine in here: TRANSIENT file I/O reaches a
// durably-blocked state afterwards so the clock still advances, and only a
// goroutine parked indefinitely on an external FD defeats a bubble.
func TestList_SortsByUpdatedAtDesc(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _ := newTestStore(t)
		_, _ = s.Mutate(t.Context(), "a", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
		synctest.Sleep(2 * time.Millisecond)
		_, _ = s.Mutate(t.Context(), "b", func(c *marotte.Chat, _ bool) bool { c.Name = "B"; return true })
		synctest.Sleep(2 * time.Millisecond)
		_, _ = s.Mutate(t.Context(), "a", func(c *marotte.Chat, _ bool) bool { return true }) // bump updated_at
		headers := s.List(t.Context())
		// Fatal: every assertion below indexes headers.
		if len(headers) != 2 {
			t.Fatalf("len = %d, want 2", len(headers))
		}
		if headers[0].ID != "a" {
			t.Errorf("first = %q, want a (most recently updated)", headers[0].ID)
		}
		// Exactly 4ms of synthetic time separates a's second mutation from b's
		// only one. On a real clock this could only ever be asserted as `> 0`,
		// which a save that stamped the wrong field, or stamped once and reused
		// the value, would satisfy.
		if gap := headers[0].UpdatedAt - headers[1].UpdatedAt; gap != 2 {
			t.Errorf("UpdatedAt gap = %dms, want exactly 2 (b at +2ms, a re-stamped at +4ms)", gap)
		}
	})
}

func TestList_IgnoresNonChatFiles(t *testing.T) {
	s, _ := newTestStore(t)
	_, _ = s.Mutate(t.Context(), "a", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	// Non-.json file → skipped by the suffix filter.
	if err := os.WriteFile(filepath.Join(s.dir, "random.txt"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Valid .json suffix but invalid chat id (contains a '.') → skipped
	// by the chatIDPattern filter in List.
	if err := os.WriteFile(filepath.Join(s.dir, "bad.id.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	headers := s.List(t.Context())
	if len(headers) != 1 || headers[0].ID != "a" {
		t.Errorf("headers = %+v", headers)
	}
}

func TestList_SkipsMalformedChatFile(t *testing.T) {
	s, _ := newTestStore(t)
	_, _ = s.Mutate(t.Context(), "good", func(c *marotte.Chat, _ bool) bool { c.Name = "ok"; return true })
	// Drop a file that matches chatIDPattern but isn't valid JSON.
	// List must log and skip it, not panic or return a zero-value
	// header that confuses clients.
	badPath := filepath.Join(s.dir, "bad.json")
	if err := os.WriteFile(badPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	headers := s.List(t.Context())
	if len(headers) != 1 || headers[0].ID != "good" {
		t.Errorf("List() = %+v, want only the good chat", headers)
	}
}

// --- Delete ---

func TestDelete_RemovesFileAndBroadcasts(t *testing.T) {
	s, b := newTestStore(t)
	_, _ = s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	b.reset()

	if err := s.Delete(t.Context(), "c1"); err != nil {
		t.Fatalf("Delete error = %v", err)
	}
	if _, ok := s.Get(t.Context(), "c1"); ok {
		t.Error("chat still exists after delete")
	}
	if len(b.events) != 1 || b.events[0].Type != "chat_deleted" {
		t.Errorf("events: %+v", b.events)
	}
}

func TestDelete_MissingChatIsNoOp(t *testing.T) {
	s, b := newTestStore(t)
	if err := s.Delete(t.Context(), "nonexistent"); err != nil {
		t.Errorf("error on missing chat: %v", err)
	}
	// Still broadcasts (so multi-device sees the delete even if stale).
	if len(b.events) != 1 {
		t.Errorf("events: %+v", b.events)
	}
}

// --- Tombstone (delete-during-turn race guard) ---

func TestDelete_TombstonesChatID(t *testing.T) {
	s, _ := newTestStore(t)
	_, _ = s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = s.Delete(t.Context(), "c1")
	if !s.isTombstoned("c1") {
		t.Error("tombstone not set after Delete")
	}
}

func TestMutate_RefusesToCreateTombstonedChat(t *testing.T) {
	s, b := newTestStore(t)
	_, _ = s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = s.Delete(t.Context(), "c1")
	b.reset()

	// Simulate a late handler racing the delete — it tries to Mutate
	// the just-deleted id. Nothing is written and nothing is broadcast,
	// and the refusal is reported so the caller can tell it apart from
	// a persisted write.
	_, err := s.Mutate(t.Context(), "c1", func(c *marotte.Chat, exists bool) bool {
		c.Name = "resurrected"
		return true
	})
	if !errors.Is(err, ErrTombstoned) {
		t.Fatalf("Mutate on tombstoned chat error = %v, want ErrTombstoned", err)
	}
	if _, ok := s.Get(t.Context(), "c1"); ok {
		t.Error("chat was resurrected despite tombstone")
	}
	// No chat_created / chat_updated event should have been emitted.
	for _, e := range b.snapshot() {
		if e.Type == "chat_created" || e.Type == "chat_updated" {
			t.Errorf("unexpected event after tombstoned mutate: %+v", e)
		}
	}
}

// The three outcomes of the single write path, pinned apart in one test
// because the defect was that two of them were indistinguishable: applied and
// no-op are both nil, and refused is ErrTombstoned. A regression that reports
// a refusal as nil passes every other test in this file.
func TestMutate_PinsAppliedNoOpAndRefusedApart(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true }); err != nil {
		t.Errorf("Mutate(applied) = %v, want nil", err)
	}
	if _, err := s.Mutate(t.Context(), "c1", func(*marotte.Chat, bool) bool { return false }); err != nil {
		t.Errorf("Mutate(no-op) = %v, want nil", err)
	}
	_, _ = s.Mutate(t.Context(), "c2", func(c *marotte.Chat, _ bool) bool { c.Name = "B"; return true })
	if err := s.Delete(t.Context(), "c2"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err := s.Mutate(t.Context(), "c2", func(c *marotte.Chat, _ bool) bool { c.Name = "ghost"; return true })
	if !errors.Is(err, ErrTombstoned) {
		t.Errorf("Mutate(refused) = %v, want ErrTombstoned", err)
	}
}

func TestMutate_UpdatingExistingChatIsNotBlockedByTombstone(t *testing.T) {
	// Tombstone only blocks the "create if missing" path. An existing
	// chat whose id happens to share a string with a tombstoned id
	// would be vanishingly rare (chat IDs are client-random), but
	// verify the code path: once the chat exists, tombstone is
	// irrelevant because we never consult it.
	s, _ := newTestStore(t)
	_, _ = s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = s.Delete(t.Context(), "c1")
	// Tombstone is now live for c1. Re-Create via a different id
	// shouldn't be affected.
	_, err := s.Mutate(t.Context(), "c2", func(c *marotte.Chat, _ bool) bool { c.Name = "B"; return true })
	if err != nil {
		t.Fatalf("unrelated chat blocked by unrelated tombstone: %v", err)
	}
	if _, ok := s.Get(t.Context(), "c2"); !ok {
		t.Error("c2 was not created")
	}
}

func TestAppend_OnTombstonedChatIsRefused(t *testing.T) {
	s, b := newTestStore(t)
	_, _ = s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = s.Delete(t.Context(), "c1")
	b.reset()

	// A late handler racing the delete appends nothing, emits nothing, and gets
	// the refusal back, so it cannot read the dropped entry as persisted.
	err := s.Append(t.Context(), "c1", entryOf("t-ghost", "", "a1", marotte.EntryKindText, marotte.EntryText{Text: "ghost"}))
	if !errors.Is(err, ErrTombstoned) {
		t.Fatalf("Append error = %v, want ErrTombstoned", err)
	}
	if _, ok := s.Get(t.Context(), "c1"); ok {
		t.Error("chat was recreated via Append after delete")
	}
	if n := len(b.snapshot()); n != 0 {
		t.Errorf("events after a refused append = %d, want 0: %+v", n, b.snapshot())
	}
}

// --- HTTP endpoints ---

func TestHandleList_ReturnsHeaders(t *testing.T) {
	s, _ := newTestStore(t)
	openPromptTurn(t, s, "c1", "m-1")
	if err := s.WriteCounters(t.Context(), "c1"); err != nil {
		t.Fatalf("WriteCounters: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/chats", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleList(rec, req)

	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"turn_count":1`) {
		t.Errorf("body = %q, want the header's turn_count", body)
	}
	// The list carries headers alone, never the log.
	if strings.Contains(body, `"entries"`) || strings.Contains(body, `"payload"`) {
		t.Errorf("entries leaked into list response: %q", body)
	}
}

func TestHandleOne_NotFound(t *testing.T) {
	s, _ := newTestStore(t)
	req := httptest.NewRequest(http.MethodGet, "/api/chats/nope", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("code = %d", rec.Code)
	}
}

func TestHandleOne_RejectsUnknownSubResource(t *testing.T) {
	// With sub-resource routing, /api/chats/a/b treats "b" as a sub-
	// resource name. Unknown sub-resources return 404. Historically this
	// was a 400 "slash in id" — the new behaviour is correct because
	// export and archive are valid sub-resources.
	s, _ := newTestStore(t)
	req := httptest.NewRequest(http.MethodGet, "/api/chats/a/b", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rec.Code)
	}
}

// --- Persistence ---

func TestStoreSurvivesReopen(t *testing.T) {
	dir := t.TempDir()

	s1, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	_, _ = s1.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "Saved"
		c.ACPSessionID = "acp-1"
		return true
	})
	turn := openPromptTurn(t, s1, "c1", "m-1")
	if err := s1.Append(t.Context(), "c1", entryOf(turn, "", "a1", marotte.EntryKindText, marotte.EntryText{Text: "hi"})); err != nil {
		t.Fatalf("Append: %v", err)
	}
	closeTurn(t, s1, "c1", turn, marotte.TurnOutcomeCompleted)

	// Reopen: the header and the log both come back off disk.
	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore reopen: %v", err)
	}
	got, ok := s2.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("chat not found after reopen")
	}
	if got.Name != "Saved" || got.ACPSessionID != "acp-1" {
		t.Errorf("reloaded chat: %+v", got)
	}
	page, err := s2.TurnPage(t.Context(), "c1", turn, 0)
	if err != nil {
		t.Fatalf("TurnPage after reopen: %v", err)
	}
	if got := turnsOfPage(page.Entries); got != turn+":turn_open "+turn+":text "+turn+":turn_close" {
		t.Errorf("entries after reopen = %q, want the whole closed turn", got)
	}
}

// --- Plan drafts ---

// --- Extended HTTP handler coverage ---

func TestHandleList_EmptyStoreReturnsEmptyArrayNotNull(t *testing.T) {
	// Regression: Go's json.Marshal of a nil slice emits `null`. The
	// frontend wire decoder rejects null for fields typed as array
	// ("$.chat_list.chats: expected array, got null") and the chat
	// list quietly stops working after a fresh container init.
	// List() must always return a non-nil slice so JSON encodes `[]`.
	s, _ := newTestStore(t)

	req := httptest.NewRequest(http.MethodGet, "/api/chats", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"chats":[]`) {
		t.Errorf("body should contain `\"chats\":[]`, got: %s", body)
	}
	if strings.Contains(body, `"chats":null`) {
		t.Errorf("body must not contain `\"chats\":null`: %s", body)
	}
}

func TestHandleList_RejectsNonGET(t *testing.T) {
	s, _ := newTestStore(t)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := httptest.NewRequest(method, "/api/chats", nil)
		rec := httptest.NewRecorder()
		NewRouter(s).handleList(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("handleList(%s) code = %d, want 405", method, rec.Code)
		}
	}
}

func TestHandleOne_RejectsEmptyOrLeadingSlashPath(t *testing.T) {
	s, _ := newTestStore(t)
	cases := []struct {
		name string
		path string
	}{
		{"empty_id", "/api/chats/"},
		{"leading_slash", "/api/chats//c1"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		rec := httptest.NewRecorder()
		NewRouter(s).handleOne(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("handleOne(%s) code = %d, want 400", tc.name, rec.Code)
		}
	}
}

func TestHandleOne_BaseRejectsNonGET(t *testing.T) {
	s, _ := newTestStore(t)
	_, _ = s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := httptest.NewRequest(method, "/api/chats/c1", nil)
		rec := httptest.NewRecorder()
		NewRouter(s).handleOne(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("handleOne(%s /api/chats/c1) code = %d, want 405", method, rec.Code)
		}
	}
}

func TestHandleOne_IgnoresInvalidQueryParams(t *testing.T) {
	s, _ := newTestStore(t)
	openPromptTurn(t, s, "c1", "m-1")
	cases := []struct {
		name  string
		query string
	}{
		{"limit_non_numeric", "?limit=abc"},
		{"limit_zero", "?limit=0"},
		{"limit_negative", "?limit=-5"},
		{"limit_over_max", "?limit=10000"},
		{"unknown_param", "?before_id=nope"},
		{"unknown_param_empty", "?before_id="},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/chats/c1"+tc.query, nil)
		rec := httptest.NewRecorder()
		NewRouter(s).handleOne(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("handleOne(%s) code = %d, want 200 (invalid params should fall back)", tc.name, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"kind":"turn_open"`) {
			t.Errorf("handleOne(%s) did not return the page: %s", tc.name, rec.Body.String())
		}
	}
}

func TestRegisterRoutes_WiresListAndOneHandlers(t *testing.T) {
	s, _ := newTestStore(t)
	_, _ = s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })

	mux := http.NewServeMux()
	s.RegisterRoutes(mux)

	// /api/chats reaches handleList
	req := httptest.NewRequest(http.MethodGet, "/api/chats", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/chats code = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"id":"c1"`) {
		t.Errorf("GET /api/chats body missing c1: %s", rec.Body.String())
	}

	// /api/chats/c1 reaches handleOne
	req = httptest.NewRequest(http.MethodGet, "/api/chats/c1", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/chats/c1 code = %d, want 200", rec.Code)
	}
}

// --- Concurrency ---

func TestAppend_SerializesSameChatConcurrentAppends(t *testing.T) {
	s, _ := newTestStore(t)
	turn := openPromptTurn(t, s, "c1", "m-1")

	const N = 50
	var wg sync.WaitGroup
	for i := range N {
		wg.Go(func() {
			_ = s.Append(t.Context(), "c1", entryOf(turn, "", fmt.Sprintf("a%d", i), marotte.EntryKindText, marotte.EntryText{Text: "x"}))
		})
	}
	wg.Wait()
	page, err := s.TurnPage(t.Context(), "c1", turn, 0)
	if err != nil {
		t.Fatalf("TurnPage: %v", err)
	}
	entries := page.Entries
	if len(entries) != N+1 {
		t.Fatalf("concurrent appends: %d entries, want %d (per-chat mutex should serialize)", len(entries), N+1)
	}
	for i := range entries {
		if entries[i].Seq != uint64(i) {
			t.Errorf("entry %d has seq %d, want %d: seq is assigned under the lock, contiguous from 0", i, entries[i].Seq, i)
		}
	}
}

func TestAppend_DifferentChatsAreIndependent(t *testing.T) {
	// Two chats must not block each other: N appends on each run to completion
	// with no deadlock and each log holds its own N.
	s, _ := newTestStore(t)
	ta := openPromptTurn(t, s, "a", "m-a")
	tb := openPromptTurn(t, s, "b", "m-b")

	const N = 20
	var wg sync.WaitGroup
	for i := range N {
		wg.Go(func() {
			_ = s.Append(t.Context(), "a", entryOf(ta, "", fmt.Sprintf("a%d", i), marotte.EntryKindText, marotte.EntryText{Text: "x"}))
		})
		wg.Go(func() {
			_ = s.Append(t.Context(), "b", entryOf(tb, "", fmt.Sprintf("b%d", i), marotte.EntryKindText, marotte.EntryText{Text: "x"}))
		})
	}
	wg.Wait()
	pageA, err := s.TurnPage(t.Context(), "a", ta, 0)
	if err != nil {
		t.Fatalf("TurnPage(a): %v", err)
	}
	pageB, err := s.TurnPage(t.Context(), "b", tb, 0)
	if err != nil {
		t.Fatalf("TurnPage(b): %v", err)
	}
	a, b := pageA.Entries, pageB.Entries
	if len(a) != N+1 || len(b) != N+1 {
		t.Errorf("independent chats: a=%d b=%d want %d each", len(a), len(b), N+1)
	}
}

// --- Tombstone prune paths ---

func TestIsTombstoned_ExpiredEntryIsPrunedAndReturnsFalse(t *testing.T) {
	s, _ := newTestStore(t)
	// Inject an expired tombstone (older than tombstoneTTL).
	s.tombMu.Lock()
	s.tombstone["c1"] = time.Now().Add(-2 * tombstoneTTL)
	s.tombMu.Unlock()

	if s.isTombstoned("c1") {
		t.Error("isTombstoned returned true for expired tombstone")
	}
	// And the expired entry is now pruned.
	s.tombMu.Lock()
	_, still := s.tombstone["c1"]
	s.tombMu.Unlock()
	if still {
		t.Error("expired tombstone was not pruned by isTombstoned")
	}
}

func TestMutate_ExpiredTombstoneDoesNotBlockRecreation(t *testing.T) {
	s, _ := newTestStore(t)
	_, _ = s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = s.Delete(t.Context(), "c1")

	// Age the tombstone past its TTL.
	s.tombMu.Lock()
	s.tombstone["c1"] = time.Now().Add(-2 * tombstoneTTL)
	s.tombMu.Unlock()

	_, err := s.Mutate(t.Context(), "c1", func(c *marotte.Chat, exists bool) bool {
		if exists {
			t.Error("exists = true after expired tombstone")
		}
		c.Name = "reborn"
		return true
	})
	if err != nil {
		t.Fatalf("Mutate after tombstone expiry: %v", err)
	}
	got, ok := s.Get(t.Context(), "c1")
	if !ok || got.Name != "reborn" {
		t.Errorf("expected recreation, got ok=%v chat=%+v", ok, got)
	}
}

func TestMarkDeleted_PrunesExpiredEntries(t *testing.T) {
	s, _ := newTestStore(t)
	// Seed 3 expired + 1 fresh tombstone.
	now := time.Now()
	s.tombMu.Lock()
	s.tombstone["expired-1"] = now.Add(-2 * tombstoneTTL)
	s.tombstone["expired-2"] = now.Add(-3 * tombstoneTTL)
	s.tombstone["expired-3"] = now.Add(-5 * tombstoneTTL)
	s.tombstone["fresh"] = now.Add(-time.Second)
	s.tombMu.Unlock()

	// markDeleted runs prune on every call.
	s.markDeleted("new-delete")

	s.tombMu.Lock()
	defer s.tombMu.Unlock()
	for _, id := range []string{"expired-1", "expired-2", "expired-3"} {
		if _, ok := s.tombstone[marotte.ChatID(id)]; ok {
			t.Errorf("markDeleted did not prune expired entry %q", id)
		}
	}
	if _, ok := s.tombstone["fresh"]; !ok {
		t.Error("markDeleted pruned a non-expired entry")
	}
	if _, ok := s.tombstone["new-delete"]; !ok {
		t.Error("markDeleted did not record the new tombstone")
	}
}

// --- Delete on missing chat: phantom tombstone guard ---

func TestDelete_MissingChatDoesNotTombstone(t *testing.T) {
	// A stale DELETE from a second device (or a client-driven retry)
	// can hit a chat id the server never knew about. Broadcasting the
	// delete is intentional (multi-device UI consistency) but
	// tombstoning a phantom id would block a future legitimate
	// create on that id for 10 minutes.
	s, _ := newTestStore(t)
	_ = s.Delete(t.Context(), "never-existed")
	if s.isTombstoned("never-existed") {
		t.Error("phantom delete tombstoned a chat that never existed")
	}
	// Creating a new chat with that id must succeed.
	_, err := s.Mutate(t.Context(), "never-existed", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	if err != nil {
		t.Fatalf("Mutate after phantom delete: %v", err)
	}
	if _, ok := s.Get(t.Context(), "never-existed"); !ok {
		t.Error("chat not created after phantom delete")
	}
}

// --- NewStore error propagation ---

func TestNewStore_MkdirFailurePropagatesError(t *testing.T) {
	// MkdirAll fails when a path component is a regular file — a
	// real-world misconfiguration users can hit by bind-mounting a file
	// onto the chats directory. The constructor must surface the error
	// so the process fails startup instead of silently running with a
	// broken store.
	base := t.TempDir()
	blocker := filepath.Join(base, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// "blocker" is a file; asking for "<base>/blocker/chats" forces
	// MkdirAll to error because it can't descend through a file.
	_, err := NewStore(filepath.Join(blocker, "chats"))
	if err == nil {
		t.Fatal("NewStore(path-through-file) = nil error, want non-nil")
	}
	if !strings.Contains(err.Error(), "chat store: mkdir") {
		t.Errorf("NewStore error = %q, want wrapping message with prefix %q",
			err.Error(), "chat store: mkdir")
	}
}

// --- Parse error must not silently overwrite ---

func TestMutate_PropagatesParseErrorDoesNotOverwrite(t *testing.T) {
	// A corrupted chat file (manual edit, partial write from a prior
	// crash) must not be silently overwritten by Mutate's auto-create
	// path. Mutate must surface the parse error so callers fail loudly
	// instead of losing the user's history to an implicit "rewrite from
	// empty" operation.
	s, _ := newTestStore(t)
	badPath := filepath.Join(s.dir, "c1", headerFileName)
	const garbage = "{not json"
	if err := os.MkdirAll(filepath.Dir(badPath), 0o700); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.WriteFile(badPath, []byte(garbage), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "would-stomp-history"
		return true
	})
	if err == nil {
		t.Fatal("Mutate on malformed chat file returned nil error, want parse error")
	}
	got, readErr := os.ReadFile(badPath)
	if readErr != nil {
		t.Fatalf("read-back: %v", readErr)
	}
	if string(got) != garbage {
		t.Errorf("Mutate stomped malformed file: got %q, want original %q",
			string(got), garbage)
	}
}

// --- Mutator must not reassign c.ID ---

func TestMutate_RefusesMutatorReassigningChatID(t *testing.T) {
	// Defensive invariant: a mutator that retargets c.ID would save
	// the chat to a different file under a different per-chat mutex,
	// allowing concurrent writes under mismatched locks. Mutate must
	// refuse and surface the error so the broken caller is visible.
	s, _ := newTestStore(t)
	_, err := s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.ID = "c2" // broken mutator
		c.Name = "stolen"
		return true
	})
	if err == nil {
		t.Fatal("Mutate accepted mutator that reassigned c.ID")
	}
	if !strings.Contains(err.Error(), "reassigned id") {
		t.Errorf("error = %q, want mention of reassigned id", err.Error())
	}
	// Neither chat should have been written.
	if _, ok := s.Get(t.Context(), "c1"); ok {
		t.Error("c1 was written despite mutator reassigning id")
	}
	if _, ok := s.Get(t.Context(), "c2"); ok {
		t.Error("c2 was written via the reassigned id")
	}
}

// --- handleOne pre-validation of chat id ---

func TestHandleOne_RejectsInvalidChatID(t *testing.T) {
	// The chat id pre-validation ensures malformed ids (bot probes,
	// typos) return 400 without emitting an slog.Error from load's
	// pathFor rejection. We stick to ids that httptest.NewRequest accepts literally —
	// characters like ' ' or '%00' fail at URL parsing before reaching
	// the handler.
	s, _ := newTestStore(t)
	for _, bad := range []string{"bad.id", "with@sign", "plus+sign"} {
		req := httptest.NewRequest(http.MethodGet, "/api/chats/"+bad, nil)
		rec := httptest.NewRecorder()
		NewRouter(s).handleOne(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("handleOne(%q) = %d, want 400", bad, rec.Code)
		}
	}
}

// --- Additional coverage for chat-id guards and error-path plumbing ---

// skipIfRoot skips the current test when the effective UID is 0.
// Root bypasses POSIX file permissions, so chmod-to-readonly tests
// designed to trip EACCES on os.Remove produce false negatives
// (remove succeeds, branch never executes). CI runs as non-root
// ubuntu; local WSL is also non-root. Docker-as-root skips.
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("chmod-based permission tests skipped when running as root")
	}
}

func TestDelete_RejectsBadChatID(t *testing.T) {
	// Parallel to TestMutate_RejectsBadChatID; pathFor in Delete
	// guards against directory-traversal ids before any filesystem op.
	// A refactor that moves the check after os.Remove would silently
	// lose the pre-validation contract, so lock the current behaviour:
	// every invalid id produces an error AND zero broadcasts.
	s, b := newTestStore(t)
	assertRejectsBadChatIDs(t, func(id marotte.ChatID) error {
		return s.Delete(t.Context(), id)
	})
	// Defensive assertion: rejected ids never emit chat_deleted.
	// A broken refactor that moves pathFor after os.Remove would
	// pass the error tests but still broadcast — catch it here.
	if evs := b.snapshot(); len(evs) != 0 {
		t.Errorf("invalid chat id deletes broadcast events: %+v", evs)
	}
}

func TestDelete_SurfacesNonENOENTChatRemoveError(t *testing.T) {
	skipIfRoot(t)
	// Delete must return the rmErr
	// verbatim when it's neither ENOENT nor nil so upstream handlers
	// can distinguish "chat gone" (no-op) from "filesystem broken"
	// (surface to operator).
	s, _ := newTestStore(t)
	_, _ = s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	if err := os.Chmod(s.dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(s.dir, 0o700) })

	err := s.Delete(t.Context(), "c1")
	if err == nil {
		t.Fatal("Delete on readonly parent dir = nil error, want EACCES")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, unexpectedly ENOENT (file should still exist)", err)
	}
}

// --- handleExport ---
// Ported from main's pre-rewrite store_test.go: the export handler and
// its filename sanitiser survived the conversational-surface rewrite
// unchanged, so their handler-level battery comes along.

func TestHandleExport_JSONFormatReturnsChatJSON(t *testing.T) {
	s, _ := newTestStore(t)
	exportSeed(t, s, "c1", "Named Chat")

	req := httptest.NewRequest(http.MethodGet, "/api/chats/c1/export?format=json", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	disp := rec.Header().Get("Content-Disposition")
	if !strings.Contains(disp, `attachment`) || !strings.Contains(disp, "Named Chat-c1.json") {
		t.Errorf("Content-Disposition = %q, want attachment with <name>-<id>.json", disp)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"kind":"turn_open"`) || !strings.Contains(body, `"name":"Named Chat"`) {
		t.Errorf("body = %q, want the header beside the log's entries", body)
	}
}

func TestHandleExport_MarkdownIsDefaultFormat(t *testing.T) {
	s, _ := newTestStore(t)
	exportSeed(t, s, "c1", "Named Chat")

	// No ?format= param — Markdown is the default.
	req := httptest.NewRequest(http.MethodGet, "/api/chats/c1/export", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/markdown") {
		t.Errorf("Content-Type = %q, want text/markdown", ct)
	}
	disp := rec.Header().Get("Content-Disposition")
	if !strings.Contains(disp, `attachment`) || !strings.Contains(disp, "Named Chat-c1.md") {
		t.Errorf("Content-Disposition = %q, want attachment with <name>-<id>.md", disp)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "# Named Chat") || !strings.Contains(body, "**User**") {
		t.Errorf("body = %q, want Markdown transcript with title and the prompt heading", body)
	}
}

func TestHandleExport_RejectsUnsupportedFormat(t *testing.T) {
	s, _ := newTestStore(t)
	_, _ = s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })

	req := httptest.NewRequest(http.MethodGet, "/api/chats/c1/export?format=xml", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400 for unsupported format", rec.Code)
	}
}

func TestHandleExport_FallsBackToChatIDWhenNameEmpty(t *testing.T) {
	s, _ := newTestStore(t)
	_, _ = s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { return true })

	req := httptest.NewRequest(http.MethodGet, "/api/chats/c1/export", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	disp := rec.Header().Get("Content-Disposition")
	if !strings.Contains(disp, "c1.md") {
		t.Errorf("Content-Disposition = %q, want chat id fallback filename", disp)
	}
}

func TestHandleExport_NotFoundForMissingChat(t *testing.T) {
	s, _ := newTestStore(t)
	req := httptest.NewRequest(http.MethodGet, "/api/chats/c1/export", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rec.Code)
	}
}

func TestHandleExport_RejectsNonGET(t *testing.T) {
	s, _ := newTestStore(t)
	_, _ = s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/api/chats/c1/export", nil)
		rec := httptest.NewRecorder()
		NewRouter(s).handleOne(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("handleExport(%s) = %d, want 405", method, rec.Code)
		}
	}
}

func TestHandleExport_RejectsInvalidChatID(t *testing.T) {
	s, _ := newTestStore(t)
	req := httptest.NewRequest(http.MethodGet, "/api/chats/bad.id/export", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
}

func TestHandleExport_SanitisesAdversarialChatName(t *testing.T) {
	// Regression: chat names with quotes, CR/LF, or path chars used to
	// break the Content-Disposition header via string concatenation.
	// mime.FormatMediaType + safeExportName now handle all of these.
	s, _ := newTestStore(t)
	_, _ = s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "evil\"; filename=\"spoof"
		return true
	})

	req := httptest.NewRequest(http.MethodGet, "/api/chats/c1/export", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	disp := rec.Header().Get("Content-Disposition")
	if strings.Contains(disp, `filename="spoof`) && !strings.Contains(disp, `filename=`) {
		t.Errorf("header leaked injected filename= param: %q", disp)
	}
	// The sanitiser must have replaced the embedded double-quote with
	// an underscore so the header stays well-formed.
	if strings.Count(disp, `"`)%2 != 0 {
		t.Errorf("Content-Disposition has unbalanced quotes: %q", disp)
	}
}

// --- exportFilename ---

func TestExportFilename(t *testing.T) {
	tests := []struct {
		name, id, ext, want string
	}{
		{"", "c1", ".md", "c1.md"},
		{"hello", "c1", ".md", "hello-c1.md"},
		{"Named Chat", "c1", ".json", "Named Chat-c1.json"},
		{"bad/name", "c1", ".md", "bad_name-c1.md"},
		{"with\"quote", "c1", ".md", "with_quote-c1.md"},
		{"win<bad>:chars", "c1", ".md", "win_bad__chars-c1.md"},
		{"   ", "c1", ".md", "c1.md"},
		{"\x01\x02ctrl", "c1", ".md", "__ctrl-c1.md"},
		{"", "", ".md", "chat.md"},
	}
	for _, tc := range tests {
		if got := exportFilename(tc.name, tc.id, tc.ext); got != tc.want {
			t.Errorf("exportFilename(%q, %q, %q) = %q, want %q",
				tc.name, tc.id, tc.ext, got, tc.want)
		}
	}
	// Rune cap on the name stem (id and ext are appended after the cap).
	got := exportFilename(strings.Repeat("x", 200), "c1", ".md")
	if len([]rune(got)) > 80+len("-c1.md") {
		t.Errorf("len(%q) = %d runes, want stem capped at 80", got, len([]rune(got)))
	}
}

// --- UTF-8 write gate ---

// TestMutate_RejectsInvalidUTF8 pins the store's write-side UTF-8 gate:
// a mutation producing invalid UTF-8 anywhere in the chat must abort
// before save (json.Marshal would otherwise silently corrupt it to
// U+FFFD) and must not broadcast.
func TestMutate_RejectsInvalidUTF8(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(c *marotte.Chat)
	}{
		{
			name:   "invalid utf-8 in name",
			mutate: func(c *marotte.Chat) { c.Name = "bad\xff\xfename" },
		},
		{
			name:   "invalid utf-8 in the draft",
			mutate: func(c *marotte.Chat) { c.Draft = "ok\xffbad" },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, b := newTestStore(t)
			_, err := s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
				tc.mutate(c)
				return true
			})
			if !errors.Is(err, errInvalidUTF8) {
				t.Fatalf("Mutate = %v, want errInvalidUTF8", err)
			}
			if _, ok := s.Get(t.Context(), "c1"); ok {
				t.Error("invalid-UTF8 chat was persisted; the mutation must abort before save")
			}
			if n := len(b.snapshot()); n != 0 {
				t.Errorf("broadcast fired %d time(s) on a rejected mutation; want 0", n)
			}
		})
	}
}

// A completed export says nothing about failing. The line exists for a client
// that hung up mid-download, and it reads as a fact about THIS export — so
// emitting it on every success is worst exactly when someone has turned Debug
// on to find out why an export looked wrong.
func TestHandleExport_SuccessfulMarkdownWriteIsQuiet(t *testing.T) {
	logs := captureStoreSlog(t)
	s, _ := newTestStore(t)
	exportSeed(t, s, "c1", "Named Chat")

	req := httptest.NewRequest(http.MethodGet, "/api/chats/c1/export", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := logs.String(); strings.Contains(got, `msg="chat export: markdown write failed"`) {
		t.Errorf("a successful export logged a write failure; logs = %q", got)
	}
}

// TestMutate_RefusesACancelledContext pins the guard at Mutate's entry, untested
// until now, and it is the negative that keeps the durable-write decision
// enforceable from this side.
//
// Deleting the guard is the cheap way to stop a shutdown discarding an assistant
// turn, and it opens nine request-context sites at once: a rewind truncation, a
// user message, a membership change would all persist for a POST the client
// abandoned. The caller detaches instead — only it can tell the two apart.
func TestMutate_RefusesACancelledContext(t *testing.T) {
	s, b := newTestStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var mutatorRan bool
	_, err := s.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool {
		mutatorRan = true
		c.Name = "written for a request nobody is waiting on"
		return true
	})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Mutate on a cancelled context = %v, want context.Canceled", err)
	}
	if mutatorRan {
		t.Error("the mutator ran, so the guard sits below the load rather than at the entry")
	}
	if _, exists := s.Get(t.Context(), "c1"); exists {
		t.Error("a refused Mutate created the chat anyway")
	}
	if events := b.snapshot(); len(events) != 0 {
		t.Errorf("a refused Mutate broadcast %d events, want none", len(events))
	}
}

// exportSeed writes one named chat holding a prompt turn with a reply, the shape
// the export handlers render.
func exportSeed(t *testing.T, s *Store, id marotte.ChatID, name string) {
	t.Helper()
	turn := openPromptTurn(t, s, id, "m-"+string(id))
	if _, err := s.Mutate(t.Context(), id, func(c *marotte.Chat, _ bool) bool { c.Name = name; return true }); err != nil {
		t.Fatalf("Mutate(name): %v", err)
	}
	if err := s.Append(t.Context(), id, entryOf(turn, "", "a1", marotte.EntryKindText, marotte.EntryText{Text: "hi back"})); err != nil {
		t.Fatalf("Append: %v", err)
	}
}
