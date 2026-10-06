package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

type fakeLocks map[string]marotte.GovernanceLock

func (f fakeLocks) GovernanceLocks() map[string]marotte.GovernanceLock { return f }

// liveLocks is a lock map that moves while the server holds it, as the runtime's does.
type liveLocks struct {
	m  map[string]marotte.GovernanceLock
	mu sync.Mutex
}

func (l *liveLocks) set(m map[string]marotte.GovernanceLock) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.m = m
}

func (l *liveLocks) GovernanceLocks() map[string]marotte.GovernanceLock {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.m
}

type argsRunner struct {
	fakeCLIRunner
	store      map[string]string
	onWrite    func(key, value string)
	runs       [][]string
	mu         sync.Mutex
	failWrites bool
}

func (a *argsRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	if len(args) == 3 && a.onWrite != nil {
		a.onWrite(args[1], args[2])
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.runs = append(a.runs, args)
	if len(args) == 2 {
		return []byte(a.store[args[1]] + " (global)"), nil
	}
	if len(args) == 3 {
		if a.failWrites {
			return nil, errors.New("refused")
		}
		if a.store == nil {
			a.store = map[string]string{}
		}
		a.store[args[1]] = args[2]
	}
	return nil, nil
}

func (a *argsRunner) value(key string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.store[key]
}

func putKiroSetting(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.writeKiroSetting(rec, httptest.NewRequest(http.MethodPut, "/api/kiro-settings", bytes.NewReader([]byte(body))))
	return rec
}

func TestWriteKiroSetting_RefusesAValueTheOrganizationLocked(t *testing.T) {
	runner := &argsRunner{}
	s := &Server{cliRunner: runner, cliTimeouts: defaultCLITimeouts(), governance: fakeLocks{
		marotte.LockTelemetry: {Value: true, Source: marotte.LockSourceOrganization, Reason: "Set by your organization"},
	}}

	if rec := putKiroSetting(t, s, `{"key":"telemetry.enabled","value":"false"}`); rec.Code != http.StatusConflict {
		t.Fatalf("PUT telemetry false under an on lock = %d, want 409: %s", rec.Code, rec.Body)
	}
	if len(runner.runs) != 0 {
		t.Errorf("a refused write still ran kiro-cli: %v", runner.runs)
	}
	if rec := putKiroSetting(t, s, `{"key":"telemetry.enabled","value":"true"}`); rec.Code != http.StatusOK {
		t.Errorf("PUT telemetry true under an on lock = %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec := putKiroSetting(t, s, `{"key":"hooks.showStatus","value":"false"}`); rec.Code != http.StatusOK {
		t.Errorf("PUT an unlocked key = %d, want 200: %s", rec.Code, rec.Body)
	}
}

// A lock pins kiro-cli's one store to the organization's value; lifting it writes the
// user's own choice back, including one recorded while the lock held.
func TestApplyGovernanceLocks_RestoresTheUserValueWhenTheLockLifts(t *testing.T) {
	const key = "telemetry.enabled"
	locked := map[string]marotte.GovernanceLock{marotte.LockTelemetry: {Value: true}}
	t.Run("lock_then_unlock", func(t *testing.T) {
		runner := &argsRunner{store: map[string]string{key: "false"}}
		gov := &liveLocks{m: locked}
		s := &Server{cliRunner: runner, cliTimeouts: defaultCLITimeouts(), governance: gov}
		s.ApplyGovernanceLocks(t.Context())
		if got := runner.value(key); got != "true" {
			t.Fatalf("under the lock kiro-cli holds %s = %q, want the organization's \"true\"", key, got)
		}
		s.ApplyGovernanceLocks(t.Context())
		gov.set(nil)
		s.ApplyGovernanceLocks(t.Context())
		if got := runner.value(key); got != "false" {
			t.Errorf("after the lock lifted kiro-cli holds %s = %q, want the user's \"false\" back", key, got)
		}
	})
	t.Run("restore_retries_after_a_failure", func(t *testing.T) {
		runner := &argsRunner{store: map[string]string{key: "false"}}
		gov := &liveLocks{m: locked}
		s := &Server{cliRunner: runner, cliTimeouts: defaultCLITimeouts(), governance: gov}
		s.ApplyGovernanceLocks(t.Context())
		gov.set(nil)
		runner.failWrites = true
		s.ApplyGovernanceLocks(t.Context())
		runner.failWrites = false
		s.ApplyGovernanceLocks(t.Context())
		if got := runner.value(key); got != "false" {
			t.Errorf("after a failed then a retried restore kiro-cli holds %s = %q, want \"false\"", key, got)
		}
	})
}

// A user write that checked the lock before it moved must not land over the
// organization's value: the lock application waits for it, then pins the store.
func TestWriteKiroSetting_ACrossingLockWinsOverTheUserWrite(t *testing.T) {
	const key = "telemetry.enabled"
	runner := &argsRunner{store: map[string]string{key: "true"}}
	gov := &liveLocks{}
	s := &Server{cliRunner: runner, cliTimeouts: defaultCLITimeouts(), governance: gov}
	applied := make(chan struct{})
	runner.onWrite = func(_, value string) {
		if value != "false" {
			return
		}
		runner.onWrite = nil
		gov.set(map[string]marotte.GovernanceLock{marotte.LockTelemetry: {Value: true}})
		go func() {
			s.ApplyGovernanceLocks(t.Context())
			close(applied)
		}()
		// Bounded: a lock application that is not held off finishes well inside it.
		select {
		case <-applied:
		case <-time.After(200 * time.Millisecond):
		}
	}
	if rec := putKiroSetting(t, s, `{"key":"`+key+`","value":"false"}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT %s false before the lock = %d, want 200: %s", key, rec.Code, rec.Body)
	}
	<-applied
	if got := runner.value(key); got != "true" {
		t.Errorf("after a lock crossed the user's write kiro-cli holds %s = %q, want the organization's \"true\"", key, got)
	}
	gov.set(nil)
	s.ApplyGovernanceLocks(t.Context())
	if got := runner.value(key); got != "false" {
		t.Errorf("after the crossing lock lifted kiro-cli holds %s = %q, want the user's \"false\"", key, got)
	}
}

// The browser can see an unlock before the server's lock hook has run, so a user
// write made in that window must survive the restore of the older preference.
func TestWriteKiroSetting_AWriteAfterTheUnlockOutlivesThePendingRestore(t *testing.T) {
	const key = "telemetry.enabled"
	runner := &argsRunner{store: map[string]string{key: "false"}}
	gov := &liveLocks{m: map[string]marotte.GovernanceLock{marotte.LockTelemetry: {Value: false}}}
	s := &Server{cliRunner: runner, cliTimeouts: defaultCLITimeouts(), governance: gov}
	s.ApplyGovernanceLocks(t.Context())
	gov.set(nil)
	if rec := putKiroSetting(t, s, `{"key":"`+key+`","value":"true"}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT %s true after the unlock = %d, want 200: %s", key, rec.Code, rec.Body)
	}
	s.ApplyGovernanceLocks(t.Context())
	if got := runner.value(key); got != "true" {
		t.Errorf("after the delayed restore kiro-cli holds %s = %q, want the user's newer \"true\"", key, got)
	}
}

// kiro-cli holds the organization's value while a lock stands, so a read must
// report the user's own value, which the client shows again on unlock.
func TestReadKiroSettings_ReportsTheHeldUserValue(t *testing.T) {
	const key = "telemetry.enabled"
	runner := &argsRunner{store: map[string]string{key: "false"}}
	gov := &liveLocks{m: map[string]marotte.GovernanceLock{marotte.LockTelemetry: {Value: true}}}
	s := &Server{cliRunner: runner, cliTimeouts: defaultCLITimeouts(), governance: gov}
	read := func() string {
		t.Helper()
		rec := httptest.NewRecorder()
		s.readKiroSettings(rec, httptest.NewRequest(http.MethodGet, "/api/kiro-settings?keys="+key, nil))
		var body struct {
			Settings map[string]string `json:"settings"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("GET /api/kiro-settings body %q: %v", rec.Body, err)
		}
		return body.Settings[key]
	}
	s.ApplyGovernanceLocks(t.Context())
	if got := read(); got != "false" {
		t.Errorf("GET %s under the lock = %q, want the user's \"false\" (kiro-cli holds %q)", key, got, runner.value(key))
	}
	gov.set(nil)
	if got := read(); got != "false" {
		t.Errorf("GET %s with the restore pending = %q, want the user's \"false\"", key, got)
	}
}

func TestSettingsWrite_ContentCollectionPushesLive(t *testing.T) {
	eng := &fakeEngine{}
	s := &Server{agent: eng, push: &testPush{}, configDir: t.TempDir()}
	patchSettings(t.Context(), t, s, `{"`+settings.KeyContentCollectionEnabled+`":true}`)
	if eng.ccPushes != 1 {
		t.Errorf("PATCH content_collection_enabled pushed %d times, want 1", eng.ccPushes)
	}
	patchSettings(t.Context(), t, s, `{"`+settings.KeyFBPath+`":"/workspace"}`)
	if eng.ccPushes != 1 {
		t.Errorf("an unrelated PATCH pushed content collection (%d pushes)", eng.ccPushes)
	}
}
