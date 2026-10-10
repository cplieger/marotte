package filebrowse

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func decodeJSON[T any](t *testing.T, body []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return v
}

func sha256ID(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// The whole read carries every line ending, BOM and missing final newline exactly, with the
// identity of those bytes, and saving the content back leaves the file byte-identical.
func TestRead_ExactBytesRoundTripThroughASave(t *testing.T) {
	cases := map[string]string{
		"crlf":            "one\r\ntwo\r\n",
		"mixed":           "a\nb\r\nc\rd\n",
		"lone_cr":         "a\rb\rc",
		"bom":             "\ufeffhead\nbody\n",
		"no_final_break":  "last line",
		"cr_at_eof":       "x\r",
		"empty":           "",
		"only_terminator": "\r\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			h, dir, prefix := testDir(t)
			target := filepath.Join(dir, "f.txt")
			if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			rec := getReq(t, h, "/api/file?path="+prefix+"/f.txt")
			if rec.Code != http.StatusOK {
				t.Fatalf("GET = %d: %s", rec.Code, rec.Body.String())
			}
			got := decodeJSON[FileRead](t, rec.Body.Bytes())
			if got.Content != content || got.Size != int64(len(content)) || !got.UTF8 {
				t.Errorf("read = %+v, want content %q, size %d, utf8", got, content, len(content))
			}
			if got.FileID != sha256ID([]byte(content)) {
				t.Errorf("file_id = %q, want %q", got.FileID, sha256ID([]byte(content)))
			}
			body, _ := json.Marshal(map[string]string{"content": got.Content, "file_id": got.FileID})
			if put := putReq(t, h, "/api/file?path="+prefix+"/f.txt", string(body)); put.Code != http.StatusOK {
				t.Fatalf("PUT = %d: %s", put.Code, put.Body.String())
			}
			if disk, _ := os.ReadFile(target); string(disk) != content {
				t.Errorf("disk after save = %q, want %q byte for byte", disk, content)
			}
		})
	}
}

// Invalid UTF-8 reaches JSON as U+FFFD, so the read says so and the client keeps it
// read-only rather than saving the replacement characters over the real bytes.
func TestRead_InvalidUTF8IsMarked(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "latin1.txt"), []byte("caf\xe9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := decodeJSON[FileRead](t, getReq(t, h, "/api/file?path="+prefix+"/latin1.txt").Body.Bytes())
	if got.UTF8 {
		t.Errorf("utf8 = true for %q, want false", "caf\xe9\n")
	}
	st := decodeJSON[FileStat](t, getReq(t, h, "/api/file/stat?path="+prefix+"/latin1.txt").Body.Bytes())
	if st.UTF8 {
		t.Errorf("stat utf8 = true, want false")
	}
}

func TestRead_OverTheCapIsTheTooLargeRefusal(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), bytes.Repeat([]byte("a"), WholeFileMax+1), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := getReq(t, h, "/api/file?path="+prefix+"/big.txt")
	got := decodeJSON[FileRefusal](t, rec.Body.Bytes())
	if rec.Code != http.StatusRequestEntityTooLarge || got.Code != RefusalTooLarge || got.Error != tooLargeSentence {
		t.Errorf("GET = %d %+v, want 413 too_large with %q", rec.Code, got, tooLargeSentence)
	}
}

func TestStat_SmallFileCarriesItsIdentity(t *testing.T) {
	h, dir, prefix := testDir(t)
	data := []byte("hello\n")
	if err := os.WriteFile(filepath.Join(dir, "s.txt"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	rec := getReq(t, h, "/api/file/stat?path="+prefix+"/s.txt")
	got := decodeJSON[FileStat](t, rec.Body.Bytes())
	if rec.Code != http.StatusOK || got.FileID != sha256ID(data) || got.Large || got.Binary || !got.UTF8 || got.Size != int64(len(data)) {
		t.Errorf("stat = %d %+v, want 200 with the sha256 identity of %q", rec.Code, got, data)
	}
	if _, err := time.Parse(time.RFC3339Nano, got.Modified); err != nil {
		t.Errorf("modified = %q, want RFC 3339: %v", got.Modified, err)
	}
}

func TestStat_LargeFileHasNoIdentity(t *testing.T) {
	h, dir, prefix := testDir(t)
	big := append([]byte{0}, bytes.Repeat([]byte("a"), WholeFileMax)...)
	if err := os.WriteFile(filepath.Join(dir, "big.bin"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	got := decodeJSON[FileStat](t, getReq(t, h, "/api/file/stat?path="+prefix+"/big.bin").Body.Bytes())
	if !got.Large || got.FileID != "" || !got.Binary || got.Size != int64(len(big)) {
		t.Errorf("stat = %+v, want large, binary, no file_id, size %d", got, len(big))
	}
}

// A same-size rewrite that restores the old mtime is a new identity, and a save carrying the
// old one is refused: the identity is a digest of a fresh read, never a stat-keyed cache.
func TestStat_SameSizeRewriteWithRestoredMtimeChangesTheIdentity(t *testing.T) {
	h, dir, prefix := testDir(t)
	target := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(target, []byte("aaaa\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(target, old, old); err != nil {
		t.Fatal(err)
	}
	before := decodeJSON[FileStat](t, getReq(t, h, "/api/file/stat?path="+prefix+"/f.txt").Body.Bytes())
	f, err := os.OpenFile(target, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("bbbb\n"), 0); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if err := os.Chtimes(target, old, old); err != nil {
		t.Fatal(err)
	}
	after := decodeJSON[FileStat](t, getReq(t, h, "/api/file/stat?path="+prefix+"/f.txt").Body.Bytes())
	if after.FileID == before.FileID {
		t.Errorf("file_id unchanged (%q) across a same-size rewrite with the mtime restored", after.FileID)
	}
	body := `{"content":"mine\n","file_id":"` + before.FileID + `"}`
	if rec := putReq(t, h, "/api/file?path="+prefix+"/f.txt", body); rec.Code != http.StatusConflict {
		t.Errorf("PUT with the old identity = %d, want 409", rec.Code)
	}
}

// A file that moves under every read is a 409 with no bytes; one that settles after one
// change is served with the settled bytes.
func TestRead_HoldsTheFileStillOrRefuses(t *testing.T) {
	t.Run("changes under every read", func(t *testing.T) {
		h, dir, prefix := testDir(t)
		target := filepath.Join(dir, "log.txt")
		if err := os.WriteFile(target, []byte("start\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		setBeforeRead(t, func(*os.File) { appendTo(t, target, "more\n") })
		rec := getReq(t, h, "/api/file?path="+prefix+"/log.txt")
		got := decodeJSON[FileRefusal](t, rec.Body.Bytes())
		if rec.Code != http.StatusConflict || got.Code != RefusalChanged {
			t.Errorf("GET = %d %+v, want 409 changed", rec.Code, got)
		}
	})
	t.Run("settles after one change", func(t *testing.T) {
		h, dir, prefix := testDir(t)
		target := filepath.Join(dir, "log.txt")
		if err := os.WriteFile(target, []byte("start\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		calls := 0
		setBeforeRead(t, func(*os.File) {
			calls++
			if calls == 1 {
				appendTo(t, target, "more\n")
			}
		})
		rec := getReq(t, h, "/api/file?path="+prefix+"/log.txt")
		got := decodeJSON[FileRead](t, rec.Body.Bytes())
		if rec.Code != http.StatusOK || got.Content != "start\nmore\n" || got.FileID != sha256ID([]byte("start\nmore\n")) {
			t.Errorf("GET = %d %+v, want the settled bytes and their identity", rec.Code, got)
		}
	})
}

func setBeforeRead(t *testing.T, fn func(*os.File)) {
	t.Helper()
	prev := beforeRead
	beforeRead = fn
	t.Cleanup(func() { beforeRead = prev })
}

func appendTo(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

// An ancestor swapped for an in-root symlink into a protected directory after the policy check
// serves no protected byte on any read route.
func TestReadRoutes_RefuseAnAncestorSwappedAfterThePolicyCheck(t *testing.T) {
	for _, route := range []string{"/api/file", "/api/file/stat", "/api/file/download"} {
		t.Run(strings.TrimPrefix(route, "/api/"), func(t *testing.T) {
			dir := t.TempDir()
			h, err := New(NewSensitive(dir), []string{dir})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(dir, "home"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "home", "x.txt"), []byte("SECRET"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(dir, "pub"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "pub", "x.txt"), []byte("public"), 0o600); err != nil {
				t.Fatal(err)
			}
			prev := afterPolicy
			afterPolicy = func(string) {
				if err := os.Rename(filepath.Join(dir, "pub"), filepath.Join(dir, "pub.old")); err != nil {
					t.Error(err)
				}
				if err := os.Symlink("home", filepath.Join(dir, "pub")); err != nil {
					t.Error(err)
				}
			}
			t.Cleanup(func() { afterPolicy = prev })
			rec := getReq(t, h, route+"?path="+url.QueryEscape(filepath.Join(dir, "pub", "x.txt")))
			if strings.Contains(rec.Body.String(), "SECRET") || rec.Code != http.StatusConflict {
				t.Errorf("%s after the swap = %d %q, want 409 and no protected byte", route, rec.Code, rec.Body.String())
			}
		})
	}
}

// A download carrying file_id serves exactly that identity's bytes, or refuses
// before any header.
func TestDownload_FileIDPinsTheBytesServed(t *testing.T) {
	h, dir, prefix := testDir(t)
	target := filepath.Join(dir, "pic.png")
	if err := os.WriteFile(target, []byte("old bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldID := sha256ID([]byte("old bytes"))
	base := "/api/file/download?path=" + prefix + "/pic.png&file_id="

	rec := getReq(t, h, base+url.QueryEscape(oldID))
	if rec.Code != http.StatusOK || rec.Body.String() != "old bytes" || rec.Header().Get("ETag") != `"`+oldID+`"` {
		t.Errorf("matching identity = %d %q ETag %q, want 200 with the bytes and ETag %q",
			rec.Code, rec.Body.String(), rec.Header().Get("ETag"), `"`+oldID+`"`)
	}

	if err := os.WriteFile(target, []byte("new bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec = getReq(t, h, base+url.QueryEscape(oldID))
	got := decodeJSON[FileRefusal](t, rec.Body.Bytes())
	if rec.Code != http.StatusConflict || got.FileID != sha256ID([]byte("new bytes")) || rec.Header().Get("Content-Disposition") != "" {
		t.Errorf("stale identity = %d %+v disposition %q, want 409 naming the current identity and no attachment headers",
			rec.Code, got, rec.Header().Get("Content-Disposition"))
	}

	for _, bad := range []string{"", "sha256:xyz", "stat:1:2:3", strings.Repeat("a", 71)} {
		if rec := getReq(t, h, base+url.QueryEscape(bad)); rec.Code != http.StatusBadRequest {
			t.Errorf("file_id %q = %d, want 400", bad, rec.Code)
		}
	}
}

func TestDownload_FileIDOnAFileOverTheCapIsRefused(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), bytes.Repeat([]byte("a"), WholeFileMax+1), 0o600); err != nil {
		t.Fatal(err)
	}
	id := sha256ID([]byte("anything"))
	if rec := getReq(t, h, "/api/file/download?path="+prefix+"/big.txt&file_id="+url.QueryEscape(id)); rec.Code != http.StatusConflict {
		t.Errorf("file_id on a large file = %d, want 409", rec.Code)
	}
	if rec := getReq(t, h, "/api/file/download?path="+prefix+"/big.txt"); rec.Code != http.StatusOK || rec.Body.Len() != WholeFileMax+1 {
		t.Errorf("plain download of a large file = %d (%d bytes), want 200 with every byte", rec.Code, rec.Body.Len())
	}
}

// A FIFO answers at once on every read route instead of wedging the handler.
func TestReadRoutes_FIFOAnswersAtOnce(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	for _, route := range []string{"/api/file", "/api/file/stat", "/api/file/download"} {
		if rec := getReq(t, h, route+"?path="+prefix+"/pipe"); rec.Code != http.StatusBadRequest {
			t.Errorf("%s on a FIFO = %d, want 400", route, rec.Code)
		}
	}
}
