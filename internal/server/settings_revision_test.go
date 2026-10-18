package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/policyfile"
	"github.com/cplieger/marotte/internal/settings"
)

func patchSettingsRaw(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/settings", bytes.NewReader([]byte(body))).WithContext(t.Context())
	rec := httptest.NewRecorder()
	s.handleSettingsWrite(rec, req)
	return rec
}

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
		got := getEffective(t, dir)
		if got.SettingsRevision != want || got.SettingsEpoch == 0 || got.SettingsEpoch != answer.SettingsEpoch {
			t.Errorf("GET after PATCH %d: epoch %d revision %d, want the answer's epoch %d and revision %d",
				want, got.SettingsEpoch, got.SettingsRevision, answer.SettingsEpoch, want)
		}
		if len(eng.events) != int(want) {
			t.Fatalf("%d events after %d PATCHes, want one each", len(eng.events), want)
		}
		last := eng.events[len(eng.events)-1]
		wantEvent := marotte.SettingsUpdatedPayload{SettingsEpoch: answer.SettingsEpoch, SettingsRevision: want}
		if p, ok := last.Payload.(marotte.SettingsUpdatedPayload); last.Type != marotte.EventSettingsUpdated || !ok || p != wantEvent {
			t.Errorf("event after PATCH %d = %s %#v, want settings_updated %#v", want, last.Type, last.Payload, wantEvent)
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

	before := getEffective(t, dir)
	for _, body := range []string{`{"theme":"light","settings_revision":99}`, `{"theme":"light","settings_epoch":1}`} {
		key := settings.KeySettingsRevision
		if strings.Contains(body, settings.KeySettingsEpoch) {
			key = settings.KeySettingsEpoch
		}
		rec := patchSettingsRaw(t, s, body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), key) {
			t.Errorf("PATCH carrying %s = %d %s, want 400 naming the key", key, rec.Code, rec.Body)
		}
	}
	got := getEffective(t, dir)
	if got.SettingsRevision != 1 || got.SettingsEpoch != before.SettingsEpoch || got.Theme != "dark" {
		t.Errorf("after the refused PATCHes: epoch %d revision %d theme %q, want %d, 1 and \"dark\" (nothing written)",
			got.SettingsEpoch, got.SettingsRevision, got.Theme, before.SettingsEpoch)
	}
	if len(eng.events) != 1 {
		t.Errorf("%d events after one accepted and two refused PATCHes, want 1", len(eng.events))
	}
}

func TestPolicyProfile_SelectionAnnouncesTheWrittenVersion(t *testing.T) {
	s, _, _, _ := profileFixture(t, nil)
	eng, ok := s.agent.(*fakeEngine)
	if !ok {
		t.Fatalf("agent = %T, want *fakeEngine", s.agent)
	}
	if rec := postProfile(t, s, profileBody{Profile: policyfile.ProfileReadOnly}); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	got := getEffective(t, s.configDir)
	want := marotte.SettingsUpdatedPayload{SettingsEpoch: got.SettingsEpoch, SettingsRevision: got.SettingsRevision}
	var announced []marotte.SettingsUpdatedPayload
	for _, evt := range eng.events {
		if p, ok := evt.Payload.(marotte.SettingsUpdatedPayload); ok && evt.Type == marotte.EventSettingsUpdated {
			announced = append(announced, p)
		}
	}
	if len(announced) != 1 || announced[0] != want || want.SettingsRevision != 1 {
		t.Errorf("settings events = %v, want exactly one at the written version %v (revision 1)", announced, want)
	}
}
