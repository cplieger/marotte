package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// ackAs is how a prompt's 200 answer says it was placed: "started", "steered" or "queued".
func ackAs(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var ack struct {
		As     string `json:"as"`
		Queued bool   `json:"queued"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &ack) != nil {
		return rec.Result().Status + " " + rec.Body.String()
	}
	switch {
	case ack.As != "":
		return ack.As
	case ack.Queued:
		return "queued"
	}
	return "started"
}

// staysFalse fails when cond turns true within d: a bounded look for a write that must not happen.
func staysFalse(t *testing.T, d time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			t.Error(what)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func cancelCmd(payload string) marotte.ClientCommand {
	return marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1", Payload: json.RawMessage(payload)}
}

// Sent as a queue, a saved prompt or context reference sent mid-turn waits behind the turn and goes as a
// session/prompt of its own when the turn ends, never as a steer, so its context reference resolves.
func TestRunOrder_APromptOnlyMessageQueuedBehindARunningTurnGoesAsItsOwnPrompt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("remember the milk\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, cs, br, _ := idleAfterFirstTurn(t, dir)
	turnID, _ := h.stagePromptTurn(t, "c1")
	const text = "/review #[[file:notes.txt]]"

	rec := postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdPrompt, ChatID: "c1",
		Payload: json.RawMessage(`{"text":"` + text + `","message_id":"m-ref","prompt_only":true,"interrupt_mode":"queue"}`),
	})
	if got := ackAs(t, rec); got != "queued" {
		t.Fatalf("prompt-only send behind a running turn in queue mode answered %s, want queued", got)
	}
	h.translateACPEvent("c1", h.originOf("c1"), newChunkMsg("an answer"))
	endTurn(t, h, "c1", turnID)

	waitFor(t, func() bool { return slices.Contains(promptIDs(t, cs, "c1"), "m-ref") })
	if n := steerWrites(br); n != 0 {
		t.Errorf("_session/steer written %d times, want none", n)
	}
	waitFor(t, func() bool { return br.paramsFor(marotte.MethodPrompt)["messageId"] == "m-ref" })
	blocks, _ := br.paramsFor(marotte.MethodPrompt)[marotte.KeyPrompt].([]map[string]any)
	if len(blocks) == 0 || blocks[0]["text"] != text {
		t.Errorf("prompt blocks = %v, want text block 0 = %q as typed, for KAS to expand", blocks, text)
	}
	resolved := slices.ContainsFunc(blocks, func(b map[string]any) bool {
		res, ok := b["resource"].(map[string]any)
		body, _ := res["text"].(string)
		return ok && strings.Contains(body, "remember the milk")
	})
	if !resolved {
		t.Errorf("prompt blocks = %v, want a resource block with notes.txt's text", blocks)
	}
}

// Where a send would steer, a saved prompt is refused 409 prompt_only and nothing reaches _session/steer.
func TestRunOrder_APromptOnlyMessageThatWouldSteerIsRefused(t *testing.T) {
	t.Parallel()
	h, _, br, _ := idleAfterFirstTurn(t, "/tmp/work")
	h.stagePromptTurn(t, "c1")

	rec := postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdPrompt, ChatID: "c1",
		Payload: json.RawMessage(`{"text":"/review","message_id":"m-cmd","prompt_only":true}`),
	})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"prompt_only"`) {
		t.Errorf("prompt-only send into a steerable turn = %d %s, want 409 prompt_only", rec.Code, rec.Body.String())
	}
	if n := steerWrites(br); n != 0 {
		t.Errorf("_session/steer written %d times, want none", n)
	}
}

// A prompt sent under a stop's KAS hold is answered at once as queued, and goes once kiro-cli reports idle.
func TestRunOrder_APromptUnderAStopsHoldIsQueuedAtOnce(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
		defer shutdownHub(t, h)
		stopUnderHold(t, h)

		first := postAsync(h, promptCmd("m-p1", "sent under the hold"))
		synctest.Wait()
		select {
		case rec := <-first:
			if got := ackAs(t, rec); got != "queued" {
				t.Errorf("prompt under the hold answered %s, want queued", got)
			}
		default:
			t.Error("the prompt under the hold is unanswered, want it queued at once")
		}
		h.coord.WireTurnActivity("c1", false)
		waitFor(t, func() bool { return slices.Contains(promptIDs(t, cs, "c1"), "m-p1") })
	})
}

// eventually reports whether cond turns true within 5s.
func eventually(cond func() bool) bool {
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
	return true
}

// A row's up arrow pressed after the turn it would stop has closed stops nothing: the row the close started runs on.
// A press with no target keeps the older contract and stops the running turn.
func TestRunOrder_ASendNowPressedAfterItsTurnClosedStopsNothing(t *testing.T) {
	t.Parallel()
	t.Run("named turn closed", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		open := promptGates(t, br)
		if code := postPrompt(t, h, "a"); code != http.StatusOK {
			t.Fatalf("Setup: prompt = %d", code)
		}
		queueDuring(t, h, "m-q1", "m-q2")

		open(0)
		waitFor(t, func() bool { return slices.Contains(promptIDs(t, cs, "c1"), "m-q1") })
		rec := postCmd(t, h, marotte.ClientCommand{
			Type: marotte.CmdSendQueuedNow, ChatID: "c1", Payload: json.RawMessage(`{"message_id":"m-q2","stop":"m-a"}`),
		})
		if rec.Code != http.StatusOK {
			t.Errorf("send_queued_now = %d %s, want 200", rec.Code, rec.Body.String())
		}
		if got := br.notified(marotte.MethodCancel); len(got) != 0 {
			t.Errorf("session/cancel sent %d times, want none: the turn the press named had closed", len(got))
		}
		open(1)
		open(2)
		waitFor(t, func() bool { return opensInOrder(t, cs, "m-a", "m-q1", "m-q2") })
	})
	t.Run("no target", func(t *testing.T) {
		h, _, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		h.stagePromptTurn(t, "c1")
		queueDuring(t, h, "m-q1", "m-q2")
		rec := postCmd(t, h, marotte.ClientCommand{
			Type: marotte.CmdSendQueuedNow, ChatID: "c1", Payload: json.RawMessage(`{"message_id":"m-q2"}`),
		})
		if rec.Code != http.StatusOK {
			t.Errorf("send_queued_now = %d %s, want 200", rec.Code, rec.Body.String())
		}
		waitFor(t, func() bool { return len(br.notified(marotte.MethodCancel)) == 1 })
	})
	t.Run("empty stop", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		h.stagePromptTurn(t, "c1")
		queueDuring(t, h, "m-q1", "m-q2")
		rec := postCmd(t, h, marotte.ClientCommand{
			Type: marotte.CmdSendQueuedNow, ChatID: "c1", Payload: json.RawMessage(`{"message_id":"m-q2","stop":""}`),
		})
		if rec.Code != http.StatusOK {
			t.Errorf("send_queued_now = %d %s, want 200", rec.Code, rec.Body.String())
		}
		staysFalse(t, 100*time.Millisecond, func() bool { return len(br.notified(marotte.MethodCancel)) > 0 },
			"a press naming no turn stopped the lone running turn")
		got := runStates(t, cs, "c1")
		if len(got) != 3 || got[1] != "m-q2 pending" || got[2] != "m-q1 pending" {
			t.Errorf("list = %v, want the running turn, then m-q2 and m-q1 pending", got)
		}
	})
}

// A prompt sent while kiro-cli reports a run marotte has no turn for is answered at once as queued, and goes when
// kiro-cli reports idle.
func TestRunOrder_APromptBehindKiroCLIsRunAnswersQueuedAtOnce(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
		defer shutdownHub(t, h)
		h.coord.WireTurnActivity("c1", true)

		answered := postAsync(h, promptCmd("m-new", "behind kiro-cli's run"))
		synctest.Wait()
		select {
		case rec := <-answered:
			if got := ackAs(t, rec); got != "queued" {
				t.Errorf("prompt behind kiro-cli's run answered %s, want queued", got)
			}
		default:
			t.Error("the prompt behind kiro-cli's run is unanswered, want it queued at once")
			h.coord.WireTurnActivity("c1", false)
			<-answered
			return
		}
		h.coord.WireTurnActivity("c1", false)
		waitFor(t, func() bool { return slices.Contains(promptIDs(t, cs, "c1"), "m-new") })
	})
}

// emptyTurnHub is chat c1 on bridges minted fresh per session; the first session's session/prompt answer waits for
// answerFirst. minted lists the sessions opened, in order.
func emptyTurnHub(t *testing.T) (h *Runtime, cs *testChatStore, minted func() []*fakeBridge, answerFirst chan struct{}) {
	t.Helper()
	answerFirst = make(chan struct{})
	var (
		mu  sync.Mutex
		all []*fakeBridge
	)
	factory := func() engineSession {
		mu.Lock()
		defer mu.Unlock()
		br := newFakeBridge()
		if len(all) == 0 {
			br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: answerFirst}
			// Only the first prompt is empty: every later one, on this session or the next, answers with words.
			var prompts atomic.Int32
			br.setOnCall(func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
				if method != marotte.MethodPrompt || prompts.Add(1) == 1 {
					return nil, nil, false
				}
				return json.RawMessage(`{"stopReason":"end_turn"}`), []*marotte.RPCResponse{newSessionChunkMsg(br.sessionID, "done")}, true
			})
		} else {
			br.chunksOnCall = map[string][]string{marotte.MethodPrompt: {"done"}}
		}
		all = append(all, br)
		return br
	}
	cs = newTestChatStore()
	h = newRuntimeOn(context.Background(), t.TempDir(), fakeEngineOf(factory, nil), cs)
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	awaitBoot(h)
	seedChat(t, cs, "c1")
	return h, cs, func() []*fakeBridge {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(all)
	}, answerFirst
}

// The re-send of a prompt whose turn produced nothing goes after the messages queued during that turn, never ahead of
// them: they run first, in order, then the retry on a fresh session.
func TestRunOrder_AnEmptyTurnsRetryNeverOvertakesQueuedRows(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h, cs, minted, answerFirst := emptyTurnHub(t)
		if code := postPrompt(t, h, "tidy"); code != http.StatusOK {
			t.Fatalf("Setup: prompt = %d", code)
		}
		synctest.Wait()
		first := minted()[0]
		first.deliver(first.promptTurnStartMsg())
		queueDuring(t, h, "m-q1", "m-q2")
		first.deliver(newTurnEndMsg("end_turn"))
		close(answerFirst)
		synctest.Wait()

		want := []string{"m-tidy", "m-q1", "m-q2", "m-tidy"}
		if !eventually(func() bool { return len(promptIDs(t, cs, "c1")) >= len(want) && !h.coord.turns.live("c1") }) ||
			!slices.Equal(promptIDs(t, cs, "c1"), want) {
			t.Errorf("turns opened = %v (sessions %d), want %v", promptIDs(t, cs, "c1"), len(minted()), want)
		}
	})
}

// A chat closed while a granted take's turn has not opened yet answers that POST chat_closed and leaves no entry for
// its message, whatever stood beside or behind it.
func TestRunOrder_AChatClosedDuringAGrantedTakeLeavesNoEntry(t *testing.T) {
	for _, shape := range []string{"beside a fold-opened turn", "on an idle list"} {
		inBubble(t, shape, func(t *testing.T) {
			h, cs, br, tab := closeTabHub(t)
			t.Cleanup(func() { shutdownHub(t, h) })
			br.chunksOnCall = map[string][]string{marotte.MethodPrompt: {"done"}}
			runPrompt(t, h, cs, "c1", "m-first")
			if shape == "beside a fold-opened turn" {
				br.deliver(newChunkMsg("kiro-cli on its own"))
				waitFor(t, func() bool { return h.coord.turns.live("c1") })
			}
			entered, releaseOpen := make(chan struct{}), make(chan struct{})
			var held atomic.Bool
			h.coord.turns.openHook = func() {
				if held.CompareAndSwap(false, true) {
					close(entered)
					<-releaseOpen
				}
			}
			answered := postAsync(h, promptCmd("m-x", "taken, then closed"))
			<-entered
			closed := make(chan int, 1)
			go func() { closed <- postCloseTab(t, h, tab) }()
			// The close is waiting on the take before the take's open goes on.
			synctest.Wait()
			close(releaseOpen)

			rec := <-answered
			var body struct {
				Reason string `json:"reason"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &body)
			if rec.Code != http.StatusConflict || body.Reason != "chat_closed" {
				t.Errorf("POST = %d %s, want 409 chat_closed", rec.Code, rec.Body.String())
			}
			if code := <-closed; code != http.StatusOK {
				t.Errorf("close = %d, want 200", code)
			}
			waitFor(t, func() bool { return !h.coord.turns.live("c1") })
			if got := runIDs(t, cs, "c1"); slices.Contains(got, "m-x") {
				t.Errorf("chat's list after the close = %v, want no entry for m-x", got)
			}
		})
	}
}

// A steer kiro-cli had not read when its turn closed clean is the next prompt once its end resolves, and no prompt
// goes before: until the clear lands, kiro-cli may still hold it.
func TestRunOrder_ASteerTheAgentDroppedAtACleanCloseIsSentNext(t *testing.T) {
	t.Parallel()
	s, cs, br := steerOpsHub(t)
	at := s.h.originOf("c1")
	id, _ := s.h.stagePromptTurn(t, "c1")
	s.h.translateACPEvent("c1", at, newPromptTurnStartMsg(id))
	if code := postSteer(t, s.h, "a", "dropped at the close"); code != http.StatusOK {
		t.Fatalf("Setup: steer = %d, want 200", code)
	}
	br.setCallResult(marotte.MethodSessionSteerClear, clearReply([]string{"steer-a"}))
	release := holdCall(t, br, marotte.MethodSessionSteerClear)

	s.h.translateACPEvent("c1", at, newTurnEndMsg("end_turn"))
	s.h.coord.releaseTurn("c1", id)
	waitForCall(t, br, marotte.MethodSessionSteerClear)
	staysFalse(t, 200*time.Millisecond, func() bool { return slices.Contains(br.callLog(), marotte.MethodPrompt) },
		"a session/prompt went out before the steer's end resolved")
	release()

	if p := resendPrompt(t, cs); p == nil || p.Text != "dropped at the close" {
		t.Errorf("next prompt = %+v, want the unread steer's text", p)
	}
}

// A rewind refused under a held close gate leaves nothing on the list, and nothing reverts once the gate lifts.
func TestRunOrder_ARewindUnderAHeldCloseGateIsRefusedAndNeverRuns(t *testing.T) {
	shrinkAdmissionWait(t, 60*time.Millisecond)
	h, cs, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	openChat(t, h, "c1")
	runPrompt(t, h, cs, "c1", "m-1")
	br.setCallResult(marotte.MethodCheckpointRevertMultiple, json.RawMessage(`{"success":true}`))
	hold := h.coord.turns.gates.hold("c1")

	rec := rewindTo(t, h, "m-1")

	if code, reason := rewindAnswer(t, rec); code != http.StatusConflict || reason != "busy" {
		t.Errorf("rewind under a held close gate = %d reason %q (%s), want 409 busy", code, reason, rec.Body.String())
	}
	if listLive(h, "c1") {
		t.Errorf("the list after the refusal = %v, want no live entry", runStates(t, cs, "c1"))
	}
	hold.release()
	staysFalse(t, 200*time.Millisecond, func() bool { return slices.Contains(br.callLog(), marotte.MethodCheckpointRevertMultiple) },
		"the refused rewind reverted once the gate lifted")
	res, ok := h.coord.turns.tryReserve("c1", marotte.TurnSourcePrompt, h.coord.LifeStamp())
	if !ok {
		t.Error("the chat's admission slot is still held after the refused rewind")
		return
	}
	h.coord.ReleaseTurnReservation(res)
}

// takenRowHub is chat c1 with m-a running and m-q1, m-q2 queued behind it; once m-a ends clean the list takes m-q1
// and its launch parks in openTurn (held) until releaseOpen is closed. open opens the n-th session/prompt from m-a on.
func takenRowHub(t *testing.T) (h *Runtime, cs *testChatStore, br *fakeBridge, open func(int), held <-chan struct{}, releaseOpen chan struct{}) {
	t.Helper()
	h, cs, br, _ = idleAfterFirstTurn(t, "/tmp/work")
	open = promptGates(t, br)
	if code := postPrompt(t, h, "a"); code != http.StatusOK {
		t.Fatalf("Setup: prompt = %d", code)
	}
	queueDuring(t, h, "m-q1", "m-q2")
	entered, releaseOpen := make(chan struct{}), make(chan struct{})
	var once atomic.Bool
	h.coord.turns.openHook = func() {
		if once.CompareAndSwap(false, true) {
			close(entered)
			<-releaseOpen
		}
	}
	t.Cleanup(func() {
		select {
		case <-releaseOpen:
		default:
			close(releaseOpen)
		}
	})
	return h, cs, br, open, entered, releaseOpen
}

// A Cancel of a taken row whose turn has not opened withdraws it: it goes back paused with the row behind it, opens
// no turn, writes nothing, and both wait for the next clean turn end.
func TestRunOrder_ACancelBeforeATakenRowsTurnOpensPutsItBackPaused(t *testing.T) {
	t.Parallel()
	h, cs, br, open, held, releaseOpen := takenRowHub(t)
	open(0)
	<-held

	if rec := postCmd(t, h, cancelCmd(`{"target":"m-q1"}`)); rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s", rec.Code, rec.Body.String())
	}
	if got := runStates(t, cs, "c1"); !slices.Equal(got, []string{"m-q1 paused", "m-q2 paused"}) {
		t.Errorf("list after the cancel = %v, want [m-q1 paused, m-q2 paused]", got)
	}
	close(releaseOpen)
	staysFalse(t, 200*time.Millisecond, func() bool { return slices.Contains(promptIDs(t, cs, "c1"), "m-q1") },
		"the withdrawn row opened a turn")
	if n := len(br.notified(marotte.MethodCancel)); n != 0 {
		t.Errorf("session/cancel sent %d times, want none: no turn was open", n)
	}

	if got := ackAs(t, postCmd(t, h, promptCmd("m-new", "after the cancel"))); got != "started" {
		t.Errorf("the next prompt answered %s, want started", got)
	}
	open(1)
	open(2)
	open(3)
	if !eventually(func() bool { return opensInOrder(t, cs, "m-a", "m-new", "m-q1", "m-q2") }) {
		t.Errorf("turns opened = %v, want m-a, m-new, m-q1, m-q2", promptIDs(t, cs, "c1"))
	}
}

// A Cancel of a taken row whose turn opened but was not sent closes that one turn cancelled and sends nothing; the
// row behind it waits paused for the next clean turn end.
func TestRunOrder_ACancelAfterATakenRowsTurnOpenedLeavesOneCancelledTurn(t *testing.T) {
	t.Parallel()
	h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
	open := promptGates(t, br)
	// The hooks are set before m-a's turn, whose send gate reads them; they arm at the next open, m-q1's.
	var opens atomic.Int32
	var parked atomic.Bool
	entered, releaseSend := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		if parked.Load() {
			select {
			case <-releaseSend:
			default:
				close(releaseSend)
			}
		}
	})
	h.coord.turns.openHook = func() { opens.Add(1) }
	h.coord.turns.claimHook = func() {
		if opens.Load() >= 2 && parked.CompareAndSwap(false, true) {
			close(entered)
			<-releaseSend
		}
	}
	if code := postPrompt(t, h, "a"); code != http.StatusOK {
		t.Fatalf("Setup: prompt = %d", code)
	}
	queueDuring(t, h, "m-q1", "m-q2")
	open(0)
	<-entered
	if !slices.Contains(promptIDs(t, cs, "c1"), "m-q1") {
		t.Fatalf("Setup: m-q1's turn is not open at its send gate; turns = %v", promptIDs(t, cs, "c1"))
	}

	if rec := postCmd(t, h, cancelCmd(`{"target":"m-q1"}`)); rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s", rec.Code, rec.Body.String())
	}
	close(releaseSend)
	waitFor(t, func() bool { return !listLive(h, "c1") })
	if got := runStates(t, cs, "c1"); !slices.Equal(got, []string{"m-q2 paused"}) {
		t.Errorf("list after the close = %v, want [m-q2 paused]", got)
	}
	if n := countOf(br.callLog(), marotte.MethodPrompt); n != 2 {
		t.Errorf("session/prompt written %d times, want 2 (m-first, m-a): m-q1 was stopped unsent", n)
	}

	if got := ackAs(t, postCmd(t, h, promptCmd("m-new", "after the cancel"))); got != "started" {
		t.Errorf("the next prompt answered %s, want started", got)
	}
	open(1)
	open(2)
	if !eventually(func() bool { return opensInOrder(t, cs, "m-a", "m-q1", "m-new", "m-q2") }) {
		t.Errorf("turns opened = %v, want m-a, m-q1, m-new, m-q2", promptIDs(t, cs, "c1"))
	}
	if n := countOf(promptIDs(t, cs, "c1"), "m-q1"); n != 1 {
		t.Errorf("m-q1 opened %d turns, want 1", n)
	}
}

// reopenOnFreshSession reopens c1's tab with the engine minting a new session that answers every prompt.
func reopenOnFreshSession(t *testing.T, h *Runtime) {
	t.Helper()
	next := newFakeBridge()
	next.chunksOnCall = map[string][]string{marotte.MethodPrompt: {"done"}}
	fakeSourceOf(t, h).setFactory(func() engineSession { return next })
	reopenTab(t, h, "op-reopen")
}

// A row queued when its chat closed is carried into the next life paused, and is sent after that life's first clean
// turn end.
func TestRunOrder_ARowCarriedIntoALaterLifeIsSentAfterItsFirstCleanTurn(t *testing.T) {
	t.Parallel()
	h, cs, br, tab := closeTabHub(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	br.chunksOnCall = map[string][]string{marotte.MethodPrompt: {"done"}}
	runPrompt(t, h, cs, "c1", "m-first")
	h.stagePromptTurn(t, "c1")
	queueDuring(t, h, "m-q1")
	if code := postCloseTab(t, h, tab); code != http.StatusOK {
		t.Fatalf("close = %d", code)
	}
	awaitTeardown(t, h, "c1")
	reopenOnFreshSession(t, h)

	if got := runStates(t, cs, "c1"); !slices.Equal(got, []string{"m-q1 paused"}) {
		t.Errorf("list in the next life = %v, want [m-q1 paused]", got)
	}
	if got := ackAs(t, postCmd(t, h, promptCmd("m-new", "in the next life"))); got != "started" {
		t.Errorf("the next life's prompt answered %s, want started", got)
	}
	if !eventually(func() bool { return opensInOrder(t, cs, "m-first", "m-new", "m-q1") }) {
		t.Errorf("turns opened = %v, want m-first, m-new, m-q1", promptIDs(t, cs, "c1"))
	}
}

// liveRunCancel is the entry a Cancel with no target stops, as the header names it.
func liveRunCancel(t *testing.T, cs *testChatStore) string {
	t.Helper()
	return runView(t, cs, "c1").Cancel
}

// A turn kiro-cli starts while marotte's sent prompt is not bound yet is listed ahead of it; Cancel names it and stops
// both; the prompt's own turn_start then closes it and the prompt is the head.
func TestRunOrder_AWireTurnBesideASentUnboundPromptIsOnTheList(t *testing.T) {
	t.Parallel()
	h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
	open := promptGates(t, br)
	if got := ackAs(t, postCmd(t, h, promptCmd("m-1", "sent, not bound"))); got != "started" {
		t.Fatalf("Setup: prompt answered %s", got)
	}
	waitFor(t, func() bool { return countOf(br.callLog(), marotte.MethodPrompt) == 2 })
	br.deliver(newPromptTurnStartMsg("another-tag"))
	waitFor(t, func() bool { return slices.Contains(runStates(t, cs, "c1"), "wire running") })

	if got := runStates(t, cs, "c1"); !slices.Equal(got, []string{"wire running", "m-1 running"}) {
		t.Errorf("list = %v, want [wire running, m-1 running]", got)
	}
	target := liveRunCancel(t, cs)
	if target == "" || target == "m-1" {
		t.Fatalf("run.cancel = %q, want the wire turn", target)
	}
	if rec := postCmd(t, h, cancelCmd(`{"target":"`+target+`"}`)); rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s", rec.Code, rec.Body.String())
	}
	waitFor(t, func() bool { return len(br.notified(marotte.MethodCancel)) == 1 })
	if got := runStates(t, cs, "c1"); !slices.Equal(got, []string{"wire stopping", "m-1 stopping"}) {
		t.Errorf("list after the cancel = %v, want both stopping", got)
	}
	br.deliver(br.promptTurnStartMsg())
	waitFor(t, func() bool {
		got := runStates(t, cs, "c1")
		return len(got) > 0 && strings.HasPrefix(got[0], "m-1 ")
	})
	if n := len(br.notified(marotte.MethodCancel)); n != 1 {
		t.Errorf("session/cancel sent %d times, want 1 for both turns", n)
	}
	open(0)
}

// A Cancel naming the sent prompt beside kiro-cli's turn (a page that has not seen the wire turn's header yet) stops
// both turns as the wire turn's id does, and the message queued behind them waits for the next turn.
func TestRunOrder_ACancelNamingThePromptBesideAWireTurnPausesTheRows(t *testing.T) {
	t.Parallel()
	h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
	open := promptGates(t, br)
	if got := ackAs(t, postCmd(t, h, promptCmd("m-1", "sent, not bound"))); got != "started" {
		t.Fatalf("Setup: prompt answered %s", got)
	}
	waitFor(t, func() bool { return countOf(br.callLog(), marotte.MethodPrompt) == 2 })
	br.deliver(newPromptTurnStartMsg("another-tag"))
	waitFor(t, func() bool { return slices.Contains(runStates(t, cs, "c1"), "wire running") })
	queueDuring(t, h, "m-q1")
	if got := runStates(t, cs, "c1"); !slices.Equal(got, []string{"wire running", "m-1 running", "m-q1 pending"}) {
		t.Fatalf("Setup: list = %v, want [wire running, m-1 running, m-q1 pending]", got)
	}

	if rec := postCmd(t, h, cancelCmd(`{"target":"m-1"}`)); rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s", rec.Code, rec.Body.String())
	}
	waitFor(t, func() bool { return len(br.notified(marotte.MethodCancel)) == 1 })
	if got := runStates(t, cs, "c1"); !slices.Equal(got, []string{"wire stopping", "m-1 stopping", "m-q1 paused"}) {
		t.Errorf("list after the cancel = %v, want both stopping and m-q1 paused", got)
	}
	br.setCallResult(marotte.MethodPrompt, json.RawMessage(`{"stopReason":"cancelled"}`))
	br.deliver(br.promptTurnStartMsg())
	open(0)
	waitFor(t, func() bool {
		got := runStates(t, cs, "c1")
		return len(got) == 1 && got[0] == "m-q1 paused"
	})
	staysFalse(t, 200*time.Millisecond, func() bool { return slices.Contains(promptIDs(t, cs, "c1"), "m-q1") },
		"the row a stopped turn left waiting was sent before the next turn")

	br.setCallResult(marotte.MethodPrompt, json.RawMessage(`{"stopReason":"end_turn"}`))
	if got := ackAs(t, postCmd(t, h, promptCmd("m-next", "the next turn"))); got != "started" {
		t.Errorf("the next prompt answered %s, want started", got)
	}
	open(1)
	open(2)
	if !eventually(func() bool { return opensInOrder(t, cs, "m-first", "m-1", "m-next", "m-q1") }) {
		t.Errorf("turns opened = %v, want m-first, m-1, m-next, m-q1", promptIDs(t, cs, "c1"))
	}
}

// Cancel with kiro-cli running a turn marotte has none for stops that run with one session/cancel and pauses the
// message queued behind it, as any Stop pauses the queue: kiro-cli's idle sends nothing, and Resume sends it.
func TestRunOrder_CancelStopsKiroCLIsRunWithNoTurnHere(t *testing.T) {
	t.Parallel()
	h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
	h.coord.WireTurnActivity("c1", true)
	waitFor(t, func() bool { return slices.Contains(runStates(t, cs, "c1"), "run running") })
	if got := ackAs(t, postCmd(t, h, promptCmd("m-new", "behind kiro-cli's run"))); got != "queued" {
		t.Errorf("the prompt behind kiro-cli's run answered %s, want queued", got)
	}
	target := liveRunCancel(t, cs)
	if !strings.HasPrefix(target, "run-") {
		t.Fatalf("run.cancel = %q, want the run entry", target)
	}

	if rec := postCmd(t, h, cancelCmd(`{"target":"`+target+`"}`)); rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s", rec.Code, rec.Body.String())
	}
	waitFor(t, func() bool { return len(br.notified(marotte.MethodCancel)) == 1 })
	if got := runStates(t, cs, "c1"); !slices.Equal(got, []string{"run stopping", "m-new paused"}) {
		t.Errorf("list after the cancel = %v, want [run stopping, m-new paused]", got)
	}
	h.coord.WireTurnActivity("c1", false)
	waitFor(t, func() bool { return slices.Equal(runStates(t, cs, "c1"), []string{"m-new paused"}) })
	if slices.Contains(promptIDs(t, cs, "c1"), "m-new") {
		t.Error("kiro-cli's idle sent the message the stop paused")
	}
	resumeQueue(t, h)
	waitFor(t, func() bool { return slices.Contains(promptIDs(t, cs, "c1"), "m-new") })
	if n := len(br.notified(marotte.MethodCancel)); n != 1 {
		t.Errorf("session/cancel sent %d times, want 1", n)
	}
}

// A prompt whose intent began before a close that committed is refused as closed and leaves no row behind, in that
// life or the next.
func TestRunOrder_APromptRefusedByACloseCommittedAfterItsStampLeavesNoRow(t *testing.T) {
	t.Parallel()
	h, cs, br, tab := closeTabHub(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	br.chunksOnCall = map[string][]string{marotte.MethodPrompt: {"done"}}
	runPrompt(t, h, cs, "c1", "m-first")
	stamp := h.coord.LifeStamp()
	if code := postCloseTab(t, h, tab); code != http.StatusOK {
		t.Fatalf("close = %d", code)
	}

	_, err := h.coord.Submit(t.Context(), "c1", stamp, &command.Submission{
		Origin: marotte.RunFromPrompt, Row: marotte.QueuedPrompt{ID: "m-late", Text: "stamped before the close"},
		Kind: marotte.SendPlain, Wait: true, Composer: true,
	})
	if !errors.Is(err, marotte.ErrChatClosedDuringCall) {
		t.Errorf("Submit stamped before the close = %v, want ErrChatClosedDuringCall", err)
	}
	awaitTeardown(t, h, "c1")
	reopenOnFreshSession(t, h)
	runPrompt(t, h, cs, "c1", "m-next")
	if got := runIDs(t, cs, "c1"); slices.Contains(got, "m-late") {
		t.Errorf("list = %v, want no row for the refused prompt", got)
	}
	if slices.Contains(promptIDs(t, cs, "c1"), "m-late") {
		t.Error("the refused prompt opened a turn in the next life")
	}
}
