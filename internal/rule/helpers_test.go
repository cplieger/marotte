package rule

import "testing"

// testEventKind is an event kind with the field shapes the later kinds bring (a string, a number, a list), so the
// rules that bind every event kind are tested before the first one ships.
const testEventKind TriggerKind = "test_event"

// withTestEventKind adds testEventKind to the kind table for t. Tests that call it never run in parallel.
func withTestEventKind(t *testing.T) {
	t.Helper()
	kinds[testEventKind] = kindDef{fields: []string{"author", "title", "number", "labels"}, lists: []string{"labels"}}
	t.Cleanup(func() { delete(kinds, testEventKind) })
}

func eventTrigger(id string, holdCap int) Trigger {
	return Trigger{ID: id, Kind: testEventKind, Limits: &TriggerLimits{MaxPerHour: 12, Hold: HoldFIFO, HoldCap: holdCap}}
}

func everyTrigger(id string, minutes int) Trigger {
	return Trigger{ID: id, Kind: KindEvery, Every: &EveryTrigger{Minutes: minutes, Anchor: AnchorLast}}
}

func atTrigger(id string, at AtTrigger) Trigger {
	return Trigger{ID: id, Kind: KindAt, At: &at}
}

func leaf(l Leaf) Cond { return Cond{Leaf: &l} }

func all(children ...Cond) Cond { return Cond{Op: OpAll, Children: children} }

func anyOf(children ...Cond) Cond { return Cond{Op: OpAny, Children: children} }

func busy() Cond { return leaf(Leaf{Kind: LeafBotBusy}) }

func by(ids ...string) Cond { return leaf(Leaf{Kind: LeafTriggeredBy, By: &ByLeaf{Triggers: ids}}) }

func busyN(n int) []Cond {
	out := make([]Cond, n)
	for i := range out {
		out[i] = busy()
	}
	return out
}

func field(name string, op FieldOp, value string) Cond {
	return leaf(Leaf{Kind: leafEventField, Event: &EventLeaf{Field: name, Op: op, Value: value}})
}
