package filebrowse

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func toolOutputFixture(t *testing.T) (h *Handler, sessions, path string) {
	t.Helper()
	h, _, _ = testDir(t)
	sessions = t.TempDir()
	h.AllowToolOutputs(sessions)
	dir := filepath.Join(sessions, "abcd1234", "sess_0001", "tool-outputs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(dir, "execute_bash-0a1b2c3d.txt")
	if err := os.WriteFile(path, []byte("full output"), 0o600); err != nil {
		t.Fatal(err)
	}
	return h, sessions, path
}

func fileReq(h *Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"content":"x"}`)
	h.handleFile(rec, httptest.NewRequest(method, "/api/file?path="+url.QueryEscape(path), body))
	return rec
}

func TestToolOutput_ReadableAndMarkedReadOnly(t *testing.T) {
	h, _, path := toolOutputFixture(t)
	rec := fileReq(h, http.MethodGet, path)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200 (%s)", path, rec.Code, rec.Body.String())
	}
	var body struct {
		Content  string `json:"content"`
		ReadOnly bool   `json:"read_only"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Content != "full output" || !body.ReadOnly {
		t.Errorf("body = %+v, want the file's content marked read_only", body)
	}
}

func TestToolOutput_NeverWritable(t *testing.T) {
	h, _, path := toolOutputFixture(t)
	if rec := fileReq(h, http.MethodPut, path); rec.Code != http.StatusForbidden {
		t.Errorf("PUT %s = %d, want 403", path, rec.Code)
	}
	if got, _ := os.ReadFile(path); string(got) != "full output" {
		t.Errorf("file after PUT = %q, want it unchanged", got)
	}
}

func TestToolOutput_OnlyTheToolOutputShapeIsGranted(t *testing.T) {
	h, sessions, path := toolOutputFixture(t)
	sessDir := filepath.Dir(filepath.Dir(path))
	if err := os.WriteFile(filepath.Join(sessDir, "messages.jsonl"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(sessDir, "messages.jsonl"),
		filepath.Join(sessDir, "tool-outputs", "notes.txt"),
		filepath.Join(sessions, "abcd1234", "other", "tool-outputs", "execute_bash-0a1b2c3d.txt"),
		filepath.Join(sessDir, "tool-outputs", "..", "..", "sess_0001", "messages.jsonl"),
	} {
		if out, granted, _ := h.openToolOutput(p); granted {
			if out.f != nil {
				_ = out.f.Close()
			}
			t.Errorf("openToolOutput(%q) granted, want refused", p)
		}
	}
}

func downloadReq(h *Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.handleDownload(rec, httptest.NewRequest(http.MethodGet, "/api/file/download?path="+url.QueryEscape(path), nil))
	return rec
}

func TestToolOutput_DownloadServesTheFile(t *testing.T) {
	h, _, path := toolOutputFixture(t)
	rec := downloadReq(h, path)
	if rec.Code != http.StatusOK || rec.Body.String() != "full output" {
		t.Errorf("download %s = %d %q, want 200 with the file", path, rec.Code, rec.Body.String())
	}
}

// A symlink at any component is refused by both endpoints, including one inside
// the sessions tree that an os.Root would follow.
func TestToolOutput_SymlinkIsRefused(t *testing.T) {
	h, sessions, path := toolOutputFixture(t)
	leaf := filepath.Join(filepath.Dir(path), "execute_bash-ffffffff.txt")
	if err := os.Symlink(path, leaf); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sess_0001", filepath.Join(sessions, "abcd1234", "sess_0002")); err != nil {
		t.Fatal(err)
	}
	viaDir := filepath.Join(sessions, "abcd1234", "sess_0002", "tool-outputs", filepath.Base(path))
	for _, p := range []string{leaf, viaDir} {
		if rec := fileReq(h, http.MethodGet, p); rec.Code != http.StatusForbidden {
			t.Errorf("GET %s = %d %q, want 403", p, rec.Code, rec.Body.String())
		}
		if rec := downloadReq(h, p); rec.Code != http.StatusForbidden {
			t.Errorf("download %s = %d %q, want 403", p, rec.Code, rec.Body.String())
		}
	}
}

// The bytes come from the descriptor opened at the check, so a symlink swapped
// in at the name afterwards serves nothing new.
// A symlink swapped in after the open must not change what is served.
func TestToolOutput_ServesTheFileItOpened(t *testing.T) {
	serve := map[string]func(*testing.T, *httptest.ResponseRecorder, toolOutput, string){
		"read": func(t *testing.T, rec *httptest.ResponseRecorder, out toolOutput, _ string) {
			data, _, err := readStable(t.Context(), out.f)
			if err != nil {
				t.Fatalf("readStable: %v", err)
			}
			_, _ = rec.Write(data)
		},
		"download": func(_ *testing.T, rec *httptest.ResponseRecorder, out toolOutput, path string) {
			serveDownload(rec, httptest.NewRequest(http.MethodGet, "/api/file/download", nil), out.f, out.info, path)
		},
	}
	for name, serveFn := range serve {
		t.Run(name, func(t *testing.T) {
			h, sessions, path := toolOutputFixture(t)
			secret := filepath.Join(sessions, "abcd1234", "sess_0001", "messages.jsonl")
			if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
				t.Fatal(err)
			}
			out, granted, err := h.openToolOutput(path)
			if !granted || err != nil {
				t.Fatalf("openToolOutput(%q) = granted %v, err %v; want an open file", path, granted, err)
			}
			defer out.f.Close()
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(secret, path); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			serveFn(t, rec, out, path)
			if strings.Contains(rec.Body.String(), "secret") || !strings.Contains(rec.Body.String(), "full output") {
				t.Errorf("%s after the swap = %q, want the file opened at the check", name, rec.Body.String())
			}
		})
	}
}

func TestToolOutput_MissingFileIsNotFound(t *testing.T) {
	h, _, path := toolOutputFixture(t)
	gone := filepath.Join(filepath.Dir(path), "execute_bash-00000000.txt")
	if rec := fileReq(h, http.MethodGet, gone); rec.Code != http.StatusNotFound {
		t.Errorf("GET %s = %d, want 404", gone, rec.Code)
	}
	if rec := downloadReq(h, gone); rec.Code != http.StatusNotFound {
		t.Errorf("download %s = %d, want 404", gone, rec.Code)
	}
}

func TestToolOutput_NoGrantWithoutAllow(t *testing.T) {
	h, _, _ := testDir(t)
	if _, granted, _ := h.openToolOutput("/x/abcd/sess_1/tool-outputs/execute_bash-0a1b2c3d.txt"); granted {
		t.Error("openToolOutput granted a path with no AllowToolOutputs, want refused")
	}
}
