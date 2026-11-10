package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// atThePoint turns Compact automatically on at 90% and puts c1's context at 95%, so the next turn end compacts.
func atThePoint(t *testing.T, h *Runtime, cs *testChatStore) {
	t.Helper()
	h.coord.autoCompact.policy = func(context.Context) (bool, int) { return true, 90 }
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Usage = marotte.Usage{HasRealData: true, ContextPct: 95}
		return true
	}); err != nil {
		t.Fatal(err)
	}
}

// stopUnderHold opens a turn on c1, queues m-q0 behind it, and closes it on the cancel grace while kiro-cli still
// reports the run: the close's KAS hold stands until the test reports kiro-cli idle.
func stopUnderHold(t *testing.T, h *Runtime) {
	t.Helper()
	turnID, _ := h.stagePromptTurn(t, "c1")
	queueTextDuring(t, h, "m-q0", "queued before the stop")
	h.coord.WireTurnActivity("c1", true)
	graceClose(t, h, turnID)
}

// stoppedTurnCompaction is chat c1 whose stopped turn's end compacts at the Compact at point under the close's KAS
// hold, the call running until release is called; underHold runs before kiro-cli reports idle.
func stoppedTurnCompaction(t *testing.T, underHold func(h *Runtime)) (h *Runtime, cs *testChatStore, br *fakeBridge, release func()) {
	t.Helper()
	h, cs, br, _ = idleAfterFirstTurn(t, "/tmp/work")
	atThePoint(t, h, cs)
	release = holdCall(t, br, marotte.MethodSessionCompact)
	t.Cleanup(release)
	stopUnderHold(t, h)
	if underHold != nil {
		underHold(h)
	}
	h.coord.WireTurnActivity("c1", false)
	waitForCall(t, br, marotte.MethodSessionCompact)
	return h, cs, br, release
}

func promptCmd(id, text string) marotte.ClientCommand {
	payload, _ := json.Marshal(marotte.PromptCommand{Text: text, MessageID: id})
	return marotte.ClientCommand{Type: marotte.CmdPrompt, ChatID: "c1", Payload: payload}
}

// postAsync posts cmd on its own goroutine; the channel carries the answer.
func postAsync(h *Runtime, cmd marotte.ClientCommand) <-chan *httptest.ResponseRecorder {
	done := make(chan *httptest.ResponseRecorder, 1)
	body, _ := json.Marshal(cmd)
	go func() {
		rec := httptest.NewRecorder()
		h.handleCommand(rec, httptest.NewRequest(http.MethodPost, "/api/command", bytes.NewReader(body)))
		done <- rec
	}()
	return done
}

// wantQueuedAck fails unless rec is the prompt's 200 ack saying it waits in the queue.
func wantQueuedAck(t *testing.T, rec *httptest.ResponseRecorder, id string) {
	t.Helper()
	var ack struct {
		MessageID string `json:"message_id"`
		As        string `json:"as"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ack); rec.Code != http.StatusOK || err != nil || ack.As != "queued" || ack.MessageID != id {
		t.Errorf("prompt answer = %d %s, want 200 queued under %s", rec.Code, rec.Body.String(), id)
	}
}

// opensInOrder reports that every id has opened a turn, in the order given.
func opensInOrder(t *testing.T, cs *testChatStore, ids ...string) bool {
	t.Helper()
	opened := promptIDs(t, cs, "c1")
	last := -1
	for _, id := range ids {
		at := slices.Index(opened, id)
		if at <= last {
			return false
		}
		last = at
	}
	return true
}

// kiro-cli aborts a running compaction on session/cancel, so the up arrow on a row the stopped turn left waiting
// sends none during that turn's compaction: the row stays marked, and it goes first once the compaction ends.
func TestSendQueuedNow_DuringAStoppedTurnsCompactionLetsItFinish(t *testing.T) {
	h, cs, br, release := stoppedTurnCompaction(t, nil)

	rec := postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdSendQueuedNow, ChatID: "c1", Payload: json.RawMessage(`{"message_id":"m-q0"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("send_queued_now during the compaction = %d %s", rec.Code, rec.Body.String())
	}
	if got := br.notified(marotte.MethodCancel); len(got) != 0 {
		t.Errorf("session/cancel sent %d times during the compaction, want none", len(got))
	}
	if _, marked := queuedIDs(t, cs, "c1"); !slices.Equal(marked, []string{"m-q0"}) {
		t.Errorf("rows marked next = %v, want [m-q0]", marked)
	}

	release()

	waitFor(t, func() bool { return opensInOrder(t, cs, "m-c1", "m-q0") && len(queuedRows(t, cs, "c1")) == 0 })
}

// A message queued under a stopped turn's KAS hold before that turn's compaction starts goes when the compaction
// ends, so the header says the compaction's end sends a row, above the row the stop paused.
func TestCompaction_AStoppedTurnsCompactionSaysItSendsTheRowQueuedUnderTheHold(t *testing.T) {
	_, cs, _, release := stoppedTurnCompaction(t, func(h *Runtime) {
		queueTextDuring(t, h, "m-q1", "sent after the stop")
	})

	if running, sends := compactionSends(t, cs, "c1"); !running || !sends {
		t.Errorf("compaction running %v, its end sends a row %v; want both", running, sends)
	}
	if ids, _ := queuedIDs(t, cs, "c1"); !slices.Equal(ids, []string{"m-q1", "m-q0"}) {
		t.Errorf("queue = %v, want [m-q1 m-q0]", ids)
	}

	release()

	waitFor(t, func() bool { return opensInOrder(t, cs, "m-c1", "m-q1", "m-q0") })
}

// A /compact pressed under a stopped turn's KAS hold runs once the hold is released; a message queued under the hold
// goes when that compaction ends, which the header says while it runs.
func TestCompaction_OneTheHoldsReleaseRunsSaysItSendsTheRowQueuedUnderTheHold(t *testing.T) {
	h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
	release := holdCall(t, br, marotte.MethodSessionCompact)
	t.Cleanup(release)
	stopUnderHold(t, h)
	queueTextDuring(t, h, "m-q1", "sent after the stop")
	// The press lands in the close tail, before its end lets the list take what runs next.
	var pressed atomic.Bool
	beforeTake(h, func(ctx context.Context) {
		if pressed.Swap(true) {
			return
		}
		if out, err := h.RequestCompact(ctx, "c1"); err != nil || out != command.CompactQueued {
			t.Errorf("/compact under the hold = (%v, %v), want queued", out, err)
		}
	})
	h.coord.WireTurnActivity("c1", false)
	waitForCall(t, br, marotte.MethodSessionCompact)

	if running, sends := compactionSends(t, cs, "c1"); !running || !sends {
		t.Errorf("compaction running %v, its end sends a row %v; want both", running, sends)
	}

	release()

	waitFor(t, func() bool { return opensInOrder(t, cs, "m-c1", "m-q1", "m-q0") })
}

// A prompt that reaches the server during a stopped turn's compaction (another device, a stale page, an API caller)
// is queued at once behind it, above the row the stop paused, and goes first when the compaction ends.
func TestPrompt_DuringAStoppedTurnsCompactionIsQueuedBehindIt(t *testing.T) {
	h, cs, _, release := stoppedTurnCompaction(t, nil)

	answered := postAsync(h, promptCmd("m-p1", "sent during the compaction"))
	select {
	case rec := <-answered:
		wantQueuedAck(t, rec, "m-p1")
	case <-time.After(5 * time.Second):
		t.Fatal("the prompt is still unanswered 5s into the compaction, want it queued behind it")
	}
	if ids, _ := queuedIDs(t, cs, "c1"); !slices.Equal(ids, []string{"m-p1", "m-q0"}) {
		t.Errorf("queue during the compaction = %v, want [m-p1 m-q0]", ids)
	}
	if running, sends := compactionSends(t, cs, "c1"); !running || !sends {
		t.Errorf("compaction running %v, its end sends a row %v; want both", running, sends)
	}

	release()

	waitFor(t, func() bool { return opensInOrder(t, cs, "m-c1", "m-p1", "m-q0") })
}
