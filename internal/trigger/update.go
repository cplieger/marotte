package trigger

import "errors"

// anyGeneration is the generation of an update that holds whatever edit came before it.
const anyGeneration = -1

// update applies fn to the rule's in-memory state under the rule's mutex and, when fn reports a change or a write
// of the rule failed, has step write the whole value (E10). A step that runs between the two writes the change too,
// so the order changes nothing. A rule launch-rules.json does not hold changes nothing, so no value is loaded for it,
// and nor does an unreadable rule or one edited since generation gen, unless gen is anyGeneration.
func (c *Confirmer) update(ruleID string, gen int, fn func(es *eventState) bool) {
	if _, ok := c.store.Get(ruleID); !ok {
		return
	}
	rv := c.lock(ruleID)
	if rv.unreadable != "" || (gen != anyGeneration && gen != rv.generation) {
		rv.mu.Unlock()
		return
	}
	retry := fn(&rv.es) || rv.writeBlocked != ""
	rv.mu.Unlock()
	if retry {
		c.deliver(ruleID, &attemptEvent{kind: evWriteRetry})
	}
}

// errRuleUnreadable is the snapshot of a rule whose state file would not load, which a poll skips.
var errRuleUnreadable = errors.New("the rule's state file is unreadable")

// snapshot is a deep copy of the rule's in-memory state with its generation, for a reader that must not hold the
// rule's mutex.
func (c *Confirmer) snapshot(ruleID string) (es eventState, gen int, err error) {
	rv := c.lock(ruleID)
	if rv.unreadable != "" {
		rv.mu.Unlock()
		return eventState{}, 0, errRuleUnreadable
	}
	st, err := rv.es.encode()
	gen = rv.generation
	rv.mu.Unlock()
	if err != nil {
		return eventState{}, 0, err
	}
	es, err = decodeState(&st)
	return es, gen, err
}
