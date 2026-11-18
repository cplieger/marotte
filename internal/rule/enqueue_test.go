package rule

import (
	"slices"
	"testing"
)

func queued(trigger, id, stage string) QueuedEvent {
	return QueuedEvent{Stage: stage, Trigger: trigger, EventID: id}
}

func idsOf(q []QueuedEvent) []string {
	out := make([]string, 0, len(q))
	for i := range q {
		out = append(out, q[i].EventID)
	}
	return out
}

// Enqueue is the one place a rule's queue grows, so every bound a queue keeps is pinned here: each event trigger's
// hold policy and cap, a time trigger's one waiting event, a rule's one waiting Run now press, and a prompt already
// in the chat that nothing drops.
func TestEnqueue(t *testing.T) {
	withTestEventKind(t)
	fifo3 := eventTrigger("e", 3)
	newest := eventTrigger("n", 10)
	newest.Limits.Hold = holdNewest
	spec := Spec{
		Name: "r", Prompt: "p", Enabled: true,
		Triggers: []Trigger{fifo3, newest, everyTrigger("t", 15), eventTrigger("one", 1)},
	}
	sentToChat := func(q QueuedEvent) QueuedEvent { q.Queued = true; q.MessageID = "m-1"; return q }

	for _, c := range []struct {
		name        string
		queue       []QueuedEvent
		add         QueuedEvent
		want        []string
		wantDropped int
		wantAdded   bool
	}{
		{
			name:  "fifo under its cap keeps every event in order",
			queue: []QueuedEvent{queued("e", "e1", StageNew), queued("e", "e2", StageFired)},
			add:   queued("e", "e3", StageNew), want: []string{"e1", "e2", "e3"}, wantAdded: true,
		},
		{
			name:  "fifo at its cap drops its oldest, both stages counted",
			queue: []QueuedEvent{queued("e", "e1", StageFired), queued("e", "e2", StageNew), queued("e", "e3", StageNew)},
			add:   queued("e", "e4", StageNew), want: []string{"e2", "e3", "e4"}, wantDropped: 1, wantAdded: true,
		},
		{
			name: "another trigger's events neither count nor go",
			queue: []QueuedEvent{
				queued("n", "n1", StageFired), queued("e", "e1", StageNew), queued("t", "t1", StageFired),
				queued("e", "e2", StageNew), queued("e", "e3", StageNew),
			},
			add: queued("e", "e4", StageNew), want: []string{"n1", "t1", "e2", "e3", "e4"}, wantDropped: 1, wantAdded: true,
		},
		{
			name:  "newest keeps only the latest",
			queue: []QueuedEvent{queued("n", "n1", StageFired), queued("n", "n2", StageNew)},
			add:   queued("n", "n3", StageNew), want: []string{"n3"}, wantDropped: 2, wantAdded: true,
		},
		{
			name:  "a prompt already in the chat is never dropped nor counted",
			queue: []QueuedEvent{sentToChat(queued("one", "o1", StageFired))},
			add:   queued("one", "o2", StageNew), want: []string{"o1", "o2"}, wantAdded: true,
		},
		{
			name:  "a time trigger's newer event replaces its waiting one",
			queue: []QueuedEvent{queued("t", "t1", StageFired), queued("e", "e1", StageNew)},
			add:   queued("t", "t2", StageFired), want: []string{"e1", "t2"}, wantDropped: 1, wantAdded: true,
		},
		{
			name:  "a time trigger's event beside one already in the chat",
			queue: []QueuedEvent{sentToChat(queued("t", "t1", StageFired))},
			add:   queued("t", "t2", StageFired), want: []string{"t1", "t2"}, wantAdded: true,
		},
		{
			name:  "a Run now press while one waits adds nothing",
			queue: []QueuedEvent{queued("", "manual:op-1", StageFired)},
			add:   queued("", "manual:op-2", StageFired), want: []string{"manual:op-1"},
		},
		{
			name:  "a Run now press once the last is in the chat",
			queue: []QueuedEvent{sentToChat(queued("", "manual:op-1", StageFired))},
			add:   queued("", "manual:op-2", StageFired), want: []string{"manual:op-1", "manual:op-2"}, wantAdded: true,
		},
		{
			name:  "a Run now press beside every trigger's events",
			queue: []QueuedEvent{queued("e", "e1", StageNew), queued("t", "t1", StageFired)},
			add:   queued("", "manual:op-1", StageFired), want: []string{"e1", "t1", "manual:op-1"}, wantAdded: true,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := Status{Queue: slices.Clone(c.queue), Dropped: 2}
			added := Enqueue(&st, &spec, &c.add)
			if added != c.wantAdded {
				t.Errorf("Enqueue = %v, want %v", added, c.wantAdded)
			}
			if got := idsOf(st.Queue); !slices.Equal(got, c.want) {
				t.Errorf("queue %v, want %v", got, c.want)
			}
			if st.Dropped != 2+c.wantDropped {
				t.Errorf("dropped %d, want %d more than the 2 before", st.Dropped, c.wantDropped)
			}
		})
	}
}
