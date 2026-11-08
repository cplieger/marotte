package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/tabs"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/turnlog"
)

// ownerHub is a runtime whose chat c1 has an open, idle session on its own fake, with a tab store holding c1's tab
// when the hub is tabbed. Build it inside a synctest bubble.
type ownerHub struct {
	h   *Runtime
	cs  *testChatStore
	st  *tabs.Store
	sb  *sharedBridge
	br  *fakeBridge
	sid marotte.SessionID
}

func newOwnerHub(t *testing.T, tabbed bool) *ownerHub {
	t.Helper()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("Setup: tab store: %v", err)
	}
	cs := newTestChatStore()
	h := newRuntimeOn(context.Background(), t.TempDir(), fakeEngineOf(func() engineSession { return newFakeBridge() }, nil),
		cs, WithConfigDir(t.TempDir()), WithTabs(st))
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true }); err != nil {
		t.Fatalf("Setup: seed c1: %v", err)
	}
	settleBoot(t, h)
	if tabbed {
		if _, _, _, err := st.Open(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c1"}); err != nil {
			t.Fatalf("Setup: open c1's tab: %v", err)
		}
	}
	sb, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("Setup: OpenBridge(c1): %v", err)
	}
	return &ownerHub{h: h, cs: cs, st: st, sb: sb, br: fakeOf(sb), sid: sb.SessionID()}
}

// keptOpen reports that c1's entry is still the first one, its session open and no close claimed on it.
func (o *ownerHub) keptOpen() bool {
	o.sb.mu.Lock()
	claimed := o.sb.closeClaim != nil
	o.sb.mu.Unlock()
	return !claimed && o.h.bridge.mgr.get("c1") == o.sb && !sessionClosed(o.br)
}

// closed reports that c1's first session closed and its entry left the registry with no generation loaded after it.
func (o *ownerHub) closed() bool {
	return sessionClosed(o.br) && o.h.bridge.mgr.get("c1") == nil
}

// reloadsInline is the chat's next open closing the first session and loading the chat's session again with the
// current settings, in that one call.
func (o *ownerHub) reloadsInline(t *testing.T) *sharedBridge {
	t.Helper()
	next, err := o.h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("the next OpenBridge(c1): %v", err)
	}
	if next == o.sb || !sessionClosed(o.br) {
		t.Fatal("the next open kept the marked session instead of closing and loading it again")
	}
	if opts := fakeOf(next).lastSessionOpts(); opts == nil || opts.SessionID != string(o.sid) {
		t.Errorf("the next generation's door = %+v, want a load of the chat's session %q", opts, o.sid)
	}
	return next
}

// frame is msg on c1's own session.
func (o *ownerHub) frame(t *testing.T, msg *marotte.RPCResponse) *marotte.RPCResponse {
	t.Helper()
	return withSessionID(t, msg, string(o.sid))
}

// kasDelivers routes msg as KAS's connection does: to the lane of the chat whose resident session the frame names,
// else to the engine lane.
func kasDelivers(t *testing.T, h *Runtime, msg *marotte.RPCResponse) {
	t.Helper()
	var p struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		t.Fatalf("decode the frame's params: %v", err)
	}
	if chatID, ok := h.coord.ChatForACPSession(p.SessionID); ok {
		h.translateACPEvent(chatID, h.originOf(chatID), msg)
		return
	}
	h.engineFrame(fakeSourceOf(t, h).current(), msg)
}

func permissionAsk(sessionID string, id int64) *marotte.RPCResponse {
	params, _ := json.Marshal(map[string]any{"sessionId": sessionID, "toolCall": map[string]any{"toolCallId": "t-owner"}})
	return &marotte.RPCResponse{Method: marotte.MethodRequestPermission, ID: &id, Params: params}
}

type openResult struct {
	sb  *sharedBridge
	err error
}

// openAsync is a non-prompt door on c1 on its own goroutine, so a door that blocks is reported, not hung.
func openAsync(t *testing.T, h *Runtime) <-chan openResult {
	t.Helper()
	got := make(chan openResult, 1)
	go func() {
		sb, err := h.coord.openBridge(t.Context(), "c1", "")
		got <- openResult{sb: sb, err: err}
	}()
	return got
}

// openReturnsCurrent asserts that the open answered at once with the first session and closed nothing.
func (o *ownerHub) openReturnsCurrent(t *testing.T, got <-chan openResult, while string) {
	t.Helper()
	synctest.Wait()
	select {
	case r := <-got:
		if r.err != nil || r.sb != o.sb || !o.keptOpen() {
			t.Errorf("an open while %s = (same entry %v, %v), session kept %v; want the current session, left open",
				while, r.sb == o.sb, r.err, o.keptOpen())
		}
	default:
		t.Errorf("an open while %s blocked; a door never waits for a hold to end", while)
	}
}

// tailGate holds every close tail of h at its drain until open closes.
type tailGate struct {
	open    chan struct{}
	reached atomic.Int32
	once    sync.Once
}

func blockTails(t *testing.T, h *Runtime) *tailGate {
	t.Helper()
	g := &tailGate{open: make(chan struct{})}
	t.Cleanup(g.release)
	drain := h.coord.drainAfterClose
	h.coord.drainAfterClose = func(ctx context.Context, chatID marotte.ChatID, cl command.CloseFacts, ends command.EndFacts) {
		g.reached.Add(1)
		select {
		case <-g.open:
		case <-ctx.Done():
			return
		}
		drain(ctx, chatID, cl, ends)
	}
	return g
}

func (g *tailGate) release() { g.once.Do(func() { close(g.open) }) }

// countCloses counts the session/close calls br receives from now on.
func countCloses(br *fakeBridge) *atomic.Int32 {
	var n atomic.Int32
	br.mu.Lock()
	br.onCloseStart = func() { n.Add(1) }
	br.mu.Unlock()
	return &n
}

func lifecycleOf(t *testing.T, h *Runtime, chatID marotte.ChatID) *chatLifecycle {
	t.Helper()
	lc, ok := h.coord.turns.lookup(chatID)
	if !ok {
		t.Fatalf("Setup: %s has no lifecycle", chatID)
	}
	return lc
}

func once(fn func()) func() {
	var o sync.Once
	return func() { o.Do(fn) }
}

const hostRun, hostStep = "wf-host", "wf-host/step"

// hostStepOn opens a run step turn on c1's session, as a step of a run the chat hosts does.
func hostStepOn(t *testing.T, h *Runtime, sessionID string) func() {
	t.Helper()
	if _, _, err := h.runs.log.open(t.Context(), &translate.RunStep{RunID: hostRun, NodePath: hostStep, SessionID: sessionID}, "c1"); err != nil {
		t.Fatalf("Setup: open the hosted step turn: %v", err)
	}
	return once(func() {
		if _, _, err := h.runs.log.closeNode(t.Context(), hostRun, hostStep, string(marotte.RunStatusCompleted), "", nil); err != nil {
			t.Errorf("close the hosted step turn: %v", err)
		}
	})
}

// launchRun gives c1 a live run it launched, whose parent session is c1's.
func (o *ownerHub) launchRun(t *testing.T, workflowID string) {
	t.Helper()
	o.h.runs.grantLease(t.Context(), workflowID, "fix", launchOrigin{origin: runlease.OriginAgent, chatID: "c1"}, hostSec())
	o.h.runs.recordParentSession(t.Context(), workflowID, string(o.sid))
}

func deliverRunComplete(t *testing.T, h *Runtime, chatID marotte.ChatID, workflowID, status string) {
	t.Helper()
	h.chatHandlers[methodWFRunComplete](t.Context(), chatID, runNotif(methodWFRunComplete, map[string]any{
		"workflowId": workflowID, "status": status,
	}))
}

func (o *ownerHub) logHolds(t *testing.T, text string) bool {
	t.Helper()
	return slices.Contains(textsOf(t, logOf(t, o.cs, "c1")), text)
}

func TestCloseOwner_ReloadWaitsForAKASWireTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := newOwnerHub(t, true)
		o.h.markSessionsForReload("test")
		send(t, o.h, newTurnStartMsg())
		turnID, open := ownID(o.h, "c1")
		if !open {
			t.Fatal("Setup: the turn_start opened no wire turn")
		}

		o.openReturnsCurrent(t, openAsync(t, o.h), "KAS runs a turn it started")

		if id, open := ownID(o.h, "c1"); !open || id != turnID {
			t.Errorf("own turn = %q (open %v), want KAS's turn %q left running", id, open, turnID)
		}
		send(t, o.h, newTurnEndMsg("end_turn"))
		synctest.Wait()
		if !o.keptOpen() {
			t.Fatal("the turn's end closed a tabbed chat's session outside a door")
		}
		o.reloadsInline(t)
	})
}

func TestCloseOwner_ReloadWaitsForAWireTurnBesideTheOpenersPrompt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := newOwnerHub(t, true)
		o.h.markSessionsForReload("test")
		o.h.stageWireTurn(t, "c1")
		res, ok := o.h.coord.turns.tryReserve("c1", marotte.TurnSourcePrompt, o.h.coord.LifeStamp())
		if !ok {
			t.Fatal("Setup: the opener's reservation was refused beside an unbracketed wire turn")
		}
		t.Cleanup(func() { o.h.coord.turns.releaseReservation(res) })
		if _, err := o.h.coord.openTurn(t.Context(), "c1", command.TurnOpen{
			Source: marotte.TurnSourcePrompt, Prompt: &marotte.EntryPrompt{ID: "m-opener", Text: "hello"},
		}); err != nil {
			t.Fatalf("Setup: open the opener's prompt turn: %v", err)
		}

		got := make(chan openResult, 1)
		go func() {
			sb, err := o.h.coord.openBridgeAt(t.Context(), "c1", "", res.Stamp)
			got <- openResult{sb: sb, err: err}
		}()
		o.openReturnsCurrent(t, got, "a wire turn runs beside the opener's unsent prompt")

		lc := lifecycleOf(t, o.h, "c1")
		lc.mu.Lock()
		defer lc.mu.Unlock()
		if lc.own == nil || lc.own.Source.PromptClass() || lc.own.bracketed || lc.own.sent {
			t.Errorf("own = %+v, want KAS's unbracketed, unsent wire turn", lc.own)
		}
		if lc.pending == nil || lc.pending.sent {
			t.Errorf("pending = %+v, want the opener's prompt still awaiting its send", lc.pending)
		}
	})
}

func TestCloseOwner_ReloadWaitsForAnotherCallersReservation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := newOwnerHub(t, true)
		o.h.markSessionsForReload("test")
		if !o.h.coord.tryReserveIdleNow("c1", marotte.TurnSourcePrompt) {
			t.Fatal("Setup: the admission slot was not free")
		}

		o.openReturnsCurrent(t, openAsync(t, o.h), "another caller holds the chat's admission")

		o.h.coord.releaseHeld("c1")
		synctest.Wait()
		if !o.keptOpen() {
			t.Fatal("the reservation's release closed a tabbed chat's session outside a door")
		}
		o.reloadsInline(t)
	})
}

func TestCloseOwner_PromptDoorReloadsUnderItsOwnAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := newOwnerHub(t, true)
		o.h.markSessionsForReload("test")

		if rec := postCmd(t, o.h, marotte.ClientCommand{
			Type: marotte.CmdPrompt, ChatID: "c1", Payload: json.RawMessage(`{"text":"hello","message_id":"m-1"}`),
		}); rec.Code != 200 {
			t.Fatalf("prompt = %d %s", rec.Code, rec.Body.String())
		}
		synctest.Wait()

		if !sessionClosed(o.br) {
			t.Error("the prompt's own door kept the marked session")
		}
		if slices.Contains(o.br.callLog(), marotte.MethodPrompt) {
			t.Error("the prompt reached the marked session")
		}
		cur := o.h.bridge.mgr.get("c1")
		if cur == nil || cur == o.sb || !slices.Contains(fakeOf(cur).callLog(), marotte.MethodPrompt) {
			t.Error("the prompt did not reach the session its door loaded again")
		}
	})
}

func TestCloseOwner_ANonPromptDoorReloadsATabbedChatInline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := newOwnerHub(t, true)
		o.h.markSessionsForReload("test")

		next := o.reloadsInline(t)

		if got := fakeOf(next).lastSessionOpts().Presets; got == nil {
			t.Error("the reloaded session's door carried no presets")
		}
	})
}

func TestCloseOwner_QueuedReloadIgnoresTheOpenersExemption(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := newOwnerHub(t, false)
		o.h.markSessionsForReload("test")
		res, ok := o.h.coord.turns.tryReserve("c1", marotte.TurnSourcePrompt, o.h.coord.LifeStamp())
		if !ok {
			t.Fatal("Setup: the opener's reservation was refused on an idle chat")
		}
		release := once(func() { o.h.coord.turns.releaseReservation(res) })
		t.Cleanup(release)
		o.h.coord.turns.noteKASActivity("c1", true)

		got := make(chan openResult, 1)
		go func() {
			sb, err := o.h.coord.openBridgeAt(t.Context(), "c1", "", res.Stamp)
			got <- openResult{sb: sb, err: err}
		}()
		o.openReturnsCurrent(t, got, "KAS reports a run")

		o.h.coord.turns.noteKASActivity("c1", false)
		synctest.Wait()
		if !o.keptOpen() {
			t.Fatal("the pending reload closed the session under the opener's own reservation")
		}
		release()
		synctest.Wait()
		if !o.closed() {
			t.Error("the opener's release left the pending reload of a tabless chat unrun")
		}
	})
}

func TestCloseOwner_ReloadWaitsForAHostedStep(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := newOwnerHub(t, false)
		o.h.markSessionsForReload("test")
		endStep := hostStepOn(t, o.h, "sess-step")

		o.openReturnsCurrent(t, openAsync(t, o.h), "the chat hosts a run step")
		if rec := postCmd(t, o.h, marotte.ClientCommand{
			Type: marotte.CmdPrompt, ChatID: "c1", Payload: json.RawMessage(`{"text":"hello","message_id":"m-1"}`),
		}); rec.Code != 200 {
			t.Fatalf("prompt = %d %s", rec.Code, rec.Body.String())
		}
		synctest.Wait()
		if !o.keptOpen() || !slices.Contains(o.br.callLog(), marotte.MethodPrompt) {
			t.Fatalf("a prompt while the chat hosts a step left its session (kept %v)", o.keptOpen())
		}

		endStep()
		synctest.Wait()
		if !o.closed() {
			t.Error("the step's close left the pending reload of a tabless chat unrun")
		}
		if next, err := o.h.coord.openBridge(t.Context(), "c1", ""); err != nil || next == o.sb {
			t.Errorf("the next open = (same entry %v, %v), want a new session", next == o.sb, err)
		}
	})
}

func TestCloseOwner_ReleaseWaitsForAHostedStep(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := newOwnerHub(t, false)
		endStep := hostStepOn(t, o.h, "sess-step")

		o.h.coord.releaseIfUnheld("c1")
		synctest.Wait()
		if !o.keptOpen() {
			t.Fatal("the release closed a session hosting a run step")
		}
		endStep()
		synctest.Wait()
		if !o.closed() {
			t.Error("the step's close left the pending release unrun")
		}
	})
}

func TestCloseOwner_ReleaseWaitsForADoorInProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := newOwnerHub(t, false)
		br := newFakeBridge()
		sb := testBridge(br, bridgeStarting)
		if !o.h.insertBridgeNow("c2", sb) {
			t.Fatal("Setup: insert c2's entry = false")
		}
		_, done, ok := lifecycleOf(t, o.h, "c2").beginWork(workDoor)
		if !ok {
			t.Fatal("Setup: c2's life refused the door's work")
		}
		done = once(done)
		t.Cleanup(done)

		o.h.coord.releaseIfUnheld("c2")
		synctest.Wait()
		if o.h.bridge.mgr.get("c2") != sb || sessionClosed(br) {
			t.Fatal("the release closed a session whose door is in progress")
		}
		sb.setIdle()
		done()
		synctest.Wait()
		if !sessionClosed(br) || o.h.bridge.mgr.get("c2") != nil {
			t.Error("the door's end left the pending release unrun")
		}
	})
}

func TestCloseOwner_ReleaseWaitsForKASRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := newOwnerHub(t, false)
		o.h.coord.turns.noteKASActivity("c1", true)

		o.h.coord.releaseIfUnheld("c1")
		synctest.Wait()
		if !o.keptOpen() {
			t.Fatal("the release closed the session while KAS reports a run")
		}
		o.h.coord.turns.noteKASActivity("c1", false)
		synctest.Wait()
		if !o.closed() {
			t.Error("KAS's idle report left the pending release unrun")
		}
	})
}

func TestCloseOwner_ReleaseWaitsForASessionCall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := newOwnerHub(t, false)
		gate := make(chan struct{})
		release := once(func() { close(gate) })
		t.Cleanup(release)
		o.br.setBlockOn("test/held", gate)
		called := make(chan error, 1)
		go func() {
			_, err := o.sb.Call(t.Context(), "test/held", nil)
			called <- err
		}()
		synctest.Wait()

		o.h.coord.releaseIfUnheld("c1")
		synctest.Wait()
		if !o.keptOpen() {
			t.Fatal("the release closed the session under a running session call")
		}
		release()
		if err := <-called; err != nil {
			t.Errorf("the held call = %v, want it answered on the open session", err)
		}
		synctest.Wait()
		if !o.closed() {
			t.Error("the call's end left the pending release unrun")
		}
	})
}

func TestCloseOwner_ReleaseWaitsForTheCloseTailOfARealTurnEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := newOwnerHub(t, false)
		tails := blockTails(t, o.h)

		send(t, o.h, newTurnStartMsg(), newTurnEndMsg("end_turn"))
		synctest.Wait()
		if tails.reached.Load() != 1 {
			t.Fatal("Setup: the turn's close tail did not reach its drain")
		}
		if !o.keptOpen() {
			t.Fatal("the turn's end closed the session under its own close tail")
		}
		tails.release()
		synctest.Wait()
		if !o.closed() {
			t.Error("the tail's end left the pending release unrun")
		}
	})
}

func TestCloseOwner_ReleaseWaitsForATailHeldBeforeTheAsk(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := newOwnerHub(t, false)
		_, done, ok := lifecycleOf(t, o.h, "c1").beginWork(workTail)
		if !ok {
			t.Fatal("Setup: the life refused the tail's work")
		}
		done = once(done)
		t.Cleanup(done)

		o.h.coord.releaseIfUnheld("c1")
		synctest.Wait()
		if !o.keptOpen() {
			t.Fatal("the release closed the session under a turn's close tail")
		}
		done()
		synctest.Wait()
		if !o.closed() {
			t.Error("the tail's end left the pending release unrun")
		}
	})
}

func TestCloseOwner_TwoReleaseAsksWhileTheTailIsHeldCloseOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := newOwnerHub(t, false)
		closes := countCloses(o.br)
		tails := blockTails(t, o.h)
		send(t, o.h, newTurnStartMsg(), newTurnEndMsg("end_turn"))
		synctest.Wait()
		if tails.reached.Load() != 1 {
			t.Fatal("Setup: the turn's close tail did not reach its drain")
		}
		if o.h.coord.tryReserveIdleNow("c1", marotte.TurnSourcePrompt) {
			o.h.coord.releaseHeld("c1")
		}
		synctest.Wait()

		tails.release()
		synctest.Wait()

		if got := closes.Load(); got != 1 {
			t.Errorf("session/close calls = %d, want 1 for two release asks", got)
		}
		if !o.closed() {
			t.Error("the two release asks left the session open")
		}
		if next, err := o.h.coord.openBridge(t.Context(), "c1", ""); err != nil || next == o.sb {
			t.Errorf("a later open = (same entry %v, %v), want a new session", next == o.sb, err)
		}
	})
}

func TestCloseOwner_OpenBridgeHoldsTheSessionUntilReleased(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := newOwnerHub(t, false)
		sb, err := o.h.coord.openBridge(t.Context(), "c1", "")
		if err != nil || sb != o.sb {
			t.Fatalf("Setup: OpenBridge(c1) = (same entry %v, %v)", sb == o.sb, err)
		}
		if !o.h.coord.tryReserveIdleNow("c1", marotte.TurnSourcePrompt) {
			t.Fatal("Setup: the admission slot was not free")
		}
		o.h.coord.releaseHeld("c1")
		synctest.Wait()

		if _, err := sb.Call(t.Context(), "test/after-open", nil); err != nil {
			t.Errorf("the opener's call = %v, want it answered", err)
		}
		if !o.keptOpen() {
			t.Error("a release closed the session between its open and the opener's call")
		}
	})
}

func TestCloseOwner_OpenDuringAGrantedCloseGetsAFreshSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := newOwnerHub(t, false)
		gate := make(chan struct{})
		release := once(func() { close(gate) })
		t.Cleanup(release)
		o.br.mu.Lock()
		o.br.stopGate = gate
		o.br.mu.Unlock()
		o.h.coord.releaseIfUnheld("c1")
		synctest.Wait()

		got := openAsync(t, o.h)
		synctest.Wait()
		select {
		case r := <-got:
			t.Fatalf("an open answered while the release's close was held (same entry %v, %v)", r.sb == o.sb, r.err)
		default:
		}
		if _, err := o.sb.Call(t.Context(), "test/during-close", nil); !errors.Is(err, marotte.ErrSessionClosing) {
			t.Errorf("a call on the closing entry = %v, want %v", err, marotte.ErrSessionClosing)
		}
		release()
		r := <-got

		if r.err != nil || r.sb == nil || r.sb == o.sb {
			t.Fatalf("the open = (same entry %v, %v), want a fresh generation", r.sb == o.sb, r.err)
		}
		if !sessionClosed(o.br) {
			t.Error("the released session was not closed")
		}
		if s := fakeOf(r.sb); s == nil || s == o.br || !s.Open() {
			t.Error("the open's generation holds no fresh open session")
		}
	})
}

func TestCloseOwner_DrainedPromptReloadsBeforeTheTailEnds(t *testing.T) {
	for _, tc := range []struct {
		name         string
		pendingFirst bool
	}{
		{name: "the mark alone"},
		{name: "with a reload already pending", pendingFirst: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				o := newOwnerHub(t, true)
				gate := make(chan struct{})
				release := once(func() { close(gate) })
				t.Cleanup(release)
				o.br.setBlockOn(marotte.MethodPrompt, gate)
				o.br.mu.Lock()
				o.br.chunksOnCall = map[string][]string{marotte.MethodPrompt: {"done"}}
				o.br.mu.Unlock()
				// The drained prompt's door always runs before the tail that drained it ends.
				drain := o.h.coord.drainAfterClose
				o.h.coord.drainAfterClose = func(ctx context.Context, chatID marotte.ChatID, cl command.CloseFacts, ends command.EndFacts) {
					drain(ctx, chatID, cl, ends)
					for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
						if cur := o.h.bridge.mgr.get("c1"); cur != nil && slices.Contains(fakeOf(cur).callLog(), marotte.MethodPrompt) {
							return
						}
					}
				}
				if rec := postCmd(t, o.h, marotte.ClientCommand{
					Type: marotte.CmdPrompt, ChatID: "c1", Payload: json.RawMessage(`{"text":"start","message_id":"m-1"}`),
				}); rec.Code != 200 {
					t.Fatalf("prompt = %d %s", rec.Code, rec.Body.String())
				}
				synctest.Wait()
				o.h.markSessionsForReload("test")
				if rec := postCmd(t, o.h, marotte.ClientCommand{
					Type: marotte.CmdQueuePrompt, ChatID: "c1", Payload: json.RawMessage(`{"text":"then","message_id":"m-q1"}`),
				}); rec.Code != 200 {
					t.Fatalf("queue_prompt = %d %s", rec.Code, rec.Body.String())
				}
				if tc.pendingFirst {
					o.openReturnsCurrent(t, openAsync(t, o.h), "the chat's prompt runs")
				}

				release()
				synctest.Wait()

				if !sessionClosed(o.br) {
					t.Error("the drained prompt ran without its door closing the marked session")
				}
				if n := countCalls(o.br, marotte.MethodPrompt); n != 1 {
					t.Errorf("session/prompt calls on the marked session = %d, want only the first prompt's", n)
				}
				cur := o.h.bridge.mgr.get("c1")
				if cur == nil || cur == o.sb || !slices.Contains(fakeOf(cur).callLog(), marotte.MethodPrompt) {
					t.Error("the drained prompt did not reach the session its door loaded again")
				}
				if rows := queuedRows(t, o.cs, "c1"); len(rows) != 0 {
					t.Errorf("queued rows = %v, want the drained one gone", rows)
				}
			})
		})
	}
}

func TestCloseOwner_PendingReloadClosesAndTheNextOpenLoads(t *testing.T) {
	// pendBehindKAS makes the reload pending behind a reported KAS run, then ends that run.
	pendBehindKAS := func(t *testing.T, o *ownerHub) {
		t.Helper()
		o.h.markSessionsForReload("test")
		o.h.coord.turns.noteKASActivity("c1", true)
		o.openReturnsCurrent(t, openAsync(t, o.h), "KAS reports a run")
		o.h.coord.turns.noteKASActivity("c1", false)
		synctest.Wait()
	}
	// residentForRun is the run's residency as the next open, which must close the first session and load it again.
	residentForRun := func(t *testing.T, o *ownerHub) {
		t.Helper()
		if !o.keptOpen() {
			t.Fatal("the pending reload closed the session of a chat with a live run it launched")
		}
		if _, err := o.h.runs.ensureResident(t.Context(), "wf_1"); err != nil {
			t.Fatalf("ensureResident(wf_1): %v", err)
		}
		next := o.h.bridge.mgr.get("c1")
		if next == nil || next == o.sb || !sessionClosed(o.br) {
			t.Fatal("the run's residency kept the marked session instead of closing and loading it again")
		}
		if opts := fakeOf(next).lastSessionOpts(); opts == nil || opts.SessionID != string(o.sid) {
			t.Errorf("the run's residency door = %+v, want a load of the chat's session %q", opts, o.sid)
		}
	}

	t.Run("a tabbed chat", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			o := newOwnerHub(t, true)
			pendBehindKAS(t, o)
			if !o.keptOpen() {
				t.Fatal("the pending reload closed a tabbed chat's session outside a door")
			}
			o.reloadsInline(t)
		})
	})
	t.Run("a tabless chat", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			o := newOwnerHub(t, false)
			pendBehindKAS(t, o)
			if !o.closed() {
				t.Fatal("KAS's idle report left the pending reload of a tabless chat unrun, or it loaded a session")
			}
			next, err := o.h.coord.openBridge(t.Context(), "c1", "")
			if err != nil || next == o.sb {
				t.Fatalf("the next open = (same entry %v, %v), want a new session", next == o.sb, err)
			}
			if opts := fakeOf(next).lastSessionOpts(); opts == nil || opts.SessionID != string(o.sid) {
				t.Errorf("the next generation's door = %+v, want a load of the chat's session %q", opts, o.sid)
			}
		})
	})
	t.Run("the chat hosts a run", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			o := newOwnerHub(t, false)
			o.launchRun(t, "wf_1")
			o.h.markSessionsForReload("test")
			endStep := hostStepOn(t, o.h, "sess-step")
			o.openReturnsCurrent(t, openAsync(t, o.h), "the chat hosts a run step")
			endStep()
			synctest.Wait()
			residentForRun(t, o)
		})
	})
	t.Run("the chat's run is paused", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			o := newOwnerHub(t, false)
			o.launchRun(t, "wf_1")
			pendBehindKAS(t, o)
			residentForRun(t, o)
		})
	})
}

func TestCloseOwner_AStepMessageReachesItsParentAfterAPendingReload(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		st, err := tabs.NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("Setup: tab store: %v", err)
		}
		h, cs, _ := newTestHub(WithTabs(st), WithConfigDir(t.TempDir()))
		t.Cleanup(func() { shutdownHub(t, h) })
		if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true }); err != nil {
			t.Fatalf("Setup: seed c1: %v", err)
		}
		chat := newFakeBridge()
		chat.sessionID = "sess_parent"
		sb := testBridge(chat, bridgeIdle)
		h.insertBridgeNow("c1", sb)
		leaseRun(t, h, runlease.Lease{WorkflowID: recordedRun, ChatID: "c1", ParentSessionID: "sess_parent", Origin: runlease.OriginAgent})
		h.markSessionsForReload("test")
		if _, _, err := h.runs.log.open(t.Context(), &translate.RunStep{RunID: recordedRun, NodePath: "seq/step", SessionID: recordedSession}, "c1"); err != nil {
			t.Fatalf("Setup: open the step turn: %v", err)
		}
		got := openAsync(t, h)
		synctest.Wait()
		if r := <-got; r.err != nil || r.sb != sb {
			t.Fatalf("Setup: an open while the chat hosts a step = (same entry %v, %v)", r.sb == sb, r.err)
		}
		if _, _, err := h.runs.log.closeNode(t.Context(), recordedRun, "seq/step", string(marotte.RunStatusCompleted), "", nil); err != nil {
			t.Fatalf("close the step turn: %v", err)
		}
		synctest.Wait()

		kasDelivers(t, h, runNotif(methodKiroSessionNotify, map[string]any{
			"sessionId": "sess_parent", "callerSessionId": recordedSession, "message": "Step done.",
			"severity": "info", "sender": "step", "workflowId": recordedRun,
		}))
		kasDelivers(t, h, withSessionID(t, newTurnStartMsg(), "sess_parent"))
		kasDelivers(t, h, withSessionID(t, newChunkMsg("Picking it up."), "sess_parent"))
		kasDelivers(t, h, permissionAsk("sess_parent", 92))
		synctest.Wait()

		if h.bridge.mgr.get("c1") != sb || !chat.Open() {
			t.Fatal("the parent chat's session was closed before the turn the step's message woke")
		}
		if _, open := ownID(h, "c1"); !open {
			t.Error("the woken turn is not the chat's own turn")
		}
		if _, answered := fakeSourceOf(t, h).lane.responseTo(92); answered {
			t.Error("the woken turn's ask was answered on the engine lane")
		}
		answerTheChatsAsk(t, h)
		if _, answered := chat.responseTo(92); !answered {
			t.Error("the answer did not reach the chat's own session")
		}
		kasDelivers(t, h, withSessionID(t, newTurnEndMsg("end_turn"), "sess_parent"))
		synctest.Wait()
		if !slices.Contains(textsOf(t, logOf(t, cs, "c1")), "Picking it up.") {
			t.Error("the woken turn's text is not in the chat's log")
		}
	})
}

// The wake tests: a run c1 launched ends while c1 has no tab, and KAS wakes c1's session after the frame.

func (o *ownerHub) wakeTurn(t *testing.T, text string, askID int64) {
	t.Helper()
	kasDelivers(t, o.h, o.frame(t, newActivityMsg(true)))
	kasDelivers(t, o.h, o.frame(t, newTurnStartMsg()))
	kasDelivers(t, o.h, o.frame(t, newChunkMsg(text)))
	if askID != 0 {
		kasDelivers(t, o.h, permissionAsk(string(o.sid), askID))
	}
	synctest.Wait()
}

func (o *ownerHub) wakeEnds(t *testing.T) {
	t.Helper()
	kasDelivers(t, o.h, o.frame(t, newTurnEndMsg("end_turn")))
	kasDelivers(t, o.h, o.frame(t, newActivityMsg(false)))
	synctest.Wait()
}

func (o *ownerHub) wakeLanded(t *testing.T, text string, askID int64) {
	t.Helper()
	if !o.keptOpen() {
		t.Fatal("the chat's session closed before the wake its run's end owed it")
	}
	if !o.logHolds(t, text) {
		t.Error("the woken turn's text is not in the chat's log")
	}
	if askID == 0 {
		return
	}
	if _, answered := fakeSourceOf(t, o.h).lane.responseTo(askID); answered {
		t.Error("the woken turn's ask was answered on the engine lane")
	}
	answerTheChatsAsk(t, o.h)
	if _, answered := o.br.responseTo(askID); !answered {
		t.Error("the answer did not reach the chat's own session")
	}
}

// answerTheChatsAsk is the reader answering c1's one pending ask on its card.
func answerTheChatsAsk(t *testing.T, h *Runtime) {
	t.Helper()
	ids := h.bus.pendingPerms.idsForChat("c1")
	if len(ids) != 1 {
		t.Fatalf("pending asks for c1 = %v, want the woken turn's one", ids)
	}
	askID, err := strconv.ParseInt(ids[0], 10, 64)
	if err != nil {
		t.Fatalf("the card's ask id %q: %v", ids[0], err)
	}
	reply, ok := h.bus.TakePendingPerm("c1", askID, marotte.DecisionKindPermission, marotte.SettledByUser)
	if !ok {
		t.Fatal("the woken turn's ask could not be taken")
	}
	if _, err := reply.Respond(t.Context(), map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": "allow"}}); err != nil {
		t.Errorf("answer the ask: %v", err)
	}
}

func TestCloseOwner_ARunEndKeepsTheChatForAnImmediateWake(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := newOwnerHub(t, false)
		o.launchRun(t, recordedRun)
		closes := countCloses(o.br)

		deliverRunComplete(t, o.h, "c1", recordedRun, "completed")

		if !o.keptOpen() {
			t.Error("the run's end closed its launching chat's session before KAS's wake")
		}
		o.wakeTurn(t, "Run finished.", 93)
		o.wakeLanded(t, "Run finished.", 93)
		o.wakeEnds(t)
		if !o.closed() || closes.Load() != 1 {
			t.Errorf("after the wake ended: closed %v with %d session/close calls, want closed once", o.closed(), closes.Load())
		}
	})
}

func TestCloseOwner_ARunEndKeepsTheChatForALateWake(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := newOwnerHub(t, false)
		o.launchRun(t, recordedRun)

		deliverRunComplete(t, o.h, "c1", recordedRun, "completed")
		time.Sleep(wakeHoldBound - time.Second)
		synctest.Wait()

		if !o.keptOpen() {
			t.Error("the chat's session closed while its run's end still owed it a wake")
		}
		o.wakeTurn(t, "Run finished late.", 0)
		o.wakeLanded(t, "Run finished late.", 0)
		o.wakeEnds(t)
		if !o.closed() {
			t.Error("the wake's end left the pending release unrun")
		}
	})
}

func TestCloseOwner_ARunEndOwingNoWakeReleasesAtOnce(t *testing.T) {
	for _, tc := range []struct {
		end  func(t *testing.T, o *ownerHub)
		name string
	}{
		{name: "a paused run's frame, then its lease released", end: func(t *testing.T, o *ownerHub) {
			deliverRunComplete(t, o.h, "c1", recordedRun, string(marotte.RunStatusPaused))
			o.h.runs.releaseLease(t.Context(), recordedRun)
		}},
		{name: "a lease released with no frame", end: func(t *testing.T, o *ownerHub) {
			o.h.runs.releaseLease(t.Context(), recordedRun)
		}},
		{name: "a terminal frame on the run's own lane", end: func(t *testing.T, o *ownerHub) {
			deliverRunComplete(t, o.h, runChatID(recordedRun), recordedRun, "completed")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				o := newOwnerHub(t, false)
				o.launchRun(t, recordedRun)

				tc.end(t, o)
				synctest.Wait()

				if !o.closed() {
					t.Error("a run end that owes the chat no wake kept its tabless session")
				}
			})
		})
	}
}

func TestCloseOwner_TheWakeBoundReleasesTheChat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := newOwnerHub(t, false)
		o.launchRun(t, recordedRun)

		deliverRunComplete(t, o.h, "c1", recordedRun, "completed")
		time.Sleep(wakeHoldBound - time.Millisecond)
		synctest.Wait()
		if !o.keptOpen() {
			t.Fatal("the chat's session closed before the wake's bound")
		}
		time.Sleep(2 * time.Millisecond)
		synctest.Wait()

		if !o.closed() {
			t.Error("the wake's bound passed and the pending release stayed unrun")
		}
	})
}

func TestCloseOwner_ABusyParentsWakeHoldsUntilTheNextTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := newOwnerHub(t, false)
		o.launchRun(t, recordedRun)
		kasDelivers(t, o.h, o.frame(t, newActivityMsg(true)))
		kasDelivers(t, o.h, o.frame(t, newTurnStartMsg()))

		deliverRunComplete(t, o.h, "c1", recordedRun, "completed")
		o.wakeEnds(t)
		if !o.keptOpen() {
			t.Fatal("the busy parent's session closed at its turn's end while its run's end still owed it a wake")
		}
		o.wakeTurn(t, "Picking up the run.", 0)
		o.wakeLanded(t, "Picking up the run.", 0)
		o.wakeEnds(t)

		if !o.closed() {
			t.Error("the late turn's end left the pending release unrun")
		}
	})
}

// holdRow acquires one claimant of c1's session the way its real path does, answering the release that ends it.
type holdRow struct {
	acquire func(t *testing.T, o *ownerHub) (release func())
	name    string
	kind    holdKind
}

var closeHoldRows = []holdRow{
	{name: "an admission held by another caller", kind: holdAdmission, acquire: func(t *testing.T, o *ownerHub) func() {
		if !o.h.coord.tryReserveIdleNow("c1", marotte.TurnSourcePrompt) {
			t.Fatal("Setup: the admission slot was not free")
		}
		return once(func() { o.h.coord.releaseHeld("c1") })
	}},
	{name: "a wire turn", kind: holdTurn, acquire: func(t *testing.T, o *ownerHub) func() {
		send(t, o.h, newTurnStartMsg())
		return once(func() { send(t, o.h, newTurnEndMsg("end_turn")) })
	}},
	{name: "a sent prompt turn", kind: holdTurn, acquire: func(t *testing.T, o *ownerHub) func() {
		id, _ := o.h.stagePromptTurn(t, "c1")
		return once(func() { endTurn(t, o.h, "c1", id) })
	}},
	{name: "a run KAS reports", kind: holdKAS, acquire: func(_ *testing.T, o *ownerHub) func() {
		o.h.coord.turns.noteKASActivity("c1", true)
		return once(func() { o.h.coord.turns.noteKASActivity("c1", false) })
	}},
	{name: "a closer's KAS hold", kind: holdKAS, acquire: func(t *testing.T, o *ownerHub) func() {
		lc := lifecycleOf(t, o.h, "c1")
		lc.kasStep(kasGraceClaim, 1)
		return once(func() { lc.kasStep(kasReleaseOwn, 1) })
	}},
	{name: "a running session call", kind: holdLease, acquire: func(t *testing.T, o *ownerHub) func() {
		gate := make(chan struct{})
		o.br.setBlockOn("test/held", gate)
		called := make(chan struct{})
		go func() {
			defer close(called)
			_, _ = o.sb.Call(context.Background(), "test/held", nil)
		}()
		synctest.Wait()
		return once(func() { close(gate); <-called })
	}},
	{name: "a turn's close tail", kind: holdTail, acquire: func(t *testing.T, o *ownerHub) func() {
		tails := blockTails(t, o.h)
		send(t, o.h, newTurnStartMsg(), newTurnEndMsg("end_turn"))
		synctest.Wait()
		if tails.reached.Load() != 1 {
			t.Fatal("Setup: the turn's close tail did not reach its drain")
		}
		return tails.release
	}},
	{name: "a door in progress", kind: holdDoor, acquire: func(t *testing.T, o *ownerHub) func() {
		_, done, ok := lifecycleOf(t, o.h, "c1").beginWork(workDoor)
		if !ok {
			t.Fatal("Setup: the life refused the door's work")
		}
		o.sb.mu.Lock()
		o.sb.state = bridgeStarting
		o.sb.mu.Unlock()
		return once(func() { o.sb.setIdle(); done() })
	}},
	{name: "a run step the session hosts", kind: holdStep, acquire: func(t *testing.T, o *ownerHub) func() {
		return hostStepOn(t, o.h, "sess-step")
	}},
	{name: "the wake a run's end owes the chat", kind: holdWake, acquire: func(t *testing.T, o *ownerHub) func() {
		o.launchRun(t, recordedRun)
		deliverRunComplete(t, o.h, "c1", recordedRun, "completed")
		return once(func() {
			kasDelivers(t, o.h, o.frame(t, newActivityMsg(true)))
			kasDelivers(t, o.h, o.frame(t, newActivityMsg(false)))
		})
	}},
}

func TestCloseOwner_EachHoldRefusesThenReleases(t *testing.T) {
	for _, row := range closeHoldRows {
		t.Run(row.name, func(t *testing.T) {
			t.Run("release", func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					o := newOwnerHub(t, false)
					release := row.acquire(t, o)
					t.Cleanup(release)

					o.h.coord.releaseIfUnheld("c1")
					synctest.Wait()
					if !o.keptOpen() {
						t.Errorf("the release closed the session while %s held it", row.name)
					}
					release()
					synctest.Wait()
					if !o.closed() {
						t.Error("the hold's end left the pending release unrun")
					}
				})
			})
			t.Run("reload", func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					o := newOwnerHub(t, true)
					o.h.markSessionsForReload("test")
					release := row.acquire(t, o)
					t.Cleanup(release)

					o.openReturnsCurrent(t, openAsync(t, o.h), row.name+" holds the session")
					release()
					synctest.Wait()
					if !o.keptOpen() {
						t.Fatal("the hold's end closed a tabbed chat's session outside a door")
					}
					o.reloadsInline(t)
				})
			})
		})
	}
	for _, tc := range []struct {
		hold func(t *testing.T, o *ownerHub) (drop func())
		name string
	}{
		{name: "an open tab", hold: func(t *testing.T, o *ownerHub) func() {
			subject, _, _, err := o.st.Open(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c1"})
			if err != nil {
				t.Fatalf("Setup: open c1's tab: %v", err)
			}
			return func() {
				if _, _, _, err := o.st.Close(t.Context(), subject.ID); err != nil {
					t.Fatalf("close c1's tab: %v", err)
				}
			}
		}},
		{name: "a live run it launched", hold: func(t *testing.T, o *ownerHub) func() {
			o.launchRun(t, "wf_1")
			return func() {
				if err := o.h.runs.leaseStore().Release(t.Context(), "wf_1"); err != nil {
					t.Fatalf("drop the lease: %v", err)
				}
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				o := newOwnerHub(t, false)
				drop := tc.hold(t, o)

				o.h.coord.releaseIfUnheld("c1")
				synctest.Wait()
				if !o.keptOpen() {
					t.Fatalf("the release closed the session while %s kept it", tc.name)
				}
				drop()
				synctest.Wait()
				if !o.keptOpen() {
					t.Fatal("the precondition's end alone closed the session")
				}
				o.h.coord.releaseIfUnheld("c1")
				synctest.Wait()
				if !o.closed() {
					t.Error("the next release ask left the session open")
				}
			})
		})
	}
}

// ownerLife is c1's lifecycle on a bare registry, with one entry on it: the owner's own answers, with no runtime path
// asking or closing beside the test.
type ownerLife struct {
	r  *turnRegistry
	lc *chatLifecycle
	sb *sharedBridge
}

func newOwnerLife(t *testing.T) *ownerLife {
	t.Helper()
	r := newTurnRegistry(nopTurnRecords{})
	lc := r.testLifecycle("c1")
	t.Cleanup(func() { r.forget("c1") })
	return &ownerLife{r: r, lc: lc, sb: newSharedBridge(lc)}
}

// reserve takes c1's admission slot, as a prompt's admission does.
func (o *ownerLife) reserve(t *testing.T) command.Reservation {
	t.Helper()
	o.lc.mu.Lock()
	a := o.r.reserveLocked(o.lc, marotte.TurnSourcePrompt, o.lc.stamp, "")
	o.lc.mu.Unlock()
	if a.res.Token == 0 {
		t.Fatal("Setup: the admission slot was not free")
	}
	return a.res
}

func (o *ownerLife) holdOpen(opener uint64) func() {
	o.lc.mu.Lock()
	defer o.lc.mu.Unlock()
	return o.lc.holdOpenLocked(opener)
}

// closeState is a snapshot of the owner's close state.
type closeState struct {
	ask     *closeAsk
	closing *closeGrant
	tails   int
	opens   int
	closer  bool
}

func (o *ownerLife) state() closeState {
	o.lc.mu.Lock()
	defer o.lc.mu.Unlock()
	var ask *closeAsk
	if o.lc.ask != nil {
		cp := *o.lc.ask
		ask = &cp
	}
	return closeState{ask: ask, closing: o.lc.closing, tails: len(o.lc.tails), opens: len(o.lc.opens), closer: o.lc.closer}
}

func (o *ownerLife) blocker(opener uint64) holdKind {
	o.lc.mu.Lock()
	defer o.lc.mu.Unlock()
	return o.lc.blockerLocked(opener)
}

// ownerHolds takes one hold of each kind through the owner's own acquire, answering its release.
var ownerHolds = map[holdKind]struct {
	take func(t *testing.T, o *ownerLife) (release func())
	name string
}{
	holdAdmission: {name: "admission", take: func(t *testing.T, o *ownerLife) func() {
		res := o.reserve(t)
		return func() { o.r.releaseReservation(res) }
	}},
	holdTurn: {name: "turn", take: func(t *testing.T, o *ownerLife) func() {
		o.lc.mu.Lock()
		tr := o.lc.openLocked("c1", &marotte.Entry{ID: "w-owner"}, marotte.TurnSourceWireTurnStart, "", turnlog.Open("w-owner", nil))
		o.lc.mu.Unlock()
		return func() { o.r.finish(tr, marotte.TurnResult{}) }
	}},
	holdKAS: {name: "KAS", take: func(_ *testing.T, o *ownerLife) func() {
		o.r.noteKASActivity("c1", true)
		return func() { o.r.noteKASActivity("c1", false) }
	}},
	holdLease: {name: "lease", take: func(t *testing.T, o *ownerLife) func() {
		_, done, err := o.lc.beginLease(o.sb, func(error) {})
		if err != nil {
			t.Fatalf("Setup: lease refused: %v", err)
		}
		return done
	}},
	holdTail: {name: "tail", take: func(t *testing.T, o *ownerLife) func() {
		_, done, _, ok := o.r.beginTail(o.lc)
		if !ok {
			t.Fatal("Setup: the life refused the tail")
		}
		return done
	}},
	holdDoor: {name: "door", take: func(t *testing.T, o *ownerLife) func() {
		_, done, ok := o.lc.beginWork(workDoor)
		if !ok {
			t.Fatal("Setup: the life refused the door")
		}
		return done
	}},
	holdOpen: {name: "open", take: func(_ *testing.T, o *ownerLife) func() { return o.holdOpen(0) }},
	holdStep: {name: "step", take: func(_ *testing.T, o *ownerLife) func() {
		o.r.holdStep("c1", "step-owner")
		return func() { o.r.endStep("c1", "step-owner") }
	}},
	holdWake: {name: "wake", take: func(_ *testing.T, o *ownerLife) func() {
		o.r.holdWake("c1", "wf-owner")
		return func() { o.r.noteKASActivity("c1", true); o.r.noteKASActivity("c1", false) }
	}},
}

func TestCloseOwner_EveryHoldKindHasARow(t *testing.T) {
	rows := make(map[holdKind]bool)
	for _, row := range closeHoldRows {
		rows[row.kind] = true
	}
	for k := range holdKinds {
		h, ok := ownerHolds[k]
		if !ok {
			t.Errorf("hold kind %d has no owner-level acquire", k)
			continue
		}
		if !rows[k] {
			t.Errorf("hold kind %s has no row in TestCloseOwner_EachHoldRefusesThenReleases", h.name)
		}
		t.Run(h.name, func(t *testing.T) {
			o := newOwnerLife(t)
			release := h.take(t, o)
			if got := o.blocker(0); got != k {
				t.Errorf("blockerLocked with only the %s hold held = %d, want %d", h.name, got, k)
			}
			release()
			if got := o.blocker(0); got != holdKinds {
				t.Errorf("blockerLocked after the %s hold's release = %d, want none", h.name, got)
			}
		})
	}
}

func TestCloseOwner_TheOpenersOwnAdmissionAndUnsentTurnDoNotRefuse(t *testing.T) {
	o := newOwnerLife(t)
	res := o.reserve(t)
	o.lc.mu.Lock()
	o.lc.openLocked("c1", &marotte.Entry{ID: "p-owner"}, marotte.TurnSourcePrompt, "", turnlog.Open("p-owner", nil))
	o.lc.mu.Unlock()
	if got := o.blocker(res.Token); got != holdKinds {
		t.Errorf("the opener's own door = %d, want no hold refusing it", got)
	}
	if got := o.blocker(0); got != holdAdmission {
		t.Errorf("another caller = %d, want the reservation refusing it", got)
	}
	o.lc.mu.Lock()
	o.lc.pending.sent = true
	o.lc.mu.Unlock()
	if got := o.blocker(res.Token); got != holdTurn {
		t.Errorf("the opener's door once its prompt was sent = %d, want the turn refusing it", got)
	}
}

func TestCloseOwner_TwoAsksWhileHeldMakeOneClose(t *testing.T) {
	for _, k := range []holdKind{holdKAS, holdTail, holdStep} {
		h := ownerHolds[k]
		t.Run(h.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				o := newOwnerLife(t)
				release := h.take(t, o)

				first := o.lc.askClose(o.sb, closeRelease, 0, false)
				second := o.lc.askClose(o.sb, closeReload, 0, false)
				if first.verdict != askPending || !first.startCloser {
					t.Errorf("the first ask = %+v, want pending, starting the closer", first)
				}
				if second.verdict != askPending || second.startCloser {
					t.Errorf("the second ask = %+v, want pending, joining the first", second)
				}
				st := o.state()
				if st.closing != nil || st.ask == nil || st.ask.sb != o.sb || st.ask.why != closeRelease|closeReload {
					t.Fatalf("while held: ask %+v, closing %v; want one pending close carrying both purposes", st.ask, st.closing)
				}

				release()
				st = o.state()
				if st.ask != nil || st.closing == nil || st.closing.sb != o.sb || st.closing.why != closeRelease|closeReload {
					t.Fatalf("after the release: ask %+v, closing %+v; want the one close granted in its section", st.ask, st.closing)
				}
				g, why, ok := o.lc.nextClose()
				if !ok || g != st.closing || why != closeRelease|closeReload {
					t.Fatalf("the closer's next close = (%v, %b, %t), want the grant with both purposes", g == st.closing, why, ok)
				}
				if o.lc.endClose(g, true) {
					t.Error("the running closer's own endClose asked for a second closer")
				}
				if _, _, ok := o.lc.nextClose(); ok {
					t.Error("the closer found a second close to run")
				}
				if o.state().closer {
					t.Error("the closer returned with lc.closer still set")
				}
			})
		})
	}
}

func TestCloseOwner_EndCloseTwiceIsANoOp(t *testing.T) {
	o := newOwnerLife(t)
	o.lc.askClose(o.sb, closeRelease, 0, false)
	g := o.state().closing
	if g == nil {
		t.Fatal("Setup: a release with nothing held was not granted")
	}
	o.lc.endClose(g, true)
	o.lc.endClose(g, true)
	if o.state().closing != nil {
		t.Error("the grant stands after its endClose")
	}
	select {
	case <-g.done:
	default:
		t.Error("endClose left the grant's done open")
	}

	o.lc.askClose(o.sb, closeRelease, 0, false)
	next := o.state().closing
	if next == nil || next == g {
		t.Fatal("Setup: the second release was not granted")
	}
	o.lc.endClose(g, true)
	if o.state().closing != next {
		t.Error("an endClose for a grant that is no longer the chat's ended the current one")
	}
	select {
	case <-next.done:
		t.Error("an endClose for an old grant closed the current grant's done")
	default:
	}
}

func TestCloseOwner_AskOnANewEntryReplacesTheStaleAsk(t *testing.T) {
	o := newOwnerLife(t)
	o.r.noteKASActivity("c1", true)
	if a := o.lc.askClose(o.sb, closeRelease, 0, false); !a.startCloser {
		t.Fatal("Setup: the first release did not start the closer")
	}
	sb2 := newSharedBridge(o.lc)
	if a := o.lc.askClose(sb2, closeRelease, 0, false); a.verdict != askPending || a.startCloser {
		t.Errorf("the ask on the new entry = %+v, want pending on the one closer", a)
	}
	if st := o.state(); st.ask == nil || st.ask.sb != sb2 {
		t.Fatalf("pending ask = %+v, want it on the new entry", st.ask)
	}
	o.r.noteKASActivity("c1", false)
	g, _, ok := o.lc.nextClose()
	if !ok || g.sb != sb2 {
		t.Fatalf("the closer's next close = (%v, %t), want the new entry's", g, ok)
	}
	o.lc.endClose(g, true)
	if g, _, ok := o.lc.nextClose(); ok {
		t.Errorf("the closer ran a second close, on the old entry %t", g.sb == o.sb)
	}
}

func TestCloseOwner_DrainPickHandsTheTailToTheReservation(t *testing.T) {
	t.Run("the slot is free", func(t *testing.T) {
		o := newOwnerLife(t)
		_, done, tail, _ := o.r.beginTail(o.lc)
		defer done()
		o.lc.askClose(o.sb, closeRelease, 0, false)

		res, ok := o.r.drainPick("c1", marotte.TurnSourcePrompt, command.TurnFence{}, tail)
		if !ok {
			t.Fatal("the drain's pick took no reservation")
		}
		if st := o.state(); st.tails != 0 || st.closing != nil {
			t.Errorf("after the pick: %d tails held, grant %v; want the tail ended and the close still pending", st.tails, st.closing)
		}
		o.r.releaseReservation(res)
		if o.state().closing == nil {
			t.Error("the drained reservation's release left the pending close ungranted")
		}
	})
	t.Run("the predecessor was a prompt", func(t *testing.T) {
		o := newOwnerLife(t)
		_, done, tail, _ := o.r.beginTail(o.lc)
		defer done()
		prev := o.reserve(t)
		o.holdOpen(prev.Token)
		o.r.releaseReservation(prev)
		o.lc.askClose(o.sb, closeRelease, 0, false)

		res, ok := o.r.drainPick("c1", marotte.TurnSourcePrompt, command.TurnFence{}, tail)
		if !ok {
			t.Fatal("the drain's pick took no reservation")
		}
		if st := o.state(); st.opens != 0 || st.tails != 0 || st.closing != nil {
			t.Errorf("after the pick: %d opens, %d tails, grant %v; want the predecessor and the tail holding nothing, the close pending",
				st.opens, st.tails, st.closing)
		}
		o.r.releaseReservation(res)
		if o.state().closing == nil {
			t.Error("the drained reservation's release left the pending close ungranted")
		}
	})
	t.Run("the pick refuses", func(t *testing.T) {
		o := newOwnerLife(t)
		_, done, tail, _ := o.r.beginTail(o.lc)
		defer done()
		o.lc.askClose(o.sb, closeRelease, 0, false)
		o.lc.mu.Lock()
		failed := command.TurnFence{Epoch: o.lc.epoch + 1}
		o.lc.mu.Unlock()

		if _, ok := o.r.drainPick("c1", marotte.TurnSourcePrompt, failed, tail); ok {
			t.Fatal("a pick on a failed fence reserved")
		}
		if st := o.state(); st.tails != 0 || st.closing == nil {
			t.Errorf("after the refused pick: %d tails, grant %v; want the tail ended and the close granted", st.tails, st.closing)
		}
	})
}

func TestCloseOwner_AReservationReleaseEndsItsDoorsOpenHold(t *testing.T) {
	o := newOwnerLife(t)
	res := o.reserve(t)
	promptDoor := o.holdOpen(res.Token)
	otherDoor := o.holdOpen(0)
	o.lc.askClose(o.sb, closeRelease, 0, false)

	o.r.releaseReservation(res)
	if st := o.state(); st.opens != 1 || st.closing != nil {
		t.Fatalf("after the reservation's release: %d opens, grant %v; want only the other door's hold left, no grant", st.opens, st.closing)
	}
	promptDoor()
	if st := o.state(); st.opens != 1 || st.closing != nil {
		t.Errorf("the prompt door's own release after its reservation's = %d opens, grant %v; want a no-op", st.opens, st.closing)
	}
	otherDoor()
	if st := o.state(); st.opens != 0 || st.closing == nil {
		t.Errorf("the last open hold's release = %d opens, grant %v; want the pending close granted", st.opens, st.closing)
	}
}

// ownerCloser runs c1's closer as the coordinator will: each granted close is recorded and ended as closed. running
// counts the closers alive, most is the highest it reached, and extra counts endClose answers asking for another.
type ownerCloser struct {
	ran                  []*sharedBridge
	running, most, extra int
	mu                   sync.Mutex
}

func (c *ownerCloser) start(o *ownerLife) {
	c.mu.Lock()
	c.running++
	c.most = max(c.most, c.running)
	c.mu.Unlock()
	go func() {
		defer func() { c.mu.Lock(); c.running--; c.mu.Unlock() }()
		for {
			g, _, ok := o.lc.nextClose()
			if !ok {
				return
			}
			again := o.lc.endClose(g, true)
			c.mu.Lock()
			c.ran = append(c.ran, g.sb)
			if again {
				c.extra++
			}
			c.mu.Unlock()
		}
	}()
}

func (c *ownerCloser) snapshot() (ran []*sharedBridge, running, most, extra int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.ran), c.running, c.most, c.extra
}

func TestCloseOwner_AnInlineGrantStartsTheCloserItLeavesWork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := newOwnerLife(t)
		sb2 := newSharedBridge(o.lc)
		res := o.reserve(t)
		var c ownerCloser
		if a := o.lc.askClose(o.sb, closeRelease, 0, false); a.startCloser {
			c.start(o)
		}
		synctest.Wait()
		if _, running, _, _ := c.snapshot(); running != 1 {
			t.Fatal("Setup: no closer waits on the pending close")
		}

		door := o.lc.askClose(o.sb, closeReload, res.Token, true)
		if door.verdict != askGranted || door.grant.why != closeReload|closeRelease {
			t.Fatalf("the opener's door = %+v, want its inline grant absorbing the pending release", door)
		}
		synctest.Wait()
		if _, running, _, _ := c.snapshot(); running != 0 || o.state().closer {
			t.Error("the running closer did not return at the door's inline grant")
		}
		if a := o.lc.askClose(sb2, closeRelease, 0, false); a.verdict != askPending || a.startCloser {
			t.Errorf("an ask during the inline grant = %+v, want pending with no closer started", a)
		}
		if !o.lc.endClose(door.grant, true) {
			t.Fatal("the inline grant's endClose left the pending close with no closer")
		}
		if !o.state().closer {
			t.Error("endClose answered a closer to start without marking one running")
		}
		c.start(o)
		o.r.releaseReservation(res)
		synctest.Wait()
		ran, _, most, extra := c.snapshot()
		if len(ran) != 1 || ran[0] != sb2 {
			t.Errorf("the closer ran %d closes (the new entry's %t), want the new entry's release once", len(ran), len(ran) == 1 && ran[0] == sb2)
		}
		if most > 1 || extra > 0 {
			t.Errorf("%d closers ran at once and %d endClose answers asked for another, want one closer", most, extra)
		}
	})
}

func TestCloseOwner_ALeaseOnTheClosingEntryIsRefused(t *testing.T) {
	o := newOwnerLife(t)
	o.lc.askClose(o.sb, closeRelease, 0, false)
	g := o.state().closing
	if g == nil {
		t.Fatal("Setup: the release was not granted")
	}
	if _, _, err := o.lc.beginLease(o.sb, func(error) {}); !errors.Is(err, marotte.ErrSessionClosing) {
		t.Errorf("a lease on the closing entry = %v, want marotte.ErrSessionClosing", err)
	}
	other := newSharedBridge(o.lc)
	_, done, err := o.lc.beginLease(other, func(error) {})
	if err != nil {
		t.Errorf("a lease on another entry = %v, want it admitted", err)
	}
	done()
	if !o.lc.closingSession(o.sb) || o.lc.closingSession(other) {
		t.Error("closingSession does not name exactly the granted entry")
	}
}

func TestCloseOwner_AWakeHoldEndsAtItsBoundOrATurn(t *testing.T) {
	t.Run("the bound", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			o := newOwnerLife(t)
			o.r.holdWake("c1", "wf-owner")
			o.lc.askClose(o.sb, closeRelease, 0, false)
			time.Sleep(wakeHoldBound - time.Millisecond)
			if o.state().closing != nil {
				t.Fatal("the release was granted before the wake's bound")
			}
			time.Sleep(time.Millisecond)
			synctest.Wait()
			if o.state().closing == nil {
				t.Error("the wake's bound left the pending release ungranted")
			}
		})
	})
	t.Run("a turn opens", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			o := newOwnerLife(t)
			o.r.holdWake("c1", "wf-owner")
			o.lc.mu.Lock()
			o.lc.openLocked("c1", &marotte.Entry{ID: "w-wake"}, marotte.TurnSourceWireTurnStart, "", turnlog.Open("w-wake", nil))
			wakes := len(o.lc.wakes)
			o.lc.mu.Unlock()
			if wakes != 0 {
				t.Errorf("%d wake holds after a turn opened, want the turn to take over", wakes)
			}
		})
	})
	t.Run("the life ends", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			o := newOwnerLife(t)
			o.r.holdWake("c1", "wf-owner")
			o.r.forget("c1")
			o.lc.mu.Lock()
			wakes := len(o.lc.wakes)
			o.lc.mu.Unlock()
			if wakes != 0 {
				t.Errorf("%d wake holds after the life ended, want none", wakes)
			}
			time.Sleep(wakeHoldBound)
			synctest.Wait()
		})
	})
}

func TestCloseOwner_AWithdrawnCloseAsksItsLatePurposesAgain(t *testing.T) {
	o := newOwnerLife(t)
	o.lc.askClose(o.sb, closeRelease, 0, false)
	g, why, ok := o.lc.nextClose()
	if !ok || why != closeRelease {
		t.Fatalf("Setup: the closer's next close = (%t, %b), want the release", ok, why)
	}
	if a := o.lc.askClose(o.sb, closeReload, 0, false); a.verdict != askClosing {
		t.Fatalf("a reload asked on the frozen grant = %+v, want it told to wait for the close", a)
	}
	if o.lc.endClose(g, false) {
		t.Error("the running closer's withdraw asked for a second closer")
	}
	if st := o.state(); st.closing == nil || st.closing == g || st.closing.why != closeReload {
		t.Errorf("after the withdraw: grant %+v, want a new grant carrying only the late reload", st.closing)
	}
}
