package server

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/webhttp/v4"
)

// apiStack wraps mux in the production middleware stack, so a test answers what a browser gets.
func apiStack(t *testing.T, s *Server, mux *http.ServeMux) http.Handler {
	t.Helper()
	idem := newIdempotencyCache(idempotencyTTL)
	t.Cleanup(idem.stop)
	return webhttp.Chain(mux, s.middlewareStack(baseCSPPolicy, idem)...)
}

// gunzipped reads a gzip-coded response body back to its identity bytes.
func gunzipped(t *testing.T, rec *httptest.ResponseRecorder) []byte {
	t.Helper()
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip (the case under test is a compressed answer)", got)
	}
	zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("gzip.NewReader over the response body: %v", err)
	}
	body, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read the decompressed body: %v", err)
	}
	return body
}

// bigJSON is a JSON body comfortably over webhttp's 1 KiB gzip floor, shaped like a chat list.
func bigJSON() string {
	return `{"chats":[` + strings.Repeat(`{"id":"c1","name":"a chat"},`, 200) + `{"id":"z"}]}`
}

func writeBigJSON(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, bigJSON())
}

func TestCompressJSON_ALargeAPIAnswerIsGzipped(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/chats", writeBigJSON)
	rec := serveStack(apiStack(t, &Server{}, mux), http.MethodGet, "/api/chats", "", "Accept-Encoding", "gzip, deflate, br")

	if got := rec.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Errorf("Vary = %q, want it to name Accept-Encoding", got)
	}
	if got := string(gunzipped(t, rec)); got != bigJSON() {
		t.Errorf("decompressed body = %d bytes, want the %d the handler wrote", len(got), len(bigJSON()))
	}
}

// TestCompressJSON_OnlyJSONIsCompressed pins the media types: JSON and a +json suffix compress,
// a stream and a page go out byte-equal.
func TestCompressJSON_OnlyJSONIsCompressed(t *testing.T) {
	cases := []struct {
		contentType string
		compressed  bool
	}{
		{"application/json", true},
		{"application/json; charset=utf-8", true},
		{"application/problem+json", true},
		// The git clone progress stream: a substring match on "json" would buffer it.
		{"application/x-ndjson", false},
		{"text/html; charset=utf-8", false},
	}
	for _, c := range cases {
		t.Run(strings.ReplaceAll(c.contentType, "/", "_"), func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/api/anything", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", c.contentType)
				_, _ = io.WriteString(w, bigJSON())
			})
			rec := serveStack(apiStack(t, &Server{}, mux), http.MethodGet, "/api/anything", "", "Accept-Encoding", "gzip")
			if c.compressed {
				if got := string(gunzipped(t, rec)); got != bigJSON() {
					t.Errorf("decompressed body = %d bytes, want the %d written", len(got), len(bigJSON()))
				}
				return
			}
			if got := rec.Header().Get("Content-Encoding"); got != "" {
				t.Errorf("Content-Encoding = %q, want none", got)
			}
			if got := rec.Body.String(); got != bigJSON() {
				t.Errorf("body = %d bytes, want the %d written", len(got), len(bigJSON()))
			}
		})
	}
}

// TestCompressJSON_SkippedRoutesAreNeverWrapped pins the skip list by its effect: a large JSON
// body there goes out as written, with no Vary, while a lookalike path is compressed.
func TestCompressJSON_SkippedRoutesAreNeverWrapped(t *testing.T) {
	cases := []struct {
		path       string
		compressed bool
	}{
		{"/api/events", false},
		{"/api/shell/ws", false},
		{"/api/shell/ws/anything", false},
		{"/preview/token/data.json", false},
		// A .json download is application/json, and a resumed download needs its byte ranges.
		{"/api/file/download", false},
		{"/api/eventsource", true},
		{"/api/shell", true},
		{"/api/file", true},
	}
	mux := http.NewServeMux()
	for _, c := range cases {
		mux.HandleFunc(c.path, writeBigJSON)
	}
	h := apiStack(t, &Server{}, mux)
	for _, c := range cases {
		t.Run(strings.ReplaceAll(c.path, "/", "_"), func(t *testing.T) {
			rec := serveStack(h, http.MethodGet, c.path, "", "Accept-Encoding", "gzip")
			if got := rec.Header().Get("Content-Encoding") == "gzip"; got != c.compressed {
				t.Errorf("compressed = %t, want %t", got, c.compressed)
			}
			if c.compressed {
				return
			}
			if got := rec.Header().Get("Vary"); got != "" {
				t.Errorf("Vary = %q, want none on a route that is not negotiated", got)
			}
			if got := rec.Body.String(); got != bigJSON() {
				t.Errorf("body = %d bytes, want the %d written", len(got), len(bigJSON()))
			}
		})
	}
}

// TestCompressJSON_AStreamedNDJSONAnswerIsNotBuffered pins the git clone progress stream: its
// first line is on the wire at the handler's Flush, whatever its size.
func TestCompressJSON_AStreamedNDJSONAnswerIsNotBuffered(t *testing.T) {
	const first = `{"progress":"cloning"}` + "\n"
	rec := httptest.NewRecorder()
	atFlush := "<never flushed>"
	mux := http.NewServeMux()
	mux.HandleFunc("/api/git/clone", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, first)
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("Flush through the stack: %v", err)
		}
		atFlush = rec.Body.String()
		_, _ = io.WriteString(w, strings.Repeat(`{"output":"line"}`+"\n", 100))
	})
	req := httptest.NewRequest(http.MethodPost, "/api/git/clone", http.NoBody)
	req.Header.Set("Accept-Encoding", "gzip")
	apiStack(t, &Server{}, mux).ServeHTTP(rec, req)

	if atFlush != first {
		t.Errorf("body on the wire at Flush = %q, want the first line %q", atFlush, first)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want none on a stream", got)
	}
}

// TestCompressJSON_AFlushedJSONAnswerIsNotHeldBack: a JSON answer under the 1 KiB floor that the
// handler flushes is on the wire at the Flush, identity-coded, rather than held for its size.
func TestCompressJSON_AFlushedJSONAnswerIsNotHeldBack(t *testing.T) {
	const first = `{"status":"started"}`
	rec := httptest.NewRecorder()
	atFlush := "<never flushed>"
	mux := http.NewServeMux()
	mux.HandleFunc("/api/anything", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, first)
		f, ok := w.(http.Flusher)
		if !ok {
			t.Error("the stack hands the handler a writer that is not an http.Flusher")
			return
		}
		f.Flush()
		atFlush = rec.Body.String()
	})
	req := httptest.NewRequest(http.MethodGet, "/api/anything", http.NoBody)
	req.Header.Set("Accept-Encoding", "gzip")
	apiStack(t, &Server{}, mux).ServeHTTP(rec, req)

	if atFlush != first {
		t.Errorf("body on the wire at Flush = %q, want %q", atFlush, first)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want none on a flushed answer under the floor", got)
	}
	if !rec.Flushed {
		t.Error("the handler's Flush never reached the client's writer")
	}
}

// TestCompressJSON_AReplayedMutationRenegotiatesItsCoding: a retried keyed mutation replays the
// first answer coded for the retry's own Accept-Encoding, since a cached gzip body sent to a
// client that did not offer gzip, or sent with no Content-Encoding, cannot be read.
func TestCompressJSON_AReplayedMutationRenegotiatesItsCoding(t *testing.T) {
	handler, calls := idemHandler(http.StatusOK, "application/json", bigJSON())
	mux := http.NewServeMux()
	mux.Handle("POST /api/command", handler)
	h := apiStack(t, &Server{}, mux)
	post := func(hdr ...string) *httptest.ResponseRecorder {
		return serveStack(h, http.MethodPost, "/api/command", `{}`,
			append([]string{idempotencyHeader, "r-gzip", "Content-Type", "application/json"}, hdr...)...)
	}

	if got := string(gunzipped(t, post("Accept-Encoding", "gzip"))); got != bigJSON() {
		t.Errorf("first answer decompressed = %d bytes, want the %d written", len(got), len(bigJSON()))
	}
	if got := string(gunzipped(t, post("Accept-Encoding", "gzip"))); got != bigJSON() {
		t.Errorf("gzip replay decompressed = %d bytes, want the %d written", len(got), len(bigJSON()))
	}
	identity := post()
	if got := identity.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("replay to a client offering no gzip: Content-Encoding = %q, want none", got)
	}
	if got := identity.Body.String(); got != bigJSON() {
		t.Errorf("identity replay = %d bytes, want the %d written", len(got), len(bigJSON()))
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("handler calls = %d, want 1 (both retries replayed)", got)
	}
}

// TestCompressJSON_TheShellUpgradeStillHijacks: the PTY WebSocket takes the connection over
// through every layer of the stack.
func TestCompressJSON_TheShellUpgradeStillHijacks(t *testing.T) {
	const reply = "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\nhijacked"
	mux := http.NewServeMux()
	mux.HandleFunc("/api/shell/ws", func(w http.ResponseWriter, _ *http.Request) {
		conn, brw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("Hijack through the stack: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = brw.WriteString(reply)
		_ = brw.Flush()
	})
	srv := httptest.NewServer(apiStack(t, &Server{}, mux))
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/api/shell/ws", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /api/shell/ws: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Errorf("status = %d, want 101 written on the hijacked connection", resp.StatusCode)
	}
}
