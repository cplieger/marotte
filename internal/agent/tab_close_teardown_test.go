package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/tabs"
)

// closeTabHub is a runtime with a tab store and chat c1 open in a tab.
func closeTabHub(t *testing.T) (*Runtime, *testChatStore, *fakeBridge, marotte.TabSubject) {
	t.Helper()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("Setup: tab store: %v", err)
	}
	h, cs, br := newTestHub(WithTabs(st))
	seedChat(t, cs, "c1")
	opened, err := h.Membership().OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c1"}, "op-open")
	if err != nil {
		t.Fatalf("Setup: open tab: %v", err)
	}
	return h, cs, br, opened.Subject
}

func postCloseTab(t *testing.T, h *Runtime, tab marotte.TabSubject) int {
	t.Helper()
	return postCloseTabOp(t, h, tab, "op-close")
}

func postCloseTabOp(t *testing.T, h *Runtime, tab marotte.TabSubject, opID string) int {
	t.Helper()
	payload, _ := json.Marshal(marotte.CloseTabCommand{ID: tab.ID, OpID: opID})
	return postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdCloseTab, Payload: payload}).Code
}

// reopenTab opens c1's tab again, as another device or the History does.
func reopenTab(t *testing.T, h *Runtime, opID string) marotte.TabSubject {
	t.Helper()
	opened, err := h.Membership().OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c1"}, opID)
	if err != nil {
		t.Fatalf("reopen tab: %v", err)
	}
	return opened.Subject
}

// postPromptReply is postPrompt answering the status and the refusal's reason.
func postPromptReply(t *testing.T, h *Runtime, text string) (int, string) {
	t.Helper()
	rec := postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdPrompt, ChatID: "c1",
		Payload: json.RawMessage(`{"text":"` + text + `","message_id":"m-` + text + `"}`),
	})
	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body.Reason
}

// doorCounter counts the doors a fake session enters, before any gate it holds.
func doorCounter(b *fakeBridge) *atomic.Int32 {
	var n atomic.Int32
	b.onStart = func() { n.Add(1) }
	return &n
}

// awaitWire waits until b wrote method.
func awaitWire(t *testing.T, b *fakeBridge, method string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !slices.Contains(b.wireLog(), method); {
		if time.Now().After(deadline) {
			t.Fatalf("wire = %v, want %s", b.wireLog(), method)
		}
		time.Sleep(time.Millisecond)
	}
}

// awaitSessionClosed waits until b's close was confirmed.
func awaitSessionClosed(t *testing.T, b *fakeBridge) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !b.isClosed(); {
		if time.Now().After(deadline) {
			t.Fatal("the session's close was never confirmed")
		}
		time.Sleep(time.Millisecond)
	}
}

// parkTeardownAt parks the first teardown that reaches step until the returned release runs; reached closes when it
// parks.
func parkTeardownAt(t *testing.T, h *Runtime, step string) (reached <-chan struct{}, release func()) {
	t.Helper()
	at, gate := make(chan struct{}), make(chan struct{})
	var once, parked sync.Once
	setTeardownHook(t, h, func(s string) {
		if s != step {
			return
		}
		first := false
		parked.Do(func() { first = true })
		if first {
			close(at)
			<-gate
		}
	})
	return at, func() { once.Do(func() { close(gate) }) }
}

// A committed close answers at once on a busy engine; the session's close, which the engine is slow to confirm, ends
// in the background.
func TestCloseTab_ABusyEngineAnswersAtOnceAndClosesTheSessionLater(t *testing.T) {
	logs := captureLogs(t)
	h, _, br, tab := closeTabHub(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("Setup: open session: %v", err)
	}
	gate := make(chan struct{})
	br.mu.Lock()
	br.stopGate = gate
	br.mu.Unlock()

	start := time.Now()
	code := postCloseTab(t, h, tab)
	took := time.Since(start)
	close(gate)
	if code != http.StatusOK {
		t.Fatalf("close_tab = %d, want 200", code)
	}
	if took > time.Second {
		t.Errorf("close_tab answered after %v, want at once: the close commits before the session teardown", took)
	}
	awaitTeardown(t, h, "c1")
	awaitSessionClosed(t, br)
	if sb := h.bridge.mgr.get("c1"); sb != nil {
		t.Error("the closed chat still has a registered session")
	}
	if strings.Contains(logs.String(), "did not drain in time") {
		t.Errorf("a slow but confirmed close was logged as a drain failure: %s", logs.String())
	}
}

// A prompt waiting on its session door when the tab closes ends cancelled with no error frame, and no second door
// opens a session for the closed chat.
func TestCloseTab_APromptWaitingOnItsDoorEndsCancelledAndOpensNoSecondDoor(t *testing.T) {
	h, _, br, tab := closeTabHub(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	gate := make(chan struct{})
	br.mu.Lock()
	br.startGate = gate
	doors := doorCounter(br)
	br.mu.Unlock()

	if code := postPrompt(t, h, "hi"); code != http.StatusOK {
		t.Fatalf("prompt = %d, want its ack", code)
	}
	for deadline := time.Now().Add(5 * time.Second); doors.Load() == 0; {
		if time.Now().After(deadline) {
			t.Fatal("the prompt's door never opened")
		}
		time.Sleep(time.Millisecond)
	}
	if code := postCloseTab(t, h, tab); code != http.StatusOK {
		t.Fatalf("close_tab = %d, want 200", code)
	}
	awaitTeardown(t, h, "c1")
	close(gate)
	joinInflight(t, h)

	if got := doors.Load(); got != 1 {
		t.Errorf("session doors = %d, want 1: a closed chat opens no second session", got)
	}
	if slices.Contains(br.wireLog(), marotte.MethodPrompt) {
		t.Errorf("wire = %v, want no session/prompt for the closed chat", br.wireLog())
	}
	if sb := h.bridge.mgr.get("c1"); sb != nil {
		t.Error("the closed chat still has a registered session")
	}
	if closed := closedBroadcasts(t, h); len(closed) != 1 || closed[0].Outcome != marotte.TurnOutcomeCancelled {
		t.Errorf("turn_closed = %+v, want the prompt's one turn cancelled", closed)
	}
	if errs := eventsOfType(h, marotte.EventError); len(errs) != 0 {
		t.Errorf("error frames = %+v, want none for a chat the user closed", errs)
	}
}

// A running turn is cancelled while its session is still open, then the session is let go.
func TestCloseTab_ARunningTurnIsCancelledBeforeItsSessionCloses(t *testing.T) {
	h, _, br, tab := closeTabHub(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("Setup: open session: %v", err)
	}
	release := make(chan struct{})
	var openAtCancel atomic.Int32
	br.mu.Lock()
	br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: release}
	br.onNotify = func(method string) {
		if method != marotte.MethodCancel {
			return
		}
		if br.Open() {
			openAtCancel.Store(1)
		} else {
			openAtCancel.Store(2)
		}
	}
	br.mu.Unlock()
	if code := postPrompt(t, h, "hi"); code != http.StatusOK {
		t.Fatalf("prompt = %d, want its ack", code)
	}
	awaitWire(t, br, marotte.MethodPrompt)

	if code := postCloseTab(t, h, tab); code != http.StatusOK {
		t.Fatalf("close_tab = %d, want 200", code)
	}
	close(release)
	awaitTeardown(t, h, "c1")
	joinInflight(t, h)

	switch openAtCancel.Load() {
	case 0:
		t.Errorf("wire = %v, want the running turn's session/cancel", br.wireLog())
	case 2:
		t.Error("session/cancel was written after the session was let go")
	}
	awaitSessionClosed(t, br)
}

// A reopen and prompt while the close's teardown is still cancelling wait for the teardown, then run on a fresh
// session: no door opens and nothing reaches the dying session meanwhile.
func TestCloseTab_AReopenDuringTheCancelWaitsForTheTeardown(t *testing.T) {
	h, _, br, tab := closeTabHub(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("Setup: open session: %v", err)
	}
	cancelGate := make(chan struct{})
	br.mu.Lock()
	br.blockNotifyOn = map[string]chan struct{}{marotte.MethodCancel: cancelGate}
	br.mu.Unlock()
	next := newFakeBridge()
	doorStarted := make(chan bool, 1)
	next.onStart = func() { doorStarted <- h.bus.steers.gone("c1") }
	h.engine.source.(*fakeEngineSource).setFactory(func() engineSession { return next })

	closed := make(chan int, 1)
	go func() { closed <- postCloseTab(t, h, tab) }()
	select {
	case code := <-closed:
		if code != http.StatusOK {
			t.Fatalf("close_tab = %d, want 200", code)
		}
	case <-time.After(5 * time.Second):
		close(cancelGate)
		t.Fatal("close_tab did not answer while its teardown was cancelling the turn")
	}
	reopenTab(t, h, "op-reopen")
	prompted := make(chan int, 1)
	go func() { prompted <- postPrompt(t, h, "again") }()
	select {
	case gone := <-doorStarted:
		t.Errorf("a door opened while the teardown still held the chat (record gone = %v)", gone)
	case <-time.After(200 * time.Millisecond):
	}
	if slices.Contains(br.wireLog(), marotte.MethodPrompt) {
		t.Errorf("closed session wire = %v: the reopened chat's prompt ran on the session its teardown closes", br.wireLog())
	}
	close(cancelGate)

	if code := <-prompted; code != http.StatusOK {
		t.Fatalf("prompt = %d, want its ack once the teardown ended", code)
	}
	select {
	case gone := <-doorStarted:
		if gone {
			t.Error("the fresh door opened on the torn-down steer record")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the reopened chat's prompt never opened a session")
	}
	awaitWire(t, next, marotte.MethodPrompt)
	if slices.Contains(br.wireLog(), marotte.MethodPrompt) {
		t.Errorf("closed session wire = %v, want no prompt on it", br.wireLog())
	}
	awaitSessionClosed(t, br)
}

// A reopen and prompt while the teardown is past its forgets and before its steer record's end wait for the forgets,
// then run on a fresh session with a fresh steer record.
func TestCloseTab_AReopenDuringTheForgetsWaitsForThem(t *testing.T) {
	h, _, br, tab := closeTabHub(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("Setup: open session: %v", err)
	}
	next := newFakeBridge()
	doorStarted := make(chan bool, 1)
	next.onStart = func() { doorStarted <- h.bus.steers.gone("c1") }
	h.engine.source.(*fakeEngineSource).setFactory(func() engineSession { return next })
	reached, release := parkTeardownAt(t, h, "forgotten")
	t.Cleanup(release)

	closed := make(chan int, 1)
	go func() { closed <- postCloseTab(t, h, tab) }()
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the teardown never reached its forgets")
	}
	reopenTab(t, h, "op-reopen")
	prompted := make(chan int, 1)
	go func() { prompted <- postPrompt(t, h, "again") }()
	select {
	case gone := <-doorStarted:
		t.Errorf("a door opened during the teardown's forgets (record gone = %v)", gone)
	case <-time.After(200 * time.Millisecond):
	}
	release()
	if code := <-closed; code != http.StatusOK {
		t.Fatalf("close_tab = %d, want 200", code)
	}
	if code := <-prompted; code != http.StatusOK {
		t.Fatalf("prompt = %d, want its ack once the teardown ended", code)
	}
	select {
	case gone := <-doorStarted:
		if gone {
			t.Error("the fresh door opened on the torn-down steer record")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the reopened chat's prompt never opened a session")
	}
	awaitWire(t, next, marotte.MethodPrompt)
	if slices.Contains(br.wireLog(), marotte.MethodPrompt) {
		t.Errorf("closed session wire = %v, want no prompt on it", br.wireLog())
	}
}

// A prompt sent to a reopened tab while the first close's teardown still runs, whose tab is then closed again, never
// runs: the second close ends it at its admission, and no door opens.
func TestCloseTab_APromptWaitingOnATeardownIsEndedByASecondClose(t *testing.T) {
	h, _, br, tab := closeTabHub(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("Setup: open session: %v", err)
	}
	cancelGate := make(chan struct{})
	br.mu.Lock()
	br.blockNotifyOn = map[string]chan struct{}{marotte.MethodCancel: cancelGate}
	br.mu.Unlock()
	next := newFakeBridge()
	doors := doorCounter(next)
	h.engine.source.(*fakeEngineSource).setFactory(func() engineSession { return next })

	if code := postCloseTab(t, h, tab); code != http.StatusOK {
		close(cancelGate)
		t.Fatalf("close_tab = %d, want 200", code)
	}
	reopened := reopenTab(t, h, "op-reopen")
	type reply struct {
		reason string
		code   int
	}
	prompted := make(chan reply, 1)
	go func() {
		code, reason := postPromptReply(t, h, "again")
		prompted <- reply{code: code, reason: reason}
	}()
	select {
	case r := <-prompted:
		close(cancelGate)
		t.Fatalf("prompt = %d %q before the second close, want it waiting on the teardown", r.code, r.reason)
	case <-time.After(200 * time.Millisecond):
	}
	if code := postCloseTabOp(t, h, reopened, "op-close-again"); code != http.StatusOK {
		close(cancelGate)
		t.Fatalf("second close_tab = %d, want 200", code)
	}
	close(cancelGate)

	r := <-prompted
	if r.code != http.StatusConflict || r.reason != "chat_closed" {
		t.Errorf("prompt = %d %q, want 409 chat_closed: the chat closed after the prompt began", r.code, r.reason)
	}
	awaitTeardown(t, h, "c1")
	if n := doors.Load(); n != 0 {
		t.Errorf("session doors after the second close = %d, want none for a closed tab", n)
	}
	if slices.Contains(next.wireLog(), marotte.MethodPrompt) || slices.Contains(br.wireLog(), marotte.MethodPrompt) {
		t.Errorf("wire = %v then %v, want no session/prompt for the closed tab", br.wireLog(), next.wireLog())
	}
	if errs := eventsOfType(h, marotte.EventError); len(errs) != 0 {
		t.Errorf("error frames = %+v, want none for a chat the user closed", errs)
	}
}

// The teardown lets its session go when kiro-cli never answers its close: close_tab has answered, the job returns,
// and a reopen's door for the same session waits until the engine confirms the close.
func TestCloseTab_TheTeardownReleasesItsSession(t *testing.T) {
	h, _, br, tab := closeTabHub(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("Setup: open session: %v", err)
	}
	gate := make(chan struct{})
	br.mu.Lock()
	br.stopGate = gate
	br.mu.Unlock()
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})

	start := time.Now()
	if code := postCloseTab(t, h, tab); code != http.StatusOK {
		t.Fatalf("close_tab = %d, want 200", code)
	}
	awaitTeardown(t, h, "c1")
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("the teardown returned after %v, want once the session was let go, not at a close budget", took)
	}
	if !br.Released() {
		t.Error("the teardown returned without letting its session go")
	}
	if br.isClosed() {
		t.Fatal("Setup: the close was confirmed before the engine answered")
	}
	if sb := h.bridge.mgr.get("c1"); sb != nil {
		t.Error("the closed chat still has a registered session")
	}
	close(gate)
	awaitSessionClosed(t, br)
}

// holdPrompt parks b's session/prompt until the returned release runs.
func holdPrompt(t *testing.T, b *fakeBridge) (release func()) {
	t.Helper()
	gate := make(chan struct{})
	var once sync.Once
	b.mu.Lock()
	b.blockOn = map[string]chan struct{}{marotte.MethodPrompt: gate}
	b.mu.Unlock()
	release = func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	return release
}

// recordSessionID is c1's recorded kiro-cli session id.
func recordSessionID(t *testing.T, cs *testChatStore) string {
	t.Helper()
	rec, ok := cs.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("c1's record is gone")
	}
	return rec.ACPSessionID
}

// A prompt's reply that lands after the close's teardown forgot the chat mints no lifecycle and closes the turn once.
func TestCloseTab_ALatePromptReplyAfterTheCloseMintsNothing(t *testing.T) {
	h, _, br, tab := closeTabHub(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("Setup: open session: %v", err)
	}
	release := holdPrompt(t, br)
	if code := postPrompt(t, h, "hi"); code != http.StatusOK {
		t.Fatalf("prompt = %d, want its ack", code)
	}
	awaitWire(t, br, marotte.MethodPrompt)

	if code := postCloseTab(t, h, tab); code != http.StatusOK {
		t.Fatalf("close_tab = %d, want 200", code)
	}
	awaitTeardown(t, h, "c1")
	release()
	joinInflight(t, h)

	if _, ok := h.coord.turns.lookup("c1"); ok {
		t.Error("a lifecycle exists for the closed chat after its prompt's reply")
	}
	if closed := closedBroadcasts(t, h); len(closed) != 1 {
		t.Errorf("turn_closed = %+v, want the prompt's turn closed once", closed)
	}
}

// An old prompt's reply landing after a reopen leaves the reopened chat's turn open until its own reply.
func TestCloseTab_ALatePromptReplyAfterAReopenLeavesTheNewTurnAlone(t *testing.T) {
	h, _, br, tab := closeTabHub(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("Setup: open session: %v", err)
	}
	releaseOld := holdPrompt(t, br)
	if code := postPrompt(t, h, "hi"); code != http.StatusOK {
		t.Fatalf("prompt = %d, want its ack", code)
	}
	awaitWire(t, br, marotte.MethodPrompt)
	next := newFakeBridge()
	next.sessionID = "fake-sess-reopened"
	next.chunksOnCall = map[string][]string{marotte.MethodPrompt: {"done"}}
	releaseNew := holdPrompt(t, next)
	h.engine.source.(*fakeEngineSource).setFactory(func() engineSession { return next })

	if code := postCloseTab(t, h, tab); code != http.StatusOK {
		t.Fatalf("close_tab = %d, want 200", code)
	}
	awaitTeardown(t, h, "c1")
	reopenTab(t, h, "op-reopen")
	if code := postPrompt(t, h, "again"); code != http.StatusOK {
		t.Fatalf("prompt = %d, want its ack", code)
	}
	awaitWire(t, next, marotte.MethodPrompt)
	before := len(closedBroadcasts(t, h))
	releaseOld()
	time.Sleep(50 * time.Millisecond)
	if got := closedBroadcasts(t, h); len(got) != before {
		t.Fatalf("turn_closed = %+v after the old reply, want the reopened chat's turn still open", got)
	}
	if !h.coord.turns.live("c1") {
		t.Error("the reopened chat's turn ended at the old session's reply")
	}
	releaseNew()
	for deadline := time.Now().Add(5 * time.Second); len(closedBroadcasts(t, h)) == before; {
		if time.Now().After(deadline) {
			t.Fatal("the reopened chat's turn never closed")
		}
		time.Sleep(time.Millisecond)
	}
	if got := closedBroadcasts(t, h); got[len(got)-1].Outcome != marotte.TurnOutcomeCompleted {
		t.Errorf("turn_closed = %+v, want the reopened turn completed on its own reply", got[len(got)-1])
	}
}

// A History delete during a background close runs after the close's job: its session/delete waits for the job.
func TestCloseTab_ADeleteDuringABackgroundCloseRunsAfterIt(t *testing.T) {
	h, _, br, tab := closeTabHub(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("Setup: open session: %v", err)
	}
	reached, release := parkTeardownAt(t, h, "forgotten")
	t.Cleanup(release)
	closed := make(chan int, 1)
	go func() { closed <- postCloseTab(t, h, tab) }()
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the close's teardown never ran")
	}

	deleted := make(chan int, 1)
	go func() {
		deleted <- postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdDeleteChat, ChatID: "c1"}).Code
	}()
	time.Sleep(100 * time.Millisecond)
	if slices.Contains(br.callLog(), marotte.MethodSessionDelete) {
		t.Errorf("calls = %v: the delete's session/delete ran beside the close's teardown", br.callLog())
	}
	release()
	if code := <-closed; code != http.StatusOK {
		t.Fatalf("close_tab = %d, want 200", code)
	}
	if code := <-deleted; code != http.StatusOK {
		t.Fatalf("delete_chat = %d, want 200", code)
	}
	awaitTeardown(t, h, "c1")
	if !slices.Contains(br.callLog(), marotte.MethodSessionDelete) {
		t.Errorf("calls = %v, want the delete's session/delete once the close's job returned", br.callLog())
	}
}

// A turn the close ends starts no tail: the parked model pick stays parked and no session/set_config_option is sent.
func TestCloseTab_TheTeardownsOwnTurnCloseStartsNoTail(t *testing.T) {
	h, cs, br, tab := closeTabHub(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("Setup: open session: %v", err)
	}
	release := holdPrompt(t, br)
	if code := postPrompt(t, h, "hi"); code != http.StatusOK {
		t.Fatalf("prompt = %d, want its ack", code)
	}
	awaitWire(t, br, marotte.MethodPrompt)
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.PendingModel = "next-model"
		return true
	}); err != nil {
		t.Fatalf("Setup: park a pick: %v", err)
	}

	if code := postCloseTab(t, h, tab); code != http.StatusOK {
		t.Fatalf("close_tab = %d, want 200", code)
	}
	awaitTeardown(t, h, "c1")
	release()
	joinInflight(t, h)

	if slices.Contains(br.callLog(), marotte.MethodSetConfigOption) {
		t.Errorf("calls = %v, want no model apply for a closed chat", br.callLog())
	}
	if rec, _ := cs.Get(t.Context(), "c1"); rec.PendingModel != "next-model" {
		t.Errorf("pending model = %q, want the pick still parked for the next life", rec.PendingModel)
	}
}

// A door in flight at the close ends at once: the close's job returns without waiting out the door, the prompt's turn
// closes cancelled with no frame, and the record keeps its kiro-cli session for the reopen to load, with no fresh
// session tried in its place.
func TestCloseTab_ADoorInFlightAtTheCloseEndsAtOnce(t *testing.T) {
	logs := captureLogs(t)
	h, cs, br, tab := closeTabHub(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.RecordSession("sess-keep")
		return true
	}); err != nil {
		t.Fatalf("Setup: record a session: %v", err)
	}
	gate := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})
	br.mu.Lock()
	br.startGate = gate
	doors := doorCounter(br)
	br.mu.Unlock()
	if code := postPrompt(t, h, "hi"); code != http.StatusOK {
		t.Fatalf("prompt = %d, want its ack", code)
	}
	for deadline := time.Now().Add(5 * time.Second); doors.Load() == 0; {
		if time.Now().After(deadline) {
			t.Fatal("the prompt's door never opened")
		}
		time.Sleep(time.Millisecond)
	}

	start := time.Now()
	if code := postCloseTab(t, h, tab); code != http.StatusOK {
		t.Fatalf("close_tab = %d, want 200", code)
	}
	awaitTeardown(t, h, "c1")
	if took := time.Since(start); took > time.Second {
		t.Errorf("the close's job returned after %v, want at once: the close ends the door", took)
	}
	if got := closedBroadcasts(t, h); len(got) != 1 || got[0].Outcome != marotte.TurnOutcomeCancelled {
		t.Errorf("turn_closed = %+v, want the prompt's turn cancelled", got)
	}
	if errs := eventsOfType(h, marotte.EventError); len(errs) != 0 {
		t.Errorf("error frames = %+v, want none for a chat the user closed", errs)
	}
	if got := recordSessionID(t, cs); got != "sess-keep" {
		t.Errorf("record session = %q, want sess-keep kept for the reopen", got)
	}
	joinInflight(t, h)
	if out := logs.String(); strings.Contains(out, "session/load failed, starting new") {
		t.Errorf("logs = %s, want the cut load ended, not retried as a new session", out)
	}
}

// A door generation the close overtakes before it opens its session writes nothing: its forward's attach is refused,
// no session/new or session/load reaches kiro-cli, and the record keeps its session.
func TestOpenGeneration_ARefusedAttachEndsTheDoorBeforeItWrites(t *testing.T) {
	for _, tc := range []struct {
		name    string
		session string
	}{
		{name: "a new session"},
		{name: "a load", session: "sess-keep"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, cs, br, tab := closeTabHub(t)
			t.Cleanup(func() { shutdownHub(t, h) })
			if tc.session != "" {
				if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
					c.RecordSession(tc.session)
					return true
				}); err != nil {
					t.Fatalf("Setup: record a session: %v", err)
				}
			}
			doors := doorCounter(br)
			at, resume := make(chan struct{}), make(chan struct{})
			var once sync.Once
			h.SetPreSessionOpen(func(context.Context) {
				once.Do(func() {
					close(at)
					<-resume
				})
			})
			opened := make(chan error, 1)
			go func() {
				_, err := h.coord.openReleased(context.Background(), "c1", "")
				opened <- err
			}()
			select {
			case <-at:
			case <-time.After(5 * time.Second):
				t.Fatal("the generation never reached its door")
			}
			if code := postCloseTab(t, h, tab); code != http.StatusOK {
				close(resume)
				t.Fatalf("close_tab = %d, want 200", code)
			}
			close(resume)
			if err := <-opened; !errors.Is(err, marotte.ErrChatClosedDuringCall) {
				t.Errorf("open = %v, want ErrChatClosedDuringCall", err)
			}
			awaitTeardown(t, h, "c1")
			if n := doors.Load(); n != 0 {
				t.Errorf("doors = %d, want none written for a closed chat", n)
			}
			if got := recordSessionID(t, cs); got != tc.session {
				t.Errorf("record session = %q, want %q unchanged", got, tc.session)
			}
			if sb := h.bridge.mgr.get("c1"); sb != nil {
				t.Error("the closed chat's generation left a registered session")
			}
		})
	}
}

// The commit cuts a running session/prompt, and the job waits for that call to give up before its own
// session/cancel, which precedes the session's release; the turn closes cancelled with no error frame.
func TestCloseTab_TheJobWaitsForEveryLeaseBeforeItsCancel(t *testing.T) {
	h, cs, br, tab := closeTabHub(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("Setup: open session: %v", err)
	}
	holdPrompt(t, br)
	var releasedAtCancel atomic.Int32
	br.mu.Lock()
	br.onNotify = func(method string) {
		if method != marotte.MethodCancel {
			return
		}
		if br.Released() {
			releasedAtCancel.Store(2)
		} else {
			releasedAtCancel.Store(1)
		}
	}
	br.mu.Unlock()
	if code := postPrompt(t, h, "hi"); code != http.StatusOK {
		t.Fatalf("prompt = %d, want its ack", code)
	}
	awaitWire(t, br, marotte.MethodPrompt)
	turn, ok := h.coord.PromptTurn("c1")
	if !ok {
		t.Fatal("Setup: the prompt has no turn")
	}

	if code := postCloseTab(t, h, tab); code != http.StatusOK {
		t.Fatalf("close_tab = %d, want 200", code)
	}
	awaitTeardown(t, h, "c1")
	joinInflight(t, h)

	wire := br.wireLog()
	cancelAt := slices.Index(wire, marotte.MethodCancel)
	br.mu.Lock()
	abandonedAt, abandoned := br.abandonedAt[marotte.MethodPrompt]
	br.mu.Unlock()
	switch {
	case cancelAt < 0:
		t.Errorf("wire = %v, want the teardown's session/cancel", wire)
	case !abandoned:
		t.Error("the running session/prompt was never cut by the close")
	case abandonedAt > cancelAt:
		t.Errorf("session/prompt gave up after %d frames, session/cancel was frame %d: the job cancelled under a live lease",
			abandonedAt, cancelAt)
	}
	if releasedAtCancel.Load() == 2 {
		t.Error("session/cancel was written after the session was released")
	}
	if c, closed := closeOfTurn(t, logOf(t, cs, "c1"), turn.ID()); !closed || c.Outcome != marotte.TurnOutcomeCancelled {
		t.Errorf("turn_close = %+v (closed %v), want cancelled", c, closed)
	}
	if failed := promptFailures(h); len(failed) != 0 {
		t.Errorf("prompt_failed = %q, want none", failed)
	}
}

// A report that the session moved its effort level, arriving after the close committed and before the teardown
// let the session go, repairs nothing: the ended life refuses the heal's call.
func TestEffortHeal_AfterTheCommitSendsNothing(t *testing.T) {
	h, cs, br, tab := closeTabHub(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Effort = "max"
		return true
	}); err != nil {
		t.Fatalf("Setup: seed effort: %v", err)
	}
	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("Setup: open session: %v", err)
	}
	reached, release := parkTeardownAt(t, h, "begun")
	t.Cleanup(release)

	closed := make(chan int, 1)
	go func() { closed <- postCloseTab(t, h, tab) }()
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the teardown never began")
	}
	h.handleSessionUpdate(t.Context(), "c1", configOptionFrame(t, "high", "", false))
	release()
	if code := <-closed; code != http.StatusOK {
		t.Fatalf("close_tab = %d, want 200", code)
	}
	awaitTeardown(t, h, "c1")
	joinInflight(t, h)

	if got := br.lastEffort(); got != "" {
		t.Errorf("effort applied = %q after the close committed, want nothing sent", got)
	}
}

// A run the chat launched keeps its verbs on the chat's resident session after the close committed, since run work
// outlives the chat's life, and moves to the engine lane once the teardown released that session.
func TestRunVerb_ACloseLeavesTheResidentSessionToTheRunUntilItsRelease(t *testing.T) {
	h, _, lane, tab := closeTabHub(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	chat := newFakeBridge()
	fakeSourceOf(t, h).setFactory(func() engineSession { return chat })
	sb, err := h.coord.openReleased(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("Setup: open session: %v", err)
	}
	leaseRun(t, h, runlease.Lease{
		WorkflowID: "wf_1", ChatID: "c1", ParentSessionID: string(sb.SessionID()), Origin: runlease.OriginAgent,
	})
	reached, release := parkTeardownAt(t, h, "begun")
	t.Cleanup(release)
	closed := make(chan int, 1)
	go func() { closed <- postCloseTab(t, h, tab) }()
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the teardown never began")
	}
	verb := func() {
		params := map[string]any{keyWorkflowID: "wf_1"}
		if err := h.runs.stopVerb(t.Context(), "wf_1", methodKiroWorkflowCancel, params); err != nil {
			t.Errorf("run verb: %v", err)
		}
	}

	onChat, onLane := countOf(chat.callLog(), methodKiroWorkflowCancel), countOf(lane.callLog(), methodKiroWorkflowCancel)
	verb()
	if got := countOf(chat.callLog(), methodKiroWorkflowCancel) - onChat; got != 1 {
		t.Errorf("verbs on the resident session after the commit = %d, want 1", got)
	}
	if got := countOf(lane.callLog(), methodKiroWorkflowCancel) - onLane; got != 0 {
		t.Errorf("verbs on the engine lane before the release = %d, want 0", got)
	}
	release()
	if code := <-closed; code != http.StatusOK {
		t.Fatalf("close_tab = %d, want 200", code)
	}
	awaitTeardown(t, h, "c1")

	onChat, onLane = countOf(chat.callLog(), methodKiroWorkflowCancel), countOf(lane.callLog(), methodKiroWorkflowCancel)
	verb()
	if got := countOf(chat.callLog(), methodKiroWorkflowCancel) - onChat; got != 0 {
		t.Errorf("verbs on the released session = %d, want 0", got)
	}
	if got := countOf(lane.callLog(), methodKiroWorkflowCancel) - onLane; got != 1 {
		t.Errorf("verbs on the engine lane after the release = %d, want 1", got)
	}
}

// A prompt past StartTurn and parked at its send gate when the close lands never sends: its turn closes cancelled
// with no frame.
func TestCloseTab_APromptBetweenStartTurnAndItsSendEndsCancelled(t *testing.T) {
	h, _, br, tab := closeTabHub(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("Setup: open session: %v", err)
	}
	atGate, resume := make(chan struct{}), make(chan struct{})
	// The hook also runs before every claim; only the first caller, the prompt's send gate, parks.
	var parked atomic.Bool
	h.coord.turns.claimHook = func() {
		if parked.CompareAndSwap(false, true) {
			close(atGate)
			<-resume
		}
	}
	if code := postPrompt(t, h, "hi"); code != http.StatusOK {
		t.Fatalf("prompt = %d, want its ack", code)
	}
	select {
	case <-atGate:
	case <-time.After(5 * time.Second):
		t.Fatal("the prompt never reached its send gate")
	}

	if code := postCloseTab(t, h, tab); code != http.StatusOK {
		t.Fatalf("close_tab = %d, want 200", code)
	}
	awaitTeardown(t, h, "c1")
	close(resume)
	joinInflight(t, h)

	if slices.Contains(br.wireLog(), marotte.MethodPrompt) {
		t.Errorf("wire = %v, want no session/prompt for the closed chat", br.wireLog())
	}
	if closed := closedBroadcasts(t, h); len(closed) != 1 || closed[0].Outcome != marotte.TurnOutcomeCancelled {
		t.Errorf("turn_closed = %+v, want the prompt's one turn cancelled", closed)
	}
	if errs := eventsOfType(h, marotte.EventError); len(errs) != 0 {
		t.Errorf("error frames = %+v, want none for a chat the user closed", errs)
	}
}
