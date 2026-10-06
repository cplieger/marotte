package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"pgregory.net/rapid"
)

func TestUtilityBridge_LazyStart(t *testing.T) {
	h, _, br := newTestHub()

	h.lifecycle.mu.Lock()
	if u := h.utility.peek(); u != nil && u.session.started {
		t.Error("utility bridge started before first call")
	}
	h.lifecycle.mu.Unlock()

	// No chunks: the drain exits on the idle timer, since the response ends the turn.
	_, err := h.UtilityPrompt(t.Context(), "test prompt", "")
	if err != nil {
		t.Fatalf("UtilityPrompt error = %v", err)
	}
	_ = br // notifCh unused in this test

	h.lifecycle.mu.Lock()
	if u := h.utility.peek(); u == nil || !u.session.started {
		t.Error("utility bridge not started after first call")
	}
	h.lifecycle.mu.Unlock()
}

func TestUtilityBridge_DrainCollectsChunks(t *testing.T) {
	h, _, br := newTestHub()

	// Chunks arrive in response to the prompt Call, so UtilityPrompt's at-start drain cannot eat them.
	br.chunksOnCall = map[string][]string{marotte.MethodPrompt: {"hello ", "world"}}

	result, err := h.UtilityPrompt(t.Context(), "test", "")
	if err != nil {
		t.Fatalf("UtilityPrompt error = %v", err)
	}
	if result != "hello world" {
		t.Errorf("result = %q, want %q", result, "hello world")
	}
}

func TestUtilityBridge_StopAndRestart(t *testing.T) {
	h, _, _ := newTestHub()

	s := &utilitySession{shutdownCtx: t.Context(), started: true, bridge: newFakeBridge()}
	h.lifecycle.mu.Lock()
	h.utility = &utilityLease{rt: &utilityRuntime{session: s, textgen: newUtilityAgent(s)}}
	h.lifecycle.mu.Unlock()

	h.stopUtilityBridge()

	h.lifecycle.mu.Lock()
	if h.utility.peek() != nil {
		t.Error("utility bridge not nil after stop")
	}
	h.lifecycle.mu.Unlock()
}

// agentForDrainTest builds an agent over a preset started session for exercising drainResponse.
// context.Background(): no *testing.T is in scope.
func agentForDrainTest(bridge ACPBridge) *utilityAgent {
	s := &utilitySession{shutdownCtx: context.Background(), bridge: bridge, started: true, gen: 1}
	return newUtilityAgent(s)
}

func TestDrainUtilityResponse_NilResponse(t *testing.T) {
	_, _, _ = newTestHub()
	ua := agentForDrainTest(newFakeBridge())
	_, err := ua.drainResponse(t.Context(), sessionLease{gen: 1}, nil)
	if err == nil {
		t.Error("expected error for nil response")
	}
}

func TestDrainUtilityResponse_ChannelClose(t *testing.T) {
	_, _, _ = newTestHub()
	ua := agentForDrainTest(newFakeBridge())

	// A closed channel ends the drain with what was collected, as a mid-drain cull does.
	chunks := make(chan utilityChunkPayload)
	close(chunks)

	resp := &marotte.RPCResponse{Result: json.RawMessage(`{}`)}
	result, err := ua.drainResponse(t.Context(), sessionLease{gen: 1, chunks: chunks}, resp)
	if err != nil {
		t.Fatalf("drainResponse on closed channel: %v", err)
	}
	if result != "" {
		t.Errorf("result = %q, want empty", result)
	}
}

func BenchmarkUtilityBridge_DrainResponse(b *testing.B) {
	chunk := func(text string) *marotte.RPCResponse {
		return newChunkMsg(text)
	}

	payload := "x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]" // 50 bytes

	for _, n := range []int{5, 20, 100, 500, 1000} {
		b.Run(fmt.Sprintf("chunks=%d", n), func(b *testing.B) {
			msgs := make([]*marotte.RPCResponse, n)
			for i := range msgs {
				msgs[i] = chunk(payload)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				ch := make(chan utilityChunkPayload, n)
				for _, m := range msgs {
					var c utilityChunkPayload
					_ = json.Unmarshal(m.Params, &c)
					ch <- c
				}
				close(ch)
				ua := agentForDrainTest(newFakeBridge())

				resp := &marotte.RPCResponse{Result: json.RawMessage(`{}`)}
				result, _ := ua.drainResponse(b.Context(), sessionLease{gen: 1, chunks: ch}, resp)
				_ = result
			}
		})
	}
}

func TestUtilityBridge_ConcurrentPrompts(t *testing.T) {
	h, _, br := newTestHub()

	// Pre-start the bridge so concurrent calls do not race on start.
	_, _ = h.UtilityPrompt(t.Context(), "warmup", "")

	freshBr := newFakeBridge()
	u := h.utility.peek()
	u.session.mu.Lock()
	u.session.bridge = freshBr
	u.session.mu.Unlock()
	u.textgen.turnMu.Lock()
	u.textgen.promptCount = 0
	u.textgen.turnMu.Unlock()
	_ = br

	const goroutines = 5
	var wg sync.WaitGroup
	results := make([]string, goroutines)
	errs := make([]error, goroutines)

	// Built on the test goroutine: newChunkMsg can t.Fatalf, which off it ends the wrong goroutine.
	frames := make([]*marotte.RPCResponse, goroutines)
	for i := range frames {
		frames[i] = newSessionChunkMsg(string(freshBr.SessionID()), fmt.Sprintf("resp-%d", i))
	}
	go func() {
		for i := range goroutines {
			// Paced so each chunk lands inside a drain window (20ms against the 50ms idle debounce).
			time.Sleep(20 * time.Millisecond)
			freshBr.mu.Lock()
			stopped := freshBr.stopped
			freshBr.mu.Unlock()
			if stopped {
				return
			}
			freshBr.deliver(frames[i])
		}
	}()

	for i := range goroutines {
		wg.Go(func() {
			results[i], errs[i] = h.UtilityPrompt(t.Context(), fmt.Sprintf("prompt-%d", i), "")
		})
	}

	wg.Wait()

	for i := range goroutines {
		if errs[i] != nil {
			t.Logf("goroutine %d error: %v", i, errs[i])
		}
	}

	// Exactly one serialized session/prompt call per goroutine.
	freshBr.mu.Lock()
	promptCalls := 0
	for _, c := range freshBr.calls {
		if c == "session/prompt" {
			promptCalls++
		}
	}
	freshBr.mu.Unlock()

	if promptCalls != goroutines {
		t.Errorf("expected %d session/prompt calls, got %d", goroutines, promptCalls)
	}
}

func TestCheapestModel_RapidInvariants(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(0, 20).Draw(rt, "catalogSize")
		catalog := make([]marotte.SessionModel, n)
		for i := range n {
			catalog[i] = marotte.SessionModel{
				ID:             rapid.StringMatching(`[a-z0-9-]{1,20}`).Draw(rt, fmt.Sprintf("id_%d", i)),
				Name:           rapid.String().Draw(rt, fmt.Sprintf("name_%d", i)),
				Description:    rapid.String().Draw(rt, fmt.Sprintf("desc_%d", i)),
				RateMultiplier: rapid.Float64Range(0, 10).Draw(rt, fmt.Sprintf("rate_%d", i)),
			}
		}

		ctx := t.Context()
		result := cheapestModel(ctx, catalog)

		if result == "" {
			return
		}

		var found bool
		for _, m := range catalog {
			if m.ID == result {
				found = true
				if m.ID == "auto" {
					rt.Fatal("selected 'auto' model")
				}
				if modelExcluded(m.Name) || modelExcluded(m.Description) {
					rt.Fatalf("selected excluded model %q", m.ID)
				}
				break
			}
		}
		if !found {
			rt.Fatalf("result %q not in catalog", result)
		}
	})
}

// newTestUtilityRuntime builds a utility runtime with an empty model catalog whose factory hands out a fresh
// fakeBridge per call, so a recycle visibly swaps it. context.Background(): no *testing.T is in scope.
func newTestUtilityRuntime() *utilityRuntime {
	return newUtilityRuntime(
		context.Background(),
		func() ACPBridge { return newFakeBridge() },
		func() []marotte.SessionModel { return nil },
		&utilitySessionHooks{},
		nil, // secrets: no credential store in tests
		false,
	)
}

// presetStartedSession marks the session started on br at generation 1 and syncs the agent's counters, so a test
// can preset counters without the resync zeroing them.
func presetStartedSession(u *utilityRuntime, bridge ACPBridge) {
	u.session.bridge = bridge
	u.session.started = true
	u.session.gen = 1
	u.textgen.counterGen = 1
}

// At the prompt cap the next UtilityPrompt recycles: the old process stops, a new generation starts, the counter
// resyncs to zero and lands at 1.
func TestUtilityPrompt_RecyclesAtPromptCap(t *testing.T) {
	u := newTestUtilityRuntime()
	br0 := newFakeBridge()
	presetStartedSession(u, br0)
	u.textgen.promptCount = maxUtilityPrompts // exact boundary
	defer u.session.Stop()

	if _, err := u.textgen.UtilityPrompt(t.Context(), "p", ""); err != nil {
		t.Fatalf("UtilityPrompt error = %v, want nil", err)
	}

	if u.session.bridge == br0 {
		t.Errorf("UtilityPrompt at the prompt cap did not recycle the bridge")
	}
	if u.textgen.promptCount != 1 {
		t.Errorf("promptCount after recycle = %d, want 1", u.textgen.promptCount)
	}
}

// A fresh runtime skips the recycle branch; one prompt leaves the counter at 1.
func TestUtilityPrompt_IncrementsPromptCount(t *testing.T) {
	u := newTestUtilityRuntime()
	defer u.session.Stop()

	if _, err := u.textgen.UtilityPrompt(t.Context(), "p", ""); err != nil {
		t.Fatalf("UtilityPrompt error = %v, want nil", err)
	}

	if u.textgen.promptCount != 1 {
		t.Errorf("promptCount after one prompt = %d, want 1", u.textgen.promptCount)
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
	responseCh := make(chan utilityChunkPayload, 4)
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

	go us.forward(newFakeBridge(), testFwdGen, notifCh, responseCh, done)
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
	responseCh := make(chan utilityChunkPayload, 1)
	done := make(chan struct{})
	var calls atomic.Int32
	us := &utilitySession{hooks: utilitySessionHooks{
		onRecipesChanged: func() { calls.Add(1) },
	}}

	go us.forward(newFakeBridge(), testFwdGen, notifCh, responseCh, done)
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

// TestForwardChunk_ForwardsAssistantText pins that a real KAS frame reaches the channel intact. The kind sits on
// the update object, so a flattened fixture tests nothing.
func TestForwardChunk_ForwardsAssistantText(t *testing.T) {
	ch := make(chan utilityChunkPayload, 4)
	forwardChunk(newSessionChunkMsg("sess-utility", "feat/branch-name"), "sess-utility", ch, nil)
	select {
	case got := <-ch:
		if got.Content.Text != "feat/branch-name" {
			t.Errorf("forwardChunk text = %q, want %q", got.Content.Text, "feat/branch-name")
		}
	default:
		t.Fatal("forwardChunk forwarded nothing; an agent_message_chunk must reach responseCh")
	}
}

// TestForwardChunk_IgnoresOtherKinds pins that only agent_message_chunk is assistant text; a tool_call would leak into a
// generated commit message.
func TestForwardChunk_IgnoresOtherKinds(t *testing.T) {
	ch := make(chan utilityChunkPayload, 4)
	forwardChunk(newToolCallMsg(t, "tc-1", "readFile", "pending"), "", ch, nil)
	if len(ch) != 0 {
		t.Errorf("responseCh len = %d, want 0 (only agent_message_chunk is text)", len(ch))
	}
}

// TestForwardChunk_NonBlockingDropsWhenFull pins that a blocking send parks the forward goroutine, which then never sees
// notifCh close and deadlocks reset()'s <-forwardDone.
func TestForwardChunk_NonBlockingDropsWhenFull(t *testing.T) {
	ch := make(chan utilityChunkPayload, 1)
	ch <- utilityChunkPayload{}

	msg := newChunkMsg("dropped")

	done := make(chan struct{})
	go func() {
		forwardChunk(msg, "", ch, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("forwardChunk blocked on a full responseCh; the send must be non-blocking")
	}
	if len(ch) != 1 {
		t.Errorf("responseCh len = %d, want 1 (the overflow chunk must be dropped)", len(ch))
	}
}

// TestForwardChunk_DropsAForeignSessionsChunk pins that KAS can hydrate a chat's session inside the utility process, and
// adopting its text puts another chat's output in a commit message or branch name.
func TestForwardChunk_DropsAForeignSessionsChunk(t *testing.T) {
	ch := make(chan utilityChunkPayload, 4)

	forwardChunk(newSessionChunkMsg("sess-somebody-elses-chat", "rm -rf the wrong thing"), "sess-utility", ch, nil)

	if len(ch) != 0 {
		got := <-ch
		t.Errorf("a foreign session's chunk reached responseCh as %q; it can land in a commit message", got.Content.Text)
	}
}

// TestForwardChunk_AdmitsItsOwnAndThePreSessionWindow pins that frames before session/new answers must pass, or every
// result comes back empty. The window cannot contaminate: a drain needs a prompt, which needs the session id.
func TestForwardChunk_AdmitsItsOwnAndThePreSessionWindow(t *testing.T) {
	tests := map[string]struct {
		frameSession string
		ownSession   string
	}{
		"its_own_session":            {frameSession: "sess-utility", ownSession: "sess-utility"},
		"before_its_own_id_is_known": {frameSession: "sess-utility", ownSession: ""},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ch := make(chan utilityChunkPayload, 4)

			forwardChunk(newSessionChunkMsg(tc.frameSession, "chore: bump the pin"), tc.ownSession, ch, nil)

			select {
			case got := <-ch:
				if got.Content.Text != "chore: bump the pin" {
					t.Errorf("forwardChunk(frame %q, own %q) text = %q, want %q",
						tc.frameSession, tc.ownSession, got.Content.Text, "chore: bump the pin")
				}
			default:
				t.Errorf("forwardChunk(frame %q, own %q) forwarded nothing, so every utility result would be empty",
					tc.frameSession, tc.ownSession)
			}
		})
	}
}

// TestUtilityPrompt_DrainsStaleResponseChAtStart pins that a chunk a prior turn left in responseCh is drained
// before the next Call.
func TestUtilityPrompt_DrainsStaleResponseChAtStart(t *testing.T) {
	u := newTestUtilityRuntime()
	defer u.session.Stop()
	ctx := t.Context()

	if _, err := u.textgen.UtilityPrompt(ctx, "warmup", ""); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	var stale utilityChunkPayload
	stale.Content.Text = "STALE"
	u.session.responseCh <- stale

	// No chunks delivered: empty, because the stale chunk was drained at the top.
	got, err := u.textgen.UtilityPrompt(ctx, "real", "")
	if err != nil {
		t.Fatalf("UtilityPrompt: %v", err)
	}
	if got != "" {
		t.Errorf("result = %q, want empty; stale chunk bled into this task's output", got)
	}
}

// TestUtilityAgent_PromptCountResetOnRestart pins that the cull stops the session without resetting counters, and the
// restart's new generation makes syncCounters zero them.
func TestUtilityAgent_PromptCountResetOnRestart(t *testing.T) {
	u := newTestUtilityRuntime()
	defer u.session.Stop()
	ctx := t.Context()

	if _, err := u.textgen.UtilityPrompt(ctx, "p1", ""); err != nil {
		t.Fatalf("p1: %v", err)
	}
	if u.textgen.promptCount != 1 {
		t.Fatalf("promptCount after p1 = %d, want 1", u.textgen.promptCount)
	}

	if !u.session.stopIfIdle(time.Now().Add(time.Minute)) {
		t.Fatal("stopIfIdle did not stop the just-active session with a future cutoff")
	}

	// The restart's new generation zeroes the count, so it lands at 1, not 2.
	if _, err := u.textgen.UtilityPrompt(ctx, "p2", ""); err != nil {
		t.Fatalf("p2: %v", err)
	}
	if u.textgen.promptCount != 1 {
		t.Errorf("promptCount after cull+restart = %d, want 1 (generation resync must zero it)", u.textgen.promptCount)
	}
}

// TestAccountUsage_CallHasTimeout pins that a wedged getUsage without a deadline holds the utility mutex indefinitely.
func TestAccountUsage_CallHasTimeout(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroGetUsage: json.RawMessage(`{"success":true,"message":"managed by admin"}`),
	}
	if _, err := h.AccountUsage(t.Context()); err != nil {
		t.Fatalf("AccountUsage: %v", err)
	}
	if !br.callHadDeadline(methodKiroGetUsage) {
		t.Error("account usage Call ran without a deadline; a wedged getUsage would hold the utility mutex forever")
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
	if _, err := u.textgen.UtilityPrompt(t.Context(), "warm", ""); err != nil {
		t.Fatalf("warm: %v", err)
	}
	victim, ok := u.session.bridge.(*fakeBridge)
	if !ok {
		t.Fatal("utility bridge is not a *fakeBridge")
	}
	u.session.mu.Lock()
	u.session.lastActiveAt = time.Now().Add(-bridgeIdleTimeout - time.Minute)
	u.session.mu.Unlock()

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
	u.session.mu.Lock()
	started := u.session.started
	u.session.mu.Unlock()
	if started {
		t.Error("cull did not mark the utility session stopped")
	}
}

// TestStopUtilityBridge_ConcurrentWithCull_NoRace pins that both sides coordinate on h.lifecycle.mu for the utility slot;
// -race catches a regression.
func TestStopUtilityBridge_ConcurrentWithCull_NoRace(t *testing.T) {
	h, _, _ := newTestHub()
	u := h.utility.get()
	if _, err := u.textgen.UtilityPrompt(t.Context(), "warm", ""); err != nil {
		t.Fatalf("warm: %v", err)
	}
	u.session.mu.Lock()
	u.session.lastActiveAt = time.Now().Add(-bridgeIdleTimeout - time.Minute)
	u.session.mu.Unlock()

	var wg sync.WaitGroup
	wg.Go(h.cullIdleUtilityBridgeOnce)
	wg.Go(h.stopUtilityBridge)
	wg.Wait()
}

// countCalls returns how many times the fake bridge received method.
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

// At the byte budget the next UtilityPrompt recycles even far below the prompt cap.
func TestUtilityPrompt_RecyclesAtByteBudget(t *testing.T) {
	u := newTestUtilityRuntime()
	br0 := newFakeBridge()
	presetStartedSession(u, br0)
	u.textgen.promptCount = 3
	u.textgen.promptBytes = maxUtilityPromptBytes // exact boundary
	defer u.session.Stop()

	if _, err := u.textgen.UtilityPrompt(t.Context(), "p", ""); err != nil {
		t.Fatalf("UtilityPrompt error = %v, want nil", err)
	}

	if u.session.bridge == br0 {
		t.Errorf("UtilityPrompt at the byte budget did not recycle the bridge")
	}
	if u.textgen.promptBytes != 1 { // len("p") accumulated on the fresh session
		t.Errorf("promptBytes after recycle = %d, want 1", u.textgen.promptBytes)
	}
}

// Effort: a new level issues one effortLevel set_config_option, the same level none.
func TestUtilityPrompt_AppliesEffortPerTask(t *testing.T) {
	br := newFakeBridge()
	u := newUtilityRuntime(
		t.Context(),
		func() ACPBridge { return br },
		func() []marotte.SessionModel { return nil },
		&utilitySessionHooks{},
		nil, // secrets: no credential store in tests
		false,
	)
	defer u.session.Stop()

	for _, effort := range []marotte.EffortLevel{marotte.EffortLow, marotte.EffortLow, marotte.EffortMedium} {
		if _, err := u.textgen.UtilityPrompt(t.Context(), "p", effort); err != nil {
			t.Fatalf("UtilityPrompt(%s) error = %v, want nil", effort, err)
		}
	}

	if n := countCalls(br, marotte.MethodSetConfigOption); n != 2 {
		t.Errorf("set_config_option calls = %d, want 2 (low once, medium once)", n)
	}
	p := br.paramsFor(marotte.MethodSetConfigOption)
	if p["configId"] != marotte.ConfigOptionEffort || p["value"] != string(marotte.EffortMedium) {
		t.Errorf("last set_config_option params = %v, want configId=%s value=%s", p, marotte.ConfigOptionEffort, marotte.EffortMedium)
	}
	if u.textgen.currentEffort != marotte.EffortMedium {
		t.Errorf("currentEffort = %q, want %q", u.textgen.currentEffort, marotte.EffortMedium)
	}
}

// A failed effortLevel set_config_option latches effortUnsupported: the prompt succeeds and later tasks skip the
// call until the next session start.
func TestUtilityPrompt_EffortUnsupportedLatches(t *testing.T) {
	br := newFakeBridge()
	br.callErrs = map[string]error{marotte.MethodSetConfigOption: fmt.Errorf("no such config option")}
	u := newUtilityRuntime(
		t.Context(),
		func() ACPBridge { return br },
		func() []marotte.SessionModel { return nil },
		&utilitySessionHooks{},
		nil, // secrets: no credential store in tests
		false,
	)
	defer u.session.Stop()

	for range 2 {
		if _, err := u.textgen.UtilityPrompt(t.Context(), "p", marotte.EffortMedium); err != nil {
			t.Fatalf("UtilityPrompt error = %v, want nil (effort failure must not fail the task)", err)
		}
	}

	if !u.textgen.effortUnsupported {
		t.Error("effortUnsupported not latched after a failed set_config_option")
	}
	if n := countCalls(br, marotte.MethodSetConfigOption); n != 1 {
		t.Errorf("set_config_option calls = %d, want 1 (no retry after the latch)", n)
	}
}

// The utility session refuses tool-use requests so none can wedge the turn until the 60s ceiling: permissions get a
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
	u := newTestUtilityRuntime()
	if got := u.session.liveID(); got != "" {
		t.Errorf("liveID before start = %q, want empty", got)
	}
	if _, err := u.textgen.UtilityPrompt(t.Context(), "p", ""); err != nil {
		t.Fatalf("UtilityPrompt error = %v", err)
	}
	if got := u.session.liveID(); got == "" {
		t.Error("liveID after start = empty, want the fake session id")
	}
	u.session.Stop()
	if got := u.session.liveID(); got != "" {
		t.Errorf("liveID after Stop = %q, want empty", got)
	}
}

// TestRPCReadsDoNotQueueBehindTextTurn pins that the session's stateless RPC reads complete while a text turn is in
// flight on the same session.
func TestRPCReadsDoNotQueueBehindTextTurn(t *testing.T) {
	br := newFakeBridge()
	release := make(chan struct{})
	br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: release}
	u := newUtilityRuntime(
		t.Context(),
		func() ACPBridge { return br },
		func() []marotte.SessionModel { return nil },
		&utilitySessionHooks{},
		nil, // secrets: no credential store in tests
		false,
	)
	defer u.session.Stop()

	turnDone := make(chan struct{})
	go func() {
		defer close(turnDone)
		_, _ = u.textgen.UtilityPrompt(t.Context(), "slow", "")
	}()
	deadline := time.Now().Add(2 * time.Second)
	for countCalls(br, marotte.MethodPrompt) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("prompt Call never started")
		}
		time.Sleep(time.Millisecond)
	}

	rpcCtx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if _, err := u.session.accountUsageRaw(rpcCtx); err != nil {
		t.Fatalf("accountUsageRaw during an in-flight turn: %v", err)
	}
	select {
	case <-turnDone:
		t.Fatal("text turn finished before the RPC asserted concurrency; test is vacuous")
	default:
	}

	close(release)
	<-turnDone
}

// The cutoff is now minus the idle timeout; a sign error would stop the live bridge every tick.
func TestCullIdleUtilityBridgeOnce_LeavesARecentlyActiveBridgeAlone(t *testing.T) {
	h, _, _ := newTestHub()
	u := h.utility.get()
	if _, err := u.textgen.UtilityPrompt(t.Context(), "warm", ""); err != nil {
		t.Fatalf("warm: %v", err)
	}
	live, ok := u.session.bridge.(*fakeBridge)
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
	u.session.mu.Lock()
	started := u.session.started
	u.session.mu.Unlock()
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
