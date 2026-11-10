package filebrowse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/go/filesafe"
)

const refusalText = "tools.json: manifest version 1, want 2"

// fakeOwner writes path itself, as a real owner does, unless it refuses or fails.
type fakeOwner struct {
	fail     error
	rewrite  func([]byte) []byte
	current  *[]byte
	onReturn func()
	path     string
	refuse   string
	calls    []string
	checks   []bool
	noWrite  bool
}

func (o *fakeOwner) Replace(_ context.Context, content []byte, check func([]byte) error) ([]byte, error) {
	if o.onReturn != nil {
		defer o.onReturn()
	}
	o.calls = append(o.calls, string(content))
	o.checks = append(o.checks, check != nil)
	if string(content) == o.refuse {
		return nil, SaveRefusalError{Err: errors.New(refusalText)}
	}
	if o.fail != nil {
		return nil, o.fail
	}
	current, err := os.ReadFile(o.path)
	if errors.Is(err, fs.ErrNotExist) {
		current = nil
	} else if err != nil {
		return nil, err
	}
	if o.current != nil {
		current = *o.current
	}
	if check != nil {
		if err := check(current); err != nil {
			return nil, err
		}
	}
	written := content
	if o.rewrite != nil {
		written = o.rewrite(content)
	}
	if o.noWrite {
		return written, nil
	}
	if err := os.WriteFile(o.path, written, 0o600); err != nil {
		return nil, err
	}
	return written, nil
}

type writeReply struct {
	Content     *string `json:"content"`
	Error       string  `json:"error"`
	Code        string  `json:"code"`
	FileID      string  `json:"file_id"`
	ContentKind string  `json:"content_kind"`
	Size        int64   `json:"size"`
	OK          bool    `json:"ok"`
}

func put(t *testing.T, h *Handler, path, content string, fileID *string) (int, writeReply) {
	t.Helper()
	body, err := json.Marshal(writeBody{Content: content, FileID: fileID})
	if err != nil {
		t.Fatal(err)
	}
	rec := putReq(t, h, "/api/file?path="+url.QueryEscape(path), string(body))
	var reply writeReply
	_ = json.Unmarshal(rec.Body.Bytes(), &reply)
	return rec.Code, reply
}

func putContent(t *testing.T, h *Handler, path, content string) (int, string) {
	t.Helper()
	code, reply := put(t, h, path, content, nil)
	return code, reply.Error
}

func derefOr(s *string, absent string) string {
	if s == nil {
		return absent
	}
	return *s
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func newOwned(t *testing.T, dir string, opts ...Option) *Handler {
	t.Helper()
	h, err := New(Sensitive{}, []string{dir}, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestWriteFile_AnOwnersRefusalWritesNothing(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "tools.json")
	writeTestFile(t, target, "before")
	owner := &fakeOwner{path: target, refuse: "broken"}
	h := newOwned(t, dir, WithOwner(target, owner))

	code, msg := putContent(t, h, target, "broken")

	if code != http.StatusBadRequest || msg != refusalText {
		t.Errorf("PUT refused content = %d %q, want 400 carrying the owner's refusal", code, msg)
	}
	if got := readTestFile(t, target); got != "before" {
		t.Errorf("file after a refused save = %q, want %q untouched", got, "before")
	}
}

// answerOrder records when the handler first answers, so a test can order it against Replace.
type answerOrder struct {
	*httptest.ResponseRecorder
	events *[]string
}

func (a answerOrder) WriteHeader(code int) {
	*a.events = append(*a.events, "answer")
	a.ResponseRecorder.WriteHeader(code)
}

func TestWriteFile_TheAnswerIsWrittenOnlyAfterReplaceReturns(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "tools.json")
	var events []string
	owner := &fakeOwner{path: target, onReturn: func() { events = append(events, "replace returned") }}
	h := newOwned(t, dir, WithOwner(target, owner))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPut, "/api/file?path="+url.QueryEscape(target), strings.NewReader(`{"content":"after"}`))
	rec := answerOrder{ResponseRecorder: httptest.NewRecorder(), events: &events}

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if want := []string{"replace returned", "answer"}; fmt.Sprint(events) != fmt.Sprint(want) {
		t.Errorf("events = %v, want %v", events, want)
	}
}

func TestWriteFile_AnOwnerLeavesOtherPathsAlone(t *testing.T) {
	dir := t.TempDir()
	owner := &fakeOwner{path: filepath.Join(dir, "tools.json"), refuse: "broken"}
	h := newOwned(t, dir, WithOwner(owner.path, owner))
	other := filepath.Join(dir, "notes.json")

	if code, msg := putContent(t, h, other, "broken"); code != http.StatusOK {
		t.Fatalf("PUT another file = %d %q, want 200", code, msg)
	}
	if got := readTestFile(t, other); got != "broken" {
		t.Errorf("other file = %q, want %q written", got, "broken")
	}
	if len(owner.calls) != 0 {
		t.Errorf("owner saw %d calls for another path, want 0", len(owner.calls))
	}
}

func TestWithOwner_ALaterOwnerForTheSamePathReplacesTheEarlier(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "tools.json")
	first := &fakeOwner{path: target, refuse: "broken"}
	second := &fakeOwner{path: target}
	h := newOwned(t, dir, WithOwner(dir+"/./tools.json", first), WithOwner(target, second))

	if code, msg := putContent(t, h, target, "broken"); code != http.StatusOK {
		t.Fatalf("PUT with the replacing owner = %d %q, want 200 from the later owner", code, msg)
	}
	if len(first.calls) != 0 || len(second.calls) != 1 {
		t.Errorf("replaced owner saw %d calls and the replacing one %d, want 0 and 1", len(first.calls), len(second.calls))
	}
}

func TestWithOwner_TheFirstRegisteredOfTwoSpellingsOwnsTheFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "tools.json")
	link := filepath.Join(t.TempDir(), "config")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	viaLink := filepath.Join(link, "tools.json")
	viaDotDot := dir + "/sub/../tools.json"
	for _, tc := range []struct {
		name          string
		first, second string
	}{
		{name: "symlink_first", first: viaLink, second: viaDotDot},
		{name: "dotdot_first", first: viaDotDot, second: viaLink},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for i := range 50 {
				first, second := &fakeOwner{path: target, refuse: "broken"}, &fakeOwner{path: target}
				h := newOwned(t, dir, WithOwner(tc.first, first), WithOwner(tc.second, second))
				code, _ := putContent(t, h, target, "broken")
				if code != http.StatusBadRequest || len(second.calls) != 0 {
					t.Fatalf("handler %d: PUT %s with owners on %s then %s = %d, second owner called %d times; want 400 from the first owner only",
						i, target, tc.first, tc.second, code, len(second.calls))
				}
			}
		})
	}
}

func TestWithOwner_MatchesThroughASymlinkedRoot(t *testing.T) {
	resolvedDir := t.TempDir()
	link := filepath.Join(t.TempDir(), "config")
	if err := os.Symlink(resolvedDir, link); err != nil {
		t.Fatal(err)
	}
	owner := &fakeOwner{path: filepath.Join(resolvedDir, "tools.json"), refuse: "broken"}
	h := newOwned(t, resolvedDir, WithOwner(filepath.Join(link, "tools.json"), owner))

	if code, _ := putContent(t, h, filepath.Join(resolvedDir, "tools.json"), "broken"); code != http.StatusBadRequest {
		t.Errorf("PUT through the resolved path = %d, want 400 from the owner registered on the link", code)
	}
}

func TestWithOwner_FollowsASymlinkSwappedInAfterNew(t *testing.T) {
	dir := t.TempDir()
	owned := filepath.Join(dir, "tools.json")
	other := filepath.Join(dir, "other.json")
	writeTestFile(t, owned, "before")
	writeTestFile(t, other, "before")
	owner := &fakeOwner{path: owned, refuse: "broken"}
	h := newOwned(t, dir, WithOwner(owned, owner))
	if err := os.Remove(owned); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, owned); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{owned, other} {
		if code, _ := putContent(t, h, path, "broken"); code != http.StatusBadRequest {
			t.Errorf("PUT %s after the swap = %d, want 400 from the owner", filepath.Base(path), code)
		}
	}
	if got := readTestFile(t, other); got != "before" {
		t.Errorf("symlink target after refused saves = %q, want %q untouched", got, "before")
	}
}

func TestWriteFile_AnOwnedPathGoesThroughItsOwner(t *testing.T) {
	for _, tc := range []struct {
		rewrite     func([]byte) []byte
		name        string
		wantWritten string
		wantContent string // "" when the answer must carry none
	}{
		{name: "the_owner_writes_what_was_sent", wantWritten: "sent"},
		{
			name:        "the_owner_writes_other_bytes",
			rewrite:     func(b []byte) []byte { return append([]byte("stamped "), b...) },
			wantWritten: "stamped sent",
			wantContent: "stamped sent",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "config.json")
			writeTestFile(t, target, "before")
			owner := &fakeOwner{path: target, rewrite: tc.rewrite, noWrite: true}
			h := newOwned(t, dir, WithOwner(target, owner))

			code, reply := put(t, h, target, "sent", nil)

			if code != http.StatusOK || !reply.OK {
				t.Fatalf("PUT = %d %+v, want 200", code, reply)
			}
			if len(owner.calls) != 1 || owner.calls[0] != "sent" {
				t.Errorf("Replace calls = %q, want one with the body", owner.calls)
			}
			if got := readTestFile(t, target); got != "before" {
				t.Errorf("file = %q, want %q: the browser must not write an owned file itself", got, "before")
			}
			if want := fileIDOf([]byte(tc.wantWritten)); reply.FileID != want {
				t.Errorf("file_id = %q, want the identity of the written bytes %q", reply.FileID, want)
			}
			if reply.Size != int64(len(tc.wantWritten)) {
				t.Errorf("size = %d, want %d", reply.Size, len(tc.wantWritten))
			}
			if got := derefOr(reply.Content, ""); got != tc.wantContent || (reply.Content != nil) != (tc.wantContent != "") {
				t.Errorf("content = %q (present %v), want %q", got, reply.Content != nil, tc.wantContent)
			}
		})
	}
}

func TestWriteFile_AnOwnersCheckFailureIsTheStale409(t *testing.T) {
	sent := func(s string) *string { return &s }
	t.Run("a_stale_file_id", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "config.json")
		writeTestFile(t, target, "on disk")
		owner := &fakeOwner{path: target}
		h := newOwned(t, dir, WithOwner(target, owner))

		code, reply := put(t, h, target, "mine", sent(fileIDOf([]byte("opened"))))

		if code != http.StatusConflict || reply.Code != string(refusalChanged) || reply.Error != errStaleWrite {
			t.Fatalf("stale PUT = %d %+v, want 409 %q", code, reply, refusalChanged)
		}
		if reply.Content == nil || *reply.Content != "on disk" || reply.FileID != fileIDOf([]byte("on disk")) {
			t.Errorf("refusal carries %v under %q, want the disk's text and identity", reply.Content, reply.FileID)
		}
		if got := readTestFile(t, target); got != "on disk" {
			t.Errorf("file after a stale PUT = %q, want it untouched", got)
		}
	})
	t.Run("the_refusal_carries_the_bytes_check_was_given", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "config.json")
		writeTestFile(t, target, "on disk")
		given := []byte("under the owner's lock")
		owner := &fakeOwner{path: target, current: &given}
		h := newOwned(t, dir, WithOwner(target, owner))

		code, reply := put(t, h, target, "mine", sent(fileIDOf([]byte("on disk"))))

		if code != http.StatusConflict || reply.Content == nil || *reply.Content != string(given) || reply.FileID != fileIDOf(given) {
			t.Errorf("PUT = %d %+v, want 409 carrying %q", code, reply, given)
		}
	})
	t.Run("a_file_now_too_large", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "config.json")
		large := make([]byte, WholeFileMax+1)
		owner := &fakeOwner{path: target, current: &large}
		h := newOwned(t, dir, WithOwner(target, owner))

		code, reply := put(t, h, target, "mine", sent(fileIDOf([]byte("opened"))))

		if code != http.StatusConflict || reply.ContentKind != string(contentTooLarge) || reply.Content != nil || reply.Size != WholeFileMax+1 {
			t.Errorf("PUT = %d %+v, want 409 too_large with no content", code, reply)
		}
	})
	t.Run("a_matching_file_id_or_an_absent_file_is_written", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "config.json")
		writeTestFile(t, target, "on disk")
		owner := &fakeOwner{path: target}
		h := newOwned(t, dir, WithOwner(target, owner))

		if code, reply := put(t, h, target, "mine", sent(fileIDOf([]byte("on disk")))); code != http.StatusOK {
			t.Fatalf("PUT with the disk's identity = %d %+v, want 200", code, reply)
		}
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
		if code, reply := put(t, h, target, "again", sent(fileIDOf([]byte("on disk")))); code != http.StatusOK {
			t.Fatalf("PUT over an absent file = %d %+v, want 200: an absent file is not stale", code, reply)
		}
		if got := readTestFile(t, target); got != "again" {
			t.Errorf("file = %q, want %q", got, "again")
		}
	})
	t.Run("no_file_id_passes_a_nil_check", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "config.json")
		owner := &fakeOwner{path: target}
		h := newOwned(t, dir, WithOwner(target, owner))

		put(t, h, target, "a", nil)
		put(t, h, target, "b", sent(fileIDOf(nil)))

		if want := []bool{false, true}; fmt.Sprint(owner.checks) != fmt.Sprint(want) {
			t.Errorf("Replace got a check = %v, want %v (none without a file_id)", owner.checks, want)
		}
	})
}

func TestWriteFile_ASaveRefusalIs400WithItsText(t *testing.T) {
	for _, tc := range []struct {
		err      error
		name     string
		wantText string
		wantCode int
	}{
		{name: "a_save_refusal", err: SaveRefusalError{Err: errors.New("x must be y")}, wantCode: http.StatusBadRequest, wantText: "x must be y"},
		{name: "a_wrapped_save_refusal", err: fmt.Errorf("owner: %w", SaveRefusalError{Err: errors.New("x must be y")}), wantCode: http.StatusBadRequest, wantText: "x must be y"},
		{name: "a_non_regular_target", err: fmt.Errorf("config.json: %w", filesafe.ErrNotRegular), wantCode: http.StatusBadRequest, wantText: "not a regular file"},
		{name: "any_other_failure", err: errors.New("disk on fire"), wantCode: http.StatusInternalServerError, wantText: "write failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "config.json")
			h := newOwned(t, dir, WithOwner(target, &fakeOwner{path: target, fail: tc.err}))

			if code, msg := putContent(t, h, target, "{}"); code != tc.wantCode || msg != tc.wantText {
				t.Errorf("PUT = %d %q, want %d %q", code, msg, tc.wantCode, tc.wantText)
			}
		})
	}
}
