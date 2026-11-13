package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// modePromptCmd is promptCmd naming what the send does beside a running turn.
func modePromptCmd(id, text string, mode marotte.InterruptMode) marotte.ClientCommand {
	payload, _ := json.Marshal(marotte.PromptCommand{Text: text, MessageID: id, InterruptMode: mode})
	return marotte.ClientCommand{Type: marotte.CmdPrompt, ChatID: "c1", Payload: payload}
}

// entryView is the header's view of id, the zero view for none.
func entryView(t *testing.T, cs *testChatStore, id string) marotte.RunEntryView {
	t.Helper()
	for _, e := range runView(t, cs, "c1").Entries {
		if e.ID == id {
			return e
		}
	}
	return marotte.RunEntryView{}
}

// sendNowCmd is a row's up arrow, drawn against stop ("" for a press with no target).
func sendNowCmd(id, stop string) marotte.ClientCommand {
	payload := `{"message_id":"` + id + `"}`
	if stop != "" {
		payload = `{"message_id":"` + id + `","stop":"` + stop + `"}`
	}
	return marotte.ClientCommand{Type: marotte.CmdSendQueuedNow, ChatID: "c1", Payload: json.RawMessage(payload)}
}

// steerWrites counts the _session/steer calls kiro-cli received.
func steerWrites(br *fakeBridge) int { return countOf(br.wireLog(), marotte.MethodSessionSteer) }

// heldCleanClose is c1 whose staged turn closed clean at the point while kiro-cli still reports the run: the close's
// KAS hold stands until the test reports kiro-cli idle, and the turn end's compaction waits behind it.
func heldCleanClose(t *testing.T) (h *Runtime, cs *testChatStore, br *fakeBridge, turnID string) {
	t.Helper()
	h, cs, br, _ = idleAfterFirstTurn(t, "/tmp/work")
	atThePoint(t, h, cs)
	turnID, _ = h.stagePromptTurn(t, "c1")
	h.translateACPEvent("c1", h.originOf("c1"), newChunkMsg("an answer"))
	h.coord.WireTurnActivity("c1", true)
	endTurn(t, h, "c1", turnID)
	waitFor(t, func() bool { return slices.Contains(runStates(t, cs, "c1"), "compaction pending") })
	return h, cs, br, turnID
}

// One row per head and event the design names: each states the answer, the order the header lists after it and the
// session calls kiro-cli received.
func TestRunOrder_Table(t *testing.T) {
	t.Run("a prompt during a clean close's hold queues behind the turn end's compaction, unsteered", func(t *testing.T) {
		h, cs, br, turnID := heldCleanClose(t)

		if got := ackAs(t, postCmd(t, h, promptCmd("m-p", "during the hold"))); got != "queued" {
			t.Errorf("prompt answered %s, want queued", got)
		}
		if n := steerWrites(br); n != 0 {
			t.Errorf("_session/steer written %d times, want none: the turn closed", n)
		}
		want := []string{turnID + " closing", "compaction pending", "m-p pending"}
		if got := runStates(t, cs, "c1"); !slices.Equal(got, want) {
			t.Errorf("list = %v, want %v", got, want)
		}
		br.deliver(newTurnStartMsg())
		waitFor(t, func() bool {
			got := runStates(t, cs, "c1")
			return len(got) > 0 && got[0] == "wire running"
		})
		target := liveRunCancel(t, cs)
		if rec := postCmd(t, h, cancelCmd(`{"target":"`+target+`"}`)); rec.Code != http.StatusOK {
			t.Fatalf("cancel = %d %s", rec.Code, rec.Body.String())
		}
		waitFor(t, func() bool { return len(br.notified(marotte.MethodCancel)) == 1 })
	})

	t.Run("a prompt while a revert holds the slot queues and goes once the slot is free", func(t *testing.T) {
		h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
		res, ok := h.coord.turns.tryReserve("c1", marotte.TurnSourceRevert, h.coord.LifeStamp())
		if !ok {
			t.Fatal("Setup: the slot was held")
		}
		if got := ackAs(t, postCmd(t, h, promptCmd("m-p", "beside the revert"))); got != "queued" {
			t.Errorf("prompt answered %s, want queued", got)
		}
		staysFalse(t, 100*time.Millisecond, func() bool { return slices.Contains(promptIDs(t, cs, "c1"), "m-p") },
			"the prompt opened beside the revert's reservation")
		h.coord.ReleaseTurnReservation(res)
		waitFor(t, func() bool { return slices.Contains(promptIDs(t, cs, "c1"), "m-p") })
	})

	t.Run("a prompt behind kiro-cli's run with no turn here queues behind the run entry", func(t *testing.T) {
		h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
		h.coord.WireTurnActivity("c1", true)
		waitFor(t, func() bool { return slices.Contains(runStates(t, cs, "c1"), "run running") })
		if got := runView(t, cs, "c1").Send; got != marotte.SendQueue {
			t.Errorf("run.send = %q, want queue", got)
		}
		if got := ackAs(t, postCmd(t, h, promptCmd("m-p", "behind the run"))); got != "queued" {
			t.Errorf("prompt answered %s, want queued", got)
		}
		if got := runStates(t, cs, "c1"); !slices.Equal(got, []string{"run running", "m-p pending"}) {
			t.Errorf("list = %v, want [run running, m-p pending]", got)
		}
		h.coord.WireTurnActivity("c1", false)
		waitFor(t, func() bool { return slices.Contains(promptIDs(t, cs, "c1"), "m-p") })
	})

	t.Run("send-now under a running turn with a compaction pending stops it, then compacts, then sends", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		open := promptGates(t, br)
		atThePoint(t, h, cs)
		// kiro-cli answers a stopped turn's prompt cancelled.
		br.setCallResult(marotte.MethodPrompt, json.RawMessage(`{"stopReason":"cancelled"}`))
		if code := postPrompt(t, h, "a"); code != http.StatusOK {
			t.Fatalf("Setup: prompt = %d", code)
		}
		waitFor(t, func() bool { return countOf(br.callLog(), marotte.MethodPrompt) == 2 })
		h.coord.ObserveContextUsage(t.Context(), "c1", 92)
		queueDuring(t, h, "m-q0", "m-q1")
		if got := entryView(t, cs, "m-q1").SendNow; got != marotte.SendNowStopThenCompaction {
			t.Errorf("m-q1's arrow = %q, want stop_then_compaction", got)
		}

		if rec := postCmd(t, h, sendNowCmd("m-q1", "m-a")); rec.Code != http.StatusOK {
			t.Fatalf("send_queued_now = %d %s", rec.Code, rec.Body.String())
		}
		waitFor(t, func() bool { return len(br.notified(marotte.MethodCancel)) == 1 })
		open(0)
		// m-q1's turn stays open at its gate, so the row the stop paused is read before any clean close.
		waitFor(t, func() bool { return countOf(br.callLog(), marotte.MethodPrompt) == 3 })
		w := orderWrites(br)
		c, k := slices.Index(w, marotte.MethodCancel), slices.Index(w, marotte.MethodSessionCompact)
		if c < 0 || k < c || !slices.Contains(w[k+1:], marotte.MethodPrompt) {
			t.Errorf("writes = %v, want session/cancel, then the compaction, then m-q1's prompt", w)
		}
		if slices.Contains(promptIDs(t, cs, "c1"), "m-q0") {
			t.Error("the row the stop paused was sent at the compaction's end, want only the send-now row")
		}
		if got := entryView(t, cs, "m-q0").State; got != marotte.RunPaused {
			t.Errorf("m-q0 = %q, want paused", got)
		}
	})

	t.Run("send-now under a closing head with a compaction pending stops nothing and goes after it", func(t *testing.T) {
		h, cs, br, _ := heldCleanClose(t)
		queueDuring(t, h, "m-q1", "m-q2")
		if got := entryView(t, cs, "m-q2").SendNow; got != marotte.SendNowAfterCompaction {
			t.Errorf("m-q2's arrow = %q, want after_compaction", got)
		}
		if rec := postCmd(t, h, sendNowCmd("m-q2", "")); rec.Code != http.StatusOK {
			t.Fatalf("send_queued_now = %d %s", rec.Code, rec.Body.String())
		}
		if n := len(br.notified(marotte.MethodCancel)); n != 0 {
			t.Errorf("session/cancel sent %d times, want none: the head is closing", n)
		}
	})

	t.Run("cancel with no target stops the running turn and writes nothing under a compaction", func(t *testing.T) {
		h, _, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		open := promptGates(t, br)
		if code := postPrompt(t, h, "a"); code != http.StatusOK {
			t.Fatalf("Setup: prompt = %d", code)
		}
		waitFor(t, func() bool { return countOf(br.callLog(), marotte.MethodPrompt) == 2 })
		if rec := postCmd(t, h, cancelCmd(`{}`)); rec.Code != http.StatusOK {
			t.Fatalf("cancel = %d %s", rec.Code, rec.Body.String())
		}
		waitFor(t, func() bool { return len(br.notified(marotte.MethodCancel)) == 1 })
		open(0)

		h2, _, br2, _ := compactingHubIn(t, "/tmp/work")
		wantStoppedNothing(t, postCmd(t, h2, cancelCmd(`{}`)))
		if n := len(br2.notified(marotte.MethodCancel)); n != 0 {
			t.Errorf("session/cancel under a compaction head = %d, want none", n)
		}
	})

	t.Run("a fold-opened turn on an idle list hands its place to a prompt", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		br.deliver(newChunkMsg("kiro-cli on its own"))
		waitFor(t, func() bool { return slices.Contains(runStates(t, cs, "c1"), "wire running") })
		if got := runView(t, cs, "c1").Send; got != marotte.SendPrompt {
			t.Errorf("run.send beside a fold-opened turn = %q, want prompt", got)
		}
		if got := ackAs(t, postCmd(t, h, promptCmd("m-p", "beside the fold"))); got != "started" {
			t.Errorf("prompt answered %s, want started", got)
		}
		waitFor(t, func() bool { return countOf(br.callLog(), marotte.MethodPrompt) == 2 })
		waitFor(t, func() bool { return !listLive(h, "c1") })
		if got := runStates(t, cs, "c1"); len(got) != 0 {
			t.Errorf("list after the prompt = %v, want empty", got)
		}
	})

	for _, mode := range []marotte.InterruptMode{"", marotte.InterruptQueue} {
		name := "steer mode"
		want := "steered"
		if mode == marotte.InterruptQueue {
			name, want = "queue mode", "queued"
		}
		t.Run("a turn_start during a fold-opened turn opens a bracketed one, "+name, func(t *testing.T) {
			h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
			br.setCallResult(marotte.MethodSessionSteer, json.RawMessage(`{"queued":true}`))
			br.deliver(newChunkMsg("kiro-cli on its own"))
			if !eventually(func() bool { return slices.Contains(runStates(t, cs, "c1"), "wire running") }) {
				t.Fatalf("no fold-opened turn: list %v, live %v", runStates(t, cs, "c1"), h.coord.turns.live("c1"))
			}
			br.deliver(newTurnStartMsg())
			if !eventually(func() bool {
				v := runView(t, cs, "c1")
				return len(v.Entries) == 1 && v.Entries[0].Steerable
			}) {
				t.Fatalf("list after the turn_start = %+v, want one steerable wire turn", runView(t, cs, "c1").Entries)
			}
			if got := ackAs(t, postCmd(t, h, modePromptCmd("m-p", "into kiro-cli's turn", mode))); got != want {
				t.Errorf("prompt answered %s, want %s", got, want)
			}
			if mode == marotte.InterruptQueue {
				br.deliver(newChunkMsg("kiro-cli's answer"))
				br.deliver(newTurnEndMsg("end_turn"))
				h.coord.WireTurnActivity("c1", false)
				if !eventually(func() bool { return slices.Contains(promptIDs(t, cs, "c1"), "m-p") }) {
					t.Errorf("m-p never opened after kiro-cli's turn ended; list %v", runStates(t, cs, "c1"))
				}
			}
		})
	}

	t.Run("a fold-opened turn while kiro-cli reports a run admits nothing until it reports idle", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		br.deliver(newChunkMsg("kiro-cli on its own"))
		waitFor(t, func() bool { return slices.Contains(runStates(t, cs, "c1"), "wire running") })
		h.coord.WireTurnActivity("c1", true)
		waitFor(t, func() bool { return runView(t, cs, "c1").Send == marotte.SendQueue })
		if v := runView(t, cs, "c1"); len(v.Entries) == 0 || v.Entries[0].Admits {
			t.Errorf("list = %+v, want the fold-opened head not admitting", v.Entries)
		}
		if got := ackAs(t, postCmd(t, h, promptCmd("m-p", "beside the fold"))); got != "queued" {
			t.Errorf("prompt answered %s, want queued", got)
		}
		if got := runWait(t, cs, "c1", "m-p"); got != "turn" {
			t.Errorf("m-p waits for %q, want a turn", got)
		}
		h.coord.WireTurnActivity("c1", false)
		waitFor(t, func() bool { return slices.Contains(promptIDs(t, cs, "c1"), "m-p") })
	})

	t.Run("kiro-cli's turn beside a compaction closing first leaves the compaction running", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		release := holdCall(t, br, marotte.MethodSessionCompact)
		go func() { _, _ = h.RequestCompact(context.WithoutCancel(t.Context()), "c1") }()
		waitForCall(t, br, marotte.MethodSessionCompact)
		br.deliver(newTurnStartMsg())
		waitFor(t, func() bool {
			return slices.Equal(runStates(t, cs, "c1"), []string{"compaction running", "wire running"})
		})
		br.deliver(newTurnEndMsg("end_turn"))
		waitFor(t, func() bool { return slices.Equal(runStates(t, cs, "c1"), []string{"compaction running"}) })
		release()
		waitFor(t, func() bool { return !listLive(h, "c1") })
		if n := countOf(br.callLog(), marotte.MethodSessionCompact); n != 1 {
			t.Errorf("compactions written = %d, want 1", n)
		}
	})

	t.Run("a taken row beside kiro-cli's bracketed turn waits for its close and is never stopped", func(t *testing.T) {
		h, cs, br, open, held, releaseOpen := takenRowHub(t)
		open(0)
		<-held
		br.deliver(newTurnStartMsg())
		waitFor(t, func() bool { return slices.Contains(runStates(t, cs, "c1"), "wire running") })
		if got := runStates(t, cs, "c1"); !slices.Equal(got, []string{"wire running", "m-q1 starting", "m-q2 pending"}) {
			t.Errorf("list = %v, want [wire running, m-q1 starting, m-q2 pending]", got)
		}
		target := liveRunCancel(t, cs)
		if rec := postCmd(t, h, cancelCmd(`{"target":"`+target+`"}`)); rec.Code != http.StatusOK {
			t.Fatalf("cancel = %d %s", rec.Code, rec.Body.String())
		}
		waitFor(t, func() bool { return len(br.notified(marotte.MethodCancel)) == 1 })
		if got := entryView(t, cs, "m-q1").State; got != marotte.RunStarting {
			t.Errorf("m-q1 after the cancel = %q, want starting", got)
		}
		close(releaseOpen)
		br.deliver(newTurnEndMsg("cancelled"))
		open(1)
		waitFor(t, func() bool { return slices.Contains(promptIDs(t, cs, "c1"), "m-q1") })
	})

	t.Run("a prompt on an idle list with a paused row goes ahead of it", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		seedQueue(t, cs, "c1", marotte.QueuedPrompt{ID: "m-q0", Text: "paused"})
		open := promptGates(t, br)
		if got := ackAs(t, postCmd(t, h, promptCmd("m-new", "ahead of the paused row"))); got != "started" {
			t.Errorf("prompt answered %s, want started", got)
		}
		if got := runStates(t, cs, "c1"); len(got) != 2 || got[1] != "m-q0 paused" || !strings.HasPrefix(got[0], "m-new ") {
			t.Errorf("list = %v, want m-new live ahead of [m-q0 paused]", got)
		}
		open(0)
	})

	t.Run("a prompt while a steer's end is pending answers queued at once", func(t *testing.T) {
		s, cs, br := steerOpsHub(t)
		at := s.h.originOf("c1")
		id, _ := s.h.stagePromptTurn(t, "c1")
		s.h.translateACPEvent("c1", at, newPromptTurnStartMsg(id))
		s.steer("steer-a", "unread")
		br.setCallResult(marotte.MethodSessionSteerClear, clearReply([]string{"steer-a"}))
		release := holdCall(t, br, marotte.MethodSessionSteerClear)
		s.h.translateACPEvent("c1", at, newTurnEndMsg("end_turn"))
		s.h.coord.releaseTurn("c1", id)
		waitForCall(t, br, marotte.MethodSessionSteerClear)

		if got := ackAs(t, postCmd(t, s.h, promptCmd("m-p", "while the end is pending"))); got != "queued" {
			t.Errorf("prompt answered %s, want queued", got)
		}
		if !slices.ContainsFunc(runView(t, cs, "c1").Entries, func(e marotte.RunEntryView) bool { return e.ID == "m-p" }) {
			t.Errorf("list = %v, want m-p on it", runStates(t, cs, "c1"))
		}
		release()
	})

	t.Run("a stop that was not delivered restores the head and the rows", func(t *testing.T) {
		sess := newOrderedSession()
		cs := newTestChatStore()
		h := newRuntimeOn(context.Background(), "/tmp/work", fakeEngineOf(func() engineSession { return sess }, nil), cs)
		cs.wire(h)
		t.Cleanup(func() { shutdownHub(t, h) })
		seedChat(t, cs, "c1")
		if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
			t.Fatalf("Setup: open the session: %v", err)
		}
		turnID, _ := h.stagePromptTurn(t, "c1")
		queueTextDuring(t, h, "m-q1", "behind the turn")
		addPermission(t, h, sess, 7, "")
		sess.mu.Lock()
		sess.failRespond[7] = cancelAttempts
		sess.mu.Unlock()

		rec := postCmd(t, h, cancelCmd(`{"target":"`+turnID+`"}`))
		if rec.Code != http.StatusBadGateway {
			t.Errorf("cancel with the ask's answer undelivered = %d %s, want 502", rec.Code, rec.Body.String())
		}
		if n := len(sess.notified(marotte.MethodCancel)); n != 0 {
			t.Errorf("session/cancel sent %d times, want none", n)
		}
		if got := runStates(t, cs, "c1"); !slices.Equal(got, []string{turnID + " running", "m-q1 pending"}) {
			t.Errorf("list after the undelivered stop = %v, want [%s running, m-q1 pending]", got, turnID)
		}
		if got := runView(t, cs, "c1").Cancel; got != turnID {
			t.Errorf("run.cancel = %q, want %q", got, turnID)
		}
		if rec := postCmd(t, h, cancelCmd(`{"target":"`+turnID+`"}`)); rec.Code != http.StatusOK {
			t.Fatalf("second cancel = %d %s", rec.Code, rec.Body.String())
		}
		waitFor(t, func() bool { return len(sess.notified(marotte.MethodCancel)) == 1 })
		if got := entryView(t, cs, turnID).State; got != marotte.RunStopping {
			t.Errorf("head after the second cancel = %q, want stopping", got)
		}
	})

	t.Run("a sent prompt reads running in the header, so a plain send steers", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		open := promptGates(t, br)
		if code := postPrompt(t, h, "a"); code != http.StatusOK {
			t.Fatalf("Setup: prompt = %d", code)
		}
		waitFor(t, func() bool { return countOf(br.callLog(), marotte.MethodPrompt) == 2 })
		if !eventually(func() bool { return entryView(t, cs, "m-a").State == marotte.RunRunning }) {
			t.Errorf("the header's m-a = %q after its call was written, want running", entryView(t, cs, "m-a").State)
		}
		if got := runView(t, cs, "c1").Send; got != marotte.SendSteer {
			t.Errorf("run.send = %q, want steer", got)
		}
		open(0)
	})

	t.Run("a repeated id adds no entry", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		open := promptGates(t, br)
		if code := postPrompt(t, h, "a"); code != http.StatusOK {
			t.Fatalf("Setup: prompt = %d", code)
		}
		queueDuring(t, h, "m-q1")
		waitFor(t, func() bool { return slices.Contains(runStates(t, cs, "c1"), "m-a running") })
		if got := ackAs(t, postCmd(t, h, promptCmd("m-q1", "again"))); got != "queued" {
			t.Errorf("a prompt repeating a queued row's id answered %s, want queued", got)
		}
		queueTextDuring(t, h, "m-q1", "again")
		if got := ackAs(t, postCmd(t, h, promptCmd("m-a", "again"))); got != "started" {
			t.Errorf("a prompt repeating the running turn's id answered %s, want started", got)
		}
		if got := runStates(t, cs, "c1"); !slices.Equal(got, []string{"m-a running", "m-q1 pending"}) {
			t.Errorf("list = %v, want [m-a running, m-q1 pending]", got)
		}
		open(0)
		open(1)
	})

	t.Run("a /compact requeued behind kiro-cli's turn answers queued, and its later failure is toasted", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		calls := onCompactor(h, 90, true, 10)
		calls.refuse = true
		release := holdCall(t, br, marotte.MethodSessionCompact)
		h.coord.autoCompact.compact = func(ctx context.Context, b command.CompactBridge) (bool, uint64, error) {
			_, _, _ = b.CallAt(ctx, marotte.MethodSessionCompact, map[string]any{})
			calls.mu.Lock()
			defer calls.mu.Unlock()
			calls.calls++
			return false, 0, nil
		}
		out := make(chan command.CompactOutcome, 1)
		go func() {
			o, _ := h.RequestCompact(context.WithoutCancel(t.Context()), "c1")
			out <- o
		}()
		waitForCall(t, br, marotte.MethodSessionCompact)
		br.deliver(newTurnStartMsg())
		waitFor(t, func() bool { return slices.Contains(runStates(t, cs, "c1"), "wire running") })
		release()
		if got := <-out; got != command.CompactQueued {
			t.Errorf("/compact answered %v, want queued", got)
		}
		br.deliver(newTurnEndMsg("end_turn"))
		waitFor(t, func() bool { return calls.count() == 2 && len(calls.toasts()) == 1 })
	})

	t.Run("the composer settles at a queued answer behind a compaction, and later drafts stay", func(t *testing.T) {
		h, cs, _, release := compactingHubIn(t, "/tmp/work")
		if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
			c.Name = marotte.DefaultChatName
			return true
		}); err != nil {
			t.Fatal(err)
		}
		setDraftOf(t, h, "sent words")
		if got := ackAs(t, postPageCmd(t.Context(), h, promptCmd("m-p", "sent words"))); got != "queued" {
			t.Errorf("prompt answered %s, want queued", got)
		}
		c, _ := cs.Get(t.Context(), "c1")
		if c.Draft != "" || c.Name == marotte.DefaultChatName {
			t.Errorf("chat right after the answer: draft %q, name %q; want the draft cleared and the chat named", c.Draft, c.Name)
		}
		setDraftOf(t, h, "next words")
		release()
		waitFor(t, func() bool { return slices.Contains(promptIDs(t, cs, "c1"), "m-p") && !listLive(h, "c1") })
		if got := draftOf(t, cs); got != "next words" {
			t.Errorf("draft after the row was sent = %q, want the words saved after the answer", got)
		}
	})

	t.Run("the composer settles once, at the answer, when the take's open is held", func(t *testing.T) {
		shrinkAdmissionWait(t, 60*time.Millisecond)
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		br.chunksOnCall = map[string][]string{marotte.MethodPrompt: {"done"}}
		entered, releaseOpen := make(chan struct{}), make(chan struct{})
		var once atomic.Bool
		h.coord.turns.openHook = func() {
			if once.CompareAndSwap(false, true) {
				close(entered)
				<-releaseOpen
			}
		}
		setDraftOf(t, h, "sent words")
		answered := make(chan *httptest.ResponseRecorder, 1)
		go func() { answered <- postPageCmd(context.WithoutCancel(t.Context()), h, promptCmd("m-p", "sent words")) }()
		<-entered
		rec := <-answered
		if got := ackAs(t, rec); got != "queued" {
			t.Errorf("prompt held past the wait answered %s, want queued", got)
		}
		if got := draftOf(t, cs); got != "" {
			t.Errorf("draft right after the answer = %q, want empty", got)
		}
		setDraftOf(t, h, "next words")
		close(releaseOpen)
		waitFor(t, func() bool { return slices.Contains(promptIDs(t, cs, "c1"), "m-p") && !listLive(h, "c1") })
		if got := draftOf(t, cs); got != "next words" {
			t.Errorf("draft after the open = %q, want the words saved after the answer", got)
		}
	})

	t.Run("a POST whose context ends while the take's open is held settles once", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		br.chunksOnCall = map[string][]string{marotte.MethodPrompt: {"done"}}
		entered, releaseOpen := make(chan struct{}), make(chan struct{})
		var once atomic.Bool
		h.coord.turns.openHook = func() {
			if once.CompareAndSwap(false, true) {
				close(entered)
				<-releaseOpen
			}
		}
		setDraftOf(t, h, "sent words")
		ctx, cancel := context.WithCancel(t.Context())
		answered := make(chan *httptest.ResponseRecorder, 1)
		go func() { answered <- postPageCmd(ctx, h, promptCmd("m-p", "sent words")) }()
		<-entered
		cancel()
		<-answered
		setDraftOf(t, h, "next words")
		close(releaseOpen)
		waitFor(t, func() bool { return slices.Contains(promptIDs(t, cs, "c1"), "m-p") && !listLive(h, "c1") })
		if got := draftOf(t, cs); got != "next words" {
			t.Errorf("draft after the later open = %q, want the words saved after the walk-away", got)
		}
	})
}

// setDraftOf saves c1's composer text, as the page's set_draft does.
func setDraftOf(t *testing.T, h *Runtime, text string) {
	t.Helper()
	payload, _ := json.Marshal(marotte.SetDraftCommand{Text: text})
	if rec := postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdSetDraft, ChatID: "c1", Payload: payload}); rec.Code != http.StatusOK {
		t.Fatalf("set_draft = %d %s", rec.Code, rec.Body.String())
	}
}

func draftOf(t *testing.T, cs *testChatStore) string {
	t.Helper()
	c, _ := cs.Get(t.Context(), "c1")
	if c == nil {
		return ""
	}
	return c.Draft
}

// postPageCmd posts cmd as a page does (an Origin header: not another program's call), on a request whose context is
// ctx.
func postPageCmd(ctx context.Context, h *Runtime, cmd marotte.ClientCommand) *httptest.ResponseRecorder {
	body, _ := json.Marshal(cmd)
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/command", strings.NewReader(string(body)))
	req.Header.Set("Origin", "http://marotte.test")
	rec := httptest.NewRecorder()
	h.handleCommand(rec, req)
	return rec
}
