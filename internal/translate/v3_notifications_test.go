package translate

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// statusFailures drives one _kiro/mcp/status frame carrying a single failed
// server and answers the RecordInitFailure calls it produced.
func statusFailures(t *testing.T, server map[string]any) []mcpFailure {
	t.Helper()
	deps, _ := newEventCaptureDeps()
	var failures []mcpFailure
	tr := New(rolesOf(&mcpCaptureDeps{baseDeps: deps, failures: &failures}))

	tr.HandleMCPStatus(t.Context(), "", &marotte.RPCResponse{
		Params: mustJSON(t, map[string]any{"servers": []map[string]any{server}}),
	})
	return failures
}

// KAS sends `failed` with no errorMessage for a server whose cause it could not
// state. Every surface appends the reason to a lead, so an empty one renders as
// a bare lead and the reader cannot tell a withheld cause from a lost one.
func TestHandleMCPStatus_SubstitutesAnUnstatedFailureReason(t *testing.T) {
	for _, tc := range []struct {
		name         string
		errorMessage any
	}{
		{name: "absent"},
		{name: "empty", errorMessage: ""},
		{name: "whitespace", errorMessage: "   \n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := map[string]any{"name": "broken", "status": "failed"}
			if tc.errorMessage != nil {
				server["errorMessage"] = tc.errorMessage
			}

			got := statusFailures(t, server)
			if len(got) != 1 {
				t.Fatalf("RecordInitFailure calls = %+v, want one", got)
			}
			if got[0].name != "broken" || got[0].reason != unstatedFailureReason {
				t.Errorf("failure = %+v, want {broken %q}", got[0], unstatedFailureReason)
			}
		})
	}
}

// A reason KAS did state travels verbatim: the substitution must not rewrite,
// truncate or prefix a real cause.
func TestHandleMCPStatus_CarriesAStatedFailureReasonVerbatim(t *testing.T) {
	got := statusFailures(t, map[string]any{
		"name":         "broken",
		"status":       "failed",
		"errorMessage": "connection refused",
	})

	if len(got) != 1 {
		t.Fatalf("RecordInitFailure calls = %+v, want one", got)
	}
	if got[0].reason != "connection refused" {
		t.Errorf("reason = %q, want %q", got[0].reason, "connection refused")
	}
}

// An OAuth failure is not an init failure: the authorizationUrl arm records the
// prompt and returns, so the reason substitution must not reach it.
func TestHandleMCPStatus_OAuthFailureRecordsNoInitFailure(t *testing.T) {
	got := statusFailures(t, map[string]any{
		"name":             "needs-auth",
		"status":           "failed",
		"authorizationUrl": "https://example.test/oauth",
	})

	if len(got) != 0 {
		t.Errorf("RecordInitFailure calls = %+v, want none for an OAuth prompt", got)
	}
}
