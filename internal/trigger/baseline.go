package trigger

import (
	"log/slog"
	"slices"
	"strings"

	"github.com/cplieger/forgeapi/v2"
)

// A rule's first poll after it is created, or after its connection, repo or kind changed, takes a baseline and
// fires nothing. A baseline lands only on a complete walk: a partial one would make every unread item look new,
// so it is retried at the next poll.

func (pr *pollRule) baselineLabeled(rc *repoCursor, items []item) {
	lc := &labeledCursor{Items: map[int]*labelEntry{}}
	for i := range items {
		if !pr.admitsItem(&items[i]) {
			continue
		}
		state := labelNo
		switch {
		case pr.hasLabel(&items[i]):
			state = labelYes
		case items[i].cut:
			state = labelUnknown
			pr.logCut(&items[i])
		}
		lc.Items[items[i].number] = &labelEntry{HasLabel: state, LastSeen: pr.start}
	}
	rc.Labeled = lc
	pr.logBaseline(len(lc.Items))
}

// baselineOpened sets the floor to the highest number read: every item at or below it existed at baseline,
// whether or not the walk read it.
func (pr *pollRule) baselineOpened(rc *repoCursor, items []item) {
	high := 0
	for i := range items {
		high = max(high, items[i].number)
	}
	rc.Opened = &openedCursor{Floor: high, PendingFloor: high}
	pr.logBaseline(len(items))
}

// baselineRuns ignores every run created before the baseline, so a run that started before the rule existed and
// concludes after it does not fire.
func (pr *pollRule) baselineRuns(rc *repoCursor, read int) {
	rc.Runs = &runCursor{BaselineAt: pr.start, LastComplete: pr.start}
	pr.logBaseline(read)
}

func (pr *pollRule) logBaseline(items int) {
	slog.Info(logMsgBaselineTaken, append(pr.attrs(), "items", items)...)
}

func (pr *pollRule) hasLabel(it *item) bool {
	return slices.ContainsFunc(it.labels, func(l forgeapi.Label) bool {
		return strings.EqualFold(l.Name, pr.ev.Filter.Label)
	})
}
