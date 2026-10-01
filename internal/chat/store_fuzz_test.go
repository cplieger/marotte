package chat

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// The header round-trips whatever name a Mutate wrote, or Get answers a record the
// writer never produced. A name Mutate refuses (invalid UTF-8) is not a round trip.
func FuzzStore_MutateGetRoundTrip(f *testing.F) {
	f.Add("test-chat")
	f.Add("")
	f.Add("unicode-名前")
	f.Add("ctrl\x01chars")
	f.Add("long-name-" + string(make([]byte, 200)))

	f.Fuzz(func(t *testing.T, name string) {
		s, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		chatID := marotte.ChatID("fuzz-chat-1")
		ctx := t.Context()

		if _, err := s.Mutate(ctx, chatID, func(c *marotte.Chat, _ bool) bool {
			c.Name = name
			return true
		}); err != nil {
			return
		}
		chat, ok := s.Get(ctx, chatID)
		if !ok {
			t.Fatal("Get returned false after Mutate")
		}
		if chat.Name != name {
			t.Fatalf("Get name = %q, want %q", chat.Name, name)
		}
	})
}
