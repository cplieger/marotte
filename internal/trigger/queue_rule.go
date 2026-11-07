package trigger

import (
	"slices"

	"github.com/cplieger/marotte/internal/launchrule"
)

// requeue is Q, for a key whose flight or live record ended while it stays queued: the entry keeps its place, unless
// the rule is newest and a newer entry of its coalesce key is queued, which then supersedes it.
func (s *stepper) requeue(key string) {
	es := &s.rv.es
	i := slices.IndexFunc(es.queue, func(e queueEntry) bool { return e.Key == key })
	if i < 0 || s.def.When.Event == nil || s.def.When.Event.Hold == nil ||
		s.def.When.Event.Hold.Policy != launchrule.HoldNewest {
		return
	}
	newestAt := -1
	for j := i + 1; j < len(es.queue); j++ {
		if es.queue[j].CoalesceKey == es.queue[i].CoalesceKey {
			newestAt = j
		}
	}
	if newestAt < 0 {
		return
	}
	es.supersedeBy(key, es.queue[newestAt].Key)
	es.queue = slices.Delete(es.queue, i, i+1)
}

// supersedeBy records key as replaced by the newer key by. Every key key had replaced is re-pointed at by, so each
// superseded key names the newest entry of its coalesce key and gains its run once it is confirmed.
func (es *eventState) supersedeBy(key, by string) {
	for j := range es.superseded {
		if es.superseded[j].By == key {
			es.superseded[j].By = by
		}
	}
	es.superseded = append(es.superseded, supersededEntry{Key: key, By: by})
	if n := len(es.superseded) - maxSuperseded; n > 0 {
		es.superseded = slices.Delete(es.superseded, 0, n)
	}
}
