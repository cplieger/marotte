package marotte

// Whether this account can use a model id: kiro-cli accepts any id locally and the SERVICE rejects
// it mid-prompt on every later turn, so the id is checked against the session's advertised set
// first, by ONE predicate (KiroCrew #1596, #1549, #1550). marotte's exposure is `last_model`,
// restored with no list check.

import "slices"

// ModelServed reports whether id is one the session can run. It fails open on an EMPTY served set
// (no catalog advertised) and an EMPTY id, the only inherit value: `auto` is an id KAS pins and lists
// like any other. served must be the UNFILTERED set (Chat.ServedModelIDs, written by ApplyServedModels),
// or a deprecated model the account can still use is refused.
func ModelServed(id string, served []string) bool {
	if id == "" || len(served) == 0 {
		return true
	}
	return slices.Contains(served, id)
}
