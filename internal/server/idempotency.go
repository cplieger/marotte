// Package server — the Idempotency-Key dedup middleware.
//
// A retried mutation carrying a stable `Idempotency-Key` header replays the first response
// instead of re-executing. This is the app's ONLY idempotency layer, POST /api/command
// included; do not add a body-keyed dedup beside it.
package server

import (
	"bytes"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/webhttp/v3"
)

// idempotencyHeader is the request header carrying the client's
// idempotency key, set by @cplieger/actions apiAction.
const idempotencyHeader = "Idempotency-Key"

// idempotencyTTL: 5 minutes is long enough to cover a client's network-retry
// window after a transient failure, short enough that a stale replay
// can't outlive the user's intent.
const idempotencyTTL = 5 * time.Minute

// idempotencyMaxEntries bounds the number of cached entries as a hard
// memory ceiling between janitor sweeps.
const idempotencyMaxEntries = 10_000

// idempotencyMaxBody caps the response body buffered for replay; a larger response is
// written through but not cached.
const idempotencyMaxBody = 1 << 20

// Keys are opaque and may contain '/', ':', '->' and spaces.
const maxIdempotencyKeyBytes = 256

// GET/HEAD/OPTIONS (and anything else) pass straight through: they are either safe/idempotent
// already or carry no mutation to replay.
func idempotentMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// validIdempotencyKey reports whether key is safe as a dedup key: non-empty, within the byte
// cap, and free of control characters (header injection, log forging). Permissive about
// charset, because rejecting a valid key would silently disable dedup.
func validIdempotencyKey(key string) bool {
	if key == "" || len(key) > maxIdempotencyKeyBytes {
		return false
	}
	for i := range len(key) {
		if key[i] < 0x20 || key[i] == 0x7f {
			return false
		}
	}
	return true
}

// idempotencyCompositeKey scopes the key to method+path+key; NUL separators keep the parts
// unambiguous.
func idempotencyCompositeKey(method, path, key string) string {
	return method + "\x00" + path + "\x00" + key
}

// An in-flight slot (inflight = true) marks a request currently executing under the key; a
// completed slot carries the captured response for replay until ts+ttl.
type idempotencyEntry struct {
	ts       time.Time
	ct       string
	body     []byte
	status   int
	inflight bool
}

// idempotencyCache deduplicates REST mutations by composite key, never holding its lock
// across the wrapped handler.
type idempotencyCache struct {
	entries    map[string]*idempotencyEntry
	done       chan struct{}
	ttl        time.Duration
	maxEntries int
	maxBody    int
	mu         sync.Mutex
	stopOnce   sync.Once
}

// newIdempotencyCache constructs a cache with the given TTL and starts its janitor; call stop().
func newIdempotencyCache(ttl time.Duration) *idempotencyCache {
	c := &idempotencyCache{
		entries:    make(map[string]*idempotencyEntry),
		done:       make(chan struct{}),
		ttl:        ttl,
		maxEntries: idempotencyMaxEntries,
		maxBody:    idempotencyMaxBody,
	}
	go c.janitor()
	return c
}

// begin also evicts lazily.
func (c *idempotencyCache) janitor() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			c.sweep(time.Now())
		}
	}
}

// stop halts the janitor goroutine. Idempotent.
func (c *idempotencyCache) stop() {
	c.stopOnce.Do(func() { close(c.done) })
}

// In-flight markers are never swept by age: that would open a double-execution window for a
// long-running handler.
func (c *idempotencyCache) sweep(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.entries {
		if !e.inflight && now.Sub(e.ts) >= c.ttl {
			delete(c.entries, k)
		}
	}
}

// begin atomically transitions the slot for key. It returns either a
// completed entry to replay, an in-flight signal, or claims the key as
// in-flight for the caller to run. Exactly one of the three outcomes:
//   - (entry, false): a fresh completed entry exists → replay it.
//   - (nil, true):    another request holds the key → 409.
//   - (nil, false):   key claimed in-flight; caller owns it and must
//     resolve via complete() or abort().
func (c *idempotencyCache) begin(key string) (*idempotencyEntry, bool) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		if e.inflight {
			return nil, true
		}
		if now.Sub(e.ts) < c.ttl {
			return e, false
		}
		delete(c.entries, key)
	}
	if len(c.entries) >= c.maxEntries {
		c.evictOldestCompletedLocked()
	}
	c.entries[key] = &idempotencyEntry{ts: now, inflight: true}
	return nil, false
}

// complete replaces the in-flight marker with a cached response (status < 500, within the
// body cap). The body is copied so the handler's buffer cannot alias it.
func (c *idempotencyCache) complete(key string, status int, ct string, body []byte) {
	cp := slices.Clone(body)
	c.mu.Lock()
	c.entries[key] = &idempotencyEntry{ts: time.Now(), status: status, ct: ct, body: cp}
	c.mu.Unlock()
}

// abort clears an in-flight marker without storing a completed entry,
// so a retry can re-execute. Used for 5xx and over-cap responses, and
// as the defer-driven safety net if the handler panics. Never clobbers
// a completed entry (in case complete already ran).
func (c *idempotencyCache) abort(key string) {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && e.inflight {
		delete(c.entries, key)
	}
	c.mu.Unlock()
}

// Callers hold c.mu. In-flight markers are never evicted (a concurrent retry would re-execute), so
// the map may briefly exceed the cap.
func (c *idempotencyCache) evictOldestCompletedLocked() {
	var oldestKey string
	var oldestTS time.Time
	for k, e := range c.entries {
		if e.inflight {
			continue
		}
		if oldestKey == "" || e.ts.Before(oldestTS) {
			oldestKey, oldestTS = k, e.ts
		}
	}
	if oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}

// Non-deduped requests get the real ResponseWriter (streaming stays correct); deduped ones write
// through and buffer.
func (c *idempotencyCache) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !idempotentMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		key := r.Header.Get(idempotencyHeader)
		if !validIdempotencyKey(key) {
			// Empty or malformed key: skip dedup rather than 400.
			next.ServeHTTP(w, r)
			return
		}

		ck := idempotencyCompositeKey(r.Method, r.URL.Path, key)
		replay, inflight := c.begin(ck)
		if replay != nil {
			writeIdempotentReplay(w, replay)
			return
		}
		if inflight {
			// Genuine retries are sequential, so a true-concurrent duplicate gets 409.
			httpreply.Conflict(w, "request already in progress")
			return
		}

		// The deferred resolve clears the in-flight marker even if the handler panics.
		cw := &idempotencyWriter{rec: webhttp.NewStatusRecorder(w), limit: c.maxBody}
		settled := false
		defer func() {
			if !settled {
				c.abort(ck)
			}
		}()
		next.ServeHTTP(cw, r)
		settled = true

		// Cache only deterministic outcomes (<500) within the cap; a 5xx stays retryable.
		if cw.rec.Status() < 500 && !cw.overflow {
			c.complete(ck, cw.rec.Status(), cw.Header().Get("Content-Type"), cw.buf.Bytes())
		} else {
			c.abort(ck)
		}
	})
}

// writeIdempotentReplay writes a cached response. securityMiddleware already set the baseline
// security headers; only the Content-Type needs restoring.
func writeIdempotentReplay(w http.ResponseWriter, e *idempotencyEntry) {
	if e.ct != "" {
		w.Header().Set("Content-Type", e.ct)
	}
	w.WriteHeader(e.status)
	_, _ = w.Write(e.body)
}

// idempotencyWriter writes through to the client while buffering status and body. It
// implements only http.ResponseWriter, deliberately not Flusher/Hijacker/ReaderFrom, so every
// byte flows through Write into the buffer. Past limit it sets overflow and stops buffering.
type idempotencyWriter struct {
	rec      *webhttp.StatusRecorder
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (cw *idempotencyWriter) Header() http.Header { return cw.rec.Header() }

func (cw *idempotencyWriter) WriteHeader(code int) { cw.rec.WriteHeader(code) }

func (cw *idempotencyWriter) Write(p []byte) (int, error) {
	n, err := cw.rec.Write(p)
	if !cw.overflow {
		if cw.buf.Len()+n > cw.limit {
			cw.overflow = true
			cw.buf.Reset() // won't cache → free the partial buffer
		} else {
			cw.buf.Write(p[:n])
		}
	}
	return n, err
}
