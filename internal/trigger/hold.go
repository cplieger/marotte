package trigger

import (
	"fmt"
	"slices"
	"strings"
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
	logEventMatched(refOf(pr.rule), e.Key, len(pr.st.queue))
	return true
}

// supersede replaces every queued entry of e's coalesce key with e, except one in flight: the newer event is then
// queued beside it, since its run may already be starting and a supersede would launch a second one.
func (pr *pollRule) supersede(e *queueEntry) {
	es := &pr.st
	kept := es.queue[:0]
	for i := range es.queue {
		o := es.queue[i]
		if o.CoalesceKey != e.CoalesceKey || pr.inFlight(&o) {
			kept = append(kept, o)
			continue
		}
		es.supersedeBy(o.Key, e.Key)
	}
	es.queue = kept
}

// logFull warns of a poll that left a match unqueued, once until a poll finds the queue with room.
func (pr *pollRule) logFull() {
	if pr.full && !pr.st.fullLogged {
		logQueueFull(refOf(pr.rule), len(pr.st.queue), pr.ev.Hold.Cap)
		pr.st.fullLogged = true
	}
}

const queueFullPrefix = "queue full: "

func queueFullText(held int) string {
	events := fmt.Sprintf("%d events", held)
	if held == 1 {
		events = "1 event"
	}
	return queueFullPrefix + events + " held, newer events wait at the forge"
}

// showFull puts a full poll on the row and takes it off after a poll that left nothing at the forge. A launch or
// hold sentence another row wrote outranks it, so it is neither replaced nor cleared.
func (pr *pollRule) showFull() {
	st := &pr.st.raw.Status
	shown := strings.HasPrefix(st.RowText, queueFullPrefix)
	switch {
	case pr.full && (st.RowText == "" || shown):
		st.RowText = queueFullText(len(pr.st.queue))
	case !pr.full && shown:
		st.RowText = ""
	}
}

// eligible answers the keys of the rule's queue the launcher may start now: none while the rule is blocked or any
// entry is in flight, since a recipe runs one run at a time; else a fifo rule's head alone, so a later entry never
// launches ahead of it, and every entry of a newest rule.
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
