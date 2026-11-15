package forges

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/marotte/internal/logsafe"
)

// TestCodeStatus_EveryForgeAPICodeHasOneStatus holds the table to forgeapi's own list, so a code a
// later forgeapi adds fails here until it is given its status.
func TestCodeStatus_EveryForgeAPICodeHasOneStatus(t *testing.T) {
	for _, c := range forgeapi.Codes() {
		status, ok := codeStatus[c]
		switch {
		case notErrorCodes[c]:
			if ok {
				t.Errorf("%s is no error code and has status %d", c, status)
			}
		case !ok:
			t.Errorf("forgeapi code %s has no status", c)
		case status < 400 || status > 599:
			t.Errorf("forgeapi code %s has status %d", c, status)
		}
	}
	for c := range codeStatus {
		if !slices.Contains(forgeapi.Codes(), c) {
			t.Errorf("codeStatus lists %s, which forgeapi does not declare", c)
		}
	}
}

func TestStatusFor_EveryKindAndEveryListedCode(t *testing.T) {
	cases := []struct {
		name string
		err  forgeapi.Error
		want int
	}{
		{"unauthorized", forgeapi.Error{Kind: forgeapi.KindUnauthorized}, http.StatusUnauthorized},
		{"reconnect required outranks its unauthorized kind", forgeapi.Error{Kind: forgeapi.KindUnauthorized, Code: forgeapi.CodeReconnectRequired}, http.StatusConflict},
		{"forbidden", forgeapi.Error{Kind: forgeapi.KindForbidden}, http.StatusForbidden},
		{"mutations disabled", forgeapi.Error{Kind: forgeapi.KindForbidden, Code: forgeapi.CodeMutationsDisabled}, http.StatusForbidden},
		{"scope insufficient", forgeapi.Error{Kind: forgeapi.KindForbidden, Code: forgeapi.CodeScopeInsufficient}, http.StatusForbidden},
		{"not found", forgeapi.Error{Kind: forgeapi.KindNotFound}, http.StatusNotFound},
		{"not mergeable", forgeapi.Error{Kind: forgeapi.KindNotMergeable}, http.StatusConflict},
		{"conflict", forgeapi.Error{Kind: forgeapi.KindConflict}, http.StatusConflict},
		{"rate limited", forgeapi.Error{Kind: forgeapi.KindRateLimited}, http.StatusTooManyRequests},
		{"transient", forgeapi.Error{Kind: forgeapi.KindTransient}, http.StatusServiceUnavailable},
		{"upstream", forgeapi.Error{Kind: forgeapi.KindUpstream}, http.StatusBadGateway},
		{"capability unsupported outranks its forbidden kind", forgeapi.Error{Kind: forgeapi.KindForbidden, Code: forgeapi.CodeCapabilityUnsupported}, http.StatusNotImplemented},
		{"grant unsupported", forgeapi.Error{Kind: forgeapi.KindUnknown, Code: forgeapi.CodeGrantUnsupported}, http.StatusNotImplemented},
		{"family undetected", forgeapi.Error{Kind: forgeapi.KindUnknown, Code: forgeapi.CodeFamilyUndetected}, http.StatusUnprocessableEntity},
		{"family undetected outranks the last read's kind", forgeapi.Error{Kind: forgeapi.KindNotFound, Code: forgeapi.CodeFamilyUndetected, Status: 404}, http.StatusUnprocessableEntity},
		{"an unlisted local code", forgeapi.Error{Kind: forgeapi.KindUnknown, Code: "something_new"}, http.StatusInternalServerError},
		{"no code at all", forgeapi.Error{Kind: forgeapi.KindUnknown}, http.StatusInternalServerError},
		// A code built with two Kinds answers its one status on both paths.
		{"validation from a conflicting create", forgeapi.Error{Kind: forgeapi.KindNotMergeable, Code: forgeapi.CodeValidation}, http.StatusBadGateway},
		{"connection invalid on a redirect", forgeapi.Error{Kind: forgeapi.KindUpstream, Code: forgeapi.CodeConnectionInvalid}, http.StatusBadRequest},
		{"plaintext refused on a redirect", forgeapi.Error{Kind: forgeapi.KindUpstream, Code: forgeapi.CodePlaintextRefused}, http.StatusBadRequest},
		{"private address refused on a redirect", forgeapi.Error{Kind: forgeapi.KindUpstream, Code: forgeapi.CodePrivateAddressRefused}, http.StatusBadRequest},
		{"missing SHA from the forge", forgeapi.Error{Kind: forgeapi.KindNotMergeable, Code: forgeapi.CodeMissingSHA}, http.StatusBadRequest},
		{"ref invalid from the forge", forgeapi.Error{Kind: forgeapi.KindNotFound, Code: forgeapi.CodeRefInvalid}, http.StatusBadRequest},
		{"list since invalid", forgeapi.Error{Kind: forgeapi.KindUnknown, Code: forgeapi.CodeListSinceInvalid}, http.StatusBadRequest},
		{"list merge state invalid", forgeapi.Error{Kind: forgeapi.KindUnknown, Code: forgeapi.CodeListMergeStateInvalid}, http.StatusBadRequest},
	}
	for _, code := range []string{
		forgeapi.CodeRepoRefInvalid, forgeapi.CodeRefInvalid, forgeapi.CodeMissingSHA,
		forgeapi.CodeStrategyNotAllowed, forgeapi.CodeCursorInvalid, forgeapi.CodeListStateInvalid,
		forgeapi.CodeListOwnerInvalid, forgeapi.CodePageBoundInvalid, forgeapi.CodeConnectionInvalid,
		forgeapi.CodeHeaderReserved, forgeapi.CodePlaintextRefused, forgeapi.CodePrivateAddressRefused,
		forgeapi.CodeAnonymousRefused, forgeapi.CodeBudgetInvalid,
	} {
		cases = append(cases, struct {
			name string
			err  forgeapi.Error
			want int
		}{"local " + code, forgeapi.Error{Kind: forgeapi.KindUnknown, Code: code}, http.StatusBadRequest})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := statusFor(&tc.err); got != tc.want {
				t.Errorf("statusFor(kind %v, code %q) = %d, want %d", tc.err.Kind, tc.err.Code, got, tc.want)
			}
		})
	}
}

func writeError(t *testing.T, err error) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/forges/github:github.com/repos", nil)
	w := httptest.NewRecorder()
	writeOpsError(w, r, err)
	var body map[string]any
	if decodeErr := json.Unmarshal(w.Body.Bytes(), &body); decodeErr != nil {
		t.Fatalf("writeOpsError(%v) wrote %q, not a JSON envelope: %v", err, w.Body.String(), decodeErr)
	}
	return w, body
}

func TestErrorEnvelope_CarriesCodeDiagIDRetryAfterSuccessorCapability(t *testing.T) {
	t.Run("rate limited", func(t *testing.T) {
		w, body := writeError(t, &forgeapi.Error{
			Op: "ListPRs", Kind: forgeapi.KindRateLimited, Code: "", Status: 403,
			RetryAfter: 1500 * time.Millisecond, DiagID: "ABCDEFGHIJKLM",
		})
		if w.Code != http.StatusTooManyRequests {
			t.Errorf("status = %d, want 429", w.Code)
		}
		if got := w.Header().Get("Retry-After"); got != "2" {
			t.Errorf("Retry-After = %q, want %q (1.5 s rounded up to whole seconds)", got, "2")
		}
		if got := body["retry_after_s"]; got != float64(2) {
			t.Errorf("retry_after_s = %v, want 2", got)
		}
		if got := body["diag_id"]; got != "ABCDEFGHIJKLM" {
			t.Errorf("diag_id = %v, want the error's DiagID", got)
		}
		if got, ok := body["code"]; !ok || got != "" {
			t.Errorf("code = %v (present %v), want an empty code present on the envelope", got, ok)
		}
		if got := body["kind"]; got != "rate_limited" {
			t.Errorf("kind = %v, want rate_limited: an empty code leaves the kind as the only class", got)
		}
	})
	t.Run("stale repository", func(t *testing.T) {
		succ := forgeapi.RepoRef{Family: forgeapi.FamilyGitHub, Selector: "New/Name", DisplayPath: "New/Name"}
		succ.ID = "v1.ANYTHING"
		w, body := writeError(t, &forgeapi.Error{
			Kind: forgeapi.KindNotFound, Code: forgeapi.CodeRepoRefStale, Successor: &succ, DiagID: "DIAG",
		})
		if w.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", w.Code)
		}
		if body["code"] != forgeapi.CodeRepoRefStale || body["kind"] != "not_found" {
			t.Errorf("code, kind = %v, %v; want %q, not_found", body["code"], body["kind"], forgeapi.CodeRepoRefStale)
		}
		s, _ := body["successor"].(map[string]any)
		if s["repo_id"] != succ.Encode() || s["display_path"] != "New/Name" {
			t.Errorf("successor = %v, want repo_id %q (the canonical id) and display_path New/Name", s, succ.Encode())
		}
	})
	t.Run("capability unsupported", func(t *testing.T) {
		w, body := writeError(t, &forgeapi.Error{
			Op: "RerunFailedChecks", Kind: forgeapi.KindForbidden, Code: forgeapi.CodeCapabilityUnsupported,
			Capability: forgeapi.CapRerunChecks,
			Evidence:   forgeapi.Evidence{Source: forgeapi.EvidenceSwagger, Detail: "no re-run verb"},
		})
		if w.Code != http.StatusNotImplemented {
			t.Errorf("status = %d, want 501", w.Code)
		}
		c, _ := body["capability"].(map[string]any)
		want := map[string]any{"name": "rerun_checks", "support": "no", "source": "swagger", "detail": "no re-run verb"}
		for k, v := range want {
			if c[k] != v {
				t.Errorf("capability[%q] = %v, want %v (capability %v)", k, c[k], v, c)
			}
		}
		if _, ok := body["retry_after_s"]; ok {
			t.Errorf("retry_after_s present on an error with no wait: %v", body)
		}
	})
}

func TestErrorEnvelope_BoundsUpstreamText(t *testing.T) {
	msg := strings.Repeat("x", 5000) + "\x1b[31m\u202e" + strings.Repeat("y", 100)
	_, body := writeError(t, &forgeapi.Error{Kind: forgeapi.KindUpstream, Code: "", Message: msg})
	got, _ := body["error"].(string)
	// logsafe.Field caps the text at MaxFieldBytes and marks the cut after it.
	if limit := logsafe.MaxFieldBytes + len("..."); len(got) > limit {
		t.Errorf("error message is %d bytes, want at most %d", len(got), limit)
	}
	if got == "" {
		t.Error("error message is empty, want the bounded upstream text")
	}
	if strings.ContainsAny(got, "\x1b\u202e") {
		t.Errorf("error message %q carries an escape or a bidi control", got)
	}
}

type listErrCore struct {
	forgeapi.Core
	err error
}

func (listErrCore) Close() {}

func (c listErrCore) ListPRs(context.Context, forgeapi.RepoRef, ...forgeapi.ListOption) (forgeapi.Page[forgeapi.PullRequest], error) {
	return forgeapi.Page[forgeapi.PullRequest]{}, c.err
}

func TestErrorRoute_LibraryDeadlineIs504AndAWalkAwayWritesNothing(t *testing.T) {
	path := githubRepoPath("v1.6f2f72", "prs")
	t.Run("the library's own deadline", func(t *testing.T) {
		mux := repoRoutes(recordManager(t, listErrCore{err: context.DeadlineExceeded}))
		if w := getRoute(t, mux, path); w.Code != http.StatusGatewayTimeout {
			t.Errorf("status = %d, want 504 (body %s)", w.Code, w.Body)
		}
	})
	t.Run("a client that walked away", func(t *testing.T) {
		mux := repoRoutes(recordManager(t, listErrCore{err: &forgeapi.Error{Kind: forgeapi.KindTransient}}))
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil))
		if w.Body.Len() != 0 || w.Header().Get("Content-Type") != "" {
			t.Errorf("a cancelled request was answered: content-type %q, body %q", w.Header().Get("Content-Type"), w.Body)
		}
	})
}
