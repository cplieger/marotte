package trigger

import (
	"log/slog"
	"slices"
	"sync"

	"github.com/cplieger/go/keyenc"
	"github.com/cplieger/marotte/internal/launchrule"
)

// maxSuperseded is how many replaced keys a rule keeps, newest last; a key in the list is never queued again.
const maxSuperseded = 50

// launchMarks are the queue entries a launch is in flight for, in memory only: after a restart the attempts the
// state file holds stand in for them.
type launchMarks struct {
	keys map[string]struct{}
	mu   sync.Mutex
}

func newLaunchMarks() *launchMarks { return &launchMarks{keys: map[string]struct{}{}} }

// mark marks the rule's entry key launching until the returned done runs.
//
//deadset:ignore DS1102 -- the launcher marks each entry it starts; until that lands only the tests do
func (m *launchMarks) mark(ruleID, key string) (done func()) {
	k := keyenc.Join(ruleID, key)
	m.mu.Lock()
	m.keys[k] = struct{}{}
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		delete(m.keys, k)
		m.mu.Unlock()
	}
}

func (m *launchMarks) has(ruleID, key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.keys[keyenc.Join(ruleID, key)]
	return ok
}

// inFlight reports whether the entry's launch has started: it is marked launching or holds a live record.
func (pr *pollRule) inFlight(e *queueEntry) bool {
	return e.Live != "" || pr.marks.has(pr.rule.ID, e.Key)
}

// hold applies the rule's hold policy to a newly matched entry, answering false when the queue is full and the
// event is left at the forge. An entry whose key is already accounted for is not queued again.
func (pr *pollRule) hold(e *queueEntry) bool {
	if pr.st.knows(e.Key) {
		return true
	}
	if pr.ev.Hold.Policy == launchrule.HoldNewest {
		pr.supersede(e)
	}
	if len(pr.st.queue) >= pr.ev.Hold.Cap {
		pr.full = true
		return false
	}
	pr.st.queue = append(pr.st.queue, *e)
	slog.Info(logMsgEventMatched, append(pr.attrs(), "key", e.Key, "held", len(pr.st.queue))...)
	return true
}

// supersede replaces every queued entry of e's coalesce key with e, except one in flight: the newer event is then
// queued beside it, since its run may already be starting and a supersede would launch a second one. Each
// superseded key names the newest entry of its coalesce key, so every one gains that entry's run once it is
// confirmed.
func (pr *pollRule) supersede(e *queueEntry) {
	es := &pr.st
	kept := es.queue[:0]
	for i := range es.queue {
		o := es.queue[i]
		if o.CoalesceKey != e.CoalesceKey || pr.inFlight(&o) {
			kept = append(kept, o)
			continue
		}
		for j := range es.superseded {
			if es.superseded[j].By == o.Key {
				es.superseded[j].By = e.Key
			}
		}
		es.superseded = append(es.superseded, supersededEntry{Key: o.Key, By: e.Key})
	}
	es.queue = kept
	if n := len(es.superseded) - maxSuperseded; n > 0 {
		es.superseded = slices.Delete(es.superseded, 0, n)
	}
}

// logFull warns once per poll that left a match unqueued.
func (pr *pollRule) logFull() {
	if pr.full {
		slog.Warn(logMsgQueueFull, append(pr.attrs(), "held", len(pr.st.queue), "cap", pr.ev.Hold.Cap)...)
	}
}

// eligible answers the keys of the rule's queue the launcher may start now: none while the rule is blocked or any
// entry is in flight, since a recipe runs one run at a time; else a fifo rule's head alone, so a later entry never
// launches ahead of it, and every entry of a newest rule.
//
//deadset:ignore DS1102 -- the launcher picks its next entry through it; until that lands only the tests do
func (pr *pollRule) eligible() []string {
	es := &pr.st
	if len(es.queue) == 0 || pr.blocked() != "" || slices.ContainsFunc(es.queue, func(e queueEntry) bool {
		return pr.inFlight(&e)
	}) {
		return nil
	}
	if pr.ev.Hold.Policy == launchrule.HoldFIFO {
		return []string{es.queue[0].Key}
	}
	return keysOf(es.queue)
}

func keysOf(q []queueEntry) []string {
	out := make([]string, 0, len(q))
	for i := range q {
		out = append(out, q[i].Key)
	}
	return out
}
