package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

func patchSettings(ctx context.Context, t *testing.T, s *Server, body string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/settings", bytes.NewReader([]byte(body))).WithContext(ctx)
	rec := httptest.NewRecorder()
	s.handleSettingsWrite(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH /api/settings = %d, want 200: %s", rec.Code, rec.Body)
	}
}

// The reconcile carries the write to every open chat, so a tab closed mid-PATCH must not cancel
// it after the write has landed.
func TestSettingsWrite_ReconcilesOnADurableContext(t *testing.T) {
	eng := &fakeEngine{}
	s := &Server{agent: eng, push: &testPush{}, configDir: t.TempDir()}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	patchSettings(ctx, t, s, `{"`+settings.KeyMCPWaitForReady+`":true}`)

	if eng.reconciles != 1 {
		t.Fatalf("PATCH %s reconciled %d times, want 1", settings.KeyMCPWaitForReady, eng.reconciles)
	}
	if eng.reconciledOnCancellableCtx {
		t.Error("the reconcile ran on the request's own context: a tab close mid-PATCH cancels the pushes and leaves open chats on the previous value")
	}
}

// A refused additional directory answers 400 with its reason and leaves config.json as it was.
func TestSettingsWrite_AdditionalDirectories(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work, config, extra := filepath.Join(base, "workspace"), filepath.Join(base, "config"), filepath.Join(base, "data")
	for _, d := range []string{work, config, extra} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	patch := func(t *testing.T, s *Server, entries ...string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string][]string{settings.KeyAdditionalDirectories: entries})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPatch, "/api/settings", bytes.NewReader(body)).WithContext(t.Context())
		rec := httptest.NewRecorder()
		s.handleSettingsWrite(rec, req)
		return rec
	}

	t.Run("an accepted directory is stored", func(t *testing.T) {
		s := &Server{agent: &fakeEngine{}, push: &testPush{}, configDir: config, workDir: work}
		if rec := patch(t, s, extra); rec.Code != http.StatusOK {
			t.Fatalf("PATCH = %d, want 200: %s", rec.Code, rec.Body)
		}
		got, _, err := settings.FieldStrict[[]string](t.Context(), config, settings.KeyAdditionalDirectories)
		if err != nil || len(got) != 1 || got[0] != extra {
			t.Errorf("stored %v (%v), want [%s]", got, err, extra)
		}
	})

	for _, refused := range []struct{ name, entry, reason string }{
		{"the config directory", config, "config directory"},
		{"the workspace", work, "the workspace"},
		{"the filesystem root", "/", "filesystem root"},
	} {
		t.Run(refused.name+" is refused with its reason", func(t *testing.T) {
			s := &Server{agent: &fakeEngine{}, push: &testPush{}, configDir: config, workDir: work}
			rec := patch(t, s, extra, refused.entry)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), refused.reason) {
				t.Errorf("PATCH %q = %d %s, want 400 naming %q", refused.entry, rec.Code, rec.Body, refused.reason)
			}
			got, _, err := settings.FieldStrict[[]string](t.Context(), config, settings.KeyAdditionalDirectories)
			if err != nil || len(got) != 1 || got[0] != extra {
				t.Errorf("stored %v (%v) after a refused PATCH, want it unchanged at [%s]", got, err, extra)
			}
		})
	}
}

// The stored list is the user's intent: an entry refused today stays stored, is shown refused,
// and never blocks a write that keeps it, so a removal always goes through.
func TestSettingsWrite_AStoredRefusedEntryBlocksNoWrite(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work, config := filepath.Join(base, "workspace"), filepath.Join(base, "config")
	granted, second, gone := filepath.Join(base, "data"), filepath.Join(base, "second"), filepath.Join(base, "gone")
	for _, d := range []string{work, config, granted, second} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	dirs := settings.AgentDirs{Workspace: work, Config: config}
	store := func(t *testing.T, entries ...string) {
		t.Helper()
		data, err := json.Marshal(map[string][]string{settings.KeyAdditionalDirectories: entries})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(config, settings.Filename), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	patch := func(t *testing.T, entries ...string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string][]string{settings.KeyAdditionalDirectories: entries})
		if err != nil {
			t.Fatal(err)
		}
		s := &Server{agent: &fakeEngine{}, push: &testPush{}, configDir: config, workDir: work}
		req := httptest.NewRequest(http.MethodPatch, "/api/settings", bytes.NewReader(body)).WithContext(t.Context())
		rec := httptest.NewRecorder()
		s.handleSettingsWrite(rec, req)
		return rec
	}
	grant := func(t *testing.T) ([]string, map[string]string) {
		t.Helper()
		g, refused, err := dirs.Granted(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return g, refused
	}

	t.Run("removing a granted entry succeeds and revokes it", func(t *testing.T) {
		store(t, granted, gone, config)
		if rec := patch(t, gone, config); rec.Code != http.StatusOK {
			t.Fatalf("PATCH = %d, want 200: %s", rec.Code, rec.Body)
		}
		g, refused := grant(t)
		if len(g) != 0 {
			t.Errorf("granted = %v after the removal, want none", g)
		}
		if !strings.Contains(refused[config], "config directory") || !strings.Contains(refused[gone], "no such directory") {
			t.Errorf("refused = %v, want the hand-edited config directory and the missing one still refused", refused)
		}
	})
	t.Run("adding a valid entry succeeds and the refused one stays refused", func(t *testing.T) {
		store(t, granted, gone, config)
		if rec := patch(t, granted, gone, config, second); rec.Code != http.StatusOK {
			t.Fatalf("PATCH = %d, want 200: %s", rec.Code, rec.Body)
		}
		g, refused := grant(t)
		if !slices.Equal(g, []string{granted, second}) {
			t.Errorf("granted = %v, want [%s %s]", g, granted, second)
		}
		if _, ok := refused[config]; !ok {
			t.Errorf("refused = %v, want the hand-edited config directory still refused", refused)
		}
	})
	t.Run("an added invalid entry is still refused with its reason", func(t *testing.T) {
		store(t, granted, gone)
		missing := filepath.Join(base, "missing")
		rec := patch(t, granted, gone, missing)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "no such directory") {
			t.Errorf("PATCH = %d %s, want 400 naming the missing directory", rec.Code, rec.Body)
		}
		if g, _ := grant(t); !slices.Equal(g, []string{granted}) {
			t.Errorf("granted = %v after a refused PATCH, want it unchanged at [%s]", g, granted)
		}
	})
	t.Run("removing the last granted entry leaves the refused one stored and grants nothing", func(t *testing.T) {
		store(t, granted, gone)
		if rec := patch(t, gone); rec.Code != http.StatusOK {
			t.Fatalf("PATCH = %d, want 200: %s", rec.Code, rec.Body)
		}
		got, _, err := settings.FieldStrict[[]string](t.Context(), config, settings.KeyAdditionalDirectories)
		if err != nil || !slices.Equal(got, []string{gone}) {
			t.Errorf("stored %v (%v), want [%s]", got, err, gone)
		}
		if g, _ := grant(t); len(g) != 0 {
			t.Errorf("granted = %v, want none", g)
		}
	})
	t.Run("the answer is the settings the write committed, with each refusal's reason", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(config, settings.Filename),
			[]byte(`{"theme":"dark","`+settings.KeyAdditionalDirectories+`":["`+granted+`","`+gone+`"]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		rec := patch(t, gone, second)
		if rec.Code != http.StatusOK {
			t.Fatalf("PATCH = %d, want 200: %s", rec.Code, rec.Body)
		}
		var got marotte.EffectiveSettings
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode answer %s: %v", rec.Body, err)
		}
		if !slices.Equal(got.AdditionalDirectories, []string{gone, second}) || got.Theme != "dark" {
			t.Errorf("answer additional_directories %v, theme %q; want [%s %s] and the stored dark", got.AdditionalDirectories, got.Theme, gone, second)
		}
		if len(got.AdditionalDirectoriesRefused) != 1 || !strings.Contains(got.AdditionalDirectoriesRefused[gone], "no such directory") {
			t.Errorf("answer additional_directories_refused = %v, want only %s, as missing", got.AdditionalDirectoriesRefused, gone)
		}
	})
	t.Run("an added entry overlapping a granted one is refused naming it", func(t *testing.T) {
		store(t, granted)
		inner := filepath.Join(granted, "project")
		if err := os.Mkdir(inner, 0o700); err != nil {
			t.Fatal(err)
		}
		rec := patch(t, granted, inner)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "already granted") {
			t.Errorf("PATCH = %d %s, want 400 naming the granted entry it lies inside", rec.Code, rec.Body)
		}
	})
}
