package testsupport

import (
	"context"
	"fmt"

	"github.com/cplieger/marotte/internal/marotte"
)

// Neither fake holds an entry log, so the log reads answer as an empty log does;
// a test whose subject is a log read stages its own double.

// PromptTexts answers no prompts.
func (s *RecordingChatStore) PromptTexts(context.Context, marotte.ChatID) ([]string, error) {
	return nil, nil
}

// EmptyCompactions answers zero.
func (s *RecordingChatStore) EmptyCompactions(context.Context, marotte.ChatID) (int, error) {
	return 0, nil
}

// PromptTexts answers no prompts.
func (s *InMemoryChatStore) PromptTexts(context.Context, marotte.ChatID) ([]string, error) {
	return nil, nil
}

// EmptyCompactions answers zero.
func (s *InMemoryChatStore) EmptyCompactions(context.Context, marotte.ChatID) (int, error) {
	return 0, nil
}

// Revert refuses: an empty log holds no turn to revert to.
func (s *InMemoryChatStore) Revert(_ context.Context, id marotte.ChatID, turn, _ string) (record, opened *marotte.Entry, err error) {
	return nil, nil, fmt.Errorf("testsupport: chat %s holds no turn %q", id, turn)
}

// RewindTarget answers no prompt.
func (s *InMemoryChatStore) RewindTarget(context.Context, marotte.ChatID, string) (marotte.RewindTarget, bool, error) {
	return marotte.RewindTarget{}, false, nil
}

// PromptAttachmentPaths answers no paths.
func (s *InMemoryChatStore) PromptAttachmentPaths(context.Context, marotte.ChatID, string) ([]string, error) {
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
