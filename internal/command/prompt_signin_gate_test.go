package command

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// gateDeps counts the bridges a prompt opens: during a known outage the session's open is the first engine call.
type gateDeps struct {
	*surfaceDeps
	opened atomic.Int64
}

func (d *gateDeps) OpenBridge(ctx context.Context, chatID marotte.ChatID, model string) (Bridge, func(), error) {
	d.opened.Add(1)
	return d.surfaceDeps.OpenBridge(ctx, chatID, model)
}

func (d *gateDeps) OpenBridgeAt(ctx context.Context, chatID marotte.ChatID, model string, _ Reservation) (Bridge, func(), error) {
	return d.OpenBridge(ctx, chatID, model)
}

func runGatedPrompt(t *testing.T, reach *reachRecorder) *gateDeps {
	t.Helper()
	deps := &gateDeps{surfaceDeps: newSurfaceDeps()}
	roles := promptRolesOf(deps)
	roles.signIn = reach
	join := &promptJoin{}
	roles.lifecycle = join
	if _, err := cmdPrompt(t.Context(), roles, promptReq(t, "c1", "do the thing")); err != nil {
		t.Fatalf("CmdPrompt = %v, want the early ack", err)
	}
	join.join()
	return deps
}

// A prompt that meets a known outage, which one bounded reprobe did not end, fails at once as a sign-in failure
// without opening a session.
func TestPrompt_AKnownOutageFailsTheTurnBeforeAnyEngineCall(t *testing.T) {
	reach := &reachRecorder{blocked: true}
	outage, auth := promptFailures(t, "signin_outage"), promptFailures(t, "auth")
	deps := runGatedPrompt(t, reach)

	if n := deps.opened.Load(); n != 0 {
		t.Errorf("bridges opened = %d, want none during a known outage", n)
	}
	if n := reach.gates.Load(); n != 1 {
		t.Errorf("sign-in gate asked %d times, want once for one prompt", n)
	}
	if len(deps.abandoned) != 1 {
		t.Fatalf("closed turns = %+v, want the prompt's one turn", deps.abandoned)
	}
	got := deps.abandoned[0]
	if got.kind != marotte.FailureKindSignIn || got.stop != marotte.StopReasonInterrupted {
		t.Errorf("close = %+v, want an interrupted sign_in failure", got)
	}
	if got.reason != marotte.SignInFailedUnreachableReason {
		t.Errorf("close reason = %q, want %q", got.reason, marotte.SignInFailedUnreachableReason)
	}
	frame := deps.onlyError(t)
	if frame.Code != marotte.ErrCodeAuthTokenUnavailable || !frame.TurnScoped {
		t.Errorf("error frame = %+v, want a turn-scoped auth_token_unavailable", frame)
	}
	if n := reach.refusals.Load(); n != 0 {
		t.Errorf("refusals noted = %d, want none: the outage is not the account's", n)
	}
	if got := promptFailures(t, "signin_outage") - outage; got != 1 {
		t.Errorf("prompt_failures_total{class=signin_outage} moved by %v, want 1", got)
	}
	if got := promptFailures(t, "auth") - auth; got != 0 {
		t.Errorf("prompt_failures_total{class=auth} moved by %v, want 0: the sign-in's own alert reports the outage", got)
	}
}

// A prompt the sign-in does not block (no outage known, or the reprobe found the provider back) runs as today.
func TestPrompt_AnAnsweringProviderRunsThePrompt(t *testing.T) {
	deps := runGatedPrompt(t, &reachRecorder{})
	if n := deps.opened.Load(); n != 1 {
		t.Errorf("bridges opened = %d, want the prompt's session opened", n)
	}
	for _, a := range deps.abandoned {
		if a.kind == marotte.FailureKindSignIn {
			t.Errorf("turn closed %+v, want no sign-in failure", a)
		}
	}
}
