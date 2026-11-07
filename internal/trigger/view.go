package trigger

import (
	"slices"

	"github.com/cplieger/marotte/internal/launchrule"
)

// View is the event rule's state as the Confirmer holds it, which carries what no write persisted: a row text set
// by a row that writes nothing, and a write-failure block, shown over the rule's own block since nothing launches
// until a write lands. It answers what Store.State does for a refused definition, the rule's
// *launchrule.UnreadableStateError for a state file that would not load, and launchrule.ErrNotFound when no event
// rule has the id.
func (c *Confirmer) View(ruleID string) (launchrule.State, error) {
	if def, ok := c.store.Get(ruleID); !ok || def.When.Event == nil {
		return launchrule.State{}, launchrule.ErrNotFound
	}
	if err := c.store.Refused(ruleID); err != nil {
		return launchrule.State{}, err
	}
	rv := c.lock(ruleID)
	defer rv.mu.Unlock()
	if rv.unreadable != nil {
		return launchrule.State{}, rv.unreadable
	}
	st, err := rv.es.encode()
	if err == nil {
		st.Attempts, err = encodeAttempts(rv.records)
	}
	if err != nil {
		return launchrule.State{}, err
	}
	st.Status.Gaps = slices.Clone(st.Status.Gaps)
	if rv.writeBlocked != "" {
		st.Status.Blocked = rv.writeBlocked
	}
	return st, nil
}
