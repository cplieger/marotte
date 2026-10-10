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

// fakeBroadcaster captures broadcasts for assertions, guarded by mu against parallel Append
// goroutines.
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

func (f *fakeBroadcaster) snapshot() []marotte.ServerEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.events)
}

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

// ageChat backdates a chat so it is eligible for purge, writing BOTH UpdatedAt (what purge ages
// from) and the mtime (the fallback for an unreadable chat).
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

var badChatIDs = []marotte.ChatID{"", "a/b", "..", "a\x00b", "a b", marotte.ChatID(strings.Repeat("x", 200))}

func assertRejectsBadChatIDs(t *testing.T, fn func(id marotte.ChatID) error) {
	t.Helper()
	for _, bad := range badChatIDs {
		if err := fn(bad); err == nil {
			t.Errorf("accepted bad id %q", bad)
		}
	}
}

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

func TestChatIDPattern(t *testing.T) {
	valid := []string{
		"abc",
		"ABC",
		"01HXYZ",
		"550e8400-e29b-41d4-a716-446655440000",
		"chat-1716000000000",
		"a_b",
		"a-b",
		strings.Repeat("x", 128),
	}
	for _, id := range valid {
		if !chatIDPattern(marotte.ChatID(id)) {
			t.Errorf("chatIDPattern(%q) = false, want true", id)
		}
	}

	invalid := []string{
		"",
		"a/b",
		"..",
		"a.b",
		"a b",
		"a\x00b",
		"a\nb",
		strings.Repeat("x", 129),
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

// TestList_SortsByUpdatedAtDesc runs in a synctest bubble so the gap between the two timestamps is
// exact; transient file I/O does not defeat the bubble.
func TestList_SortsByUpdatedAtDesc(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _ := newTestStore(t)
		_, _ = s.Mutate(t.Context(), "a", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
		synctest.Sleep(2 * time.Millisecond)
		_, _ = s.Mutate(t.Context(), "b", func(c *marotte.Chat, _ bool) bool { c.Name = "B"; return true })
		synctest.Sleep(2 * time.Millisecond)
		_, _ = s.Mutate(t.Context(), "a", func(c *marotte.Chat, _ bool) bool { return true })
		headers := s.List(t.Context())
		if len(headers) != 2 {
			t.Fatalf("len = %d, want 2", len(headers))
		}
		if headers[0].ID != "a" {
			t.Errorf("first = %q, want a (most recently updated)", headers[0].ID)
		}
		// A real clock could only assert `> 0`, which a save stamping the wrong field, or reusing
		// one stamp, would satisfy.
		if gap := headers[0].UpdatedAt - headers[1].UpdatedAt; gap != 2 {
			t.Errorf("UpdatedAt gap = %dms, want exactly 2 (b at +2ms, a re-stamped at +4ms)", gap)
		}
	})
}

func TestList_IgnoresNonChatFiles(t *testing.T) {
	s, _ := newTestStore(t)
	_, _ = s.Mutate(t.Context(), "a", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	if err := os.WriteFile(filepath.Join(s.dir, "random.txt"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
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
	badPath := filepath.Join(s.dir, "bad.json")
	if err := os.WriteFile(badPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	headers := s.List(t.Context())
	if len(headers) != 1 || headers[0].ID != "good" {
		t.Errorf("List() = %+v, want only the good chat", headers)
	}
}

func TestDelete_RemovesFileAndBroadcasts(t *testing.T) {
	s, b := newTestStore(t)
	_, _ = s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	b.reset()

	if _, err := s.Delete(t.Context(), "c1"); err != nil {
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
	if _, err := s.Delete(t.Context(), "nonexistent"); err != nil {
		t.Errorf("error on missing chat: %v", err)
	}
	if len(b.events) != 1 {
		t.Errorf("events: %+v", b.events)
	}
}

func TestDelete_TombstonesChatID(t *testing.T) {
	s, _ := newTestStore(t)
	_, _ = s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	_, _ = s.Delete(t.Context(), "c1")
	if !s.isTombstoned("c1") {
		t.Error("tombstone not set after Delete")
	}
}

func TestMutate_RefusesToCreateTombstonedChat(t *testing.T) {
	s, b := newTestStore(t)
	_, _ = s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	_, _ = s.Delete(t.Context(), "c1")
	b.reset()

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
	for _, e := range b.snapshot() {
		if e.Type == "chat_created" || e.Type == "chat_updated" {
			t.Errorf("unexpected event after tombstoned mutate: %+v", e)
		}
	}
}

// The three outcomes of the single write path, pinned apart in one test:
// applied and no-op are both nil, and refused is ErrTombstoned. A regression that reports
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
	if _, err := s.Delete(t.Context(), "c2"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err := s.Mutate(t.Context(), "c2", func(c *marotte.Chat, _ bool) bool { c.Name = "ghost"; return true })
	if !errors.Is(err, ErrTombstoned) {
		t.Errorf("Mutate(refused) = %v, want ErrTombstoned", err)
	}
}

func TestMutate_UpdatingExistingChatIsNotBlockedByTombstone(t *testing.T) {
	s, _ := newTestStore(t)
	_, _ = s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	_, _ = s.Delete(t.Context(), "c1")
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
	_, _ = s.Delete(t.Context(), "c1")
	b.reset()

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

func TestHandleList_ReturnsHeaders(t *testing.T) {
	s, _ := newTestStore(t)
	openPromptTurn(t, s, "c1", "m-1")
	if err := s.WriteCounters(t.Context(), "c1"); err != nil {
		t.Fatalf("WriteCounters: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/chats", nil)
	rec := httptest.NewRecorder()
	newRouter(s).handleList(rec, req)

	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"turn_count":1`) {
		t.Errorf("body = %q, want the header's turn_count", body)
	}
	if strings.Contains(body, `"entries"`) || strings.Contains(body, `"payload"`) {
		t.Errorf("entries leaked into list response: %q", body)
	}
}

func TestHandleOne_NotFound(t *testing.T) {
	s, _ := newTestStore(t)
	req := httptest.NewRequest(http.MethodGet, "/api/chats/nope", nil)
	rec := httptest.NewRecorder()
	newRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("code = %d", rec.Code)
	}
}

func TestHandleOne_RejectsUnknownSubResource(t *testing.T) {
	s, _ := newTestStore(t)
	req := httptest.NewRequest(http.MethodGet, "/api/chats/a/b", nil)
	rec := httptest.NewRecorder()
	newRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rec.Code)
	}
}

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

func TestHandleList_EmptyStoreReturnsEmptyArrayNotNull(t *testing.T) {
	// List must return a non-nil slice: a nil slice encodes as null, which the wire decoder
	// rejects.
	s, _ := newTestStore(t)

	req := httptest.NewRequest(http.MethodGet, "/api/chats", nil)
	rec := httptest.NewRecorder()
	newRouter(s).handleList(rec, req)

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
		newRouter(s).handleList(rec, req)
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
		newRouter(s).handleOne(rec, req)
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
		newRouter(s).handleOne(rec, req)
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
		newRouter(s).handleOne(rec, req)
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

	req := httptest.NewRequest(http.MethodGet, "/api/chats", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/chats code = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"id":"c1"`) {
		t.Errorf("GET /api/chats body missing c1: %s", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/chats/c1", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/chats/c1 code = %d, want 200", rec.Code)
	}
}

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

func TestIsTombstoned_ExpiredEntryIsPrunedAndReturnsFalse(t *testing.T) {
	s, _ := newTestStore(t)
	s.tombMu.Lock()
	s.tombstone["c1"] = tombstone{at: time.Now().Add(-2 * tombstoneTTL)}
	s.tombMu.Unlock()

	if s.isTombstoned("c1") {
		t.Error("isTombstoned returned true for expired tombstone")
	}
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
	_, _ = s.Delete(t.Context(), "c1")

	s.tombMu.Lock()
	s.tombstone["c1"] = tombstone{at: time.Now().Add(-2 * tombstoneTTL)}
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
	now := time.Now()
	s.tombMu.Lock()
	s.tombstone["expired-1"] = tombstone{at: now.Add(-2 * tombstoneTTL)}
	s.tombstone["expired-2"] = tombstone{at: now.Add(-3 * tombstoneTTL)}
	s.tombstone["expired-3"] = tombstone{at: now.Add(-5 * tombstoneTTL)}
	s.tombstone["fresh"] = tombstone{at: now.Add(-time.Second)}
	s.tombMu.Unlock()

	s.markDeleted("new-delete", "")

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

func TestDepartedName_IsTheNameTheChatHadWhenDeleted(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "Release notes"; return true }); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if name, ok := s.DepartedName("c1"); ok {
		t.Fatalf("DepartedName(c1) before delete = %q, true; want false", name)
	}
	if _, err := s.Delete(t.Context(), "c1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if name, ok := s.DepartedName("c1"); !ok || name != "Release notes" {
		t.Errorf("DepartedName(c1) = %q, %v; want %q, true", name, ok, "Release notes")
	}
	if name, ok := s.DepartedName("never"); ok {
		t.Errorf("DepartedName(never) = %q, true; want false", name)
	}
}

func TestDelete_MissingChatDoesNotTombstone(t *testing.T) {
	// A stale DELETE for an id the server never knew still broadcasts, but must not tombstone the
	// id and block a legitimate create for 10 minutes.
	s, _ := newTestStore(t)
	_, _ = s.Delete(t.Context(), "never-existed")
	if s.isTombstoned("never-existed") {
		t.Error("phantom delete tombstoned a chat that never existed")
	}
	_, err := s.Mutate(t.Context(), "never-existed", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	if err != nil {
		t.Fatalf("Mutate after phantom delete: %v", err)
	}
	if _, ok := s.Get(t.Context(), "never-existed"); !ok {
		t.Error("chat not created after phantom delete")
	}
}

func TestNewStore_MkdirFailurePropagatesError(t *testing.T) {
	// A regular file in the path makes MkdirAll fail; the constructor must surface it so startup
	// fails.
	base := t.TempDir()
	blocker := filepath.Join(base, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err := NewStore(filepath.Join(blocker, "chats"))
	if err == nil {
		t.Fatal("NewStore(path-through-file) = nil error, want non-nil")
	}
	if !strings.Contains(err.Error(), "chat store: mkdir") {
		t.Errorf("NewStore error = %q, want wrapping message with prefix %q",
			err.Error(), "chat store: mkdir")
	}
}

func TestMutate_PropagatesParseErrorDoesNotOverwrite(t *testing.T) {
	// A corrupted chat file must not be silently overwritten by Mutate's auto-create path.
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

func TestMutate_RefusesMutatorReassigningChatID(t *testing.T) {
	s, _ := newTestStore(t)
	_, err := s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.ID = "c2"
		c.Name = "stolen"
		return true
	})
	if err == nil {
		t.Fatal("Mutate accepted mutator that reassigned c.ID")
	}
	if !strings.Contains(err.Error(), "reassigned id") {
		t.Errorf("error = %q, want mention of reassigned id", err.Error())
	}
	if _, ok := s.Get(t.Context(), "c1"); ok {
		t.Error("c1 was written despite mutator reassigning id")
	}
	if _, ok := s.Get(t.Context(), "c2"); ok {
		t.Error("c2 was written via the reassigned id")
	}
}

func TestHandleOne_RejectsInvalidChatID(t *testing.T) {
	s, _ := newTestStore(t)
	for _, bad := range []string{"bad.id", "with@sign", "plus+sign"} {
		req := httptest.NewRequest(http.MethodGet, "/api/chats/"+bad, nil)
		rec := httptest.NewRecorder()
		newRouter(s).handleOne(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("handleOne(%q) = %d, want 400", bad, rec.Code)
		}
	}
}

// skipIfRoot skips the test under euid 0, where chmod-to-readonly cannot trip EACCES.
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("chmod-based permission tests skipped when running as root")
	}
}

func TestDelete_RejectsBadChatID(t *testing.T) {
	// Delete validates the id before any filesystem op: every invalid id errors AND broadcasts
	// nothing.
	s, b := newTestStore(t)
	assertRejectsBadChatIDs(t, func(id marotte.ChatID) error {
		_, err := s.Delete(t.Context(), id)
		return err
	})
	if evs := b.snapshot(); len(evs) != 0 {
		t.Errorf("invalid chat id deletes broadcast events: %+v", evs)
	}
}

func TestDelete_SurfacesNonENOENTChatRemoveError(t *testing.T) {
	skipIfRoot(t)
	s, _ := newTestStore(t)
	_, _ = s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	if err := os.Chmod(s.dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(s.dir, 0o700) })

	_, err := s.Delete(t.Context(), "c1")
	if err == nil {
		t.Fatal("Delete on readonly parent dir = nil error, want EACCES")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, unexpectedly ENOENT (file should still exist)", err)
	}
}

func TestHandleExport_JSONFormatReturnsChatJSON(t *testing.T) {
	s, _ := newTestStore(t)
	exportSeed(t, s, "c1", "Named Chat")

	req := httptest.NewRequest(http.MethodGet, "/api/chats/c1/export?format=json", nil)
	rec := httptest.NewRecorder()
	newRouter(s).handleOne(rec, req)

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

	req := httptest.NewRequest(http.MethodGet, "/api/chats/c1/export", nil)
	rec := httptest.NewRecorder()
	newRouter(s).handleOne(rec, req)

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
	newRouter(s).handleOne(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400 for unsupported format", rec.Code)
	}
}

func TestHandleExport_FallsBackToChatIDWhenNameEmpty(t *testing.T) {
	s, _ := newTestStore(t)
	_, _ = s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { return true })

	req := httptest.NewRequest(http.MethodGet, "/api/chats/c1/export", nil)
	rec := httptest.NewRecorder()
	newRouter(s).handleOne(rec, req)

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
	newRouter(s).handleOne(rec, req)
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
		newRouter(s).handleOne(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("handleExport(%s) = %d, want 405", method, rec.Code)
		}
	}
}

func TestHandleExport_RejectsInvalidChatID(t *testing.T) {
	s, _ := newTestStore(t)
	req := httptest.NewRequest(http.MethodGet, "/api/chats/bad.id/export", nil)
	rec := httptest.NewRecorder()
	newRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
}

func TestHandleExport_SanitisesAdversarialChatName(t *testing.T) {
	s, _ := newTestStore(t)
	_, _ = s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "evil\"; filename=\"spoof"
		return true
	})

	req := httptest.NewRequest(http.MethodGet, "/api/chats/c1/export", nil)
	rec := httptest.NewRecorder()
	newRouter(s).handleOne(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	disp := rec.Header().Get("Content-Disposition")
	if strings.Contains(disp, `filename="spoof`) && !strings.Contains(disp, `filename=`) {
		t.Errorf("header leaked injected filename= param: %q", disp)
	}
	if strings.Count(disp, `"`)%2 != 0 {
		t.Errorf("Content-Disposition has unbalanced quotes: %q", disp)
	}
}

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
	got := exportFilename(strings.Repeat("x", 200), "c1", ".md")
	if len([]rune(got)) > 80+len("-c1.md") {
		t.Errorf("len(%q) = %d runes, want stem capped at 80", got, len([]rune(got)))
	}
}

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
	newRouter(s).handleOne(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := logs.String(); strings.Contains(got, `msg="chat export: markdown write failed"`) {
		t.Errorf("a successful export logged a write failure; logs = %q", got)
	}
}

// TestMutate_RefusesACancelledContext pins the guard at Mutate's entry: deleting it would let every
// request-context write persist for a POST the client abandoned. A caller that needs the write
// detaches instead.
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
