package chat

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func BenchmarkStore_AppendMessage(b *testing.B) {
	dir := b.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		b.Fatalf("NewStore: %v", err)
	}

	chatID := marotte.ChatID("bench-chat")
	ctx := b.Context()

	// Create chat with 10 pre-existing messages.
	_, err = s.Mutate(ctx, chatID, func(c *marotte.Chat, _ bool) bool {
		c.Name = "benchmark chat"
		for i := range 10 {
			c.Messages = append(c.Messages, marotte.Message{
				ID:      fmt.Sprintf("pre-%d", i),
				Role:    marotte.RoleAssistant,
				Content: strings.Repeat("x", 200),
			})
		}
		return true
	})
	if err != nil {
		b.Fatalf("setup Mutate: %v", err)
	}

	// Realistic message payload (~500 bytes content).
	msg := &marotte.Message{
		ID:      "bench-msg",
		Role:    marotte.RoleAssistant,
		Content: strings.Repeat("benchmark content ", 28), // ~504 bytes
	}

	// No b.ResetTimer: b.Loop resets the timer itself on its first call, so the
	// call here was dead. No tool reports this — there is no bloop analyzer in
	// go fix's 26 modernizers and golangci-lint's modernize is the same analyzer.
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		msg.ID = fmt.Sprintf("bench-%d", i)
		if err := s.AppendMessage(ctx, chatID, msg); err != nil {
			b.Fatalf("AppendMessage: %v", err)
		}
		i++
	}
}
