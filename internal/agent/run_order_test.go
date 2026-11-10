package agent

import (
	"context"
	"encoding/json"
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
	waitForCall(t, br, marotte.MethodSessionCompact)
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

// A prompt and a send-now racing an idle /compact's start never put a session/prompt under the compaction.
func TestRunOrder_APromptOrSendNowAsACompactionStartsOpensNoTurnUnderIt(t *testing.T) {
	for range 5 {
		h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
		seedQueue(t, cs, "c1", marotte.QueuedPrompt{ID: "m-q1", Text: "queued"})
		release := holdCall(t, br, marotte.MethodSessionCompact)
		var wg sync.WaitGroup
		wg.Go(func() { _, _ = h.RequestCompact(context.WithoutCancel(t.Context()), "c1") })
		answered := postAsync(h, promptCmd("m-p", "racing the compaction"))
		marked := postAsync(h, marotte.ClientCommand{
			Type: marotte.CmdSendQueuedNow, ChatID: "c1", Payload: json.RawMessage(`{"message_id":"m-q1"}`),
		})
		staysFalse(t, 100*time.Millisecond, func() bool { return promptAfterLastCompact(br) },
			"a session/prompt went out under the compaction")
		release()
		wg.Wait()
		<-answered
		<-marked
		shutdownHub(t, h)
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
	withCompactionReturnHook(t, func(marotte.ChatID) { gap = postAsync(h, promptCmd("m-gap", "as it ends")) })
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
