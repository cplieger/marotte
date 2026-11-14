package agent

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
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

// orderWrites is the session calls kiro-cli received that decide what runs next, in write order.
func orderWrites(br *fakeBridge) []string {
	var out []string
	for _, m := range br.wireLog() {
		switch m {
		case marotte.MethodPrompt, marotte.MethodCancel, marotte.MethodSessionCompact:
			out = append(out, m)
		}
	}
	return out
}

// promptAfterLastCompact reports a session/prompt written after the newest _kiro/session/compact.
func promptAfterLastCompact(br *fakeBridge) bool {
	w := orderWrites(br)
	last := -1
	for i, m := range w {
		if m == marotte.MethodSessionCompact {
			last = i
		}
	}
	return last >= 0 && slices.Contains(w[last+1:], marotte.MethodPrompt)
}

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

// wantStoppedNothing fails unless rec is a 200 whose body says it stopped nothing, with no "skipped" key: the client
// reads "skipped" as a turn still running.
func wantStoppedNothing(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	var body map[string]any
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
		t.Errorf("cancel = %d %s, want 200", rec.Code, rec.Body.String())
		return
	}
	delete(body, "run")
	if want := map[string]any{"ok": true, "stopped": false}; !maps.Equal(body, want) {
		t.Errorf("cancel body = %s, want {ok: true, stopped: false} (plus run)", rec.Body.String())
	}
}

// withCompactionReturnHook runs fn once, the first time a compaction's call returns.
func withCompactionReturnHook(t *testing.T, fn func(marotte.ChatID)) {
	t.Helper()
	var once sync.Once
	prev := testHookAfterCompactionReturn
	testHookAfterCompactionReturn = func(chatID marotte.ChatID) { once.Do(func() { fn(chatID) }) }
	t.Cleanup(func() { testHookAfterCompactionReturn = prev })
}

// A message queued during the compaction that ends a turn the model refused for the context's size waits under it and
// goes when it ends.
func TestRunOrder_ARowSentDuringAContextLimitTurnsCompactionGoesAtItsEnd(t *testing.T) {
	h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
	h.coord.autoCompact.policy = func(context.Context) (bool, int) { return true, 90 }
	release := holdCall(t, br, marotte.MethodSessionCompact)
	id, _ := h.stagePromptTurn(t, "c1")
	h.AbandonInFlightTurn(t.Context(), "c1", id, marotte.StopReasonInterrupted, "too long", marotte.FailureKindContextLimit, 0)
	waitForCall(t, br, marotte.MethodSessionCompact)

	queueTextDuring(t, h, "m-again", "send it again")

	if got := runIDs(t, cs, "c1"); !slices.Equal(got, []string{"compaction", "m-again"}) {
		t.Errorf("dock during the compaction = %v, want [compaction m-again]", got)
	}
	if promptAfterLastCompact(br) {
		t.Errorf("a session/prompt went out under the compaction; writes = %v", orderWrites(br))
	}
	release()
	waitFor(t, func() bool { return slices.Contains(promptIDs(t, cs, "c1"), "m-again") })
}

// A message past the draft cap stands for the prompt it would have been while only a compaction runs, so it queues;
// behind a running turn the draft cap still applies.
func TestRunOrder_AMessagePastTheDraftCapQueuesBehindACompactionOnly(t *testing.T) {
	long := strings.Repeat("x", marotte.MaxDraftBytes+1)
	t.Run("behind a compaction", func(t *testing.T) {
		h, cs, release := compactingHub(t)
		queueTextDuring(t, h, "m-long", long)
		release()
		waitFor(t, func() bool { return slices.Contains(promptIDs(t, cs, "c1"), "m-long") })
	})
	t.Run("behind a running turn", func(t *testing.T) {
		h, _, _, _ := idleAfterFirstTurn(t, "/tmp/work")
		h.stagePromptTurn(t, "c1")
		payload, _ := json.Marshal(marotte.QueuePromptCommand{Text: long, MessageID: "m-long"})
		rec := postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdQueuePrompt, ChatID: "c1", Payload: payload})
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("queue_prompt past the draft cap behind a turn = %d %s, want 413", rec.Code, rec.Body.String())
		}
	})
}

// A /compact that starts in a clean close's tail, before that close takes its queued row, is waited out: the row goes
// after it.
func TestRunOrder_ACompactionStartedBeforeAClosesDrainIsWaitedOut(t *testing.T) {
	h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
	open := promptGates(t, br)
	release := holdCall(t, br, marotte.MethodSessionCompact)
	if code := postPrompt(t, h, "a"); code != http.StatusOK {
		t.Fatalf("Setup: prompt = %d", code)
	}
	queueTextDuring(t, h, "m-q1", "queued behind the turn")
	compacted := make(chan struct{})
	beforeTake(h, func(ctx context.Context) {
		go func() {
			defer close(compacted)
			_, _ = h.RequestCompact(context.WithoutCancel(ctx), "c1")
		}()
		for compactionEntry(t, cs, "c1").ID == "" {
			time.Sleep(time.Millisecond)
		}
	})

	open(0)
	// m-first and m-a were written; m-q1 is the third session/prompt.
	if !eventually(func() bool {
		return slices.Contains(br.callLog(), marotte.MethodSessionCompact) || countOf(br.callLog(), marotte.MethodPrompt) >= 3
	}) {
		t.Fatalf("neither the compaction nor m-q1 was written; writes = %v", orderWrites(br))
	}
	if w := orderWrites(br); slices.Index(w, marotte.MethodSessionCompact) < 0 {
		t.Fatalf("m-q1 was written before the compaction listed in the close's tail; writes = %v", w)
	}
	staysFalse(t, 200*time.Millisecond, func() bool { return promptAfterLastCompact(br) },
		"a session/prompt went out under the compaction")
	release()
	<-compacted
	waitFor(t, func() bool { return opensInOrder(t, cs, "m-a", "m-q1") })
}

// A context reference sent while a compaction runs is queued behind it, though Steer cannot carry it, and its own
// turn resolves it.
func TestRunOrder_AContextReferenceQueuedBehindACompactionResolvesInItsTurn(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("remember the milk\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, cs, br, release := compactingHubIn(t, dir)
	const token = "#[[file:notes.txt]]"

	rec := postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdPrompt, ChatID: "c1",
		Payload: json.RawMessage(`{"text":"read ` + token + `","message_id":"m-ref","prompt_only":true}`),
	})
	if got := ackAs(t, rec); got != "queued" {
		t.Errorf("prompt-only send during the compaction answered %s, want queued", got)
	}
	release()

	waitFor(t, func() bool { return slices.Contains(promptIDs(t, cs, "c1"), "m-ref") && !h.coord.turns.live("c1") })
	blocks, _ := br.paramsFor(marotte.MethodPrompt)[marotte.KeyPrompt].([]map[string]any)
	resolved := slices.ContainsFunc(blocks, func(b map[string]any) bool {
		res, ok := b["resource"].(map[string]any)
		text, _ := res["text"].(string)
		return ok && res["uri"] == token && strings.Contains(text, "remember the milk")
	})
	if !resolved {
		t.Errorf("prompt blocks = %v, want a resource block for %s with the file's text", blocks, token)
	}
}

// Sent as a queue, a saved prompt or context reference sent mid-turn waits behind the turn and goes as a
// session/prompt of its own when the turn ends, never as a steer, so its context reference resolves.
func TestRunOrder_APromptOnlyMessageQueuedBehindARunningTurnGoesAsItsOwnPrompt(t *testing.T) {
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

// After a stopped turn, a message sent during its compaction goes first when the compaction ends; the row the stop
// left waits for the next clean turn end.
func TestRunOrder_AfterAStoppedTurnTheMessageSentDuringItsCompactionGoesFirst(t *testing.T) {
	h, cs, br, release := stoppedTurnCompaction(t, nil)

	if got := ackAs(t, postCmd(t, h, promptCmd("m-new", "sent during the compaction"))); got != "queued" {
		t.Errorf("prompt during the compaction answered %s, want queued", got)
	}
	if got := runIDs(t, cs, "c1"); !slices.Equal(got, []string{"compaction", "m-new", "m-q0"}) {
		t.Errorf("dock during the compaction = %v, want [compaction m-new m-q0]", got)
	}
	if promptAfterLastCompact(br) {
		t.Errorf("a session/prompt went out under the compaction; writes = %v", orderWrites(br))
	}
	release()
	waitFor(t, func() bool { return opensInOrder(t, cs, "m-c1", "m-new", "m-q0") })
}

// withCompactionStartHook runs fn once, the first time h's taken compaction is about to start. Its cleanup shuts h
// down before restoring the seam: cleanups run last-first, so h's own shutdown would otherwise come after the restore,
// racing a compaction h starts as the test ends.
func withCompactionStartHook(t *testing.T, h *Runtime, fn func(marotte.ChatID)) {
	t.Helper()
	var once sync.Once
	prev := testHookBeforeCompactionStart
	testHookBeforeCompactionStart = func(chatID marotte.ChatID) { once.Do(func() { fn(chatID) }) }
	t.Cleanup(func() {
		shutdownHub(t, h)
		testHookBeforeCompactionStart = prev
	})
}

// answerWithin is the answer on ch, or nil (a test error) when none came within 3s.
func answerWithin(t *testing.T, ch <-chan *httptest.ResponseRecorder, what string) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case rec := <-ch:
		return rec
	case <-time.After(3 * time.Second):
		t.Errorf("%s is unanswered after 3s", what)
		return nil
	}
}

// A prompt and a send-now landing between an idle /compact's take and its call are queued behind it: no
// session/prompt goes out until the compaction ends, and then the marked row goes first.
func TestRunOrder_APromptOrSendNowAsACompactionStartsOpensNoTurnUnderIt(t *testing.T) {
	h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
	seedQueue(t, cs, "c1", marotte.QueuedPrompt{ID: "m-q1", Text: "queued"})
	release := holdCall(t, br, marotte.MethodSessionCompact)
	var prompted, marked *httptest.ResponseRecorder
	withCompactionStartHook(t, h, func(marotte.ChatID) {
		prompted = answerWithin(t, postAsync(h, promptCmd("m-p", "racing the compaction")), "the prompt")
		marked = answerWithin(t, postAsync(h, marotte.ClientCommand{
			Type: marotte.CmdSendQueuedNow, ChatID: "c1", Payload: json.RawMessage(`{"message_id":"m-q1"}`),
		}), "the send-now")
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = h.RequestCompact(context.WithoutCancel(t.Context()), "c1")
	}()
	waitForCall(t, br, marotte.MethodSessionCompact)

	if prompted != nil {
		if got := ackAs(t, prompted); got != "queued" {
			t.Errorf("the prompt as the compaction started answered %s, want queued", got)
		}
	}
	if marked != nil && marked.Code != http.StatusOK {
		t.Errorf("send_queued_now as the compaction started = %d %s, want 200", marked.Code, marked.Body.String())
	}
	if n := countOf(br.callLog(), marotte.MethodPrompt); n != 1 {
		t.Errorf("session/prompt written %d times by the time the compaction ran, want 1 (m-first); writes = %v",
			n, orderWrites(br))
	}
	release()
	<-done
	if !eventually(func() bool { return opensInOrder(t, cs, "m-first", "m-q1", "m-p") }) {
		t.Errorf("turns opened = %v, want [m-first m-q1 m-p]", promptIDs(t, cs, "c1"))
	}
}

// The up arrow on the row a stopped turn left waiting does not stop that turn's compaction: the compaction completes
// and the row goes first after it.
func TestRunOrder_SendNowDuringAStoppedTurnsCompactionLetsItFinish(t *testing.T) {
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
	release()
	waitFor(t, func() bool { return opensInOrder(t, cs, "m-c1", "m-q0") })
}

// A Cancel during a compaction, naming the turn that closed or nothing, stops nothing and says so.
func TestRunOrder_ACancelDuringACompactionSendsNoSessionCancel(t *testing.T) {
	h, _, br, _ := compactingHubIn(t, "/tmp/work")

	wantStoppedNothing(t, postCmd(t, h, cancelCmd(`{"target":"m-first"}`)))
	wantStoppedNothing(t, postCmd(t, h, cancelCmd(`{}`)))
	if got := br.notified(marotte.MethodCancel); len(got) != 0 {
		t.Errorf("session/cancel sent %d times during the compaction, want none", len(got))
	}
}

// A message queued under a stopped turn's KAS hold reads as waiting for that turn's compaction; the row the stop
// left reads as waiting for a turn.
func TestRunOrder_ARowQueuedUnderTheHoldReadsAfterTheCompaction(t *testing.T) {
	_, cs, _, _ := stoppedTurnCompaction(t, func(h *Runtime) {
		queueTextDuring(t, h, "m-q1", "sent after the stop")
	})

	if got := runWait(t, cs, "c1", "m-q1"); got != "compaction" {
		t.Errorf("m-q1 waits for %q, want the compaction", got)
	}
	if got := runWait(t, cs, "c1", "m-q0"); got != "turn" {
		t.Errorf("m-q0 waits for %q, want a turn", got)
	}
}

// A prompt sent under a stop's KAS hold, and one sent during the hold's compaction, are each answered at once as
// queued, and go after the compaction one per turn in the order sent.
func TestRunOrder_APromptUnderAStopsHoldIsQueuedAtOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		defer shutdownHub(t, h)
		atThePoint(t, h, cs)
		release := holdCall(t, br, marotte.MethodSessionCompact)
		defer release()
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
		waitForCall(t, br, marotte.MethodSessionCompact)
		second := postAsync(h, promptCmd("m-p2", "sent during the compaction"))
		synctest.Wait()
		select {
		case rec := <-second:
			if got := ackAs(t, rec); got != "queued" {
				t.Errorf("prompt during the compaction answered %s, want queued", got)
			}
		default:
			t.Error("the prompt during the compaction is unanswered, want it queued at once")
		}

		release()
		waitFor(t, func() bool { return opensInOrder(t, cs, "m-c1", "m-p1", "m-p2") })
	})
}

// A prompt that lands as the reader's compaction returns goes behind the row queued during it.
func TestRunOrder_APromptAsACompactionEndsGoesBehindTheQueuedRow(t *testing.T) {
	h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
	release := holdCall(t, br, marotte.MethodSessionCompact)
	var gap <-chan *httptest.ResponseRecorder
	withCompactionReturnHook(t, func(chatID marotte.ChatID) {
		gap = postAsync(h, promptCmd("m-gap", "as it ends"))
		// The prompt must reach the list before the compaction's end applies: one arriving after m-q1 took steers it.
		eventually(func() bool { return onRunList(h, chatID, "m-gap") })
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = h.RequestCompact(context.WithoutCancel(t.Context()), "c1")
	}()
	waitForCall(t, br, marotte.MethodSessionCompact)
	queueDuring(t, h, "m-q1")

	release()
	<-done
	rec := <-gap

	if !eventually(func() bool { return opensInOrder(t, cs, "m-first", "m-q1", "m-gap") }) {
		t.Errorf("turns opened = %v (the prompt as it ended answered %d %s), want [m-first m-q1 m-gap]",
			promptIDs(t, cs, "c1"), rec.Code, rec.Body.String())
	}
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

// A message sent during the compaction a stop's hold release runs goes behind the row queued under the hold.
func TestRunOrder_ARowSentDuringTheHoldReleasesCompactionGoesBehindTheHoldsRow(t *testing.T) {
	h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
	release := holdCall(t, br, marotte.MethodSessionCompact)
	stopUnderHold(t, h)
	queueTextDuring(t, h, "m-q1", "queued under the hold")
	compacted := make(chan struct{})
	go func() {
		defer close(compacted)
		_, _ = h.RequestCompact(context.WithoutCancel(t.Context()), "c1")
	}()
	waitFor(t, func() bool { return slices.Contains(runIDs(t, cs, "c1"), "compaction") })
	h.coord.WireTurnActivity("c1", false)
	waitForCall(t, br, marotte.MethodSessionCompact)
	queueTextDuring(t, h, "m-q2", "sent during the compaction")

	if got := runIDs(t, cs, "c1"); !slices.Equal(got, []string{"compaction", "m-q1", "m-q2", "m-q0"}) {
		t.Errorf("dock during the compaction = %v, want [compaction m-q1 m-q2 m-q0]", got)
	}
	release()
	<-compacted
	waitFor(t, func() bool { return opensInOrder(t, cs, "m-c1", "m-q1", "m-q2", "m-q0") })
}

// A Cancel from a page that has not drawn the turn's close, landing during a compaction, changes nothing.
func TestRunOrder_AStaleCancelDuringACompactionChangesNothing(t *testing.T) {
	h, cs, br, _ := compactingHubIn(t, "/tmp/work")
	queueDuring(t, h, "m-q1")
	before := runIDs(t, cs, "c1")

	wantStoppedNothing(t, postCmd(t, h, cancelCmd(`{"target":"m-first"}`)))
	if got := br.notified(marotte.MethodCancel); len(got) != 0 {
		t.Errorf("session/cancel sent %d times during the compaction, want none", len(got))
	}
	if got := runIDs(t, cs, "c1"); !slices.Equal(got, before) {
		t.Errorf("dock after the cancel = %v, want %v", got, before)
	}
}

// A message queued as a turn end's compaction returns, before anything else is taken, is queued, and goes after the
// row queued during the turn.
func TestRunOrder_ARowQueuedAsATurnEndsCompactionEndsIsQueued(t *testing.T) {
	h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
	open := promptGates(t, br)
	atThePoint(t, h, cs)
	release := holdCall(t, br, marotte.MethodSessionCompact)
	if code := postPrompt(t, h, "a"); code != http.StatusOK {
		t.Fatalf("Setup: prompt = %d", code)
	}
	queueTextDuring(t, h, "m-q1", "queued behind the turn")
	answered := make(chan *httptest.ResponseRecorder, 1)
	withCompactionReturnHook(t, func(marotte.ChatID) {
		payload, _ := json.Marshal(marotte.QueuePromptCommand{Text: "as it ends", MessageID: "m-q2"})
		answered <- postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdQueuePrompt, ChatID: "c1", Payload: payload})
	})

	open(0)
	waitForCall(t, br, marotte.MethodSessionCompact)
	release()
	open(1)

	if rec := <-answered; rec.Code != http.StatusOK {
		t.Errorf("queue_prompt as the compaction ended = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if !eventually(func() bool { return opensInOrder(t, cs, "m-a", "m-q1", "m-q2") }) {
		t.Errorf("turns opened = %v, want m-a, m-q1, m-q2", promptIDs(t, cs, "c1"))
	}
}

// A stop whose session/cancel is still being written when a turn end's compaction is taken reaches kiro-cli first:
// kiro-cli aborts a compaction on session/cancel.
func TestRunOrder_AStopHeldAcrossACompactionsStartIsWrittenFirst(t *testing.T) {
	h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
	open := promptGates(t, br)
	atThePoint(t, h, cs)
	gate := make(chan struct{})
	br.mu.Lock()
	br.blockNotifyOn = map[string]chan struct{}{marotte.MethodCancel: gate}
	br.mu.Unlock()
	if code := postPrompt(t, h, "a"); code != http.StatusOK {
		t.Fatalf("Setup: prompt = %d", code)
	}
	waitFor(t, func() bool { return countOf(br.callLog(), marotte.MethodPrompt) == 2 })
	stopped := postAsync(h, cancelCmd(`{}`))
	waitFor(t, func() bool { return stopHeld(h, "c1") })

	open(0)
	staysFalse(t, 200*time.Millisecond, func() bool { return slices.Contains(br.callLog(), marotte.MethodSessionCompact) },
		"_kiro/session/compact went out while the stop's session/cancel waited")
	close(gate)
	<-stopped
	if !eventually(func() bool { return slices.Contains(br.callLog(), marotte.MethodSessionCompact) }) {
		t.Logf("no compaction after the stop; writes = %v", orderWrites(br))
		return
	}
	w := orderWrites(br)
	if c, k := slices.Index(w, marotte.MethodCancel), slices.Index(w, marotte.MethodSessionCompact); c < 0 || c > k {
		t.Errorf("writes = %v, want session/cancel before the compaction", w)
	}
}

func stopHeld(h *Runtime, chatID marotte.ChatID) bool {
	lc, ok := h.coord.turns.lookup(chatID)
	if !ok {
		return false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return len(lc.stops) > 0
}

// A row's up arrow pressed after the turn it would stop has closed stops nothing: the row the close started runs on.
// A press with no target keeps the older contract and stops the running turn.
func TestRunOrder_ASendNowPressedAfterItsTurnClosedStopsNothing(t *testing.T) {
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
	settleBoot(t, h)
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
	for _, shape := range []string{"beside a fold-opened turn", "with a compaction pending behind it"} {
		t.Run(shape, func(t *testing.T) {
			h, cs, br, tab := closeTabHub(t)
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
			if shape == "with a compaction pending behind it" {
				go func() { _, _ = h.RequestCompact(context.WithoutCancel(t.Context()), "c1") }()
			}
			closed := make(chan int, 1)
			go func() { closed <- postCloseTab(t, h, tab) }()
			time.Sleep(50 * time.Millisecond)
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
			if got := runIDs(t, cs, "c1"); slices.Contains(got, "m-x") || slices.Contains(got, "compaction") {
				t.Errorf("chat's list after the close = %v, want no entry for m-x and no compaction", got)
			}
		})
	}
}

// A steer kiro-cli had not read when its turn closed clean is the next prompt once its end resolves, and no prompt
// goes before: until the clear lands, kiro-cli may still hold it.
func TestRunOrder_ASteerTheAgentDroppedAtACleanCloseIsSentNext(t *testing.T) {
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

// A chat forgotten while its compaction runs answers the /compact waiting on it with the close, and its next life
// starts clean: the old call's return changes nothing there, and a message sent next opens its turn.
func TestRunOrder_ASendAfterTheChatIsForgottenMidCompactionStarts(t *testing.T) {
	h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
	release := holdCall(t, br, marotte.MethodSessionCompact)
	returned := make(chan struct{})
	withCompactionReturnHook(t, func(marotte.ChatID) { close(returned) })
	answered := make(chan error, 1)
	go func() {
		_, err := h.RequestCompact(context.WithoutCancel(t.Context()), "c1")
		answered <- err
	}()
	waitForCall(t, br, marotte.MethodSessionCompact)

	h.coord.turns.forget("c1")
	select {
	case err := <-answered:
		if !errors.Is(err, marotte.ErrChatClosedDuringCall) {
			t.Errorf("the /compact held across the forget answered %v, want ErrChatClosedDuringCall", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("the /compact held across the forget is unanswered")
	}
	if _, err := h.coord.turns.lifecycleAt(t.Context(), "c1", h.coord.LifeStamp()); err != nil {
		t.Fatalf("Setup: the next life: %v", err)
	}
	release()
	<-returned

	if got := runStates(t, cs, "c1"); slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, "compaction") }) {
		t.Errorf("the new life's list = %v, want no compaction", got)
	}
	if got := ackAs(t, postCmd(t, h, promptCmd("m-new", "after the forget"))); got != "started" {
		t.Errorf("the prompt after the forget answered %s, want started", got)
	}
	waitFor(t, func() bool { return slices.Contains(promptIDs(t, cs, "c1"), "m-new") })
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

// A compaction behind a fold-opened turn (frames kiro-cli sent with no turn_start) closes that turn and waits for its
// tail before the call; a prompt sent meanwhile waits behind the compaction.
func TestRunOrder_ACompactionBehindAFoldOpenedTurnClosesItThenRuns(t *testing.T) {
	for _, path := range []string{"/compact", "a reading at the point"} {
		t.Run(path, func(t *testing.T) {
			h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
			br.deliver(newChunkMsg("kiro-cli on its own"))
			waitFor(t, func() bool { return h.coord.turns.live("c1") })
			release := holdCall(t, br, marotte.MethodSessionCompact)
			compacted := make(chan struct{})
			switch path {
			case "/compact":
				go func() {
					defer close(compacted)
					_, _ = h.RequestCompact(context.WithoutCancel(t.Context()), "c1")
				}()
			default:
				close(compacted)
				atThePoint(t, h, cs)
				h.coord.ObserveContextUsage(t.Context(), "c1", 95)
			}
			waitForCall(t, br, marotte.MethodSessionCompact)
			if h.coord.turns.live("c1") {
				t.Error("the fold-opened turn is still open while the compaction runs")
			}

			if got := ackAs(t, postCmd(t, h, promptCmd("m-new", "behind the compaction"))); got != "queued" {
				t.Errorf("the prompt during the compaction answered %s, want queued", got)
			}
			if got := runStates(t, cs, "c1"); !slices.Equal(got, []string{"compaction running", "m-new pending"}) {
				t.Errorf("list during the compaction = %v, want [compaction running, m-new pending]", got)
			}
			if promptAfterLastCompact(br) {
				t.Errorf("a session/prompt went out under the compaction; writes = %v", orderWrites(br))
			}
			release()
			<-compacted
			waitFor(t, func() bool { return slices.Contains(promptIDs(t, cs, "c1"), "m-new") })
		})
	}
}

// A compaction waiting for its fold turn's close tail sleeps until the chat changes: it wakes the chat's waiters only
// when one of its looks changed the list, so the wait never spins against the tail it waits for.
func TestRunOrder_ACompactionWaitingOnItsFoldTurnsTailWakesNoWaiterMeanwhile(t *testing.T) {
	h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
	br.deliver(newChunkMsg("kiro-cli on its own"))
	waitFor(t, func() bool { return h.coord.turns.live("c1") })
	holdTail, inTail := make(chan struct{}), make(chan struct{})
	var once sync.Once
	tailHooks(h, func(_ marotte.ChatID, ended bool) {
		if !ended {
			once.Do(func() {
				close(inTail)
				<-holdTail
			})
		}
	})
	t.Cleanup(func() {
		select {
		case <-holdTail:
		default:
			close(holdTail)
		}
	})
	compacted := make(chan struct{})
	go func() {
		defer close(compacted)
		_, _ = h.RequestCompact(context.WithoutCancel(t.Context()), "c1")
	}()
	<-inTail
	waitFor(t, func() bool {
		return slices.Equal(runStates(t, cs, "c1"), []string{"wire closing", "compaction starting"})
	})

	lc, ok := h.coord.turns.lookup("c1")
	if !ok {
		t.Fatal("Setup: no lifecycle for c1")
	}
	wakes := 0
	window := time.NewTimer(200 * time.Millisecond)
	defer window.Stop()
	for counting := true; counting; {
		lc.mu.Lock()
		changed := lc.changed
		lc.mu.Unlock()
		select {
		case <-changed:
			wakes++
		case <-window.C:
			counting = false
		}
	}
	if wakes > 5 {
		t.Errorf("the chat's waiters were woken %d times in 200 ms while the fold turn's tail was held, want at most 5", wakes)
	}
	close(holdTail)
	waitForCall(t, br, marotte.MethodSessionCompact)
	<-compacted
}

// liveRunCancel is the entry a Cancel with no target stops, as the header names it.
func liveRunCancel(t *testing.T, cs *testChatStore) string {
	t.Helper()
	return runView(t, cs, "c1").Cancel
}

// A turn kiro-cli starts while marotte's sent prompt is not bound yet is listed ahead of it; Cancel names it and stops
// both; the prompt's own turn_start then closes it and the prompt is the head.
func TestRunOrder_AWireTurnBesideASentUnboundPromptIsOnTheList(t *testing.T) {
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

// A wire turn opening between a turn end's compaction take and its call puts the compaction back: compact is written
// only after that turn's close and tail, and a Cancel stops that turn, never the compaction.
func TestRunOrder_AWireTurnBeforeACompactionIsWrittenRequeuesIt(t *testing.T) {
	h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
	atThePoint(t, h, cs)
	entered, releaseStart := make(chan struct{}), make(chan struct{})
	withCompactionStartHook(t, h, func(marotte.ChatID) {
		close(entered)
		<-releaseStart
	})
	t.Cleanup(func() {
		select {
		case <-releaseStart:
		default:
			close(releaseStart)
		}
	})
	runAsync := postAsync(h, promptCmd("m-a", "ends at the point"))
	<-entered
	<-runAsync
	br.deliver(newTurnStartMsg())
	waitFor(t, func() bool { return slices.Contains(runStates(t, cs, "c1"), "wire running") })
	close(releaseStart)

	staysFalse(t, 200*time.Millisecond, func() bool { return slices.Contains(br.callLog(), marotte.MethodSessionCompact) },
		"the compaction was written while kiro-cli's turn was open")
	if got := runStates(t, cs, "c1"); !slices.Equal(got, []string{"wire running", "compaction pending"}) {
		t.Errorf("list = %v, want [wire running, compaction pending]", got)
	}
	target := liveRunCancel(t, cs)
	if rec := postCmd(t, h, cancelCmd(`{"target":"`+target+`"}`)); rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s", rec.Code, rec.Body.String())
	}
	waitFor(t, func() bool { return len(br.notified(marotte.MethodCancel)) == 1 })
	if got := runStates(t, cs, "c1"); !slices.Equal(got, []string{"wire stopping", "compaction pending"}) {
		t.Errorf("list after the cancel = %v, want [wire stopping, compaction pending]", got)
	}
	br.deliver(newTurnEndMsg("cancelled"))
	waitForCall(t, br, marotte.MethodSessionCompact)
	w := orderWrites(br)
	if c, k := slices.Index(w, marotte.MethodCancel), slices.Index(w, marotte.MethodSessionCompact); c < 0 || k < c {
		t.Errorf("writes = %v, want the turn's session/cancel before the compaction", w)
	}
}

// Cancel with kiro-cli running a turn marotte has none for stops that run with one session/cancel and pauses the
// message queued behind it, as any Stop pauses the queue: kiro-cli's idle sends nothing, and Resume sends it.
func TestRunOrder_CancelStopsKiroCLIsRunWithNoTurnHere(t *testing.T) {
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

// A compaction kiro-cli refuses with no turn beside it runs once: a /compact answers refused with no toast, a
// threshold one is toasted once; neither is listed again, and a row behind the threshold one goes next.
func TestRunOrder_ACompactionKiroCLIRefusesWithNoTurnBesideItRunsOnce(t *testing.T) {
	t.Run("/compact", func(t *testing.T) {
		h, cs, _, _ := idleAfterFirstTurn(t, "/tmp/work")
		calls := onCompactor(h, 90, true, 10)
		calls.refuse = true
		if out, err := h.RequestCompact(t.Context(), "c1"); err != nil || out != command.CompactRefused {
			t.Errorf("/compact refused by kiro-cli = (%v, %v), want refused", out, err)
		}
		staysFalse(t, 100*time.Millisecond, func() bool { return calls.count() != 1 }, "the compaction ran more than once")
		if got := calls.toasts(); len(got) != 0 {
			t.Errorf("toasts = %v, want none: the /compact's answer reports it", got)
		}
		if compactionEntry(t, cs, "c1").ID != "" {
			t.Error("the refused compaction is still listed")
		}
	})
	t.Run("at a clean turn end", func(t *testing.T) {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		open := promptGates(t, br)
		calls := onCompactor(h, 90, true, 95)
		calls.refuse = true
		if code := postPrompt(t, h, "a"); code != http.StatusOK {
			t.Fatalf("Setup: prompt = %d", code)
		}
		queueDuring(t, h, "m-q1")
		open(0)
		// m-q1's turn stays open at its gate: its own end would read the point again. The toast is spawned after
		// m-q1 starts, so it is waited for too.
		waitFor(t, func() bool { return slices.Contains(promptIDs(t, cs, "c1"), "m-q1") && len(calls.toasts()) > 0 })
		if calls.count() != 1 {
			t.Errorf("compact calls = %d, want 1", calls.count())
		}
		// The toast is spawned after the next row's take (flushRunLocked).
		waitFor(t, func() bool { return len(calls.toasts()) > 0 })
		if got := calls.toasts(); !slices.Equal(got, []string{compactFailed}) {
			t.Errorf("toasts = %v, want one %q", got, compactFailed)
		}
		if compactionEntry(t, cs, "c1").ID != "" {
			t.Error("the refused compaction is still listed")
		}
	})
}

// Stopping kiro-cli's own turn while a compaction runs aborts the call; the compaction goes back behind that turn
// and is written again only after the turn's close, with no toast.
func TestRunOrder_StoppingKiroCLIsTurnUnderACompactionRequeuesItBehindThatTurn(t *testing.T) {
	h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
	calls := onCompactor(h, 90, true, 10)
	release := holdCall(t, br, marotte.MethodSessionCompact)
	// The fake's compact answers the held call refused (kiro-cli's abort), then accepts the second.
	h.coord.autoCompact.compact = func(ctx context.Context, b command.CompactBridge) (bool, uint64, error) {
		_, _, _ = b.CallAt(ctx, marotte.MethodSessionCompact, map[string]any{})
		calls.mu.Lock()
		defer calls.mu.Unlock()
		calls.calls++
		return calls.calls > 1, 0, nil
	}
	compacted := make(chan struct{})
	go func() {
		defer close(compacted)
		_, _ = h.RequestCompact(context.WithoutCancel(t.Context()), "c1")
	}()
	waitForCall(t, br, marotte.MethodSessionCompact)
	br.deliver(newTurnStartMsg())
	waitFor(t, func() bool { return slices.Contains(runStates(t, cs, "c1"), "wire running") })
	queueDuring(t, h, "m-q1")

	target := liveRunCancel(t, cs)
	if rec := postCmd(t, h, cancelCmd(`{"target":"`+target+`"}`)); rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s", rec.Code, rec.Body.String())
	}
	waitFor(t, func() bool { return len(br.notified(marotte.MethodCancel)) == 1 })
	release()
	<-compacted
	waitFor(t, func() bool { return slices.Contains(runStates(t, cs, "c1"), "compaction pending") })
	if got := runStates(t, cs, "c1"); !slices.Equal(got, []string{"wire stopping", "compaction pending", "m-q1 paused"}) {
		t.Errorf("list once the call returned = %v, want [wire stopping, compaction pending, m-q1 paused]", got)
	}
	if calls.count() != 1 {
		t.Errorf("compact calls before the turn closed = %d, want 1", calls.count())
	}
	br.deliver(newTurnEndMsg("cancelled"))
	waitFor(t, func() bool { return calls.count() == 2 })
	if got := calls.toasts(); len(got) != 0 {
		t.Errorf("toasts = %v, want none", got)
	}
}
