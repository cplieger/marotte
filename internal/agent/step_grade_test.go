package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/translate"
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
	succeeded := workflow.StepRecord{Status: "completed", CompletionSignal: "success"}
	unsignalled := workflow.StepRecord{Status: "completed"}
	for _, tc := range []struct {
		desc  string
		close stepClose
		kas   workflow.StepRecord
		known bool
		want  stepClose
	}{
		{desc: "a success signal outranks a tool_use stop", close: toolUse, kas: succeeded, known: true, want: toolUse},
		{
			desc: "a success signal clears a stored model-call-limit failure", close: limit, kas: succeeded, known: true,
			want: stepClose{outcome: marotte.TurnOutcomeCompleted, rawStop: marotte.StopReasonToolUse},
		},
		{
			desc: "a success signal clears a refusal", close: refused, kas: succeeded, known: true,
			want: stepClose{outcome: marotte.TurnOutcomeCompleted, rawStop: marotte.StopReasonRefusal},
		},
		{desc: "no signal on a completed step names the limit", close: toolUse, kas: unsignalled, known: true, want: limit},
		{
			desc:  "KAS's own failure keeps the close",
			close: stepClose{outcome: marotte.TurnOutcomeFailed, rawStop: marotte.StopReasonToolUse},
			kas:   workflow.StepRecord{Status: "failed"}, known: true,
			want: stepClose{outcome: marotte.TurnOutcomeFailed, rawStop: marotte.StopReasonToolUse},
		},
		{
			desc:  "an end_turn stop is never the limit",
			close: stepClose{outcome: marotte.TurnOutcomeCompleted, rawStop: marotte.StopReasonEndTurn},
			kas:   unsignalled, known: true,
			want: stepClose{outcome: marotte.TurnOutcomeCompleted, rawStop: marotte.StopReasonEndTurn},
		},
		{desc: "an unread record keeps the close", close: toolUse, want: toolUse},
		{desc: "an unread record keeps a refusal", close: refused, want: refused},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			got := tc.close.graded(tc.kas, tc.known)
			if got.outcome != tc.want.outcome || got.kind != tc.want.kind || got.reason != tc.want.reason ||
				got.rawStop != tc.want.rawStop || got.refusal != tc.want.refusal {
				t.Errorf("graded = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// fixtureFile reads one recorded file under testdata/step_end.
func fixtureFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "step_end", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// recordedStop is the stop reason of the recorded KAS session tail's turn_end line.
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

// stepEndsOf is GET /api/runs/{id}'s `step_ends`.
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

// newestCloseOf is the newest turn_close of a step's transcript as GET /api/runs/{id}/steps/{path} serves it.
func newestCloseOf(t *testing.T, h *Runtime, workflowID, nodePath string) marotte.EntryTurnClose {
	t.Helper()
	out, err := h.runs.stepTranscript(t.Context(), workflowID, nodePath)
	if err != nil {
		t.Fatalf("step transcript: %v", err)
	}
	return newestClose(t, out.Entries)
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

	t.Run("the run read names no step end", func(t *testing.T) {
		if ends := stepEndsOf(t, h, id); len(ends) != 0 {
			t.Errorf("step_ends = %+v, want none", ends)
		}
	})
	t.Run("the step's turn reads completed", func(t *testing.T) {
		c := newestCloseOf(t, h, id, nodePath)
		if c.Outcome != marotte.TurnOutcomeCompleted || c.FailureKind != "" || c.FailureReason != "" {
			t.Errorf("turn_close = {%s %q %q}, want {completed \"\" \"\"}", c.Outcome, c.FailureKind, c.FailureReason)
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
		c := newestCloseOf(t, h, id, nodePath)
		if c.Outcome != limit.Outcome || c.FailureKind != limit.FailureKind || c.FailureReason != limit.FailureReason {
			t.Errorf("turn_close = {%s %q %q}, want {%s %q %q}",
				c.Outcome, c.FailureKind, c.FailureReason, limit.Outcome, limit.FailureKind, limit.FailureReason)
		}
	})
	t.Run("the launching chat gets the note", func(t *testing.T) {
		steers := chatSteers(t, cs, chatID)
		if len(steers) != 1 {
			t.Fatalf("the launching chat got %d notes, want one: %+v", len(steers), steers)
		}
		if want := stepModelCallLimitNoteText("nightly", path); steers[0].Text != want {
			t.Errorf("note text = %q, want %q", steers[0].Text, want)
		}
	})
}

// armReplayEndingOn answers `session/load` of sessionID with one turn whose turn_end carries the recorded stop.
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

	t.Run("the run read names no step end", func(t *testing.T) {
		if ends := stepEndsOf(t, h, id); len(ends) != 0 {
			t.Errorf("step_ends = %+v, want none", ends)
		}
	})
	t.Run("the step's turn reads completed", func(t *testing.T) {
		c := newestCloseOf(t, h, id, nodePath)
		if c.Outcome != marotte.TurnOutcomeCompleted || c.FailureKind != "" || c.FailureReason != "" || c.StopReasonRaw != "tool_use" {
			t.Errorf("turn_close = {%s %q %q %s}, want {completed \"\" \"\" tool_use}",
				c.Outcome, c.FailureKind, c.FailureReason, c.StopReasonRaw)
		}
	})
}
