package agent

// A notify door's wake against the user's Stop and send-now, on the chat's real run list and steer queue, kiro-cli
// 3.2.0's steering faked at the bridge.

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// listedRows is chatID's waiting rows in list order, a wake named "grelot", each with its state.
func listedRows(h *Runtime, chatID marotte.ChatID) []string {
	var out []string
	for _, e := range listOf(h, chatID) {
		if e.Row == nil || e.State.Live() {
			continue
		}
		name := e.ID
		if fromGrelot(e.Row.Caller) {
			name = "grelot"
		}
		out = append(out, name+":"+string(e.State))
	}
	return out
}

// idleAfter waits until turnID's turn left chatID's list and every launch it set off ran.
func idleAfter(t *testing.T, h *Runtime, chatID marotte.ChatID, turnID string) {
	t.Helper()
	waitFor(t, func() bool {
		for _, e := range listOf(h, chatID) {
			if e.Turn == turnID {
				return false
			}
		}
		return true
	})
	joinInflight(t, h)
}

func plainStop(t *testing.T, h *Runtime) {
	t.Helper()
	if rec := postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1"}); rec.Code != http.StatusOK {
		t.Fatalf("Setup: cancel = %d %s", rec.Code, rec.Body.String())
	}
}

// A plain Stop pauses the row the user queued and the wake behind it: nothing opens on its own, and the user's next
// action decides what goes: the row sent now opens the next turn, and discarding it leaves the wake paused.
func TestNotify_AWakeBehindTheUsersRowPausedByAStopWaitsForThatRow(t *testing.T) {
	for _, tc := range []struct {
		name string
		act  marotte.ClientCommand
		// want is the next turn's words: the user's row, or the wake once the user discarded that row.
		// want is the next turn's prompt, "" for no turn.
		want string
	}{
		{
			name: "the user sends the row now",
			act:  marotte.ClientCommand{Type: marotte.CmdSendQueuedNow, ChatID: "c1", Payload: json.RawMessage(`{"message_id":"m-u1"}`)},
			want: "then run the tests",
		},
		{
			name: "the user discards the row",
			act:  marotte.ClientCommand{Type: marotte.CmdUnqueuePrompt, ChatID: "c1", Payload: json.RawMessage(`{"message_id":"m-u1"}`)},
			want: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, cs, br := steerOpsHub(t)
			kas320On(s.h, "c1", br)
			id, _ := s.h.stagePromptTurn(t, "c1")
			s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
			queueTextDuring(t, s.h, "m-u1", "then run the tests")
			if got := awaitDelivery(t, notifyWake(t, s.h, "c1")); got != command.NotifyQueued {
				t.Fatalf("Setup: wake answered %q, want queued behind the user's row", got)
			}

			plainStop(t, s.h)
			s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnEndMsg("cancelled"))
			s.h.coord.releaseTurn("c1", id)
			idleAfter(t, s.h, "c1", id)

			if n := len(opensOf(t, logOf(t, cs, "c1"))); n != 1 {
				t.Errorf("turns opened after the stop = %d, want none: the wake waits behind the user's paused row", n-1)
			}
			if got, want := listedRows(s.h, "c1"), []string{"m-u1:paused", "grelot:paused"}; !slices.Equal(got, want) {
				t.Errorf("waiting rows = %v, want %v", got, want)
			}

			if rec := postCmd(t, s.h, tc.act); rec.Code != http.StatusOK {
				t.Fatalf("%s = %d %s, want 200", tc.act.Type, rec.Code, rec.Body.String())
			}

			if tc.want == "" {
				joinInflight(t, s.h)
				if n := len(opensOf(t, logOf(t, cs, "c1"))); n != 1 {
					t.Errorf("turns opened after the discard = %d, want none: the wake stays paused", n-1)
				}
				if got, want := listedRows(s.h, "c1"), []string{"grelot:paused"}; !slices.Equal(got, want) {
					t.Errorf("waiting rows = %v, want %v", got, want)
				}
				return
			}
			if next := nextPrompt(t, cs, "c1", 1); next.Text != tc.want {
				t.Errorf("next turn = %q (caller %+v), want %q", next.Text, next.Caller, tc.want)
			}
		})
	}
}

// A wake arriving while a Stop winds the turn down joins the queue paused: it is never steered into the stopping turn,
// and the stopped turn's close opens nothing.
func TestNotify_AWakeDuringAStopJoinsTheQueuePaused(t *testing.T) {
	s, cs, br := steerOpsHub(t)
	k := kas320On(s.h, "c1", br)
	id, _ := s.h.stagePromptTurn(t, "c1")
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
	plainStop(t, s.h)

	if got := awaitDelivery(t, notifyWake(t, s.h, "c1")); got != command.NotifyQueued {
		t.Errorf("wake answered %q, want queued: the turn is stopping", got)
	}
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnEndMsg("cancelled"))
	s.h.coord.releaseTurn("c1", id)
	idleAfter(t, s.h, "c1", id)

	if n := len(opensOf(t, logOf(t, cs, "c1"))); n != 1 {
		t.Errorf("turns opened after the stop = %d, want none", n-1)
	}
	if got, want := listedRows(s.h, "c1"), []string{"grelot:paused"}; !slices.Equal(got, want) {
		t.Errorf("waiting rows = %v, want %v", got, want)
	}
	if got := k.steered(); len(got) != 0 {
		t.Errorf("steers sent = %q, want none", got)
	}
}

// stageUnpromptedTurn stages a prompt turn sent but whose session/prompt is not yet written; commit writes it.
func (rt *Runtime) stageUnpromptedTurn(t *testing.T, chatID marotte.ChatID) (turnID string, commit func() error) {
	t.Helper()
	id, err := rt.coord.openTurnNow(t.Context(), chatID, command.TurnOpen{
		Source: marotte.TurnSourcePrompt,
		Prompt: &marotte.EntryPrompt{ID: "m-" + string(chatID), Text: "prompt"},
		Init:   func(c *marotte.Chat) { c.Name = "test chat" },
	})
	if err != nil {
		t.Fatalf("Setup: OpenTurn: %v", err)
	}
	if !rt.coord.startTurn(t.Context(), command.TurnRef{Chat: chatID, Turn: id}, nil) {
		t.Fatal("Setup: startTurn refused")
	}
	commit, err = rt.coord.AwaitPromptSend(t.Context(), chatID, id)
	if err != nil || commit == nil {
		t.Fatalf("Setup: AwaitPromptSend = %v (commit %t)", err, commit != nil)
	}
	return id, commit
}

// A wake placed while the turn started, then the user's Send, which kiro-cli takes first once the prompt is written:
// when the user's send-now returns that steer unread, the user's words open the next turn and the wake, though placed
// first, waits behind them; a plain Stop pauses both, the user's words ahead.
func TestNotify_TheUsersSteerBackFromAStopGoesAheadOfAWakePlacedBeforeIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
	}{
		{"a plain stop", ""},
		{"send now on the steer", `{"lead":"steer-m-u"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, cs, br := steerOpsHub(t)
			k := kas320On(s.h, "c1", br)
			id, commit := s.h.stageUnpromptedTurn(t, "c1")
			s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
			placeWake(t, s.h, "c1", "n-1")
			sent := sendAsUser(s.h, "c1", "m-u", "check the logs")
			if !reachesState(s.h, "c1", "m-u", marotte.RunPending) {
				t.Fatal("Setup: the user's send never stood on the list")
			}
			if err := commit(); err != nil {
				t.Fatalf("Setup: the prompt's write commit: %v", err)
			}
			if got := ackAs(t, awaitAnswer(t, sent)); got != "steered" {
				t.Fatalf("Setup: the user's send answered %q, want steered", got)
			}
			if got := k.steered(); !slices.Equal(got, []string{"check the logs"}) {
				t.Fatalf("Setup: steers sent = %q, want the user's first", got)
			}

			rec := postCmd(t, s.h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1", Payload: json.RawMessage(tc.payload)})
			if rec.Code != http.StatusOK {
				t.Fatalf("cancel = %d %s, want 200", rec.Code, rec.Body.String())
			}
			s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnEndMsg("cancelled"))
			s.h.coord.releaseTurn("c1", id)

			if tc.payload == "" {
				idleAfter(t, s.h, "c1", id)
				waitFor(t, func() bool { return stateOf(s.h, "c1", "m-u") == marotte.RunPaused })
				if got, want := listedRows(s.h, "c1"), []string{"m-u:paused", "grelot:paused"}; !slices.Equal(got, want) {
					t.Errorf("waiting rows = %v, want %v", got, want)
				}
				if n := len(opensOf(t, logOf(t, cs, "c1"))); n != 1 {
					t.Errorf("turns opened after the stop = %d, want none", n-1)
				}
				return
			}
			if next := nextPrompt(t, cs, "c1", 1); next.Text != "check the logs" || next.Caller != nil {
				t.Errorf("next turn = %q (caller %+v), want the user's steer", next.Text, next.Caller)
			}
			if !grelotWaits(s.h, "c1", "n-1") {
				t.Errorf("list = %+v, want the wake still waiting", listOf(s.h, "c1"))
			}
			if got := k.steered(); slices.Contains(got, grelotWake) {
				t.Errorf("steers sent = %q, want no wake", got)
			}
		})
	}
}

// turnOfPromptText is the turn id of the turn whose prompt is text, "" for none.
func turnOfPromptText(t *testing.T, cs *testChatStore, chatID marotte.ChatID, text string) string {
	t.Helper()
	for _, e := range logOf(t, cs, chatID) {
		if e.Kind != marotte.EntryKindTurnOpen {
			continue
		}
		var open marotte.EntryTurnOpen
		decodePayload(t, e, &open)
		if open.Prompt != nil && open.Prompt.Text == text {
			return e.Turn
		}
	}
	return ""
}

// kiro-cli continues the send-now turn's session/prompt for a steer the user sent into it: that continuation is
// still the turn the user asked for, so the wake is not steered into it, and goes once the session/prompt returned.
func TestNotify_AContinuationOfTheSendNowTurnTakesNoWake(t *testing.T) {
	s, cs, br := steerOpsHub(t)
	s.recs.note = s.h.coord.recordSteer
	k := kas320On(s.h, "c1", br)
	id, _ := s.h.stagePromptTurn(t, "c1")
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
	s.userSteer(t, "steer-a", "my message")
	if got := awaitDelivery(t, notifyWake(t, s.h, "c1")); got != command.NotifyQueued {
		t.Fatalf("Setup: wake answered %q, want queued behind the user's steer", got)
	}
	userTurn := make(chan struct{})
	br.mu.Lock()
	br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: userTurn}
	br.mu.Unlock()
	released := false
	t.Cleanup(func() {
		if !released {
			close(userTurn)
		}
	})

	rec := postCmd(t, s.h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1", Payload: json.RawMessage(`{"lead":"steer-a"}`)})
	if rec.Code != http.StatusOK {
		t.Fatalf("Setup: cancel = %d %s", rec.Code, rec.Body.String())
	}
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnEndMsg("cancelled"))
	s.h.coord.releaseTurn("c1", id)
	if next := nextPrompt(t, cs, "c1", 1); next.Text != "my message" {
		t.Fatalf("Setup: next turn = %q, want the user's send-now steer", next.Text)
	}
	userID := turnOfPromptText(t, cs, "c1", "my message")
	waitForCall(t, br, marotte.MethodPrompt)
	send(t, s.h, newPromptTurnStartMsg(userID), newActivityMsg(true))
	if got := ackAs(t, awaitAnswer(t, sendAsUser(s.h, "c1", "m-u2", "and the tests"))); got != "steered" {
		t.Fatalf("Setup: the user's second send answered %q, want steered into the send-now turn", got)
	}

	// The turn ends with the user's steer unread, so kiro-cli continues the same session/prompt, which reads it.
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnEndMsg("end_turn"))
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnStartMsg())
	s.h.translateACPEvent("c1", s.h.originOf("c1"), injectedMsg("steer-m-u2", "and the tests"))
	// The read's fate and the list's next delivery apply in one section, so the wake's state once the read steer
	// left the list is what the list decided.
	waitFor(t, func() bool { return stateOf(s.h, "c1", "m-u2") == "" })

	var wake marotte.RunEntry
	for _, e := range listOf(s.h, "c1") {
		if e.Row != nil && fromGrelot(e.Row.Caller) {
			wake = e
		}
	}
	if wake.State != marotte.RunPending {
		t.Errorf("wake = %+v, want pending while the send-now turn's session/prompt runs", wake)
	}
	if got := k.steered(); slices.Contains(got, grelotWake) {
		t.Errorf("steers sent = %q, want no wake while the send-now turn's session/prompt runs", got)
	}

	send(t, s.h, newTurnEndMsg("end_turn"), newActivityMsg(false))
	released = true
	close(userTurn)

	waitFor(t, func() bool {
		for _, o := range opensOf(t, logOf(t, cs, "c1")) {
			if o.Prompt != nil && fromGrelot(o.Prompt.Caller) {
				return true
			}
		}
		return false
	})
	if got := k.steered(); slices.Contains(got, grelotWake) {
		t.Errorf("steers sent = %q, want the wake taken as a prompt of its own", got)
	}
}
