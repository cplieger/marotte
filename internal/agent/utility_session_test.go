package agent

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

func TestUtilityBridge_LazyStart(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroGetUsage: json.RawMessage(`{"success":true,"message":"ok"}`),
	}

	if u := h.utility.peek(); u != nil && sessionStarted(u) {
		t.Error("utility bridge started before first call")
	}

	if _, err := h.AccountUsage(t.Context()); err != nil {
		t.Fatalf("AccountUsage: %v", err)
	}

	if u := h.utility.peek(); u == nil || !sessionStarted(u) {
		t.Error("utility bridge not started after first call")
	}
}

func sessionStarted(s *utilitySession) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started
}

func TestUtilityBridge_StopAndRestart(t *testing.T) {
	h, _, _ := newTestHub()

	s := &utilitySession{shutdownCtx: t.Context(), started: true, bridge: newFakeBridge()}
	h.utility = &utilityLease{session: s}

	h.stopUtilityBridge()

	if h.utility.peek() != nil {
		t.Error("utility bridge not nil after stop")
	}
}

// TestAnswerUtilityHostRequest pins that the utility bridge answers the shell type request.
func TestAnswerUtilityHostRequest(t *testing.T) {
	t.Run("shell_type answered with bash", func(t *testing.T) {
		rb := newRespondingBridge()
		id := int64(7)
		(&utilitySession{}).answerHostRequest(rb, &marotte.RPCResponse{ID: &id, Method: methodKiroShellType})
		rb.respMu.Lock()
		defer rb.respMu.Unlock()
		if rb.response.id != id {
			t.Fatalf("shell_type request not answered: got id %d, want %d", rb.response.id, id)
		}
		m, ok := rb.response.result.(map[string]any)
		if !ok || m["shellType"] != "bash" {
			t.Errorf("shell_type result = %v, want map{shellType: bash}", rb.response.result)
		}
	})
}

// TestForward_RoutesPolicyNotifications pins that _kiro/policy/{changed,error} reach the utility session's hook.
// With no chat bridge alive this is the only path to Settings, Permissions; without it the switch paints itself
// back off.
func TestForward_RoutesPolicyNotifications(t *testing.T) {
	notifCh := make(chan marotte.Notification, 2)
	done := make(chan struct{})

	var mu sync.Mutex
	var seen []string
	us := &utilitySession{hooks: utilitySessionHooks{
		onPolicyNotification: func(msg *marotte.RPCResponse) {
			mu.Lock()
			defer mu.Unlock()
			seen = append(seen, msg.Method)
		},
	}}

	go us.forward(newFakeBridge(), testFwdGen, notifCh, done)
	notifCh <- marotte.Notification{Msg: &marotte.RPCResponse{Method: methodV3PolicyChanged}, Seq: 1}
	notifCh <- marotte.Notification{Msg: &marotte.RPCResponse{Method: methodV3PolicyError}, Seq: 2}
	close(notifCh)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("forward did not exit after notifCh closed")
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{methodV3PolicyChanged, methodV3PolicyError}
	if !slices.Equal(seen, want) {
		t.Errorf("hook saw %v, want %v", seen, want)
	}
}

// TestForward_RecipesChangedBroadcastsAnInvalidation pins that the drop arm must not swallow _kiro/workflow/recipes_changed.
func TestForward_RecipesChangedBroadcastsAnInvalidation(t *testing.T) {
	notifCh := make(chan marotte.Notification, 1)
	done := make(chan struct{})
	var calls atomic.Int32
	us := &utilitySession{hooks: utilitySessionHooks{
		onRecipesChanged: func() { calls.Add(1) },
	}}

	go us.forward(newFakeBridge(), testFwdGen, notifCh, done)
	notifCh <- marotte.Notification{Msg: &marotte.RPCResponse{Method: methodKiroWorkflowRecipesChanged}, Seq: 1}
	close(notifCh)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("forward did not exit after notifCh closed")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("onRecipesChanged called %d times, want 1", got)
	}
}

// newTestUtilitySession builds a stopped utility session whose factory hands out a fresh fakeBridge per start.
// context.Background(): no *testing.T is in scope.
func newTestUtilitySession() *utilitySession {
	return newUtilitySession(
		context.Background(),
		func() ACPBridge { return newFakeBridge() },
		&utilitySessionHooks{},
		nil, // secrets: no credential store in tests
		false,
	)
}

// offered records what offerForeign handed its reader, consuming every frame.
type offered struct{ sessions []string }

func (o *offered) hook(sessionID string, _ marotte.ACPUpdateKind, _ json.RawMessage) bool {
	o.sessions = append(o.sessions, sessionID)
	return true
}

// KAS can hydrate a chat's or a step's session inside the utility process: its frames go to their reader.
func TestOfferForeign_HandsAForeignSessionsFrameToItsReader(t *testing.T) {
	var got offered
	offerForeign(newSessionChunkMsg("sess-step", "the step's reply"), "sess-utility", got.hook)
	if !slices.Equal(got.sessions, []string{"sess-step"}) {
		t.Errorf("offered %v, want [sess-step]", got.sessions)
	}
}

// Its own frames and the window before session/new names it are no reader's: offering them would put the utility
// session's text in a step transcript.
func TestOfferForeign_KeepsItsOwnAndThePreSessionWindow(t *testing.T) {
	tests := map[string]struct {
		frameSession string
		ownSession   string
	}{
		"its_own_session":            {frameSession: "sess-utility", ownSession: "sess-utility"},
		"before_its_own_id_is_known": {frameSession: "sess-step", ownSession: ""},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var got offered
			offerForeign(newSessionChunkMsg(tc.frameSession, "text"), tc.ownSession, got.hook)
			if len(got.sessions) != 0 {
				t.Errorf("offerForeign(frame %q, own %q) offered %v, want nothing", tc.frameSession, tc.ownSession, got.sessions)
			}
		})
	}
}

// An update whose kind cannot be decoded goes to nobody: consuming it would hide a wire shape change.
func TestOfferForeign_WithholdsAnUndecodableUpdate(t *testing.T) {
	var got offered
	msg := &marotte.RPCResponse{
		Method: marotte.MethodSessionUpdate,
		Params: json.RawMessage(`{"sessionId":"sess-step","update":"not an object"}`),
	}
	offerForeign(msg, "sess-utility", got.hook)
	if len(got.sessions) != 0 {
		t.Errorf("offered %v for an undecodable update, want nothing", got.sessions)
	}
}

// TestAccountUsage_CallHasTimeout pins a deadline on getUsage: without one a wedged answer hangs the request for good.
func TestAccountUsage_CallHasTimeout(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroGetUsage: json.RawMessage(`{"success":true,"message":"managed by admin"}`),
	}
	if _, err := h.AccountUsage(t.Context()); err != nil {
		t.Fatalf("AccountUsage: %v", err)
	}
	if !br.callHadDeadline(methodKiroGetUsage) {
		t.Error("account usage Call ran without a deadline; a wedged getUsage would hang the request forever")
	}
}

// TestPolicyList_CallHasTimeout pins the same deadline for PolicyList.
func TestPolicyList_CallHasTimeout(t *testing.T) {
	h, _, br := newTestHub()
	seedPolicy(br, `{"rules":[]}`, `{}`)
	if _, err := h.config.PolicyList(t.Context(), ""); err != nil {
		t.Fatalf("PolicyList: %v", err)
	}
	if !br.callHadDeadline(methodV3PermissionsList) {
		t.Error("permissions/list Call ran without a deadline")
	}
}

// TestPolicyExplain_CallHasTimeout pins the same deadline for PolicyExplain.
func TestPolicyExplain_CallHasTimeout(t *testing.T) {
	h, _, br := newTestHub()
	seedPolicy(br, `{}`, `{"capability":"fs_write","effect":"ask"}`)
	if _, err := h.config.PolicyExplain(t.Context(), marotte.PolicyExplainRequest{Capability: "fs_write"}); err != nil {
		t.Fatalf("PolicyExplain: %v", err)
	}
	if !br.callHadDeadline(methodV3PermissionsExplain) {
		t.Error("permissions/explain Call ran without a deadline")
	}
}

// TestCullIdleUtilityBridgeOnce_StopsIdleUtilityBridge pins that the sweep stops the exact bridge it captured under
// the session mutex. Only the utility session has an idle timer.
func TestCullIdleUtilityBridgeOnce_StopsIdleUtilityBridge(t *testing.T) {
	h, _, _ := newTestHub()
	u := h.utility.get()
	if err := u.ensureStarted(t.Context()); err != nil {
		t.Fatalf("warm: %v", err)
	}
	victim, ok := u.bridge.(*fakeBridge)
	if !ok {
		t.Fatal("utility bridge is not a *fakeBridge")
	}
	u.mu.Lock()
	u.lastActiveAt = time.Now().Add(-bridgeIdleTimeout - time.Minute)
	u.mu.Unlock()

	h.cullIdleUtilityBridgeOnce()

	// The cull stops its victim in a goroutine.
	deadline := time.Now().Add(2 * time.Second)
	stopped := false
	for time.Now().Before(deadline) {
		victim.mu.Lock()
		stopped = victim.stopped
		victim.mu.Unlock()
		if stopped {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !stopped {
		t.Error("cull did not stop the idle utility bridge")
	}
	u.mu.Lock()
	started := u.started
	u.mu.Unlock()
	if started {
		t.Error("cull did not mark the utility session stopped")
	}
}

// TestStopUtilityBridge_ConcurrentWithCull_NoRace pins that both sides coordinate on the utility lease's lock for the
// slot; -race catches a regression.
func TestStopUtilityBridge_ConcurrentWithCull_NoRace(t *testing.T) {
	h, _, _ := newTestHub()
	u := h.utility.get()
	if err := u.ensureStarted(t.Context()); err != nil {
		t.Fatalf("warm: %v", err)
	}
	u.mu.Lock()
	u.lastActiveAt = time.Now().Add(-bridgeIdleTimeout - time.Minute)
	u.mu.Unlock()

	var wg sync.WaitGroup
	wg.Go(h.cullIdleUtilityBridgeOnce)
	wg.Go(h.stopUtilityBridge)
	wg.Wait()
}

func countCalls(b *fakeBridge, method string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, m := range b.calls {
		if m == method {
			n++
		}
	}
	return n
}

// The utility session refuses tool-use requests rather than leaving them pending: permissions get a
// cancelled outcome, fs, terminal and unknown requests an error.
func TestAnswerHostRequest_DeniesToolRequests(t *testing.T) {
	cases := []struct {
		method    string
		wantErr   bool // error response vs result response
		cancelled bool // result carries outcome=cancelled
	}{
		{method: marotte.MethodRequestPermission, cancelled: true},
		{method: marotte.MethodFSRead, wantErr: true},
		{method: marotte.MethodFSWrite, wantErr: true},
		{method: "terminal/create", wantErr: true},
		{method: "_kiro/auth/get" + "AccessToken", wantErr: true},
		{method: "_kiro/some/future_request", wantErr: true},
		// executeHook runs a shell command a hook file names; it must reach the default refusal like any capability marotte
		// does not offer.
		{method: "_kiro/hooks/executeHook", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			rb := newRespondingBridge()
			id := int64(11)
			(&utilitySession{}).answerHostRequest(rb, &marotte.RPCResponse{ID: &id, Method: tc.method})
			rb.respMu.Lock()
			defer rb.respMu.Unlock()
			if rb.response.id != id {
				t.Fatalf("request %s not answered", tc.method)
			}
			if tc.wantErr {
				if rb.response.err == nil {
					t.Fatalf("%s: want error response, got result %v", tc.method, rb.response.result)
				}
				return
			}
			if rb.response.err != nil {
				t.Fatalf("%s: want result response, got error %v", tc.method, rb.response.err)
			}
			if tc.cancelled {
				o, ok := rb.response.result.(*marotte.PermissionOutcome)
				if !ok {
					t.Fatalf("%s: result type %T, want *marotte.PermissionOutcome", tc.method, rb.response.result)
				}
				if o.Outcome.Outcome != "cancelled" {
					t.Fatalf("%s: result = %+v, want outcome.outcome=cancelled", tc.method, o)
				}
			}
		})
	}
}

// The live utility session id is exempt from the orphan-session sweep; a stopped or absent one contributes nothing.
func TestUtilityLiveSessionID(t *testing.T) {
	u := newTestUtilitySession()
	if got := u.liveID(); got != "" {
		t.Errorf("liveID before start = %q, want empty", got)
	}
	if err := u.ensureStarted(t.Context()); err != nil {
		t.Fatalf("ensureStarted: %v", err)
	}
	if got := u.liveID(); got == "" {
		t.Error("liveID after start = empty, want the fake session id")
	}
	u.Stop()
	if got := u.liveID(); got != "" {
		t.Errorf("liveID after Stop = %q, want empty", got)
	}
}

// The cutoff is now minus the idle timeout; a sign error would stop the live bridge every tick.
func TestCullIdleUtilityBridgeOnce_LeavesARecentlyActiveBridgeAlone(t *testing.T) {
	h, _, _ := newTestHub()
	u := h.utility.get()
	if err := u.ensureStarted(t.Context()); err != nil {
		t.Fatalf("warm: %v", err)
	}
	live, ok := u.bridge.(*fakeBridge)
	if !ok {
		t.Fatal("utility bridge is not a *fakeBridge")
	}

	h.cullIdleUtilityBridgeOnce()

	// The cull stops its victim in a goroutine, so allow the idle-cull test's window before concluding.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		live.mu.Lock()
		stopped := live.stopped
		live.mu.Unlock()
		if stopped {
			t.Fatal("the sweep stopped a bridge used a moment ago; its idle cutoff is in the future")
		}
		time.Sleep(2 * time.Millisecond)
	}
	u.mu.Lock()
	started := u.started
	u.mu.Unlock()
	if !started {
		t.Error("session.started = false after a sweep of a recently-active session, want true")
	}
}

// TestUtilityBridge_DeclaresSecretStorageOnlyWhenThisProcessHoldsAStore pins that KAS asks a client that declared the
// capability to persist every credential, so declaring it without a store drops them and withholding it with one
// fails the same way. The store opens best-effort, so this is a runtime question.
func TestUtilityBridge_DeclaresSecretStorageOnlyWhenThisProcessHoldsAStore(t *testing.T) {
	startedUtilityBridge := func(t *testing.T, opts ...Option) *fakeBridge {
		t.Helper()
		cs := newTestChatStore()
		br := newFakeBridge()
		h := New(context.Background(), t.TempDir(), func() ACPBridge { return br }, cs, opts...)
		cs.wire(h)
		br.callResults = map[string]json.RawMessage{
			methodKiroGetUsage: json.RawMessage(`{"success":true,"message":"ok"}`),
		}
		// Any utility call starts the session; usage is the cheapest.
		if _, err := h.AccountUsage(t.Context()); err != nil {
			t.Fatalf("AccountUsage: %v", err)
		}
		if br.lastStartOpts() == nil {
			t.Fatal("the utility bridge never started, so there is nothing to assert on")
		}
		return br
	}

	t.Run("a runtime with a credential store declares the capability", func(t *testing.T) {
		br := startedUtilityBridge(t, WithConfigDir(t.TempDir()))
		if !br.lastStartOpts().SecretStorage {
			t.Error("the utility bridge did not declare secretStorage although this process holds " +
				"a store, so KAS never asks it to persist an MCP credential and every OAuth " +
				"registration is redone on the next spawn")
		}
	})

	t.Run("a runtime without one declares it off", func(t *testing.T) {
		br := startedUtilityBridge(t)
		if br.lastStartOpts().SecretStorage {
			t.Error("the utility bridge declared secretStorage with no store behind it, so KAS " +
				"hands it credentials that go nowhere")
		}
	})
}
