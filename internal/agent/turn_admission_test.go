package agent

// The admission slot against the real registry and coordinator.

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

// Not parallel-safe.
func shrinkAdmissionWait(t *testing.T, d time.Duration) {
	t.Helper()
	prev := command.AdmissionWait
	command.AdmissionWait = d
	t.Cleanup(func() { command.AdmissionWait = prev })
}

// setCancelGrace sets the cancel grace short to drive expiry or long to keep it from firing. Not parallel-safe.
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

// The refusal keys on the holder's source alone: a shell holder answers "starting" with or without a bridge; a
// prompt holder answers busy whatever its bridge.
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
				if _, err := h.coord.openBridge(t.Context(), chatID, ""); err != nil {
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

// A free slot admits at once; a shell-held one parks the waiter until the release admits it.
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

// A prompt-class holder whose session door is still open answers busy at once, from its source rather than readiness or expiry.
func TestReserveTurnForPrompt_AnOpeningPromptHolderAnswersBusyAtOnce(t *testing.T) {
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
	opened := make(chan error, 1)
	go func() {
		_, err := h.coord.openBridge(t.Context(), "c1", "")
		opened <- err
	}()

	const budget = 10 * time.Second
	start := time.Now()
	got := h.coord.ReserveTurnForPrompt(context.Background(), "c1", budget)
	elapsed := time.Since(start)
	close(startGate)
	if err := <-opened; err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	if got != command.AdmissionBusy {
		t.Fatalf("ReserveTurnForPrompt = %v, want busy: the holder is prompt-class, bridge or no bridge", got)
	}
	if elapsed > 2*time.Second {
		t.Errorf("the answer took %v, want the holder's source read at once, not the %v budget", elapsed, budget)
	}
}

// A prompt against a shell-held chat answers 409 with `reason":"starting"` at the budget and writes nothing.
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

// gateSessionOpen parks every session door on the returned gate, installed through the public setter so the wiring is
// pinned too.
func gateSessionOpen(h *Runtime) (entered, gate chan struct{}) {
	entered = make(chan struct{})
	gate = make(chan struct{})
	var once sync.Once
	h.SetPreSessionOpen(func(ctx context.Context) {
		once.Do(func() { close(entered) })
		select {
		case <-gate:
		case <-ctx.Done():
		}
	})
	return entered, gate
}

// `!echo hi` during a prompt's blocked session door is refused at once: the shell door tries, never waits.
func TestShellDuringABlockedSessionOpenIsRefusedImmediately(t *testing.T) {
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1")
	entered, gate := gateSessionOpen(h)
	defer close(gate)

	if rec := postCmd(t, h, marotte.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	<-entered // the prompt goroutine is parked inside its session open, holding the reservation

	start := time.Now()
	rec := postCmd(t, h, marotte.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"!echo hi","message_id":"m-2"}`),
	})
	elapsed := time.Since(start)
	if rec.Code != http.StatusConflict {
		t.Fatalf("shell during session open = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "busy") {
		t.Errorf("body = %q, want busy", rec.Body.String())
	}
	if elapsed > time.Second {
		t.Errorf("the shell refusal took %v, want an immediate try — never a wait", elapsed)
	}
}

// waitForTurnClosed polls until exactly want turn_closed frames exist and returns their turn_close entries; a surplus is a defect.
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

// The model is stamped at StartTurn with the bridge live; credits are the turn's own frames, so session-open-window spend never reaches its close.
func TestPromptTurn_MeteringAndModelStampAtStartTurn(t *testing.T) {
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1") // cold: no model on the record yet
	entered, gate := gateSessionOpen(h)

	if rec := postCmd(t, h, marotte.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	<-entered
	// Spend lands before StartTurn.
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
		t.Errorf("turn_close credits = %v, want 0: spend during the session-open window is not the turn's", got)
	}
	if closed[0].Model == "" {
		t.Error("turn_close model is empty on a cold chat; StartTurn runs after the session open persisted the session's model")
	}
}

// The recovery arbitrates on the captured result: an empty wire-ended turn closes `empty` and re-prompts on a fresh
// session. ReleaseTurn before AwaitTurn would blind it.
func TestPromptTurn_EmptyWireEndedTurnRecoversThroughTheRealRegistry(t *testing.T) {
	cs := newTestChatStore()
	gate := make(chan struct{})
	var mu sync.Mutex
	var minted []*fakeBridge
	factory := func() engineSession {
		mu.Lock()
		defer mu.Unlock()
		br := newFakeBridge()
		if len(minted) == 0 {
			// Only the first session's prompt is held, so turn_end lands mid-call.
			br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: gate}
		}
		minted = append(minted, br)
		return br
	}
	h := newRuntimeOn(context.Background(), t.TempDir(), fakeEngineOf(factory, nil), cs)
	cs.wire(h)
	seedChat(t, cs, "c1")

	if rec := postCmd(t, h, marotte.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	// The wire closes the turn empty while the response is pending.
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
				t.Fatal("the prompt goroutine never opened a session")
			}
			time.Sleep(time.Millisecond)
		}
	}()
	waitForCall(t, first, marotte.MethodPrompt)
	first.deliver(newTurnStartMsg())
	first.deliver(newTurnEndMsg("end_turn"))
	close(gate)

	// The recovery re-prompts on a fresh session.
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		reopened := len(minted) >= 2
		var second *fakeBridge
		if reopened {
			second = minted[1]
		}
		mu.Unlock()
		if reopened && slices.Contains(second.callLog(), marotte.MethodPrompt) {
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

// Shutdown mid-Call: the prompt unwinds, the turn finalizes, inflight drains and Shutdown returns.
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

// Shutdown right after the ack: the in-flight registration precedes the ack, so Shutdown waits for the turn.
func TestPromptTurn_ShutdownPreGoroutineStillDrainsTheTurn(t *testing.T) {
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1")
	entered, gate := gateSessionOpen(h)
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
	// The goroutine reached a terminal signal under shutdown.
	types := extractTypes(t, bufferedSince(h, 0))
	if missing := missingEvents(types, string(marotte.EventTurnClosed)); missing != nil {
		if missingErr := missingEvents(types, string(marotte.EventError)); missingErr != nil {
			t.Errorf("events = %v, want a terminal turn_closed or error for the in-flight prompt", types)
		}
	}
}

// A cancel between the ack and BeginPromptCall answers 200 with nothing to arm; the turn closes and the chat accepts the next prompt.
func TestPromptTurn_CancelBetweenAckAndBeginPromptCall(t *testing.T) {
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1")
	entered, gate := gateSessionOpen(h)

	if rec := postCmd(t, h, marotte.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	<-entered // parked in the session open: no BeginPromptCall yet

	if rec := postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1"}); rec.Code != http.StatusOK {
		t.Fatalf("cancel in the session-open window = %d, want 200", rec.Code)
	}
	close(gate)
	waitForTurnClosed(t, h, 1)

	// The slots released, so a fresh prompt is admitted.
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

// When KAS never answers after an unacked session/cancel, the grace expiry must conclude `cancelled`, on both
// surfaces, closed once.
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

// Shutdown's cause changes the sentence, never the stop, so a turn without the grace sentinel stays `interrupted`.
// The grace is armed long and Shutdown runs after StartTurn, so the two contend for one close.
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

func shutdownMidPrompt(t *testing.T, arm func(h *Runtime)) *testChatStore {
	t.Helper()
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
	if arm != nil {
		arm(h)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := h.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown = %v", err)
	}
	return cs
}

// TestShutdown_RecordsAKASQueuedSteerAsADroppedRestart pins that the skipped death closer would otherwise lose a queued steer.
func TestShutdown_RecordsAKASQueuedSteerAsADroppedRestart(t *testing.T) {
	cs := shutdownMidPrompt(t, func(h *Runtime) {
		sends, refuse := h.steerQueue.RouteSteer("c1", "s-1", "use tabs",
			command.SteerHolder{Held: true, PromptClass: true, Live: true})
		if refuse != "" || len(sends) != 1 {
			t.Fatalf("RouteSteer = %+v %q, want one send", sends, refuse)
		}
		h.bus.steers.SteerWaiting("c1", &marotte.SteerQueuedPayload{
			SteerID: sends[0].ID, Text: sends[0].Text, Origin: marotte.SteerOriginUser,
		})
		h.steerQueue.SteerSent("c1", sends[0], true, nil)
	})

	entries := logOf(t, cs, "c1")
	closeAt, steerAt := -1, -1
	var steer marotte.EntrySteer
	for i, e := range entries {
		switch e.Kind {
		case marotte.EntryKindTurnClose:
			closeAt = i
		case marotte.EntryKindSteer:
			if steerAt >= 0 {
				t.Errorf("a second steer entry %q, want exactly one", e.ID)
			}
			steerAt = i
			if e.ID != "s-1" {
				t.Errorf("steer id = %q, want s-1", e.ID)
			}
			if err := json.Unmarshal(e.Payload, &steer); err != nil {
				t.Fatalf("decode the steer: %v", err)
			}
		}
	}
	if steerAt < 0 {
		t.Fatalf("no steer entry after the shutdown, want a dropped/restart record of s-1")
	}
	if closeAt < 0 || steerAt < closeAt {
		t.Errorf("steer at %d, turn_close at %d, want the steer after the close", steerAt, closeAt)
	}
	want := marotte.EntrySteer{Origin: marotte.SteerOriginUser, State: marotte.SteerStateDropped, Reason: marotte.SteerReasonRestart}
	if steer.Text != "" || steer.Origin != want.Origin || steer.State != want.State || steer.Reason != want.Reason {
		t.Errorf("steer = %+v, want text-less %+v: the empty text is the reconcile signal", steer, want)
	}
}

// TestShutdown_RecordsAParkedSteerWithItsText pins that only this process has its text, so it goes in the dropped row and a Held row.
func TestShutdown_RecordsAParkedSteerWithItsText(t *testing.T) {
	cs := shutdownMidPrompt(t, func(h *Runtime) {
		if _, refuse := h.steerQueue.RouteSteer("c1", "s-park", "use tabs",
			command.SteerHolder{Held: true, PromptClass: true}); refuse != "" {
			t.Fatalf("RouteSteer refused %q", refuse)
		}
	})

	got := chatSteers(t, cs, "c1")
	if len(got) != 1 || got[0].Text != "use tabs" || got[0].State != marotte.SteerStateDropped ||
		got[0].Reason != marotte.SteerReasonRestart {
		t.Errorf("steers = %+v, want one dropped/restart carrying the parked text", got)
	}
	rows := queuedRows(t, cs, "c1")
	if len(rows) != 1 || !rows[0].Held || !rows[0].Carried() || rows[0].Text != "use tabs" {
		t.Errorf("queue = %+v, want one held carried row with the parked text", rows)
	}
}

// TestShutdown_TheCutTurnNamesTheRestartAsItsCause: the reader cancelled nothing, so
// the footer must not say they did.
func TestShutdown_TheCutTurnNamesTheRestartAsItsCause(t *testing.T) {
	cs := shutdownMidPrompt(t, nil)

	closes := closesOf(t, logOf(t, cs, "c1"))
	if len(closes) != 1 {
		t.Fatalf("turn_close entries = %d, want one", len(closes))
	}
	if closes[0].Outcome != marotte.TurnOutcomeInterrupted || closes[0].FailureReason != string(shutdownInterruptCause) {
		t.Errorf("turn_close = outcome %q reason %q, want interrupted with %q",
			closes[0].Outcome, closes[0].FailureReason, shutdownInterruptCause)
	}
}

// TestShutdown_AUserCancelCauseOutranksTheShutdownCause: the cause is first-wins, so
// one already claimed keeps its word.
func TestShutdown_AUserCancelCauseOutranksTheShutdownCause(t *testing.T) {
	const earlier marotte.InterruptCause = "an earlier cause"
	cs := shutdownMidPrompt(t, func(h *Runtime) {
		own, ok := h.coord.turns.ownTurn("c1")
		if !ok {
			t.Fatal("no own turn to claim a cause on")
		}
		if !h.coord.turns.interrupt("c1", own.ID, earlier) {
			t.Fatal("the earlier cause was not claimed")
		}
	})

	closes := closesOf(t, logOf(t, cs, "c1"))
	if len(closes) != 1 || closes[0].FailureReason != string(earlier) {
		t.Errorf("turn_close = %+v, want the earlier cause kept", closes)
	}
}

// Every reserve refuses beside a turn KAS bracketed, since each one sends a session/prompt or opens a turn; beside a
// fold-opened wire turn only the idle reserve, which reads every holder, refuses.
func TestTryReserve_BesideAWireTurn(t *testing.T) {
	cases := []struct {
		name      string
		open      func(t *testing.T, h *Runtime)
		wantBare  bool
		wantFence bool
	}{
		{
			name: "a bracketed wire turn",
			open: func(_ *testing.T, h *Runtime) { h.translateACPEvent("c1", h.originOf("c1"), newTurnStartMsg()) },
		},
		{
			name: "a fold-opened wire turn",
			open: func(t *testing.T, h *Runtime) {
				if h.stageWireTurn(t, "c1") == nil {
					t.Fatal("setup: the fold opened no wire turn")
				}
			},
			wantBare:  true,
			wantFence: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, cs, _ := newTestHub()
			seedChat(t, cs, "c1")
			tc.open(t, h)
			if _, open := ownID(h, "c1"); !open {
				t.Fatal("setup: no wire turn is open")
			}

			if h.coord.TryReserveIdleTurn("c1", marotte.TurnSourceLocalShell) {
				t.Error("TryReserveIdleTurn reserved beside an open wire turn, want refused")
				h.coord.ReleaseTurnReservation("c1")
			}
			got := h.coord.TryReserveTurn("c1", marotte.TurnSourcePrompt)
			if got {
				h.coord.ReleaseTurnReservation("c1")
			}
			if got != tc.wantBare {
				t.Errorf("TryReserveTurn = %t, want %t", got, tc.wantBare)
			}
			got = h.coord.TryReserveTurnFenced("c1", marotte.TurnSourcePrompt, command.TurnFence{})
			if got {
				h.coord.ReleaseTurnReservation("c1")
			}
			if got != tc.wantFence {
				t.Errorf("TryReserveTurnFenced = %t, want %t", got, tc.wantFence)
			}
		})
	}
}

func TestTryReserveIdleTurn_TakesAnIdleSlotAndRefusesAHeldOne(t *testing.T) {
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1")
	if !h.coord.TryReserveIdleTurn("c1", marotte.TurnSourceLocalShell) {
		t.Fatal("TryReserveIdleTurn(c1) refused an idle chat")
	}
	if src, held := h.coord.AdmissionHolderSource("c1"); !held || src != marotte.TurnSourceLocalShell {
		t.Errorf("after TryReserveIdleTurn the holder is %v (held %t), want local_shell", src, held)
	}
	if h.coord.TryReserveIdleTurn("c1", marotte.TurnSourcePrompt) {
		t.Error("a second TryReserveIdleTurn(c1) took a held slot")
	}
}

// A turn KAS starts itself (a send_message wake) holds no reservation, yet a prompt beside it must answer Busy: KAS
// aborts its running turn on any session/prompt. A fold-opened wire turn admits: no turn_end is owed for it, so a
// refusal would last until a later turn_start or the bridge's death.
func TestReserveTurnForPrompt_BesideAWireTurn(t *testing.T) {
	cases := []struct {
		name string
		open func(t *testing.T, h *Runtime)
		want command.AdmissionOutcome
	}{
		{
			name: "a bracketed wire turn answers busy",
			open: func(_ *testing.T, h *Runtime) { h.translateACPEvent("c1", h.originOf("c1"), newTurnStartMsg()) },
			want: command.AdmissionBusy,
		},
		{
			name: "a bracketed wire turn that ended admits",
			open: func(_ *testing.T, h *Runtime) {
				h.translateACPEvent("c1", h.originOf("c1"), newTurnStartMsg())
				h.translateACPEvent("c1", h.originOf("c1"), newTurnEndMsg("end_turn"))
			},
			want: command.AdmissionAcquired,
		},
		{
			name: "a fold-opened wire turn admits",
			open: func(t *testing.T, h *Runtime) {
				if h.stageWireTurn(t, "c1") == nil {
					t.Fatal("setup: the fold opened no wire turn")
				}
			},
			want: command.AdmissionAcquired,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, cs, _ := newTestHub()
			seedChat(t, cs, "c1")
			tc.open(t, h)

			got := h.coord.ReserveTurnForPrompt(t.Context(), "c1", 60*time.Millisecond)
			if got == command.AdmissionAcquired {
				h.coord.ReleaseTurnReservation("c1")
			}
			if got != tc.want {
				t.Errorf("ReserveTurnForPrompt = %v, want %v", got, tc.want)
			}
		})
	}
}

// wakeTurnHub is a chat with a live bridge and a turn KAS opened itself, nothing streamed into it yet.
func wakeTurnHub(t *testing.T) (*Runtime, *testChatStore, *fakeBridge) {
	t.Helper()
	s, cs, br := steerOpsHub(t)
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnStartMsg())
	if _, open := ownID(s.h, "c1"); !open {
		t.Fatal("setup: a turn_start with nothing pending opened no wire turn")
	}
	return s.h, cs, br
}

// The composer's Send beside an agent-initiated turn answers the plain 409 the client steers or queues on, and
// reaches neither the log nor KAS.
func TestCmdPrompt_BesideAWireTurnAnswersThePlainBusyAndSendsNothing(t *testing.T) {
	h, cs, br := wakeTurnHub(t)
	opens := len(opensOf(t, logOf(t, cs, "c1")))

	rec := postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdPrompt, ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	})
	joinInflight(t, h)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body %q: %v", rec.Body.String(), err)
	}
	if body.Reason != "" {
		t.Errorf("reason = %q, want the plain 409 the client converts into its busy verb", body.Reason)
	}
	if got := len(opensOf(t, logOf(t, cs, "c1"))); got != opens {
		t.Errorf("turn_open entries = %d, want %d: a refused prompt opens no turn", got, opens)
	}
	if slices.Contains(br.callLog(), marotte.MethodPrompt) {
		t.Errorf("bridge calls = %v, want no session/prompt beside the running turn", br.callLog())
	}
}

// A `!cmd` beside an agent-initiated turn is refused as busy: its turn cannot open beside an open one.
func TestShell_BesideAWireTurnIsRefusedAsBusy(t *testing.T) {
	h, cs, _ := wakeTurnHub(t)
	opens := len(opensOf(t, logOf(t, cs, "c1")))

	rec := postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdPrompt, ChatID: "c1",
		Payload: json.RawMessage(`{"text":"!echo hi","message_id":"m-1"}`),
	})

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if got := len(opensOf(t, logOf(t, cs, "c1"))); got != opens {
		t.Errorf("turn_open entries = %d, want %d", got, opens)
	}
}

// mergeHub is a hub whose engine lists the tangent "c-tangent" (session s-fork) as forked from the parent "c1"
// (s-parent) and answers a prompt with a summary.
func mergeHub(t *testing.T) (*Runtime, *testChatStore, func(method string) int) {
	t.Helper()
	h, cs, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	br.callResults = map[string]json.RawMessage{
		marotte.MethodSessionList: sessionRowsJSON(t, map[string]string{"s-fork": "s-parent", "s-parent": ""}),
	}
	br.chunksOnCall = map[string][]string{marotte.MethodPrompt: {"Found it."}}
	seedChain(t, cs, "c1", "s-parent")
	seedChain(t, cs, "c-tangent", "s-fork")
	return h, cs, func(method string) int { return countCalls(br, method) }
}

func mergeCmd(tangent marotte.ChatID) marotte.ClientCommand {
	return marotte.ClientCommand{Type: marotte.CmdMergeTangent, ChatID: tangent, Payload: json.RawMessage(`{"op_id":"op-1"}`)}
}

// Merging a tangent while KAS runs a turn of its own there is refused as busy, like any running turn: the summary
// prompt would abort it.
func TestMergeTangent_ATangentRunningAWakeTurnIsRefusedBusy(t *testing.T) {
	h, _, calls := mergeHub(t)
	h.translateACPEvent("c-tangent", h.originOf("c-tangent"), newTurnStartMsg())

	rec := postCmd(t, h, mergeCmd("c-tangent"))

	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"busy"`) {
		t.Errorf("merge_tangent = %d %s, want 409 busy", rec.Code, rec.Body.String())
	}
	if n := calls(marotte.MethodPrompt); n != 0 {
		t.Errorf("session/prompt calls = %d, want none beside the running turn", n)
	}
}

// The findings for a parent running a turn KAS started are queued behind it, never prompted into it.
func TestMergeTangent_QueuesTheFindingsBehindAParentsWakeTurn(t *testing.T) {
	h, cs, calls := mergeHub(t)
	h.translateACPEvent("c1", h.originOf("c1"), newTurnStartMsg())

	if rec := postCmd(t, h, mergeCmd("c-tangent")); rec.Code != http.StatusOK {
		t.Fatalf("merge_tangent = %d %s, want 200", rec.Code, rec.Body.String())
	}
	waitFor(t, func() bool {
		c, _ := cs.Get(t.Context(), "c-tangent")
		return len(c.TangentMerges) == 1 && c.TangentMerges[0].State != marotte.TangentMergeRunning
	})

	if c, _ := cs.Get(t.Context(), "c-tangent"); c.TangentMerges[0].State != marotte.TangentMergeSucceeded {
		t.Errorf("merge state = %q, want succeeded", c.TangentMerges[0].State)
	}
	rows := queuedRows(t, cs, "c1")
	if len(rows) != 1 || !strings.Contains(rows[0].Text, "Found it.") {
		t.Errorf("parent's queue = %+v, want the findings queued", rows)
	}
	if n := calls(marotte.MethodPrompt); n != 1 {
		t.Errorf("session/prompt calls = %d, want the tangent's summary only", n)
	}
	if ids := promptIDs(t, cs, "c1"); len(ids) != 0 {
		t.Errorf("parent prompt turns = %v, want none beside the running turn", ids)
	}
}

// A steer while the chat's session door is still opening is parked for the session, never sent on a record that
// holds none.
func TestPromptTurn_SteerInTheSessionDoorWindowIsAccepted(t *testing.T) {
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1")
	entered, gate := gateSessionOpen(h)

	if rec := postCmd(t, h, marotte.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	<-entered

	rec := postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdSteer, ChatID: "c1",
		Payload: json.RawMessage(`{"text":"also this","message_id":"m-2"}`),
	})
	if rec.Code >= http.StatusInternalServerError {
		t.Errorf("steer in the door window = %d %s, want an answer the client can act on", rec.Code, rec.Body.String())
	}
	close(gate)
	// The fake engine never takes the steer up, so the first turn's end sends it as the next prompt.
	waitForTurnClosed(t, h, 2)
	opens := opensOf(t, logOf(t, cs, "c1"))
	if len(opens) != 2 || opens[1].Prompt == nil || !strings.Contains(opens[1].Prompt.Text, "also this") {
		t.Errorf("turn_open entries = %+v, want the prompt's turn then one carrying the parked steer", opens)
	}
}
