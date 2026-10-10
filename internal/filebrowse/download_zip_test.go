package filebrowse

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func zipNames(t *testing.T, raw []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	got := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open zip entry %q: %v", f.Name, err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		got[f.Name] = string(data)
	}
	return got
}

// A link inside a zipped folder must not be followed: the deny list judges the
// path the walk names, so following x -> home would archive the credential tree
// under x/.
func TestHandleDownloadZip_DoesNotFollowASymlinkIntoTheSensitiveTree(t *testing.T) {
	backing := t.TempDir()
	creds := filepath.Join(backing, "home", ".aws")
	if err := os.MkdirAll(creds, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(creds, "credentials"), []byte("secret-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backing, "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("home", filepath.Join(backing, "x")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(backing)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	m := &mount{root: root, dir: "/config", name: "config"}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	z := &zipStream{zw: zw, ctx: t.Context()}
	z.add(loc{m: m, abs: "/config"}, "config")
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	got := zipNames(t, buf.Bytes())
	for name, body := range got {
		if strings.Contains(body, "secret-key") {
			t.Errorf("zip entry %q carries the credentials; the walk followed a link into /config/home", name)
		}
	}
	if got[filepath.Join("config", "notes.txt")] != "hello" {
		t.Errorf("zip entries = %v, want config/notes.txt archived", got)
	}
}

func TestHandleDownloadZip_AFifoInTheTreeDoesNotHangTheStream(t *testing.T) {
	h, dir, prefix := testDir(t)
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(sub, "p"), 0o600); err != nil {
		t.Skipf("mkfifo unsupported here: %v", err)
	}

	done := make(chan []byte, 1)
	go func() {
		rec := postReq(t, h, "/api/files/download", `{"paths":["`+prefix+`/sub"]}`)
		done <- rec.Body.Bytes()
	}()
	select {
	case raw := <-done:
		if got := zipNames(t, raw); got[filepath.Join("sub", "a.txt")] != "alpha" {
			t.Errorf("zip entries = %v, want sub/a.txt archived beside the FIFO", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("zip download still streaming after 5s: a FIFO in the tree wedged it")
	}
}

// Both single-path handlers must answer a FIFO rather than block in open(2),
// which no client disconnect can interrupt.
func TestHandleDownloadAndFiles_RefuseAFifoInsteadOfBlocking(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := syscall.Mkfifo(filepath.Join(dir, "p"), 0o600); err != nil {
		t.Skipf("mkfifo unsupported here: %v", err)
	}
	for _, route := range []string{"/api/file/download", "/api/files"} {
		t.Run(strings.TrimPrefix(strings.ReplaceAll(route, "/", "_"), "_"), func(t *testing.T) {
			done := make(chan int, 1)
			go func() { done <- getReq(t, h, route+"?path="+prefix+"/p").Code }()
			select {
			case code := <-done:
				if code != http.StatusBadRequest {
					t.Errorf("GET %s?path=<fifo> = %d, want 400", route, code)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("GET %s?path=<fifo> still blocked after 5s", route)
			}
		})
	}
}
