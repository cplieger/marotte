package rule

// Enqueue adds ev to the rule's queue under its trigger's bounds and answers whether it was added: a Run now press
// while another waits adds nothing; a time trigger's event replaces its waiting one (design §7.2); an event
// trigger's keeps the newest only or drops its oldest at HoldCap (§8.3). A dropped event counts in Dropped. An event
// already in the chat (Queued) is no longer waiting, so nothing drops or counts it.
func Enqueue(st *Status, spec *Spec, ev *QueuedEvent) bool {
	waiting := func(q *QueuedEvent) bool { return !q.Queued && q.Trigger == ev.Trigger }
	n := 0
	for i := range st.Queue {
		if waiting(&st.Queue[i]) {
			n++
		}
	}
	keep, ok := heldBeside(spec, ev.Trigger)
	if !ok || (ev.Trigger == "" && n > 0) {
		return false
	}
	drop := n - keep
	out := make([]QueuedEvent, 0, len(st.Queue)+1)
	for i := range st.Queue {
		if drop > 0 && waiting(&st.Queue[i]) {
			drop--
			st.Dropped++
			continue
		}
		out = append(out, st.Queue[i])
	}
	out = append(out, *ev)
	st.Queue = out
	return true
}

// heldBeside answers how many of trigger's waiting events a new one keeps, and false for a trigger the spec lacks;
// a Run now press ("") keeps none.
func heldBeside(spec *Spec, trigger string) (int, bool) {
	if trigger == "" {
		return 0, true
	}
	for i := range spec.Triggers {
		t := &spec.Triggers[i]
		if t.ID != trigger {
			continue
		}
		if lim := t.Limits; lim != nil && !kinds[t.Kind].time && lim.Hold != holdNewest {
			return max(lim.HoldCap, 1) - 1, true
		}
		return 0, true
	}
	return 0, false
}
