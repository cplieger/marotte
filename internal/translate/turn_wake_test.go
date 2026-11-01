package translate

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

const (
	wakeChatSession = "sess_66362e75-badd-490c-ad48-593859625c29"
	wakeRun         = "wf_1a6d245a6e99e18e"
	wakeStepNode    = "prep/wait-apps"
)

// wakeFrames is testdata/turn_wake_frames.json by name.
func wakeFrames(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile("testdata/turn_wake_frames.json")
	if err != nil {
		t.Fatalf("read frames: %v", err)
	}
	var frames map[string]json.RawMessage
	if err := json.Unmarshal(raw, &frames); err != nil {
		t.Fatalf("parse frames: %v", err)
	}
	return frames
}

type wakeHarness struct {
	t      *testing.T
	deps   *baseDeps
	tr     *Translator
	frames map[string]json.RawMessage
}

// newWakeHarness is the chat whose agent launched wakeRun, with the step's node_start seen.
func newWakeHarness(t *testing.T) *wakeHarness {
	t.Helper()
	deps, _, _ := depsWithStore(t, testChat)
	deps.parent = wakeChatSession
	deps.runNames = map[string]string{wakeRun: "turn-wake"}
	h := &wakeHarness{t: t, deps: deps, tr: New(rolesOf(deps)), frames: wakeFrames(t)}
	h.send("node_start")
	return h
}

func (h *wakeHarness) frame(name string) json.RawMessage {
	h.t.Helper()
	raw, ok := h.frames[name]
	if !ok {
		h.t.Fatalf("no frame %q in the fixture", name)
	}
	return raw
}

// send dispatches a notification frame to the handler its method reaches in production.
func (h *wakeHarness) send(name string) {
	h.t.Helper()
	var f struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(h.frame(name), &f); err != nil {
		h.t.Fatalf("frame %q: %v", name, err)
	}
	msg := &marotte.RPCResponse{Method: f.Method, Params: f.Params}
	ctx := h.t.Context()
	switch f.Method {
	case "_kiro/workflow/node_start":
		h.tr.RunProgressHandler(marotte.RunProgressNodeStart)(ctx, testChat, msg)
	case "_kiro/workflow/node_paused":
		h.tr.RunProgressHandler(marotte.RunProgressNodePaused)(ctx, testChat, msg)
	case "_kiro/workflow/run_complete":
		h.tr.HandleRunComplete(ctx, testChat, msg)
	case "_kiro/session/notify":
		h.tr.HandleWorkflowMessage(ctx, testChat, msg)
	default:
		h.t.Fatalf("frame %q has method %q, which the harness does not route", name, f.Method)
	}
}

// wake is KAS's wake turn on the chat: its turn_start, then the named content frames.
func (h *wakeHarness) wake(content ...string) {
	h.t.Helper()
	turnStartOn(h.t, h.tr, FrameAttribution{})
	h.content(content...)
}

func (h *wakeHarness) content(names ...string) {
	h.t.Helper()
	for _, name := range names {
		var head struct {
			SessionUpdate string `json:"sessionUpdate"`
		}
		raw := h.frame(name)
		if err := json.Unmarshal(raw, &head); err != nil {
			h.t.Fatalf("frame %q: %v", name, err)
		}
		if head.SessionUpdate == "tool_call" {
			h.tr.HandleToolCall(h.t.Context(), testChat, raw, FrameAttribution{})
			continue
		}
		h.tr.HandleAssistantChunk(h.t.Context(), testChat, raw, false, FrameAttribution{})
	}
}

// wakes is every turn_wake in the chat's log, in file order.
func (h *wakeHarness) wakes() []marotte.EntryTurnWake {
	h.t.Helper()
	var out []marotte.EntryTurnWake
	for _, e := range h.deps.turns.chatLog[testChat] {
		if e.Kind == marotte.EntryKindTurnWake {
			out = append(out, decodePayload[marotte.EntryTurnWake](h.t, &e))
		}
	}
	return out
}

// onlyWake is the one turn_wake the log holds.
func (h *wakeHarness) onlyWake() marotte.EntryTurnWake {
	h.t.Helper()
	w := h.wakes()
	if len(w) != 1 {
		h.t.Fatalf("turn_wake entries = %+v, want exactly one", w)
	}
	return w[0]
}

// rowID is the chat row filed with text.
func (h *wakeHarness) rowID(text string) string {
	h.t.Helper()
	for _, r := range chatLogRows(h.t, h.deps) {
		if r.Text == text {
			return r.ID
		}
	}
	h.t.Fatalf("no chat row reads %q", text)
	return ""
}

func (h *wakeHarness) notifyText(name string) string {
	h.t.Helper()
	var f struct {
		Params struct {
			Message string `json:"message"`
		} `json:"params"`
	}
	if err := json.Unmarshal(h.frame(name), &f); err != nil {
		h.t.Fatal(err)
	}
	return f.Params.Message
}

func TestTurnWake_AStepMessageWakeNamesTheMessageThatWokeIt(t *testing.T) {
	for _, c := range []struct{ frame, severity string }{
		{"notify_info", "info"}, {"notify_warning", "warning"}, {"notify_error", "error"},
	} {
		t.Run(c.severity, func(t *testing.T) {
			h := newWakeHarness(t)
			h.send(c.frame)
			h.wake("chunk_send_message_wake")

			w := h.onlyWake()
			text := h.notifyText(c.frame)
			want := marotte.WakeSteer{
				ID: h.rowID(text), Run: wakeRun, RunLabel: "turn-wake", Step: wakeStepNode, Severity: c.severity, Text: text,
			}
			if w.Reason != marotte.WakeReasonStepMessage || w.Note != "" || len(w.Steers) != 1 || w.Steers[0] != want {
				t.Errorf("turn_wake = %+v, want a step message naming %+v", w, want)
			}
		})
	}
}

func TestTurnWake_AStepsSuccessNeverWakesSoTheNextMessageIsTheWaker(t *testing.T) {
	h := newWakeHarness(t)
	h.send("notify_success")
	h.send("notify_info")
	h.wake("chunk_send_message_wake")

	w := h.onlyWake()
	if len(w.Steers) != 1 || w.Steers[0].Severity != "info" {
		t.Errorf("turn_wake steers = %+v, want the info message: KAS buffers a step's success", w.Steers)
	}
}

func TestTurnWake_IsFiledOnceBeforeTheTurnsFirstContent(t *testing.T) {
	for _, first := range []string{"chunk_send_message_wake", "tool_call_send_message_wake"} {
		t.Run(first, func(t *testing.T) {
			h := newWakeHarness(t)
			h.send("notify_warning")
			h.wake(first, "chunk_send_message_wake", "tool_call_send_message_wake")

			h.onlyWake()
			log := h.deps.turns.chatLog[testChat]
			var kinds []marotte.EntryKind
			for i := range log {
				if log[i].Kind != marotte.EntryKindSteer {
					kinds = append(kinds, log[i].Kind)
				}
			}
			if len(kinds) == 0 || kinds[0] != marotte.EntryKindTurnWake {
				t.Errorf("the turn's entries run %v, want the turn_wake first", kinds)
			}
		})
	}
}

func TestTurnWake_KASsNetworkNoteIsMarkedFromTheStepsRetryWait(t *testing.T) {
	t.Run("a note while the step waits out connection errors", func(t *testing.T) {
		h := newWakeHarness(t)
		h.send("node_paused_retry_wait")
		h.send("notify_network_waiting")
		h.wake("chunk_send_message_wake")

		w := h.onlyWake()
		if w.Note != marotte.WakeNoteNetwork || len(w.Steers) != 1 || w.Steers[0].Text != h.notifyText("notify_network_waiting") {
			t.Errorf("turn_wake = %+v, want the network note with KAS's sentence", w)
		}
	})
	t.Run("a message after the wait ended is the step's own", func(t *testing.T) {
		h := newWakeHarness(t)
		h.send("node_paused_retry_wait")
		h.send("node_start")
		h.send("notify_info")
		h.wake("chunk_send_message_wake")

		if w := h.onlyWake(); w.Note != "" {
			t.Errorf("turn_wake note = %q, want none: the step was running again", w.Note)
		}
	})
}

func TestTurnWake_ARepeatCapIsWordedFromTheRunsTypedPause(t *testing.T) {
	h := newWakeHarness(t)
	h.send("run_complete_repeat_cap")
	h.send("notify_repeat_cap")
	h.wake("chunk_send_message_wake")

	w := h.onlyWake()
	if w.Note != marotte.WakeNoteRepeatCap || w.Loop != "fix-loop" || w.MaxIterations != 6 {
		t.Errorf("turn_wake = %+v, want the repeat-cap note for fix-loop at 6 rounds", w)
	}
	if len(w.Steers) != 1 || w.Steers[0].Run != "wf_e5a4052e1ec9060d" || w.Steers[0].Step != "fix-loop" {
		t.Errorf("turn_wake steers = %+v, want KAS's note row for the run's repeat", w.Steers)
	}
}

func TestTurnWake_ARunFinishedWakeNamesTheRunAndHowItEnded(t *testing.T) {
	cases := []struct {
		frame string
		want  marotte.EntryTurnWake
	}{
		{"run_complete_completed", marotte.EntryTurnWake{
			Reason: marotte.WakeReasonRunFinished, Run: "wf_28486344b9bdedb0", RunLabel: "release-impact",
			RunStatus: marotte.RunStatusCompleted,
		}},
		{"run_complete_stopped_by_user", marotte.EntryTurnWake{
			Reason: marotte.WakeReasonRunFinished, Run: "wf_9c1d0e7b3a5f2e48", RunLabel: "release-impact-leg2-held-build",
			RunStatus: marotte.RunStatusAborted, Initiator: "user", StopReason: "tab closed",
		}},
	}
	for _, c := range cases {
		t.Run(c.frame, func(t *testing.T) {
			h := newWakeHarness(t)
			h.send(c.frame)
			h.wake("chunk_workflow_complete_wake")

			if w := h.onlyWake(); w.Reason != c.want.Reason || w.Run != c.want.Run || w.RunLabel != c.want.RunLabel ||
				w.RunStatus != c.want.RunStatus || w.Initiator != c.want.Initiator || w.StopReason != c.want.StopReason ||
				len(w.Steers) != 0 {
				t.Errorf("turn_wake = %+v, want %+v", w, c.want)
			}
		})
	}
}

func TestTurnWake_OnlyARunThatEndedWhileTheChatWasIdleCanWakeIt(t *testing.T) {
	t.Run("a run ending during a turn was buffered as a notice", func(t *testing.T) {
		h := newWakeHarness(t)
		h.deps.turns.chatTurn(testChat)
		h.send("run_complete_completed")
		closeChatTurn(t, h.deps, testChat)
		h.wake("chunk_workflow_complete_wake")

		if w := h.onlyWake(); w.Run != "" {
			t.Errorf("turn_wake = %+v, want no run: KAS buffered that completion", w)
		}
	})
	t.Run("a second run ending before the wake turn started was buffered behind it", func(t *testing.T) {
		h := newWakeHarness(t)
		h.send("run_complete_completed")
		h.send("run_complete_stopped_by_user")
		h.wake("chunk_workflow_complete_wake")
		closeChatTurn(t, h.deps, testChat)
		h.wake("chunk_workflow_complete_wake")

		w := h.wakes()
		if len(w) != 2 || w[0].Run != "wf_28486344b9bdedb0" || w[1].Run != "" {
			t.Errorf("turn_wake entries = %+v, want the first run on the first wake and no run on the second", w)
		}
	})
}

func TestTurnWake_TheTurnAfterAReadersCancelResumesWhatTheCancelCleared(t *testing.T) {
	for _, reason := range []string{"chunk_notification_rewake", "chunk_send_message_wake"} {
		t.Run(reason, func(t *testing.T) {
			h := newWakeHarness(t)
			h.deps.turns.chatTurn(testChat)
			h.send("notify_warning")
			stopTurn(h.deps, testChat)
			clearSteering(t, h.tr, "notify-347a6dd1-5d4b-4f0e-8a51-6b0f4c7e2d90")
			resumeAfterStop(h.deps)
			closeChatTurn(t, h.deps, testChat)
			h.wake(reason)

			w := h.onlyWake()
			text := h.notifyText("notify_warning")
			if w.Reason != marotte.WakeReasonRewake || len(w.Steers) != 1 || w.Steers[0].ID != h.rowID(text) || w.Steers[0].Text != text {
				t.Errorf("turn_wake = %+v, want a re-delivery naming the cleared question", w)
			}
		})
	}
}

func TestTurnWake_AnUnknownReasonIsKeptUnknown(t *testing.T) {
	for _, c := range []struct{ name, reason string }{{"a later wake", "some-later-wake"}, {"no reason", ""}} {
		t.Run(c.name, func(t *testing.T) {
			resetCensus(t)
			var logbuf bytes.Buffer
			defer captureSlog(&logbuf)()
			h := newWakeHarness(t)
			h.send("notify_info")
			turnStartOn(t, h.tr, FrameAttribution{})
			wakeChunk(t, h.tr, c.reason, FrameAttribution{})
			wakeChunk(t, h.tr, c.reason, FrameAttribution{})

			if w := h.onlyWake(); w.Reason != marotte.WakeReasonUnknown || len(w.Steers) != 0 || w.Run != "" {
				t.Errorf("turn_wake = %+v, want unknown and naming nothing", w)
			}
			reported := strings.Count(logbuf.String(), "UNKNOWN agentInitiatedReason")
			if want := min(len(c.reason), 1); reported != want {
				t.Errorf("the census reported the reason %d times, want %d: %s", reported, want, logbuf.String())
			}
		})
	}
}

// KAS wakes the idle chat for the first message to reach it and buffers what follows behind that wake, even when
// the first was filed before marotte closed the previous turn.
func TestTurnWake_TheOldestMessageWaitingWhenTheTurnOpenedIsTheWaker(t *testing.T) {
	h := newWakeHarness(t)
	h.deps.turns.chatTurn(testChat)
	h.send("notify_warning")
	closeChatTurn(t, h.deps, testChat)
	h.send("notify_error")
	h.wake("chunk_send_message_wake")

	w := h.onlyWake()
	if len(w.Steers) != 1 || w.Steers[0].ID != h.rowID(h.notifyText("notify_warning")) {
		t.Errorf("turn_wake steers = %+v, want the warning filed first", w.Steers)
	}
}

// rewakeWithInfo re-delivers what the cancel cleared under a send-message-wake; a message filed after the cancel was
// buffered behind that re-delivery and is not what the turn is about.
func TestTurnWake_AReDeliveryIsNotTheMessageFiledAfterTheCancel(t *testing.T) {
	h := newWakeHarness(t)
	h.deps.turns.chatTurn(testChat)
	h.send("notify_info")
	stopTurn(h.deps, testChat)
	clearSteering(t, h.tr, "notify-70b49ad5-0c2e-4f61-9d7a-3e5b8f1a2c64")
	resumeAfterStop(h.deps)
	h.send("notify_warning")
	closeChatTurn(t, h.deps, testChat)
	h.wake("chunk_send_message_wake")

	w := h.onlyWake()
	if w.Reason != marotte.WakeReasonRewake || len(w.Steers) != 1 || w.Steers[0].ID != h.rowID(h.notifyText("notify_info")) {
		t.Errorf("turn_wake = %+v, want the re-delivery of the cleared info message", w)
	}
}

func TestTurnWake_AStepsOwnWakeWritesNothingInTheChat(t *testing.T) {
	h := newWakeHarness(t)
	h.tr.HandleWorkflowMessage(t.Context(), testChat, workflowNotify("parent", wakeChatSession, testStep, "info", "take option B"))
	attr := testStepAttr()
	turnStartOn(t, h.tr, attr)
	wakeChunk(t, h.tr, "send-message-wake", attr)

	if w := h.wakes(); len(w) != 0 {
		t.Errorf("chat turn_wake entries = %+v, want none: the wake was the step's", w)
	}
	for _, e := range h.deps.runEntries(testWfRun, attr.NodePath) {
		if e.Kind == marotte.EntryKindTurnWake {
			t.Errorf("the step's run log holds a turn_wake %s, want none: a step turn has no header", e.Payload)
		}
	}
}
