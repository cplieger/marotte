package agent

// The idle window measures LIVENESS, not tool-call frames: any live step frame refills
// it, a live agent terminal holds it, and it fires only on a true stall, which is what
// the recorded reason then says. The trade-off it accepts is stated at cancelExpired: a
// shell command that is genuinely hung is bounded by the backstop alone.

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
)

// terminalsAnswering is a runTerminalReader with one fixed answer per chat, for the
// cases below whose subject is the refill arithmetic rather than the scoping: it
// answers for whatever sessions it is asked about. What the scope itself decides is
// pinned by terminalsBySession (run_bounds_step_test.go).
type terminalsAnswering map[marotte.ChatID]bool

func (t terminalsAnswering) LiveTerminalForSession(chatID marotte.ChatID, sessions map[string]struct{}) bool {
	return len(sessions) > 0 && t[chatID]
}

// stagedExpiry leases, arms and stages a deadline already in the past, returning
// the staged deadline the timer would have fired for.
func stagedExpiry(t *testing.T, rs *Runs, id string, launch launchOrigin) time.Time {
	t.Helper()
	rs.grantLease(t.Context(), id, "publish", launch)
	rs.armDeadline(t.Context(), id)
	deadline := time.Now().Add(-time.Second)
	if err := rs.leaseStore().SetDeadline(t.Context(), id, deadline); err != nil {
		t.Fatalf("stage an expired deadline: %v", err)
	}
	return deadline
}

func TestCancelExpired_ALiveTerminalRefillsTheWindow(t *testing.T) {
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})
	h.runs.terminals = terminalsAnswering{runChatID(id): true}
	h.runs.log = newRunLog(t.TempDir())
	openStep(t, h, id, "root/a", "step-session-a")
	deadline := stagedExpiry(t, h.runs, id, manualLaunch())

	h.runs.cancelExpired(id, deadline)

	if got := h.runs.endReason(id); got != "" {
		t.Errorf("a run waiting on its own shell was cancelled %q; want the window refilled", got)
	}
	if slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
		t.Errorf("a cancel went out for a run with a live terminal: %v", br.callLog())
	}
	l, _ := h.runs.lease(id)
	if budget := time.Until(l.Deadline); budget < runIdleWindow-time.Second {
		t.Errorf("the refill left %v of budget, want a full idle window %v", budget.Round(time.Second), runIdleWindow)
	}
}

// The carrier for an agent-launched run is the LAUNCHING chat, not run:<id>.
func TestCancelExpired_AnAgentRunsCarrierIsTheLaunchingChat(t *testing.T) {
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})
	h.runs.terminals = terminalsAnswering{"c1": true}
	h.runs.log = newRunLog(t.TempDir())
	openStep(t, h, id, "root/a", "step-session-a")
	deadline := stagedExpiry(t, h.runs, id, launchOrigin{origin: runlease.OriginAgent, chatID: "c1"})

	h.runs.cancelExpired(id, deadline)

	if got := h.runs.endReason(id); got != "" {
		t.Errorf("an agent-launched run whose chat holds a live terminal was cancelled %q", got)
	}
}

func TestCancelExpired_NoFrameAndNoShellIsAStall(t *testing.T) {
	logs := captureLogs(t)
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})
	h.runs.terminals = terminalsAnswering{runChatID(id): false}
	deadline := stagedExpiry(t, h.runs, id, manualLaunch())

	h.runs.cancelExpired(id, deadline)

	if got := h.runs.endReason(id); got != runEndStalled {
		t.Errorf("endReason = %q, want %q", got, runEndStalled)
	}
	if !slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
		t.Errorf("no cancel went out for the stalled run: %v", br.callLog())
	}
	if out := logs.String(); !strings.Contains(out, logMsgRunStalled) {
		t.Errorf("the stall was not logged as one: %s", out)
	}
}

// A live shell holds the idle window and nothing else: the backstop is the bound a
// genuinely hung command meets, so it fires whatever the terminal registry says.
func TestCancelExpired_ASpentBackstopOutranksALiveTerminal(t *testing.T) {
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})
	h.runs.terminals = terminalsAnswering{runChatID(id): true}
	h.runs.log = newRunLog(t.TempDir())
	openStep(t, h, id, "root/a", "step-session-a")
	deadline := stagedExpiry(t, h.runs, id, manualLaunch())
	h.runs.mu.Lock()
	h.runs.bounds.executed = map[string]time.Duration{id: runBackstop}
	h.runs.mu.Unlock()

	h.runs.cancelExpired(id, deadline)

	if got := h.runs.endReason(id); got != runEndOverran {
		t.Errorf("endReason = %q, want %q: a spent backstop is not a stall and a live shell does not lift it", got, runEndOverran)
	}
}

// A live terminal met inside the last minute before the backstop: the refill used to
// decline (the throttle read the spent deadline as a standing timer) and the arm
// returned anyway, leaving the run bounded by nothing. Now the refill stamps the
// backstop instant, so a timer stands and no cancel goes out.
func TestCancelExpired_ARefillInsideTheBackstopsLastMinuteStillBounds(t *testing.T) {
	h, _, br := newTestHub()
	defer shutdownHub(t, h)
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})
	h.runs.terminals = terminalsAnswering{runChatID(id): true}
	h.runs.log = newRunLog(t.TempDir())
	openStep(t, h, id, "root/a", "step-session-a")
	deadline := stagedExpiry(t, h.runs, id, manualLaunch())
	h.runs.mu.Lock()
	h.runs.bounds.armedAt[id] = time.Now().Add(-runBackstop + 30*time.Second)
	h.runs.mu.Unlock()

	h.runs.cancelExpired(id, deadline)

	if slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
		t.Errorf("a cancel went out for a run with a live terminal: %v", br.callLog())
	}
	l, _ := h.runs.lease(id)
	if budget := time.Until(l.Deadline); budget <= 0 || budget > time.Minute {
		t.Errorf("the refill left %v of budget, want the backstop instant inside the next minute", budget.Round(time.Second))
	}
}

// A permission ask is the last frame a step produces until a person answers, and it
// arrives as its own request rather than a session/update, so the report has its own
// door. Attributed over the request's session id.
func TestRequestPermission_AStepsAskRefillsTheWindow(t *testing.T) {
	const (
		chatID  = marotte.ChatID("chat-step")
		stepSID = "step-session-1"
		id      = "wf_1"
	)
	h, _, _ := newTestHub()
	defer shutdownHub(t, h)
	registerParentSession(t, h, chatID, "parent-A")
	h.translator.RecordStepSession(stepSID, id, "build", id+"/build")
	leased(t, h.runs, id)
	h.runs.armDeadline(t.Context(), id)
	if err := h.runs.leaseStore().SetDeadline(t.Context(), id, time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("stage a near-expiry deadline: %v", err)
	}

	reqID := int64(7)
	params := mustJSON(t, map[string]any{
		"sessionId": stepSID,
		"toolCall":  map[string]any{"toolCallId": "tc-1", "title": "Run tests", "kind": "execute"},
		"options":   []map[string]any{{"optionId": "allow", "kind": "allow_once"}},
	})
	h.chatHandlers[marotte.MethodRequestPermission](t.Context(), chatID, &marotte.RPCResponse{
		ID: &reqID, Method: marotte.MethodRequestPermission, Params: params,
	})

	l, _ := h.runs.lease(id)
	if budget := time.Until(l.Deadline); budget < runIdleWindow-time.Second {
		t.Errorf("a step's permission ask left %v of budget, want a full idle window %v",
			budget.Round(time.Second), runIdleWindow)
	}
}

// TestHandleSessionUpdate_AStepsThinkingChunkRefillsTheWindow pins the report's
// placement at the frame's door: a thinking chunk reaches no tool handler, so under
// the old tool-call report a step that only reasoned for fifteen minutes was a stall.
func TestHandleSessionUpdate_AStepsThinkingChunkRefillsTheWindow(t *testing.T) {
	const (
		chatID  = marotte.ChatID("chat-step")
		stepSID = "step-session-1"
		id      = "wf_1"
	)
	h, _, _ := newTestHub()
	defer shutdownHub(t, h)
	registerParentSession(t, h, chatID, "parent-A")
	h.translator.RecordStepSession(stepSID, id, "build", id+"/build")
	leased(t, h.runs, id)
	h.runs.armDeadline(t.Context(), id)
	if err := h.runs.leaseStore().SetDeadline(t.Context(), id, time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("stage a near-expiry deadline: %v", err)
	}

	update := mustJSON(t, map[string]any{
		"sessionUpdate": string(marotte.ACPUpdateThoughtChunk),
		"content":       map[string]any{"type": "text", "text": "weighing the options"},
	})
	params := mustJSON(t, map[string]any{"sessionId": stepSID, "update": update})
	h.handleSessionUpdate(t.Context(), chatID, &marotte.RPCResponse{Params: params})

	l, _ := h.runs.lease(id)
	if budget := time.Until(l.Deadline); budget < runIdleWindow-time.Second {
		t.Errorf("a step's thinking chunk left %v of budget, want a full idle window %v",
			budget.Round(time.Second), runIdleWindow)
	}
}

// A replayed frame is stored history, not progress.
func TestHandleSessionUpdate_AReplayedStepFrameIsNotProgress(t *testing.T) {
	const (
		chatID  = marotte.ChatID("chat-step")
		stepSID = "step-session-1"
		id      = "wf_1"
	)
	h, _, _ := newTestHub()
	defer shutdownHub(t, h)
	registerParentSession(t, h, chatID, "parent-A")
	h.translator.RecordStepSession(stepSID, id, "build", id+"/build")
	leased(t, h.runs, id)
	h.runs.armDeadline(t.Context(), id)
	staged := time.Now().Add(time.Minute)
	if err := h.runs.leaseStore().SetDeadline(t.Context(), id, staged); err != nil {
		t.Fatalf("stage a near-expiry deadline: %v", err)
	}

	update := mustJSON(t, map[string]any{
		"sessionUpdate": string(marotte.ACPUpdateThoughtChunk),
		"content":       map[string]any{"type": "text", "text": "weighing the options"},
		"_meta":         map[string]any{"kiro": map[string]any{"replay": true}},
	})
	params := mustJSON(t, map[string]any{"sessionId": stepSID, "update": update})
	h.handleSessionUpdate(t.Context(), chatID, &marotte.RPCResponse{Params: params})

	l, _ := h.runs.lease(id)
	if !l.Deadline.Equal(staged) {
		t.Errorf("a replayed frame moved the deadline to %v from %v", l.Deadline, staged)
	}
}

// TestFinishTermination_TellsTheLaunchingChatWhy: KAS's cancel carries no reason, so
// the launching chat hears only "aborted" unless the bound leaves its own note, as an
// agent-origin steer carrying the run and when it finished.
func TestFinishTermination_TellsTheLaunchingChatWhy(t *testing.T) {
	h, cs, br := newTestHub()
	defer shutdownHub(t, h)
	const (
		id     = "wf_1"
		chatID = marotte.ChatID("c1")
	)
	if _, err := cs.Mutate(t.Context(), chatID, func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		return true
	}); err != nil {
		t.Fatalf("seed the chat: %v", err)
	}
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	carrier := &sharedBridge{bridge: br, state: bridgeIdle}
	h.bridge.mgr.insert(chatID, carrier)
	h.runs.grantLease(t.Context(), id, "nightly", launchOrigin{origin: runlease.OriginAgent, chatID: string(chatID)})
	before := time.Now().UnixMilli()

	if err := h.runs.finishTermination(t.Context(), id, runEndStalled, carrier); err != nil {
		t.Fatalf("finishTermination: %v", err)
	}

	entries, err := cs.All(t.Context(), chatID)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	i := slices.IndexFunc(entries, func(e marotte.Entry) bool { return e.Kind == marotte.EntryKindSteer })
	if i < 0 {
		t.Fatalf("no steer entry reached the launching chat; entries: %+v", entries)
	}
	var steer marotte.EntrySteer
	if err := json.Unmarshal(entries[i].Payload, &steer); err != nil {
		t.Fatalf("decode the steer: %v", err)
	}
	if want := runEndNoteID(id, steer.ProducedTs); entries[i].ID != want {
		t.Errorf("note id = %q, want %q (the stop's own instant, so a retried run's second stop is a second note)", entries[i].ID, want)
	}
	if steer.Origin != marotte.SteerOriginAgent || steer.State != marotte.SteerStateRead {
		t.Errorf("note origin/state = %q/%q, want agent/read", steer.Origin, steer.State)
	}
	if !strings.Contains(steer.Text, "nightly") || !strings.Contains(steer.Text, "no activity and no live shell for 15 minutes") {
		t.Errorf("note text = %q, want the recipe and the stall reason in minutes", steer.Text)
	}
	if steer.OriginRun != id || steer.ProducedTs < before {
		t.Errorf("note provenance = run %q at %d, want run %q at or after %d", steer.OriginRun, steer.ProducedTs, id, before)
	}
}

// A parentless run has no chat to tell; its row carries the reason.
func TestFinishTermination_AParentlessRunLeavesNoNote(t *testing.T) {
	h, cs, br := newTestHub()
	defer shutdownHub(t, h)
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	carrier := &sharedBridge{bridge: br, state: bridgeIdle}
	h.bridge.mgr.insert(runChatID(id), carrier)
	leased(t, h.runs, id)

	if err := h.runs.finishTermination(t.Context(), id, runEndStalled, carrier); err != nil {
		t.Fatalf("finishTermination: %v", err)
	}
	if got := h.runs.endReason(id); got != runEndStalled {
		t.Errorf("endReason = %q, want %q", got, runEndStalled)
	}
	if len(cs.List(t.Context())) != 0 {
		t.Errorf("a parentless run's note created a chat: %+v", cs.List(t.Context()))
	}
}
