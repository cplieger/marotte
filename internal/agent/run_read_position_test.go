package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/workflow"
)

func TestRunReadPosition(t *testing.T) {
	cases := []struct {
		status marotte.RunStatus
		turns  uint64
		want   uint64
	}{
		{status: marotte.RunStatusCompleted, turns: 0, want: 1},
		{status: marotte.RunStatusCompleted, turns: 3, want: 4},
		{status: marotte.RunStatusFailed, turns: 3, want: 4},
		{status: marotte.RunStatusAborted, turns: 0, want: 1},
		{status: marotte.RunStatusCancelled, turns: 3, want: 4},
		{status: marotte.RunStatusRunning, turns: 3, want: 0},
		{status: marotte.RunStatusPaused, turns: 3, want: 0},
		{status: "unheard-of", turns: 3, want: 0},
	}
	for _, tc := range cases {
		t.Run(string(tc.status)+" with "+string(rune('0'+tc.turns))+" turns", func(t *testing.T) {
			if got := runReadPosition(tc.status, tc.turns); got != tc.want {
				t.Errorf("runReadPosition(%q, %d) = %d, want %d", tc.status, tc.turns, got, tc.want)
			}
		})
	}
}

// stepTurnsInRunLog opens and closes n step turns of wf_1 in a fresh run log.
func stepTurnsInRunLog(t *testing.T, h *Runtime, n int) {
	t.Helper()
	h.runs.log = newRunLog(t.TempDir())
	for i := range n {
		openStepTurnOf(t, h, "s"+string(rune('a'+i)))
		path := workflow.PathKey([]string{"wf_1", "s" + string(rune('a'+i))})
		if _, _, err := h.runs.log.closeNode(t.Context(), "wf_1", path, "completed", ""); err != nil {
			t.Fatalf("Setup: closeNode: %v", err)
		}
	}
}

func openStepTurnOf(t *testing.T, h *Runtime, node string) {
	t.Helper()
	path := workflow.PathKey([]string{"wf_1", node})
	if _, _, err := h.runs.log.open(t.Context(), &translate.RunStep{RunID: "wf_1", NodePath: path}, "c1"); err != nil {
		t.Fatalf("Setup: open %s: %v", node, err)
	}
}

func servedPosition(t *testing.T, h *Runtime) uint64 {
	t.Helper()
	_, body := getRun(t, h, "wf_1")
	var reply struct {
		ReadPosition *uint64 `json:"read_position"`
	}
	if err := json.Unmarshal([]byte(body), &reply); err != nil {
		t.Fatalf("decoding the run reply: %s", err)
	}
	if reply.ReadPosition == nil {
		t.Fatalf("the reply carries no read_position: %s", body)
	}
	return *reply.ReadPosition
}

func TestHandleRun_ServesTheReadPosition(t *testing.T) {
	t.Run("a terminal run counts its log's turns plus one", func(t *testing.T) {
		h, br := seedChatParentedRun(t, true)
		br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, "wf_1", marotte.RunStatusAborted, ""))
		stepTurnsInRunLog(t, h, 2)

		if got := servedPosition(t, h); got != 3 {
			t.Errorf("read_position = %d, want 3", got)
		}
	})

	t.Run("a live run serves 0", func(t *testing.T) {
		h, br := seedChatParentedRun(t, true)
		br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, "wf_1", marotte.RunStatusRunning, ""))
		stepTurnsInRunLog(t, h, 1)
		if got := servedPosition(t, h); got != 0 {
			t.Errorf("read_position of a running run = %d, want 0", got)
		}
	})

	t.Run("an unreadable state serves 0 and still answers", func(t *testing.T) {
		h, br := seedChatParentedRun(t, true)
		br.setCallResult(methodKiroWorkflowInspect, json.RawMessage(`{"state":{"status":"failed"},"workflowId":"wf_other"}`))
		stepTurnsInRunLog(t, h, 1)
		if got := servedPosition(t, h); got != 0 {
			t.Errorf("read_position of an unreadable state = %d, want 0", got)
		}
	})

	t.Run("an unreadable run log serves 0 and still answers", func(t *testing.T) {
		h, br := seedChatParentedRun(t, true)
		br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, "wf_1", marotte.RunStatusFailed, ""))
		unreadableRunLog(t, h)
		if got := servedPosition(t, h); got != 0 {
			t.Errorf("read_position with an unreadable run log = %d, want 0", got)
		}
	})
}

// unreadableRunLog gives h a run log whose wf_1 directory is a regular file, so opening it fails.
func unreadableRunLog(t *testing.T, h *Runtime) {
	t.Helper()
	config := t.TempDir()
	h.runs.log = newRunLog(config)
	if err := os.MkdirAll(h.runs.log.root, 0o700); err != nil {
		t.Fatalf("Setup: mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(h.runs.log.root, "wf_1"), nil, 0o600); err != nil {
		t.Fatalf("Setup: write: %v", err)
	}
}

// The ack clamp reads the run as the reply does, in any process: one restarted since the page read
// the run, or one that never served it, still lets that page's acknowledgement land.
func TestRunReadBound_IsTheRunsPositionNow(t *testing.T) {
	t.Run("a terminal run never read in this process", func(t *testing.T) {
		h, br := seedChatParentedRun(t, true)
		br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, "wf_1", marotte.RunStatusCompleted, ""))
		stepTurnsInRunLog(t, h, 2)
		if got, err := h.runs.RunReadBound(t.Context(), "wf_1"); err != nil || got != 3 {
			t.Errorf("RunReadBound() = (%d, %v), want 3", got, err)
		}
	})
	t.Run("a live run is 0", func(t *testing.T) {
		h, br := seedChatParentedRun(t, true)
		br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, "wf_1", marotte.RunStatusFailed, ""))
		stepTurnsInRunLog(t, h, 1)
		servedPosition(t, h)
		br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, "wf_1", marotte.RunStatusRunning, ""))
		if got, err := h.runs.RunReadBound(t.Context(), "wf_1"); err != nil || got != 0 {
			t.Errorf("RunReadBound() of a retry running = (%d, %v), want 0", got, err)
		}
	})
	t.Run("an unreadable run log is an error", func(t *testing.T) {
		h, br := seedChatParentedRun(t, true)
		br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, "wf_1", marotte.RunStatusFailed, ""))
		unreadableRunLog(t, h)
		if got, err := h.runs.RunReadBound(t.Context(), "wf_1"); err == nil {
			t.Errorf("RunReadBound() = (%d, nil), want an error: an unread count cannot bound an ack", got)
		}
	})
	t.Run("an unreadable state is an error", func(t *testing.T) {
		h, br := seedChatParentedRun(t, true)
		br.setCallResult(methodKiroWorkflowInspect, json.RawMessage(`{"state":{"status":"failed"},"workflowId":"wf_other"}`))
		stepTurnsInRunLog(t, h, 1)
		if got, err := h.runs.RunReadBound(t.Context(), "wf_1"); err == nil {
			t.Errorf("RunReadBound() = (%d, nil), want an error", got)
		}
	})
}

// A retry's step turn opening while the terminal state is being read must not pair with that state
// into a position no reply describes, which a watching page would acknowledge ahead of the cue.
func TestHandleRun_CountsTurnsBeforeReadingTheState(t *testing.T) {
	h, br := seedChatParentedRun(t, true)
	stepTurnsInRunLog(t, h, 2)
	terminal := inspectReply(t, "wf_1", marotte.RunStatusFailed, "")
	var armed atomic.Bool
	br.mu.Lock()
	br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method != methodKiroWorkflowInspect {
			return nil, nil, false
		}
		if armed.CompareAndSwap(true, false) {
			path := workflow.PathKey([]string{"wf_1", "retry"})
			if _, _, err := h.runs.log.open(t.Context(), &translate.RunStep{RunID: "wf_1", NodePath: path}, "c1"); err != nil {
				t.Errorf("opening the retry's step turn: %v", err)
			}
		}
		return terminal, nil, true
	}
	br.mu.Unlock()
	armed.Store(true)

	if got := servedPosition(t, h); got != 3 {
		t.Errorf("read_position = %d, want 3: the turn the retry opened during the read belongs to its own terminal state", got)
	}
}
