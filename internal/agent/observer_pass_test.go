package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

// An observer pass opens a session with the profile's presets as they are now and nothing else, answers every tool
// or permission request on it with a refusal, and closes the session when its reads return.
func TestObserverPass_OpensWithTheCurrentPresetsRefusesToolRequestsAndCloses(t *testing.T) {
	h, dir := reopenFixture(t)
	settleBoot(t, h)
	writeSetting(t, dir, settings.KeySecurityProfile, "unrestricted")
	o := oneObserver(t, h)
	fsID, permID := int64(41), int64(42)

	err := h.observe(t.Context(), func(_ context.Context, s engineSession) error {
		params := json.RawMessage(`{"sessionId":"` + string(s.SessionID()) + `"}`)
		o.deliver(&marotte.RPCResponse{ID: &fsID, Method: "fs/read_text_file", Params: params})
		o.deliver(&marotte.RPCResponse{ID: &permID, Method: marotte.MethodRequestPermission, Params: params})
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
			if _, a := o.responseTo(fsID); a {
				if _, b := o.responseTo(permID); b {
					return nil
				}
			}
		}
		return errors.New("the pass's requests were not answered within 5s")
	})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}

	so := o.lastSessionOpts()
	if so.Role != marotte.SessionRoleObserver || so.Mode != "" || so.Model != "" || so.SessionID != "" {
		t.Errorf("door = {Role %v, Mode %q, Model %q, SessionID %q}, want a new observer session with no mode or model",
			so.Role, so.Mode, so.Model, so.SessionID)
	}
	if !slices.Equal(so.Presets, []string{"allow-all"}) {
		t.Errorf("presets = %v, want [allow-all] from the profile set just now", so.Presets)
	}
	var rpcErr *marotte.RPCError
	if r, _ := o.responseTo(fsID); !errors.As(r.err, &rpcErr) || rpcErr.Code != marotte.RPCCodeMethodNotFound {
		t.Errorf("fs request answered %+v, want a method-not-found refusal", r)
	}
	if r, _ := o.responseTo(permID); !samePermissionOutcome(r.result, marotte.PermissionOutcomeCancelled()) {
		t.Errorf("permission request answered %+v, want cancelled", r)
	}
	if !sessionClosed(o) {
		t.Error("the observer pass left its session open")
	}
}

func samePermissionOutcome(got, want any) bool {
	a, errA := json.Marshal(got)
	b, errB := json.Marshal(want)
	return errA == nil && errB == nil && string(a) == string(b)
}
