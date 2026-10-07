package filebrowse

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/atomicfile/v4"
)

func testDir(t *testing.T) (h *Handler, dir, prefix string) {
	t.Helper()
	dir = t.TempDir()
	var err error
	h, err = New(Sensitive{}, []string{dir})
	if err != nil {
		t.Fatal(err)
	}
	prefix = strings.TrimPrefix(dir, "/")
	return h, dir, prefix
}

// testHandlerAt builds a handler whose single mount claims policyDir
// (e.g. "/config") while its os.Root is backed by a throwaway temp
// dir. Lexical-layer tests exercise the REAL sensitive prefixes
// without touching (or requiring) the actual policy path on the host;
// no filesystem op ever reaches the backing dir in these tests.
func testHandlerAt(t *testing.T, policyDir string) *Handler {
	t.Helper()
	backing, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &Handler{mounts: []mount{{
		root: backing,
		dir:  policyDir,
		name: strings.TrimPrefix(policyDir, "/"),
	}}}
}

// locAt builds a loc for abs inside the handler's first (only) mount,
// for tests that call action funcs directly, bypassing resolvePath.
func locAt(h *Handler, abs string) loc {
	return loc{m: &h.mounts[0], abs: abs}
}

func getReq(t *testing.T, h *Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func postReq(t *testing.T, h *Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func putReq(t *testing.T, h *Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// multipartUpload builds an /api/file/upload multipart body targeting
// dir. Returns the request ready to ServeHTTP against the handler's
// mux. Files map order is non-deterministic; callers that care about
// upload order should assert on set membership.
func multipartUpload(t *testing.T, targetDir string, files map[string][]byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("dir", targetDir); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		fw, err := w.CreateFormFile("files", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/file/upload", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

func TestResolvePath_OutsideRoots(t *testing.T) {
	h, _, _ := testDir(t)
	for _, dir := range []string{"etc/passwd", "proc/1/status", "var/log/syslog", "workspace2/x"} {
		_, err := h.resolvePath(dir)
		if err == nil {
			t.Errorf("expected error for ungranted path %q", dir)
		}
	}
}

func TestResolvePath_Allowed(t *testing.T) {
	h, dir, prefix := testDir(t)
	got, err := h.resolvePath(prefix + "/myrepo/file.go")
	if err != nil {
		t.Fatal(err)
	}
	if want := dir + "/myrepo/file.go"; got.abs != want {
		t.Errorf("got %q, want %q", got.abs, want)
	}
	if got.m == nil || got.m.dir != dir {
		t.Errorf("loc mount = %+v, want mount at %q", got.m, dir)
	}
	if want := "myrepo/file.go"; got.rel() != want {
		t.Errorf("rel() = %q, want %q", got.rel(), want)
	}
}

// "/" is not resolvable — it is the synthetic mount listing, handled
// before resolvePath in handleFiles. Every other route 403s on it.
func TestResolvePath_Root_Denied(t *testing.T) {
	h, _, _ := testDir(t)
	if _, err := h.resolvePath("/"); err == nil {
		t.Fatal("resolvePath(\"/\") = nil error, want denial (no mount is /)")
	}
}

func TestResolvePath_TraversalOutOfMount(t *testing.T) {
	h, _, prefix := testDir(t)
	_, err := h.resolvePath(prefix + "/../../etc/passwd")
	if err == nil {
		t.Error("expected error for traversal out of the granted mount")
	}
}

// Normalisation: resolvePath cleans common noisy input forms.
func TestResolvePath_Normalisation(t *testing.T) {
	h, dir, prefix := testDir(t)
	tests := []struct {
		in   string
		want string
	}{
		{prefix + "/myrepo", "/myrepo"},
		{prefix + "//myrepo", "/myrepo"},
		{prefix + "/./myrepo", "/myrepo"},
		{prefix + "/myrepo/", "/myrepo"},
		{prefix + "/a/../b", "/b"},
		{"/" + prefix + "/a", "/a"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := h.resolvePath(tc.in)
			if err != nil {
				t.Fatalf("resolvePath(%q) error: %v", tc.in, err)
			}
			if want := dir + tc.want; got.abs != want {
				t.Errorf("resolvePath(%q) = %q, want %q", tc.in, got.abs, want)
			}
		})
	}
}

// A symlink planted inside a granted mount must not
// grant access to an out-of-mount target.
func TestResolvePath_SymlinkOutOfMountRejected(t *testing.T) {
	h, dir, prefix := testDir(t)

	link := filepath.Join(dir, "evil-link")
	if err := os.Symlink("/etc", link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, err := h.resolvePath(prefix + "/evil-link")
	if err == nil {
		t.Errorf("resolvePath via symlink to /etc returned nil error; expected rejection")
	}
}

// TestReadFile_SymlinkToSensitive_Blocked: a read through a symlink to a sensitive path is blocked
// by the symlink-aware resolver; the sensitive target is injected, since a test cannot create the
// real one.
func TestReadFile_SymlinkToSensitive_Blocked(t *testing.T) {
	h, dir, prefix := testDir(t)

	secret := filepath.Join(dir, "secret.json")
	if err := os.WriteFile(secret, []byte("top"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "peek")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	h.sensitive = h.sensitive.with(sensitivePath{Path: secret, IsDir: false})

	rec := getReq(t, h, "/api/file?path="+prefix+"/peek")
	if rec.Code != 403 {
		t.Errorf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// Writing through a symlink whose parent escapes the
// workspace is refused at resolve time (not absorbed by O_NOFOLLOW on
// the wrong side of the check).
func TestWriteFile_SymlinkedParent_Blocked(t *testing.T) {
	h, dir, prefix := testDir(t)

	link := filepath.Join(dir, "evil-parent")
	if err := os.Symlink("/etc", link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	rec := putReq(t, h, "/api/file?path="+prefix+"/evil-parent/foo.txt",
		`{"content":"pwned"}`)
	if rec.Code != 403 {
		t.Errorf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

func TestSensitive_Blocks(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{"home_env_md", "/config/home/.kiro/steering/environment.md", true},
		{"home_agents", "/config/home/.kiro/agents/foo.json", true},
		{"home_ssh_key", "/config/home/.ssh/id_ed25519", true},
		{"legacy_env_md", "/config/kiro/steering/environment.md", true},
		{"legacy_agents", "/config/kiro/agents/foo.json", true},
		{"legacy_any_file", "/config/kiro/steering/other.md", true},
		{"unrelated_file", "/workspace/repo/main.go", false},
		{"chats_dir_deep", "/config/chats/deep/nested.json", true},
		{"exact_push_subs", "/config/push-subs.json", true},
		// Settings → Tools links open these two in the editor, so a deny entry for either makes
		// both links 403.
		{"tools_manifest_not_denied", "/config/tools.json", false},
		{"app_settings_not_denied", "/config/config.json", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := (Sensitive{}).Blocks(tc.path); got != tc.want {
				t.Errorf("Blocks(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// protectedDir: blocks a container of any sensitive path, whether
// the sensitive entry is a dir prefix (trailing /) or an exact file.
func TestIsProtectedDir(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/config/chats", true},
		{"/config/home/.kiro/agents", true},
		{"/config/kiro/steering", true},
		{"/config/kiro", true},
		{"/config/home", true},
		{"/config", true},
		{"/workspace", false},
		{"/workspace/repo", false},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			if got := (Sensitive{}).protectedDir(tc.path); got != tc.want {
				t.Errorf("protectedDir(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestLooksBinary_Boundaries(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{"empty", []byte{}, false},
		{"ascii_only", []byte("hello world"), false},
		{"leading_nul", []byte{0x00, 'a'}, true},
		{"trailing_nul_within_sniff", append(bytes.Repeat([]byte("a"), binarySniffN-1), 0x00), true},
		{"nul_past_sniff_window", append(bytes.Repeat([]byte("a"), binarySniffN+100), 0x00), false},
		{"utf8_no_nul", []byte("café — ÿ"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksBinary(tc.data); got != tc.want {
				t.Errorf("looksBinary(len=%d) = %v, want %v", len(tc.data), got, tc.want)
			}
		})
	}
}

func TestReadWriteFile(t *testing.T) {
	h, dir, prefix := testDir(t)

	rec := putReq(t, h, "/api/file?path="+prefix+"/hello.txt", `{"content":"world"}`)
	if rec.Code != 200 {
		t.Fatalf("write status %d: %s", rec.Code, rec.Body.String())
	}

	rec = getReq(t, h, "/api/file?path="+prefix+"/hello.txt")
	if rec.Code != 200 {
		t.Fatalf("read status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct{ Content string }
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Content != "world" {
		t.Errorf("content = %q, want %q", resp.Content, "world")
	}

	data, _ := os.ReadFile(filepath.Join(dir, "hello.txt"))
	if string(data) != "world" {
		t.Errorf("disk content = %q", string(data))
	}
}

func TestReadFile_OutsideRoots(t *testing.T) {
	h, _, _ := testDir(t)
	rec := getReq(t, h, "/api/file?path=etc/passwd")
	if rec.Code != 403 {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestReadFile_Binary(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "bin.dat"), []byte{0x89, 0x50, 0x4E, 0x47, 0x00}, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := getReq(t, h, "/api/file?path="+prefix+"/bin.dat")
	if rec.Code != 415 {
		t.Errorf("status = %d, want 415", rec.Code)
	}
}

func TestReadFile_MissingPath(t *testing.T) {
	h, _, _ := testDir(t)
	rec := getReq(t, h, "/api/file")
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestReadFile_MissingOnDisk(t *testing.T) {
	h, _, prefix := testDir(t)
	rec := getReq(t, h, "/api/file?path="+prefix+"/missing.txt")
	if rec.Code != 404 {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestReadFile_IsDirectory(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := getReq(t, h, "/api/file?path="+prefix+"/sub")
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestReadFile_TooLarge(t *testing.T) {
	h, dir, prefix := testDir(t)
	big := bytes.Repeat([]byte("a"), MaxFileSize+1)
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := getReq(t, h, "/api/file?path="+prefix+"/big.txt")
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}

// Writing onto a directory returns a clean 400, not a
// generic 500 leaking the raw EISDIR path.
func TestWriteFile_TargetIsDirectory(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := putReq(t, h, "/api/file?path="+prefix+"/sub", `{"content":"x"}`)
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "is a directory") == false {
		t.Errorf("body = %s, want \"path is a directory\"", rec.Body.String())
	}
}

func TestWriteFile_InvalidJSON(t *testing.T) {
	h, _, prefix := testDir(t)
	rec := putReq(t, h, "/api/file?path="+prefix+"/x.txt", `{not json`)
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestFile_MethodNotAllowed(t *testing.T) {
	h, _, prefix := testDir(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodDelete, "/api/file?path="+prefix+"/x", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 405 {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

// The root listing is synthetic: exactly the granted mounts, sorted by
// name, never writable. Nothing else on the host filesystem leaks in.
func TestListFiles_Root_ListsMounts(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	h, err := New(Sensitive{}, []string{dirB, dirA})
	if err != nil {
		t.Fatal(err)
	}
	rec := getReq(t, h, "/api/files?path=.")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Path     string `json:"path"`
		Files    []fileEntry
		Writable bool
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Writable {
		t.Error("synthetic root must not be writable")
	}
	wantNames := []string{strings.TrimPrefix(dirA, "/"), strings.TrimPrefix(dirB, "/")}
	slices.Sort(wantNames)
	if len(resp.Files) != 2 {
		t.Fatalf("files = %+v, want exactly the 2 mounts", resp.Files)
	}
	for i, want := range wantNames {
		f := resp.Files[i]
		if f.Name != want || !f.IsDir {
			t.Errorf("files[%d] = %+v, want dir %q", i, f, want)
		}
	}
}

func TestListFiles_Subdir(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	rec := getReq(t, h, "/api/files?path="+prefix)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Files    []fileEntry
		Writable bool
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Files) != 2 {
		t.Fatalf("files count = %d, want 2", len(resp.Files))
	}
	if !resp.Writable {
		t.Error("expected writable=true for temp dir")
	}
}

func TestListFiles_EmptyPath_TreatedAsRoot(t *testing.T) {
	h, _, _ := testDir(t)
	rec := getReq(t, h, "/api/files?path=")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Path != "/" {
		t.Errorf("path = %q, want %q", resp.Path, "/")
	}
}

func TestListFiles_NotFound(t *testing.T) {
	h, _, prefix := testDir(t)
	rec := getReq(t, h, "/api/files?path="+prefix+"/does-not-exist")
	if rec.Code != 404 {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestListFiles_MethodNotAllowed(t *testing.T) {
	h, _, _ := testDir(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, "/api/files?path=.", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 405 {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestAction_Mkdir(t *testing.T) {
	h, dir, prefix := testDir(t)
	rec := postReq(t, h, "/api/files/action", `{"action":"mkdir","path":"`+prefix+`/newdir"}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	info, err := os.Stat(filepath.Join(dir, "newdir"))
	if err != nil || !info.IsDir() {
		t.Error("directory not created")
	}
}

func TestAction_Touch(t *testing.T) {
	h, dir, prefix := testDir(t)
	rec := postReq(t, h, "/api/files/action", `{"action":"touch","path":"`+prefix+`/new.txt"}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); err != nil {
		t.Error("file not created")
	}
}

func TestAction_Delete(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "del.txt"), []byte("bye"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := postReq(t, h, "/api/files/action", `{"action":"delete","path":"`+prefix+`/del.txt"}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "del.txt")); err == nil {
		t.Error("file still exists")
	}
}

// swappedAncestor stages the race a pinned parent exists for: a directory component that was real
// and empty when the policy resolved it is swapped for a symlink to a sensitive directory before
// the operation.
func swappedAncestor(t *testing.T, h *Handler, dir string) (victim string, l loc) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "store"), 0o755); err != nil {
		t.Fatal(err)
	}
	victim = filepath.Join(dir, "store", "keep.txt")
	if err := os.WriteFile(victim, []byte("the chat store"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	l = locAt(h, filepath.Join(dir, "x", "keep.txt"))
	if err := os.Remove(filepath.Join(dir, "x")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("store", filepath.Join(dir, "x")); err != nil {
		t.Fatal(err)
	}
	return victim, l
}

// TestActionDelete_RefusesASwappedAncestor: the sensitive check is exact-prefix over the resolved
// path, so only the pinned parent stops a delete through a swapped ancestor.
func TestActionDelete_RefusesASwappedAncestor(t *testing.T) {
	h, dir, _ := testDir(t)
	victim, l := swappedAncestor(t, h, dir)

	err := actionDelete(context.Background(), httptest.NewRecorder(), fileAction{Action: "delete"}, l, h)
	if err == nil {
		t.Error("delete through a symlinked ancestor was accepted, want a refusal")
	}
	if _, statErr := os.Lstat(victim); statErr != nil {
		t.Errorf("the protected file was deleted through the symlinked ancestor: %v", statErr)
	}
}

// TestAction_SecurityRejections collects every action rejection that must answer 403 for a
// sensitive, protected or out-of-grant path.
func TestAction_SecurityRejections(t *testing.T) {
	type secCase struct {
		checkSourceExists  string
		action             string
		pathSuffix         string
		dest               string
		renameName         string
		sensitivePrefix    string
		setupFile          string
		setupDir           string
		name               string
		checkDestAbsent    string
		wantBodyContains   string
		useTempDir         bool
		checkNoTempOrphans bool
	}

	cases := []secCase{
		{
			name:            "mkdir/protected_dir",
			action:          "mkdir",
			useTempDir:      true,
			pathSuffix:      "chats",
			sensitivePrefix: "chats/",
			checkDestAbsent: "chats",
		},
		{
			name:            "touch/protected_path",
			action:          "touch",
			useTempDir:      true,
			pathSuffix:      "chats",
			sensitivePrefix: "chats/",
			checkDestAbsent: "chats",
		},
		{
			name:            "touch/sensitive_exact_file",
			action:          "touch",
			useTempDir:      true,
			pathSuffix:      "secret.json",
			sensitivePrefix: "secret.json",
			checkDestAbsent: "secret.json",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, dir, prefix := testDir(t)

			if tc.sensitivePrefix != "" && tc.useTempDir {
				var entry string
				isDir := strings.HasSuffix(tc.sensitivePrefix, "/")
				if isDir {
					entry = filepath.Join("/", prefix, tc.sensitivePrefix) + "/"
				} else {
					entry = filepath.Join("/", prefix, tc.sensitivePrefix)
				}
				h.sensitive = h.sensitive.with(sensitivePath{Path: entry, IsDir: isDir})
			}

			if tc.setupFile != "" {
				if err := os.WriteFile(filepath.Join(dir, tc.setupFile), []byte("payload"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.setupDir != "" {
				if err := os.Mkdir(filepath.Join(dir, tc.setupDir), 0o755); err != nil {
					t.Fatal(err)
				}
			}

			var reqPath string
			if tc.useTempDir {
				reqPath = prefix + "/" + tc.pathSuffix
			} else {
				reqPath = tc.pathSuffix
			}

			var body string
			switch {
			case tc.dest != "":
				destPath := prefix + "/" + tc.dest
				body = `{"action":"` + tc.action + `","path":"` + reqPath + `","dest":"` + destPath + `"}`
			case tc.renameName != "":
				body = `{"action":"` + tc.action + `","path":"` + reqPath + `","name":"` + tc.renameName + `"}`
			default:
				body = `{"action":"` + tc.action + `","path":"` + reqPath + `"}`
			}

			rec := postReq(t, h, "/api/files/action", body)
			if rec.Code != 403 {
				t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
			}

			if tc.wantBodyContains != "" {
				if !strings.Contains(rec.Body.String(), tc.wantBodyContains) {
					t.Errorf("body = %s, want substring %q", rec.Body.String(), tc.wantBodyContains)
				}
			}

			if tc.checkSourceExists != "" {
				if _, err := os.Stat(filepath.Join(dir, tc.checkSourceExists)); err != nil {
					t.Errorf("source %q removed despite rejection: %v", tc.checkSourceExists, err)
				}
			}

			if tc.checkDestAbsent != "" {
				if _, err := os.Stat(filepath.Join(dir, tc.checkDestAbsent)); err == nil {
					t.Errorf("destination %q created despite rejection", tc.checkDestAbsent)
				}
			}

			if tc.checkNoTempOrphans {
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range entries {
					if strings.Contains(e.Name(), ".copy-") {
						t.Errorf("leftover copy temp file %q", e.Name())
					}
				}
			}
		})
	}
}

func TestAction_Rename(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "old.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := postReq(t, h, "/api/files/action", `{"action":"rename","path":"`+prefix+`/old.txt","name":"new.txt"}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, "new.txt"))
	if err != nil || string(data) != "data" {
		t.Error("renamed file missing or wrong content")
	}
}

func TestAction_Rename_InvalidNames(t *testing.T) {
	bad := []string{"", ".", "..", "a/b", "a\\b", "with\x00nul"}
	for _, name := range bad {
		t.Run("name="+name, func(t *testing.T) {
			h, dir, prefix := testDir(t)
			if err := os.WriteFile(filepath.Join(dir, "old.txt"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			nameJSON, _ := json.Marshal(name)
			body := `{"action":"rename","path":"` + prefix + `/old.txt","name":` + string(nameJSON) + `}`
			rec := postReq(t, h, "/api/files/action", body)
			if rec.Code != 400 {
				t.Errorf("rename name=%q: status = %d, want 400", name, rec.Code)
			}
			if _, err := os.Stat(filepath.Join(dir, "old.txt")); err != nil {
				t.Errorf("rename name=%q: original file missing: %v", name, err)
			}
		})
	}
}

func TestAction_OutsideRoots(t *testing.T) {
	h, _, _ := testDir(t)
	rec := postReq(t, h, "/api/files/action", `{"action":"touch","path":"etc/evil.txt"}`)
	if rec.Code != 403 {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestAction_UnknownAction(t *testing.T) {
	h, _, prefix := testDir(t)
	rec := postReq(t, h, "/api/files/action", `{"action":"nope","path":"`+prefix+`/x"}`)
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestAction_MethodNotAllowed(t *testing.T) {
	h, _, _ := testDir(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, "/api/files/action", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 405 {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestAction_InvalidJSON(t *testing.T) {
	h, _, _ := testDir(t)
	rec := postReq(t, h, "/api/files/action", `not json at all`)
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// stubAvailableBytes drives the copy and upload free-space prechecks from a
// test without filling a real filesystem: the seam exists precisely so the
// refusal path is reachable at a few bytes instead of a few gigabytes. It
// replaces a package-level var, so its callers must not run in parallel.
func stubAvailableBytes(t *testing.T, avail int64, err error) {
	t.Helper()
	orig := availableBytes
	t.Cleanup(func() { availableBytes = orig })
	availableBytes = func(string) (int64, error) { return avail, err }
}

func TestHandleDownload_File(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "doc.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := getReq(t, h, "/api/file/download?path="+prefix+"/doc.txt")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "hello" {
		t.Errorf("body = %q, want %q", got, "hello")
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.Contains(cd, `filename="doc.txt"`) {
		t.Errorf("Content-Disposition = %q, want attachment filename", cd)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain*", ct)
	}
}

// condGet issues a conditional download the way a browser revalidating a
// no-cache response does: both validators from the previous 200.
func condGet(t *testing.T, h *Handler, path, etag, lastMod string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastMod != "" {
		req.Header.Set("If-Modified-Since", lastMod)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestHandleDownload_ETagSurvivesASameSecondRewrite pins the strong validator: with Last-Modified
// alone a rewrite inside one second answers 304 with stale bytes.
func TestHandleDownload_ETagSurvivesASameSecondRewrite(t *testing.T) {
	h, dir, prefix := testDir(t)
	path := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(path, []byte("frame-one"), 0o644); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, base, base.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.ModTime().Nanosecond() == 0 {
		t.Skipf("%s stores mtime at second granularity, so sub-second rewrites are indistinguishable here", dir)
	}

	url := "/api/file/download?path=" + prefix + "/shot.png"
	first := getReq(t, h, url)
	if first.Code != 200 {
		t.Fatalf("first GET: status %d: %s", first.Code, first.Body.String())
	}
	etag, lastMod := first.Header().Get("ETag"), first.Header().Get("Last-Modified")
	if len(etag) < 3 || !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `"`) {
		t.Fatalf("ETag = %q, want a quoted strong validator (an unquoted one ServeContent never matches)", etag)
	}

	if rec := condGet(t, h, url, etag, lastMod); rec.Code != http.StatusNotModified {
		t.Errorf("unchanged file: status = %d, want 304", rec.Code)
	}

	if err := os.WriteFile(path, []byte("frame-two"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, base, base.Add(2*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	rec := condGet(t, h, url, etag, lastMod)
	if rec.Code != 200 {
		t.Fatalf("same-second rewrite: status = %d, want 200 with the new bytes", rec.Code)
	}
	if got := rec.Body.String(); got != "frame-two" {
		t.Errorf("same-second rewrite: body = %q, want %q", got, "frame-two")
	}
	if next := rec.Header().Get("ETag"); next == etag {
		t.Errorf("ETag = %q for both generations, want it to change", next)
	}
}

// TestHandleDownload_SVGIsAttachment pins the one download header that is a SECURITY control: an
// SVG served inline runs script as a document.
func TestHandleDownload_SVGIsAttachment(t *testing.T) {
	h, dir, prefix := testDir(t)
	const svg = `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`
	if err := os.WriteFile(filepath.Join(dir, "arch.svg"), []byte(svg), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := getReq(t, h, "/api/file/download?path="+prefix+"/arch.svg")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/svg+xml") {
		t.Errorf("Content-Type = %q, want image/svg+xml*", ct)
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment") {
		t.Errorf("Content-Disposition = %q, want to start with attachment", cd)
	}
	if strings.Contains(cd, "inline") {
		t.Errorf("Content-Disposition = %q, must never be inline for image/svg+xml", cd)
	}
	if got := rec.Body.String(); got != svg {
		t.Errorf("body = %q, want the file verbatim", got)
	}
}

func TestHandleDownload_UnknownExtension(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "blob"), []byte{0x01, 0x02}, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := getReq(t, h, "/api/file/download?path="+prefix+"/blob")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if ct == "" {
		t.Errorf("Content-Type unexpectedly empty")
	}
}

// TestHandleDownload_ErrorPaths consolidates the 5 error-path download
// tests into a single table-driven test. The 2 happy-path tests (File,
// UnknownExtension) remain separate because they assert response headers
// and body content.
func TestHandleDownload_ErrorPaths(t *testing.T) {
	type dlCase struct {
		name       string
		path       string
		setupDir   string
		method     string
		wantStatus int
	}

	h, dir, prefix := testDir(t)

	cases := []dlCase{
		{
			name:       "missing_path_param",
			path:       "",
			wantStatus: 400,
		},
		{
			name:       "missing_file",
			path:       prefix + "/missing.txt",
			wantStatus: 404,
		},
		{
			name:       "directory_rejected",
			path:       prefix + "/sub",
			setupDir:   "sub",
			wantStatus: 400,
		},
		{
			name:       "blacklisted",
			path:       "etc/passwd",
			wantStatus: 403,
		},
		{
			name:       "method_not_allowed",
			path:       prefix,
			method:     http.MethodPost,
			wantStatus: 405,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setupDir != "" {
				if err := os.MkdirAll(filepath.Join(dir, tc.setupDir), 0o755); err != nil {
					t.Fatal(err)
				}
			}

			url := "/api/file/download"
			if tc.path != "" {
				url += "?path=" + tc.path
			}

			method := http.MethodGet
			if tc.method != "" {
				method = tc.method
			}

			mux := http.NewServeMux()
			h.RegisterRoutes(mux)
			req := httptest.NewRequest(method, url, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d; body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestHandleUpload_SingleFile(t *testing.T) {
	h, dir, prefix := testDir(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := multipartUpload(t, prefix, map[string][]byte{"hello.txt": []byte("world")})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Uploaded []string `json:"uploaded"`
		OK       bool     `json:"ok"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.OK || len(resp.Uploaded) != 1 || resp.Uploaded[0] != "hello.txt" {
		t.Errorf("resp = %+v, want {OK:true Uploaded:[hello.txt]}", resp)
	}
	data, err := os.ReadFile(filepath.Join(dir, "hello.txt"))
	if err != nil || string(data) != "world" {
		t.Errorf("disk content = %q err=%v, want %q", string(data), err, "world")
	}
}

// Partial writes never surface under the user's
// filename. The temp-rename pattern leaves `.upload-*` siblings on
// error, never a truncated file at the expected path.
func TestHandleUpload_NoPartialFileOnSuccess(t *testing.T) {
	h, dir, prefix := testDir(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := multipartUpload(t, prefix, map[string][]byte{"ok.txt": []byte("contents")})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".upload-") {
			t.Errorf("leftover temp file %q", e.Name())
		}
	}
}

func TestHandleUpload_MultipleFiles(t *testing.T) {
	h, dir, prefix := testDir(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := multipartUpload(t, prefix, map[string][]byte{
		"a.txt": []byte("A"),
		"b.txt": []byte("B"),
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("file %s missing: %v", name, err)
		}
	}
}

func TestHandleUpload_StripsPathPrefixInFilename(t *testing.T) {
	h, dir, prefix := testDir(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := multipartUpload(t, prefix,
		map[string][]byte{"subdir/hidden.txt": []byte("naughty")})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "hidden.txt")); err != nil {
		t.Errorf("expected basename-only file, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "subdir")); err == nil {
		t.Error("handler escaped target dir via path prefix")
	}
}

// Invalid filenames surface as 400 with a descriptive
// error instead of silently succeeding with a subset `uploaded` array.
func TestHandleUpload_DotDotFilenameReturns400(t *testing.T) {
	h, _, prefix := testDir(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := multipartUpload(t, prefix, map[string][]byte{"..": []byte("skip")})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid filename") {
		t.Errorf("body = %s, want \"invalid filename\"", rec.Body.String())
	}
}

func TestHandleUpload_NoFiles(t *testing.T) {
	h, _, prefix := testDir(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("dir", prefix)
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/file/upload", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandleUpload_OutsideRootsDir(t *testing.T) {
	h, _, _ := testDir(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := multipartUpload(t, "etc", map[string][]byte{"x.txt": []byte("x")})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

// TestHandleUpload_RefusesProtectedDir: a dir= naming a protected directory, or a container of one,
// is refused.
func TestHandleUpload_RefusesProtectedDir(t *testing.T) {
	h, dir, prefix := testDir(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	h.sensitive = h.sensitive.with(sensitivePath{Path: filepath.Join("/", prefix, "chats") + "/", IsDir: true})

	if err := os.Mkdir(filepath.Join(dir, "chats"), 0o755); err != nil {
		t.Fatal(err)
	}

	req := multipartUpload(t, prefix+"/chats",
		map[string][]byte{"fake-chat.json": []byte("[]")})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "chats", "fake-chat.json")); err == nil {
		t.Error("file written despite protected-dir rejection")
	}
}

// TestHandleUpload_RefusesSensitiveFilename: an upload into an ordinary parent must not overwrite a
// sensitive exact-match file.
func TestHandleUpload_RefusesSensitiveFilename(t *testing.T) {
	h, dir, prefix := testDir(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	secret := filepath.Join(dir, "keys.json")
	h.sensitive = h.sensitive.with(sensitivePath{Path: secret, IsDir: false})

	req := multipartUpload(t, prefix,
		map[string][]byte{"keys.json": []byte("[]")})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 400 && rec.Code != 403 {
		t.Fatalf("status = %d, want 400 or 403; body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(secret); err == nil {
		t.Error("sensitive file written despite sensitive-path gates")
	}
}

// TestWriteFile_DanglingSymlinkToSensitive_Blocked: a symlink planted between resolvePath and the
// write must not materialise its target.
func TestWriteFile_DanglingSymlinkToSensitive_Blocked(t *testing.T) {
	h, dir, prefix := testDir(t)

	target := filepath.Join(dir, "protected.json")
	h.sensitive = h.sensitive.with(sensitivePath{Path: target, IsDir: false})

	link := filepath.Join(dir, "trojan")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	rec := putReq(t, h, "/api/file?path="+prefix+"/trojan",
		`{"content":"pwn"}`)
	if rec.Code != 400 && rec.Code != 403 && rec.Code != 500 {
		t.Errorf("status = %d, want 400, 403 or 500; body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(target); err == nil {
		t.Error("sensitive target file was created despite symlink guard")
	}
}

// TestWriteFile_RelativeSymlinkSwappedAfterResolve: a RELATIVE symlink swapped in after resolvePath
// accepted a regular file must not be written through.
func TestWriteFile_RelativeSymlinkSwappedAfterResolve(t *testing.T) {
	h, dir, _ := testDir(t)

	victim := filepath.Join(dir, "victim.txt")
	if err := os.WriteFile(victim, []byte("KEEP"), 0o600); err != nil {
		t.Fatalf("seed victim: %v", err)
	}
	decoy := filepath.Join(dir, "decoy.txt")
	if err := os.WriteFile(decoy, []byte("decoy"), 0o600); err != nil {
		t.Fatalf("seed decoy: %v", err)
	}

	l, err := h.resolvePath(strings.TrimPrefix(decoy, "/"))
	if err != nil {
		t.Fatalf("resolvePath(%q) = %v, want nil", decoy, err)
	}

	swap := func() {
		t.Helper()
		if rErr := os.Remove(decoy); rErr != nil && !errors.Is(rErr, os.ErrNotExist) {
			t.Fatalf("remove decoy: %v", rErr)
		}
		if sErr := os.Symlink("victim.txt", decoy); sErr != nil {
			t.Fatalf("symlink decoy -> victim: %v", sErr)
		}
	}

	swap()
	f, err := l.m.root.OpenFile(l.rel(),
		os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		t.Fatalf("Root.OpenFile with O_NOFOLLOW refused a relative in-root symlink (%v); "+
			"the exposure this test exists to close is not reachable, so the refusal "+
			"below proves nothing", err)
	}
	if _, err := f.WriteString("CLOBBERED"); err != nil {
		t.Fatalf("write through the link: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got, _ := os.ReadFile(victim); string(got) != "CLOBBERED" {
		t.Fatalf("victim after the ambient write = %q, want %q", got, "CLOBBERED")
	}

	if err := os.WriteFile(victim, []byte("KEEP"), 0o600); err != nil {
		t.Fatalf("restore victim: %v", err)
	}
	swap()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/file", strings.NewReader(`{"content":"pwn"}`))
	writeFile(rec, req, l, SaveHook{})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if got, _ := os.ReadFile(victim); string(got) != "KEEP" {
		t.Errorf("victim after the refused write = %q, want %q", got, "KEEP")
	}
}

func TestHandleUpload_MethodNotAllowed(t *testing.T) {
	h, _, _ := testDir(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, "/api/file/upload", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 405 {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestHandleUpload_InvalidMultipart(t *testing.T) {
	h, _, _ := testDir(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, "/api/file/upload",
		strings.NewReader("not multipart"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// An oversize upload returns 413, not a generic 400.
func TestHandleUpload_TooLargeReturns413(t *testing.T) {
	h, _, prefix := testDir(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Streamed rather than built, so the test is not the package's memory peak.
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pr.Close() })
	mw := multipart.NewWriter(pw)
	go func() {
		if err := mw.WriteField("dir", prefix); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		part, err := mw.CreateFormFile("files", "big.bin")
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		if _, err := io.CopyN(part, filler{}, maxUploadSize+1024); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_ = mw.Close()
		_ = pw.Close()
	}()

	req := httptest.NewRequest(http.MethodPost, "/api/file/upload", pr)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413; body=%s", rec.Code, rec.Body.String())
	}
}

// filler is an unbounded byte source, so an oversize request body costs a
// buffer rather than its own length in memory.
type filler struct{}

func (filler) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}

// TestResolvePath_SymlinkAncestorWithDeepMissingLeaf: a symlinked ancestor over two or more missing
// components must not bypass the allow-list.
func TestResolvePath_SymlinkAncestorWithDeepMissingLeaf_Rejected(t *testing.T) {
	h, dir, prefix := testDir(t)

	link := filepath.Join(dir, "evil")
	if err := os.Symlink("/etc", link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, err := h.resolvePath(prefix + "/evil/newdir/sub")
	if err == nil {
		t.Errorf("resolvePath through symlinked ancestor + deep missing leaf returned nil error; expected rejection")
	}
}

// actionMkdir must not create directories under a symlinked ancestor's
// blacklisted target. A temp dir registered as sensitive stands in for /etc,
// so the test writes nothing real even if the guard fails.
func TestAction_Mkdir_ThroughSymlinkedAncestor_Rejected(t *testing.T) {
	h, dir, prefix := testDir(t)

	sink := t.TempDir()
	link := filepath.Join(dir, "evil")
	if err := os.Symlink(sink, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	h.sensitive = h.sensitive.with(sensitivePath{Path: sink + "/", IsDir: true})

	rec := postReq(t, h, "/api/files/action",
		`{"action":"mkdir","path":"`+prefix+`/evil/newdir/sub"}`)
	if rec.Code != 403 {
		t.Errorf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(sink, "newdir")); err == nil {
		t.Error("created dir under symlink target despite rejection")
	}
}

// Direct ctxReader coverage: writeUploads exercises the cancellation branch only indirectly, where
// a successful upload masks a regression.

// errReader always returns its configured error on Read.
type errReader struct{ err error }

func (e *errReader) Read(_ []byte) (int, error) { return 0, e.err }

func TestCtxReader_Read(t *testing.T) {
	cancelledCtx, cancel := context.WithCancel(t.Context())
	cancel()

	expiredCtx, expiredCancel := context.WithDeadline(t.Context(), time.Now().Add(-1*time.Second))
	defer expiredCancel()

	sentinel := errors.New("inner io failure")

	cases := []struct {
		ctx     context.Context
		inner   io.Reader
		wantErr error
		name    string
		wantN   int
	}{
		{
			name:    "cancelled_context",
			ctx:     cancelledCtx,
			inner:   strings.NewReader("should not be read"),
			wantN:   0,
			wantErr: context.Canceled,
		},
		{
			name:    "deadline_exceeded",
			ctx:     expiredCtx,
			inner:   strings.NewReader("unused"),
			wantN:   0,
			wantErr: context.DeadlineExceeded,
		},
		{
			name:    "live_context_forwards_read",
			ctx:     t.Context(),
			inner:   strings.NewReader("hello"),
			wantN:   5,
			wantErr: nil,
		},
		{
			name:    "live_context_forwards_inner_error",
			ctx:     t.Context(),
			inner:   &errReader{err: sentinel},
			wantN:   0,
			wantErr: sentinel,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr := &ctxReader{ctx: tc.ctx, r: tc.inner}
			buf := make([]byte, 8)
			n, err := cr.Read(buf)

			if n != tc.wantN {
				t.Errorf("ctxReader.Read() n = %d, want %d", n, tc.wantN)
			}
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("ctxReader.Read() err = %v, want %v", err, tc.wantErr)
				}
			} else if err != nil && !errors.Is(err, io.EOF) {
				t.Errorf("ctxReader.Read() unexpected err = %v", err)
			}
		})
	}
}

// TestAction_Rename_DestRunsResolvePath pins that rename's destination runs through resolvePath,
// not only the lexical checks.
func TestAction_Rename_DestRunsResolvePath(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "old.txt"),
		[]byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.sensitive = h.sensitive.with(sensitivePath{Path: filepath.Join("/", prefix, "chats") + "/", IsDir: true})

	rec := postReq(t, h, "/api/files/action",
		`{"action":"rename","path":"`+prefix+`/old.txt","name":"chats"}`)
	if rec.Code != 403 {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "old.txt")); err != nil {
		t.Errorf("source file disappeared after rejected rename: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "chats")); err == nil {
		t.Error("destination created despite rejection")
	}
}

func TestWriteUploads_ContextCancelled_AbortsEarly(t *testing.T) {
	h, dir, _ := testDir(t)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		fw, err := w.CreateFormFile("files", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte("content-" + name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	reader := multipart.NewReader(&buf, w.Boundary())
	form, err := reader.ReadForm(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	defer form.RemoveAll()

	files := form.File["files"]
	if len(files) != 3 {
		t.Fatalf("expected 3 file headers, got %d", len(files))
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	uploaded, _, wErr := writeUploads(ctx, locAt(h, dir), files, h.sensitive)
	if wErr == nil {
		t.Fatal("expected error from cancelled context")
	}
	if !errors.Is(wErr, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", wErr)
	}
	if len(uploaded) != 0 {
		t.Errorf("expected 0 uploaded files, got %d", len(uploaded))
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("expected empty dir, got %d entries", len(entries))
	}
}

func BenchmarkResolvePath(b *testing.B) {
	dir := b.TempDir()
	prefix := strings.TrimPrefix(dir, "/")
	h, err := New(Sensitive{}, []string{dir})
	if err != nil {
		b.Fatal(err)
	}

	deep := filepath.Join(dir, "a", "b", "c", "d")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		b.Fatal(err)
	}

	linkTarget := filepath.Join(dir, "a", "b")
	link := filepath.Join(dir, "linked")
	if err := os.Symlink(linkTarget, link); err != nil {
		b.Fatal(err)
	}

	cases := []struct {
		name string
		path string
	}{
		{"allowed_path", prefix + "/a/b/c/d"},
		{"outside_roots_path", "etc/passwd"},
		{"deep_nested_path", prefix + "/a/b/c/d/../../../b/c/d"},
		{"symlink_path", prefix + "/linked/c/d"},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				h.resolvePath(tc.path)
			}
		})
	}
}

func FuzzResolvePath(f *testing.F) {
	dir := f.TempDir()
	backing, err := os.OpenRoot(f.TempDir())
	if err != nil {
		f.Fatal(err)
	}
	h, err := New(Sensitive{}, []string{dir})
	if err != nil {
		f.Fatal(err)
	}
	h.mounts = append(h.mounts, mount{root: backing, dir: "/config", name: "config"})
	prefix := strings.TrimPrefix(dir, "/")

	seeds := []string{
		prefix + "/myrepo/file.go",
		"../../../etc/passwd",
		prefix + "/../../etc/shadow",
		"etc/passwd",
		"proc/1/status",
		"var/log/syslog",
		"/",
		".",
		"..",
		prefix + "/./../../etc/passwd",
		prefix + "/%2e%2e/etc/passwd",
		prefix + "/\x00/etc/passwd",
		prefix + "/symlink/../../../etc",
		strings.Repeat("../", 50) + "etc/passwd",
		prefix + "/a/b/c/../../../../etc/passwd",
		"config/home/.kiro/steering/marotte.md",
		"config/chats/deep/nested.json",
		"config/push-subs.json",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, input string) {
		result, err := h.resolvePath(input)
		if err != nil {
			return
		}

		if !filepath.IsAbs(result.abs) {
			t.Errorf("resolvePath(%q) = %q is not absolute", input, result.abs)
		}
		if cleaned := filepath.Clean(result.abs); cleaned != result.abs {
			t.Errorf("resolvePath(%q) = %q is not clean (Clean = %q)", input, result.abs, cleaned)
		}

		owner := h.mountFor(result.abs)
		if owner == nil {
			t.Errorf("resolvePath(%q) = %q is outside every granted mount", input, result.abs)
		} else if result.m != owner {
			t.Errorf("resolvePath(%q) returned mount %q, want owner %q", input, result.m.dir, owner.dir)
		}

		if (Sensitive{}).Blocks(result.abs) {
			t.Errorf("resolvePath(%q) = %q is a sensitive path", input, result.abs)
		}
	})
}

func FuzzSensitiveBlocks(f *testing.F) {
	seeds := []string{
		"/config/home/.kiro/steering/marotte.md",
		"/config/home/.kiro/steering/environment.md",
		"/config/home/.kiro/agents/foo.json",
		"/config/chats/deep/nested.json",
		"/config/push-subs.json",
		"/config/vapid-keys.json",
		"/workspace/repo/main.go",
		"/config/home/.kiro/steering/other.md",
		"/config/chats",
		"/config/chats/",
		"/config/home/.kiro/agents/",
		"/config/home/.kiro/agents",
		"",
		"/",
		"/config",
		"/config/home/.kiro/steering/marotte.md/extra",
		"/config/push-subs.json/",
		"/config/push-subs.jsonx",
		"\x00",
		"/config/chats/\x00/evil",
		"//config//chats//foo",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, input string) {
		result := (Sensitive{}).Blocks(input)

		if result {
			matched := false
			for _, sp := range (Sensitive{}).entries() {
				if sp.IsDir {
					if strings.HasPrefix(input, sp.Path) {
						matched = true
						break
					}
				} else if input == sp.Path {
					matched = true
					break
				}
			}
			if !matched {
				t.Errorf("Blocks(%q) = true but no deny-list entry matches", input)
			}
		}

		if !result {
			for _, sp := range (Sensitive{}).entries() {
				if sp.IsDir {
					if strings.HasPrefix(input, sp.Path) {
						t.Errorf("Blocks(%q) = false but matches dir prefix %q", input, sp.Path)
					}
				} else if input == sp.Path {
					t.Errorf("Blocks(%q) = false but matches exact path %q", input, sp.Path)
				}
			}
		}
	})
}

// discardStatusRecorder captures the HTTP status code without buffering
// the response body, so the large-file download tests never allocate the
// served payload.
type discardStatusRecorder struct {
	hdr     http.Header
	code    int
	written bool
}

func (s *discardStatusRecorder) Header() http.Header {
	if s.hdr == nil {
		s.hdr = http.Header{}
	}
	return s.hdr
}

func (s *discardStatusRecorder) WriteHeader(code int) {
	if !s.written {
		s.code = code
		s.written = true
	}
}

func (s *discardStatusRecorder) Write(p []byte) (int, error) {
	if !s.written {
		s.code = http.StatusOK
		s.written = true
	}
	return len(p), nil
}

// actionMkdir creates any path inside a mount; only the mount point
// itself is refused.
func TestAction_Mkdir_InsideMount(t *testing.T) {
	h, dir, _ := testDir(t)
	l := locAt(h, filepath.Join(dir, "a", "b"))
	err := actionMkdir(t.Context(), httptest.NewRecorder(), fileAction{}, l, h)
	if err != nil {
		t.Fatalf("actionMkdir(%q) = %v, want nil (paths inside a mount must be creatable)", l.abs, err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "a", "b")); statErr != nil {
		t.Errorf("actionMkdir(%q): expected dir created at <mount>/a/b, stat err: %v", l.abs, statErr)
	}
}

// actionMkdir returns the raw MkdirAll error (MkdirAll under a regular
// file is ENOTDIR) rather than swallowing it or returning errHandled.
func TestAction_Mkdir_PropagatesError(t *testing.T) {
	h, dir, _ := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "afile"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	l := locAt(h, filepath.Join(dir, "afile", "sub"))
	err := actionMkdir(t.Context(), httptest.NewRecorder(), fileAction{}, l, h)
	if err == nil {
		t.Fatalf("actionMkdir(%q) = nil, want non-nil (MkdirAll under a regular file must error)", l.abs)
	}
	if errors.Is(err, errHandled) {
		t.Fatalf("actionMkdir(%q) returned errHandled; expected the raw MkdirAll error", l.abs)
	}
}

// actionTouch creates a file anywhere inside the mount with an
// existing parent.
func TestAction_Touch_InsideMount(t *testing.T) {
	h, dir, _ := testDir(t)
	if err := os.Mkdir(filepath.Join(dir, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	l := locAt(h, filepath.Join(dir, "x", "y"))
	err := actionTouch(t.Context(), httptest.NewRecorder(), fileAction{}, l, h)
	if err != nil {
		t.Fatalf("actionTouch(%q) = %v, want nil (paths inside a mount must be touchable)", l.abs, err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "x", "y")); statErr != nil {
		t.Errorf("actionTouch(%q): expected file created at <mount>/x/y, stat err: %v", l.abs, statErr)
	}
}

// actionTouch returns the raw OpenFile error (OpenFile under a regular
// file is ENOTDIR) rather than swallowing it or closing a nil file.
func TestAction_Touch_PropagatesOpenError(t *testing.T) {
	h, dir, _ := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "pfile"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	l := locAt(h, filepath.Join(dir, "pfile", "child"))
	err := actionTouch(t.Context(), httptest.NewRecorder(), fileAction{}, l, h)
	if err == nil {
		t.Fatalf("actionTouch(%q) = nil, want non-nil (OpenFile under a regular file must error)", l.abs)
	}
	if errors.Is(err, errHandled) {
		t.Fatalf("actionTouch(%q) returned errHandled; expected the raw OpenFile error", l.abs)
	}
}

// actionDelete removes anything inside the mount; only the mount point
// itself is refused.
func TestAction_Delete_InsideMount(t *testing.T) {
	h, dir, _ := testDir(t)
	if err := os.MkdirAll(filepath.Join(dir, "p", "q"), 0o755); err != nil {
		t.Fatal(err)
	}
	l := locAt(h, filepath.Join(dir, "p", "q"))
	err := actionDelete(t.Context(), httptest.NewRecorder(), fileAction{}, l, h)
	if err != nil {
		t.Fatalf("actionDelete(%q) = %v, want nil (paths inside a mount must be deletable)", l.abs, err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "p", "q")); !os.IsNotExist(statErr) {
		t.Errorf("actionDelete(%q): expected <mount>/p/q removed, stat err = %v", l.abs, statErr)
	}
}

// actionDelete returns the raw RemoveAll error when the path traverses
// an escaping symlink (os.Root refuses the operation).
func TestAction_Delete_PropagatesRemoveError(t *testing.T) {
	h, dir, _ := testDir(t)
	if err := os.Symlink("/etc", filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	l := locAt(h, filepath.Join(dir, "escape", "leaf"))
	err := actionDelete(t.Context(), httptest.NewRecorder(), fileAction{}, l, h)
	if err == nil {
		t.Fatalf("actionDelete(%q) = nil, want non-nil (RemoveAll through an escaping symlink must error)", l.abs)
	}
	if errors.Is(err, errHandled) {
		t.Fatalf("actionDelete(%q) returned errHandled; expected the raw RemoveAll error", l.abs)
	}
}

// actionRename surfaces the raw Rename error (renaming a missing source)
// past every guard rather than returning errHandled.
func TestAction_Rename_PropagatesError(t *testing.T) {
	h, dir, _ := testDir(t)
	l := locAt(h, filepath.Join(dir, "ghost.txt"))
	err := actionRename(t.Context(), httptest.NewRecorder(),
		fileAction{Name: "renamed.txt"}, l, h)
	if err == nil {
		t.Fatalf("actionRename(%q -> renamed.txt) = nil, want non-nil (renaming a missing source must error)", l.abs)
	}
	if errors.Is(err, errHandled) {
		t.Fatalf("actionRename returned errHandled; expected the raw Rename error (a guard fired before Rename)")
	}
}

// listEntries keeps dotfiles (the synthetic mount listing replaced the
// old real-root special case) and hides sensitive paths.
func TestListEntries_KeepsDotfiles_HidesSensitive(t *testing.T) {
	tmp := t.TempDir()
	for _, name := range []string{"visible.txt", ".hidden", "secret.json"} {
		if err := os.WriteFile(filepath.Join(tmp, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sens := (Sensitive{}).with(sensitivePath{Path: filepath.Join(tmp, "secret.json"), IsDir: false})

	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	got := listEntries(t.Context(), entries, tmp, sens)
	names := map[string]bool{}
	for _, f := range got {
		names[f.Name] = true
	}
	if !names["visible.txt"] || !names[".hidden"] {
		t.Errorf("listEntries dropped a visible entry: %v", names)
	}
	if names["secret.json"] {
		t.Errorf("listEntries leaked the sensitive entry: %v", names)
	}
}

// Reading a file whose size exactly equals MaxFileSize returns 200 with
// the full content: the size guard and the post-read length check are
// both strictly-greater-than, and the LimitReader reads MaxFileSize+1.
func TestReadFile_AtExactMaxSize(t *testing.T) {
	h, dir, prefix := testDir(t)
	content := bytes.Repeat([]byte("a"), MaxFileSize)
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := getReq(t, h, "/api/file?path="+prefix+"/big.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET big.txt (size == MaxFileSize) status = %d, want 200 (a file exactly at the cap is readable)", rec.Code)
	}
	var resp struct {
		Content string `json:"content"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal read response: %v", err)
	}
	if len(resp.Content) != MaxFileSize {
		t.Errorf("read content length = %d, want %d (the full file must be returned, not truncated)",
			len(resp.Content), MaxFileSize)
	}
}

// hugeDownloadSize (1 GiB) is far past any plausible size cap, so serving it
// proves handleDownload has none.
const hugeDownloadSize = 1 << 30

// writeSparseFile creates a file of the requested size without allocating it,
// which is what makes the large-download tests below cost milliseconds.
func writeSparseFile(t *testing.T, path string, size int64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// countingRecorder captures a response's status, headers and byte COUNT while
// keeping at most keep bytes of the body. The Range tests run against a file
// larger than this process should ever hold, so a full ResponseRecorder is not
// an option: a regression that served the whole file has to fail an assertion
// rather than the machine.
type countingRecorder struct {
	hdr     http.Header
	code    int
	written bool
	n       int64
	body    []byte
	keep    int
}

func (c *countingRecorder) Header() http.Header {
	if c.hdr == nil {
		c.hdr = http.Header{}
	}
	return c.hdr
}

func (c *countingRecorder) WriteHeader(code int) {
	if !c.written {
		c.code = code
		c.written = true
	}
}

func (c *countingRecorder) Write(p []byte) (int, error) {
	if !c.written {
		c.code = http.StatusOK
		c.written = true
	}
	c.n += int64(len(p))
	if room := c.keep - len(c.body); room > 0 {
		c.body = append(c.body, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

// The download path is deliberately UNCAPPED: handleDownload hands the open fd
// to http.ServeContent, which streams through a constant-size buffer, so peak
// memory is the same for a 1 MB file and a 10 GB one and a download consumes no
// disk. A file well past the deleted 100 MB guard must answer 200.
func TestHandleDownload_LargeFileIsNotCapped(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeSparseFile(t, filepath.Join(dir, "big.bin"), hugeDownloadSize)

	req := httptest.NewRequest(http.MethodGet, "/api/file/download?path="+prefix+"/big.bin", nil)
	sw := &discardStatusRecorder{}
	h.handleDownload(sw, req)
	if sw.code != http.StatusOK {
		t.Fatalf("download of a %d-byte file status = %d, want 200 (this path is deliberately uncapped)",
			int64(hugeDownloadSize), sw.code)
	}
}

// The concrete argument for deleting that guard: it ran BEFORE ServeContent saw
// the Range header, so the cheapest request there is — the first bytes of a
// huge file — was answered 413. A Range request must come back 206 carrying
// only the bytes asked for.
func TestHandleDownload_RangeOnLargeFileServesOnlyTheRequestedBytes(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeSparseFile(t, filepath.Join(dir, "big.bin"), hugeDownloadSize)

	req := httptest.NewRequest(http.MethodGet, "/api/file/download?path="+prefix+"/big.bin", nil)
	req.Header.Set("Range", "bytes=0-15")
	rec := &countingRecorder{keep: 64}
	h.handleDownload(rec, req)

	if rec.code != http.StatusPartialContent {
		t.Fatalf("Range request on a %d-byte file status = %d, want 206",
			int64(hugeDownloadSize), rec.code)
	}
	if rec.n != 16 {
		t.Errorf("Range request wrote %d bytes, want 16 (only the requested range may be served)", rec.n)
	}
	wantRange := fmt.Sprintf("bytes 0-15/%d", int64(hugeDownloadSize))
	if got := rec.Header().Get("Content-Range"); got != wantRange {
		t.Errorf("Content-Range = %q, want %q", got, wantRange)
	}
}

// writeOneUpload propagates the write error when the destination's
// parent directory is missing.
func TestWriteOneUpload_PropagatesError(t *testing.T) {
	req := multipartUpload(t, "ignored", map[string][]byte{"a.txt": []byte("hi")})
	if err := req.ParseMultipartForm(1 << 20); err != nil {
		t.Fatal(err)
	}
	fhs := req.MultipartForm.File["files"]
	if len(fhs) == 0 {
		t.Fatal("no multipart file parsed")
	}
	h, dir, _ := testDir(t)
	dest := locAt(h, filepath.Join(dir, "missing-dir", "x.txt"))
	n, err := writeOneUpload(t.Context(), dest, fhs[0])
	if err == nil {
		t.Fatalf("writeOneUpload(dest with missing parent) = (n=%d, nil), want non-nil error", n)
	}
}

// handleDownloadZip streams a workspace dir/file selection as a zip:
// top-level entries are named by their base, and a selected directory
// recurses with the directory base as the zip-path prefix. Every path
// is resolved through the workspace-containment guard before any bytes
// are written.
func TestHandleDownloadZip_StreamsFilesAndDirs(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("bravo"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "c.txt"), []byte("charlie"), 0o644); err != nil {
		t.Fatal(err)
	}

	body := `{"paths":["` + prefix + `/a.txt","` + prefix + `/sub"]}`
	rec := postReq(t, h, "/api/files/download", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Errorf("Content-Type = %q, want application/zip", ct)
	}

	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
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
	if got["a.txt"] != "alpha" {
		t.Errorf("zip entry a.txt = %q, want %q", got["a.txt"], "alpha")
	}
	if want := filepath.Join("sub", "b.txt"); got[want] != "bravo" {
		t.Errorf("zip entry %q = %q, want %q (directory must recurse)", want, got[want], "bravo")
	}
	if want := filepath.Join("sub", "c.txt"); got[want] != "charlie" {
		t.Errorf("zip entry %q = %q, want %q (every child of a directory must be archived)",
			want, got[want], "charlie")
	}
}

// handleDownloadZip rejects a non-POST method and an empty path list
// before streaming anything.
func TestHandleDownloadZip_Rejects(t *testing.T) {
	h, _, _ := testDir(t)

	rec := getReq(t, h, "/api/files/download")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "POST" {
		t.Errorf("Allow = %q, want %q", got, "POST")
	}
	if rec := postReq(t, h, "/api/files/download", `{"paths":[]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("empty paths status = %d, want 400", rec.Code)
	}
}

// handleFile serves one resource under two methods, so its 405 must list
// both. Routes are registered as plain paths (no ServeMux method patterns),
// which is why an unsupported method reaches the handler at all. The path
// must resolve inside the granted mount: the resolve prelude runs before the
// method switch, so an unresolvable path yields 403 and never reaches 405.
func TestHandleFile_RejectionListsEveryPermittedMethod(t *testing.T) {
	h, _, prefix := testDir(t)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodDelete, "/api/file?path="+prefix+"/f.txt", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE status = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "GET, PUT" {
		t.Errorf("Allow = %q, want %q", got, "GET, PUT")
	}
}

// TestSensitivePaths_CredentialStoresRefused: the container HOME under /config/home holds the real
// credential stores, which must all be refused.
func TestSensitivePaths_CredentialStoresRefused(t *testing.T) {
	credPaths := []string{
		"config/home/.aws/sso/cache/kiro-auth-token.json",
		"config/home/.aws/sso/cache/botocore-client-id.json",
		"config/home/.ssh/id_ed25519",
		"config/home/.config/gh/hosts.yml",
		"config/home/.gitconfig",
		"config/mcp.json",
		"config/mcp-secrets.json",
	}
	h := testHandlerAt(t, "/config")
	for _, p := range credPaths {
		t.Run(p, func(t *testing.T) {
			abs := filepath.Clean("/" + p)
			if !(Sensitive{}).Blocks(abs) {
				t.Errorf("Blocks(%q) = false, want true (credential store must be protected)", abs)
			}
			if _, err := h.resolvePath(p); err == nil {
				t.Errorf("resolvePath(%q) = nil error, want rejection", p)
			}
			if rec := getReq(t, h, "/api/file?path="+p); rec.Code != http.StatusForbidden {
				t.Errorf("GET /api/file?path=%s: status = %d, want 403; body=%s",
					p, rec.Code, rec.Body.String())
			}
			if rec := getReq(t, h, "/api/file/download?path="+p); rec.Code != http.StatusForbidden {
				t.Errorf("GET /api/file/download?path=%s: status = %d, want 403; body=%s",
					p, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestWriteFile_StaleWriteGuard pins the guard that stops the editor silently discarding an agent's
// write to the file it has open.
func TestWriteFile_StaleWriteGuard(t *testing.T) {
	dir := t.TempDir()
	h, err := New(Sensitive{}, []string{dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	target := filepath.Join(dir, "note.md")
	if err := os.WriteFile(target, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := getReq(t, h, "/api/file?path="+target)
	var read struct {
		Content     string `json:"content"`
		ContentHash string `json:"content_hash"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &read); err != nil {
		t.Fatalf("decode read: %v", err)
	}
	if read.ContentHash == "" {
		t.Fatal("read returned no content_hash, so a client has nothing to send back")
	}

	t.Run("matching hash writes", func(t *testing.T) {
		body := `{"content":"mine\n","expected_hash":"` + read.ContentHash + `"}`
		rec := putReq(t, h, "/api/file?path="+target, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		got, _ := os.ReadFile(target)
		if string(got) != "mine\n" {
			t.Errorf("file = %q, want the written content", got)
		}
	})

	t.Run("stale hash is refused and returns the current content", func(t *testing.T) {
		body := `{"content":"clobber\n","expected_hash":"` + read.ContentHash + `"}`
		rec := putReq(t, h, "/api/file?path="+target, body)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409", rec.Code)
		}
		var out struct {
			Error       string `json:"error"`
			Content     string `json:"content"`
			ContentHash string `json:"content_hash"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode conflict: %v", err)
		}
		if out.Content != "mine\n" {
			t.Errorf("conflict content = %q, want the on-disk bytes", out.Content)
		}
		if out.ContentHash == "" || out.ContentHash == read.ContentHash {
			t.Error("conflict must carry the NEW hash so the next save can succeed")
		}
		if got, _ := os.ReadFile(target); string(got) != "mine\n" {
			t.Errorf("file = %q, the refused write must not have landed", got)
		}
	})

	t.Run("omitted hash still writes", func(t *testing.T) {
		rec := putReq(t, h, "/api/file?path="+target, `{"content":"unguarded\n"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
	})
}

// touch is a no-op on an entry that already exists: O_EXCL refuses the create
// with EEXIST, which the action reports as success without disturbing the bytes
// or the mtime. The doc comment on actionTouch rests on exactly this.
func TestAction_Touch_ExistingFileSucceedsAndKeepsContent(t *testing.T) {
	h, dir, prefix := testDir(t)
	const want = "keep me"
	existing := filepath.Join(dir, "have.txt")
	if err := os.WriteFile(existing, []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := postReq(t, h, "/api/files/action", `{"action":"touch","path":"`+prefix+`/have.txt"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("touch on an existing file: status = %d, want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}
	got, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("touch on an existing file: content = %q, want %q", got, want)
	}
}

// The synthetic root listing reports each mount's REAL mode and modification
// time, statted through the mount's own root handle. The entry is seeded with a
// bare directory placeholder, so a listing that skipped the stat would still
// look plausible while carrying a zero timestamp.
func TestListFiles_Root_MountEntriesCarryStattedMetadata(t *testing.T) {
	dirA := t.TempDir()
	h, err := New(Sensitive{}, []string{dirA})
	if err != nil {
		t.Fatal(err)
	}
	rec := getReq(t, h, "/api/files?path=.")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp struct {
		Files []fileEntry
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Files) != 1 {
		t.Fatalf("files = %+v, want exactly the 1 mount", resp.Files)
	}
	if got := resp.Files[0].ModTime; got == 0 {
		t.Errorf("files[0].ModTime = %d for mount %q, want the statted timestamp", got, dirA)
	}
	if got := resp.Files[0].Mode; got == os.ModeDir.String() {
		t.Errorf("files[0].Mode = %q for mount %q, want the statted mode rather than the bare directory placeholder",
			got, dirA)
	}
}

// captureFilebrowseLogs redirects the slog default into a buffer for one test; tests using it must
// not run in parallel.
func captureFilebrowseLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	swapDefaultLogger(t, slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return buf
}

// swapDefaultLogger installs h as the slog default for the duration of tb; see
// captureFilebrowseLogs for why the log package's writer is restored with it.
func swapDefaultLogger(tb testing.TB, h slog.Handler) {
	tb.Helper()
	prevLogger, prevWriter, prevFlags := slog.Default(), log.Writer(), log.Flags()
	tb.Cleanup(func() {
		slog.SetDefault(prevLogger)
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})
	slog.SetDefault(slog.New(h))
}

// A writability probe that works says nothing. Every directory listing runs one,
// so a probe that logged on the success path would put a line per listing into
// the operator's log — and its cleanup warning, the one that means a probe file
// leaked, would be buried among them.
func TestIsWritable_SucceedsWithoutLogging(t *testing.T) {
	h, dir, _ := testDir(t)
	buf := captureFilebrowseLogs(t)

	if got := isWritable(locAt(h, dir)); !got {
		t.Fatalf("isWritable(%q) = false, want true (a fresh temp dir is writable)", dir)
	}
	if got := buf.String(); got != "" {
		t.Errorf("isWritable(%q) logged %q, want nothing on the success path", dir, got)
	}
}

// A touch that creates a file is recorded: the log line is the operator's only
// trace that the file appeared, and it must survive the close.
func TestAction_Touch_CreationIsLogged(t *testing.T) {
	h, dir, _ := testDir(t)
	buf := captureFilebrowseLogs(t)

	l := locAt(h, filepath.Join(dir, "fresh.txt"))
	if err := actionTouch(t.Context(), httptest.NewRecorder(), fileAction{}, l, h); err != nil {
		t.Fatalf("actionTouch(%q) = %v, want nil", l.abs, err)
	}
	if got := buf.String(); !strings.Contains(got, "filebrowse: touch") {
		t.Errorf("actionTouch(%q) logged %q, want a line naming the touch", l.abs, got)
	}
}

// The zip stream stops ON its budgets, not one entry past them: the file that
// takes the running totals to either ceiling is the last one written. Driven at
// the counters because the real boundary is 500 MB and 10 000 files.
func TestZipStream_WriteFileStopsOnEachBudget(t *testing.T) {
	src := filepath.Join(t.TempDir(), "five.txt")
	if err := os.WriteFile(src, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name         string
		bytesBefore  int64
		filesBefore  int
		wantContinue bool
	}{
		{name: "both_budgets_untouched", wantContinue: true},
		{name: "one_byte_short_of_the_byte_budget", bytesBefore: maxZipBytes - 6, wantContinue: true},
		{name: "this_file_reaches_the_byte_budget", bytesBefore: maxZipBytes - 5, wantContinue: false},
		{name: "two_files_short_of_the_file_budget", filesBefore: maxZipFiles - 2, wantContinue: true},
		{name: "this_file_reaches_the_file_budget", filesBefore: maxZipFiles - 1, wantContinue: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, err := os.Open(src)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()

			zw := zip.NewWriter(&bytes.Buffer{})
			z := &zipStream{
				zw:         zw,
				flusher:    httptest.NewRecorder(),
				ctx:        t.Context(),
				totalBytes: tc.bytesBefore,
				fileCount:  tc.filesBefore,
			}
			got := z.writeFile(f, "five.txt")
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			if got != tc.wantContinue {
				t.Errorf("writeFile(five.txt) after %d bytes and %d files = %v, want %v",
					tc.bytesBefore, tc.filesBefore, got, tc.wantContinue)
			}
		})
	}
}

// nonFlushingWriter is an http.ResponseWriter that deliberately does NOT
// implement http.Flusher — the shape any middleware that wraps the writer
// without forwarding Flush produces.
type nonFlushingWriter struct {
	header http.Header
	body   bytes.Buffer
	code   int
}

func (w *nonFlushingWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *nonFlushingWriter) Write(p []byte) (int, error) { return w.body.Write(p) }
func (w *nonFlushingWriter) WriteHeader(code int)        { w.code = code }

// The zip download streams over a ResponseWriter that cannot flush. The
// handler's flusher is an optional type assertion precisely so a wrapped writer
// does not have to carry Flush, and every entry must still reach the client.
func TestHandleDownloadZip_WriterWithoutFlushSupport(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/files/download",
		strings.NewReader(`{"paths":["`+prefix+`/a.txt"]}`))
	req.Header.Set("Content-Type", "application/json")
	w := &nonFlushingWriter{}
	mux.ServeHTTP(w, req)

	zr, err := zip.NewReader(bytes.NewReader(w.body.Bytes()), int64(w.body.Len()))
	if err != nil {
		t.Fatalf("open zip written to a non-flushing writer: %v", err)
	}
	if len(zr.File) != 1 || zr.File[0].Name != "a.txt" {
		t.Fatalf("zip entries = %+v, want just a.txt", zr.File)
	}
}

func TestIsOutOfSpace_MatchesThroughAtomicfileWrapping(t *testing.T) {
	wrapped := func(errno syscall.Errno) error {
		return &atomicfile.WriteError{
			Phase: atomicfile.PhaseTempWrite,
			Err:   &os.PathError{Op: "write", Path: "/mount/.atomicfile-1.tmp", Err: errno},
		}
	}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"bare ENOSPC", syscall.ENOSPC, true},
		{"bare EDQUOT", syscall.EDQUOT, true},
		{"PathError over ENOSPC", &os.PathError{Op: "write", Path: "/x", Err: syscall.ENOSPC}, true},
		{"PathError over EDQUOT", &os.PathError{Op: "write", Path: "/x", Err: syscall.EDQUOT}, true},
		{"atomicfile WriteError over ENOSPC", wrapped(syscall.ENOSPC), true},
		{"atomicfile WriteError over EDQUOT", wrapped(syscall.EDQUOT), true},
		{"fmt-wrapped ENOSPC", fmt.Errorf("copy failed: %w", wrapped(syscall.ENOSPC)), true},
		{"another errno", &os.PathError{Op: "write", Path: "/x", Err: syscall.EACCES}, false},
		{"atomicfile size refusal", atomicfile.ErrFileTooLarge, false},
		{"plain error", errors.New("something else"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isOutOfSpace(tc.err); got != tc.want {
				t.Errorf("isOutOfSpace(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// writeFileError is the /api/file write path's whole mapping, so driving it
// directly is what pins the 507 without a filesystem that can run out of space.
func TestWriteFileError_Statuses(t *testing.T) {
	h, dir, _ := testDir(t)
	l := locAt(h, filepath.Join(dir, "f.txt"))
	enospc := &atomicfile.WriteError{
		Phase: atomicfile.PhaseTempWrite,
		Err:   &os.PathError{Op: "write", Path: l.abs, Err: syscall.ENOSPC},
	}
	edquot := &atomicfile.WriteError{
		Phase: atomicfile.PhaseTempWrite,
		Err:   &os.PathError{Op: "write", Path: l.abs, Err: syscall.EDQUOT},
	}
	cases := []struct {
		name     string
		err      error
		wantCode int
		wantBody string
	}{
		{"symlink target", atomicfile.ErrSymlinkTarget, http.StatusBadRequest, "not a regular file"},
		{"not regular", atomicfile.ErrNotRegular, http.StatusBadRequest, "not a regular file"},
		{"volume full", enospc, http.StatusInsufficientStorage, errNoSpaceLeft},
		{"quota exhausted", edquot, http.StatusInsufficientStorage, errNoSpaceLeft},
		{"anything else", errors.New("disk on fire"), http.StatusInternalServerError, "write failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeFileError(rec, l, tc.err)
			if rec.Code != tc.wantCode {
				t.Errorf("writeFileError(%v) status = %d, want %d", tc.err, rec.Code, tc.wantCode)
			}
			if !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Errorf("writeFileError(%v) body = %s, want it to contain %q",
					tc.err, rec.Body.String(), tc.wantBody)
			}
		})
	}
}
