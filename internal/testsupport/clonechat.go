package testsupport

import (
	"encoding/json"

	"github.com/cplieger/marotte/internal/marotte"
)

// cloneChat returns a chat that shares nothing with c, which is what both fakes'
// Get must hand out for "Mutate is the only write path" to mean anything.
//
// Round-trips through JSON deliberately: that is exactly how the real store
// achieves independence (chat.Store.Get decodes the header file, so its result is
// new bytes every time), so this cannot drift from it the way a hand-written clone
// would — Chat has several slice fields, and a `clone := *c` shallow copy shares
// every slice header inside it, so a caller could edit a stored attachment list
// with no Mutate anywhere. The contract suite's Get_returns_an_independent_copy
// case fails if this is reverted.
//
// A marshal error is unreachable for a wire type (no channels, funcs, or
// cycles), so the shallow copy is a safe fallback rather than a real path.
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
