package translate

import (
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// at is a fixed frame-arrival instant, so the stamped timings are assertable.
var at = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

const atRFC = "2026-03-04T05:06:07Z"

// A node frame carries the node's state, so the client applies it without a refetch.
func TestRunProgress_NodeFramesCarryTheNodesState(t *testing.T) {
	cases := []struct {
		name  string
		kind  marotte.RunProgressKind
		frame kasRunNode
		want  marotte.RunProgressPayload
	}{
		{
			name: "node_start asserts running and stamps the start",
			kind: marotte.RunProgressNodeStart,
			frame: kasRunNode{
				WorkflowID: "wf1", NodeID: "coder", NodePath: []string{"seq", "coder"},
			},
			want: marotte.RunProgressPayload{
				WorkflowID: "wf1", NodeID: "coder", NodePath: "seq/coder",
				Status: "running", StartedAt: atRFC, Kind: marotte.RunProgressNodeStart,
			},
		},
		{
			name: "node_complete forwards KAS's own terminal word and stamps the end",
			kind: marotte.RunProgressNodeComplete,
			frame: kasRunNode{
				WorkflowID: "wf1", NodeID: "coder", NodePath: []string{"seq", "coder"},
				Status: "completed",
			},
			want: marotte.RunProgressPayload{
				WorkflowID: "wf1", NodeID: "coder", NodePath: "seq/coder",
				Status: "completed", EndedAt: atRFC, Kind: marotte.RunProgressNodeComplete,
			},
		},
		{
			name: "a failed node carries KAS's reason, so the row says why without a refetch",
			kind: marotte.RunProgressNodeComplete,
			frame: kasRunNode{
				WorkflowID: "wf1", NodeID: "coder", NodePath: []string{"coder"},
				Status: "failed", Reason: "the build did not link",
			},
			want: marotte.RunProgressPayload{
				WorkflowID: "wf1", NodeID: "coder", NodePath: "coder",
				Status: "failed", EndedAt: atRFC, FailureReason: "the build did not link",
				Kind: marotte.RunProgressNodeComplete,
			},
		},
		{
			name: "node_paused asserts paused and stamps neither end",
			kind: marotte.RunProgressNodePaused,
			frame: kasRunNode{
				WorkflowID: "wf1", NodeID: "ask", NodePath: []string{"ask"},
				Reason: "Step requested user input via send_message.",
			},
			want: marotte.RunProgressPayload{
				WorkflowID: "wf1", NodeID: "ask", NodePath: "ask",
				Status: "paused", Kind: marotte.RunProgressNodePaused,
			},
		},
		{
			// A poll re-states running: a frame the client cannot apply costs a tree rebuild.
			name: "watch_poll names its node and re-states running, stamping neither end",
			kind: marotte.RunProgressWatchPoll,
			frame: kasRunNode{
				WorkflowID: "wf1", NodeID: "watch", NodePath: []string{"watch"},
			},
			want: marotte.RunProgressPayload{
				WorkflowID: "wf1", NodeID: "watch", NodePath: "watch",
				Status: "running", Kind: marotte.RunProgressWatchPoll,
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			node := c.frame.NodeID
			if node == "" {
				node = c.frame.LoopID
			}
			got := runProgress(c.kind, node, &c.frame, at)
			if got != c.want {
				t.Errorf("runProgress(%q, …) = %+v, want %+v", c.kind, got, c.want)
			}
		})
	}
}

// An empty node path tells the client to refetch; these three kinds are not per-node patches.
func TestRunProgress_ShapeChangingKindsCarryNoNodePath(t *testing.T) {
	cases := []struct {
		kind  marotte.RunProgressKind
		frame kasRunNode
		why   string
	}{
		{
			kind:  marotte.RunProgressLoopIteration,
			frame: kasRunNode{WorkflowID: "wf1", LoopID: "loop"},
			why:   "a new iteration container appears in the tree",
		},
		{
			kind:  marotte.RunProgressStepsQueued,
			frame: kasRunNode{WorkflowID: "wf1"},
			why:   "steps are appended to the tree",
		},
		{
			kind:  marotte.RunProgressPaused,
			frame: kasRunNode{WorkflowID: "wf1"},
			why:   "it is run-level and its pauseReason is on inspect alone",
		},
	}
	for _, c := range cases {
		t.Run(string(c.kind), func(t *testing.T) {
			node := c.frame.NodeID
			if node == "" {
				node = c.frame.LoopID
			}
			got := runProgress(c.kind, node, &c.frame, at)
			if got.NodePath != "" {
				t.Errorf("node_path = %q, want empty (%s)", got.NodePath, c.why)
			}
			if got.Status != "" || got.StartedAt != "" || got.EndedAt != "" {
				t.Errorf("carries node state %+v, want none", got)
			}
			if got.Kind != c.kind || got.WorkflowID != "wf1" {
				t.Errorf("address = (%q, %q), want (wf1, %q)", got.WorkflowID, got.Kind, c.kind)
			}
		})
	}
}

// A node frame without a path must not silently join the run-level kinds.
func TestRunProgress_FallsBackToTheNodeIDWithNoPath(t *testing.T) {
	f := kasRunNode{WorkflowID: "wf1", NodeID: "coder"}
	got := runProgress(marotte.RunProgressNodeStart, "coder", &f, at)
	if got.NodePath != "coder" {
		t.Errorf("node_path = %q, want %q", got.NodePath, "coder")
	}
}
