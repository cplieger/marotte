package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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

// A page may adopt a write's answer as the settings at its version, so the answer is the stored
// document resolved as a read resolves it, never the request echoed back.
func TestSettingsWrite_AnswersTheStoredSettingsAtItsVersion(t *testing.T) {
	dir := seedConfig(t, `{"debug_logs":true,"auto_compact_pct":"most"}`)
	s := &Server{agent: &fakeEngine{}, push: &testPush{}, configDir: dir}

	rec := patchSettingsRaw(t, s, `{"theme":"dark"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH = %d, want 200: %s", rec.Code, rec.Body)
	}
	var answer marotte.EffectiveSettings
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode the PATCH answer: %v", err)
	}
	if answer.Theme != "dark" || !answer.DebugLogs || answer.AutoCompactPct != settings.EffectiveDefaults().AutoCompactPct {
		t.Errorf("answer theme %q debug_logs %t auto_compact_pct %d, want the write's theme, the stored debug_logs and the default for the stored value that does not fit",
			answer.Theme, answer.DebugLogs, answer.AutoCompactPct)
	}
	if got := getEffective(t, dir); !reflect.DeepEqual(got, answer) {
		t.Errorf("GET at the answer's version = %+v\nwant the PATCH answer %+v", got, answer)
	}
}

// announceHook runs onEvent as each event leaves, before the fake records it.
type announceHook struct {
	*fakeEngine
	onEvent func()
}

func (h announceHook) Broadcast(ctx context.Context, evt marotte.ServerEvent) {
	h.onEvent()
	h.fakeEngine.Broadcast(ctx, evt)
}

// A page adopts a read its settings event prompts over the write's answer at the same version, so
// the answer must be resolved before the event leaves, never after.
func TestSettingsWrite_AnswersSettingsResolvedBeforeTheEvent(t *testing.T) {
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
	eng := &fakeEngine{}
	// The directory goes once the event is out, as it could before the read the event prompts.
	hook := announceHook{fakeEngine: eng, onEvent: func() {
		if err := os.Remove(extra); err != nil {
			t.Errorf("remove %s: %v", extra, err)
		}
	}}
	s := &Server{agent: hook, push: &testPush{}, configDir: config, workDir: work}
	body, err := json.Marshal(map[string][]string{settings.KeyAdditionalDirectories: {extra}})
	if err != nil {
		t.Fatal(err)
	}

	rec := patchSettingsRaw(t, s, string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH = %d, want 200: %s", rec.Code, rec.Body)
	}
	var answer marotte.EffectiveSettings
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode the PATCH answer: %v", err)
	}
	if len(eng.events) != 1 {
		t.Fatalf("%d events, want the write's one", len(eng.events))
	}
	if !slices.Equal(answer.AdditionalDirectories, []string{extra}) || len(answer.AdditionalDirectoriesRefused) != 0 {
		t.Errorf("answer additional_directories %v, refused %v; want [%s] granted, as it stood when the event left",
			answer.AdditionalDirectories, answer.AdditionalDirectoriesRefused, extra)
	}
	read := httptest.NewRecorder()
	handleSettingsGet(read, settings.AgentDirs{Workspace: work, Config: config}, nil)
	var got marotte.EffectiveSettings
	if err := json.Unmarshal(read.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode the GET answer %s: %v", read.Body, err)
	}
	if !strings.Contains(got.AdditionalDirectoriesRefused[extra], "no such directory") || got.SettingsRevision != answer.SettingsRevision {
		t.Errorf("GET after the event: revision %d refused %v, want revision %d with %s missing",
			got.SettingsRevision, got.AdditionalDirectoriesRefused, answer.SettingsRevision, extra)
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
