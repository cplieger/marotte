package trigger

import (
	"cmp"
	"slices"
	"strings"
)

// applyOpened diffs one walk of the open items against the opened cursor. An item above floor and not in seen is
// new; it fires when its author is on the rule's allowlist. The rows are matched against floor and seen as they
// stood when the walk started.
//
// floor lags one complete walk: a list can reorder under a walk that still ends complete (an item updated
// mid-walk moves ahead of the cursor), so an item one walk skipped is still above floor at the next and is read
// as new then. A partial walk only adds to seen.
func (pr *pollRule) applyOpened(items []item, complete bool) {
	rc := pr.repoCursor()
	if rc.Opened == nil {
		if complete {
			pr.baselineOpened(rc, items)
		}
		return
	}
	oc := rc.Opened
	floor, seen := oc.Floor, slices.Clone(oc.Seen)
	slices.SortFunc(items, func(a, b item) int { return cmp.Compare(a.number, b.number) })
	high, unqueued := 0, 0
	for i := range items {
		it := &items[i]
		high = max(high, it.number)
		if _, found := slices.BinarySearch(seen, it.number); found || it.number <= floor {
			continue
		}
		if !pr.observeOpened(oc, it) && unqueued == 0 {
			unqueued = it.number
		}
	}
	if !complete {
		return
	}
	// A match left at the forge stays out of seen, and both floors stop below it, so it is new again at the
	// first poll after the queue has room.
	next := oc.PendingFloor
	if unqueued != 0 {
		next, high = min(next, unqueued-1), min(high, unqueued-1)
	}
	oc.Floor = max(oc.Floor, next)
	oc.PendingFloor = high
	oc.Seen = slices.DeleteFunc(oc.Seen, func(n int) bool { return n <= oc.Floor })
}

// observeOpened accounts for a new item, false when it matched and the queue is full, which leaves it unseen.
func (pr *pollRule) observeOpened(oc *openedCursor, it *item) bool {
	if author, ok := pr.allowed(it); ok && !pr.enqueueOpened(it.number, author) {
		return false
	}
	if at, found := slices.BinarySearch(oc.Seen, it.number); !found {
		oc.Seen = slices.Insert(oc.Seen, at, it.number)
	}
	return true
}

// allowed answers the allowlist entry, as the operator typed it, that its author matches.
func (pr *pollRule) allowed(it *item) (string, bool) {
	if !pr.admitsItem(it) {
		return "", false
	}
	for _, a := range pr.ev.Filter.Authors {
		if strings.EqualFold(a, it.author) {
			return a, true
		}
	}
	return "", false
}

// enqueueOpened queues the item's opening, false when the queue is full.
func (pr *pollRule) enqueueOpened(number int, author string) bool {
	part := itemPart(pr.ev.Kind)
	repo := foldRepo(pr.feed.Selector)
	_, host := connectionParts(pr.feed.Connection)
	key := openedKey(pr.rule.ID, repo, part, number)
	in := pr.eventInputs(key, openedEventID(host, repo, part, number))
	return pr.hold(&queueEntry{
		Key: key, CoalesceKey: itemCoalesceKey(pr.rule.ID, repo, part, number), ObservedAt: pr.start,
		Inputs: pr.itemInputs(in, part, number, author),
	})
}
