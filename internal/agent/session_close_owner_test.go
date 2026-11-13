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
	sb, err := h.coord.openReleased(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("Setup: OpenBridge(c1): %v", err)
	}
	return &ownerHub{h: h, cs: cs, st: st, sb: sb, br: fakeOf(sb), sid: sb.SessionID()}
}

// keptOpen reports that c1's entry is still the first one, its session open and no close granted on it.
func (o *ownerHub) keptOpen() bool {
	return !o.sb.life.closingSession(o.sb) && o.h.bridge.mgr.get("c1") == o.sb && !sessionClosed(o.br)
}

// closed reports that c1's first session closed and its entry left the registry with no generation loaded after it.
func (o *ownerHub) closed() bool {
	return sessionClosed(o.br) && o.h.bridge.mgr.get("c1") == nil
}

// tookPrompt reports whether sb's session ran a session/prompt. A registry entry whose door is still loading holds
// no session yet, so a poll of the chat's entry can read one.
func tookPrompt(sb *sharedBridge) bool {
	if sb == nil {
		return false
	}
	s := fakeOf(sb)
	return s != nil && slices.Contains(s.callLog(), marotte.MethodPrompt)
}

// reloadsInline is the chat's next open closing the first session and loading the chat's session again with the
// current settings, in that one call.
func (o *ownerHub) reloadsInline(t *testing.T) *sharedBridge {
	t.Helper()
	next, err := o.h.coord.openReleased(t.Context(), "c1", "")
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
		sb, err := h.coord.openReleased(t.Context(), "c1", "")
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

// tailGate holds every close tail of h just before its end, where the run list takes what runs next, until open
// closes.
type tailGate struct {
	open    chan struct{}
	reached atomic.Int32
	once    sync.Once
}

func blockTails(t *testing.T, h *Runtime) *tailGate {
	t.Helper()
	g := &tailGate{open: make(chan struct{})}
	t.Cleanup(g.release)
	tailHooks(h, func(_ marotte.ChatID, ended bool) {
		if !ended {
			g.reached.Add(1)
			<-g.open
		}
	})
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
			sb, err := o.h.coord.openAtReleased(t.Context(), "c1", "", res)
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
		if cur := o.h.bridge.mgr.get("c1"); cur == o.sb || !tookPrompt(cur) {
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

		sb, door, err := o.h.coord.openBridgeAt(t.Context(), "c1", "", res)
		t.Cleanup(door)
		if err != nil || sb != o.sb || !o.keptOpen() {
			t.Fatalf("an open while KAS reports a run = (same entry %v, %v), session kept %v; want the current session",
				sb == o.sb, err, o.keptOpen())
		}

		o.h.coord.turns.noteKASActivity("c1", false)
		synctest.Wait()
		if !o.keptOpen() {
			t.Fatal("the pending reload closed the session under the opener's open hold and reservation")
		}
		door()
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
		if next, err := o.h.coord.openReleased(t.Context(), "c1", ""); err != nil || next == o.sb {
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
		sb := chatEntry(t, o.h, "c2", br, bridgeStarting)
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
		sb.setReady()
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
		_, done, ok := o.h.coord.turns.beginTail(lifecycleOf(t, o.h, "c1"))
		if !ok {
			t.Fatal("Setup: the life refused the tail")
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
		if next, err := o.h.coord.openReleased(t.Context(), "c1", ""); err != nil || next == o.sb {
			t.Errorf("a later open = (same entry %v, %v), want a new session", next == o.sb, err)
		}
	})
}

func TestCloseOwner_OpenBridgeHoldsTheSessionUntilReleased(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := newOwnerHub(t, false)
		sb, release, err := o.h.coord.openBridge(t.Context(), "c1", "")
		t.Cleanup(release)
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
			t.Fatal("a release closed the session between its open and the opener's call")
		}
		release()
		synctest.Wait()
		if !o.closed() {
			t.Error("the opener's release left the pending release unrun")
		}
	})
}

func TestCloseOwner_LeaseEndAloneDoesNotCloseAnOpenedBridge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := newOwnerHub(t, false)
		if _, err := o.cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Effort = "max"; return true }); err != nil {
			t.Fatalf("Setup: give c1 an effort: %v", err)
		}
		o.h.coord.turns.noteKASActivity("c1", true)
		o.h.coord.releaseIfUnheld("c1")
		// KAS goes idle inside the open's effort repair, so its lease is the last hold to end before the open returns.
		o.br.mu.Lock()
		o.br.onEnsureEffort = func() { o.h.coord.turns.noteKASActivity("c1", false) }
		o.br.mu.Unlock()

		sb, release, err := o.h.coord.openBridge(t.Context(), "c1", "")
		t.Cleanup(release)
		if err != nil || sb != o.sb {
			t.Fatalf("OpenBridge(c1) = (same entry %v, %v), want the current session", sb == o.sb, err)
		}
		if got := o.br.lastEffort(); got != "max" {
			t.Fatalf("Setup: the open's effort repair = %q, want it to run", got)
		}
		synctest.Wait()
		if _, err := sb.Call(t.Context(), "test/after-open", nil); err != nil || !o.keptOpen() {
			t.Fatalf("the opener's call = %v, session kept %v; want it answered on the open session", err, o.keptOpen())
		}
		release()
		synctest.Wait()
		if !o.closed() {
			t.Error("the opener's release left the pending release unrun")
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
		name            string
		pendingFirst    bool
		heldPredecessor bool
	}{
		{name: "the mark alone"},
		{name: "with a reload already pending", pendingFirst: true},
		{name: "the predecessor is held after its reservation release", heldPredecessor: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				o := newOwnerHub(t, true)
				// drainedOnNew reports that the drained prompt reached a session its door loaded again.
				drainedOnNew := func() bool {
					cur := o.h.bridge.mgr.get("c1")
					return cur != o.sb && tookPrompt(cur)
				}
				var heldPast atomic.Bool
				if tc.heldPredecessor {
					// A prompt door's release func waits until the drained prompt ran, so the running prompt's goroutine
					// stays past its reservation release with its deferred release func not yet run.
					o.h.coord.turns.openReleaseHook = func(_ marotte.ChatID, opener uint64) {
						if opener == 0 || drainedOnNew() {
							return
						}
						heldPast.Store(true)
						for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && !drainedOnNew(); {
							time.Sleep(time.Millisecond)
						}
					}
				}
				gate := make(chan struct{})
				release := once(func() { close(gate) })
				t.Cleanup(release)
				o.br.setBlockOn(marotte.MethodPrompt, gate)
				o.br.mu.Lock()
				o.br.chunksOnCall = map[string][]string{marotte.MethodPrompt: {"done"}}
				o.br.mu.Unlock()
				// The drained prompt's door always runs before the tail that drained it ends.
				tailHooks(o.h, func(_ marotte.ChatID, ended bool) {
					if !ended {
						return
					}
					for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
						if tookPrompt(o.h.bridge.mgr.get("c1")) {
							return
						}
					}
				})
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
				if cur := o.h.bridge.mgr.get("c1"); cur == o.sb || !tookPrompt(cur) {
					t.Error("the drained prompt did not reach the session its door loaded again")
				}
				if rows := queuedRows(t, o.cs, "c1"); len(rows) != 0 {
					t.Errorf("queued rows = %v, want the drained one gone", rows)
				}
				if tc.heldPredecessor && !heldPast.Load() {
					t.Error("Setup: no prompt's release func ran before the drained prompt did")
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
		if _, err := o.h.runs.residentReleased(t.Context(), "wf_1"); err != nil {
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
			next, err := o.h.coord.openReleased(t.Context(), "c1", "")
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
		sb := chatEntry(t, h, "c1", chat, bridgeReady)
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

// chatEntry registers br as chatID's entry in state on the chat's own life, as its door would; an entry past its door
// is settled.
func chatEntry(t *testing.T, h *Runtime, chatID marotte.ChatID, br *fakeBridge, state bridgeState) *sharedBridge {
	t.Helper()
	h.coord.turns.ensureTestLife(chatID)
	sb := newSharedBridge(lifecycleOf(t, h, chatID))
	sb.attach(br)
	sb.state = state
	if state != bridgeStarting {
		sb.settled.settle(nil)
	}
	if !h.bridge.mgr.insert(chatID, sb) {
		t.Fatalf("Setup: insert %s's entry = false", chatID)
	}
	return sb
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

// wakeLogged asserts the ended wake turn's text is in the chat's log.
func (o *ownerHub) wakeLogged(t *testing.T, text string) {
	t.Helper()
	if !o.logHolds(t, text) {
		t.Errorf("the woken turn's text %q is not in the chat's log", text)
	}
}

// wakeLanded asserts the woken turn reached the chat's own session; wakeEnds then lands its text in the chat's log.
func (o *ownerHub) wakeLanded(t *testing.T, askID int64) {
	t.Helper()
	if !o.keptOpen() {
		t.Fatal("the chat's session closed before the wake its run's end owed it")
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
		o.wakeLanded(t, 93)
		o.wakeEnds(t)
		o.wakeLogged(t, "Run finished.")
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
		o.wakeLanded(t, 0)
		o.wakeEnds(t)
		o.wakeLogged(t, "Run finished late.")
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
		o.wakeLanded(t, 0)
		o.wakeEnds(t)
		o.wakeLogged(t, "Picking up the run.")

		if !o.closed() {
			t.Error("the late turn's end left the pending release unrun")
		}
	})
}

func TestCloseOwner_EachRunEndKeepsTheChatForItsOwnWake(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := newOwnerHub(t, false)
		o.launchRun(t, recordedRun)
		o.launchRun(t, "wf-second")
		deliverRunComplete(t, o.h, "c1", recordedRun, "completed")
		deliverRunComplete(t, o.h, "c1", "wf-second", "completed")

		o.wakeTurn(t, "First run finished.", 0)
		o.wakeEnds(t)
		o.wakeLogged(t, "First run finished.")
		lc := lifecycleOf(t, o.h, "c1")
		lc.mu.Lock()
		owed := len(lc.wakes)
		lc.mu.Unlock()
		if owed != 1 || !o.keptOpen() {
			t.Fatalf("after the first wake: %d wakes owed, session kept %v; want the second run's wake held", owed, o.keptOpen())
		}
		o.wakeTurn(t, "Second run finished.", 94)
		o.wakeLanded(t, 94)
		o.wakeEnds(t)
		o.wakeLogged(t, "Second run finished.")

		if !o.closed() {
			t.Error("the second wake's end left the pending release unrun")
		}
	})
}

// holdRow acquires one claimant of c1's session the way its real path does, answering the release that ends it.
type holdRow struct {
	acquire func(t *testing.T, o *ownerHub) (release func())
	name    string
	kind    holdKind
	// doorWaits: a reload asked at a door waits for this hold to end, then closes and loads in that same open.
	doorWaits bool
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
	{name: "a door in progress", kind: holdDoor, doorWaits: true, acquire: func(t *testing.T, o *ownerHub) func() {
		_, done, ok := lifecycleOf(t, o.h, "c1").beginWork(workDoor)
		if !ok {
			t.Fatal("Setup: the life refused the door's work")
		}
		o.sb.mu.Lock()
		o.sb.state = bridgeStarting
		o.sb.mu.Unlock()
		return once(func() { o.sb.setReady(); done() })
	}},
	{name: "an open its caller still uses", kind: holdOpen, acquire: func(t *testing.T, o *ownerHub) func() {
		sb, release, err := o.h.coord.openBridge(t.Context(), "c1", "")
		t.Cleanup(release)
		if err != nil || sb != o.sb {
			t.Fatalf("Setup: OpenBridge(c1) = (same entry %v, %v)", sb == o.sb, err)
		}
		return once(release)
	}},
	{name: "a prompt door's open, ended by its reservation", kind: holdOpen, acquire: func(t *testing.T, o *ownerHub) func() {
		res, ok := o.h.coord.turns.tryReserve("c1", marotte.TurnSourcePrompt, o.h.coord.LifeStamp())
		if !ok {
			t.Fatal("Setup: the prompt's reservation was refused on an idle chat")
		}
		sb, door, err := o.h.coord.openBridgeAt(t.Context(), "c1", "", res)
		t.Cleanup(door)
		if err != nil || sb != o.sb {
			t.Fatalf("Setup: OpenBridgeAt(c1) = (same entry %v, %v)", sb == o.sb, err)
		}
		return once(func() { o.h.coord.turns.releaseReservation(res) })
	}},
	{name: "a run step the session hosts", kind: holdStep, acquire: func(t *testing.T, o *ownerHub) func() {
		return hostStepOn(t, o.h, "sess-step")
	}},
	{name: "the wake a run's end owes the chat", kind: holdWake, acquire: func(t *testing.T, o *ownerHub) func() {
		o.launchRun(t, recordedRun)
		deliverRunComplete(t, o.h, "c1", recordedRun, "completed")
		return once(func() {
			kasDelivers(t, o.h, o.frame(t, newActivityMsg(true)))
			kasDelivers(t, o.h, o.frame(t, newTurnStartMsg()))
			kasDelivers(t, o.h, o.frame(t, newTurnEndMsg("end_turn")))
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
					// Marked after the hold: an open the row takes is itself a door, which reloads a marked chat.
					release := row.acquire(t, o)
					t.Cleanup(release)
					o.h.markSessionsForReload("test")

					if row.doorWaits {
						got := openAsync(t, o.h)
						synctest.Wait()
						if !o.keptOpen() || len(got) != 0 {
							t.Fatal("a door's reload did not wait for the door before it")
						}
						release()
						r := <-got
						if r.err != nil || r.sb == o.sb || !sessionClosed(o.br) {
							t.Errorf("the waiting open = (same entry %v, %v), want it to close and load in that call", r.sb == o.sb, r.err)
						}
						return
					}
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
				if _, _, _, err := o.st.Close(t.Context(), subject.ID, nil, nil); err != nil {
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
		_, done, ok := o.r.beginTail(o.lc)
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
		return func() {
			o.lc.mu.Lock()
			tr := o.lc.openLocked("c1", &marotte.Entry{ID: "w-wake"}, marotte.TurnSourceWireTurnStart, "", turnlog.Open("w-wake", nil))
			o.lc.mu.Unlock()
			o.r.finish(tr, marotte.TurnResult{})
		}
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

func TestCloseOwner_OnlyAConfirmedCloseRefusesALease(t *testing.T) {
	o := newOwnerLife(t)
	o.lc.askClose(o.sb, closeRelease, 0, false)
	g, _, ok := o.lc.nextClose()
	if !ok {
		t.Fatal("Setup: the release was not granted")
	}
	_, done, err := o.lc.beginLease(o.sb, func(error) {})
	if err != nil {
		t.Fatalf("a lease while the closer has not confirmed the close = %v, want it admitted", err)
	}
	if o.lc.closingSession(o.sb) {
		t.Error("closingSession names a close the closer has not confirmed")
	}
	if o.lc.confirmClose(g) {
		t.Fatal("the close confirmed while a lease on its entry runs")
	}
	if st := o.state(); st.closing != nil || st.ask == nil || st.ask.why != closeRelease {
		t.Errorf("after the refused confirm: grant %+v, ask %+v; want the release pending again", st.closing, st.ask)
	}
	done()
	g, _, ok = o.lc.nextClose()
	if !ok || !o.lc.confirmClose(g) {
		t.Fatal("the lease's end did not grant the release again for the closer to confirm")
	}
	if _, _, err := o.lc.beginLease(o.sb, func(error) {}); !errors.Is(err, marotte.ErrSessionClosing) {
		t.Errorf("a lease on the closing entry = %v, want marotte.ErrSessionClosing", err)
	}
	other := newSharedBridge(o.lc)
	_, done, err = o.lc.beginLease(other, func(error) {})
	if err != nil {
		t.Errorf("a lease on another entry = %v, want it admitted", err)
	}
	done()
	if !o.lc.closingSession(o.sb) || o.lc.closingSession(other) {
		t.Error("closingSession does not name exactly the confirmed entry")
	}
}

func TestCloseOwner_AHoldStartedSinceTheGrantPutsTheCloseBack(t *testing.T) {
	for _, k := range []holdKind{holdTurn, holdTail, holdKAS, holdStep, holdWake, holdLease} {
		h := ownerHolds[k]
		t.Run(h.name, func(t *testing.T) {
			o := newOwnerLife(t)
			o.lc.askClose(o.sb, closeRelease, 0, false)
			g, _, ok := o.lc.nextClose()
			if !ok {
				t.Fatal("Setup: the release was not granted")
			}
			release := h.take(t, o)
			if o.lc.confirmClose(g) {
				t.Fatalf("the close confirmed while a %s hold started since its grant stands", h.name)
			}
			if st := o.state(); st.closing != nil || st.ask == nil || st.ask.why != closeRelease {
				t.Errorf("after the refused confirm: grant %+v, ask %+v; want the release pending again", st.closing, st.ask)
			}
			release()
			g, _, ok = o.lc.nextClose()
			if !ok || !o.lc.confirmClose(g) {
				t.Errorf("the %s hold's end did not grant the release again for the closer to confirm", h.name)
			}
		})
	}
}

func TestCloseOwner_OnlyAWireTurnTakesAWake(t *testing.T) {
	for _, row := range []struct {
		name   string
		source marotte.TurnOpenSource
	}{
		{name: "a prompt", source: marotte.TurnSourcePrompt},
		{name: "an empty retry", source: marotte.TurnSourceEmptyRetry},
	} {
		t.Run(row.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				o := newOwnerLife(t)
				o.r.holdWake("c1", "wf-owner")
				o.lc.askClose(o.sb, closeRelease, 0, false)
				o.lc.mu.Lock()
				own := o.lc.openLocked("c1", &marotte.Entry{ID: "p-wake"}, row.source, "", turnlog.Open("p-wake", nil))
				o.lc.mu.Unlock()
				o.r.finish(own, marotte.TurnResult{})
				if got := o.blocker(0); got != holdWake {
					t.Errorf("the hold after marotte's own turn ended = %d, want the wake hold standing for KAS's turn", got)
				}
				if o.state().closing != nil {
					t.Fatal("the release was granted before KAS's wake turn opened")
				}
				o.lc.mu.Lock()
				wake := o.lc.openLocked("c1", &marotte.Entry{ID: "w-wake"}, marotte.TurnSourceWireTurnStart, "", turnlog.Open("w-wake", nil))
				wakes := len(o.lc.wakes)
				o.lc.mu.Unlock()
				if wakes != 0 {
					t.Errorf("%d wake holds after KAS's wake turn opened, want it to take the hold", wakes)
				}
				o.r.finish(wake, marotte.TurnResult{})
				if o.state().closing == nil {
					t.Error("KAS's wake turn ended and the release stayed pending")
				}
			})
		})
	}
}

func TestCloseOwner_TheLifesEndEndsAGrantNoCloserTook(t *testing.T) {
	o := newOwnerLife(t)
	o.lc.askClose(o.sb, closeRelease, 0, false)
	if g := o.state().closing; g == nil || g.frozen {
		t.Fatalf("Setup: the release's grant = %+v, want granted and not yet taken by a closer", g)
	}
	door := o.lc.askClose(o.sb, closeReload, 0, true)
	if door.verdict != askClosing {
		t.Fatalf("Setup: a door on the granted entry = %+v, want it told to wait for the close", door)
	}

	o.r.forget("c1")

	select {
	case <-door.done:
	default:
		t.Error("the life ended and the door still waits on a close no closer will run")
	}
	if o.state().closing != nil {
		t.Error("the ended life kept a grant no closer took")
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
			o.r.holdWake("c1", "wf-second")
			o.lc.mu.Lock()
			o.lc.openLocked("c1", &marotte.Entry{ID: "w-wake"}, marotte.TurnSourceWireTurnStart, "", turnlog.Open("w-wake", nil))
			wakes := len(o.lc.wakes)
			o.lc.mu.Unlock()
			if wakes != 1 {
				t.Errorf("%d wake holds after one turn opened for two owed wakes, want that turn to take over one", wakes)
			}
		})
	})
	t.Run("KAS's activity", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			o := newOwnerLife(t)
			o.r.holdWake("c1", "wf-owner")
			o.r.noteKASActivity("c1", true)
			o.lc.mu.Lock()
			wakes := len(o.lc.wakes)
			o.lc.mu.Unlock()
			if wakes != 1 {
				t.Errorf("%d wake holds after KAS reported activity, want the wake held until its turn", wakes)
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

// holdCloser makes h's closers wait at closerHook until the returned open runs; reached counts the grants it took.
func holdCloser(t *testing.T, h *Runtime) (reached *atomic.Int32, open func()) {
	t.Helper()
	gate := make(chan struct{})
	open = once(func() { close(gate) })
	t.Cleanup(open)
	reached = new(atomic.Int32)
	h.coord.turns.closerHook = func(marotte.ChatID) {
		reached.Add(1)
		<-gate
	}
	return reached, open
}

func TestCloseOwner_TheRunEndKeepsATabbedParentResident(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cs, _ := newTestHub(WithConfigDir(t.TempDir()))
		t.Cleanup(func() { shutdownHub(t, h) })
		if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true }); err != nil {
			t.Fatalf("Setup: seed c1: %v", err)
		}
		chat := newFakeBridge()
		chat.sessionID = "sess_parent"
		sb := chatEntry(t, h, "c1", chat, bridgeReady)
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
		reached, openHook := holdCloser(t, h)

		handled := make(chan struct{})
		go func() {
			defer close(handled)
			deliverRunComplete(t, h, "c1", recordedRun, "completed")
		}()
		<-handled
		if _, held := h.runs.lease(recordedRun); held {
			t.Fatal("Setup: the run's end kept its lease")
		}
		openHook()
		synctest.Wait()
		kasDelivers(t, h, withSessionID(t, newTurnStartMsg(), "sess_parent"))
		kasDelivers(t, h, withSessionID(t, newChunkMsg("Run finished."), "sess_parent"))
		kasDelivers(t, h, withSessionID(t, newTurnEndMsg("end_turn"), "sess_parent"))
		synctest.Wait()

		if n := reached.Load(); n != 0 {
			t.Errorf("a tabbed chat's reload reached the closer %d times, want it left to the chat's next door", n)
		}
		if h.bridge.mgr.get("c1") != sb || !chat.Open() {
			t.Fatal("the run's end closed a tabbed parent's session outside a door")
		}
		lc := lifecycleOf(t, h, "c1")
		lc.mu.Lock()
		marked, closers := lc.reloadFor == sb, lc.work[workCloser]
		lc.mu.Unlock()
		if !marked || closers != 0 {
			t.Errorf("after the closer ran: marked %v, %d closers; want the mark kept and no closer left", marked, closers)
		}
		if !slices.Contains(textsOf(t, logOf(t, cs, "c1")), "Run finished.") {
			t.Error("the wake turn's text is not in the chat's log")
		}
		next, err := h.coord.openReleased(t.Context(), "c1", "")
		if err != nil || next == sb || chat.Open() {
			t.Errorf("the next open = (same entry %v, %v), first session open %v; want it closed and a new generation", next == sb, err, chat.Open())
		}
	})
}

func TestCloseOwner_AKeptChatsLeaseIsNeverRefusedForACloseThatWillNotHappen(t *testing.T) {
	for _, tc := range []struct {
		// ask runs while c1 runs a wire turn: a close asked that its re-check will find c1 kept resident.
		ask func(t *testing.T, o *ownerHub)
		// closerLooks is whether the hold's end grants the close to the closer.
		closerLooks bool
		name        string
		tabbed      bool
	}{
		{name: "a tabbed chat's door reload a turn refused", tabbed: true, ask: func(t *testing.T, o *ownerHub) {
			o.h.markSessionsForReload("test")
			o.openReturnsCurrent(t, openAsync(t, o.h), "a turn runs")
		}},
		{name: "a release asked before the chat's tab opened", closerLooks: true, ask: func(t *testing.T, o *ownerHub) {
			o.h.coord.releaseIfUnheld("c1")
			if _, _, _, err := o.st.Open(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c1"}); err != nil {
				t.Fatalf("Setup: open c1's tab: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				o := newOwnerHub(t, tc.tabbed)
				reached, openHook := holdCloser(t, o.h)
				send(t, o.h, newTurnStartMsg())
				tc.ask(t, o)
				send(t, o.h, newTurnEndMsg("end_turn"))
				synctest.Wait()

				if err := o.sb.use(t.Context(), func(context.Context, engineSession) error { return nil }); err != nil {
					t.Errorf("a lease after the turn ended = %v, want it admitted", err)
				}
				if looked := reached.Load() != 0; looked != tc.closerLooks {
					t.Errorf("the closer looked %v after the turn ended, want %v", looked, tc.closerLooks)
				}
				openHook()
				synctest.Wait()
				if !o.keptOpen() {
					t.Error("the kept chat's session closed")
				}
			})
		})
	}
}

func TestCloseOwner_ARunEndDuringAFrozenCloseRequeues(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := newOwnerHub(t, false)
		reached, openHook := holdCloser(t, o.h)
		o.h.coord.releaseIfUnheld("c1")
		synctest.Wait()
		if reached.Load() != 1 {
			t.Fatal("Setup: the release reached no closer")
		}

		deliverRunComplete(t, o.h, "c1", recordedRun, "completed")
		openHook()
		synctest.Wait()

		if !o.keptOpen() {
			t.Fatal("the closer closed the session while the run's end owed it a wake")
		}
		lc := lifecycleOf(t, o.h, "c1")
		lc.mu.Lock()
		pending := lc.ask != nil && lc.ask.sb == o.sb && lc.closing == nil
		lc.mu.Unlock()
		if !pending {
			t.Error("the closer's withdraw left no pending release for the wake's end")
		}
		o.wakeTurn(t, "Run finished.", 0)
		o.wakeLanded(t, 0)
		o.wakeEnds(t)
		o.wakeLogged(t, "Run finished.")
		if !o.closed() {
			t.Error("the wake's end left the pending release unrun")
		}
	})
}

func TestCloseOwner_AReleaseAskedDuringTheNextDoorRunsOnTheCloser(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := newOwnerHub(t, false)
		o.h.markSessionsForReload("test")
		o.h.coord.turns.noteKASActivity("c1", true)
		o.openReturnsCurrent(t, openAsync(t, o.h), "KAS reports a run")
		o.h.coord.turns.noteKASActivity("c1", false)
		synctest.Wait()
		if !o.closed() {
			t.Fatal("Setup: the pending reload of a tabless chat did not close it")
		}
		next, err := o.h.coord.openReleased(t.Context(), "c1", "")
		if err != nil || next == o.sb {
			t.Fatalf("Setup: the next open = (same entry %v, %v)", next == o.sb, err)
		}
		closes := countCloses(fakeOf(next))
		lc := lifecycleOf(t, o.h, "c1")
		_, done, ok := lc.beginWork(workDoor)
		if !ok {
			t.Fatal("Setup: the life refused the door")
		}

		o.h.coord.releaseIfUnheld("c1")
		synctest.Wait()
		if o.h.bridge.mgr.get("c1") != next || closes.Load() != 0 {
			t.Fatal("the release closed the next generation while its door is counted")
		}
		done()
		synctest.Wait()

		if closes.Load() != 1 || o.h.bridge.mgr.get("c1") != nil {
			t.Errorf("after the door's end: %d session/close calls, entry kept %v; want one close", closes.Load(), o.h.bridge.mgr.get("c1") != nil)
		}
		lc.mu.Lock()
		closer, closers := lc.closer, lc.work[workCloser]
		lc.mu.Unlock()
		if closer || closers != 0 {
			t.Errorf("closer running %v with %d closers counted, want none", closer, closers)
		}
	})
}

func TestCloseOwner_ReleaseAskOnTheForwardNeverBlocks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := newOwnerHub(t, false)
		gate := make(chan struct{})
		release := once(func() { close(gate) })
		t.Cleanup(release)
		o.br.mu.Lock()
		o.br.stopGate = gate
		o.br.mu.Unlock()
		kasDelivers(t, o.h, o.frame(t, newActivityMsg(true)))
		o.h.coord.releaseIfUnheld("c1")

		kasDelivers(t, o.h, o.frame(t, newActivityMsg(false)))
		synctest.Wait()

		if !o.sb.life.closingSession(o.sb) {
			t.Fatal("KAS's idle report granted the pending release no close")
		}
		release()
		synctest.Wait()
		if !o.closed() {
			t.Error("the granted release left the session open")
		}
	})
}

func TestCloseOwner_AStaleReleaseGrantMovesToTheCurrentEntry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := newOwnerHub(t, false)
		_, done, ok := o.h.coord.turns.beginTail(lifecycleOf(t, o.h, "c1"))
		if !ok {
			t.Fatal("Setup: the life refused the tail")
		}
		done = once(done)
		t.Cleanup(done)
		o.h.coord.releaseIfUnheld("c1")
		synctest.Wait()
		o.br.end()
		synctest.Wait()
		if o.h.bridge.mgr.get("c1") != nil {
			t.Fatal("Setup: the session that ended on its own kept its entry")
		}
		next, err := o.h.coord.openReleased(t.Context(), "c1", "")
		if err != nil || next == o.sb {
			t.Fatalf("Setup: the next open = (same entry %v, %v)", next == o.sb, err)
		}
		closes := countCloses(fakeOf(next))

		done()
		synctest.Wait()

		if closes.Load() != 1 || o.h.bridge.mgr.get("c1") != nil {
			t.Errorf("the release owed when the first entry ended: %d session/close calls on the next one, entry kept %v; want it closed once",
				closes.Load(), o.h.bridge.mgr.get("c1") != nil)
		}
	})
}

func TestCloseOwner_AStepOpenedBeforeTheChatsFirstDoorHolds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := newOwnerHub(t, false)
		if _, err := o.cs.Mutate(t.Context(), "c2", func(c *marotte.Chat, _ bool) bool { c.Name = "B"; return true }); err != nil {
			t.Fatalf("Setup: seed c2: %v", err)
		}
		if _, ok := o.h.coord.turns.lookup("c2"); ok {
			t.Fatal("Setup: c2 has a life before its first door")
		}
		if _, _, err := o.h.runs.log.open(t.Context(), &translate.RunStep{RunID: "wf-c2", NodePath: "wf-c2/step", SessionID: "sess-step"}, "c2"); err != nil {
			t.Fatalf("Setup: open the hosted step turn: %v", err)
		}
		sb, err := o.h.coord.openReleased(t.Context(), "c2", "")
		if err != nil {
			t.Fatalf("Setup: OpenBridge(c2): %v", err)
		}

		o.h.coord.releaseIfUnheld("c2")
		synctest.Wait()
		if o.h.bridge.mgr.get("c2") != sb || sessionClosed(fakeOf(sb)) {
			t.Fatal("the release closed a session hosting a step opened before the chat's first door")
		}
		if _, _, err := o.h.runs.log.closeNode(t.Context(), "wf-c2", "wf-c2/step", string(marotte.RunStatusCompleted), "", nil); err != nil {
			t.Fatalf("close the hosted step turn: %v", err)
		}
		synctest.Wait()
		if !sessionClosed(fakeOf(sb)) || o.h.bridge.mgr.get("c2") != nil {
			t.Error("the step's close left the pending release unrun")
		}
	})
}

func TestCloseOwner_EveryAcquireReleasesOnError(t *testing.T) {
	t.Run("a closer the runtime refuses to start", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			o := newOwnerHub(t, false)
			o.h.coord.turns.noteKASActivity("c1", true)
			o.h.lifecycle.draining.Store(true)
			o.h.coord.releaseIfUnheld("c1")
			o.h.lifecycle.draining.Store(false)

			lc := lifecycleOf(t, o.h, "c1")
			lc.mu.Lock()
			closer, closers, ask := lc.closer, lc.work[workCloser], lc.ask
			lc.mu.Unlock()
			if closer || closers != 0 || ask != nil {
				t.Errorf("after a refused start: closer %v, %d closers, ask %+v; want nothing owed", closer, closers, ask)
			}
			o.h.coord.turns.noteKASActivity("c1", false)
			o.h.coord.releaseIfUnheld("c1")
			synctest.Wait()
			if !o.closed() {
				t.Error("a later release ask found the refused start still owed")
			}
		})
	})
	for _, tc := range []struct {
		// fail drives one acquire site into an error after its open, answering how many open holds it took.
		fail func(t *testing.T, o *ownerHub) (opens int)
		name string
	}{
		{name: "a run host on a chat open on another session", fail: func(t *testing.T, o *ownerHub) int {
			o.h.runs.grantLease(t.Context(), "wf_1", "fix", launchOrigin{origin: runlease.OriginAgent, chatID: "c1"}, hostSec())
			o.h.runs.recordParentSession(t.Context(), "wf_1", "sess-other")
			_, release, err := o.h.runs.ensureResident(t.Context(), "wf_1")
			defer release()
			if !errors.Is(err, errLaunchSessionUnavailable) {
				t.Errorf("ensureResident = %v, want %v", err, errLaunchSessionUnavailable)
			}
			return 1
		}},
		{name: "a peer's target chat open on another session", fail: func(t *testing.T, o *ownerHub) int {
			if _, err := o.cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.ACPSessionID = "sess-old"; return true }); err != nil {
				t.Fatalf("Setup: record c1 on another session: %v", err)
			}
			_, _, release, err := o.h.peerTargetChat(t.Context(), "sess-old")
			defer release()
			if !errors.Is(err, errPeerTargetMoved) {
				t.Errorf("peerTargetChat = %v, want %v", err, errPeerTargetMoved)
			}
			return 1
		}},
		{name: "a notice to the launching agent that fails", fail: func(t *testing.T, o *ownerHub) int {
			o.launchRun(t, "wf_1")
			o.br.setCallErr(marotte.MethodSessionSteer, errors.New("broken pipe"))
			o.h.runs.tellLaunchingAgent(t.Context(), "wf_1", "c1", "test", "hello")
			if !slices.Contains(o.br.callLog(), marotte.MethodSessionSteer) {
				t.Error("Setup: the notice never reached the launching session")
			}
			return 1
		}},
		{name: "the boot pass over two chat-parented runs", fail: func(t *testing.T, o *ownerHub) int {
			o.launchRun(t, "wf_1")
			o.launchRun(t, "wf_2")
			o.h.runs.askBootPass(t.Context())
			return 2
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				o := newOwnerHub(t, false)
				var released atomic.Int32
				o.h.coord.turns.openReleaseHook = func(chatID marotte.ChatID, _ uint64) {
					if chatID == "c1" {
						released.Add(1)
					}
				}
				opens := tc.fail(t, o)
				synctest.Wait()

				if got := released.Load(); got != int32(opens) {
					t.Errorf("open holds released = %d, want %d", got, opens)
				}
				o.assertNoHolds(t, "c1")
				for _, l := range o.h.runs.leaseStore().List() {
					if err := o.h.runs.leaseStore().Release(t.Context(), l.WorkflowID); err != nil {
						t.Fatalf("drop the lease %s: %v", l.WorkflowID, err)
					}
				}
				o.h.coord.releaseIfUnheld("c1")
				synctest.Wait()
				if !o.closed() {
					t.Error("a release ask after the failed acquire left the session open")
				}
			})
		})
	}
	t.Run("a turn whose close tail the runtime refuses to start", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			o := newOwnerHub(t, false)
			o.h.lifecycle.draining.Store(true)
			send(t, o.h, newTurnStartMsg(), newTurnEndMsg("end_turn"))
			synctest.Wait()
			o.h.lifecycle.draining.Store(false)
			o.assertNoHolds(t, "c1")
		})
	})
	t.Run("a door whose generation fails", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			o := newOwnerHub(t, false)
			sb, release, err := o.h.coord.openBridge(t.Context(), "c-missing", "")
			defer release()
			if err == nil || sb != nil {
				t.Fatalf("OpenBridge(c-missing) = (%v, %v), want the door's failure", sb, err)
			}
			synctest.Wait()
			o.assertNoHolds(t, "c-missing")
		})
	})
}

// assertNoHolds fails when chatID's life still counts a hold or work an acquire took.
func (o *ownerHub) assertNoHolds(t *testing.T, chatID marotte.ChatID) {
	t.Helper()
	lc, ok := o.h.coord.turns.lookup(chatID)
	if !ok {
		return
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if len(lc.opens) != 0 || len(lc.tails) != 0 || lc.work[workDoor] != 0 || lc.work[workSession] != 0 ||
		lc.work[workTail] != 0 || lc.blockerLocked(0) != holdKinds {
		t.Errorf("%s holds after the failed acquire: %d opens, %d tails, work %v, blocker %d; want none",
			chatID, len(lc.opens), len(lc.tails), lc.work, lc.blockerLocked(0))
	}
}

func TestCloseOwner_TeardownEndsWithAnOpenHoldHeld(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := newOwnerHub(t, false)
		_, withheld, err := o.h.coord.openBridge(t.Context(), "c1", "")
		if err != nil {
			t.Fatalf("Setup: OpenBridge(c1): %v", err)
		}
		o.h.coord.releaseIfUnheld("c1")
		synctest.Wait()
		if !o.keptOpen() {
			t.Fatal("Setup: the release closed the session under the caller's open hold")
		}
		lc := lifecycleOf(t, o.h, "c1")
		var mu sync.Mutex
		var steps []string
		setTeardownHook(t, o.h, func(step string) {
			mu.Lock()
			steps = append(steps, step)
			mu.Unlock()
		})

		o.h.cleanupChatState(t.Context(), "c1")
		synctest.Wait()

		mu.Lock()
		reached := slices.Contains(steps, "forgotten")
		mu.Unlock()
		if !reached {
			t.Fatalf("teardown steps = %v, want it past its wait for the life's work", steps)
		}
		lc.mu.Lock()
		opens, closer, closers := len(lc.opens), lc.closer, lc.work[workCloser]
		lc.mu.Unlock()
		if opens != 0 || closer || closers != 0 {
			t.Errorf("after the life's end: %d opens, closer %v, %d closers; want none", opens, closer, closers)
		}
		withheld()
		lc.mu.Lock()
		opens = len(lc.opens)
		lc.mu.Unlock()
		if opens != 0 {
			t.Errorf("the withheld release after the life's end left %d opens", opens)
		}
	})
}
