package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

func answerCmd(typ marotte.CommandType, payload string) marotte.ClientCommand {
	return marotte.ClientCommand{Type: typ, ChatID: "c1", Payload: json.RawMessage(payload)}
}

func dismissCmd(askID int64) marotte.ClientCommand {
	return answerCmd(marotte.CmdUserInputResponse, `{"request_id":`+strconv.FormatInt(askID, 10)+`,"action":"dismissed"}`)
}

type askFamily struct {
	payload any
	cmd     func(askID int64) marotte.ClientCommand
	event   marotte.EventType
}

func askFamilies() map[string]askFamily {
	allow := []marotte.PermissionOption{{OptionID: "allow", Name: "Allow", Kind: "allow_once"}}
	return map[string]askFamily{
		"permission": {
			event:   marotte.EventPermissionNeeded,
			payload: marotte.PermissionNeededPayload{Options: allow},
			cmd: func(askID int64) marotte.ClientCommand {
				return answerCmd(marotte.CmdPermissionResponse, `{"request_id":`+strconv.FormatInt(askID, 10)+`,"option_id":"allow"}`)
			},
		},
		"elicitation": {
			event:   marotte.EventElicitationNeeded,
			payload: marotte.ElicitationNeededPayload{},
			cmd: func(askID int64) marotte.ClientCommand {
				return answerCmd(marotte.CmdElicitationResponse, `{"request_id":`+strconv.FormatInt(askID, 10)+`,"action":"decline"}`)
			},
		},
		"userInput": {
			event:   marotte.EventUserInputNeeded,
			payload: marotte.UserInputNeededPayload{},
			cmd:     dismissCmd,
		},
	}
}

// An ask's answer goes back on the bridge it arrived on, under the ACP id that bridge sent: the
// chat's current bridge would resolve an unrelated request under the same id.
func TestAskAnswer_GoesBackOnTheBridgeTheAskArrivedOn(t *testing.T) {
	for name, tc := range askFamilies() {
		t.Run(name, func(t *testing.T) {
			h, _, _ := newTestHub()
			origin, current := newFakeBridge(), newFakeBridge()
			h.bridge.mgr.insert("c1", &sharedBridge{bridge: current, state: bridgeIdle})
			askID := requestIDOf(t, h.bus.PendingPermsAdd(42, marotte.NewEvent(tc.event, "c1", tc.payload), origin))

			if rec := postCmd(t, h, tc.cmd(askID)); rec.Code != http.StatusOK {
				t.Fatalf("%s answer code = %d (%s), want 200", name, rec.Code, rec.Body.String())
			}
			if got := origin.answeredIDs(); len(got) != 1 || got[0] != 42 {
				t.Errorf("answers on the bridge the ask arrived on = %v, want its own ACP id [42]", got)
			}
			if got := current.respondCount(); got != 0 {
				t.Errorf("answers on the chat's current bridge = %d, want 0", got)
			}
		})
	}
}

// A delivered answer settles the ask once, attributed to whoever answered.
func TestAskAnswer_ADeliveredAnswerSettlesItOnceAsTheUsers(t *testing.T) {
	h, _, _ := newTestHub()
	origin := newFakeBridge()
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: origin, state: bridgeIdle})
	askID := requestIDOf(t, h.bus.PendingPermsAdd(42, marotte.NewEvent(marotte.EventUserInputNeeded, "c1",
		marotte.UserInputNeededPayload{}), origin))
	head := h.bus.fanout.Position().Head

	if rec := postCmd(t, h, dismissCmd(askID)); rec.Code != http.StatusOK {
		t.Fatalf("answer code = %d (%s), want 200", rec.Code, rec.Body.String())
	}

	want := marotte.DecisionSettledPayload{RequestID: askID, Kind: marotte.DecisionKindUserInput, SettledBy: marotte.SettledByUser}
	if got := settledEvents(t, bufferedSince(h, head)); len(got) != 1 || got[0] != want {
		t.Errorf("decision_settled = %+v, want exactly [%+v]", got, want)
	}
}

type exitedBridge struct {
	*fakeBridge
}

func (*exitedBridge) Respond(context.Context, int64, any, error) error {
	return fmt.Errorf("respond on ACP: %w", marotte.ErrBridgeExited)
}

// A claim won just before its bridge ended loses the write: the expected loss at a bridge's end,
// not a failure to report, and the card is told nothing received the answer. The end's own
// retirement, landing after, announces it no second time.
func TestAskAnswer_ToABridgeThatEndedSinceTheClaimSettlesItEnded(t *testing.T) {
	h, _, _ := newTestHub()
	ended := &exitedBridge{fakeBridge: newFakeBridge()}
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: ended, state: bridgeIdle})
	askID := requestIDOf(t, h.bus.PendingPermsAdd(42, marotte.NewEvent(marotte.EventUserInputNeeded, "c1",
		marotte.UserInputNeededPayload{}), ended))
	head := h.bus.fanout.Position().Head
	logs := captureLogs(t)

	rec := postCmd(t, h, dismissCmd(askID))

	if rec.Code != http.StatusGone || !strings.Contains(rec.Body.String(), `"ask_withdrawn"`) {
		t.Fatalf("answer code = %d (%s), want 410 ask_withdrawn", rec.Code, rec.Body.String())
	}
	if strings.Contains(logs.String(), `"level":"ERROR"`) {
		t.Errorf("an answer lost to its bridge's end logged a failure:\n%s", logs.String())
	}
	h.bus.endAsksOf(ended)
	want := marotte.DecisionSettledPayload{RequestID: askID, Kind: marotte.DecisionKindUserInput, SettledBy: marotte.SettledByEnded}
	if got := settledEvents(t, bufferedSince(h, head)); len(got) != 1 || got[0] != want {
		t.Errorf("decision_settled = %+v, want exactly [%+v]", got, want)
	}
}

// endingBridge is the chat's live bridge whose end lands while an answer is being written: the
// write fails after the tracker saw the end.
type endingBridge struct {
	*fakeBridge
	h *Runtime
}

func (b *endingBridge) Respond(context.Context, int64, any, error) error {
	b.h.bus.endAsksOf(b)
	return errors.New("bridge stdin closed")
}

// An end that lands during the claimed answer's write settles it ended exactly once: the end
// marks the claim, the failed write settles it.
func TestAskAnswer_AnEndDuringTheWriteSettlesItEndedOnce(t *testing.T) {
	h, _, _ := newTestHub()
	br := &endingBridge{fakeBridge: newFakeBridge(), h: h}
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})
	askID := requestIDOf(t, h.bus.PendingPermsAdd(42, marotte.NewEvent(marotte.EventUserInputNeeded, "c1",
		marotte.UserInputNeededPayload{}), br))
	head := h.bus.fanout.Position().Head

	if rec := postCmd(t, h, dismissCmd(askID)); rec.Code != http.StatusGone || !strings.Contains(rec.Body.String(), `"ask_withdrawn"`) {
		t.Fatalf("answer code = %d (%s), want 410 ask_withdrawn", rec.Code, rec.Body.String())
	}

	want := marotte.DecisionSettledPayload{RequestID: askID, Kind: marotte.DecisionKindUserInput, SettledBy: marotte.SettledByEnded}
	if got := settledEvents(t, bufferedSince(h, head)); len(got) != 1 || got[0] != want {
		t.Errorf("decision_settled = %+v, want exactly [%+v]", got, want)
	}
	if left := h.bus.pendingPerms.list("c1"); len(left) != 0 {
		t.Errorf("asks pending after the end = %v, want none", left)
	}
}

// An answer that fails on a bridge still running reached nothing, so the ask is still KAS's open
// question: the command is refused rather than acknowledged, the chat keeps waiting on the user, and
// the ask is pending and re-offered to every surface, unannounced, for another answer.
func TestAskAnswer_AFailedWriteOnALiveBridgeRefusesTheAnswerAndReoffersTheAsk(t *testing.T) {
	for name, tc := range askFamilies() {
		t.Run(name, func(t *testing.T) {
			h, _, _ := newTestHub()
			live := &droppingBridge{fakeBridge: newFakeBridge()}
			h.bridge.mgr.insert("c1", &sharedBridge{bridge: live, state: bridgeIdle})
			h.bus.chatStatus.Merge("c1", marotte.ChatStatusPayload{Status: marotte.ChatStatusWaitingOnUser})
			askID := requestIDOf(t, h.bus.PendingPermsAdd(42, marotte.NewEvent(tc.event, "c1", tc.payload), live))
			head := h.bus.fanout.Position().Head

			rec := postCmd(t, h, tc.cmd(askID))

			if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), `"answer_not_delivered"`) {
				t.Errorf("%s answer on a failed write = %d %s, want 502 answer_not_delivered", name, rec.Code, rec.Body.String())
			}
			if got := h.bus.chatStatus.Get("c1").Status; got != marotte.ChatStatusWaitingOnUser {
				t.Errorf("chat status after an undelivered answer = %q, want %q", got, marotte.ChatStatusWaitingOnUser)
			}
			events := bufferedSince(h, head)
			if got := settledEvents(t, events); len(got) != 0 {
				t.Errorf("decision_settled = %+v, want none: nothing answered the ask", got)
			}
			if got := extractTypes(t, events); !slices.Contains(got, string(tc.event)) {
				t.Errorf("events after the failed write = %v, want the ask %s re-offered", got, tc.event)
			}
			if got := h.bus.pendingPerms.list("c1"); len(got) != 1 || requestIDOf(t, got[0]) != askID {
				t.Errorf("asks pending after the failed write = %d, want ask %d again", len(got), askID)
			}
			if _, ok := h.bus.TakePendingPerm("c1", askID, marotte.SettledByUser); !ok {
				t.Error("the reopened ask cannot be claimed again")
			}
		})
	}
}

// An ask a terminal transition settled while its answer was being written was answered by nobody:
// the command says so (410 ask_withdrawn) instead of acknowledging delivery, and the answer does not
// discharge the chat's wait; the transition that won owns the card and the chat's status.
func TestAskAnswer_AnAskSettledDuringTheWriteRefusesTheAnswerAsWithdrawn(t *testing.T) {
	transitions := map[string]func(h *Runtime, origin acpResponder){
		"turnCancel":  func(h *Runtime, _ acpResponder) { h.bus.ClearPendingPermsForChat("c1") },
		"bridgeEnded": func(h *Runtime, origin acpResponder) { h.bus.endAsksOf(origin) },
	}
	for name, tc := range askFamilies() {
		for transition, settle := range transitions {
			t.Run(name+"_"+transition, func(t *testing.T) {
				h, _, _ := newTestHub()
				br := &duringWriteBridge{fakeBridge: newFakeBridge()}
				br.during = func() { settle(h, br) }
				h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})
				h.bus.chatStatus.Merge("c1", marotte.ChatStatusPayload{Status: marotte.ChatStatusWaitingOnUser})
				askID := requestIDOf(t, h.bus.PendingPermsAdd(42, marotte.NewEvent(tc.event, "c1", tc.payload), br))

				rec := postCmd(t, h, tc.cmd(askID))

				if rec.Code != http.StatusGone || !strings.Contains(rec.Body.String(), `"ask_withdrawn"`) {
					t.Errorf("%s answer after a %s during its write = %d %s, want 410 ask_withdrawn", name, transition, rec.Code, rec.Body.String())
				}
				if got := h.bus.chatStatus.Get("c1").Status; got != marotte.ChatStatusWaitingOnUser {
					t.Errorf("chat status after a withdrawn answer = %q, want %q kept for the transition that won", got, marotte.ChatStatusWaitingOnUser)
				}
			})
		}
	}
}

type duringWriteBridge struct {
	*fakeBridge
	during func()
	err    error
}

func (b *duringWriteBridge) Respond(context.Context, int64, any, error) error {
	b.during()
	return b.err
}

// A terminal transition that lands while a claimed answer is being written settles the claim: the
// write's result, delivered or failed on the still-running bridge, can neither reopen it nor
// settle it a second time, and the bridge's later end finds nothing.
func TestAskAnswer_ATerminalTransitionDuringTheWriteSettlesTheClaimOnce(t *testing.T) {
	const toolCallID, workflowID = "tc-1", "wf-1"
	cases := map[string]struct {
		transition func(h *Runtime) bool
		want       []marotte.SettledBy
	}{
		"turnCancel": {
			transition: func(h *Runtime) bool { h.bus.ClearPendingPermsForChat("c1"); return true },
		},
		"runFinished": {
			transition: func(h *Runtime) bool { h.bus.ClearPendingPermsForRun(workflowID); return true },
		},
		"kasWithdrew": {
			transition: func(h *Runtime) bool { return h.bus.PendingPermsWithdraw("c1", toolCallID) },
			want:       []marotte.SettledBy{marotte.SettledByMoot},
		},
		"bridgeEnded": {
			want: []marotte.SettledBy{marotte.SettledByEnded},
		},
		"kasWithdrewThenBridgeEnded": {
			transition: func(h *Runtime) bool {
				ok := h.bus.PendingPermsWithdraw("c1", toolCallID)
				h.bus.endAsksOf(h.bridge.mgr.get("c1").current())
				return ok
			},
			want: []marotte.SettledBy{marotte.SettledByMoot},
		},
	}
	writes := map[string]error{"delivered": nil, "failedOnALiveBridge": errors.New("context canceled")}
	for name, tc := range cases {
		for write, wErr := range writes {
			t.Run(name+"_"+write, func(t *testing.T) {
				h, _, _ := newTestHub()
				br := &duringWriteBridge{fakeBridge: newFakeBridge(), err: wErr}
				transitioned := true
				br.during = func() {
					if tc.transition == nil {
						h.bus.endAsksOf(br)
						return
					}
					transitioned = tc.transition(h)
				}
				h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})
				askID := requestIDOf(t, h.bus.PendingPermsAdd(42, marotte.NewEvent(marotte.EventUserInputNeeded, "c1",
					marotte.UserInputNeededPayload{ToolCallID: toolCallID, RunID: workflowID}), br))
				head := h.bus.fanout.Position().Head

				reply, ok := h.bus.TakePendingPerm("c1", askID, marotte.SettledByUser)
				if !ok {
					t.Fatal("TakePendingPerm refused a pending ask")
				}
				if outcome, err := reply.Respond(t.Context(), nil); outcome != command.AnswerWithdrawn || err != nil {
					t.Errorf("Respond after %s = (%v, %v), want (AnswerWithdrawn, nil): the ask is settled, not delivered or reopened", name, outcome, err)
				}
				h.bus.endAsksOf(br)

				if !transitioned {
					t.Errorf("%s during the write found no open ask, want the claimed one", name)
				}
				var got []marotte.SettledBy
				for _, s := range settledEvents(t, bufferedSince(h, head)) {
					got = append(got, s.SettledBy)
				}
				if !slices.Equal(got, tc.want) {
					t.Errorf("%s during a %s write: decision_settled = %v, want %v", name, write, got, tc.want)
				}
				if left := h.bus.pendingPerms.list(""); len(left) != 0 {
					t.Errorf("%s during a %s write left asks pending = %v, want none", name, write, left)
				}
				if _, ok := h.bus.TakePendingPerm("c1", askID, marotte.SettledByUser); ok {
					t.Errorf("%s during a %s write: the settled ask could be claimed again", name, write)
				}
			})
		}
	}
}
