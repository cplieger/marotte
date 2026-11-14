package agent

// The notify door's wakes against the chat's real run list and steer queue, the engine faked at the bridge.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

const grelotWake = "1 new; run grelot inbox"

// notifyWake delivers a grelot wake through the door's own deliver (Runtime.Notifier binds it), on its own goroutine.
func notifyWake(t *testing.T, h *Runtime, chatID marotte.ChatID) <-chan command.NotifyDelivery {
	t.Helper()
	got := make(chan command.NotifyDelivery, 1)
	go func() {
		verb, err := h.dispatcher.Notify(t.Context(), chatID, grelotWake, &marotte.Caller{Label: "grelot"})
		if err != nil {
			t.Errorf("Notify = %v, want accepted", err)
		}
		got <- verb
	}()
	return got
}

func awaitDelivery(t *testing.T, got <-chan command.NotifyDelivery) command.NotifyDelivery {
	t.Helper()
	select {
	case v := <-got:
		return v
	case <-time.After(10 * time.Second):
		t.Fatal("the wake's delivery never answered")
	}
	return ""
}

// listOf is the chat's live run list as the header draws it.
func listOf(h *Runtime, chatID marotte.ChatID) []marotte.RunEntry {
	lc, ok := h.coord.turns.lookup(chatID)
	if !ok {
		return nil
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.run.project()
}

func fromGrelot(c *marotte.Caller) bool { return c != nil && c.Label == "grelot" }

// A wake arriving while the user's row waits behind the running turn goes behind that row as its own row, labelled
// with its caller, and answers queued: it is never steered into the running turn ahead of the user's message.
func TestNotify_AWakeQueuesBehindTheUsersWaitingRow(t *testing.T) {
	s, _, br := steerOpsHub(t)
	id, _ := s.h.stagePromptTurn(t, "c1")
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
	queueTextDuring(t, s.h, "m-user", "then run the tests")

	got := awaitDelivery(t, notifyWake(t, s.h, "c1"))

	if steers := slices.Collect(func(yield func(string) bool) {
		for _, m := range br.callLog() {
			if m == marotte.MethodSessionSteer && !yield(m) {
				return
			}
		}
	}); len(steers) != 0 {
		t.Errorf("the wake reached session/steer (%d calls) while the user's row waited", len(steers))
	}
	if got != "queued" {
		t.Errorf("delivery = %q, want queued: held behind the user's row", got)
	}
	var rows []string
	for _, e := range listOf(s.h, "c1") {
		if e.Row == nil || e.State.Live() {
			continue
		}
		name := e.ID
		if fromGrelot(e.Row.Caller) {
			name = "grelot:" + e.Row.Text
		}
		rows = append(rows, name)
	}
	if want := []string{"m-user", "grelot:" + grelotWake}; !slices.Equal(rows, want) {
		t.Errorf("waiting rows = %v, want %v: the wake shows as a row behind the user's", rows, want)
	}
}

// wakeAt is where in a send-now's stop a wake lands.
type wakeAt int

const (
	wakeBeforeSendNow wakeAt = iota
	wakeBeforeTurnEnd
	wakeAfterTurnEnd
	wakeAfterRelease
)

// Send now on the only steer row stops the turn so that message takes the next turn; a wake arriving before the press
// or in the gap the stop opens goes after it, never in its place or folded into it.
func TestNotify_SendNowOnTheOnlySteerTakesTheNextTurnOverAWake(t *testing.T) {
	for _, tc := range []struct {
		name string
		at   wakeAt
	}{
		{"the wake lands before the press", wakeBeforeSendNow},
		{"the wake lands before the turn end", wakeBeforeTurnEnd},
		{"the wake lands after the turn end", wakeAfterTurnEnd},
		{"the wake lands after the turn is released", wakeAfterRelease},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, cs, br := steerOpsHub(t)
			s.recs.note = s.h.coord.recordSteer
			k := kas320On(s.h, "c1", br)
			id, _ := s.h.stagePromptTurn(t, "c1")
			s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
			s.userSteer(t, "steer-a", "my message")
			wake := func(at wakeAt) {
				if tc.at == at {
					awaitDelivery(t, notifyWake(t, s.h, "c1"))
				}
			}

			// The send-now turn's prompt stays unanswered, so that turn is still running when the test reads the list.
			userTurn := make(chan struct{})
			t.Cleanup(func() { close(userTurn) })
			br.mu.Lock()
			br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: userTurn}
			br.mu.Unlock()

			wake(wakeBeforeSendNow)
			rec := postCmd(t, s.h, marotte.ClientCommand{
				Type: marotte.CmdCancel, ChatID: "c1", Payload: json.RawMessage(`{"lead":"steer-a"}`),
			})
			if rec.Code != http.StatusOK || len(br.notified(marotte.MethodCancel)) == 0 {
				t.Fatalf("Setup: cancel = %d %s, cancels sent %v", rec.Code, rec.Body.String(), br.notified(marotte.MethodCancel))
			}
			wake(wakeBeforeTurnEnd)
			s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnEndMsg("cancelled"))
			wake(wakeAfterTurnEnd)
			s.h.coord.releaseTurn("c1", id)
			wake(wakeAfterRelease)

			var next *marotte.EntryPrompt
			waitFor(t, func() bool {
				opens := opensOf(t, logOf(t, cs, "c1"))
				if len(opens) < 2 {
					return false
				}
				next = opens[1].Prompt
				return true
			})
			switch {
			case next == nil:
				t.Fatal("the turn after the stop opened with no prompt")
			case fromGrelot(next.Caller):
				t.Errorf("next turn = the wake %q from %s, want the user's send-now message", next.Text, next.Caller.Label)
			case next.Text != "my message" || !slices.Equal(next.Resends, []string{"steer-a"}):
				t.Errorf("next turn = %q resending %v (caller %+v), want the user's steer-a alone", next.Text, next.Resends, next.Caller)
			}
			if got := k.steered(); slices.Contains(got, grelotWake) {
				t.Errorf("steers sent = %q, want no wake: it is never steered beside or into the send-now turn", got)
			}
			var waiting []string
			for _, e := range listOf(s.h, "c1") {
				if e.Row != nil && !e.State.Live() && fromGrelot(e.Row.Caller) {
					waiting = append(waiting, e.Row.Text)
				}
			}
			if !slices.Equal(waiting, []string{grelotWake}) {
				t.Errorf("waiting grelot rows = %q, want the wake once, behind the send-now turn", waiting)
			}
		})
	}
}

// sendAsUser posts the composer's Send of text under message id id into chatID, on its own goroutine.
func sendAsUser(h *Runtime, chatID marotte.ChatID, id, text string) <-chan *httptest.ResponseRecorder {
	payload, _ := json.Marshal(map[string]string{"text": text, "message_id": id})
	return postAsync(h, marotte.ClientCommand{Type: marotte.CmdPrompt, ChatID: chatID, Payload: payload})
}

func awaitAnswer(t *testing.T, got <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case rec := <-got:
		return rec
	case <-time.After(10 * time.Second):
		t.Fatal("the user's send never answered")
	}
	return nil
}

// reachesState reports whether id stands in state on chatID's live list within 5s.
func reachesState(h *Runtime, chatID marotte.ChatID, id string, state marotte.RunEntryState) bool {
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if stateOf(h, chatID, id) == state {
			return true
		}
	}
	return false
}

// placeWake puts a grelot wake on chatID's live list as the notify door's send, and lets the list deliver.
func placeWake(t *testing.T, h *Runtime, chatID marotte.ChatID, id string) {
	t.Helper()
	placeWakeFrom(t, h, chatID, id, "grelot")
}

// placeWakeFrom is placeWake for a wake whose source is label.
func placeWakeFrom(t *testing.T, h *Runtime, chatID marotte.ChatID, id, label string) {
	t.Helper()
	lc := liveLife(t, h, chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	e := &runEntry{
		id: id, kind: marotte.RunKindRow, origin: marotte.RunFromNotify,
		row: &marotte.QueuedPrompt{ID: id, Text: grelotWake, Caller: &marotte.Caller{Label: label}},
	}
	f := lc.orderFactsLocked()
	if a, _ := lc.run.doorSend(e, &f, lc.stamp, false); a.refused != takeGranted {
		t.Fatalf("Setup: the wake was refused (%d)", a.refused)
	}
	lc.wakeLocked()
}

func liveLife(t *testing.T, h *Runtime, chatID marotte.ChatID) *chatLifecycle {
	t.Helper()
	lc, ok := h.coord.turns.lookup(chatID)
	if !ok {
		t.Fatalf("Setup: chat %s has no live life", chatID)
	}
	return lc
}

// stateOf is id's state on chatID's live list, "" once it left.
func stateOf(h *Runtime, chatID marotte.ChatID, id string) marotte.RunEntryState {
	for _, e := range listOf(h, chatID) {
		if e.ID == id {
			return e.State
		}
	}
	return ""
}

// A wake arriving while the user's steer is in kiro-cli's buffer waits; once the agent reads the user's steer, the
// wake is steered into the running turn (nothing of the user's waits any more).
func TestNotify_AWakeIsSteeredOnceTheUsersSteerIsRead(t *testing.T) {
	s, _, br := steerOpsHub(t)
	k := kas320On(s.h, "c1", br)
	id, _ := s.h.stagePromptTurn(t, "c1")
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
	sent := sendAsUser(s.h, "c1", "m-u", "check the logs")
	if !reachesState(s.h, "c1", "m-u", marotte.RunInKAS) {
		t.Fatal("the user's send never stood on the list as a steer kiro-cli holds")
	}
	if got := ackAs(t, awaitAnswer(t, sent)); got != "steered" {
		t.Errorf("the user's send answered %q, want steered into the running turn", got)
	}

	placeWake(t, s.h, "c1", "n-1")

	joinInflight(t, s.h)
	if got := k.steered(); !slices.Equal(got, []string{"check the logs"}) {
		t.Errorf("steers sent = %q, want the user's alone while it is unread", got)
	}
	if got := stateOf(s.h, "c1", "n-1"); got != marotte.RunPending {
		t.Errorf("wake state = %q, want pending behind the user's steer", got)
	}

	s.h.translateACPEvent("c1", s.h.originOf("c1"), injectedMsg("steer-m-u", "check the logs"))

	waitFor(t, func() bool { return len(k.steered()) == 2 })
	if got := k.steered(); got[1] != grelotWake {
		t.Errorf("second steer = %q, want the wake", got[1])
	}
	waitFor(t, func() bool { return stateOf(s.h, "c1", "n-1") == marotte.RunInKAS })
	if got := stateOf(s.h, "c1", "m-u"); got != "" {
		t.Errorf("the read steer is still listed, %q", got)
	}
}

// A user's Send bound for the running turn as a steer, whose turn ends before the steer reaches kiro-cli, keeps its
// place on the list: a wake placed meanwhile goes behind it, the user's words take the next turn, and the wake waits
// until that turn's prompt is written.
func TestNotify_AUserSteerRefusedForNoTurnTakesTheNextTurnBeforeAWake(t *testing.T) {
	s, cs, br := steerOpsHub(t)
	id, _ := s.h.stagePromptTurn(t, "c1")
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
	unlock, err := s.h.steerQueue.LockSteerOps(t.Context(), "c1")
	if err != nil {
		t.Fatalf("Setup: steer lock: %v", err)
	}
	var once sync.Once
	release := func() { once.Do(unlock) }
	t.Cleanup(release)
	sent := sendAsUser(s.h, "c1", "m-u", "check the logs")
	if !reachesState(s.h, "c1", "m-u", marotte.RunOffered) {
		t.Fatal("the user's send is not on the list while its steer waits for the steer lock")
	}
	placeWake(t, s.h, "c1", "n-1")

	s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnEndMsg("end_turn"))
	s.h.coord.releaseTurn("c1", id)
	release()
	// Queued while the ended turn's close completes, started once it has: never steered, since no turn took it.
	if got := ackAs(t, awaitAnswer(t, sent)); got != "queued" && got != "started" {
		t.Errorf("the user's send answered %q, want queued or started: no turn took its steer", got)
	}

	var next *marotte.EntryPrompt
	waitFor(t, func() bool {
		opens := opensOf(t, logOf(t, cs, "c1"))
		if len(opens) < 2 {
			return false
		}
		next = opens[1].Prompt
		return true
	})
	switch {
	case next == nil:
		t.Fatal("the next turn opened with no prompt")
	case next.Text != "check the logs" || next.Caller != nil:
		t.Errorf("next turn = %q (caller %+v), want the user's refused steer", next.Text, next.Caller)
	}
	// The staged turn wrote no session/prompt, so the first one is the user's turn's: the wake may be steered only
	// into that turn, once its prompt is written.
	calls := br.callLog()
	if steer := slices.Index(calls, marotte.MethodSessionSteer); steer >= 0 && steer < slices.Index(calls, marotte.MethodPrompt) {
		t.Errorf("calls = %v, want no steer before the user's turn's prompt", calls)
	}
	if got := stateOf(s.h, "c1", "n-1"); got == "" {
		t.Error("the wake left the list, want it waiting or steered into the user's turn")
	}
}

// A wake kiro-cli 3.2.0 drops at a turn boundary ({"queued":false,"dropped":"epoch_changed"}) is not steered again
// into that turn; it waits as a row and takes a turn of its own once the turn closes.
func TestNotify_AWakeKiroCLIDropsWaitsForATurnOfItsOwn(t *testing.T) {
	s, cs, br := steerOpsHub(t)
	k := kas320On(s.h, "c1", br)
	id, _ := s.h.stagePromptTurn(t, "c1")
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
	k.boundaryDuringNextSteer()

	placeWake(t, s.h, "c1", "n-1")

	waitFor(t, func() bool { return len(k.steered()) == 1 && stateOf(s.h, "c1", "n-1") == marotte.RunPending })
	joinInflight(t, s.h)
	if f := s.lastFrame("steer-n-1"); f.State != marotte.SteerRowRemoved {
		t.Errorf("the wake's dock frame = %q, want removed: it waits as a row, not as a steer", f.State)
	}
	if got := k.steered(); len(got) != 1 {
		t.Errorf("steers sent = %q, want the one kiro-cli dropped, not retried into the same turn", got)
	}
	if got := k.holds(); len(got) != 0 {
		t.Errorf("KAS holds %v, want nothing", got)
	}

	s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnEndMsg("end_turn"))
	s.h.coord.releaseTurn("c1", id)

	var next *marotte.EntryPrompt
	waitFor(t, func() bool {
		opens := opensOf(t, logOf(t, cs, "c1"))
		if len(opens) < 2 {
			return false
		}
		next = opens[1].Prompt
		return true
	})
	if next == nil || next.Text != grelotWake || !fromGrelot(next.Caller) {
		t.Errorf("next turn = %+v, want the wake from grelot", next)
	}
}

// nextPrompt waits for chatID's n-th turn open (0 the first) and answers its prompt.
func nextPrompt(t *testing.T, cs *testChatStore, chatID marotte.ChatID, n int) *marotte.EntryPrompt {
	t.Helper()
	var p *marotte.EntryPrompt
	waitFor(t, func() bool {
		opens := opensOf(t, logOf(t, cs, chatID))
		if len(opens) <= n {
			return false
		}
		p = opens[n].Prompt
		return true
	})
	if p == nil {
		t.Fatalf("turn open %d carries no prompt", n)
	}
	return p
}

// grelotWaits reports whether the wake still stands on chatID's list under grelot's name, not delivered as a prompt.
func grelotWaits(h *Runtime, chatID marotte.ChatID, id string) bool {
	for _, e := range listOf(h, chatID) {
		if e.ID == id && e.Row != nil && fromGrelot(e.Row.Caller) && !e.State.Live() {
			return true
		}
	}
	return false
}

// A wake kiro-cli held unread when the turn ended goes behind the row the user queued meanwhile: the user's row takes
// the next turn, alone.
func TestNotify_AWakeBackUnreadGoesBehindTheUsersQueuedRow(t *testing.T) {
	s, cs, br := steerOpsHub(t)
	k := kas320On(s.h, "c1", br)
	id, _ := s.h.stagePromptTurn(t, "c1")
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
	placeWake(t, s.h, "c1", "n-1")
	if !reachesState(s.h, "c1", "n-1", marotte.RunInKAS) {
		t.Fatal("Setup: the wake was never steered into the running turn")
	}
	queueTextDuring(t, s.h, "m-user", "then run the tests")

	s.h.translateACPEvent("c1", s.h.originOf("c1"), newChunkMsg("done"))
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnEndMsg("end_turn"))
	s.h.coord.releaseTurn("c1", id)

	next := nextPrompt(t, cs, "c1", 1)
	if next.Text != "then run the tests" || next.Caller != nil {
		t.Errorf("next turn = %q (caller %+v), want the user's queued row alone", next.Text, next.Caller)
	}
	if got := k.steered(); !slices.Equal(got, []string{grelotWake}) {
		t.Errorf("steers sent = %q, want the wake once", got)
	}
}

// A user's Send while a wake's turn starts is the user's own steer into that turn, never carried in the wake's prompt.
func TestNotify_AUserSendDuringAWakesTurnIsSteeredUnderTheUsersName(t *testing.T) {
	s, cs, br := steerOpsHub(t)
	k := kas320On(s.h, "c1", br)
	id, _ := s.h.stagePromptTurn(t, "c1")
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnEndMsg("end_turn"))
	s.h.coord.releaseTurn("c1", id)
	wakeTurn := make(chan struct{})
	t.Cleanup(func() { close(wakeTurn) })
	br.mu.Lock()
	br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: wakeTurn}
	br.mu.Unlock()

	placeWake(t, s.h, "c1", "n-1")
	wake := nextPrompt(t, cs, "c1", 1)
	if !reachesState(s.h, "c1", "n-1", marotte.RunRunning) {
		t.Fatal("Setup: the wake's turn never ran")
	}
	sent := sendAsUser(s.h, "c1", "m-u", "check the logs")

	if got := ackAs(t, awaitAnswer(t, sent)); got != "steered" {
		t.Errorf("the user's send answered %q, want steered into the wake's turn", got)
	}
	if wake.Text != grelotWake || !fromGrelot(wake.Caller) || len(wake.Resends) != 0 {
		t.Errorf("the wake's turn = %q (caller %+v) resending %v, want the wake alone", wake.Text, wake.Caller, wake.Resends)
	}
	if got := k.steered(); !slices.Equal(got, []string{"check the logs"}) {
		t.Errorf("steers sent = %q, want the user's words as their own steer", got)
	}
}

// The user's steers a turn left unread go out together as the next prompt under the user's name, and a wake held
// beside them is never part of it.
func TestNotify_TheUsersUnreadSteersJoinWithoutTheWake(t *testing.T) {
	s, cs, br := steerOpsHub(t)
	k := kas320On(s.h, "c1", br)
	id, _ := s.h.stagePromptTurn(t, "c1")
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
	placeWake(t, s.h, "c1", "n-1")
	if !reachesState(s.h, "c1", "n-1", marotte.RunInKAS) {
		t.Fatal("Setup: the wake was never steered into the running turn")
	}
	for _, m := range []struct{ id, text string }{{"m-u1", "first"}, {"m-u2", "second"}} {
		if got := ackAs(t, awaitAnswer(t, sendAsUser(s.h, "c1", m.id, m.text))); got != "steered" {
			t.Fatalf("Setup: %s answered %q, want steered", m.id, got)
		}
	}
	userTurn := make(chan struct{})
	t.Cleanup(func() { close(userTurn) })
	br.mu.Lock()
	br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: userTurn}
	br.mu.Unlock()

	s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnEndMsg("end_turn"))
	s.h.coord.releaseTurn("c1", id)

	next := nextPrompt(t, cs, "c1", 1)
	if next.Text != "first\n\nsecond" || next.Caller != nil {
		t.Errorf("next turn = %q (caller %+v), want the user's two steers joined under the user's name", next.Text, next.Caller)
	}
	if next.Label != "" {
		t.Errorf("next turn label = %q, want none: the words are the user's, so they show as written", next.Label)
	}
	if want := []string{"steer-m-u1", "steer-m-u2"}; !slices.Equal(next.Resends, want) {
		t.Errorf("next turn resends %v, want %v", next.Resends, want)
	}
	if got := k.steered(); len(got) == 0 || got[0] != grelotWake {
		t.Errorf("steers sent = %q, want the wake first", got)
	}
	if !grelotWaits(s.h, "c1", "n-1") {
		t.Errorf("list = %+v, want the wake still waiting under grelot's name", listOf(s.h, "c1"))
	}
}

// The steer command is a send of the user's like the composer's: a wake arriving while the user's steer is in
// kiro-cli's buffer waits on the list, and is steered once the agent read the user's.
func TestNotify_AWakeWaitsBehindTheUsersSteerCommand(t *testing.T) {
	s, _, br := steerOpsHub(t)
	k := kas320On(s.h, "c1", br)
	id, _ := s.h.stagePromptTurn(t, "c1")
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
	if code := postSteer(t, s.h, "m-u", "check the logs"); code != http.StatusOK {
		t.Fatalf("Setup: steer = %d, want 200", code)
	}

	placeWake(t, s.h, "c1", "n-1")

	joinInflight(t, s.h)
	if got := k.steered(); !slices.Equal(got, []string{"check the logs"}) {
		t.Errorf("steers sent = %q, want the user's alone while it is unread", got)
	}
	if got := stateOf(s.h, "c1", "m-u"); got != marotte.RunInKAS {
		t.Errorf("the user's steer stands %q on the list, want in_kas", got)
	}
	if got := stateOf(s.h, "c1", "n-1"); got != marotte.RunPending {
		t.Errorf("wake state = %q, want pending behind the user's steer", got)
	}

	s.h.translateACPEvent("c1", s.h.originOf("c1"), injectedMsg("steer-m-u", "check the logs"))

	waitFor(t, func() bool { return len(k.steered()) == 2 })
	if got := k.steered(); got[1] != grelotWake {
		t.Errorf("second steer = %q, want the wake", got[1])
	}
}

// A steer command with no running turn to take it is refused no_turn and leaves nothing on the list for a later turn.
func TestNotify_ASteerCommandWithNothingRunningPlacesNothing(t *testing.T) {
	s, _, _ := steerOpsHub(t)
	if code := postSteer(t, s.h, "m-u", "check the logs"); code != http.StatusConflict {
		t.Fatalf("steer = %d, want 409 no_turn", code)
	}
	if got := stateOf(s.h, "c1", "m-u"); got != "" {
		t.Errorf("the refused steer stands %q on the list, want nothing", got)
	}
}

// wakeWith delivers text from grelot through the door's own deliver and answers its outcome.
func wakeWith(t *testing.T, h *Runtime, chatID marotte.ChatID, text string) (command.NotifyDelivery, error) {
	t.Helper()
	return h.dispatcher.Notify(t.Context(), chatID, text, &marotte.Caller{Label: "grelot"})
}

// grelotRows are the ids and texts of the rows waiting on chatID's list under grelot's name.
func grelotRows(h *Runtime, chatID marotte.ChatID) (ids, texts []string) {
	for _, e := range listOf(h, chatID) {
		if e.Row != nil && fromGrelot(e.Row.Caller) && !e.State.Live() {
			ids, texts = append(ids, e.ID), append(texts, e.Row.Text)
		}
	}
	return ids, texts
}

// A wake into an idle chat opens its turn under grelot's name and answers prompt; the draft cap, which holds what
// waits on the list, does not apply to it.
func TestNotify_AWakeIntoAnIdleChatOpensItsTurn(t *testing.T) {
	for name, text := range map[string]string{
		"a short wake":              grelotWake,
		"a wake past the draft cap": strings.Repeat("x", marotte.MaxDraftBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			s, cs, br := steerOpsHub(t)
			id, _ := s.h.stagePromptTurn(t, "c1")
			s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
			s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnEndMsg("end_turn"))
			s.h.coord.releaseTurn("c1", id)
			waitFor(t, func() bool { return len(listOf(s.h, "c1")) == 0 })
			wakeTurn := make(chan struct{})
			t.Cleanup(func() { close(wakeTurn) })
			br.mu.Lock()
			br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: wakeTurn}
			br.mu.Unlock()

			got, err := wakeWith(t, s.h, "c1", text)

			if err != nil || got != command.NotifyPrompt {
				t.Errorf("delivery = %q, %v; want prompt", got, err)
			}
			if p := nextPrompt(t, cs, "c1", 1); p.Text != text || !fromGrelot(p.Caller) {
				t.Errorf("the turn opened with %d bytes (caller %+v), want the wake from grelot", len(p.Text), p.Caller)
			}
		})
	}
}

// A later wake from the same caller while its earlier one waits replaces that row's text and keeps its id and its
// place behind the user's row: one waiting row per caller.
func TestNotify_ALaterWakeReplacesTheWaitingOnesTextInPlace(t *testing.T) {
	s, _, br := steerOpsHub(t)
	id, _ := s.h.stagePromptTurn(t, "c1")
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
	queueTextDuring(t, s.h, "m-user", "then run the tests")
	if got, err := wakeWith(t, s.h, "c1", "1 new; run grelot inbox"); err != nil || got != command.NotifyQueued {
		t.Fatalf("Setup: first wake = %q, %v; want queued", got, err)
	}
	first, _ := grelotRows(s.h, "c1")

	got, err := wakeWith(t, s.h, "c1", "2 new; run grelot inbox")

	if err != nil || got != command.NotifyQueued {
		t.Errorf("second wake = %q, %v; want queued", got, err)
	}
	ids, texts := grelotRows(s.h, "c1")
	if !slices.Equal(texts, []string{"2 new; run grelot inbox"}) || !slices.Equal(ids, first) {
		t.Errorf("grelot rows = %v %q, want the one row %v carrying the later text", ids, texts, first)
	}
	var order []string
	for _, e := range listOf(s.h, "c1") {
		if e.Row != nil && !e.State.Live() {
			order = append(order, e.ID)
		}
	}
	if want := append([]string{"m-user"}, first...); !slices.Equal(order, want) {
		t.Errorf("waiting rows = %v, want %v: the replaced row keeps its place", order, want)
	}
	if n := countCalls(br, marotte.MethodSessionSteer); n != 0 {
		t.Errorf("session/steer calls = %d, want 0 while the user's row waits", n)
	}
}

// A wake into a running turn with nothing of the user's waiting is steered and answers steer; a later wake while
// kiro-cli still holds it unread adds nothing and answers steer, since the agent reads the earlier one anyway.
func TestNotify_AWakeBesideOneTheAgentHoldsAddsNothing(t *testing.T) {
	s, _, br := steerOpsHub(t)
	k := kas320On(s.h, "c1", br)
	id, _ := s.h.stagePromptTurn(t, "c1")
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
	if got, err := wakeWith(t, s.h, "c1", "1 new; run grelot inbox"); err != nil || got != command.NotifySteer {
		t.Fatalf("Setup: first wake = %q, %v; want steer", got, err)
	}

	got, err := wakeWith(t, s.h, "c1", "2 new; run grelot inbox")

	if err != nil || got != command.NotifySteer {
		t.Errorf("second wake = %q, %v; want steer", got, err)
	}
	joinInflight(t, s.h)
	if got := k.steered(); !slices.Equal(got, []string{"1 new; run grelot inbox"}) {
		t.Errorf("steers sent = %q, want the first wake once", got)
	}
	var grelot int
	for _, e := range listOf(s.h, "c1") {
		if e.Row != nil && fromGrelot(e.Row.Caller) {
			grelot++
		}
	}
	if grelot != 1 {
		t.Errorf("list = %+v, want one grelot entry", listOf(s.h, "c1"))
	}
}

// A wake past the draft cap that would wait behind a running turn is refused as one to retry later, and places
// nothing; a waiting wake keeps its text when a later one is refused so.
func TestNotify_AWakePastTheDraftCapBehindARunningTurnIsRetriedLater(t *testing.T) {
	s, _, _ := steerOpsHub(t)
	id, _ := s.h.stagePromptTurn(t, "c1")
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
	queueTextDuring(t, s.h, "m-user", "then run the tests")
	big := strings.Repeat("x", marotte.MaxDraftBytes+1)

	t.Run("a new wake", func(t *testing.T) {
		_, err := wakeWith(t, s.h, "c1", big)
		if got := command.ClassifyNotifyError(err); got != command.NotifyRetryLater {
			t.Errorf("error = %v (class %v), want retry later", err, got)
		}
		if ids, _ := grelotRows(s.h, "c1"); len(ids) != 0 {
			t.Errorf("grelot rows = %v, want none", ids)
		}
	})
	t.Run("a wake replacing a waiting one", func(t *testing.T) {
		if got, err := wakeWith(t, s.h, "c1", grelotWake); err != nil || got != command.NotifyQueued {
			t.Fatalf("Setup: wake = %q, %v; want queued", got, err)
		}
		_, err := wakeWith(t, s.h, "c1", big)
		if got := command.ClassifyNotifyError(err); got != command.NotifyRetryLater {
			t.Errorf("error = %v (class %v), want retry later", err, got)
		}
		if _, texts := grelotRows(s.h, "c1"); !slices.Equal(texts, []string{grelotWake}) {
			t.Errorf("grelot rows = %q, want the waiting wake's text kept", texts)
		}
	})
}

// The draft cap holds a wake that would wait on the list, not one the running turn takes as a steer at once.
func TestNotify_AWakePastTheDraftCapIsSteeredWhenNothingWaits(t *testing.T) {
	s, _, br := steerOpsHub(t)
	k := kas320On(s.h, "c1", br)
	id, _ := s.h.stagePromptTurn(t, "c1")
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
	big := strings.Repeat("x", marotte.MaxDraftBytes+1)

	got, err := wakeWith(t, s.h, "c1", big)

	if err != nil || got != command.NotifySteer {
		t.Errorf("delivery = %q, %v; want steer", got, err)
	}
	if sent := k.steered(); len(sent) != 1 || sent[0] != big {
		t.Errorf("steers sent = %d, want the wake once", len(sent))
	}
}
