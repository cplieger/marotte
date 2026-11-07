package trigger

import (
	"slices"
	"time"
)

// ttlInterval is how often the TTL ticker looks for records past abandonedTTL, so a due record goes within
// abandonedTTL plus one interval of its cancel.
const ttlInterval = time.Hour

// expireAbandoned delivers ttl to each rule holding a record whose cancel landed.
func (c *Confirmer) expireAbandoned() {
	for id, rv := range c.loaded() {
		rv.mu.Lock()
		due := slices.ContainsFunc(rv.records, func(r *record) bool { return r.state == stateAbandonedCancelled })
		rv.mu.Unlock()
		if due {
			c.deliver(id, &attemptEvent{kind: evTTL})
		}
	}
}
