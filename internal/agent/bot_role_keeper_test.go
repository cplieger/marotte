package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// TestRoleKeeper_FollowsTheDesignTable pins every cell of design §7.1 "The role keeper": a missing or wrong cell lets a
// prompt out off the role, sends a set_mode beside running work, or never lets a prompt go again.
func TestRoleKeeper_FollowsTheDesignTable(t *testing.T) {
	const (
		u = roleUnkept
		n = roleOn
		f = roleOff
		r = roleReasserting
		x = roleFailed
	)
	inputs := []roleInput{
		roleResetBot, roleResetOther, roleReportRole, roleReportOther, roleIdle, roleAccepted, roleRefused, roleCut,
	}
	want := map[roleState][]roleState{
		roleUnkept:      {f, u, u, u, u, u, u, u},
		roleOn:          {f, u, n, f, n, n, n, n},
		roleOff:         {f, u, n, f, r, f, f, f},
		roleReasserting: {f, u, r, r, r, n, x, f},
		roleFailed:      {f, u, n, x, x, x, x, x},
	}
	for from, row := range want {
		for i, in := range inputs {
			if got := roleTransitions[from][in]; got != row[i] {
				t.Errorf("state %d on input %d moves to %d, want %d", from, in, got, row[i])
			}
		}
	}
}

// keeperLife is a bare lifecycle whose keeper holds sb in state; sent answers the sessions its sender was started
// on. The sender holds until the test ends, which ends the life first so its done wakes nothing.
func keeperLife(t *testing.T, state roleState, sb *sharedBridge) (lc *chatLifecycle, sent func() []*sharedBridge) {
	t.Helper()
	var mu sync.Mutex
	var on []*sharedBridge
	hold := make(chan struct{})
	reg := &turnRegistry{
		sendRole: func(_ context.Context, _ *chatLifecycle, s *sharedBridge) {
			mu.Lock()
			on = append(on, s)
			mu.Unlock()
			<-hold
		},
		spawnRole: func(work func()) bool {
			go work()
			return true
		},
	}
	lc = &chatLifecycle{
		reg: reg, changed: make(chan struct{}), quiet: make(chan struct{}), role: botRoleKeeper{sb: sb, state: state},
	}
	t.Cleanup(func() {
		lc.mu.Lock()
		lc.ended = true
		lc.mu.Unlock()
		close(hold)
	})
	return lc, func() []*sharedBridge {
		lc.mu.Lock()
		claims := lc.work[workRole]
		lc.mu.Unlock()
		waitFor(t, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(on) == claims
		})
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(on)
	}
}

// TestRoleKeeper_CarriesOutItsRowsEffects pins the effects design §7.1 gives the table's rows: the claim from off
// sends once on the keeper's session, a refusal marks that session for reload, a cut detaches it so no idle edge sends
// on it again, and an input naming another session is dropped.
func TestRoleKeeper_CarriesOutItsRowsEffects(t *testing.T) {
	t.Run("off at an idle edge sends once on its session", func(t *testing.T) {
		sb := &sharedBridge{}
		lc, sent := keeperLife(t, roleOff, sb)
		lc.mu.Lock()
		lc.feedRoleIdleLocked()
		lc.feedRoleIdleLocked()
		lc.mu.Unlock()
		if got := sent(); len(got) != 1 || got[0] != sb {
			t.Errorf("sender started on %v, want once on the keeper's session", got)
		}
	})
	for _, busy := range []struct {
		name string
		take func(lc *chatLifecycle)
	}{
		{"a hosted step", func(lc *chatLifecycle) { lc.steps = map[string]struct{}{"turn_step": {}} }},
		{"a turn KAS opened", func(lc *chatLifecycle) {
			lc.own = &activeTurn{Source: marotte.TurnSourceWireTurnStart}
		}},
		{"a sent prompt", func(lc *chatLifecycle) {
			lc.pending = &activeTurn{Source: marotte.TurnSourcePrompt, sent: true}
		}},
		{"a KAS run", func(lc *chatLifecycle) { lc.kas.state = kasRunning }},
		{"a close tail", func(lc *chatLifecycle) { lc.tails = map[uint64]struct{}{1: {}} }},
		{"a door", func(lc *chatLifecycle) { lc.work[workDoor] = 1 }},
		{"a granted close", func(lc *chatLifecycle) { lc.closing = &closeGrant{} }},
	} {
		t.Run("off sends nothing beside "+busy.name, func(t *testing.T) {
			lc, sent := keeperLife(t, roleOff, &sharedBridge{})
			busy.take(lc)
			lc.mu.Lock()
			lc.feedRoleIdleLocked()
			lc.mu.Unlock()
			if got := sent(); len(got) != 0 {
				t.Errorf("sender started %d times beside %s, want none", len(got), busy.name)
			}
		})
	}
	t.Run("off sends beside a prompt not yet sent", func(t *testing.T) {
		lc, sent := keeperLife(t, roleOff, &sharedBridge{})
		lc.own = &activeTurn{Source: marotte.TurnSourcePrompt}
		lc.mu.Lock()
		lc.feedRoleIdleLocked()
		lc.mu.Unlock()
		if got := sent(); len(got) != 1 {
			t.Errorf("sender started %d times beside an unsent prompt, want once", len(got))
		}
	})
	t.Run("an ended life claims nothing", func(t *testing.T) {
		lc, sent := keeperLife(t, roleOff, &sharedBridge{})
		lc.ended = true
		lc.mu.Lock()
		lc.feedRoleIdleLocked()
		lc.mu.Unlock()
		if got := sent(); len(got) != 0 || lc.role.state != roleOff {
			t.Errorf("an ended life: sender started %d times, state %d; want none and off", len(got), lc.role.state)
		}
	})
	t.Run("a claim Shutdown refuses lets its session go", func(t *testing.T) {
		lc, _ := keeperLife(t, roleOff, &sharedBridge{})
		lc.reg.spawnRole = func(func()) bool { return false }
		lc.mu.Lock()
		lc.feedRoleIdleLocked()
		state, held, work := lc.role.state, lc.role.sb, lc.work[workRole]
		lc.mu.Unlock()
		if state != roleOff || held != nil || work != 0 {
			t.Errorf("a refused claim: state %d, session %v, role work %d; want off, none, 0", state, held, work)
		}
	})
	t.Run("a refusal marks its session for reload", func(t *testing.T) {
		sb := &sharedBridge{}
		lc, _ := keeperLife(t, roleReasserting, sb)
		lc.mu.Lock()
		failed := lc.keepRoleLocked(sb, roleRefused)
		lc.mu.Unlock()
		if !failed || lc.role.state != roleFailed || lc.reloadFor != sb {
			t.Errorf("after a refusal: failed %v, state %d, reload marked %v; want true, failed, marked",
				failed, lc.role.state, lc.reloadFor == sb)
		}
	})
	t.Run("a cut detaches its session", func(t *testing.T) {
		sb := &sharedBridge{}
		lc, sent := keeperLife(t, roleReasserting, sb)
		lc.mu.Lock()
		lc.keepRoleLocked(sb, roleCut)
		lc.feedRoleIdleLocked()
		lc.mu.Unlock()
		if got := sent(); len(got) != 0 || lc.role.sb != nil {
			t.Errorf("after a cut: sender started %d times, keeper holds %v; want none and no session", len(got), lc.role.sb)
		}
	})
	t.Run("another session's input is dropped", func(t *testing.T) {
		sb := &sharedBridge{}
		lc, _ := keeperLife(t, roleReasserting, sb)
		lc.mu.Lock()
		failed := lc.keepRoleLocked(&sharedBridge{}, roleRefused)
		lc.mu.Unlock()
		if failed || lc.role.state != roleReasserting || lc.reloadFor != nil {
			t.Errorf("a stale refusal moved the keeper: failed %v, state %d", failed, lc.role.state)
		}
	})
	t.Run("a door resets onto its session", func(t *testing.T) {
		old, next := &sharedBridge{}, &sharedBridge{}
		lc, _ := keeperLife(t, roleFailed, old)
		lc.mu.Lock()
		lc.keepRoleLocked(next, roleResetBot)
		lc.mu.Unlock()
		if lc.role.state != roleOff || lc.role.sb != next {
			t.Errorf("after a door: state %d on the new session %v, want off on it", lc.role.state, lc.role.sb == next)
		}
	})
}

// keeperIs reports whether chatID's role keeper is in state.
func keeperIs(h *Runtime, chatID marotte.ChatID, state roleState) bool {
	lc, ok := h.coord.turns.lookup(chatID)
	if !ok {
		return false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.role.state == state
}

// keeperHoldsASession reports whether chatID's role keeper holds any session.
func keeperHoldsASession(h *Runtime, chatID marotte.ChatID) bool {
	lc, ok := h.coord.turns.lookup(chatID)
	if !ok {
		return false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.role.sb != nil
}

// awaitRoleSent waits until chatID's role keeper has no set_mode in flight.
func awaitRoleSent(t *testing.T, h *Runtime, chatID marotte.ChatID) {
	t.Helper()
	waitFor(t, func() bool {
		lc, ok := h.coord.turns.lookup(chatID)
		if !ok {
			return true
		}
		lc.mu.Lock()
		defer lc.mu.Unlock()
		return lc.work[workRole] == 0
	})
}

// stallSetMode holds b's session/set_mode calls once sent until the returned release, which answers them with
// refuse (nil for accepted).
func stallSetMode(b *fakeBridge, refuse error) (release func()) {
	gate := make(chan struct{})
	b.mu.Lock()
	b.onCallFails = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool, error) {
		if method != marotte.MethodSetMode {
			return nil, nil, false, nil
		}
		<-gate
		return nil, nil, false, refuse
	}
	b.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { close(gate) }) }
}

// wakeLife wakes chatID's waiters once. A send gate that answered wait passes again; one that let its prompt go never
// does, so a second pass proves the first section held the prompt.
func wakeLife(t *testing.T, h *Runtime, chatID marotte.ChatID) {
	t.Helper()
	lc, ok := h.coord.turns.lookup(chatID)
	if !ok {
		t.Fatalf("%s has no life", chatID)
	}
	lc.mu.Lock()
	lc.wakeLocked()
	lc.mu.Unlock()
}

// postPromptAsync posts messageID into chatID without waiting for its answer, which comes at its turn open.
func postPromptAsync(t *testing.T, h *Runtime, chatID marotte.ChatID, messageID string) <-chan int {
	t.Helper()
	code := make(chan int, 1)
	go func() {
		code <- postCmd(t, h, marotte.ClientCommand{
			Type: marotte.CmdPrompt, ChatID: chatID,
			Payload: json.RawMessage(`{"text":"hello","message_id":"` + messageID + `"}`),
		}).Code
	}()
	return code
}

// gatePasses counts the send gate's sections from now on.
func gatePasses(h *Runtime) *atomic.Int32 {
	var n atomic.Int32
	h.coord.turns.claimHook = func() { n.Add(1) }
	return &n
}

// switchedBot is a started Bot c1 on br whose first turn m1 moved its session to kiro_spec; before runs once the hub
// is up, before that turn.
func switchedBot(t *testing.T, br *fakeBridge, before func(h *Runtime), more ...*fakeBridge) (*Runtime, *testChatStore) {
	t.Helper()
	modeUpdateOnPrompt(br, "kiro_spec")
	h, cs := botRuntime(t, append([]*fakeBridge{br}, more...)...)
	seedModeChat(t, cs, "c1", true, false)
	if before != nil {
		before(h)
	}
	runPrompt(t, h, cs, "c1", "m1")
	return h, cs
}

// TestBotRole_AHostedStepDefersTheReassertToItsEnd pins design §7.1's idle edge: a Bot whose turn left its role while
// its session hosts a run step sends no set_mode (KAS would switch the profile under the step) and lets every
// non-prompt door through; the step's end sends exactly one, and the next prompt goes after it.
func TestBotRole_AHostedStepDefersTheReassertToItsEnd(t *testing.T) {
	br := reporting(marotte.BotRoleID, marotte.DefaultModeID, "kiro_spec", marotte.BotRoleID)
	h, cs := switchedBot(t, br, func(h *Runtime) { h.coord.turns.holdStep("c1", "turn_step") })
	since := h.bus.fanout.Position().Head

	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Errorf("a non-prompt door under the held step failed: %v", err)
	}
	if got := setModeParams(br); len(got) != 0 {
		t.Errorf("session/set_mode modeIds with the step held = %v, want none", got)
	}
	if got := modeErrorsSince(t, h, since); len(got) != 0 {
		t.Errorf("mode_not_applied %q under the held step, want none", got)
	}

	// A prompt sent under the held step waits at the send gate: the session is off its role and not idle.
	br.setOnCall(nil)
	passes := gatePasses(h)
	if code := <-postPromptAsync(t, h, "c1", "m2"); code != http.StatusOK {
		t.Fatalf("prompt m2 = %d", code)
	}
	waitFor(t, func() bool { return passes.Load() > 0 })
	wakeLife(t, h, "c1")
	waitFor(t, func() bool { return passes.Load() > 1 })
	if n := countCalls(br, marotte.MethodPrompt); n != 1 {
		t.Fatalf("%d prompts reached the session off its role under the held step, want only m1", n)
	}

	h.coord.turns.endStep("c1", "turn_step")

	waitFor(t, func() bool { return countCalls(br, marotte.MethodPrompt) == 2 })
	waitFor(t, func() bool { return len(closesOf(t, logOf(t, cs, "c1"))) == 2 && !h.coord.turns.live("c1") })
	if got := setModeParams(br); !slices.Equal(got, []any{marotte.BotRoleID}) {
		t.Errorf("session/set_mode modeIds after the step and a prompt = %v, want one [%s]", got, marotte.BotRoleID)
	}
	if set, prompt := lastCall(br, marotte.MethodSetMode), lastCall(br, marotte.MethodPrompt); set > prompt {
		t.Errorf("the second prompt (call %d) went before the role (call %d)", prompt, set)
	}
}

// TestBotRole_ANonPromptDoorOpensWhileTheRoleFailed pins design §7.1 rule "only prompt admission reads the keeper": a
// Bot whose role KAS refused back, with a run step now held, still opens for a run verb or a step's answer, with no
// set_mode and no second message, so the held step can finish.
func TestBotRole_ANonPromptDoorOpensWhileTheRoleFailed(t *testing.T) {
	br := reporting(marotte.BotRoleID, marotte.DefaultModeID, "kiro_spec", marotte.BotRoleID)
	br.setCallErr(marotte.MethodSetMode, context.DeadlineExceeded)
	h, _ := switchedBot(t, br, nil)
	waitFor(t, func() bool { return len(modeErrorsSince(t, h, 0)) == 1 })
	h.coord.turns.holdStep("c1", "turn_step")
	since := h.bus.fanout.Position().Head

	_, err := h.coord.openReleased(t.Context(), "c1", "")
	if err != nil {
		t.Errorf("a non-prompt door failed while a step is held: %v", err)
	}
	if got := setModeParams(br); len(got) != 1 {
		t.Errorf("session/set_mode modeIds = %v, want only the refused try", got)
	}
	if got := modeErrorsSince(t, h, since); len(got) != 0 {
		t.Errorf("mode_not_applied %q at a non-prompt door, want none", got)
	}
}

// TestBotRole_APromptWaitsWhileTheRoleIsReasserted pins design §7.1's gate: a prompt sent while the set_mode is in
// flight waits at the send gate and goes out only once KAS accepted the role; when KAS refuses, it is refused unsent
// with the Bot's sentence.
func TestBotRole_APromptWaitsWhileTheRoleIsReasserted(t *testing.T) {
	waiting := func(t *testing.T, refuse error) (*Runtime, *testChatStore, *fakeBridge, func()) {
		t.Helper()
		br := reporting(marotte.BotRoleID, marotte.DefaultModeID, "kiro_spec", marotte.BotRoleID)
		release := stallSetMode(br, refuse)
		t.Cleanup(release)
		h, cs := switchedBot(t, br, nil)
		waitFor(t, func() bool { return len(setModeParams(br)) == 1 })
		br.setOnCall(nil)
		passes := gatePasses(h)
		if code := <-postPromptAsync(t, h, "c1", "m2"); code != http.StatusOK {
			t.Fatalf("prompt m2 = %d", code)
		}
		waitFor(t, func() bool { return passes.Load() > 0 })
		wakeLife(t, h, "c1")
		waitFor(t, func() bool { return passes.Load() > 1 })
		if n := countCalls(br, marotte.MethodPrompt); n != 1 {
			t.Fatalf("%d prompts reached the session while its role was in flight, want only m1", n)
		}
		return h, cs, br, release
	}

	t.Run("accepted: the prompt goes after the role", func(t *testing.T) {
		h, cs, br, release := waiting(t, nil)

		release()

		waitFor(t, func() bool { return countCalls(br, marotte.MethodPrompt) == 2 })
		waitFor(t, func() bool { return len(closesOf(t, logOf(t, cs, "c1"))) == 2 && !h.coord.turns.live("c1") })
		if got := setModeParams(br); len(got) != 1 {
			t.Errorf("session/set_mode modeIds = %v, want one", got)
		}
	})
	t.Run("refused: the prompt is refused unsent", func(t *testing.T) {
		h, cs, br, release := waiting(t, &marotte.RPCError{Code: -32602, Message: "Unknown mode: marotte-bot"})
		since := h.bus.fanout.Position().Head

		release()

		waitFor(t, func() bool { return len(closesOf(t, logOf(t, cs, "c1"))) == 2 })
		closes := closesOf(t, logOf(t, cs, "c1"))
		if got := closes[1].FailureReason; got != botRoleMessage {
			t.Errorf("m2's turn_close failure_reason = %q, want %q", got, botRoleMessage)
		}
		if n := countCalls(br, marotte.MethodPrompt); n != 1 {
			t.Errorf("%d prompts reached the session KAS refused the role on, want only m1", n)
		}
		waitFor(t, func() bool { return len(modeErrorsSince(t, h, since)) == 1 })
		if got := modeErrorsSince(t, h, since); got[0] != botRoleMessage {
			t.Errorf("mode_not_applied = %q, want %q", got, botRoleMessage)
		}
	})
}

// TestBotRole_ASetModeTheSessionsCloseCutIsNoRefusal pins design §7.1's cut column: a re-assert whose session closed
// under it was never answered, so the keeper lets go of that session without failing it, and the chat hears nothing.
func TestBotRole_ASetModeTheSessionsCloseCutIsNoRefusal(t *testing.T) {
	br := reporting(marotte.BotRoleID, marotte.DefaultModeID, "kiro_spec", marotte.BotRoleID)
	release := stallSetMode(br, marotte.ErrSessionClosed)
	t.Cleanup(release)
	h, _ := switchedBot(t, br, nil)
	waitFor(t, func() bool { return len(setModeParams(br)) == 1 })
	since := h.bus.fanout.Position().Head

	release()

	awaitRoleSent(t, h, "c1")
	if keeperIs(h, "c1", roleFailed) || keeperHoldsASession(h, "c1") {
		t.Error("a cut re-assert failed the keeper or kept its session, want it let go")
	}
	if got := modeErrorsSince(t, h, since); len(got) != 0 {
		t.Errorf("mode_not_applied %q for a cut re-assert, want none", got)
	}
}

// TestBotRole_AFailedRoleRefusesPromptsUntilADoorResetsIt pins design §7.1 "failed lasts until the next door reset":
// while a held config.json defers the reload the refusal marked, a prompt is refused unsent with the Bot's sentence and
// no set_mode is retried; once config.json reads, the next prompt's door reloads the session and runs on the role.
func TestBotRole_AFailedRoleRefusesPromptsUntilADoorResetsIt(t *testing.T) {
	br := reporting(marotte.BotRoleID, marotte.DefaultModeID, "kiro_spec", marotte.BotRoleID)
	br.setCallErr(marotte.MethodSetMode, context.DeadlineExceeded)
	reload := reporting(marotte.BotRoleID, marotte.DefaultModeID, "kiro_spec", marotte.BotRoleID)
	reload.chunksOnCall = map[string][]string{marotte.MethodPrompt: {"done"}}
	h, cs := switchedBot(t, br, nil, reload)
	waitFor(t, func() bool { return len(setModeParams(br)) == 1 })
	h.coord.configHeld = func() bool { return true }

	runPrompt(t, h, cs, "c1", "m2")

	if got := closesOf(t, logOf(t, cs, "c1"))[1].FailureReason; got != botRoleMessage {
		t.Errorf("m2's turn_close failure_reason = %q, want %q", got, botRoleMessage)
	}
	if sessionClosed(br) {
		t.Fatal("the session was reloaded while config.json did not read")
	}
	if n := countCalls(br, marotte.MethodPrompt); n != 1 {
		t.Errorf("%d prompts reached the session off its role, want only m1", n)
	}
	if got := setModeParams(br); len(got) != 1 {
		t.Errorf("session/set_mode modeIds = %v, want no retry before a door resets the keeper", got)
	}

	h.coord.configHeld = func() bool { return false }
	runPrompt(t, h, cs, "c1", "m3")

	if !sessionClosed(br) {
		t.Error("the session KAS refused the role on was kept once config.json read")
	}
	if n := countCalls(reload, marotte.MethodPrompt); n != 1 {
		t.Errorf("%d prompts reached the reloaded session, want m3", n)
	}
}
