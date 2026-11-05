package translate

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func countEvents(events *[]marotte.ServerEvent, typ marotte.EventType) int {
	n := 0
	for _, e := range *events {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// TestHandlePolicyChanged pins one global (empty chatID) permissions_changed nudge with an empty
// payload, the signal a loaded panel refetches on, and the errors handed to governance.
func TestHandlePolicyChanged(t *testing.T) {
	deps, events := newEventCaptureDeps()
	var govErrs []marotte.PolicyErrorItem
	var govCalls int
	deps.onPolicyChanged = func(errs []marotte.PolicyErrorItem, reloaded bool) {
		govCalls++
		govErrs = errs
		if !reloaded {
			t.Error("governance got reloaded=false for a policy/changed")
		}
	}
	tr := New(rolesOf(deps))

	tr.HandlePolicyChanged(t.Context(), marotte.ChatID("c1"), &marotte.RPCResponse{
		Params: mustJSON(t, map[string]any{
			"sessionId": "sess-1", "status": "success",
			"errors": []map[string]any{{"scope": "user", "message": "unknown capability"}},
		}),
	})

	if countEvents(events, marotte.EventPermissionsChanged) != 1 {
		t.Fatalf("permissions_changed count = %d, want 1", countEvents(events, marotte.EventPermissionsChanged))
	}
	for _, e := range *events {
		if e.Type != marotte.EventPermissionsChanged {
			continue
		}
		if e.ChatID != "" {
			t.Errorf("event ChatID = %q, want empty (policy is global)", e.ChatID)
		}
		if _, ok := e.Payload.(marotte.PermissionsChangedPayload); !ok {
			t.Errorf("payload = %#v, want the PermissionsChangedPayload nudge", e.Payload)
		}
	}
	if govCalls != 1 || len(govErrs) != 1 || govErrs[0].Message != "unknown capability" {
		t.Errorf("governance got %d calls with %+v, want one with the notification's error", govCalls, govErrs)
	}
}

// TestHandlePolicyError pins one policy_error event carrying the error list, rendered in
// Settings → Permissions; without it a rule KAS rejects is silent.
func TestHandlePolicyError(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))

	tr.HandlePolicyError(t.Context(), marotte.ChatID("c1"), &marotte.RPCResponse{
		Params: mustJSON(t, map[string]any{
			"sessionId": "sess-1",
			"errors": []map[string]any{
				{"scope": "user", "source": "permissions.yaml", "message": "bad rule", "fatal": true},
			},
		}),
	})

	if countEvents(events, marotte.EventPolicyError) != 1 {
		t.Fatalf("policy_error count = %d, want 1", countEvents(events, marotte.EventPolicyError))
	}
	for _, e := range *events {
		if e.Type != marotte.EventPolicyError {
			continue
		}
		p, ok := e.Payload.(marotte.PolicyErrorPayload)
		if !ok || len(p.Errors) != 1 || p.Errors[0].Message != "bad rule" || !p.Errors[0].Fatal {
			t.Errorf("payload = %+v (ok=%v)", e.Payload, ok)
		}
	}
}

// TestHandlePolicyMalformedNoop pins that malformed params are dropped
// without a broadcast.
func TestHandlePolicyMalformedNoop(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	tr.HandlePolicyChanged(t.Context(), marotte.ChatID("c1"), &marotte.RPCResponse{Params: []byte("{")})
	tr.HandlePolicyError(t.Context(), marotte.ChatID("c1"), &marotte.RPCResponse{Params: []byte("{")})
	if len(*events) != 0 {
		t.Errorf("malformed params produced %d events, want 0", len(*events))
	}
}

// Governance hears every policy notification with its errors, so a fatal
// administration error can fail the lock map closed; only policy/changed is a
// completed reload that may lift it.
func TestPolicyNotifications_ReachGovernanceWithTheirErrors(t *testing.T) {
	fatal := []map[string]any{{"scope": "administration", "message": "parse", "fatal": true}}
	for _, tc := range []struct {
		handle       func(tr *Translator, msg *marotte.RPCResponse)
		name         string
		wantReloaded bool
	}{
		{name: "changed", wantReloaded: true, handle: func(tr *Translator, msg *marotte.RPCResponse) {
			tr.HandlePolicyChanged(t.Context(), "", msg)
		}},
		{name: "error", handle: func(tr *Translator, msg *marotte.RPCResponse) {
			tr.HandlePolicyError(t.Context(), "", msg)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, _ := newEventCaptureDeps()
			var gotErrs []marotte.PolicyErrorItem
			calls, reloaded := 0, false
			deps.onPolicyChanged = func(errs []marotte.PolicyErrorItem, r bool) {
				calls++
				gotErrs, reloaded = errs, r
			}
			tc.handle(New(rolesOf(deps)), &marotte.RPCResponse{
				Params: mustJSON(t, map[string]any{"sessionId": "s", "status": "failed", "errors": fatal}),
			})
			if calls != 1 || len(gotErrs) != 1 || !gotErrs[0].Fatal || gotErrs[0].Scope != "administration" {
				t.Fatalf("PolicyChanged calls = %d errs = %+v, want one call carrying the fatal administration error", calls, gotErrs)
			}
			if reloaded != tc.wantReloaded {
				t.Errorf("PolicyChanged reloaded = %v, want %v", reloaded, tc.wantReloaded)
			}
		})
	}
}
