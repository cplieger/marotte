package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/webhttp/v3"
)

func idemHandler(status int, ct, body string) (http.Handler, *atomic.Int32) {
	var calls atomic.Int32
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		if ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
	return h, &calls
}

func idemReq(method, path, key string) *http.Request {
	req := httptest.NewRequest(method, "http://example.com"+path, http.NoBody)
	if key != "" {
		req.Header.Set(idempotencyHeader, key)
	}
	return req
}

func serveIdem(mw http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	return rec
}

// TestIdempotency_replaysCachedResponse pins that a repeated method+path+key replays the
// cached status, body and Content-Type without re-invoking the handler.
func TestIdempotency_replaysCachedResponse(t *testing.T) {
	c := newIdempotencyCache(idempotencyTTL)
	defer c.stop()
	h, calls := idemHandler(http.StatusCreated, "application/json", `{"ok":true}`)
	mw := c.middleware(h)

	rec1 := serveIdem(mw, idemReq(http.MethodPost, "/api/git/stash", "k1"))
	if calls.Load() != 1 {
		t.Fatalf("first request: handler calls = %d, want 1", calls.Load())
	}
	if rec1.Code != http.StatusCreated || rec1.Body.String() != `{"ok":true}` {
		t.Fatalf("first response: %d %q", rec1.Code, rec1.Body.String())
	}

	rec2 := serveIdem(mw, idemReq(http.MethodPost, "/api/git/stash", "k1"))
	if calls.Load() != 1 {
		t.Fatalf("replay: handler calls = %d, want 1 (should not re-run)", calls.Load())
	}
	if rec2.Code != rec1.Code {
		t.Errorf("replay status = %d, want %d", rec2.Code, rec1.Code)
	}
	if rec2.Body.String() != rec1.Body.String() {
		t.Errorf("replay body = %q, want %q", rec2.Body.String(), rec1.Body.String())
	}
	if got, want := rec2.Header().Get("Content-Type"), rec1.Header().Get("Content-Type"); got != want {
		t.Errorf("replay Content-Type = %q, want %q", got, want)
	}
}

// (3): a different key is a fresh request — the handler runs again.
func TestIdempotency_differentKeyReexecutes(t *testing.T) {
	c := newIdempotencyCache(idempotencyTTL)
	defer c.stop()
	h, calls := idemHandler(http.StatusOK, "application/json", "x")
	mw := c.middleware(h)

	serveIdem(mw, idemReq(http.MethodPost, "/api/git/stash", "k1"))
	serveIdem(mw, idemReq(http.MethodPost, "/api/git/stash", "k2"))
	if calls.Load() != 2 {
		t.Fatalf("handler calls = %d, want 2 (distinct keys)", calls.Load())
	}
}

// TestIdempotency_sameKeyDifferentPathReexecutes pins that the composite key keeps routes
// from colliding.
func TestIdempotency_sameKeyDifferentPathReexecutes(t *testing.T) {
	c := newIdempotencyCache(idempotencyTTL)
	defer c.stop()
	h, calls := idemHandler(http.StatusOK, "application/json", "x")
	mw := c.middleware(h)

	serveIdem(mw, idemReq(http.MethodPost, "/api/git/stash", "same"))
	serveIdem(mw, idemReq(http.MethodPost, "/api/git/stash-pop", "same"))
	if calls.Load() != 2 {
		t.Fatalf("handler calls = %d, want 2 (different path, same key)", calls.Load())
	}
}

// TestIdempotency_passthrough pins that safe methods, a missing header, a control-char key
// and an over-length key are never deduped.
func TestIdempotency_passthrough(t *testing.T) {
	longKey := strings.Repeat("a", maxIdempotencyKeyBytes+1)
	cases := []struct {
		name   string
		method string
		key    string
	}{
		{"GET with key", http.MethodGet, "g1"},
		{"HEAD with key", http.MethodHead, "h1"},
		{"OPTIONS with key", http.MethodOptions, "o1"},
		{"POST missing key", http.MethodPost, ""},
		{"POST control-char key", http.MethodPost, "bad\nkey"},
		{"POST oversized key", http.MethodPost, longKey},
		{"PUT oversized key", http.MethodPut, longKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newIdempotencyCache(idempotencyTTL)
			defer c.stop()
			h, calls := idemHandler(http.StatusOK, "application/json", "ok")
			mw := c.middleware(h)

			serveIdem(mw, idemReq(tc.method, "/api/git/stash", tc.key))
			serveIdem(mw, idemReq(tc.method, "/api/git/stash", tc.key))
			if calls.Load() != 2 {
				t.Fatalf("handler calls = %d, want 2 (no dedup)", calls.Load())
			}
		})
	}
}

// (8): a 5xx is transient and must not be cached, so a retry can
// re-execute against a possibly-recovered backend.
func TestIdempotency_serverErrorNotCached(t *testing.T) {
	c := newIdempotencyCache(idempotencyTTL)
	defer c.stop()
	h, calls := idemHandler(http.StatusServiceUnavailable, "application/json", `{"error":"x"}`)
	mw := c.middleware(h)

	rec1 := serveIdem(mw, idemReq(http.MethodPost, "/api/git/push", "boom"))
	rec2 := serveIdem(mw, idemReq(http.MethodPost, "/api/git/push", "boom"))
	if calls.Load() != 2 {
		t.Fatalf("handler calls = %d, want 2 (5xx must re-execute)", calls.Load())
	}
	if rec1.Code != http.StatusServiceUnavailable || rec2.Code != http.StatusServiceUnavailable {
		t.Fatalf("statuses = %d, %d, want 503, 503", rec1.Code, rec2.Code)
	}
}

// (9): a 4xx is a deterministic outcome — cache and replay it.
func TestIdempotency_clientErrorCachedAndReplayed(t *testing.T) {
	c := newIdempotencyCache(idempotencyTTL)
	defer c.stop()
	h, calls := idemHandler(http.StatusConflict, "application/json", `{"error":"dup"}`)
	mw := c.middleware(h)

	rec1 := serveIdem(mw, idemReq(http.MethodPost, "/api/git/stash", "c4"))
	rec2 := serveIdem(mw, idemReq(http.MethodPost, "/api/git/stash", "c4"))
	if calls.Load() != 1 {
		t.Fatalf("handler calls = %d, want 1 (4xx should replay)", calls.Load())
	}
	if rec1.Code != http.StatusConflict || rec2.Code != http.StatusConflict {
		t.Fatalf("statuses = %d, %d, want 409, 409", rec1.Code, rec2.Code)
	}
	if rec2.Body.String() != `{"error":"dup"}` {
		t.Errorf("replay body = %q", rec2.Body.String())
	}
}

// TestIdempotency_completeDoesNotAliasTheCallerBody pins complete's copy contract: no test
// caught a straight assignment, and the copy guards against a future pooled writer.
func TestIdempotency_completeDoesNotAliasTheCallerBody(t *testing.T) {
	c := newIdempotencyCache(idempotencyTTL)
	defer c.stop()

	buf := []byte(`{"ok":true}`)
	c.complete("k", http.StatusOK, "application/json", buf)
	copy(buf, `{"ok":FALS`) // the caller reuses its buffer

	e, inflight := c.begin("k")
	if e == nil || inflight {
		t.Fatalf("begin returned entry=%v inflight=%v, want the cached entry", e, inflight)
	}
	if got := string(e.body); got != `{"ok":true}` {
		t.Errorf("cached body = %q, want %q (complete aliased the caller's buffer)", got, `{"ok":true}`)
	}
}

// TestIdempotency_ttlExpiryReexecutes pins lazy TTL eviction at the PRODUCTION
// idempotencyTTL in a synctest bubble. The 30-second offset is load-bearing: without it the
// entry expires exactly on a janitor tick, which evicts it before begin's own boundary runs.
func TestIdempotency_ttlExpiryReexecutes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newIdempotencyCache(idempotencyTTL)
		defer c.stop()
		h, calls := idemHandler(http.StatusOK, "application/json", "x")
		mw := c.middleware(h)

		synctest.Sleep(30 * time.Second) // see the offset note above

		serveIdem(mw, idemReq(http.MethodPost, "/api/git/stash", "ttl"))
		serveIdem(mw, idemReq(http.MethodPost, "/api/git/stash", "ttl")) // replay, within TTL
		if calls.Load() != 1 {
			t.Fatalf("within TTL: handler calls = %d, want 1", calls.Load())
		}

		// A replay does not re-stamp ts, so age is measured from the first request.
		synctest.Sleep(idempotencyTTL - time.Nanosecond)
		serveIdem(mw, idemReq(http.MethodPost, "/api/git/stash", "ttl"))
		if calls.Load() != 1 {
			t.Fatalf("at TTL-1ns: handler calls = %d, want 1 (still inside the window)", calls.Load())
		}

		synctest.Sleep(time.Nanosecond)
		serveIdem(mw, idemReq(http.MethodPost, "/api/git/stash", "ttl"))
		if calls.Load() != 2 {
			t.Fatalf("at exactly TTL: handler calls = %d, want 2 (the window is half-open: `< ttl` replays)", calls.Load())
		}
	})
}

// TestIdempotency_concurrentDuplicateGets409 pins that a truly-concurrent duplicate gets 409
// and the handler runs once.
func TestIdempotency_concurrentDuplicateGets409(t *testing.T) {
	c := newIdempotencyCache(idempotencyTTL)
	defer c.stop()

	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	mw := c.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		once.Do(func() { close(entered) })
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))

	rec1 := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		mw.ServeHTTP(rec1, idemReq(http.MethodPost, "/api/git/stash", "cc"))
		close(done)
	}()

	<-entered // first request is now inside the handler, holding in-flight
	rec2 := serveIdem(mw, idemReq(http.MethodPost, "/api/git/stash", "cc"))
	close(release)
	<-done

	if rec2.Code != http.StatusConflict {
		t.Fatalf("concurrent duplicate: status = %d, want 409", rec2.Code)
	}
	if rec1.Code != http.StatusOK || rec1.Body.String() != `{"ok":true}` {
		t.Fatalf("first request: %d %q", rec1.Code, rec1.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("handler calls = %d, want 1", calls.Load())
	}
}

// TestIdempotency_oversizeBodyNotCached pins that an over-cap response is written through
// in full but not cached.
func TestIdempotency_oversizeBodyNotCached(t *testing.T) {
	c := newIdempotencyCache(idempotencyTTL)
	c.maxBody = 16 // tiny cap so the test stays cheap
	defer c.stop()
	big := strings.Repeat("x", 100)
	h, calls := idemHandler(http.StatusOK, "application/json", big)
	mw := c.middleware(h)

	rec1 := serveIdem(mw, idemReq(http.MethodPost, "/api/git/stash", "fat"))
	rec2 := serveIdem(mw, idemReq(http.MethodPost, "/api/git/stash", "fat"))
	if calls.Load() != 2 {
		t.Fatalf("handler calls = %d, want 2 (oversize must not cache)", calls.Load())
	}
	if rec1.Body.String() != big || rec2.Body.String() != big {
		t.Fatalf("client did not receive full body through the cap")
	}
}

// TestIdempotency_sweepDropsExpiredKeepsInflightAndFresh pins that sweep drops expired
// completed entries and never an in-flight marker.
func TestIdempotency_sweepDropsExpiredKeepsInflightAndFresh(t *testing.T) {
	c := newIdempotencyCache(time.Hour) // long TTL; sweep is driven directly
	defer c.stop()
	now := time.Now()
	c.mu.Lock()
	c.entries["old"] = &idempotencyEntry{ts: now.Add(-2 * time.Hour), status: http.StatusOK}
	c.entries["fresh"] = &idempotencyEntry{ts: now, status: http.StatusOK}
	c.entries["busy"] = &idempotencyEntry{ts: now.Add(-2 * time.Hour), inflight: true}
	c.mu.Unlock()

	c.sweep(now)

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries["old"]; ok {
		t.Error("expired completed entry was not swept")
	}
	if _, ok := c.entries["fresh"]; !ok {
		t.Error("fresh completed entry was wrongly swept")
	}
	if _, ok := c.entries["busy"]; !ok {
		t.Error("in-flight marker was wrongly age-swept")
	}
}

// TestValidIdempotencyKey: opaque composite keys pass; empty, control-char and over-length
// keys do not.
func TestValidIdempotencyKey(t *testing.T) {
	cases := []struct {
		name string
		key  string
		want bool
	}{
		{"uuid-like", "018f-2a1c-7e", true},
		{"composite file rename", "files.rename:dir/old.txt->dir/new.txt", true},
		{"composite with spaces", "files.rename:a/old name.txt->a/new name.txt", true},
		{"utf8 filename", "files.create:dir/café.txt", true},
		{"empty", "", false},
		{"newline", "abc\ndef", false},
		{"carriage return", "abc\rdef", false},
		{"nul", "abc\x00def", false},
		{"tab", "abc\tdef", false},
		{"del", "abc\x7fdef", false},
		{"too long", strings.Repeat("a", maxIdempotencyKeyBytes+1), false},
		{"at limit", strings.Repeat("a", maxIdempotencyKeyBytes), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validIdempotencyKey(tc.key); got != tc.want {
				t.Errorf("validIdempotencyKey(%q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

// idempotentMethod gates which methods participate. Mutations dedup;
// safe methods and anything else pass through.
func TestIdempotentMethod(t *testing.T) {
	cases := map[string]bool{
		http.MethodPost:    true,
		http.MethodPut:     true,
		http.MethodPatch:   true,
		http.MethodDelete:  true,
		http.MethodGet:     false,
		http.MethodHead:    false,
		http.MethodOptions: false,
		http.MethodConnect: false,
		http.MethodTrace:   false,
	}
	for method, want := range cases {
		if got := idempotentMethod(method); got != want {
			t.Errorf("idempotentMethod(%q) = %v, want %v", method, got, want)
		}
	}
}

// TestIdempotency_sweepDeletesEntryAtExactTTL pins the sweep's inclusive TTL boundary.
func TestIdempotency_sweepDeletesEntryAtExactTTL(t *testing.T) {
	c := &idempotencyCache{
		entries: map[string]*idempotencyEntry{},
		ttl:     100 * time.Millisecond,
	}
	now := time.Now()
	c.entries["k"] = &idempotencyEntry{ts: now.Add(-c.ttl)} // age == ttl exactly

	c.sweep(now)

	if _, ok := c.entries["k"]; ok {
		t.Errorf("sweep kept an entry aged exactly ttl; want deleted (inclusive boundary)")
	}
}

// TestIdempotency_beginEvictsAtCapacity verifies that begin, called when the
// cache is exactly at capacity, evicts the oldest completed entry before
// adding the new key, so the map size stays at the cap and the oldest entry is
// gone.
func TestIdempotency_beginEvictsAtCapacity(t *testing.T) {
	c := &idempotencyCache{
		entries:    map[string]*idempotencyEntry{},
		ttl:        time.Hour,
		maxEntries: 2,
	}
	now := time.Now()
	c.entries["old"] = &idempotencyEntry{ts: now.Add(-2 * time.Minute)}
	c.entries["new"] = &idempotencyEntry{ts: now.Add(-1 * time.Minute)}

	replay, inflight := c.begin("fresh")
	if replay != nil || inflight {
		t.Fatalf("begin(fresh) = (%v, %v), want (nil, false) for a new key", replay, inflight)
	}
	if got := len(c.entries); got != 2 {
		t.Errorf("len(entries) after begin at capacity = %d, want 2 (oldest evicted, new added)", got)
	}
	if _, ok := c.entries["old"]; ok {
		t.Errorf("oldest completed entry survived; want evicted at capacity")
	}
}

// TestIdempotency_middlewareDoesNotCache500 pins that exactly 500 is not cached.
func TestIdempotency_middlewareDoesNotCache500(t *testing.T) {
	c := &idempotencyCache{
		entries:    map[string]*idempotencyEntry{},
		ttl:        idempotencyTTL,
		maxEntries: idempotencyMaxEntries,
		maxBody:    idempotencyMaxBody,
	}
	h, _ := idemHandler(http.StatusInternalServerError, "application/json", `{"err":1}`)
	mw := c.middleware(h)

	rec := serveIdem(mw, idemReq(http.MethodPost, "/x", "k500"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("response status = %d, want 500", rec.Code)
	}

	ck := idempotencyCompositeKey(http.MethodPost, "/x", "k500")
	if _, cached := c.entries[ck]; cached {
		t.Errorf("a 500 response was cached; want aborted (status < 500 boundary)")
	}
}

// TestIdempotency_writerBuffersExactlyAtLimit verifies the buffering boundary
// is inclusive of the limit: writing exactly `limit` bytes from an empty
// buffer does not overflow, so the full body is buffered (and thus cacheable).
func TestIdempotency_writerBuffersExactlyAtLimit(t *testing.T) {
	rec := httptest.NewRecorder()
	cw := &idempotencyWriter{rec: webhttp.NewStatusRecorder(rec), limit: 4}

	n, err := cw.Write([]byte("abcd")) // exactly limit bytes
	if err != nil || n != 4 {
		t.Fatalf("Write(4 bytes) = (%d, %v), want (4, nil)", n, err)
	}
	if cw.overflow {
		t.Errorf("writing exactly limit bytes set overflow=true; want false (inclusive boundary)")
	}
	if got := cw.buf.String(); got != "abcd" {
		t.Errorf("buffered = %q, want %q", got, "abcd")
	}
}

// TestIdempotency_commandRouteParticipates pins the WIRING: POST /api/command sits inside
// the production stack's dedup layer (s.middlewareStack, not a hand-built chain).
func TestIdempotency_commandRouteParticipates(t *testing.T) {
	handler, calls := idemHandler(http.StatusOK, "application/json", `{"ok":true}`)
	mux := http.NewServeMux()
	mux.Handle("POST /api/command", handler)

	idem := newIdempotencyCache(idempotencyTTL)
	t.Cleanup(idem.stop)
	s := New()
	h := webhttp.Chain(mux, s.middlewareStack(baseCSPPolicy, idem)...)

	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "http://example.com/api/command",
			strings.NewReader(`{"type":"create_chat","chat_id":"c1"}`))
		req.Header.Set("Content-Type", "application/json")
		// Same-origin so the CSRF check outside this layer lets it through.
		req.Header.Set("Origin", "http://example.com")
		req.Header.Set(idempotencyHeader, "r-abc123")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	first := post()
	if first.Code != http.StatusOK {
		t.Fatalf("first POST = %d, want 200 (body %q)", first.Code, first.Body.String())
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("handler calls after first POST = %d, want 1", got)
	}

	second := post()
	if second.Code != http.StatusOK {
		t.Errorf("replayed POST = %d, want 200", second.Code)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("handler calls after duplicate = %d, want 1 — the command route is "+
			"outside the idempotency middleware", got)
	}
	if first.Body.String() != second.Body.String() {
		t.Errorf("replay body = %q, want the cached %q", second.Body.String(), first.Body.String())
	}
}
