package forges

// A probe reads the account and leaves its verdict on the connection's row in
// the error envelope's terms, so the row can say what went wrong and what fixes
// it without the client reading the message.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
)

const githubProbePath = "/api/forges/github%3Agithub.com/probe"

func postProbe(t *testing.T, mux *http.ServeMux) ProbeResult {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, githubProbePath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST %s = %d %s, want 200", githubProbePath, rec.Code, rec.Body)
	}
	var got ProbeResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("POST %s body %q: %v", githubProbePath, rec.Body, err)
	}
	if got.Forge == nil {
		t.Fatalf("POST %s = %s, want the row the probe left", githubProbePath, rec.Body)
	}
	return got
}

func TestProbeRoute_AScopeRefusalCodesTheRow(t *testing.T) {
	core := &whoamiCore{err: &forgeapi.Error{
		Op: "Whoami", Code: forgeapi.CodeScopeInsufficient, Kind: forgeapi.KindForbidden, Status: http.StatusForbidden,
		Message: "[User: Read] Access denied",
	}}
	got := postProbe(t, repoRoutes(recordManager(t, core)))

	if got.Connected || got.Forge.Connected {
		t.Errorf("probe over a scope refusal = %+v, want disconnected", got)
	}
	if got.Forge.ErrorCode != forgeapi.CodeScopeInsufficient || got.Forge.ErrorKind != "forbidden" {
		t.Errorf("row error code, kind = %q, %q, want %q, forbidden", got.Forge.ErrorCode, got.Forge.ErrorKind, forgeapi.CodeScopeInsufficient)
	}
	if !strings.Contains(got.Forge.LastError, "[User: Read]") || got.Error != got.Forge.LastError {
		t.Errorf("probe error %q, row last_error %q, want both the same sentence naming what the token lacks", got.Error, got.Forge.LastError)
	}
}

func TestProbeRoute_ATemporaryFailureAnswersConnectedWithItsSentence(t *testing.T) {
	core := &whoamiCore{err: &forgeapi.Error{Op: "Whoami", Kind: forgeapi.KindTransient, Message: "no route to host"}}
	got := postProbe(t, repoRoutes(recordManager(t, core)))

	if !got.Connected || !got.Forge.Connected {
		t.Errorf("probe over a dead network = %+v, want connected", got)
	}
	if got.Error == "" || got.Error != got.Forge.LastError || got.Forge.ErrorKind != "transient" {
		t.Errorf("probe error %q, row last_error %q, kind %q, want both the same sentence, kind transient",
			got.Error, got.Forge.LastError, got.Forge.ErrorKind)
	}
}

func TestProbeRoute_ARateLimitNamesTheWaitInWholeSeconds(t *testing.T) {
	core := &whoamiCore{err: &forgeapi.Error{
		Op: "Whoami", Kind: forgeapi.KindRateLimited, Status: http.StatusTooManyRequests, RetryAfter: 41200 * time.Millisecond,
	}}
	got := postProbe(t, repoRoutes(recordManager(t, core)))

	if got.Forge.ErrorKind != "rate_limited" || got.Forge.RetryAfterS != 42 {
		t.Errorf("row error kind, retry_after_s = %q, %d, want rate_limited, 42", got.Forge.ErrorKind, got.Forge.RetryAfterS)
	}
	if got.Forge.LastProbed == 0 {
		t.Errorf("row = %+v, want the probe time the wait counts from", got.Forge)
	}
}

func TestProbeRoute_ASuccessClearsTheCodedError(t *testing.T) {
	core := &whoamiCore{err: &forgeapi.Error{Op: "Whoami", Kind: forgeapi.KindRateLimited, RetryAfter: time.Minute}}
	mux := repoRoutes(recordManager(t, core))
	_ = postProbe(t, mux)

	core.err = nil
	got := postProbe(t, mux)
	if !got.Connected || got.Error != "" {
		t.Errorf("probe after the limit lifted = %+v, want connected with no error", got)
	}
	if f := got.Forge; f.LastError != "" || f.ErrorCode != "" || f.ErrorKind != "" || f.RetryAfterS != 0 {
		t.Errorf("row after a successful probe = %+v, want no error, code, kind or wait left from the refusal", f)
	}
}

// A probe's refusal stays the row's verdict until a probe succeeds: a refresh
// rebuilds the row from the record and the store, and neither holds it.
func TestProbeRoute_ARefusalSurvivesARefresh(t *testing.T) {
	core := &whoamiCore{err: &forgeapi.Error{
		Op: "Whoami", Code: forgeapi.CodeScopeInsufficient, Kind: forgeapi.KindForbidden, Status: http.StatusForbidden,
		Message: "denied",
	}}
	m := recordManager(t, core)
	mux := repoRoutes(m)
	_ = postProbe(t, mux)
	if err := m.Refresh(t.Context()); err != nil {
		t.Fatalf("Setup: Refresh() = %v", err)
	}
	if f := m.Get("github:github.com"); f == nil || f.Connected || f.ErrorCode != forgeapi.CodeScopeInsufficient {
		t.Errorf("row after a refused probe and a Refresh = %+v, want disconnected and coded %q", f, forgeapi.CodeScopeInsufficient)
	}

	core.err = nil
	_ = postProbe(t, mux)
	if err := m.Refresh(t.Context()); err != nil {
		t.Fatalf("Setup: Refresh() = %v", err)
	}
	if f := m.Get("github:github.com"); f == nil || !f.Connected || f.LastError != "" {
		t.Errorf("row after a successful probe and a Refresh = %+v, want connected with no error", f)
	}
}

func TestProbeRoute_TheRowsErrorIsSanitized(t *testing.T) {
	core := &whoamiCore{err: &forgeapi.Error{
		Op: "Whoami", Kind: forgeapi.KindForbidden, Status: http.StatusForbidden, Message: "denied \u202egnihton\u202c here\nand on",
	}}
	got := postProbe(t, repoRoutes(recordManager(t, core)))

	for name, text := range map[string]string{"error": got.Error, "last_error": got.Forge.LastError} {
		if strings.ContainsAny(text, "\u202e\u202c\n") {
			t.Errorf("probe %s = %q, want the upstream text's bidi controls and line break gone", name, text)
		}
		if !strings.Contains(text, "denied") {
			t.Errorf("probe %s = %q, want the upstream sentence kept", name, text)
		}
	}
}

func TestProbeRoute_ANonForgeFailureCarriesNoCode(t *testing.T) {
	core := &whoamiCore{err: errNoClient}
	got := postProbe(t, repoRoutes(recordManager(t, core)))

	if got.Forge.ErrorCode != "" || got.Forge.ErrorKind != "" || got.Forge.LastError == "" {
		t.Errorf("row after a failure that is not a forge refusal = %+v, want its sentence and no code or kind", got.Forge)
	}
}
