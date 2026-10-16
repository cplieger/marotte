package agent

// Stock KAS never wakes a parent session when a repeat pauses at maxIterations: its
// autoWakeParentOnComplete admits only terminal statuses (kirodotdev/Kiro#11753). Until it does,
// marotte sends the launching chat the message its user would otherwise have to type.

import (
	"context"
	"errors"
	"log/slog"
	"strconv"

	"github.com/cplieger/keyenc"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/settings"
)

// loopLimitWake sends the wake; the run's lease records which pause it was claimed for.
type loopLimitWake struct {
	// deliver sends a message into a chat on its user's behalf (command.Dispatcher.DeliverForUser);
	// nil sends nothing.
	deliver func(ctx context.Context, chatID marotte.ChatID, text, label string) error
}

// wakeAtLoopLimit sends a parented run's launching chat one message per iteration-limit pause.
// The claim is on the lease before the send, so no replay, crash or failed write sends it twice,
// and a failed send is never retried.
func (rs *Runs) wakeAtLoopLimit(ctx context.Context, f *lifecycleFrame) {
	pause := f.FinalState.Pause
	if pause.Kind != pauseKindMaxIterations || pause.NodeID == "" || rs.loopWake.deliver == nil {
		return
	}
	lease, held := rs.lease(f.WorkflowID)
	if !held || !loopLimitWakeOn(ctx, rs.lifecycle.configDir) {
		return
	}
	parent := marotte.ChatID(lease.ChatID)
	// A parentless lease names no chat, so this also refuses it.
	if _, ok := rs.chats.Get(ctx, parent); !ok {
		return
	}
	key := loopWakeKey(f)
	text, label := loopLimitMessage(rs.frameLabel(f), f.WorkflowID, pause.NodeID, pause.IterationsRun)
	// Claiming under the drain gate means a frame Shutdown refuses leaves no claim, so the restarted
	// process still sends it. The send runs off the frame's goroutine: the parent's own bridge delivers
	// this frame, and a steer waits on that bridge's reader.
	tried := false
	rs.lifecycle.goClaimed(func() func() {
		tried = true
		claimed, err := rs.leaseStore().ClaimLoopWake(ctx, f.WorkflowID, key)
		switch {
		case errors.Is(err, runlease.ErrNotFound) || (err == nil && !claimed):
			return nil
		case err != nil:
			slog.Warn("could not record the iteration-limit wake before sending it, so it was not sent",
				"chat_id", parent, "workflow_id", f.WorkflowID, "error", err)
			return nil
		}
		return func() {
			dctx, cancel := rs.lifecycle.derivedContext()
			defer cancel()
			rs.deliverLoopWake(dctx, parent, f.WorkflowID, key, text, label)
		}
	})
	if !tried {
		slog.Warn("the iteration-limit wake was not sent: marotte is shutting down; the restarted process sends it "+
			"only if it sees the pause again", "chat_id", parent, "workflow_id", f.WorkflowID)
	}
}

// loopWakeKey names one cap pause: the paused repeat's path through the run tree, each step with its iteration, and
// the passes it ran. KAS restarts an inner repeat's count on each outer pass, so the node id and count alone would
// make a later pass's pause read as one already sent. A frame with no tree keys on the node id alone.
func loopWakeKey(f *lifecycleFrame) string {
	pause := f.FinalState.Pause
	path := pausedRepeatPath(f.FinalState.Root, pause.NodeID)
	if path == nil {
		path = []string{pause.NodeID}
	}
	return keyenc.Join(append(append([]string{f.WorkflowID}, path...), strconv.Itoa(pause.IterationsRun))...)
}

// pausedRepeatPath is the path to the paused node named nodeID, two fields per step (node id and iteration, "" when
// the step has none), or nil when the tree holds no such node.
func pausedRepeatPath(n *runTreeNode, nodeID string) []string {
	if n == nil {
		return nil
	}
	step := []string{n.NodeID, ""}
	if n.Iteration != nil {
		step[1] = strconv.Itoa(*n.Iteration)
	}
	if n.NodeID == nodeID && n.Status == marotte.RunNodeStatusPaused {
		return step
	}
	for i := range n.Children {
		if rest := pausedRepeatPath(&n.Children[i], nodeID); rest != nil {
			return append(step, rest...)
		}
	}
	return nil
}

func (rs *Runs) deliverLoopWake(ctx context.Context, parent marotte.ChatID, workflowID, key, text, label string) {
	state := runlease.LoopWakeDelivered
	if err := rs.loopWake.deliver(ctx, parent, text, label); err != nil {
		state = runlease.LoopWakeFailed
		slog.Warn("could not wake the launching chat at its workflow's iteration limit",
			"chat_id", parent, "workflow_id", workflowID, "error", err)
	} else {
		slog.Info("woke the launching chat at its workflow's iteration limit", "chat_id", parent, "workflow_id", workflowID)
	}
	if err := rs.leaseStore().SetLoopWakeState(ctx, workflowID, key, state); err != nil && !errors.Is(err, runlease.ErrNotFound) {
		slog.Warn("could not record the iteration-limit wake's outcome",
			"chat_id", parent, "workflow_id", workflowID, "state", state, "error", err)
	}
}

// loopLimitMessage is the wake's text for the parent agent and its transcript label.
func loopLimitMessage(runLabel, workflowID, nodeID string, iterations int) (text, label string) {
	name := runLabel
	if name == "" {
		name = workflowID
	}
	text = "Workflow " + strconv.Quote(name) + " (" + workflowID + ") paused: loop " + nodeID +
		" reached its iteration limit after " + strconv.Itoa(iterations) + " iterations. Decide: extend it " +
		"(update_workflow action extend_repeat, nodeId " + nodeID + "), end the loop (update_workflow action " +
		"update_status completed, nodeId " + nodeID + "), or fix something first."
	return text, "Loop limit reached: " + name
}

// Absent or unreadable is settings.DefaultLoopLimitWake.
func loopLimitWakeOn(ctx context.Context, configDir string) bool {
	on := settings.DefaultLoopLimitWake
	if !settings.FieldInto(ctx, configDir, settings.KeyLoopLimitWake, &on) {
		return settings.DefaultLoopLimitWake
	}
	return on
}
