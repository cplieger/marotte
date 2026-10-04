package forges

import (
	"errors"
	"testing"

	"github.com/cplieger/forgeapi"
)

// A cancel ends the grant with the library's Close (ADR-0099): the grant leaves
// the registry at once, sends no token request, and its endpoint is released.

func TestGrantRegistry_CancelFreesItsPlace(t *testing.T) {
	e := newGrantEndpoint(t, "900", pendingAnswer)
	r := newGrantRegistry()
	var ids []string
	for range maxHeldGrants {
		ids = append(ids, startTestGrant(t, r, e).GrantID)
	}
	if !r.cancel(ids[0]) {
		t.Fatalf("Setup: cancel(%s) = false, want true", ids[0])
	}

	req, _ := marotteApp(forgeapi.FamilyGitHub)
	rec := e.record()
	if _, err := r.start(t.Context(), &rec, req); err != nil {
		t.Errorf("start() after a cancel with %d grants started = %v, want the cancelled grant's place free",
			maxHeldGrants, err)
	}
}

func TestGrantRegistry_CancelReleasesTheEndpointAndSendsNothing(t *testing.T) {
	e := newGrantEndpoint(t, "900", pendingAnswer)
	r := newGrantRegistry()
	started := startTestGrant(t, r, e)

	if !r.cancel(started.GrantID) {
		t.Fatalf("Setup: cancel(%s) = false, want true", started.GrantID)
	}
	if _, err := r.poll(t.Context(), started.GrantID); !errors.Is(err, errGrantNotFound) {
		t.Errorf("poll() after cancel = %v, want errGrantNotFound", err)
	}
	waitFor(t, "the cancelled grant's endpoint connection to close", func() bool { return e.closed.Load() > 0 })
	if got := e.tokens.Load(); got != 0 {
		t.Errorf("token requests after a cancel = %d, want 0", got)
	}
	if r.cancel(started.GrantID) {
		t.Error("a second cancel of one grant = true, want false")
	}
}
