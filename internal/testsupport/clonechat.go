package testsupport

import (
	"encoding/json"

	"github.com/cplieger/marotte/internal/marotte"
)

// cloneChat returns a chat that shares nothing with c, via a JSON round trip, which is how
// the real store's Get achieves independence (a `*c` copy would share every slice header).
// A marshal error is unreachable for a wire type, so the shallow copy is a safe fallback.
func cloneChat(c *marotte.Chat) *marotte.Chat {
	shallow := *c
	data, err := json.Marshal(c)
	if err != nil {
		return &shallow
	}
	var out marotte.Chat
	if err := json.Unmarshal(data, &out); err != nil {
		return &shallow
	}
	return &out
}
