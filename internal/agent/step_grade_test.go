package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/turnlog"
	"github.com/cplieger/marotte/internal/workflow"
)

// inspectWithStep is an inspect reply whose tree holds one step under the run's root sequence, at path
// [workflowID, nodeID].
func inspectWithStep(t *testing.T, workflowID, nodeID, status, signal string) json.RawMessage {
	t.Helper()
	step := map[string]any{"nodeId": nodeID, "type": "step", "status": status, "sessionId": "step-session-1"}
	if signal != "" {
		step["completionSignal"] = signal
		step["completionSignalSource"] = "send_message"
	}
	raw, err := json.Marshal(map[string]any{
		"workflowId": workflowID,
		"state": map[string]any{
			"workflowId": workflowID, "status": "running",
			"root": map[string]any{"nodeId": workflowID, "type": "sequence", "status": "running", "children": []any{step}},
		},
	})
	if err != nil {
		t.Fatalf("marshal inspect: %v", err)
	}
	return raw
}

func TestStepClose_GradedUnderKASsRecord(t *testing.T) {
	limit := stepClose{
		outcome: marotte.TurnOutcomeFailed, kind: marotte.FailureKindModelCallLimit,
		reason: marotte.ModelCallLimitStepReason, rawStop: marotte.StopReasonToolUse,
	}
	toolUse := stepClose{outcome: marotte.TurnOutcomeCompleted, rawStop: marotte.StopReasonToolUse}
	refused := stepClose{
		outcome: marotte.TurnOutcomeRefused, rawStop: marotte.StopReasonRefusal,
		refusal: &marotte.RefusalInfo{Category: "policy"},
	}
	silent := stepClose{outcome: marotte.TurnOutcomeCompleted, rawStop: marotte.StopReasonEndTurn, unanswered: true}
	offTurn := stepClose{outcome: marotte.TurnOutcomeCompleted, rawStop: marotte.StopReasonEndTurn, offTurn: true}
	byHand := stepClose{outcome: marotte.TurnOutcomeCompleted, rawStop: marotte.StopReasonEndTurn, byHand: true}
	succeeded := workflow.StepRecord{Status: "completed", CompletionSignal: "success"}
	unsignalled := workflow.StepRecord{Status: "completed"}
	for _, tc := range []struct {
		desc  string
		close stepClose
		kas   workflow.StepRecord
		want  stepClose
	}{
		{desc: "a success signal outranks a tool_use stop", close: toolUse, kas: succeeded, want: toolUse},
		{
			desc: "a success signal clears a stored model-call-limit failure", close: limit, kas: succeeded,
			want: stepClose{outcome: marotte.TurnOutcomeCompleted, rawStop: marotte.StopReasonToolUse},
		},
		{
			desc: "a success signal clears a refusal", close: refused, kas: succeeded,
			want: stepClose{outcome: marotte.TurnOutcomeCompleted, rawStop: marotte.StopReasonRefusal},
		},
		{desc: "no signal on a completed step names the limit", close: toolUse, kas: unsignalled, want: limit},
		{
			desc:  "a max_turn_requests stop on a completed step names the limit",
			close: stepClose{outcome: marotte.TurnOutcomeCompleted, rawStop: marotte.StopReasonMaxTurnRequests},
			kas:   unsignalled,
			want: stepClose{
				outcome: marotte.TurnOutcomeFailed, kind: marotte.FailureKindModelCallLimit,
				reason: marotte.ModelCallLimitStepReason, rawStop: marotte.StopReasonMaxTurnRequests,
			},
		},
		{
			desc:  "KAS's own failure keeps the close",
			close: stepClose{outcome: marotte.TurnOutcomeFailed, rawStop: marotte.StopReasonToolUse},
			kas:   workflow.StepRecord{Status: "failed"},
			want:  stepClose{outcome: marotte.TurnOutcomeFailed, rawStop: marotte.StopReasonToolUse},
		},
		{
			desc:  "an end_turn stop is never the limit",
			close: stepClose{outcome: marotte.TurnOutcomeCompleted, rawStop: marotte.StopReasonEndTurn},
			kas:   unsignalled,
			want:  stepClose{outcome: marotte.TurnOutcomeCompleted, rawStop: marotte.StopReasonEndTurn},
		},
		{
			desc: "no signal on a completed step after an empty answer names the empty answer", close: silent,
			kas: unsignalled,
			want: stepClose{
				outcome: marotte.TurnOutcomeFailed, kind: marotte.FailureKindEmptyAnswer,
				reason: marotte.EmptyAnswerStepReason, rawStop: marotte.StopReasonEndTurn,
			},
		},
		{desc: "a success signal outranks an empty answer", close: silent, kas: succeeded, want: silent},
		{desc: "KAS's own failure keeps an empty answer's close", close: silent, kas: workflow.StepRecord{Status: "failed"}, want: silent},
		{desc: "no signal on a step its own turn did not end reads ended by hand", close: offTurn, kas: unsignalled, want: byHand},
		{
			desc: "a hand end outranks a tool_use stop", kas: unsignalled,
			close: stepClose{outcome: marotte.TurnOutcomeCompleted, rawStop: marotte.StopReasonToolUse, offTurn: true},
			want:  stepClose{outcome: marotte.TurnOutcomeCompleted, rawStop: marotte.StopReasonToolUse, byHand: true},
		},
		{
			desc: "a hand end outranks an empty answer stored before it was told apart", kas: unsignalled,
			close: stepClose{outcome: marotte.TurnOutcomeCompleted, rawStop: marotte.StopReasonEndTurn, unanswered: true, offTurn: true},
			want:  byHand,
		},
		{
			desc: "a refusal ended by hand stays refused", kas: unsignalled,
			close: stepClose{outcome: marotte.TurnOutcomeRefused, rawStop: marotte.StopReasonRefusal, refusal: refused.refusal, offTurn: true},
			want:  refused,
		},
		{desc: "a success signal on a step its own turn did not end is no hand end", close: offTurn, kas: succeeded, want: offTurn},
		{desc: "KAS's own failure keeps a close its own turn did not end", close: offTurn, kas: workflow.StepRecord{Status: "failed"}, want: offTurn},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			got := tc.close.graded(tc.kas)
			if got.outcome != tc.want.outcome || got.kind != tc.want.kind || got.reason != tc.want.reason ||
				got.rawStop != tc.want.rawStop || got.refusal != tc.want.refusal || got.byHand != tc.want.byHand {
				t.Errorf("graded = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func fixtureFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "step_end", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

func recordedStop(t *testing.T, name string) marotte.StopReason {
	t.Helper()
	var stop marotte.StopReason
	sc := bufio.NewScanner(bytes.NewReader(fixtureFile(t, name)))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var line struct {
			Payload struct {
				Type       string `json:"type"`
				StopReason string `json:"stopReason"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
		if line.Payload.Type == "turn_end" {
			stop = marotte.StopReason(line.Payload.StopReason)
		}
	}
	if stop == "" {
		t.Fatalf("%s holds no turn_end", name)
	}
	return stop
}

func stepEndsOf(t *testing.T, h *Runtime, workflowID string) map[string]marotte.RunStepEnd {
	t.Helper()
	_, body := getRun(t, h, workflowID)
	var reply struct {
		StepEnds map[string]marotte.RunStepEnd `json:"step_ends"`
	}
	if err := json.Unmarshal([]byte(body), &reply); err != nil {
		t.Fatalf("decoding the run reply: %s", err)
	}
	return reply.StepEnds
}

func newestClose(t *testing.T, entries []marotte.Entry) marotte.EntryTurnClose {
	t.Helper()
	for _, e := range slices.Backward(entries) {
		if e.Kind == marotte.EntryKindTurnClose {
			return closeEntryOf(t, e)
		}
	}
	t.Fatalf("the step transcript holds no turn_close: %+v", entries)
	return marotte.EntryTurnClose{}
}

// newestGradeOf is the newest step_graded payload in a step's entries; false when they hold none.
func newestGradeOf(t *testing.T, entries []marotte.Entry) (marotte.EntryStepGraded, bool) {
	t.Helper()
	for _, e := range slices.Backward(entries) {
		if e.Kind != marotte.EntryKindStepGraded {
			continue
		}
		var g marotte.EntryStepGraded
		if err := json.Unmarshal(e.Payload, &g); err != nil {
			t.Fatalf("decode step_graded %s: %v", e.ID, err)
		}
		return g, true
	}
	return marotte.EntryStepGraded{}, false
}

func transcriptOf(t *testing.T, h *Runtime, workflowID, nodePath string) []marotte.Entry {
	t.Helper()
	out, err := h.runs.stepTranscript(t.Context(), workflowID, nodePath)
	if err != nil {
		t.Fatalf("step transcript: %v", err)
	}
	return out.Entries
}

// runRecordedStep drives one step turn as the frames recorded it: node_start, the session's turn_end, then
// node_complete with KAS's status, its note's goroutine joined.
func runRecordedStep(t *testing.T, h *Runtime, workflowID, nodePath string, path []string, stop marotte.StopReason) {
	t.Helper()
	h.runs.RunNodeStart(t.Context(), &translate.RunStep{RunID: workflowID, NodePath: nodePath, SessionID: "step-session-1"}, "")
	if !h.runs.RunStopReason(workflowID, nodePath, stop) {
		t.Fatalf("RunStopReason(%q) refused on the open turn", stop)
	}
	h.runs.RunNodeComplete(t.Context(), workflowID, path, "completed", "")
	joinInflight(t, h)
}

// TestStepEnd_AKiroCLI232HandoffStepReadsCompleted replays kiro-cli 2.32.0's create-workflow step: its turn ends on
// the model's tool_use stop after update_workflow's handoff ends it, and KAS records the step completed on a success
// signal. No surface may grade it failed.
func TestStepEnd_AKiroCLI232HandoffStepReadsCompleted(t *testing.T) {
	const (
		id     = "wf_04b7a7ba1affa505"
		chatID = marotte.ChatID("c1")
	)
	path := []string{id, "create-workflow"}
	nodePath := workflow.PathKey(path)
	h, cs, br := runLoggingHub(t)
	br.setCallResult(methodKiroWorkflowInspect, fixtureFile(t, "creator_232_inspect.json"))
	seedChat(t, cs, chatID)
	h.runs.grantLease(t.Context(), id, "example-plan",
		launchOrigin{origin: runlease.OriginAgent, chatID: string(chatID)}, hostSec())

	runRecordedStep(t, h, id, nodePath, path, recordedStop(t, "creator_232_session_tail.jsonl"))

	t.Run("the run read names the step's recorded completed end", func(t *testing.T) {
		if ends, want := stepEndsOf(t, h, id), (marotte.RunStepEnd{Outcome: marotte.TurnOutcomeCompleted}); len(ends) != 1 || ends[nodePath] != want {
			t.Errorf("step_ends = %+v, want only %s = %+v", ends, nodePath, want)
		}
	})
	t.Run("the step's turn reads completed", func(t *testing.T) {
		g, ok := newestGradeOf(t, transcriptOf(t, h, id, nodePath))
		if want := (marotte.EntryStepGraded{Outcome: marotte.TurnOutcomeCompleted}); !ok || g != want {
			t.Errorf("step_graded = %+v (found %v), want %+v", g, ok, want)
		}
	})
	t.Run("the launching chat gets no note", func(t *testing.T) {
		if steers := chatSteers(t, cs, chatID); len(steers) != 0 {
			t.Errorf("the launching chat got %d notes, want none: %+v", len(steers), steers)
		}
	})
}

// TestStepEnd_AStepKASCompletedWithNoSignalAfterAToolUseStopIsTheModelCallLimit replays a step kiro-cli stopped at
// 300 model calls (2.31.0, `[IterationLimit] Reached iteration limit` in kiro.log): KAS records it completed with no
// completion signal.
func TestStepEnd_AStepKASCompletedWithNoSignalAfterAToolUseStopIsTheModelCallLimit(t *testing.T) {
	const (
		id     = "wf_42066808f4c28538"
		chatID = marotte.ChatID("c1")
	)
	path := []string{id, "p2-loop", "iter-0", "p2-build"}
	nodePath := workflow.PathKey(path)
	h, cs, br := runLoggingHub(t)
	br.setCallResult(methodKiroWorkflowInspect, fixtureFile(t, "limit_231_inspect.json"))
	seedChat(t, cs, chatID)
	h.runs.grantLease(t.Context(), id, "nightly",
		launchOrigin{origin: runlease.OriginAgent, chatID: string(chatID)}, hostSec())

	runRecordedStep(t, h, id, nodePath, path, recordedStop(t, "limit_231_session_tail.jsonl"))

	limit := marotte.RunStepEnd{
		Outcome:       marotte.TurnOutcomeFailed,
		FailureReason: marotte.ModelCallLimitStepReason,
		FailureKind:   marotte.FailureKindModelCallLimit,
	}
	t.Run("the run read names the limit", func(t *testing.T) {
		if ends := stepEndsOf(t, h, id); ends[nodePath] != limit || len(ends) != 1 {
			t.Errorf("step_ends = %+v, want only %s = %+v", ends, nodePath, limit)
		}
	})
	t.Run("the step's turn reads the limit", func(t *testing.T) {
		g, ok := newestGradeOf(t, transcriptOf(t, h, id, nodePath))
		want := marotte.EntryStepGraded{Outcome: limit.Outcome, FailureKind: limit.FailureKind, FailureReason: limit.FailureReason}
		if !ok || g != want {
			t.Errorf("step_graded = %+v (found %v), want %+v", g, ok, want)
		}
	})
	t.Run("the launching chat gets the note", func(t *testing.T) {
		steers := chatSteers(t, cs, chatID)
		if len(steers) != 1 {
			t.Fatalf("the launching chat got %d notes, want one: %+v", len(steers), steers)
		}
		// The run read learned the name from the inspect fixture's workflowName, which outranks the lease.
		if want := stepModelCallLimitNoteText("one-engine-build", nodePath); steers[0].Text != want {
			t.Errorf("note text = %q, want %q", steers[0].Text, want)
		}
	})
}

func armReplayEndingOn(br *fakeBridge, sessionID string, stop marotte.StopReason) {
	end, _ := json.Marshal(map[string]any{
		"sessionUpdate": "session_info_update",
		"_meta":         map[string]any{"kiro": map[string]any{"kind": "turn_end", "turnEnd": map[string]any{"stopReason": stop}}},
	})
	br.mu.Lock()
	defer br.mu.Unlock()
	if br.notifsOnCall == nil {
		br.notifsOnCall = map[string][]*marotte.RPCResponse{}
	}
	br.notifsOnCall[marotte.MethodSessionLoad] = []*marotte.RPCResponse{
		replayFrame(sessionID, `{"sessionUpdate":"session_info_update","_meta":{"kiro":{"kind":"turn_start","turnStart":true}}}`),
		replayFrame(sessionID, `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"done"}}`),
		replayFrame(sessionID, string(end)),
	}
}

// TestStepEnd_AReplayedStepTurnReadsUnderKASsRecord reads a step marotte's run log holds no turn for, so its
// transcript is KAS's replay, whose projection grades a tool_use turn_end the model-call limit on its own.
func TestStepEnd_AReplayedStepTurnReadsUnderKASsRecord(t *testing.T) {
	for _, tc := range []struct {
		desc, id, session, inspect, tail string
		path                             []string
		want                             marotte.EntryTurnClose
	}{
		{
			desc: "the 2.32.0 handoff step reads completed", id: "wf_04b7a7ba1affa505",
			session: "sess_bb88c405-020f-42b1-9185-1aca2328fadb", inspect: "creator_232_inspect.json",
			tail: "creator_232_session_tail.jsonl", path: []string{"wf_04b7a7ba1affa505", "create-workflow"},
			want: marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted},
		},
		{
			desc: "the step stopped at 300 model calls reads the limit", id: "wf_42066808f4c28538",
			session: "sess_f269cde4-db44-4dd4-aeb2-c2d3cae6cbcb", inspect: "limit_231_inspect.json",
			tail: "limit_231_session_tail.jsonl", path: []string{"wf_42066808f4c28538", "p2-loop", "iter-0", "p2-build"},
			want: marotte.EntryTurnClose{
				Outcome: marotte.TurnOutcomeFailed, FailureKind: marotte.FailureKindModelCallLimit,
				FailureReason: marotte.ModelCallLimitStepReason,
			},
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			h, _, br := runLoggingHub(t)
			br.setCallResult(methodKiroWorkflowInspect, fixtureFile(t, tc.inspect))
			armReplayEndingOn(br, tc.session, recordedStop(t, tc.tail))
			nodePath := workflow.PathKey(tc.path)

			out, err := h.runs.stepTranscript(t.Context(), tc.id, nodePath)
			if err != nil {
				t.Fatalf("step transcript: %v", err)
			}
			if out.Source != marotte.RunStepTranscriptSourceReplay {
				t.Fatalf("source = %q, want the replay", out.Source)
			}
			c := newestClose(t, out.Entries)
			if c.Outcome != tc.want.Outcome || c.FailureKind != tc.want.FailureKind || c.FailureReason != tc.want.FailureReason {
				t.Errorf("turn_close = {%s %q %q}, want {%s %q %q}", c.Outcome, c.FailureKind, c.FailureReason,
					tc.want.Outcome, tc.want.FailureKind, tc.want.FailureReason)
			}
			if c.StopReasonRaw != string(marotte.StopReasonToolUse) {
				t.Errorf("stop_reason_raw = %q, want tool_use", c.StopReasonRaw)
			}
		})
	}
}

// TestStepEnd_AStoredModelCallLimitFailureOfAStepKASCompletedOnSuccessReadsCompleted reads a run log as kiro-cli
// 2.32.0's first marotte build wrote it, failed at the model-call limit, back under KAS's record of the step.
func TestStepEnd_AStoredModelCallLimitFailureOfAStepKASCompletedOnSuccessReadsCompleted(t *testing.T) {
	const id = "wf_04b7a7ba1affa505"
	nodePath := workflow.PathKey([]string{id, "create-workflow"})
	h, br := seedChatParentedRun(t, true)
	br.setCallResult(methodKiroWorkflowInspect, fixtureFile(t, "creator_232_inspect.json"))
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, runLogDir, id), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, runLogDir, id, "entries.jsonl"), fixtureFile(t, "creator_232_run_log.jsonl"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.runs.log = newRunLog(dir)

	t.Run("the run read names the step's recorded completed end", func(t *testing.T) {
		if ends, want := stepEndsOf(t, h, id), (marotte.RunStepEnd{Outcome: marotte.TurnOutcomeCompleted}); len(ends) != 1 || ends[nodePath] != want {
			t.Errorf("step_ends = %+v, want only %s = %+v", ends, nodePath, want)
		}
	})
	t.Run("the step's turn reads completed", func(t *testing.T) {
		entries := transcriptOf(t, h, id, nodePath)
		if c := newestClose(t, entries); c.FailureKind != marotte.FailureKindModelCallLimit || c.StopReasonRaw != "tool_use" {
			t.Errorf("turn_close = {%s %q %s}, want the stored {failed model_call_limit tool_use}", c.Outcome, c.FailureKind, c.StopReasonRaw)
		}
		g, ok := newestGradeOf(t, entries)
		if want := (marotte.EntryStepGraded{Outcome: marotte.TurnOutcomeCompleted}); !ok || g != want {
			t.Errorf("step_graded = %+v (found %v), want %+v", g, ok, want)
		}
	})
}

// TestStepEnd_AStepKASCompletedWithNoSignalAfterAnEmptyAnswerReadsFailed replays kiro-cli 3.0.0's empty end_turn
// after a tool result: KAS records the step completed with no signal, and its session holds no answer or KAS's
// "No assistant response was recorded" stand-in.
func TestStepEnd_AStepKASCompletedWithNoSignalAfterAnEmptyAnswerReadsFailed(t *testing.T) {
	const (
		id      = "wf_aabb60d3e105b178"
		session = "step-session-1"
		stand   = "(No assistant response was recorded for the preceding turn. This placeholder was inserted automatically to preserve message alternation.)"
	)
	path := []string{id, "review-b"}
	nodePath := workflow.PathKey(path)
	for _, tc := range []struct {
		desc, before, after, signal string
		// card is a marotte card filed after the answer: kiro-cli fires a Stop hook after the model's last word.
		card   marotte.ToolKind
		failed bool
	}{
		{desc: "KAS's stand-in after the tool result", before: "Reading the diff.", after: stand, failed: true},
		{desc: "an answer, then a Stop hook's card", after: "Two findings, written to the review file.", card: marotte.ToolKindHook},
		{desc: "an answer, then a steering box", after: "Two findings, written to the review file.", card: marotte.ToolKindSteering},
		{desc: "nothing after the tool result but a Stop hook's card", before: "Reading the diff.", card: marotte.ToolKindHook, failed: true},
		{desc: "nothing after the tool result", before: "Reading the diff.", failed: true},
		{desc: "a blank answer after the tool result", after: " \n", failed: true},
		{desc: "an answer after the tool result", after: "Two findings, written to the review file."},
		{
			desc:  "an answer opening with the stand-in's first words",
			after: "No assistant response was recorded in the log, so I wrote the findings to the review file.",
		},
		{desc: "a success signal", after: "", signal: "success"},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			g := newWakeRig(t, id, inspectWithStep(t, id, "review-b", "completed", tc.signal))
			h := g.h
			step := &translate.RunStep{RunID: id, NodePath: nodePath, SessionID: session}
			h.runs.RunNodeStart(t.Context(), step, "")
			turn, ok := h.runs.RunFoldTarget(t.Context(), step, "")
			if !ok {
				t.Fatal("Setup: the step has no open turn")
			}
			if tc.before != "" {
				if _, err := turn.TextDelta(t.Context(), "", "say-1", tc.before); err != nil {
					t.Fatalf("Setup: text: %v", err)
				}
			}
			if _, err := turn.ToolCall(t.Context(), "", &marotte.EntryToolCall{ID: "call-1", Title: "rg", Kind: marotte.ToolKindOther}); err != nil {
				t.Fatalf("Setup: tool call: %v", err)
			}
			if _, err := turn.ToolResult(t.Context(), "", "call-1", &marotte.EntryToolResult{Status: marotte.ToolCompleted}); err != nil {
				t.Fatalf("Setup: tool result: %v", err)
			}
			if tc.after != "" {
				if _, err := turn.TextDelta(t.Context(), "", "say-2", tc.after); err != nil {
					t.Fatalf("Setup: text: %v", err)
				}
			}
			if tc.card != "" {
				if _, err := turn.ToolCall(t.Context(), "", &marotte.EntryToolCall{ID: "card-1", Title: "Hook fired: news", Kind: tc.card}); err != nil {
					t.Fatalf("Setup: card: %v", err)
				}
			}
			if !h.runs.RunStopReason(id, nodePath, marotte.StopReasonEndTurn) {
				t.Fatal("Setup: RunStopReason refused on the open turn")
			}
			h.runs.RunNodeComplete(t.Context(), id, path, "completed", "")
			joinInflight(t, h)

			// A close that said nothing is due a grade: the empty answer, or completed under a success signal.
			var grade *marotte.EntryStepGraded
			switch {
			case tc.failed:
				grade = &marotte.EntryStepGraded{
					Outcome: marotte.TurnOutcomeFailed, FailureKind: marotte.FailureKindEmptyAnswer,
					FailureReason: marotte.EmptyAnswerStepReason,
				}
			case tc.signal == "success":
				grade = &marotte.EntryStepGraded{Outcome: marotte.TurnOutcomeCompleted}
			}
			t.Run("the run read", func(t *testing.T) {
				end, named := stepEndsOf(t, h, id)[nodePath]
				switch {
				case grade == nil && named:
					t.Errorf("step_ends[%s] = %+v, want no row", nodePath, end)
				case grade != nil && (!named || end != marotte.RunStepEnd{Outcome: grade.Outcome, FailureKind: grade.FailureKind, FailureReason: grade.FailureReason}):
					t.Errorf("step_ends[%s] = %+v (named %v), want %+v", nodePath, end, named, *grade)
				}
			})
			t.Run("the step's turn", func(t *testing.T) {
				entries := transcriptOf(t, h, id, nodePath)
				g, ok := newestGradeOf(t, entries)
				if grade != nil {
					if !ok || g != *grade {
						t.Errorf("step_graded = %+v (found %v), want %+v", g, ok, *grade)
					}
					return
				}
				if ok {
					t.Errorf("step_graded = %+v, want none", g)
				}
				if c := newestClose(t, entries); c.Outcome != marotte.TurnOutcomeCompleted || c.FailureKind != "" {
					t.Errorf("turn_close = {%s %q}, want the sealed {completed \"\"}", c.Outcome, c.FailureKind)
				}
			})
			t.Run("the launching agent's wake", func(t *testing.T) {
				waitUntil(t, "the launching agent's wake", func() bool { return !tc.failed || len(g.wakes()) > 0 })
				joinInflight(t, h)
				wakes := g.wakes()
				if !tc.failed {
					if len(wakes) != 0 {
						t.Errorf("the launching agent got %d wakes, want none: %q", len(wakes), wakes)
					}
					return
				}
				if len(wakes) != 1 {
					t.Fatalf("the launching agent got %d wakes, want one: %q", len(wakes), wakes)
				}
				for _, want := range []string{nodePath, session, "ended with an empty model turn and no completion signal"} {
					if !strings.Contains(wakes[0], want) {
						t.Errorf("wake text = %q, want it to name %q", wakes[0], want)
					}
				}
			})
		})
	}
}

// stepEnding drives one step's frames through the run, as marotte receives them.
type stepEnding struct {
	t        *testing.T
	h        *Runtime
	id       string
	path     []string
	nodePath string
	session  string
	n        int
}

func (s *stepEnding) turn() *turnlog.Turn {
	s.t.Helper()
	turn, ok := s.h.runs.RunFoldTarget(s.t.Context(), &translate.RunStep{RunID: s.id, NodePath: s.nodePath, SessionID: s.session}, "")
	if !ok {
		s.t.Fatal("Setup: the step has no open turn")
	}
	return turn
}

func (s *stepEnding) say(text string) {
	s.t.Helper()
	s.n++
	if _, err := s.turn().TextDelta(s.t.Context(), "", "say-"+strconv.Itoa(s.n), text); err != nil {
		s.t.Fatalf("Setup: text: %v", err)
	}
}

// call runs one tool call to its result, named as KAS titles a built-in.
func (s *stepEnding) call(toolName string) {
	s.t.Helper()
	s.n++
	id := "call-" + strconv.Itoa(s.n)
	turn := s.turn()
	if _, err := turn.ToolCall(s.t.Context(), "", &marotte.EntryToolCall{ID: id, Title: toolName, ToolName: toolName, Kind: marotte.ToolKindOther}); err != nil {
		s.t.Fatalf("Setup: tool call: %v", err)
	}
	if _, err := turn.ToolResult(s.t.Context(), "", id, &marotte.EntryToolResult{Status: marotte.ToolCompleted}); err != nil {
		s.t.Fatalf("Setup: tool result: %v", err)
	}
}

// edge delivers a step session's turn bracket through the run host's session_info_update door.
func (s *stepEnding) edge(kiro map[string]any) {
	s.t.Helper()
	update := mustJSON(s.t, map[string]any{"sessionUpdate": "session_info_update", "_meta": map[string]any{"kiro": kiro}})
	s.h.translator.HandleStepInfoUpdate(s.t.Context(), runChatID(s.id), update,
		translate.FrameAttribution{SessionID: s.session, RunID: s.id, NodePath: s.nodePath, Step: true})
}

func (s *stepEnding) turnStart() { s.edge(map[string]any{"kind": "turn_start", "turnStart": true}) }

func (s *stepEnding) turnEnd() {
	s.edge(map[string]any{"kind": "turn_end", "turnEnd": map[string]any{"stopReason": "end_turn"}})
}

func (s *stepEnding) paused() { s.h.runs.RunNodePaused(s.t.Context(), s.id, s.path) }

// resume delivers the node_start KAS sends when it resumes a parked step.
func (s *stepEnding) resume() {
	s.h.runs.RunNodeStart(s.t.Context(), &translate.RunStep{RunID: s.id, NodePath: s.nodePath, SessionID: s.session}, "")
}

func (s *stepEnding) complete(status string) {
	s.h.runs.RunNodeComplete(s.t.Context(), s.id, s.path, status, "")
}

// TestStepEnd_OnlyAStepsOwnTurnEndingWithNothingSaidReadsAnEmptyAnswer walks every way a step ends. The empty-answer
// grade judges a step its own turn ended with no answer text and no send_message (kiro-cli 3.0.0's empty end_turn,
// which KAS completes at once). KAS also records completed with no signal when something else ends the step: Mark step
// done and the parent's update_status clear the signal (applyPausedStepFinish), and a run's end closes what is open.
// Mark step done and update_status read completed, ended by hand.
func TestStepEnd_OnlyAStepsOwnTurnEndingWithNothingSaidReadsAnEmptyAnswer(t *testing.T) {
	const (
		id      = "wf_aabb60d3e105b178"
		session = "step-session-1"
	)
	path := []string{id, "review-b"}
	for _, tc := range []struct {
		desc, status, signal string
		drive                func(s *stepEnding)
		empty, byHand        bool
	}{
		{
			desc: "its own turn ends with an answer", status: "completed",
			drive: func(s *stepEnding) {
				s.turnStart()
				s.call("rg")
				s.say("Two findings, written to the review file.")
				s.turnEnd()
				s.complete("completed")
			},
		},
		{
			desc: "its own turn ends with nothing said", status: "completed", empty: true,
			drive: func(s *stepEnding) {
				s.turnStart()
				s.say("Reading the diff.")
				s.call("rg")
				s.turnEnd()
				s.complete("completed")
			},
		},
		{
			desc: "its own turn ends on a send_message", status: "completed",
			drive: func(s *stepEnding) {
				s.turnStart()
				s.call("rg")
				s.call("send_message")
				s.turnEnd()
				s.complete("completed")
			},
		},
		{
			desc: "Mark step done after its warning parked it", status: "completed", byHand: true,
			drive: func(s *stepEnding) {
				s.turnStart()
				s.call("rg")
				s.call("send_message")
				s.turnEnd()
				s.paused()
				s.complete("completed")
			},
		},
		{
			desc: "Mark step done after its success signal parked it behind a sibling", status: "completed", byHand: true,
			drive: func(s *stepEnding) {
				s.turnStart()
				s.call("send_message")
				s.turnEnd()
				s.paused()
				s.complete("completed")
			},
		},
		{
			desc: "the parent's update_status completes it after a silent wake turn re-parked it", status: "completed", byHand: true,
			drive: func(s *stepEnding) {
				s.turnStart()
				s.call("send_message")
				s.turnEnd()
				s.paused()
				s.turnStart()
				s.call("rg")
				s.turnEnd()
				s.paused()
				s.complete("completed")
			},
		},
		{
			desc: "its own turn ends with nothing said after an answered warning resumed it", status: "completed", empty: true,
			drive: func(s *stepEnding) {
				s.turnStart()
				s.call("send_message")
				s.turnEnd()
				s.paused()
				s.resume()
				s.turnStart()
				s.call("rg")
				s.turnEnd()
				s.complete("completed")
			},
		},
		{
			desc: "its own turn ends with an answer after an answered warning resumed it", status: "completed",
			drive: func(s *stepEnding) {
				s.turnStart()
				s.call("send_message")
				s.turnEnd()
				s.paused()
				s.resume()
				s.turnStart()
				s.call("rg")
				s.say("Done.")
				s.turnEnd()
				s.complete("completed")
			},
		},
		{
			desc: "the parent's update_status fails it after a park", status: "failed",
			drive: func(s *stepEnding) {
				s.turnStart()
				s.call("rg")
				s.turnEnd()
				s.paused()
				s.complete("failed")
			},
		},
		{
			desc: "the parent's update_status completes it inside its first turn", status: "completed", byHand: true,
			drive: func(s *stepEnding) {
				s.turnStart()
				s.call("rg")
				s.complete("completed")
			},
		},
		{
			desc: "the parent's update_status completes it inside a later turn", status: "completed", byHand: true,
			drive: func(s *stepEnding) {
				s.turnStart()
				s.say("Reading the diff.")
				s.turnEnd()
				s.turnStart()
				s.call("rg")
				s.complete("completed")
			},
		},
		{
			desc: "a cancel", status: "cancelled",
			drive: func(s *stepEnding) {
				s.turnStart()
				s.call("rg")
				s.turnEnd()
				s.complete("cancelled")
			},
		},
		{
			desc: "the run aborts", status: "running",
			drive: func(s *stepEnding) {
				s.turnStart()
				s.call("rg")
				s.h.runs.closeRun(s.t.Context(), s.id, "aborted")
			},
		},
		{
			desc: "the run completes with its turn open", status: "completed",
			drive: func(s *stepEnding) {
				s.turnStart()
				s.call("rg")
				s.turnEnd()
				s.h.runs.closeRun(s.t.Context(), s.id, "completed")
			},
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			g := newWakeRig(t, id, inspectWithStep(t, id, "review-b", tc.status, tc.signal))
			h := g.h
			nodePath := workflow.PathKey(path)
			h.runs.RunNodeStart(t.Context(), &translate.RunStep{RunID: id, NodePath: nodePath, SessionID: session}, "")
			tc.drive(&stepEnding{t: t, h: h, id: id, path: path, nodePath: nodePath, session: session})
			joinInflight(t, h)

			t.Run("the run read", func(t *testing.T) {
				end := stepEndsOf(t, h, id)[nodePath]
				if got := end.FailureKind == marotte.FailureKindEmptyAnswer; got != tc.empty {
					t.Errorf("step_ends[%s] = %+v, want empty_answer: %v", nodePath, end, tc.empty)
				}
				if end.EndedByHand != tc.byHand || (tc.byHand && end.Outcome != marotte.TurnOutcomeCompleted) {
					t.Errorf("step_ends[%s] = %+v, want ended by hand: %v", nodePath, end, tc.byHand)
				}
			})
			t.Run("the step's turn", func(t *testing.T) {
				entries := transcriptOf(t, h, id, nodePath)
				g, ok := newestGradeOf(t, entries)
				if tc.empty {
					if !ok || g.FailureKind != marotte.FailureKindEmptyAnswer {
						t.Errorf("step_graded = %+v (found %v), want the empty answer", g, ok)
					}
					return
				}
				if tc.byHand {
					if want := (marotte.EntryStepGraded{Outcome: marotte.TurnOutcomeCompleted, EndedByHand: true}); !ok || g != want {
						t.Errorf("step_graded = %+v (found %v), want %+v", g, ok, want)
					}
					return
				}
				if ok {
					t.Errorf("step_graded = %+v, want none", g)
				}
				if c := newestClose(t, entries); c.FailureKind == marotte.FailureKindEmptyAnswer {
					t.Errorf("turn_close = {%s %q}, want its sealed end", c.Outcome, c.FailureKind)
				}
			})
			t.Run("the launching agent's wake", func(t *testing.T) {
				waitUntil(t, "the launching agent's wake", func() bool { return !tc.empty || len(g.wakes()) > 0 })
				joinInflight(t, h)
				var wakes int
				for _, w := range g.wakes() {
					if strings.Contains(w, "ended with an empty model turn and no completion signal") {
						wakes++
					}
				}
				if want := map[bool]int{true: 1}[tc.empty]; wakes != want {
					t.Errorf("the launching agent got %d empty-answer wakes, want %d", wakes, want)
				}
			})
		})
	}
}

// A replayed close folds its grade as a recorded one does (stepClose.withGrade): its refusal survives only a refused
// grade.
func TestGradeNewestClose_KeepsARefusalOnlyUnderARefusedGrade(t *testing.T) {
	refusal := &marotte.RefusalInfo{Category: "policy"}
	unsignalled := workflow.StepRecord{Status: "completed"}
	for _, tc := range []struct {
		desc  string
		close marotte.EntryTurnClose
		want  marotte.EntryTurnClose
	}{
		{
			desc:  "a tool_use stop graded the model-call limit",
			close: marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted, StopReasonRaw: string(marotte.StopReasonToolUse), Refusal: refusal},
			want: marotte.EntryTurnClose{
				Outcome: marotte.TurnOutcomeFailed, FailureKind: marotte.FailureKindModelCallLimit,
				FailureReason: marotte.ModelCallLimitStepReason, StopReasonRaw: string(marotte.StopReasonToolUse),
			},
		},
		{
			desc:  "a failure the grade keeps",
			close: marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeFailed, StopReasonRaw: string(marotte.StopReasonEndTurn), Refusal: refusal},
			want:  marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeFailed, StopReasonRaw: string(marotte.StopReasonEndTurn)},
		},
		{
			desc:  "a refusal the grade keeps",
			close: marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeRefused, StopReasonRaw: string(marotte.StopReasonRefusal), Refusal: refusal},
			want:  marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeRefused, StopReasonRaw: string(marotte.StopReasonRefusal), Refusal: refusal},
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			payload, err := json.Marshal(&tc.close)
			if err != nil {
				t.Fatal(err)
			}
			entries := []marotte.Entry{{Turn: "t1", Kind: marotte.EntryKindTurnClose, Payload: payload}}
			gradeNewestClose(entries, func() (workflow.StepRecord, bool) { return unsignalled, true })
			got := newestClose(t, entries)
			if got.Outcome != tc.want.Outcome || got.FailureKind != tc.want.FailureKind ||
				got.FailureReason != tc.want.FailureReason || got.StopReasonRaw != tc.want.StopReasonRaw ||
				(got.Refusal == nil) != (tc.want.Refusal == nil) {
				t.Errorf("turn_close = %+v, want %+v", got, tc.want)
			}
		})
	}
}
