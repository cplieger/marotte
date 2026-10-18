package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

func patchSettingsRaw(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/settings", bytes.NewReader([]byte(body))).WithContext(t.Context())
	rec := httptest.NewRecorder()
	s.handleSettingsWrite(rec, req)
	return rec
}

// GET, the PATCH answer and the settings_updated event each state the revision of the document
// they describe, so a client can tell which of two payloads is newer.
func TestSettingsRevision_IsOnEveryPayloadAClientAdopts(t *testing.T) {
	eng := &fakeEngine{}
	dir := t.TempDir()
	s := &Server{agent: eng, push: &testPush{}, configDir: dir}

	if got := getEffective(t, dir).SettingsRevision; got != 0 {
		t.Errorf("GET before any write: settings_revision = %d, want 0", got)
	}
	for want := uint64(1); want <= 2; want++ {
		rec := patchSettingsRaw(t, s, `{"theme":"dark"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("PATCH = %d, want 200: %s", rec.Code, rec.Body)
		}
		var answer marotte.EffectiveSettings
		if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
			t.Fatalf("decode the PATCH answer: %v", err)
		}
		if answer.SettingsRevision != want {
			t.Errorf("PATCH %d answer: settings_revision = %d, want %d", want, answer.SettingsRevision, want)
		}
		if got := getEffective(t, dir).SettingsRevision; got != want {
			t.Errorf("GET after PATCH %d: settings_revision = %d, want %d", want, got, want)
		}
		last := eng.events[len(eng.events)-1]
		if p, ok := last.Payload.(marotte.SettingsUpdatedPayload); last.Type != marotte.EventSettingsUpdated || !ok || p.SettingsRevision != want {
			t.Errorf("event after PATCH %d = %s %#v, want settings_updated at revision %d", want, last.Type, last.Payload, want)
		}
	}
}

func TestSettingsWrite_RefusesAClientRevision(t *testing.T) {
	eng := &fakeEngine{}
	dir := t.TempDir()
	s := &Server{agent: eng, push: &testPush{}, configDir: dir}
	if rec := patchSettingsRaw(t, s, `{"theme":"dark"}`); rec.Code != http.StatusOK {
		t.Fatalf("PATCH = %d, want 200: %s", rec.Code, rec.Body)
	}

	rec := patchSettingsRaw(t, s, `{"theme":"light","settings_revision":99}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), settings.KeySettingsRevision) {
		t.Errorf("PATCH carrying settings_revision = %d %s, want 400 naming the key", rec.Code, rec.Body)
	}
	got := getEffective(t, dir)
	if got.SettingsRevision != 1 || got.Theme != "dark" {
		t.Errorf("after the refused PATCH: revision %d theme %q, want 1 and \"dark\" (nothing written)", got.SettingsRevision, got.Theme)
	}
	if len(eng.events) != 1 {
		t.Errorf("%d events after one accepted and one refused PATCH, want 1", len(eng.events))
	}
}
