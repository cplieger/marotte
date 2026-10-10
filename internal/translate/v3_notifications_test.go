package translate

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

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

// KAS sends `failed` with no errorMessage when it cannot state a cause; an empty reason
// renders as a bare lead.
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

// statusRecorder drives one _kiro/mcp/status frame and answers everything the
// recorder saw: the provenance per name, the OAuth prompts and the failures.
func statusRecorder(t *testing.T, servers ...map[string]any) (map[string]marotte.MCPSource, []string, []mcpFailure) {
	t.Helper()
	deps, _ := newEventCaptureDeps()
	sources := map[string]marotte.MCPSource{}
	var oauth []string
	var failures []mcpFailure
	tr := New(rolesOf(&mcpCaptureDeps{baseDeps: deps, sources: sources, oauth: &oauth, failures: &failures}))
	tr.HandleMCPStatus(t.Context(), "", &marotte.RPCResponse{
		Params: mustJSON(t, map[string]any{"servers": servers}),
	})
	return sources, oauth, failures
}

func resourceMeta(source map[string]any) map[string]any {
	return map[string]any{"kiro": map[string]any{"resource": map[string]any{
		"resourceType": "mcpServer", "source": source,
	}}}
}

// The _meta.kiro.resource.source stamp is the only statement of where KAS took the
// winning definition from.
func TestHandleMCPStatus_DecodesResourceSource(t *testing.T) {
	sources, _, _ := statusRecorder(t,
		map[string]any{"name": "ws", "status": "connected", "_meta": resourceMeta(map[string]any{
			"origin": "workspace", "root": "/workspace/app",
		})},
		map[string]any{"name": "pw", "status": "disabled", "_meta": resourceMeta(map[string]any{
			"origin": "power", "power": map[string]any{"name": "aws-docs", "source": "registry"},
		})},
		map[string]any{"name": "plain", "status": "failed", "errorMessage": "x"},
	)

	want := map[string]marotte.MCPSource{
		"ws":    {Origin: "workspace", Root: "/workspace/app"},
		"pw":    {Origin: "power", Power: "aws-docs"},
		"plain": {},
	}
	for name, w := range want {
		if got := sources[name]; got != w {
			t.Errorf("source[%s] = %+v, want %+v", name, got, w)
		}
	}
}

// failedAuthorization with no URL is a credentials refusal, so the reason names it rather
// than echoing KAS's bare "Unauthorized".
func TestHandleMCPStatus_FailedAuthorizationWithoutURL(t *testing.T) {
	for _, tc := range []struct {
		name, msg, want string
	}{
		{name: "bare unauthorized", msg: "Unauthorized", want: credentialsRejectedReason},
		{name: "no message", msg: "", want: credentialsRejectedReason},
		{name: "a stated detail is kept", msg: "token expired", want: credentialsRejectedReason + ": token expired"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, oauth, failures := statusRecorder(t, map[string]any{
				"name": "api", "status": "failed", "failedAuthorization": true, "errorMessage": tc.msg,
			})
			if len(oauth) != 0 {
				t.Errorf("RecordOAuth calls = %v, want none without a URL", oauth)
			}
			if len(failures) != 1 || failures[0].reason != tc.want {
				t.Errorf("failures = %+v, want one with reason %q", failures, tc.want)
			}
		})
	}
}

// The authorization URL outranks failedAuthorization: an OAuth server needing
// sign-in carries both, and signing in is its remedy.
func TestHandleMCPStatus_FailedAuthorizationWithURLIsAnOAuthPrompt(t *testing.T) {
	_, oauth, failures := statusRecorder(t, map[string]any{
		"name": "api", "status": "failed", "failedAuthorization": true,
		"authorizationUrl": "https://example.test/oauth",
	})
	if len(oauth) != 1 || oauth[0] != "api" {
		t.Errorf("RecordOAuth calls = %v, want [api]", oauth)
	}
	if len(failures) != 0 {
		t.Errorf("failures = %+v, want none", failures)
	}
}

// KAS reports a definition it cannot connect as failed with failedAuthorization false, so
// it takes the ordinary failure path.
func TestHandleMCPStatus_ConfigInvalidArrivesAsFailed(t *testing.T) {
	_, oauth, failures := statusRecorder(t, map[string]any{
		"name": "bad", "status": "failed", "failedAuthorization": false,
		"errorMessage": "Invalid MCP server configuration: missing command",
	})
	if len(oauth) != 0 {
		t.Errorf("RecordOAuth calls = %v, want none", oauth)
	}
	if len(failures) != 1 || failures[0].reason != "Invalid MCP server configuration: missing command" {
		t.Errorf("failures = %+v, want the configuration reason verbatim", failures)
	}
}
