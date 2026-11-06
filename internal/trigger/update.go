package trigger

// update applies fn to the rule's in-memory state under the rule's mutex and, when fn reports a change or a write
// of the rule failed, has step write the whole value (E10). A step that runs between the two writes the change too,
// so the order changes nothing. A rule launch-rules.json does not hold changes nothing, so no value is loaded for it.
func (c *Confirmer) update(ruleID string, fn func(es *eventState) bool) {
	if _, ok := c.store.Get(ruleID); !ok {
		return
	}
	rv := c.lock(ruleID)
	retry := fn(&rv.es) || rv.writeBlocked != ""
	rv.mu.Unlock()
	if retry {
		c.deliver(ruleID, &attemptEvent{kind: evWriteRetry})
	}
}

// snapshot is a deep copy of the rule's in-memory state, for a reader that must not hold the rule's mutex.
func (c *Confirmer) snapshot(ruleID string) (eventState, error) {
	rv := c.lock(ruleID)
	st, err := rv.es.encode()
	rv.mu.Unlock()
	if err != nil {
		return eventState{}, err
	}
	return decodeState(&st)
}
