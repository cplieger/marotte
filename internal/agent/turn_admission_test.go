package agent

// The admission slot against the REAL registry and coordinator: holder-keyed
// refusals, the bridge-ready wake, the prime window, StartTurn's stamp timing,
// and the full prompt path from ack to recovery.

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// shrinkAdmissionWait bounds a deliberately contended full-path wait so a
// refusal test does not sit out the production budget. Not parallel-safe, so
// no test using it may call t.Parallel.
func shrinkAdmissionWait(t *testing.T, d time.Duration) {
	t.Helper()
	prev := command.AdmissionWait
	command.AdmissionWait = d
	t.Cleanup(func() { command.AdmissionWait = prev })
}

// setCancelGrace writes the unresponsive-cancel budget, in both directions: SHORT so a
// test can drive the expiry instead of sitting out the production 10s, and LONG so a test
// can arm the grace and be sure its timer cannot fire inside the run. Not parallel-safe,
// so no test using it may call t.Parallel.
func setCancelGrace(t *testing.T, d time.Duration) {
	t.Helper()
	prev := command.CancelGrace
	command.CancelGrace = d
	t.Cleanup(func() { command.CancelGrace = prev })
}

func seedChat(t *testing.T, cs *testChatStore, id marotte.ChatID) {
	t.Helper()
	if _, err := cs.Mutate(t.Context(), id, func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true }); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
}

// The refusal arm keys on the HOLDER'S SOURCE alone, never on bridge liveness: a
// shell holder answers "starting" on a bridgeless AND on a bridged chat — a shell
// has a live bridge and no steerable turn — and a prompt-class holder answers
// busy whatever its bridge, because the steer the client converts to is parked
// until that bridge is live.
func TestReserveTurnForPrompt_RefusalKeysOnTheHoldersSource(t *testing.T) {
	cases := []struct {
		name    string
		holder  marotte.TurnOpenSource
		bridged bool
		want    command.AdmissionOutcome
	}{
		{name: "shell holder on a bridgeless chat answers starting", holder: marotte.TurnSourceLocalShell, want: command.AdmissionStarting},
		{name: "shell holder on a bridged chat answers starting", holder: marotte.TurnSourceLocalShell, bridged: true, want: command.AdmissionStarting},
		{name: "prompt holder with no bridge answers busy", holder: marotte.TurnSourcePrompt, want: command.AdmissionBusy},
		{name: "prompt holder with a live bridge answers busy", holder: marotte.TurnSourcePrompt, bridged: true, want: command.AdmissionBusy},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, cs, _ := newTestHub()
			chatID := marotte.ChatID("c-admission-" + string(rune('a'+i)))
			seedChat(t, cs, chatID)
			if tc.bridged {
				if _, err := h.coord.OpenBridge(t.Context(), chatID, ""); err != nil {
					t.Fatalf("OpenBridge: %v", err)
				}
			}
			if !h.coord.TryReserveTurn(chatID, tc.holder) {
				t.Fatal("setup: the admission slot was already held")
			}
			t.Cleanup(func() { h.coord.ReleaseTurnReservation(chatID) })

			got := h.coord.ReserveTurnForPrompt(t.Context(), chatID, 60*time.Millisecond)
			if got != tc.want {
				t.Errorf("ReserveTurnForPrompt(holder %v, bridged %v) = %v, want %v", tc.holder, tc.bridged, got, tc.want)
			}
		})
	}
}

// A free slot admits immediately, and a slot a shell holds parks the waiter
// until the release admits exactly it. A shell holder is the fixture because it
// is the one holder a prompt still waits behind.
func TestReserveTurnForPrompt_AcquiresAFreeOrFreedSlot(t *testing.T) {
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1")
	if got := h.coord.ReserveTurnForPrompt(t.Context(), "c1", 60*time.Millisecond); got != command.AdmissionAcquired {
		t.Fatalf("free slot = %v, want acquired", got)
	}
	h.coord.ReleaseTurnReservation("c1")
	if !h.coord.TryReserveTurn("c1", marotte.TurnSourceLocalShell) {
		t.Fatal("setup: the admission slot was already held")
	}
	answered := make(chan command.AdmissionOutcome, 1)
	go func() {
		answered <- h.coord.ReserveTurnForPrompt(context.Background(), "c1", 5*time.Second)
	}()
	select {
	case got := <-answered:
		t.Fatalf("the waiter answered %v while the slot was held", got)
	case <-time.After(50 * time.Millisecond):
	}
	h.coord.ReleaseTurnReservation("c1")
	select {
	case got := <-answered:
		if got != command.AdmissionAcquired {
			t.Fatalf("waiter = %v, want acquired after the release", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the waiter never woke on the release")
	}
	h.coord.ReleaseTurnReservation("c1")
}

// A prompt arriving while a prompt-class holder's bridge is still SPAWNING answers
// busy at once, not after the budget: the fake's startGate holds the spawn open,
// so the holder has no live bridge for the whole wait, and the answer must come
// from the holder's source rather than from bridge-ready or from expiry.
func TestReserveTurnForPrompt_ASpawningPromptHolderAnswersBusyAtOnce(t *testing.T) {
	h, cs, br := newTestHub()
	seedChat(t, cs, "c1")
	startGate := make(chan struct{})
	br.mu.Lock()
	br.startGate = startGate
	br.mu.Unlock()
	if !h.coord.TryReserveTurn("c1", marotte.TurnSourcePrompt) {
		t.Fatal("setup: the admission slot was already held")
	}
	t.Cleanup(func() { h.coord.ReleaseTurnReservation("c1") })
	spawned := make(chan error, 1)
	go func() {
		_, err := h.coord.OpenBridge(t.Context(), "c1", "")
		spawned <- err
	}()

	const budget = 10 * time.Second
	start := time.Now()
	got := h.coord.ReserveTurnForPrompt(context.Background(), "c1", budget)
	elapsed := time.Since(start)
	close(startGate)
	if err := <-spawned; err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	if got != command.AdmissionBusy {
		t.Fatalf("ReserveTurnForPrompt = %v, want busy: the holder is prompt-class, bridge or no bridge", got)
	}
	if elapsed > 2*time.Second {
		t.Errorf("the answer took %v, want the holder's source read at once, not the %v budget", elapsed, budget)
	}
}

// The full 409-starting path: a prompt against a shell-held chat answers 409
// with the additive `reason":"starting"` on the wire, at the wait budget, and
// writes nothing: admission runs before the turn_open, so a refused prompt
// leaves the log as it found it.
func TestPrompt_FullPath409StartingCarriesTheReason(t *testing.T) {
	shrinkAdmissionWait(t, 60*time.Millisecond)
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1")
	if !h.coord.TryReserveTurn("c1", marotte.TurnSourceLocalShell) {
		t.Fatal("setup: the admission slot was already held")
	}
	t.Cleanup(func() { h.coord.ReleaseTurnReservation("c1") })

	rec := postCmd(t, h, marotte.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Error  string `json:"error"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body %q: %v", rec.Body.String(), err)
	}
	if body.Reason != "starting" {
		t.Errorf(`body = %s, want the additive "reason":"starting"`, rec.Body.String())
	}
	if entries := logOf(t, cs, "c1"); len(entries) != 0 {
		t.Errorf("log = %d entries, want none: a refused prompt opens no turn", len(entries))
	}
}

// gateSpawn parks every bridge spawn on the returned gate, signalling entry.
// It installs the hook through the PUBLIC setter — the composition root's own
// path — so these tests also pin that the setter reaches the coordinator.
func gateSpawn(h *Runtime) (entered, gate chan struct{}) {
	entered = make(chan struct{})
	gate = make(chan struct{})
	var once sync.Once
	h.SetPreBridgeSpawn(func(ctx context.Context) {
		once.Do(func() { close(entered) })
		select {
		case <-gate:
		case <-ctx.Done():
		}
	})
	return entered, gate
}

// `!echo hi` during a prompt's BLOCKED SPAWN is refused immediately: the shell
// door's reservation is a try, never a wait, and the spawn window is exactly
// when the bridge slot does not exist to refuse for it.
func TestShellDuringABlockedSpawnIsRefusedImmediately(t *testing.T) {
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1")
	entered, gate := gateSpawn(h)
	defer close(gate)

	if rec := postCmd(t, h, marotte.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	<-entered // the prompt goroutine is parked inside its spawn, holding the reservation

	start := time.Now()
	rec := postCmd(t, h, marotte.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"!echo hi","message_id":"m-2"}`),
	})
	elapsed := time.Since(start)
	if rec.Code != http.StatusConflict {
		t.Fatalf("shell during spawn = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "busy") {
		t.Errorf("body = %q, want busy", rec.Body.String())
	}
	if elapsed > time.Second {
		t.Errorf("the shell refusal took %v, want an immediate try — never a wait", elapsed)
	}
}

// waitForTurnClosed polls the replay buffer until EXACTLY want turn_closed frames
// exist and returns their turn_close entries. Deadline-bounded, fails closed with
// the count. Exact rather than at-least because a surplus is a defect in its own
// right: a turn announcing its close twice is what the single closer exists to
// prevent. A surplus landing after the count is reached is not caught.
func waitForTurnClosed(t *testing.T, h *Runtime, want int) []marotte.EntryTurnClose {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := closedBroadcasts(t, h)
		if len(got) > want {
			t.Fatalf("turn_closed frames = %d, want exactly %d: a turn announced its close more than once", len(got), want)
		}
		if len(got) == want {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("turn_closed frames = %d, want %d", len(got), want)
		}
		time.Sleep(time.Millisecond)
	}
}

// The model is stamped at StartTurn, with the bridge live: a cold chat's turn
// carries the model the spawn persisted rather than an empty latch. The turn's
// credits are what its own frames metered, so spend landing on the chat record
// during the spawn window never reaches the turn_close.
func TestPromptTurn_MeteringAndModelStampAtStartTurn(t *testing.T) {
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1") // cold: no model on the record yet
	entered, gate := gateSpawn(h)

	if rec := postCmd(t, h, marotte.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	<-entered
	// Spend lands while the spawn is still in flight — BEFORE StartTurn.
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Usage.Credits = 5
		c.Usage.HasRealData = true
		return true
	}); err != nil {
		t.Fatalf("stage spend: %v", err)
	}
	close(gate)

	closed := waitForTurnClosed(t, h, 1)
	if got := closed[0].Credits; got != 0 {
		t.Errorf("turn_close credits = %v, want 0: spend during the spawn window is not the turn's", got)
	}
	if closed[0].Model == "" {
		t.Error("turn_close model is empty on a cold chat; StartTurn runs after the spawn persisted the session's model")
	}
}

// The recovery arbitrates on the CAPTURED result through the real registry: a
// wire-ended empty turn closes `empty` and re-prompts on a fresh session as a
// turn of its own. The capture order is load-bearing — the registry drops the
// retained result at the last release, so a ReleaseTurn before the AwaitTurn
// would leave the recovery blind and this test red.
func TestPromptTurn_EmptyWireEndedTurnRecoversThroughTheRealRegistry(t *testing.T) {
	cs := newTestChatStore()
	gate := make(chan struct{})
	var mu sync.Mutex
	var minted []*fakeBridge
	factory := func() ACPBridge {
		mu.Lock()
		defer mu.Unlock()
		br := newFakeBridge()
		if len(minted) == 0 {
			// Only the FIRST session's prompt is held open, so the wire turn_end
			// can land while the call is in flight; the retry's flows freely.
			br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: gate}
		}
		minted = append(minted, br)
		return br
	}
	h := New(context.Background(), t.TempDir(), factory, cs)
	cs.wire(h)
	h.mcpRegistry.SignalReady()
	seedChat(t, cs, "c1")

	if rec := postCmd(t, h, marotte.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	// The first bridge's prompt call is in flight; the wire closes the turn
	// EMPTY while the response is still pending.
	first := func() *fakeBridge {
		deadline := time.Now().Add(5 * time.Second)
		for {
			mu.Lock()
			var br *fakeBridge
			if len(minted) > 0 {
				br = minted[0]
			}
			mu.Unlock()
			if br != nil {
				return br
			}
			if time.Now().After(deadline) {
				t.Fatal("the prompt goroutine never spawned a bridge")
			}
			time.Sleep(time.Millisecond)
		}
	}()
	waitForCall(t, first, marotte.MethodPrompt)
	first.deliver(newTurnEndMsg("end_turn"))
	close(gate)

	// The recovery tears the session down and re-prompts on a fresh one.
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		respawned := len(minted) >= 2
		var second *fakeBridge
		if respawned {
			second = minted[1]
		}
		mu.Unlock()
		if respawned && slices.Contains(second.callLog(), marotte.MethodPrompt) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the empty wire-ended turn was never retried: the captured result did not reach the recovery")
		}
		time.Sleep(time.Millisecond)
	}
	// The log records the empty close and the retry as a turn of its own.
	entries := logOf(t, cs, "c1")
	closes := closesOf(t, entries)
	if len(closes) == 0 || closes[0].Outcome != marotte.TurnOutcomeEmpty {
		t.Errorf("turn_close outcomes = %+v, want the first turn closed %q", closes, marotte.TurnOutcomeEmpty)
	}
	opens := opensOf(t, entries)
	if len(opens) != 2 || opens[1].Source != marotte.TurnOpenNameEmptyRetry {
		t.Errorf("turn_open sources = %+v, want [prompt empty_retry]", opens)
	}
}

// Shutdown mid-Call: the blocked prompt call unwinds, the turn finalizes, the
// in-flight count drains, and Shutdown returns.
func TestPromptTurn_ShutdownMidCallDrainsTheTurn(t *testing.T) {
	h, cs, br := newTestHub()
	seedChat(t, cs, "c1")
	gate := make(chan struct{})
	defer close(gate)
	br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: gate}

	if rec := postCmd(t, h, marotte.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	waitForCall(t, br, marotte.MethodPrompt)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := h.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown = %v: the in-flight prompt goroutine did not drain", err)
	}
}

// Shutdown between the ack and the goroutine's first real step: the in-flight
// registration happened BEFORE the ack, so Shutdown waits for the turn rather
// than racing past it, and the turn still reaches a terminal broadcast.
func TestPromptTurn_ShutdownPreGoroutineStillDrainsTheTurn(t *testing.T) {
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1")
	entered, gate := gateSpawn(h)
	defer close(gate)

	if rec := postCmd(t, h, marotte.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := h.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown = %v: the pre-goroutine window is registered in-flight before the ack", err)
	}
	// The goroutine ran to a terminal signal under shutdown rather than being
	// abandoned mid-flight: either the turn closed or its failure broadcast.
	types := extractTypes(t, bufferedSince(h, 0))
	if missing := missingEvents(types, string(marotte.EventTurnClosed)); missing != nil {
		if missingErr := missingEvents(types, string(marotte.EventError)); missingErr != nil {
			t.Errorf("events = %v, want a terminal turn_closed or error for the in-flight prompt", types)
		}
	}
}

// A cancel landing between the ack and BeginPromptCall neither wedges the chat
// nor strands the turn: the cancel answers 200 with nothing to arm against,
// the turn closes, and the chat accepts the next prompt.
func TestPromptTurn_CancelBetweenAckAndBeginPromptCall(t *testing.T) {
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1")
	entered, gate := gateSpawn(h)

	if rec := postCmd(t, h, marotte.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	<-entered // parked in the spawn: no BeginPromptCall yet

	if rec := postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1"}); rec.Code != http.StatusOK {
		t.Fatalf("cancel in the spawn window = %d, want 200", rec.Code)
	}
	close(gate)
	waitForTurnClosed(t, h, 1)

	// The chat is not wedged: the slots released, so a fresh prompt is admitted.
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec := postCmd(t, h, marotte.ClientCommand{
			Type: "prompt", ChatID: "c1",
			Payload: json.RawMessage(`{"text":"again","message_id":"m-2"}`),
		})
		if rec.Code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a follow-up prompt never got in: last answer %d %s", rec.Code, rec.Body.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	waitForTurnClosed(t, h, 2)
}

// session/cancel is a NOTIFICATION nothing acks, so when KAS never answers the
// pending session/prompt the grace budget cancels the prompt context itself. That
// exit used to take the ordinary prompt-failure route and conclude `interrupted`,
// a red card for a stop the reader asked for. Both surfaces are asserted because
// they are two writes of one verdict, and the close count pins that the turn is
// closed ONCE: a second closer writing `interrupted` beside it would be two footers.
func TestPromptTurn_UnackedCancelConcludesCancelled(t *testing.T) {
	setCancelGrace(t, 20*time.Millisecond)
	h, cs, br := newTestHub()
	seedChat(t, cs, "c1")
	gate := make(chan struct{})
	defer close(gate)
	br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: gate}

	if rec := postCmd(t, h, marotte.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	waitForCall(t, br, marotte.MethodPrompt)

	if rec := postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1"}); rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d, want 200", rec.Code)
	}
	closed := waitForTurnClosed(t, h, 1)

	closes := closesOf(t, logOf(t, cs, "c1"))
	if len(closes) != 1 {
		t.Fatalf("turn_close entries = %d, want exactly one: %+v", len(closes), closes)
	}
	if closes[0].Outcome != marotte.TurnOutcomeCancelled {
		t.Errorf("turn_close outcome = %q, want %q: the reader pressed Stop, so nothing failed",
			closes[0].Outcome, marotte.TurnOutcomeCancelled)
	}
	if closes[0].FailureReason != "" {
		t.Errorf("turn_close failure_reason = %q, want empty: a cancel has no account to give",
			closes[0].FailureReason)
	}
	if closed[0].Outcome != marotte.TurnOutcomeCancelled {
		t.Errorf("turn_closed outcome = %q, want %q: the live surface must agree with the "+
			"persisted one", closed[0].Outcome, marotte.TurnOutcomeCancelled)
	}
}

// The other direction, and what proves the two paths were separated rather than
// both moved: Shutdown writes no cause on the way to the turn, so an absent grace
// sentinel keeps meaning `interrupted`. The grace is genuinely ARMED with a budget
// that outlives the run, so the two cancellers contend for the one close this turn
// gets and shutdown reaches it first. The waitForCall puts Shutdown strictly AFTER
// StartTurn and after the bridge is registered, which is what keeps this test off
// the open shutdown-closer race its pre-goroutine sibling accepts either frame for.
func TestPromptTurn_ShutdownDuringTheGraceStaysInterrupted(t *testing.T) {
	setCancelGrace(t, time.Minute)
	h, cs, br := newTestHub()
	seedChat(t, cs, "c1")
	gate := make(chan struct{})
	defer close(gate)
	br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: gate}

	if rec := postCmd(t, h, marotte.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	waitForCall(t, br, marotte.MethodPrompt)

	if rec := postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1"}); rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d, want 200", rec.Code)
	}
	sb := h.bridge.mgr.get("c1")
	if sb == nil {
		t.Fatal("the prompt opened no bridge")
	}
	sb.mu.Lock()
	armed := sb.cancelTimer != nil
	sb.mu.Unlock()
	if !armed {
		t.Fatal("the cancel armed no grace, so the two cancellers were never contended")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := h.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown = %v: the in-flight prompt goroutine did not drain", err)
	}

	closed := waitForTurnClosed(t, h, 1)
	if closed[0].Outcome != marotte.TurnOutcomeInterrupted {
		t.Errorf("turn_closed outcome = %q, want %q: a shutdown carries no grace sentinel, "+
			"so it is a fault rather than a stop the reader asked for",
			closed[0].Outcome, marotte.TurnOutcomeInterrupted)
	}
}

// waitForPromptCall blocks until the chat's bridge has registered an in-flight prompt
// context. That is the state ArmCancelGrace refuses without, so a cancel posted earlier
// arms nothing and the test would drive a different path than it claims.
func waitForPromptCall(t *testing.T, h *Runtime, chatID marotte.ChatID) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if sb := h.bridge.mgr.get(chatID); sb != nil {
			sb.mu.Lock()
			armable := sb.state == bridgePrompting && sb.promptCancel != nil
			sb.mu.Unlock()
			if armable {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the prompt goroutine never registered its prompt context")
		}
		time.Sleep(time.Millisecond)
	}
}

// The SECOND mechanism the same gesture reaches: BeginPromptCall runs before the
// MCP wait, so a cancel in that window arms the grace and the expiry kills the
// context before StartTurn. The turn CmdPrompt opened at admission is then closed
// by the pre-start exit itself through the turn end rule, `cancelled` with no
// account and no prompt_failed toast. The MCP wait is the fixture because it is
// the widest part of that window: 30s on a first prompt after boot.
func TestPromptTurn_CancelDuringTheMCPWaitConcludesCancelled(t *testing.T) {
	setCancelGrace(t, 20*time.Millisecond)
	h, cs, _ := newTestHubUnready()
	seedChat(t, cs, "c1")

	if rec := postCmd(t, h, marotte.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	waitForPromptCall(t, h, "c1")

	if rec := postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1"}); rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d, want 200", rec.Code)
	}
	closed := waitForTurnClosed(t, h, 1)

	entries := logOf(t, cs, "c1")
	closes := closesOf(t, entries)
	if len(closes) != 1 || closes[0].Outcome != marotte.TurnOutcomeCancelled {
		t.Errorf("turn_close entries = %+v, want exactly one, %q: an interrupted close here "+
			"grades the turn broken and paints it red", closes, marotte.TurnOutcomeCancelled)
	}
	if len(closes) == 1 && closes[0].FailureReason != "" {
		t.Errorf("turn_close failure_reason = %q, want empty: a cancel has no account to give",
			closes[0].FailureReason)
	}
	if texts := textsOf(t, entries); len(texts) != 0 {
		t.Errorf("text entries = %q, want none: the agent was never asked", texts)
	}
	if closed[0].Outcome != marotte.TurnOutcomeCancelled {
		t.Errorf("turn_closed outcome = %q, want %q", closed[0].Outcome, marotte.TurnOutcomeCancelled)
	}
	if types := extractTypes(t, bufferedSince(h, 0)); slices.Contains(types, string(marotte.EventError)) {
		t.Errorf("events = %v, want no error frame: prompt_failed routes to a toast, and a "+
			"red toast for a stop the reader asked for is the same wrong signal as the red card",
			types)
	}
}
