package agent

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/workflow"
)

// throttle drives one turn the model service refused (a ClientThrottleError): its turn_end stops on error, and KAS
// parks the node.
func (s *stepEnding) throttle() {
	s.t.Helper()
	s.turnStart()
	s.edge(map[string]any{"kind": "turn_end", "turnEnd": map[string]any{"stopReason": string(marotte.StopReasonError)}})
	s.paused()
}

// stopHook delivers a Stop hook's card, which kiro-cli fires after the model's last answer.
func (s *stepEnding) stopHook() {
	s.t.Helper()
	s.n++
	s.edge(map[string]any{"hook": map[string]any{
		"hookId": "agent-mail#hook-0", "operationId": "op-" + strconv.Itoa(s.n),
		"name": "Agent Mail news (turn end)", "status": "completed",
	}})
}

// signalledTurn is a turn that worked, sent send_message, answered, and ended with the Stop hook's card last.
func (s *stepEnding) signalledTurn() {
	s.t.Helper()
	s.turnStart()
	s.call("execute_bash")
	s.call("send_message")
	s.say("I'm blocking batch bSL1: two behaviours have no test.")
	s.stopHook()
	s.turnEnd()
}

// wireBool reads one boolean key of v's wire form, the form the client decodes.
func wireBool(t *testing.T, v any, key string) bool {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode %T: %v", v, err)
	}
	return m[key] == true
}

// byHandHub is a run hub whose launching chat is c1 and whose KAS record holds the step at status and signal, its step
// started.
func byHandHub(t *testing.T, id, nodeID, status, signal string) (*Runtime, *testChatStore, *stepEnding) {
	t.Helper()
	h, cs, br := runLoggingHub(t)
	br.setCallResult(methodKiroWorkflowInspect, inspectWithStep(t, id, nodeID, status, signal))
	seedChat(t, cs, "c1")
	h.runs.grantLease(t.Context(), id, "auth-v7-lanes-security-log",
		launchOrigin{origin: runlease.OriginAgent, chatID: "c1"}, hostSec())
	path := []string{id, nodeID}
	nodePath := workflow.PathKey(path)
	session := "sess_aa84bef2-19c9-4356-8250-fbc8c2e2dd9c"
	h.runs.RunNodeStart(t.Context(), &translate.RunStep{RunID: id, NodePath: nodePath, SessionID: session}, "")
	return h, cs, &stepEnding{t: t, h: h, id: id, path: path, nodePath: nodePath, session: session}
}

// TestStepEnd_AStepEndedByHandAfterAThrottledTurnReadsCompletedEndedByHand replays wf_09004949bdedbb2b's
// account-review-claude: throttled turns parked the node, a later turn on the parked node signalled and answered, and
// the parent's update_status completed it, so KAS records it completed with no signal. A step its own warning parked
// and Mark step done completed (account-gate) reads the same.
func TestStepEnd_AStepEndedByHandAfterAThrottledTurnReadsCompletedEndedByHand(t *testing.T) {
	const id = "wf_09004949bdedbb2b"
	for _, tc := range []struct {
		desc  string
		drive func(s *stepEnding)
	}{
		{
			desc: "a throttled turn, a turn that signalled, then the parent's update_status",
			drive: func(s *stepEnding) {
				s.throttle()
				s.signalledTurn()
			},
		},
		{
			desc: "its own warning parked it, then Mark step done",
			drive: func(s *stepEnding) {
				s.signalledTurn()
				s.paused()
			},
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			h, cs, s := byHandHub(t, id, "account-review-claude", "completed", "")
			tc.drive(s)
			s.complete("completed")
			joinInflight(t, h)

			t.Run("the run read", func(t *testing.T) {
				end, named := stepEndsOf(t, h, id)[s.nodePath]
				if !named || end.Outcome != marotte.TurnOutcomeCompleted || end.FailureKind != "" || !wireBool(t, end, "ended_by_hand") {
					t.Errorf("step_ends[%s] = %+v (named %v), want completed and ended_by_hand", s.nodePath, end, named)
				}
			})
			t.Run("the step's recorded grade", func(t *testing.T) {
				g, ok := newestGradeOf(t, transcriptOf(t, h, id, s.nodePath))
				if !ok || g.Outcome != marotte.TurnOutcomeCompleted || g.FailureKind != "" || !wireBool(t, g, "ended_by_hand") {
					t.Errorf("step_graded = %+v (found %v), want completed and ended_by_hand", g, ok)
				}
			})
			t.Run("the launching chat's note", func(t *testing.T) {
				if steers := chatSteers(t, cs, "c1"); len(steers) != 0 {
					t.Errorf("the launching chat got %d notes, want none: %+v", len(steers), steers)
				}
			})
		})
	}
}

// TestStepEnd_AStepKASFailedAfterAThrottledTurnStaysFailed: KAS's failed status is the step's end, whatever turn
// came before it.
func TestStepEnd_AStepKASFailedAfterAThrottledTurnStaysFailed(t *testing.T) {
	const id = "wf_09004949bdedbb2b"
	h, _, s := byHandHub(t, id, "account-review-claude", "failed", "")
	s.throttle()
	s.signalledTurn()
	h.runs.RunNodeComplete(t.Context(), id, s.path, "failed", "model service error")
	joinInflight(t, h)

	end, named := stepEndsOf(t, h, id)[s.nodePath]
	if !named || end.Outcome != marotte.TurnOutcomeFailed || wireBool(t, end, "ended_by_hand") {
		t.Errorf("step_ends[%s] = %+v (named %v), want failed and not ended_by_hand", s.nodePath, end, named)
	}
}

// TestStepEnd_AStepThatSignalledSuccessAfterThrottlesReadsPlainCompleted replays wf_09004949bdedbb2b's
// account-build iteration 0: three throttled turns, then its own success signal applied.
func TestStepEnd_AStepThatSignalledSuccessAfterThrottlesReadsPlainCompleted(t *testing.T) {
	const id = "wf_09004949bdedbb2b"
	h, cs, s := byHandHub(t, id, "account-build", "completed", "success")
	s.throttle()
	s.throttle()
	s.throttle()
	s.signalledTurn()
	s.complete("completed")
	joinInflight(t, h)

	t.Run("the run read", func(t *testing.T) {
		end, named := stepEndsOf(t, h, id)[s.nodePath]
		if named && (end.Outcome != marotte.TurnOutcomeCompleted || end.FailureKind != "" || wireBool(t, end, "ended_by_hand")) {
			t.Errorf("step_ends[%s] = %+v, want no row or plain completed", s.nodePath, end)
		}
	})
	t.Run("the launching chat's note", func(t *testing.T) {
		if steers := chatSteers(t, cs, "c1"); len(steers) != 0 {
			t.Errorf("the launching chat got %d notes, want none: %+v", len(steers), steers)
		}
	})
}
