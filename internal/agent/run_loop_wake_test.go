package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
)

type loopWakeCall struct {
	chat  marotte.ChatID
	text  string
	label string
}

type loopWakeRecorder struct {
	err   error
	calls []loopWakeCall
	mu    sync.Mutex
}

func (r *loopWakeRecorder) deliver(_ context.Context, chatID marotte.ChatID, text, label string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, loopWakeCall{chat: chatID, text: text, label: label})
	return r.err
}

func (r *loopWakeRecorder) sent() []loopWakeCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]loopWakeCall(nil), r.calls...)
}

// newLoopWakeHub is a runtime whose chat c1 launched run wf_1, with its delivery recorded.
func newLoopWakeHub(t *testing.T, opts ...Option) (*Runtime, *loopWakeRecorder) {
	t.Helper()
	cs := newTestChatStore()
	h := newRuntimeOn(context.Background(), t.TempDir(), fakeEngineOf(func() engineSession { return newFakeBridge() }, nil), cs, opts...)
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	settleBoot(t, h)
	rec := &loopWakeRecorder{}
	h.runs.loopWake.deliver = rec.deliver
	cs.seed(t, "c1", func(c *marotte.Chat) { c.Name = "Ship it" })
	if _, held := h.runs.lease("wf_1"); !held {
		h.runs.grantLease(t.Context(), "wf_1", "fix-loop", launchOrigin{origin: runlease.OriginAgent, chatID: "c1"}, hostSec())
	}
	return h, rec
}

func loopPausedFrame(pause map[string]any) *marotte.RPCResponse {
	return runNotif(methodWFRunComplete, map[string]any{
		"workflowId": "wf_1", "status": "paused",
		"finalState": map[string]any{"runLabel": "Fix the flaky tests", "pause": pause},
	})
}

func capPause(iterations int) map[string]any {
	return map[string]any{
		"kind": "maxIterations", "nodeId": "review-loop", "maxIterations": iterations, "iterationsRun": iterations,
	}
}

// otherPause is a pause of another kind that still names the loop, so only the kind can refuse it.
func otherPause(kind string) map[string]any {
	return map[string]any{"kind": kind, "nodeId": "review-loop", "iterationsRun": 5}
}

func observePause(t *testing.T, h *Runtime, door marotte.ChatID, pause map[string]any) {
	t.Helper()
	h.runs.observeComplete(t.Context(), door, loopPausedFrame(pause))
	joinInflight(t, h)
}

func TestLoopLimitWake_SendsTheLaunchingChatOneMessage(t *testing.T) {
	h, rec := newLoopWakeHub(t)

	observePause(t, h, "c1", capPause(5))

	got := rec.sent()
	if len(got) != 1 {
		t.Fatalf("deliveries = %d (%+v), want exactly 1", len(got), got)
	}
	if got[0].chat != "c1" {
		t.Errorf("delivered to %q, want the launching chat c1", got[0].chat)
	}
	for _, want := range []string{`"Fix the flaky tests"`, "(wf_1)", "loop review-loop", "after 5 iterations", "extend_repeat", "update_status"} {
		if !strings.Contains(got[0].text, want) {
			t.Errorf("text = %q, want it to carry %q", got[0].text, want)
		}
	}
	if want := "Loop limit reached: Fix the flaky tests"; got[0].label != want {
		t.Errorf("label = %q, want %q", got[0].label, want)
	}
	if l, _ := h.runs.lease("wf_1"); l.LoopWake.State != runlease.LoopWakeDelivered {
		t.Errorf("lease LoopWake = %+v, want the pause recorded delivered", l.LoopWake)
	}
}

func TestLoopLimitWake_UnlabelledRunIsNamedFromItsLease(t *testing.T) {
	h, rec := newLoopWakeHub(t)

	h.runs.observeComplete(t.Context(), "c1", runNotif(methodWFRunComplete, map[string]any{
		"workflowId": "wf_1", "status": "paused", "finalState": map[string]any{"pause": capPause(3)},
	}))
	joinInflight(t, h)

	got := rec.sent()
	if len(got) != 1 || got[0].label != "Loop limit reached: fix-loop" || !strings.Contains(got[0].text, `"fix-loop" (wf_1)`) {
		t.Errorf("deliveries = %+v, want one naming the run by its recipe", got)
	}
}

func TestLoopLimitWake_TheSamePauseSendsOnce(t *testing.T) {
	t.Run("a replayed frame", func(t *testing.T) {
		h, rec := newLoopWakeHub(t)
		observePause(t, h, "c1", capPause(5))
		observePause(t, h, "c1", capPause(5))
		if n := len(rec.sent()); n != 1 {
			t.Errorf("deliveries = %d, want 1: the second frame is the same pause", n)
		}
	})
	t.Run("after a restart", func(t *testing.T) {
		dir := t.TempDir()
		first, err := runlease.NewStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		h, rec := newLoopWakeHub(t, WithRunLeases(first))
		observePause(t, h, "c1", capPause(5))
		if n := len(rec.sent()); n != 1 {
			t.Fatalf("first process deliveries = %d, want 1", n)
		}

		reopened, err := runlease.NewStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		again, rec2 := newLoopWakeHub(t, WithRunLeases(reopened))
		observePause(t, again, "c1", capPause(5))
		if n := len(rec2.sent()); n != 0 {
			t.Errorf("restarted process deliveries = %d, want 0: the run is still in the pause already delivered", n)
		}
	})
	t.Run("after a crash during the send", func(t *testing.T) {
		dir := t.TempDir()
		first, err := runlease.NewStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		h, _ := newLoopWakeHub(t, WithRunLeases(first))
		// What a process killed mid-send leaves on disk.
		var atCrash *runlease.Store
		h.runs.loopWake.deliver = func(context.Context, marotte.ChatID, string, string) error {
			s, openErr := runlease.NewStore(dir)
			atCrash = s
			return openErr
		}
		observePause(t, h, "c1", capPause(5))
		if atCrash == nil {
			t.Fatal("the wake was never sent")
		}

		again, rec2 := newLoopWakeHub(t, WithRunLeases(atCrash))
		observePause(t, again, "c1", capPause(5))
		if n := len(rec2.sent()); n != 0 {
			t.Errorf("restarted process deliveries = %d, want 0: the pause was claimed on disk before the send", n)
		}
	})
}

// Serial: captureLogs swaps the global logger.
func TestLoopLimitWake_AClaimThatCannotBeWrittenIsNotSent(t *testing.T) {
	logs := captureLogs(t)
	dir := t.TempDir()
	store, err := runlease.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	h, rec := newLoopWakeHub(t, WithRunLeases(store))
	// A non-empty directory where the lease file goes refuses the atomic rename, root or not.
	path := filepath.Join(dir, runlease.FileName)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "blocker"), 0o700); err != nil {
		t.Fatal(err)
	}

	observePause(t, h, "c1", capPause(5))

	if n := len(rec.sent()); n != 0 {
		t.Errorf("deliveries = %d, want 0: a restart could send an unrecorded wake again", n)
	}
	if out := logs.String(); !strings.Contains(out, `"level":"WARN"`) || !strings.Contains(out, "was not sent") ||
		!strings.Contains(out, `"workflow_id":"wf_1"`) {
		t.Errorf("logs = %s, want a WARN naming the run", out)
	}
}

// Serial: captureLogs swaps the global logger.
func TestLoopLimitWake_APauseSeenDuringShutdownIsSentAfterTheRestart(t *testing.T) {
	logs := captureLogs(t)
	dir := t.TempDir()
	first, err := runlease.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	h, rec := newLoopWakeHub(t, WithRunLeases(first))
	h.lifecycle.draining.Store(true)
	t.Cleanup(func() { h.lifecycle.draining.Store(false) })

	observePause(t, h, "c1", capPause(5))

	if n := len(rec.sent()); n != 0 {
		t.Errorf("draining process deliveries = %d, want 0", n)
	}
	if out := logs.String(); !strings.Contains(out, `"level":"WARN"`) || !strings.Contains(out, "shutting down") ||
		!strings.Contains(out, `"workflow_id":"wf_1"`) {
		t.Errorf("logs = %s, want a WARN naming the run whose wake shutdown refused", out)
	}
	reopened, err := runlease.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if l, held := reopened.Get("wf_1"); !held || l.LoopWake.Key != "" {
		t.Errorf("lease LoopWake on disk = %+v, want no claim for a pause shutdown refused", l.LoopWake)
	}
	again, rec2 := newLoopWakeHub(t, WithRunLeases(reopened))
	observePause(t, again, "c1", capPause(5))
	if n := len(rec2.sent()); n != 1 {
		t.Errorf("restarted process deliveries = %d, want 1: the refused pause was never sent", n)
	}
}

// The run's host session and its parent's bridge can each deliver the same frame.
func TestLoopLimitWake_ConcurrentFramesSendOnce(t *testing.T) {
	h, rec := newLoopWakeHub(t)

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { h.runs.observeComplete(t.Context(), "c1", loopPausedFrame(capPause(5))) })
	}
	wg.Wait()
	joinInflight(t, h)

	if n := len(rec.sent()); n != 1 {
		t.Errorf("deliveries = %d, want 1 for one pause seen eight times at once", n)
	}
}

// A wake still sending when the extended loop pauses again must not unclaim the newer pause.
func TestLoopLimitWake_ASlowSendKeepsTheNewerClaim(t *testing.T) {
	h, rec := newLoopWakeHub(t)
	release := make(chan struct{})
	h.runs.loopWake.deliver = func(ctx context.Context, chatID marotte.ChatID, text, label string) error {
		if strings.Contains(text, "after 5 iterations") {
			<-release
		}
		return rec.deliver(ctx, chatID, text, label)
	}

	h.runs.observeComplete(t.Context(), "c1", loopPausedFrame(capPause(5)))
	h.runs.observeComplete(t.Context(), "c1", loopPausedFrame(capPause(8)))
	waitFor(t, func() bool { l, _ := h.runs.lease("wf_1"); return l.LoopWake.State == runlease.LoopWakeDelivered })
	close(release)
	joinInflight(t, h)
	observePause(t, h, "c1", capPause(8))

	if n := len(rec.sent()); n != 2 {
		t.Errorf("deliveries = %d, want 2: one per cap, the replayed newer pause sending nothing", n)
	}
}

func TestLoopLimitWake_ANewCapAfterAnExtendSendsAgain(t *testing.T) {
	h, rec := newLoopWakeHub(t)

	observePause(t, h, "c1", capPause(5))
	observePause(t, h, "c1", capPause(8))

	got := rec.sent()
	if len(got) != 2 {
		t.Fatalf("deliveries = %d, want 2: the extended loop stopped at a new cap", len(got))
	}
	if !strings.Contains(got[1].text, "after 8 iterations") {
		t.Errorf("second text = %q, want the new count", got[1].text)
	}
}

func TestLoopLimitWake_SendsNothing(t *testing.T) {
	for _, tc := range []struct {
		setup    func(t *testing.T, h *Runtime)
		pause    map[string]any
		name     string
		workflow string
		door     marotte.ChatID
		config   string
	}{
		{
			name: "for a parentless run", workflow: "wf_2", door: runChatID("wf_2"), pause: capPause(5),
			setup: func(t *testing.T, h *Runtime) {
				h.runs.grantLease(t.Context(), "wf_2", "fix-loop", manualLaunch(), hostSec())
			},
		},
		{
			name: "for a run whose launching chat is gone", workflow: "wf_2", door: "c-gone", pause: capPause(5),
			setup: func(t *testing.T, h *Runtime) {
				h.runs.grantLease(t.Context(), "wf_2", "fix-loop", launchOrigin{origin: runlease.OriginAgent, chatID: "c-gone"}, hostSec())
			},
		},
		{name: "for a step waiting on input", workflow: "wf_1", door: "c1", pause: otherPause("stepInput")},
		{name: "for a retry wait", workflow: "wf_1", door: "c1", pause: otherPause("retry-wait")},
		{name: "for a pause the user asked for", workflow: "wf_1", door: "c1", pause: otherPause("")},
		{name: "for a cap pause naming no loop", workflow: "wf_1", door: "c1", pause: map[string]any{"kind": "maxIterations", "iterationsRun": 5}},
		{name: "with the setting off", workflow: "wf_1", door: "c1", pause: capPause(5), config: `{"loop_limit_wake": false}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var opts []Option
			if tc.config != "" {
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(tc.config), 0o600); err != nil {
					t.Fatal(err)
				}
				opts = append(opts, WithConfigDir(dir))
			}
			h, rec := newLoopWakeHub(t, opts...)
			if tc.setup != nil {
				tc.setup(t, h)
			}
			h.runs.observeComplete(t.Context(), tc.door, runNotif(methodWFRunComplete, map[string]any{
				"workflowId": tc.workflow, "status": "paused", "finalState": map[string]any{"pause": tc.pause},
			}))
			joinInflight(t, h)
			if got := rec.sent(); len(got) != 0 {
				t.Errorf("deliveries = %+v, want none", got)
			}
		})
	}
}

// Serial: captureLogs swaps the global logger.
func TestLoopLimitWake_AFailedDeliveryWarnsAndIsNeverRetried(t *testing.T) {
	logs := captureLogs(t)
	dir := t.TempDir()
	store, err := runlease.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	h, rec := newLoopWakeHub(t, WithRunLeases(store))
	rec.err = errors.New("the bridge could not start")

	observePause(t, h, "c1", capPause(5))
	observePause(t, h, "c1", capPause(5))

	if n := len(rec.sent()); n != 1 {
		t.Errorf("deliveries = %d, want 1: a failed delivery is not retried", n)
	}
	if l, _ := h.runs.lease("wf_1"); l.LoopWake.Key == "" || l.LoopWake.State != runlease.LoopWakeFailed {
		t.Errorf("lease LoopWake = %+v after a failed delivery, want the pause claimed and recorded failed", l.LoopWake)
	}
	out := logs.String()
	if !strings.Contains(out, `"level":"WARN"`) || !strings.Contains(out, "iteration limit") ||
		!strings.Contains(out, `"chat_id":"c1"`) || !strings.Contains(out, `"workflow_id":"wf_1"`) {
		t.Errorf("logs = %s, want a WARN naming the chat and the run", out)
	}

	reopened, err := runlease.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	again, rec2 := newLoopWakeHub(t, WithRunLeases(reopened))
	observePause(t, again, "c1", capPause(5))
	if n := len(rec2.sent()); n != 0 {
		t.Errorf("restarted process deliveries = %d, want 0: a failed wake is not retried after a restart", n)
	}
}

// Through the real prompt path: an idle chat's bridge opens and its agent gets one labelled prompt.
func TestLoopLimitWake_AnIdleChatGetsOneLabelledPrompt(t *testing.T) {
	h, cs, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	seedChat(t, cs, "c1")
	h.runs.grantLease(t.Context(), "wf_1", "fix-loop", launchOrigin{origin: runlease.OriginAgent, chatID: "c1"}, hostSec())
	br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method == marotte.MethodPrompt {
			h.translateACPEvent("c1", h.originOf("c1"), newSessionInfoMsg(map[string]any{
				"kind": "user_message_id_assigned", "userMessageId": "kas-wake",
			}))
		}
		return nil, nil, false
	}

	h.runs.observeComplete(t.Context(), "c1", loopPausedFrame(capPause(5)))
	waitFor(t, func() bool { l, _ := h.runs.lease("wf_1"); return l.LoopWake.State == runlease.LoopWakeDelivered })

	if n := countCalls(br, marotte.MethodPrompt); n != 1 {
		t.Fatalf("session/prompt calls = %d, want 1", n)
	}
	if n := countCalls(br, marotte.MethodSessionSteer); n != 0 {
		t.Errorf("steers = %d, want none to an idle chat", n)
	}
	if got := kasReplayedUserText(t, br.paramsFor(marotte.MethodPrompt)); got != "Loop limit reached: Fix the flaky tests" {
		t.Errorf("transcript label = %q, want the wake's label", got)
	}
}

// Through the real steer path: a chat mid-turn gets one steer and no second prompt.
func TestLoopLimitWake_AChatMidTurnGetsOneSteer(t *testing.T) {
	h, cs, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	seedChat(t, cs, "c1")
	h.runs.grantLease(t.Context(), "wf_1", "fix-loop", launchOrigin{origin: runlease.OriginAgent, chatID: "c1"}, hostSec())
	br.callResults = map[string]json.RawMessage{marotte.MethodSessionSteer: json.RawMessage(`{"queued":true}`)}
	unblock := make(chan struct{})
	br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: unblock}
	postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdPrompt, ChatID: "c1", Payload: json.RawMessage(`{"text":"start","message_id":"m-1"}`),
	})
	waitForCall(t, br, marotte.MethodPrompt)

	h.runs.observeComplete(t.Context(), "c1", loopPausedFrame(capPause(5)))
	waitFor(t, func() bool { l, _ := h.runs.lease("wf_1"); return l.LoopWake.State == runlease.LoopWakeDelivered })
	steers, prompts := countCalls(br, marotte.MethodSessionSteer), countCalls(br, marotte.MethodPrompt)
	close(unblock)

	if steers != 1 {
		t.Errorf("steers = %d, want 1", steers)
	}
	if prompts != 1 {
		t.Errorf("session/prompt calls = %d while the turn ran, want only the user's own", prompts)
	}
}

// Through the real steer record: a steer KAS drops because its turn already ended (queued false) is carried by the
// record into the next prompt once marotte sees that turn end, so the agent reads the wake exactly once and no row
// is left in the chat's steer dock.
func TestLoopLimitWake_ADroppedSteerReachesTheAgentOnceAndLeavesNoRow(t *testing.T) {
	h, cs, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	seedChat(t, cs, "c1")
	h.runs.grantLease(t.Context(), "wf_1", "fix-loop", launchOrigin{origin: runlease.OriginAgent, chatID: "c1"}, hostSec())
	br.callResults = map[string]json.RawMessage{
		marotte.MethodSessionSteer: json.RawMessage(`{"queued":false,"dropped":"epoch_changed"}`),
	}
	unblock := make(chan struct{})
	br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: unblock}
	postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdPrompt, ChatID: "c1", Payload: json.RawMessage(`{"text":"start","message_id":"m-1"}`),
	})
	waitForCall(t, br, marotte.MethodPrompt)

	h.runs.observeComplete(t.Context(), "c1", loopPausedFrame(capPause(5)))
	waitFor(t, func() bool {
		l, _ := h.runs.lease("wf_1")
		return l.LoopWake.State == runlease.LoopWakeDelivered || l.LoopWake.State == runlease.LoopWakeFailed
	})
	br.mu.Lock()
	delete(br.blockOn, marotte.MethodPrompt)
	br.mu.Unlock()
	close(unblock)
	waitFor(t, func() bool { return countCalls(br, marotte.MethodPrompt) == 2 && liveSteerRows(h, "c1") == 0 })

	if l, _ := h.runs.lease("wf_1"); l.LoopWake.State != runlease.LoopWakeDelivered {
		t.Errorf("lease LoopWake = %+v, want the wake recorded delivered", l.LoopWake)
	}
	if n := countCalls(br, marotte.MethodSessionSteer); n != 1 {
		t.Errorf("steers = %d, want the one KAS dropped", n)
	}
	if n := countCalls(br, marotte.MethodPrompt); n != 2 {
		t.Errorf("session/prompt calls = %d, want the user's and the one carrying the wake", n)
	}
	if text := kasReplayedUserText(t, br.paramsFor(marotte.MethodPrompt)); strings.Count(text, "reached its iteration limit") != 1 {
		t.Errorf("the carried prompt = %q, want the wake's text exactly once", text)
	}
	if n := liveSteerRows(h, "c1"); n != 0 {
		t.Errorf("steer dock rows still live = %d, want the carried row retired", n)
	}
}

func liveSteerRows(h *Runtime, chatID marotte.ChatID) int {
	h.bus.steers.mu.Lock()
	defer h.bus.steers.mu.Unlock()
	n := 0
	if rec := h.bus.steers.chats[chatID]; rec != nil {
		rec.live(func(*dockRow) { n++ })
	}
	return n
}

// nestedCapFrame is the run tree of an outer repeat on pass outer whose inner repeat "review-loop" paused at its cap.
func nestedCapFrame(outer, iterations int) *marotte.RPCResponse {
	inner := map[string]any{"nodeId": "review-loop", "type": "repeat", "status": "paused"}
	done := map[string]any{"nodeId": "review-loop", "type": "repeat", "status": "completed"}
	passes := make([]any, 0, outer)
	for i := 1; i <= outer; i++ {
		child := done
		if i == outer {
			child = inner
		}
		passes = append(passes, map[string]any{
			"nodeId": "outer", "type": "iteration", "iteration": i, "status": "running", "children": []any{child},
		})
	}
	root := map[string]any{"nodeId": "outer", "type": "repeat", "status": "running", "children": passes}
	return runNotif(methodWFRunComplete, map[string]any{
		"workflowId": "wf_1", "status": "paused",
		"finalState": map[string]any{"runLabel": "Fix the flaky tests", "root": root, "pause": capPause(iterations)},
	})
}

// KAS restarts an inner repeat's count on each outer pass: the inner loop's cap on a later outer pass is a new pause,
// while the same pass's pause seen again is not.
func TestLoopLimitWake_AnInnerLoopsCapOnALaterOuterPassSendsAgain(t *testing.T) {
	h, rec := newLoopWakeHub(t)

	h.runs.observeComplete(t.Context(), "c1", nestedCapFrame(1, 3))
	joinInflight(t, h)
	h.runs.observeComplete(t.Context(), "c1", nestedCapFrame(1, 3))
	joinInflight(t, h)
	h.runs.observeComplete(t.Context(), "c1", nestedCapFrame(2, 3))
	joinInflight(t, h)

	if n := len(rec.sent()); n != 2 {
		t.Errorf("deliveries = %d, want 2: one per outer pass whose inner loop reached its cap", n)
	}
}
