package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// failsafeChat is chat c1 with Compact automatically on at 90%, whose prompt m-a runs with its session/prompt held
// until release, which answers it with mAStop (cancelled unless set); m-q1 and m-q2 wait behind it. Every later prompt
// answers end_turn, unless hold named its words.
type failsafeChat struct {
	h       *Runtime
	cs      *testChatStore
	br      *fakeBridge
	release func()
	holds   map[string]chan struct{}
	mAStop  string
	toasts  []string
	mu      sync.Mutex
}

func newFailsafeChat(t *testing.T, policyOn bool) *failsafeChat {
	t.Helper()
	h, cs, br, _ := idleAfterFirstTurn(t, "/tmp/work")
	fc := &failsafeChat{h: h, cs: cs, br: br, holds: map[string]chan struct{}{}, mAStop: "cancelled"}
	ac := h.coord.autoCompact
	ac.policy = func(context.Context) (bool, int) { return policyOn, 90 }
	ac.failed = func(_ context.Context, _ marotte.ChatID, detail string) {
		fc.mu.Lock()
		defer fc.mu.Unlock()
		fc.toasts = append(fc.toasts, detail)
	}
	gate := make(chan struct{})
	var once sync.Once
	fc.release = func() { once.Do(func() { close(gate) }) }
	t.Cleanup(fc.release)
	first := true
	br.setOnCall(func(method string, params map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method != marotte.MethodPrompt {
			return nil, nil, false
		}
		words, _ := json.Marshal(params)
		fc.mu.Lock()
		held := first
		first = false
		var wait chan struct{}
		for text, ch := range fc.holds {
			if bytes.Contains(words, []byte(text)) {
				wait = ch
			}
		}
		fc.mu.Unlock()
		switch {
		case held:
			<-gate
			fc.mu.Lock()
			stop := fc.mAStop
			fc.mu.Unlock()
			return json.RawMessage(`{"stopReason":"` + stop + `"}`), nil, true
		case wait != nil:
			<-wait
		}
		return nil, nil, false
	})
	if code := postPrompt(t, h, "a"); code != http.StatusOK {
		t.Fatalf("Setup: prompt = %d", code)
	}
	waitFor(t, func() bool { return slices.Equal(runStates(t, cs, "c1"), []string{"m-a running"}) })
	queueTextDuring(t, h, "m-q1", "first follow-up")
	queueTextDuring(t, h, "m-q2", "second follow-up")
	return fc
}

// hold holds the session/prompt whose words hold text until the returned release.
func (fc *failsafeChat) hold(t *testing.T, text string) (release func()) {
	t.Helper()
	ch := make(chan struct{})
	fc.mu.Lock()
	fc.holds[text] = ch
	fc.mu.Unlock()
	var once sync.Once
	release = func() { once.Do(func() { close(ch) }) }
	t.Cleanup(release)
	return release
}

// compactsWith makes kiro-cli's compaction send frames before it answers, as KAS 3.1.0's handleCompact does: its
// stored figure republished at pct, then the summary.
func (fc *failsafeChat) compactsWith(pct float64) {
	fc.br.mu.Lock()
	defer fc.br.mu.Unlock()
	if fc.br.notifsOnCall == nil {
		fc.br.notifsOnCall = map[string][]*marotte.RPCResponse{}
	}
	fc.br.notifsOnCall[marotte.MethodSessionCompact] = []*marotte.RPCResponse{
		publishedReadingMsg(pct),
		newSessionInfoMsg(map[string]any{"summarization": map[string]any{
			"status": "success", "summary": map[string]any{"conversationSummary": "the work so far"},
		}}),
	}
}

// respond is a model response at pct that called a tool, so its turn goes on: the tool_call frame, then the
// response's own reading, in KAS's order (processChunkStream emits the reading after the response's tool calls).
func (fc *failsafeChat) respond(pct float64) {
	fc.br.deliver(toolCallMsg())
	fc.br.deliver(liveReadingMsg(pct))
}

var toolCalls atomic.Int64

// toolCallMsg is a tool call the chat's own agent opens.
func toolCallMsg() *marotte.RPCResponse {
	update, _ := json.Marshal(map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": fmt.Sprintf("tc-%d", toolCalls.Add(1)),
		"title": "readFile", "kind": "read", "status": "pending",
	})
	params, _ := json.Marshal(map[string]any{"update": json.RawMessage(update)})
	return &marotte.RPCResponse{Method: marotte.MethodSessionUpdate, Params: params}
}

// liveReadingMsg is a model response's own reading, as KAS's in-turn event sends it: no summarizationThreshold.
func liveReadingMsg(pct float64) *marotte.RPCResponse {
	return newSessionInfoMsg(map[string]any{
		"kind": "context_usage", "usagePercentage": pct, "contextUsage": map[string]any{"usagePercentage": pct},
	})
}

// publishedReadingMsg is KAS publishing its stored figure (publishContextUsage), which carries the threshold.
func publishedReadingMsg(pct float64) *marotte.RPCResponse {
	return newSessionInfoMsg(map[string]any{
		"kind": "context_usage", "usagePercentage": pct, "summarizationThreshold": 80,
		"contextUsage": map[string]any{"usagePercentage": pct, "summarizationThreshold": 80},
	})
}

// settled waits until the read loop has handled every frame delivered so far.
func (fc *failsafeChat) settled(t *testing.T) {
	t.Helper()
	want := fc.br.seqNow()
	waitFor(t, func() bool {
		lc, ok := fc.h.coord.turns.lookup("c1")
		if !ok {
			return false
		}
		lc.mu.Lock()
		defer lc.mu.Unlock()
		return lc.observedSeq >= want
	})
}

func (fc *failsafeChat) toastsSoFar() []string {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return slices.Clone(fc.toasts)
}

func (fc *failsafeChat) cancels() int { return len(fc.br.notified(marotte.MethodCancel)) }

// failsafeWords is the chat's stored run list as "<name> <state>", the compaction named "compaction <origin>" and the
// resume "resume".
func failsafeWords(t *testing.T, cs *testChatStore) []string {
	t.Helper()
	c, ok := cs.Get(t.Context(), "c1")
	if !ok {
		return nil
	}
	var out []string
	for _, e := range c.Run {
		name := e.ID
		switch e.Kind {
		case marotte.RunKindCompaction:
			name = "compaction " + string(e.Origin)
		case marotte.RunKindResume:
			name = "resume"
		}
		out = append(out, name+" "+string(e.State))
	}
	return out
}

// openedPrompts is every prompt the chat's turns opened with, in log order.
func openedPrompts(t *testing.T, cs *testChatStore) []marotte.EntryPrompt {
	t.Helper()
	var out []marotte.EntryPrompt
	for _, o := range opensOf(t, logOf(t, cs, "c1")) {
		if o.Prompt != nil {
			out = append(out, *o.Prompt)
		}
	}
	return out
}

// A reading at 95% inside a running turn stops it with one session/cancel, compacts the chat, then sends the resume
// as marotte's own prompt before the rows the reader queued; the stopped turn's close says why it stopped.
func TestFailsafe_CancelsCompactsThenResumesBeforeTheQueuedRows(t *testing.T) {
	fc := newFailsafeChat(t, true)
	fc.compactsWith(96)

	fc.respond(96)

	if !eventually(func() bool { return fc.cancels() == 1 }) {
		t.Fatalf("session/cancel writes = %d after a reading at 96%%, want 1; list %v", fc.cancels(), failsafeWords(t, fc.cs))
	}
	want := []string{"m-a stopping", "compaction failsafe pending", "resume pending", "m-q1 paused", "m-q2 paused"}
	if got := failsafeWords(t, fc.cs); !slices.Equal(got, want) {
		t.Errorf("list after the stop = %v, want %v", got, want)
	}

	fc.release()

	if !eventually(func() bool { return slices.Contains(promptIDs(t, fc.cs, "c1"), "m-q2") }) {
		t.Fatalf("turns opened = %v, want the resume then m-q1 and m-q2", promptIDs(t, fc.cs, "c1"))
	}
	prompts := openedPrompts(t, fc.cs)
	if len(prompts) != 5 {
		t.Fatalf("prompts opened = %+v, want m-first, m-a, the resume, m-q1, m-q2", prompts)
	}
	ids := []string{prompts[0].ID, prompts[1].ID, prompts[3].ID, prompts[4].ID}
	if !slices.Equal(ids, []string{"m-first", "m-a", "m-q1", "m-q2"}) {
		t.Errorf("prompt order = %v around the resume, want [m-first m-a m-q1 m-q2]", promptIDs(t, fc.cs, "c1"))
	}
	r := prompts[2]
	if r.Text != resumeText || !r.Automatic || r.Label != "" || r.Caller != nil {
		t.Errorf("third prompt = %+v, want marotte's automatic resume with its text, no label and no caller", r)
	}
	for _, p := range []marotte.EntryPrompt{prompts[1], prompts[3], prompts[4]} {
		if p.Automatic {
			t.Errorf("prompt %s marked automatic, want only the resume", p.ID)
		}
	}
	wantWrites := []string{
		marotte.MethodCancel, marotte.MethodSessionCompact, marotte.MethodPrompt, marotte.MethodPrompt, marotte.MethodPrompt,
	}
	afterCancel := func() []string {
		w := orderWrites(fc.br)
		if i := slices.Index(w, marotte.MethodCancel); i >= 0 {
			return w[i:]
		}
		return nil
	}
	if !eventually(func() bool { return len(afterCancel()) >= len(wantWrites) }) || !slices.Equal(afterCancel(), wantWrites) {
		t.Errorf("writes = %v, want the cancel, one compaction, then the resume, m-q1 and m-q2", orderWrites(fc.br))
	}
	closes := closesOf(t, logOf(t, fc.cs, "c1"))
	if len(closes) < 2 {
		t.Fatalf("closes = %+v, want m-a's", closes)
	}
	if c := closes[1]; c.Outcome != marotte.TurnOutcomeCancelled || !c.StoppedToCompact || c.FailureReason != failsafeStopNotice {
		t.Errorf("m-a's close = outcome %q stopped_to_compact %v reason %q, want cancelled, stopped to compact, %q",
			c.Outcome, c.StoppedToCompact, c.FailureReason, failsafeStopNotice)
	}
}

// The failsafe point is 95%: a reading under it only lists the point's compaction for the turn's end.
func TestFailsafe_FiresAtNinetyFiveNotBelow(t *testing.T) {
	fc := newFailsafeChat(t, true)

	fc.respond(94.9)
	staysFalse(t, 150*time.Millisecond, func() bool { return fc.cancels() > 0 }, "a reading at 94.9% stopped the turn")
	if got := failsafeWords(t, fc.cs); !slices.Equal(got, []string{"m-a running", "compaction threshold pending", "m-q1 pending", "m-q2 pending"}) {
		t.Errorf("list after 94.9%% = %v, want the point's compaction behind the running turn", got)
	}

	fc.respond(95)
	if !eventually(func() bool { return fc.cancels() == 1 }) {
		t.Errorf("session/cancel writes = %d after a reading at 95%%, want 1", fc.cancels())
	}
}

// A turn parked on an ask is not growing its context: a reading then fires nothing, and the first reading after the
// ask is answered does.
func TestFailsafe_ATurnParkedOnAnAskDoesNotFireUntilItIsAnswered(t *testing.T) {
	fc := newFailsafeChat(t, true)
	id := addPermission(t, fc.h, fc.br, 7, "")

	fc.respond(96)
	staysFalse(t, 150*time.Millisecond, func() bool { return fc.cancels() > 0 }, "a reading while parked on an ask stopped the turn")

	if _, ok := fc.h.bus.pendingPerms.takeIfPresent("c1", id, decisionKindOf(marotte.EventPermissionNeeded)); !ok {
		t.Fatal("Setup: the ask was not pending")
	}
	fc.respond(96)
	if !eventually(func() bool { return fc.cancels() == 1 }) {
		t.Errorf("session/cancel writes = %d after the ask was answered, want 1", fc.cancels())
	}
}

// With Auto-compaction off nothing compacts a chat until the reader asks, so the failsafe stops nothing either.
func TestFailsafe_OffWhenAutoCompactionIsOff(t *testing.T) {
	fc := newFailsafeChat(t, false)

	fc.respond(99)

	staysFalse(t, 150*time.Millisecond, func() bool { return fc.cancels() > 0 }, "a reading at 99% stopped the turn with Auto-compaction off")
	if c := compactionEntry(t, fc.cs, "c1"); c.ID != "" {
		t.Errorf("compaction listed with Auto-compaction off: %+v", c)
	}
}

// A failsafe compaction kiro-cli refuses continues nothing: no resume is sent, the rows stay paused, and the toast
// says the stopped turn was not continued.
func TestFailsafe_ARefusedCompactionsToastSaysTheTurnWasNotContinued(t *testing.T) {
	fc := newFailsafeChat(t, true)
	fc.br.setCallResult(marotte.MethodSessionCompact, json.RawMessage(`{"success":false}`))

	fc.respond(96)
	if !eventually(func() bool { return fc.cancels() == 1 }) {
		t.Fatalf("session/cancel writes = %d, want 1", fc.cancels())
	}
	fc.release()

	if !eventually(func() bool { return len(fc.toastsSoFar()) == 1 }) {
		t.Fatalf("toasts = %v, want one", fc.toastsSoFar())
	}
	if got := fc.toastsSoFar(); !slices.Equal(got, []string{compactFailsafeFailed}) {
		t.Errorf("toasts = %v, want %q", got, compactFailsafeFailed)
	}
	staysFalse(t, 150*time.Millisecond, func() bool { return len(openedPrompts(t, fc.cs)) > 2 },
		"a prompt opened after the refused failsafe compaction")
	if got := failsafeWords(t, fc.cs); !slices.Equal(got, []string{"m-q1 paused", "m-q2 paused"}) {
		t.Errorf("list = %v, want the rows paused and no resume", got)
	}
}

// A reading at 95% from a response that called no tool since the last reading is the turn's last reply: stopping it
// would only interrupt a turn whose end compacts anyway, so it stops nothing and the point's compaction stays listed
// for that end. The same reading from a response that called a tool stops the turn.
func TestFailsafe_OnlyAResponseThatCalledAToolStopsTheTurn(t *testing.T) {
	fc := newFailsafeChat(t, true)

	fc.respond(50)
	fc.br.deliver(liveReadingMsg(96))
	fc.settled(t)
	staysFalse(t, 150*time.Millisecond, func() bool { return fc.cancels() > 0 }, "a tool-free reply at 96% stopped the turn")
	if got := failsafeWords(t, fc.cs); !slices.Equal(got, []string{"m-a running", "compaction threshold pending", "m-q1 pending", "m-q2 pending"}) {
		t.Errorf("list after a tool-free reply at 96%% = %v, want the point's compaction behind the running turn", got)
	}

	fc.respond(96)
	if !eventually(func() bool { return fc.cancels() == 1 }) {
		t.Errorf("session/cancel writes = %d after a reply at 96%% that called a tool, want 1", fc.cancels())
	}
}

// KAS keeps publishing its pre-summary figure after a compaction (handleCompact leaves lastContextUsagePct, and a
// session load seeds it from the last persisted reading) until its next live reading: that figure stops nothing,
// lists nothing and is not shown, while a fresh reading at 95% in the resumed turn still stops it.
func TestFailsafe_KASRepublishingItsPreSummaryFigureStopsNothing(t *testing.T) {
	fc := newFailsafeChat(t, true)
	fc.compactsWith(96)
	releaseResume := fc.hold(t, "your last turn was stopped")
	defer releaseResume()

	fc.respond(96)
	if !eventually(func() bool { return fc.cancels() == 1 }) {
		t.Fatalf("Setup: session/cancel writes = %d, want 1", fc.cancels())
	}
	fc.release()
	waitFor(t, func() bool {
		w := failsafeWords(t, fc.cs)
		return len(w) > 0 && w[0] == "resume running"
	})

	fc.br.deliver(toolCallMsg())
	fc.br.deliver(publishedReadingMsg(96))
	fc.settled(t)
	if c, _ := fc.cs.Get(t.Context(), "c1"); c.Usage.ContextPct != 0 || c.Usage.HasRealData || !c.Usage.ContextCompacted {
		t.Errorf("usage after the republished figure = %+v, want the compaction's cleared figure kept", c.Usage)
	}
	fc.respond(40)
	fc.br.deliver(toolCallMsg())
	fc.br.deliver(publishedReadingMsg(96))
	fc.settled(t)
	staysFalse(t, 150*time.Millisecond, func() bool { return fc.cancels() > 1 }, "KAS's republished figure stopped the resumed turn")
	if c := compactionEntry(t, fc.cs, "c1"); c.ID != "" {
		t.Errorf("compaction listed for KAS's republished figure: %+v", c)
	}

	fc.respond(96)
	if !eventually(func() bool { return fc.cancels() == 2 }) {
		t.Errorf("session/cancel writes = %d after a fresh reading at 96%% in the resumed turn, want 2", fc.cancels())
	}
}

// The same holds after a compaction at the point: the next turn is not stopped or compacted for the figure KAS
// republishes, only for its own readings.
func TestFailsafe_KASRepublishingItsFigureAfterAPointCompactionStopsNothing(t *testing.T) {
	fc := newFailsafeChat(t, true)
	fc.compactsWith(92)
	fc.mu.Lock()
	fc.mAStop = "end_turn"
	fc.mu.Unlock()
	releaseNext := fc.hold(t, "first follow-up")
	defer releaseNext()

	fc.respond(92)
	waitFor(t, func() bool { return compactionEntry(t, fc.cs, "c1").ID != "" })
	fc.release()
	waitFor(t, func() bool {
		w := failsafeWords(t, fc.cs)
		return len(w) > 0 && w[0] == "m-q1 running"
	})

	fc.br.deliver(toolCallMsg())
	fc.br.deliver(publishedReadingMsg(96))
	fc.settled(t)
	staysFalse(t, 150*time.Millisecond, func() bool { return fc.cancels() > 0 }, "KAS's republished figure stopped the next turn")
	if c := compactionEntry(t, fc.cs, "c1"); c.ID != "" {
		t.Errorf("compaction listed for KAS's republished figure: %+v", c)
	}

	fc.respond(96)
	if !eventually(func() bool { return fc.cancels() == 1 }) {
		t.Errorf("session/cancel writes = %d after a fresh reading at 96%%, want 1", fc.cancels())
	}
}
