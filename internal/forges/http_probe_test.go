package forges

// A Check reads the account and leaves its verdict on the connection's row as
// one status with its reason in the error envelope's terms, so the row can say
// what went wrong and what fixes it without the client reading the message.

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
		t.Fatalf("POST %s = %s, want the row the check left", githubProbePath, rec.Body)
	}
	return got
}

func TestProbeRoute_AScopeRefusalRefusesTheRow(t *testing.T) {
	isolateGit(t)
	core := &whoamiCore{err: &forgeapi.Error{
		Op: "Whoami", Code: forgeapi.CodeScopeInsufficient, Kind: forgeapi.KindForbidden, Status: http.StatusForbidden,
		Message: "[User: Read] Access denied",
	}}
	got := postProbe(t, repoRoutes(recordManager(t, core)))

	if got.Forge.Status != ForgeRefused || got.Forge.Error == nil {
		t.Fatalf("check over a scope refusal = %+v, want refused with its reason", got.Forge)
	}
	if e := got.Forge.Error; e.Code != forgeapi.CodeScopeInsufficient || e.Kind != "forbidden" {
		t.Errorf("row error code, kind = %q, %q, want %q, forbidden", e.Code, e.Kind, forgeapi.CodeScopeInsufficient)
	}
	if !strings.Contains(got.Forge.Error.Message, "[User: Read]") || got.Error != got.Forge.Error.Message {
		t.Errorf("check error %q, row message %q, want both the same sentence naming what the token lacks",
			got.Error, got.Forge.Error.Message)
	}
}

func TestProbeRoute_ATemporaryFailureAnswersRetryingWithItsSentence(t *testing.T) {
	isolateGit(t)
	core := &whoamiCore{err: &forgeapi.Error{Op: "Whoami", Kind: forgeapi.KindTransient, Message: "no route to host"}}
	got := postProbe(t, repoRoutes(recordManager(t, core)))

	if got.Forge.Status != ForgeRetrying || got.Forge.Error == nil || got.Forge.Error.Kind != "transient" {
		t.Fatalf("check over a dead network = %+v, want retrying with kind transient", got.Forge)
	}
	if got.Error == "" || got.Error != got.Forge.Error.Message {
		t.Errorf("check error %q, row message %q, want both the same sentence", got.Error, got.Forge.Error.Message)
	}
}

func TestProbeRoute_ARateLimitNamesWhenItLifts(t *testing.T) {
	isolateGit(t)
	core := &whoamiCore{err: &forgeapi.Error{
		Op: "Whoami", Kind: forgeapi.KindRateLimited, Status: http.StatusTooManyRequests, RetryAfter: 41200 * time.Millisecond,
	}}
	got := postProbe(t, repoRoutes(recordManager(t, core)))

	f := got.Forge
	if f.Status != ForgeRetrying || f.Error == nil || f.Error.Kind != "rate_limited" || f.CheckedAt == 0 {
		t.Fatalf("row = %+v, want retrying, rate_limited and the check's time", f)
	}
	if wait := f.Error.RetryAt - f.CheckedAt; wait != 41200 {
		t.Errorf("retry_at - checked_at = %d ms, want the forge's 41200", wait)
	}
}

func TestProbeRoute_ASuccessClearsTheCodedError(t *testing.T) {
	isolateGit(t)
	core := &whoamiCore{err: &forgeapi.Error{Op: "Whoami", Kind: forgeapi.KindRateLimited, RetryAfter: time.Minute}}
	mux := repoRoutes(recordManager(t, core))
	_ = postProbe(t, mux)

	core.err = nil
	got := postProbe(t, mux)
	if got.Forge.Status != ForgeConnected || got.Error != "" || got.Forge.Error != nil {
		t.Errorf("check after the limit lifted = %+v, want connected with no error left from the refusal", got)
	}
}

func TestProbeRoute_TheRowsErrorIsSanitized(t *testing.T) {
	isolateGit(t)
	core := &whoamiCore{err: &forgeapi.Error{
		Op: "Whoami", Kind: forgeapi.KindForbidden, Status: http.StatusForbidden, Message: "denied \u202egnihton\u202c here\nand on",
	}}
	got := postProbe(t, repoRoutes(recordManager(t, core)))

	for name, text := range map[string]string{"error": got.Error, "message": got.Forge.Error.Message} {
		if strings.ContainsAny(text, "\u202e\u202c\n") {
			t.Errorf("check %s = %q, want the upstream text's bidi controls and line break gone", name, text)
		}
		if !strings.Contains(text, "denied") {
			t.Errorf("check %s = %q, want the upstream sentence kept", name, text)
		}
	}
}

func TestProbeRoute_ANonForgeFailureCarriesNoCode(t *testing.T) {
	isolateGit(t)
	core := &whoamiCore{err: errNoClient}
	got := postProbe(t, repoRoutes(recordManager(t, core)))

	if f := got.Forge; f.Status != ForgeRetrying || f.Error == nil || f.Error.Code != "" || f.Error.Kind != "" || f.Error.Message == "" {
		t.Errorf("row after a failure that is not a forge refusal = %+v, want retrying with its sentence and no code or kind", f)
	}
}
