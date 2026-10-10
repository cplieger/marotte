package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type fakeSteering struct{ path string }

func (f fakeSteering) CustomPath() string { return f.path }

func steeringServer(t *testing.T) (*Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "custom.md")
	return &Server{steering: fakeSteering{path: path}}, path
}

func getSteering(t *testing.T, s *Server) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handleSteering(rec, httptest.NewRequest(http.MethodGet, "/api/steering", http.NoBody))
	return rec
}

func putSteering(t *testing.T, s *Server, ifMatch, content string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{"content": content})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/steering", bytes.NewReader(body))
	if ifMatch != "" {
		req.Header.Set(headerIfMatch, ifMatch)
	}
	rec := httptest.NewRecorder()
	s.handleSteering(rec, req)
	return rec
}

// TestHandleSteeringGet_RefusesADocumentItCannotRead pins no 200 {"content":""} for an
// unreadable file: the first keystroke's save would replace it.
func TestHandleSteeringGet_RefusesADocumentItCannotRead(t *testing.T) {
	tests := map[string]func(t *testing.T, path string){
		"a directory at the name": func(t *testing.T, path string) {
			t.Helper()
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatalf("seed dir %s: %v", path, err)
			}
		},
		"a symlink at the name": func(t *testing.T, path string) {
			t.Helper()
			target := filepath.Join(filepath.Dir(path), "elsewhere.md")
			if err := os.WriteFile(target, []byte("# elsewhere\n"), 0o600); err != nil {
				t.Fatalf("seed link target: %v", err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatalf("seed symlink %s: %v", path, err)
			}
		},
		"a document past the read cap": func(t *testing.T, path string) {
			t.Helper()
			doc := bytes.Repeat([]byte("x"), maxSteeringBytes+1)
			if err := os.WriteFile(path, doc, 0o600); err != nil {
				t.Fatalf("seed %s: %v", path, err)
			}
		},
	}
	for name, seed := range tests {
		t.Run(name, func(t *testing.T) {
			s, path := steeringServer(t)
			seed(t, path)

			rec := getSteering(t, s)

			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("GET /api/steering = %d, want 500 (a read failure is not an empty document)", rec.Code)
			}
			if got := rec.Body.String(); !strings.Contains(got, "not overwritten") {
				t.Errorf("refusal body = %s, want it to state the instructions were not overwritten", got)
			}
		})
	}
}

// TestHandleSteeringGet_AbsentDocumentIsTheNormalCase pins an empty document plus a token
// for a fresh volume.
func TestHandleSteeringGet_AbsentDocumentIsTheNormalCase(t *testing.T) {
	s, _ := steeringServer(t)

	rec := getSteering(t, s)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/steering with no file = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body not JSON: %v; body=%s", err, rec.Body)
	}
	if got["content"] != "" {
		t.Errorf("content = %q, want empty", got["content"])
	}
	if etag := rec.Header().Get(headerETag); etag != steeringAbsentETag {
		t.Errorf("ETag = %q, want %q — the first save of a fresh volume needs a token to offer",
			etag, steeringAbsentETag)
	}
}

// TestHandleSteeringGet_DoesNotBlockOnAFIFO pins the FIFO refusal on the read side. Bounded:
// a revert HANGS rather than fails.
func TestHandleSteeringGet_DoesNotBlockOnAFIFO(t *testing.T) {
	s, path := steeringServer(t)
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo %s: %v", path, err)
	}

	done := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		s.handleSteering(rec, httptest.NewRequest(http.MethodGet, "/api/steering", http.NoBody))
		done <- rec.Code
	}()

	select {
	case code := <-done:
		if code != http.StatusInternalServerError {
			t.Errorf("GET over a FIFO = %d, want 500", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("GET /api/steering blocked on a FIFO at custom.md; every read would strand a goroutine there")
	}
}

// TestHandleSteeringPut_StoresTheDocument0600 pins the mode on disk, not the argument.
func TestHandleSteeringPut_StoresTheDocument0600(t *testing.T) {
	s, path := steeringServer(t)

	if rec := putSteering(t, s, steeringAbsentETag, "# Global\n\nAlways answer in French.\n"); rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/steering = %d, want 200; body %s", rec.Code, rec.Body)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("custom.md mode = %04o, want 0600", got)
	}
}

// TestHandleSteeringPut_RequiresAValidatorToken is the 428 arm: a save offering no
// If-Match cannot be told from one that would overwrite a newer document, so it is
// refused before anything is written.
func TestHandleSteeringPut_RequiresAValidatorToken(t *testing.T) {
	s, path := steeringServer(t)
	const stored = "# mine\n"
	if err := os.WriteFile(path, []byte(stored), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}

	rec := putSteering(t, s, "", "# theirs\n")

	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("PUT with no If-Match = %d, want 428", rec.Code)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back %s: %v", path, err)
	}
	if string(after) != stored {
		t.Errorf("custom.md after a refused save = %q, want it untouched (%q)", after, stored)
	}
}

// TestHandleSteeringPut_RefusesAStaleToken pins the 409 for a stale token, carrying the fresh
// token AND the document.
func TestHandleSteeringPut_RefusesAStaleToken(t *testing.T) {
	s, path := steeringServer(t)
	if err := os.WriteFile(path, []byte("# loaded\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	stale := getSteering(t, s).Header().Get(headerETag)
	if stale == "" {
		t.Fatal("GET published no ETag, so there is no stale token to offer")
	}

	// Somebody else writes, moving both token inputs.
	const theirs = "# somebody else's, and longer\n"
	if err := os.WriteFile(path, []byte(theirs), 0o600); err != nil {
		t.Fatalf("rewrite %s: %v", path, err)
	}

	rec := putSteering(t, s, stale, "# mine\n")

	if rec.Code != http.StatusConflict {
		t.Fatalf("PUT with a stale If-Match = %d, want 409", rec.Code)
	}
	var got steeringConflictBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("409 body not JSON: %v; body=%s", err, rec.Body)
	}
	if got.Content != theirs {
		t.Errorf("409 content = %q, want the document on disk (%q) so the box can be re-seeded", got.Content, theirs)
	}
	if got.ETag == "" || got.ETag == stale {
		t.Errorf("409 etag = %q, want the fresh token (not the stale %q)", got.ETag, stale)
	}
	if got.Error == "" {
		t.Error("409 carries no error message, so the reader is not told their text was not saved")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back %s: %v", path, err)
	}
	if string(after) != theirs {
		t.Errorf("custom.md after the refused save = %q, want the other writer's document (%q)", after, theirs)
	}
}

func savedETag(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var got steeringSaveBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("save body not JSON: %v; body=%s", err, rec.Body)
	}
	if !got.OK {
		t.Errorf("save body ok = false, want true; body=%s", rec.Body)
	}
	return got.ETag
}

// TestHandleSteeringPut_PublishesTheNextToken pins the fresh token in body and header, or a
// debounced second save would 409 against the first.
func TestHandleSteeringPut_PublishesTheNextToken(t *testing.T) {
	s, path := steeringServer(t)

	first := putSteering(t, s, steeringAbsentETag, "# one\n")
	if first.Code != http.StatusOK {
		t.Fatalf("first PUT = %d, want 200; body %s", first.Code, first.Body)
	}
	next := first.Header().Get(headerETag)
	if next == "" || next == steeringAbsentETag {
		t.Fatalf("PUT published ETag %q, want the token for the document it just wrote", next)
	}
	if body := savedETag(t, first); body != next {
		t.Errorf("save body etag = %q, want the header's %q — the client reads the body", body, next)
	}

	second := putSteering(t, s, next, "# two, which is longer\n")
	if second.Code != http.StatusOK {
		t.Fatalf("second PUT with the published token = %d, want 200; body %s", second.Code, second.Body)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back %s: %v", path, err)
	}
	if string(got) != "# two, which is longer\n" {
		t.Errorf("custom.md = %q, want the second save's content", got)
	}
}

// TestHandleSteeringPut_EmptyContentRemovesTheFile pins that the reply's token is then the
// absent one.
func TestHandleSteeringPut_EmptyContentRemovesTheFile(t *testing.T) {
	s, path := steeringServer(t)
	if err := os.WriteFile(path, []byte("# mine\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	token := getSteering(t, s).Header().Get(headerETag)

	rec := putSteering(t, s, token, "   \n\t ")

	if rec.Code != http.StatusOK {
		t.Fatalf("PUT of whitespace = %d, want 200; body %s", rec.Code, rec.Body)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("custom.md still stands after an empty save: err = %v, want not-exist", err)
	}
	if got := rec.Header().Get(headerETag); got != steeringAbsentETag {
		t.Errorf("ETag = %q, want %q", got, steeringAbsentETag)
	}
	if got := savedETag(t, rec); got != steeringAbsentETag {
		t.Errorf("save body etag = %q, want %q", got, steeringAbsentETag)
	}
}

// TestSteeringETag_MovesWithTheDocument is the token's own contract: it has to
// change when the document does, or every case above passes against a constant.
func TestSteeringETag_MovesWithTheDocument(t *testing.T) {
	s, path := steeringServer(t)
	if err := os.WriteFile(path, []byte("# one\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	before := getSteering(t, s).Header().Get(headerETag)

	if err := os.WriteFile(path, []byte("# one, extended\n"), 0o600); err != nil {
		t.Fatalf("rewrite %s: %v", path, err)
	}
	after := getSteering(t, s).Header().Get(headerETag)

	if before == "" || after == "" {
		t.Fatalf("GET published no ETag: before %q, after %q", before, after)
	}
	if before == after {
		t.Errorf("ETag unchanged across a rewrite (%q); a constant token refuses nothing", before)
	}
}

// custom.md shares a steering directory with the owner-only environment.md, so
// a save that has to create that directory creates it owner-only.
func TestHandleSteeringPut_CreatesAnOwnerOnlyDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "steering")
	s := &Server{steering: fakeSteering{path: filepath.Join(dir, "custom.md")}}

	if rec := putSteering(t, s, steeringAbsentETag, "be terse"); rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/steering = %d, want 200; body %q", rec.Code, rec.Body.String())
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("steering dir mode = %#o, want 0o700", got)
	}
}
