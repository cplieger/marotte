package filebrowse

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A file at the cap made of bytes JSON escapes loads and saves; the body limit measures the
// escaped JSON, not the decoded content.
func TestWrite_AcceptsAFileAtTheCapOfEscapedBytes(t *testing.T) {
	h, dir, prefix := testDir(t)
	content := bytes.Repeat([]byte("\"\n"), (WholeFileMax-1)/2)
	content = append(content, '"')
	if err := os.WriteFile(filepath.Join(dir, "q.txt"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	read := decodeJSON[FileRead](t, getReq(t, h, "/api/file?path="+prefix+"/q.txt").Body.Bytes())
	body, err := json.Marshal(map[string]string{"content": read.Content, "file_id": read.FileID})
	if err != nil {
		t.Fatal(err)
	}
	rec := putReq(t, h, "/api/file?path="+prefix+"/q.txt", string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT of a %d-byte file (%d-byte body) = %d: %s", len(content), len(body), rec.Code, rec.Body.String())
	}
	got := decodeJSON[FileWriteResult](t, rec.Body.Bytes())
	if !got.OK || got.FileID != sha256ID(content) || got.Size != int64(len(content)) {
		t.Errorf("PUT reply = %+v, want ok with the written bytes' identity", got)
	}
}

func TestWrite_RefusesDecodedContentOverTheCap(t *testing.T) {
	h, _, prefix := testDir(t)
	body, _ := json.Marshal(map[string]string{"content": strings.Repeat("a", WholeFileMax+1)})
	rec := putReq(t, h, "/api/file?path="+prefix+"/big.txt", string(body))
	got := decodeJSON[FileRefusal](t, rec.Body.Bytes())
	if rec.Code != http.StatusRequestEntityTooLarge || got.Code != RefusalTooLarge {
		t.Errorf("PUT = %d %+v, want 413 too_large", rec.Code, got)
	}
}

// A file grown past the cap since the read answers 409 too_large with no content, not 500.
func TestWrite_StaleGuardOnAFileGrownPastTheCap(t *testing.T) {
	h, dir, prefix := testDir(t)
	target := filepath.Join(dir, "log.txt")
	if err := os.WriteFile(target, []byte("small\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := sha256ID([]byte("small\n"))
	if err := os.WriteFile(target, bytes.Repeat([]byte("x"), 3<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := putReq(t, h, "/api/file?path="+prefix+"/log.txt", `{"content":"mine","file_id":"`+id+`"}`)
	got := decodeJSON[FileRefusal](t, rec.Body.Bytes())
	if rec.Code != http.StatusConflict || got.ContentKind != ContentTooLarge || got.Content != nil ||
		got.Size == nil || *got.Size != 3<<20 {
		t.Errorf("PUT = %d %s, want 409 content_kind too_large, size %d, no content", rec.Code, rec.Body.String(), 3<<20)
	}
}

// A file that crosses the cap while the stale guard reads it is the same 409 too_large
// refusal as one already over the cap, not the viewer's 413.
func TestWrite_StaleGuardOnAFileGrowingPastTheCapMidRead(t *testing.T) {
	h, dir, prefix := testDir(t)
	target := filepath.Join(dir, "log.txt")
	if err := os.WriteFile(target, []byte("small\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := sha256ID([]byte("small\n"))
	grown := false
	setBeforeRead(t, func(*os.File) {
		if !grown {
			grown = true
			appendTo(t, target, strings.Repeat("x", 3<<20))
		}
	})
	rec := putReq(t, h, "/api/file?path="+prefix+"/log.txt", `{"content":"mine","file_id":"`+id+`"}`)
	got := decodeJSON[FileRefusal](t, rec.Body.Bytes())
	want := int64(len("small\n") + 3<<20)
	if rec.Code != http.StatusConflict || got.Code != RefusalChanged || got.ContentKind != ContentTooLarge ||
		got.Content != nil || got.Size == nil || *got.Size != want {
		t.Errorf("PUT = %d %s, want 409 changed, content_kind too_large, size %d, no content", rec.Code, rec.Body.String(), want)
	}
	if b, _ := os.ReadFile(target); int64(len(b)) != want {
		t.Errorf("disk holds %d bytes, the refused write must not land", len(b))
	}
}

// A save after the file turned binary or invalid UTF-8 carries no content, which JSON would
// rewrite into U+FFFD the client could save over the real bytes.
func TestWrite_StaleGuardWithholdsUnrepresentableContent(t *testing.T) {
	for kind, disk := range map[ContentKind]string{
		ContentBinary:  "a\x00b",
		ContentNotUTF8: "caf\xe9",
	} {
		t.Run(string(kind), func(t *testing.T) {
			h, dir, prefix := testDir(t)
			target := filepath.Join(dir, "f.txt")
			if err := os.WriteFile(target, []byte(disk), 0o600); err != nil {
				t.Fatal(err)
			}
			stale := sha256ID([]byte("what the client read"))
			rec := putReq(t, h, "/api/file?path="+prefix+"/f.txt", `{"content":"mine","file_id":"`+stale+`"}`)
			got := decodeJSON[FileRefusal](t, rec.Body.Bytes())
			if rec.Code != http.StatusConflict || got.ContentKind != kind || got.Content != nil || got.FileID != sha256ID([]byte(disk)) {
				t.Errorf("PUT = %d %s, want 409 content_kind %s with no content", rec.Code, rec.Body.String(), kind)
			}
			if b, _ := os.ReadFile(target); string(b) != disk {
				t.Errorf("disk = %q, the refused write must not land", b)
			}
		})
	}
}

// A malformed file_id is refused before the destination is opened, so nothing is created.
func TestWrite_MalformedFileIDIsRefusedBeforeTheDestinationOpens(t *testing.T) {
	for _, id := range []string{"", "sha256:", "sha256:" + strings.Repeat("G", 64), "stat:1:2:3", strings.Repeat("a", 64)} {
		t.Run(id, func(t *testing.T) {
			h, dir, prefix := testDir(t)
			body, _ := json.Marshal(map[string]string{"content": "x", "file_id": id})
			rec := putReq(t, h, "/api/file?path="+prefix+"/new.txt", string(body))
			got := decodeJSON[FileRefusal](t, rec.Body.Bytes())
			if rec.Code != http.StatusBadRequest || got.Code != RefusalInvalidFileID {
				t.Errorf("PUT with file_id %q = %d %+v, want 400 invalid_file_id", id, rec.Code, got)
			}
			if _, err := os.Stat(filepath.Join(dir, "new.txt")); !os.IsNotExist(err) {
				t.Errorf("new.txt exists after a refused save: %v", err)
			}
		})
	}
}

// The destination parent is pinned once, so an ancestor swapped for an in-root
// symlink into a protected directory after the policy check cannot redirect the write.
func TestWrite_PinsTheParentAgainstAnAncestorSwap(t *testing.T) {
	dir := t.TempDir()
	h, err := New(NewSensitive(dir), []string{dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"home", "pub"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "home", "x.txt"), []byte("SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pub", "x.txt"), []byte("public"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := h.resolvePath(filepath.Join(dir, "pub", "x.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "pub"), filepath.Join(dir, "pub.old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("home", filepath.Join(dir, "pub")); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/file", strings.NewReader(`{"content":"clobbered"}`))
	writeFile(rec, req, l, SaveHook{})
	if rec.Code == http.StatusOK {
		t.Errorf("PUT through a swapped ancestor = 200, want a refusal")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "home", "x.txt")); string(b) != "SECRET" {
		t.Errorf("protected file = %q after the swap, want it untouched", b)
	}
}
