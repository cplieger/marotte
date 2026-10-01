package chat

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// BenchmarkStore_Append measures one sealed text entry landing in an open turn: the
// append, its seq assignment and the fdatasync the seal costs.
func BenchmarkStore_Append(b *testing.B) {
	s, err := NewStore(b.TempDir())
	if err != nil {
		b.Fatalf("NewStore: %v", err)
	}
	chatID := marotte.ChatID("bench-chat")
	ctx := b.Context()
	opened, err := s.OpenTurn(ctx, chatID, &TurnSpec{
		Source: marotte.TurnOpenNamePrompt,
		Prompt: &marotte.EntryPrompt{ID: "m-1", Text: "hello"},
	}, func(c *marotte.Chat) { c.Name = "benchmark chat" })
	if err != nil {
		b.Fatalf("setup OpenTurn: %v", err)
	}
	text := strings.Repeat("benchmark content ", 28)

	b.ReportAllocs()
	i := 0
	for b.Loop() {
		e := entryOf(opened.Turn, "", fmt.Sprintf("say-%d", i), marotte.EntryKindText, marotte.EntryText{Text: text})
		if err := s.Append(ctx, chatID, e); err != nil {
			b.Fatalf("Append: %v", err)
		}
		i++
	}
}
