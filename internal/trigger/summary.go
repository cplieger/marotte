package trigger

import (
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
)

// HeldEvent is one held event as the rule list shows it.
type HeldEvent struct {
	ObservedAt time.Time `json:"observed_at"`
	Key        string    `json:"key"`
}

// CursorSummary is an event rule's cursor as the rule list shows it: counts and sentences, never item lists.
type CursorSummary struct {
	// Blocked is why the poller holds the rule's launches, empty when nothing does.
	Blocked string `json:"blocked,omitempty"`
	// Notes name the watched runs the row should mention.
	Notes []string `json:"notes,omitempty"`
	// Items counts a labeled rule's tracked items.
	Items int `json:"items,omitempty"`
	// Floor is an opened rule's floor: every item at or below it is accounted for.
	Floor int `json:"floor,omitempty"`
	// Watching and Fired count a run rule's runs still to be decided and runs already queued.
	Watching  int  `json:"watching,omitempty"`
	Fired     int  `json:"fired,omitempty"`
	Baselined bool `json:"baselined"`
}

// Summarize reads an event rule's state for the rule list at now; an error is a state that does not decode.
func Summarize(st *launchrule.State, now time.Time) ([]HeldEvent, CursorSummary, error) {
	es, err := decodeState(st)
	if err != nil {
		return nil, CursorSummary{}, err
	}
	held := make([]HeldEvent, 0, len(es.queue))
	for i := range es.queue {
		held = append(held, HeldEvent{Key: es.queue[i].Key, ObservedAt: es.queue[i].ObservedAt})
	}
	var c CursorSummary
	for _, rc := range es.cursor {
		if rc == nil || rc.empty() {
			continue
		}
		c.Baselined = true
		if l := rc.Labeled; l != nil {
			c.Items += len(l.Items)
			if l.TooMany {
				c.Blocked = tooManyOpen
			}
		}
		if o := rc.Opened; o != nil {
			c.Floor = max(c.Floor, o.Floor)
		}
		if r := rc.Runs; r != nil {
			c.Watching += len(r.Runs)
			c.Fired += len(r.Fired)
			c.Notes = append(c.Notes, runNotes(r, now)...)
		}
	}
	return held, c, nil
}
