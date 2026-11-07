package trigger

import (
	"cmp"
	"maps"
	"slices"
	"time"
)

const (
	// closedItemTTL is how long a labeled entry outlives its item's absence from complete walks. An item reopened
	// inside it with its label still on is no new gain.
	closedItemTTL = 30 * 24 * time.Hour
	// maxLabeledEntries caps a labeled cursor. The cap prunes only entries absent from the last complete walk:
	// a pruned open item carrying the label would fire as a gain at its next read.
	maxLabeledEntries = 5000
)

// tooManyOpen is the block of a labeled rule whose last complete walk read more than maxLabeledEntries open items.
const tooManyOpen = "more than 5,000 open items"

// pruneLabeled runs after a complete walk that read open items: the 30-day drop, then the cap, oldest last_seen
// first among the absent entries.
func (pr *pollRule) pruneLabeled(lc *labeledCursor, open int) {
	lc.TooMany = open > maxLabeledEntries
	maps.DeleteFunc(lc.Items, func(_ int, e *labelEntry) bool { return e.LastSeen.Before(pr.start.Add(-closedItemTTL)) })
	over := len(lc.Items) - maxLabeledEntries
	if over <= 0 {
		return
	}
	var absent []int
	for n, e := range lc.Items {
		if !e.LastSeen.Equal(pr.start) {
			absent = append(absent, n)
		}
	}
	slices.SortFunc(absent, func(a, b int) int {
		return cmp.Or(lc.Items[a].LastSeen.Compare(lc.Items[b].LastSeen), cmp.Compare(a, b))
	})
	for _, n := range absent[:min(over, len(absent))] {
		delete(lc.Items, n)
	}
}

// blocked is why the rule may launch nothing now from what the poller saw, empty when nothing holds it.
func (pr *pollRule) blocked() string {
	if rc := pr.st.cursor[pr.feed.repoID()]; rc != nil && rc.Labeled != nil && rc.Labeled.TooMany {
		return tooManyOpen
	}
	return ""
}
