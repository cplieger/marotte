package trigger

// update applies fn to the rule's in-memory state under the rule's mutex and, when fn reports a change, has step
// write the whole value (E10). A step that runs between the two writes the change too, so the order changes nothing.
func (c *Confirmer) update(ruleID string, fn func(es *eventState) bool) {
	rv := c.lock(ruleID)
	changed := fn(&rv.es)
	rv.mu.Unlock()
	if changed {
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
