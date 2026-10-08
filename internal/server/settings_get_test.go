package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

// getEffective issues GET /api/settings and decodes into the wire STRUCT: a missing field
// would read as its zero value (0 retention days, "delete on close"), which the cases catch.
func getEffective(t *testing.T, dir string) marotte.EffectiveSettings {
	t.Helper()
	rec := httptest.NewRecorder()
	handleSettingsGet(rec, filepath.Join(dir, settings.Filename))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/settings = %d, want 200 (the read fails OPEN)", rec.Code)
	}
	var got marotte.EffectiveSettings
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response %s: %v", rec.Body.String(), err)
	}
	return got
}

// seedConfig writes raw bytes to config.json in a fresh dir.
func seedConfig(t *testing.T, raw string) string {
	t.Helper()
	dir := t.TempDir()
	if raw != "" {
		if err := os.WriteFile(filepath.Join(dir, settings.Filename), []byte(raw), 0o600); err != nil {
			t.Fatalf("seed config.json: %v", err)
		}
	}
	return dir
}

// TestSettingsGet_AbsentFileServesEveryDefault pins the fresh-volume answer. It is
// the case the handler always got right, and it is here so the cases below are
// read as changes to the OTHER branch rather than to this one.
func TestSettingsGet_AbsentFileServesEveryDefault(t *testing.T) {
	got := getEffective(t, t.TempDir())
	if want := settings.EffectiveDefaults(); !effectiveEqual(got, want) {
		t.Errorf("GET with no config.json = %+v, want the defaults %+v", got, want)
	}
}

// TestSettingsGet_ResolvesDefaultsUnderStoredValues pins that a file naming two keys still
// answers every other key at its default.
func TestSettingsGet_ResolvesDefaultsUnderStoredValues(t *testing.T) {
	dir := seedConfig(t, `{"chat_retention_days":-1,"theme":"dark"}`)
	got := getEffective(t, dir)

	// Stored values win.
	if got.ChatRetentionDays != -1 {
		t.Errorf("chat_retention_days = %d, want -1 (the stored value)", got.ChatRetentionDays)
	}
	if got.Theme != "dark" {
		t.Errorf("theme = %q, want dark (the stored value)", got.Theme)
	}
	// The keys whose default is NOT the zero value.
	if !got.KnowledgeEnabled {
		t.Error("knowledge_enabled = false, want true: absent must not read as the zero value")
	}
	if !got.NotifyAgentFinished || !got.NotifyRunOutcome {
		t.Errorf("notify kinds = (%v, %v), want (true, true): agent_finished and run_outcome report work this server did while nobody was looking, so both default ON",
			got.NotifyAgentFinished, got.NotifyRunOutcome)
	}
	if got.NotifyPRStatus {
		t.Error("notify_pr_status = true, want false: it is the one keyed kind that defaults OFF, because a pull request's CI verdict is already on the forge — the polarity is not uniform across the three")
	}
	// Never null: the field has no omitempty and the client's required string[] cannot decode null.
	if got.AgentIgnoreFiles == nil {
		t.Error("agent_ignore_files is nil; it marshals as null and the client cannot decode it")
	}
	if len(got.AgentIgnoreFiles) != 0 {
		t.Errorf("agent_ignore_files = %v, want it empty: a seeded entry filters reads nobody asked to filter", got.AgentIgnoreFiles)
	}
	// The master switch really is off, so the above is about polarity.
	if got.NotificationsEnabled {
		t.Error("notifications_enabled = true, want false")
	}
}

// TestSettingsGet_AWrongTypedStoredValueYieldsTheDefault pins that a well-typed-JSON value of
// the wrong Go type yields the default (a hand-edited config.json).
func TestSettingsGet_AWrongTypedStoredValueYieldsTheDefault(t *testing.T) {
	tests := []struct {
		desc  string
		raw   string
		check func(*testing.T, marotte.EffectiveSettings)
	}{
		{
			desc: "a string where a number is declared",
			raw:  `{"chat_retention_days":"seven"}`,
			check: func(t *testing.T, got marotte.EffectiveSettings) {
				if got.ChatRetentionDays != settings.DefaultChatRetentionDays {
					t.Errorf("chat_retention_days = %d, want the default %d", got.ChatRetentionDays, settings.DefaultChatRetentionDays)
				}
			},
		},
		{
			desc: "a number where a bool is declared",
			raw:  `{"knowledge_enabled":0}`,
			check: func(t *testing.T, got marotte.EffectiveSettings) {
				if !got.KnowledgeEnabled {
					t.Error("knowledge_enabled = false; a 0 must not be read as false, it must be refused for the default true")
				}
			},
		},
		{
			desc: "a null, which encoding/json would otherwise accept as a no-op",
			raw:  `{"chat_retention_days":null}`,
			check: func(t *testing.T, got marotte.EffectiveSettings) {
				// null into an int succeeds and leaves 0 ("delete chats on close").
				if got.ChatRetentionDays != settings.DefaultChatRetentionDays {
					t.Errorf("chat_retention_days = %d, want the default %d; a stored null must not resolve to the zero value",
						got.ChatRetentionDays, settings.DefaultChatRetentionDays)
				}
			},
		},
		{
			desc: "a string where a list is declared",
			raw:  `{"agent_ignore_files":".gitignore"}`,
			check: func(t *testing.T, got marotte.EffectiveSettings) {
				if !slices.Equal(got.AgentIgnoreFiles, settings.DefaultAgentIgnoreFiles()) {
					t.Errorf("agent_ignore_files = %v, want the default %v", got.AgentIgnoreFiles, settings.DefaultAgentIgnoreFiles())
				}
				// The default is EMPTY, so check the nil axis or the case passes without the discipline.
				if got.AgentIgnoreFiles == nil {
					t.Error("agent_ignore_files is nil; the default must stand, and nil marshals as null")
				}
			},
		},
		{
			// An in-place decode of ["zzz",7] leaves [zzz .kiroignore], a MIXTURE; the fixture values
			// differ from the defaults so that bug cannot pass by coincidence.
			desc: "a partially-decodable list does not mix stored and default elements",
			raw:  `{"agent_ignore_files":["zzz",7]}`,
			check: func(t *testing.T, got marotte.EffectiveSettings) {
				if !slices.Equal(got.AgentIgnoreFiles, settings.DefaultAgentIgnoreFiles()) {
					t.Errorf("agent_ignore_files = %v, want the whole default %v with nothing of the stored list in it",
						got.AgentIgnoreFiles, settings.DefaultAgentIgnoreFiles())
				}
				if got.AgentIgnoreFiles == nil {
					t.Error("agent_ignore_files is nil; the default must stand, and nil marshals as null")
				}
			},
		},
		{
			// A null decoded in place over a slice sets it to NIL with no error.
			desc: "a null over a list does not wipe it",
			raw:  `{"agent_ignore_files":null}`,
			check: func(t *testing.T, got marotte.EffectiveSettings) {
				if !slices.Equal(got.AgentIgnoreFiles, settings.DefaultAgentIgnoreFiles()) {
					t.Errorf("agent_ignore_files = %v, want the default %v; a stored null must not empty the list",
						got.AgentIgnoreFiles, settings.DefaultAgentIgnoreFiles())
				}
				// Nil is the only axis left: a wiped list and the empty default compare equal.
				if got.AgentIgnoreFiles == nil {
					t.Error("agent_ignore_files is nil; a stored null must be refused, not assigned")
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			tc.check(t, getEffective(t, seedConfig(t, tc.raw)))
		})
	}
}

// TestSettingsGet_AWrongTypedValueIsReportedByKeyNotByValue pins that the log names the key,
// never the value.
func TestSettingsGet_AWrongTypedValueIsReportedByKeyNotByValue(t *testing.T) {
	const secret = "ghp_notarealtokenbutshapedlikeone"
	logs := captureLogs(t)
	getEffective(t, seedConfig(t, `{"chat_retention_days":"`+secret+`"}`))

	line := logs.String()
	if !strings.Contains(line, settings.KeyChatRetentionDays) {
		t.Errorf("log = %q, want it to name the key %q", line, settings.KeyChatRetentionDays)
	}
	if strings.Contains(line, secret) {
		t.Errorf("log contains the stored VALUE; a settings file can hold a pasted credential:\n%s", line)
	}
}

// TestSettingsGet_AStoredNullIsReported pins that a stored null is reported: decoding it in
// place is a silent no-op.
func TestSettingsGet_AStoredNullIsReported(t *testing.T) {
	logs := captureLogs(t)
	got := getEffective(t, seedConfig(t, `{"knowledge_enabled":null}`))

	if !got.KnowledgeEnabled {
		t.Error("knowledge_enabled = false, want the default true")
	}
	if !strings.Contains(logs.String(), settings.KeyKnowledgeEnabled) {
		t.Errorf("a stored null was applied silently; want the key reported:\n%s", logs.String())
	}
}

// TestSettingsGet_FailsOpenOnAnUnreadableDocument pins the read half of read-open/write-closed:
// the shapes the write refuses are served as defaults here.
func TestSettingsGet_FailsOpenOnAnUnreadableDocument(t *testing.T) {
	tests := []struct {
		desc string
		seed func(*testing.T, string)
	}{
		{
			desc: "invalid json",
			seed: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(`{"theme":`), 0o600); err != nil {
					t.Fatalf("seed: %v", err)
				}
			},
		},
		{
			desc: "a top-level null",
			seed: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(`null`), 0o600); err != nil {
					t.Fatalf("seed: %v", err)
				}
			},
		},
		{
			desc: "a top-level array",
			seed: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(`[]`), 0o600); err != nil {
					t.Fatalf("seed: %v", err)
				}
			},
		},
		{
			desc: "over the size cap",
			seed: func(t *testing.T, path string) {
				t.Helper()
				big := append(bytes.Repeat([]byte(" "), maxSettingsBytes+1), []byte("{}")...)
				if err := os.WriteFile(path, big, 0o600); err != nil {
					t.Fatalf("seed: %v", err)
				}
			},
		},
		{
			desc: "a directory at the name",
			seed: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatalf("seed: %v", err)
				}
			},
		},
		{
			desc: "a symlink at the name",
			seed: func(t *testing.T, path string) {
				t.Helper()
				other := filepath.Join(filepath.Dir(path), "elsewhere.json")
				if err := os.WriteFile(other, []byte(`{"theme":"light"}`), 0o600); err != nil {
					t.Fatalf("seed target: %v", err)
				}
				if err := os.Symlink(other, path); err != nil {
					t.Fatalf("seed symlink: %v", err)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			dir := t.TempDir()
			tc.seed(t, filepath.Join(dir, settings.Filename))
			logs := captureLogs(t)

			got := getEffective(t, dir)
			if want := settings.EffectiveDefaults(); !effectiveEqual(got, want) {
				t.Errorf("GET over %s = %+v, want the defaults", tc.desc, got)
			}
			// Not silently: the operator must be told to look at the file.
			if !strings.Contains(logs.String(), "unreadable") {
				t.Errorf("no warning logged for %s; failing open must say so:\n%s", tc.desc, logs.String())
			}
		})
	}
}

// TestSettingsGet_DoesNotBlockOnAFIFO pins OpenRegular's FIFO refusal on the GET. A revert
// to os.ReadFile makes this HANG rather than fail.
func TestSettingsGet_DoesNotBlockOnAFIFO(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, settings.Filename)
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		rec := httptest.NewRecorder()
		handleSettingsGet(rec, path)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleSettingsGet blocked on a FIFO at config.json")
	}
}

// TestSettingsGet_ThenPatchPersistsOnlyTheStoredKeys pins that a complete RESPONSE does not
// become a complete FILE: PATCH merges against the stored document.
func TestSettingsGet_ThenPatchPersistsOnlyTheStoredKeys(t *testing.T) {
	dir := seedConfig(t, `{"theme":"dark"}`)
	path := filepath.Join(dir, settings.Filename)

	// The client reads the full effective document...
	if got := getEffective(t, dir); !got.KnowledgeEnabled {
		t.Fatal("precondition: the response should carry knowledge_enabled=true")
	}
	// ...then writes one key.
	s := &Server{agent: &fakeEngine{}, push: &testPush{}, configDir: dir}
	req := httptest.NewRequest(http.MethodPatch, "/api/settings", bytes.NewReader([]byte(`{"fb_path":"/workspace"}`)))
	rec := httptest.NewRecorder()
	s.handleSettingsWrite(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var onDisk map[string]json.RawMessage
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("parse %s: %v", raw, err)
	}
	if len(onDisk) != 2 {
		t.Errorf("config.json holds %d keys (%s), want exactly 2 — the stored theme and the patched fb_path", len(onDisk), raw)
	}
	for _, unwanted := range []string{settings.KeyKnowledgeEnabled, settings.KeyChatRetentionDays, settings.KeyNotifyAgentFinished} {
		if _, ok := onDisk[unwanted]; ok {
			t.Errorf("config.json gained %q; the GET's defaults must not materialise on disk", unwanted)
		}
	}
}

// TestSettingsGet_PatchAgainstANullDocumentDoesNotPanic pins the top-level `null` document:
// it unmarshals to a nil map, and maps.Copy onto nil panicked into an opaque 500.
func TestSettingsGet_PatchAgainstANullDocumentDoesNotPanic(t *testing.T) {
	dir := seedConfig(t, `null`)
	path := filepath.Join(dir, settings.Filename)
	s := &Server{agent: &fakeEngine{}, push: &testPush{}, configDir: dir}

	req := httptest.NewRequest(http.MethodPatch, "/api/settings", bytes.NewReader([]byte(`{"theme":"dark"}`)))
	rec := httptest.NewRecorder()
	// No recover(): an unrecovered panic failing the test IS the assertion.
	s.handleSettingsWrite(rec, req)

	// The write REFUSES: it cannot read what is stored.
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("PATCH over a null document = %d, want 500 (refuse, do not overwrite)", rec.Code)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(after) != "null" {
		t.Errorf("config.json = %s, want it untouched", after)
	}
}

// TestSettingsRoundTrip_RunOutcomeToggle drives a click's whole sequence over HTTP (read the
// default, write the opposite, read it back, check the file), since GET and PATCH resolve the
// value through different code.
func TestSettingsRoundTrip_RunOutcomeToggle(t *testing.T) {
	dir := seedConfig(t, "")
	path := filepath.Join(dir, settings.Filename)

	// A fresh volume answers the registry default (ON).
	if !getEffective(t, dir).NotifyRunOutcome {
		t.Fatal("GET on a fresh config dir = notify_run_outcome false, want true (the registry row is DefaultOn)")
	}

	s := &Server{agent: &fakeEngine{}, push: &testPush{}, configDir: dir}
	req := httptest.NewRequest(http.MethodPatch, "/api/settings",
		bytes.NewReader([]byte(`{"notify_run_outcome":false}`)))
	rec := httptest.NewRecorder()
	s.handleSettingsWrite(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH notify_run_outcome=false = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// The value round-trips on the next read...
	if got := getEffective(t, dir); got.NotifyRunOutcome {
		t.Error("GET after the patch = notify_run_outcome true, want false — the write did not reach the read")
	}
	// ...and it is on disk, so a restart honours it rather than reverting to the
	// registry default.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var onDisk map[string]json.RawMessage
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("parse %s: %v", raw, err)
	}
	if got := string(onDisk[settings.KeyNotifyRunOutcome]); got != "false" {
		t.Errorf("config.json[%s] = %q, want \"false\" (whole file: %s)",
			settings.KeyNotifyRunOutcome, got, raw)
	}

	// Back on, so a key stuck at one value cannot pass.
	req = httptest.NewRequest(http.MethodPatch, "/api/settings",
		bytes.NewReader([]byte(`{"notify_run_outcome":true}`)))
	rec = httptest.NewRecorder()
	s.handleSettingsWrite(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH notify_run_outcome=true = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !getEffective(t, dir).NotifyRunOutcome {
		t.Error("GET after re-enabling = notify_run_outcome false, want true")
	}
}

// effectiveEqual compares two views over EVERY field with one DeepEqual (this package owns
// the type), normalising an empty agent_ignore_files to nil.
func effectiveEqual(a, b marotte.EffectiveSettings) bool {
	if len(a.AgentIgnoreFiles) == 0 {
		a.AgentIgnoreFiles = nil
	}
	if len(b.AgentIgnoreFiles) == 0 {
		b.AgentIgnoreFiles = nil
	}
	return reflect.DeepEqual(a, b)
}
