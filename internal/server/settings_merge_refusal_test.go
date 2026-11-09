package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/settings"
)

// storedCap is the settings package's config.json read cap (its maxBytes).
const storedCap = 1 << 20

// TestSettingsWrite_RefusesWhenTheStoredSettingsCannotBeRead pins that a write which cannot
// read the stored document writes nothing. The byte comparison catches a write that left
// only the request's key on disk.
func TestSettingsWrite_RefusesWhenTheStoredSettingsCannotBeRead(t *testing.T) {
	// Real keys a user can set.
	const stored = `{"chat_retention_days":-1,"security_profile":"trusted","theme":"dark"}`

	tests := []struct {
		desc   string
		method string
		// seed installs whatever stands at config.json and returns the bytes a
		// later read must still find, or "" when the subject is not a file whose
		// contents can be compared.
		seed func(t *testing.T, path string) string
	}{
		{
			desc:   "PATCH over an unparseable document",
			method: http.MethodPatch,
			seed: func(t *testing.T, path string) string {
				t.Helper()
				// A trailing comma: the hand-edit invariant 6 expects on this volume.
				const broken = `{"chat_retention_days":-1,"theme":"dark",}`
				if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
					t.Fatalf("seed %s: %v", path, err)
				}
				return broken
			},
		},
		{
			desc:   "PATCH over a truncated document",
			method: http.MethodPatch,
			seed: func(t *testing.T, path string) string {
				t.Helper()
				const broken = `{`
				if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
					t.Fatalf("seed %s: %v", path, err)
				}
				return broken
			},
		},
		{
			desc:   "PATCH over a file past the size cap",
			method: http.MethodPatch,
			seed: func(t *testing.T, path string) string {
				t.Helper()
				// Valid JSON padded past the cap, so only the cap refuses it.
				doc := append([]byte(stored), bytes.Repeat([]byte(" "), storedCap+1-len(stored))...)
				if err := os.WriteFile(path, doc, 0o600); err != nil {
					t.Fatalf("seed %s: %v", path, err)
				}
				return string(doc)
			},
		},
		{
			desc:   "PATCH over a directory at the name",
			method: http.MethodPatch,
			seed: func(t *testing.T, path string) string {
				t.Helper()
				// os.Open succeeds on a directory and the read then fails.
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatalf("seed dir %s: %v", path, err)
				}
				return ""
			},
		},
		{
			desc:   "PATCH over a symlink at the name",
			method: http.MethodPatch,
			seed: func(t *testing.T, path string) string {
				t.Helper()
				other := filepath.Join(filepath.Dir(path), "elsewhere.json")
				if err := os.WriteFile(other, []byte(`{"theme":"light"}`), 0o600); err != nil {
					t.Fatalf("seed link target: %v", err)
				}
				if err := os.Symlink(other, path); err != nil {
					t.Fatalf("seed symlink %s: %v", path, err)
				}
				return ""
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, settings.Filename)
			before := tc.seed(t, path)
			s := &Server{agent: &fakeEngine{}, push: &testPush{}, configDir: dir}

			req := httptest.NewRequest(tc.method, "/api/settings", bytes.NewReader([]byte(`{"last_merge_method":"squash"}`)))
			rec := httptest.NewRecorder()
			s.handleSettingsWrite(rec, req)

			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("%s /api/settings = %d, want %d", tc.method, rec.Code, http.StatusInternalServerError)
			}
			// The body must say the file was not overwritten.
			if body := rec.Body.String(); !bytes.Contains([]byte(body), []byte("not overwritten")) {
				t.Errorf("refusal body = %s, want it to state the settings were not overwritten", body)
			}
			if before == "" {
				return // a directory or a symlink has no contents to compare
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read back %s: %v", path, err)
			}
			if string(after) != before {
				t.Errorf("config.json after a refused %s =\n%s\nwant it untouched:\n%s", tc.method, after, before)
			}
		})
	}
}

// TestSettingsWrite_StillMergesAReadableDocument is the control: unnamed keys survive.
func TestSettingsWrite_StillMergesAReadableDocument(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, settings.Filename)
	if err := os.WriteFile(path, []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	s := &Server{agent: &fakeEngine{}, push: &testPush{}, configDir: dir}

	req := httptest.NewRequest(http.MethodPatch, "/api/settings", bytes.NewReader([]byte(`{"last_model":"opus"}`)))
	rec := httptest.NewRecorder()
	s.handleSettingsWrite(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH /api/settings = %d, want %d; body %s", rec.Code, http.StatusOK, rec.Body)
	}
	got, err := settings.Stored(dir)
	if err != nil {
		t.Fatalf("read back %s: %v", path, err)
	}
	if string(got[settings.KeyLastModel]) != `"opus"` {
		t.Errorf("%s = %s, want \"opus\"", settings.KeyLastModel, got[settings.KeyLastModel])
	}
	if string(got[settings.KeyTheme]) != `"dark"` {
		t.Errorf("%s = %s, want \"dark\" (the merge dropped a key the request did not name)",
			settings.KeyTheme, got[settings.KeyTheme])
	}
}
