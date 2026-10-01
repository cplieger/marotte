package chat

import (
	"context"
	"encoding/json"

	"github.com/cplieger/marotte/internal/marotte"
)

// RewindTarget resolves the prompt id a rewind addresses to the earliest turn that
// prompt opened and the id KAS holds the prompt under. False when no turn's prompt
// carries that id. One prompt id can open several turns (a re-send, the empty-turn
// retry); the revert drops the first and everything after it, and the KAS id is the
// bind on any of those turns whose session is the chat's current one, because a
// bind is valid only on the session that minted it. Absent, the target has no KAS id.
func (s *Store) RewindTarget(ctx context.Context, chatID marotte.ChatID, promptID string) (marotte.RewindTarget, bool, error) {
	var target marotte.RewindTarget
	found := false
	err := s.readLog(ctx, chatID, func(l *EntryLog) error {
		c, err := s.load(ctx, chatID)
		if err != nil {
			return err
		}
		entries, err := l.All()
		if err != nil {
			return err
		}
		target, found = rewindTargetIn(entries, promptID, c.ACPSessionID)
		return nil
	})
	return target, found, err
}

// rewindTargetIn is RewindTarget's scan over a whole log: the earliest turn_open
// carrying promptID leads, the bind on any of those turns whose session is the
// current one supplies the KAS id, and a run is inside the revert's window when its
// LAUNCHING call is at or after that turn_open. The window reaches from that turn to
// the newest one, and readers skip it, so a run launched before the window stays
// untouched however often a later entry mentions it.
func rewindTargetIn(entries []marotte.Entry, promptID, session string) (marotte.RewindTarget, bool) {
	sc := rewindScan{
		promptID: promptID, session: session,
		opened: map[string]bool{}, mentioned: map[string]bool{}, preCutTools: map[string]bool{},
	}
	for i := range entries {
		sc.visit(&entries[i])
	}
	return sc.target, sc.found
}

// rewindScan is rewindTargetIn's state over one file-order pass.
type rewindScan struct {
	opened      map[string]bool
	mentioned   map[string]bool
	preCutTools map[string]bool
	promptID    string
	session     string
	target      marotte.RewindTarget
	found       bool
}

func (sc *rewindScan) visit(e *marotte.Entry) {
	switch e.Kind {
	case marotte.EntryKindTurnOpen:
		sc.visitOpen(e)
	case marotte.EntryKindTurnBind:
		sc.visitBind(e)
	case marotte.EntryKindToolCall, marotte.EntryKindToolResult:
		sc.visitTool(e)
	}
}

func (sc *rewindScan) visitOpen(e *marotte.Entry) {
	open, ok := promptOf(e)
	if !ok || open.Prompt.ID != sc.promptID {
		return
	}
	sc.opened[e.Turn] = true
	if !sc.found {
		sc.target, sc.found = marotte.RewindTarget{Turn: e.Turn}, true
	}
}

func (sc *rewindScan) visitBind(e *marotte.Entry) {
	var bind marotte.EntryTurnBind
	if sc.opened[e.Turn] && json.Unmarshal(e.Payload, &bind) == nil && bind.SessionID == sc.session {
		sc.target.KASMessageID = bind.KASMessageID
	}
}

// visitTool decides, once per run, whether the revert un-says its launch. The FIRST
// entry carrying a run's workflow id is the launch: run_workflow and
// inspect_workflow answer identically on the wire, so a later mention is a run the
// revert did not start. The launch is where its tool_call was issued, which is what a
// tool_result inside the window inherits from a call before it.
func (sc *rewindScan) visitTool(e *marotte.Entry) {
	if !sc.found {
		sc.preCutTools[e.ID] = true
	}
	var payload struct {
		WorkflowID string `json:"workflow_id"`
	}
	if json.Unmarshal(e.Payload, &payload) != nil || payload.WorkflowID == "" || sc.mentioned[payload.WorkflowID] {
		return
	}
	sc.mentioned[payload.WorkflowID] = true
	if !sc.preCutTools[toolCallIDOfResult(e.ID)] {
		sc.target.LaunchedRuns = append(sc.target.LaunchedRuns, payload.WorkflowID)
	}
}

// PromptAttachmentPaths is every attachment path on the log's prompts after the
// compaction watermark entry (the whole log when watermark is ""), in turn order.
func (s *Store) PromptAttachmentPaths(ctx context.Context, chatID marotte.ChatID, watermark string) ([]string, error) {
	var paths []string
	err := s.readLog(ctx, chatID, func(l *EntryLog) error {
		entries, err := l.All()
		if err != nil {
			return err
		}
		for i := range entries {
			e := &entries[i]
			if e.Kind == marotte.EntryKindCompaction && e.ID == watermark {
				paths = paths[:0]
				continue
			}
			open, ok := promptOf(e)
			if !ok {
				continue
			}
			for _, a := range open.Prompt.Attachments {
				paths = append(paths, a.Path)
			}
		}
		return nil
	})
	return paths, err
}

// PromptTexts is every prompt text the chat's log holds, in turn order.
func (s *Store) PromptTexts(ctx context.Context, chatID marotte.ChatID) ([]string, error) {
	var texts []string
	err := s.readLog(ctx, chatID, func(l *EntryLog) error {
		entries, err := l.All()
		if err != nil {
			return err
		}
		for i := range entries {
			if open, ok := promptOf(&entries[i]); ok {
				texts = append(texts, open.Prompt.Text)
			}
		}
		return nil
	})
	return texts, err
}

// EmptyCompactions counts the chat's empty-summary compaction entries, which
// numbers the next one's id.
func (s *Store) EmptyCompactions(ctx context.Context, chatID marotte.ChatID) (int, error) {
	n := 0
	err := s.readLog(ctx, chatID, func(l *EntryLog) error {
		entries, err := l.All()
		if err != nil {
			return err
		}
		for i := range entries {
			e := &entries[i]
			if e.Kind != marotte.EntryKindCompaction {
				continue
			}
			var c marotte.EntryCompaction
			if json.Unmarshal(e.Payload, &c) == nil && c.Summary == "" {
				n++
			}
		}
		return nil
	})
	return n, err
}

// TurnCount is the header's turn_count; false for a chat with no header.
func (s *Store) TurnCount(ctx context.Context, chatID marotte.ChatID) (uint64, bool) {
	c, ok := s.Get(ctx, chatID)
	if !ok {
		return 0, false
	}
	return uint64(max(c.TurnCount, 0)), true
}

// promptOf decodes a turn_open that carries a prompt; false for any other entry.
func promptOf(e *marotte.Entry) (marotte.EntryTurnOpen, bool) {
	var open marotte.EntryTurnOpen
	if e.Kind != marotte.EntryKindTurnOpen || json.Unmarshal(e.Payload, &open) != nil || open.Prompt == nil {
		return marotte.EntryTurnOpen{}, false
	}
	return open, true
}
