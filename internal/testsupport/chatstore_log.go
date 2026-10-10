package testsupport

import (
	"context"
	"fmt"

	"github.com/cplieger/marotte/internal/marotte"
)

// Neither fake holds an entry log, so log reads answer as an empty log does.

// PromptTexts answers no prompts.
func (*RecordingChatStore) PromptTexts(context.Context, marotte.ChatID) ([]string, error) {
	return nil, nil
}

// EmptyCompactions answers zero.
func (*RecordingChatStore) EmptyCompactions(context.Context, marotte.ChatID) (int, error) {
	return 0, nil
}

// PromptTexts answers no prompts.
func (*InMemoryChatStore) PromptTexts(context.Context, marotte.ChatID) ([]string, error) {
	return nil, nil
}

// EmptyCompactions answers zero.
func (*InMemoryChatStore) EmptyCompactions(context.Context, marotte.ChatID) (int, error) {
	return 0, nil
}

// WaitingWorkflowMessages answers none.
func (*RecordingChatStore) WaitingWorkflowMessages(context.Context, marotte.ChatID) ([]marotte.WorkflowMessage, error) {
	return nil, nil
}

// WaitingWorkflowMessages answers none.
func (*InMemoryChatStore) WaitingWorkflowMessages(context.Context, marotte.ChatID) ([]marotte.WorkflowMessage, error) {
	return nil, nil
}

// DepartedName answers false: neither fake keeps a tombstone.
func (*InMemoryChatStore) DepartedName(marotte.ChatID) (string, bool) { return "", false }

// DepartedName answers false: neither fake keeps a tombstone.
func (*RecordingChatStore) DepartedName(marotte.ChatID) (string, bool) { return "", false }

// Revert refuses: an empty log holds no turn to revert to.
func (*InMemoryChatStore) Revert(_ context.Context, id marotte.ChatID, turn, _ string) (record *marotte.Entry, minted []*marotte.Entry, err error) {
	return nil, nil, fmt.Errorf("testsupport: chat %s holds no turn %q", id, turn)
}

// RewindTarget answers no prompt.
func (*InMemoryChatStore) RewindTarget(context.Context, marotte.ChatID, string) (marotte.RewindTarget, bool, error) {
	return marotte.RewindTarget{}, false, nil
}

// PromptReceipt answers a prompt the log never held.
func (*InMemoryChatStore) PromptReceipt(context.Context, marotte.ChatID, string) (marotte.PromptReceipt, error) {
	return marotte.PromptReceipt{}, nil
}

// PromptAttachmentPaths answers no paths.
func (*InMemoryChatStore) PromptAttachmentPaths(context.Context, marotte.ChatID, string) ([]string, error) {
	return nil, nil
}

// TurnCount is the header's turn_count, as the real store reads it.
func (s *InMemoryChatStore) TurnCount(ctx context.Context, id marotte.ChatID) (uint64, bool) {
	c, ok := s.Get(ctx, id)
	if !ok {
		return 0, false
	}
	return uint64(max(c.TurnCount, 0)), true
}
