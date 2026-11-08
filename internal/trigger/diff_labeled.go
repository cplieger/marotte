package trigger

import (
	"cmp"
	"slices"
	"time"
)

// applyLabeled diffs one walk of the open items against the labeled cursor: the label read present over
// has_label false, or over no entry, is a gain and fires; present over unknown settles it silently; absent sets
// has_label false. An entry the walk did not read is left as it was, and so is one whose row may be cut and
// lacks the label, so a cut set never reads as a removal followed by a gain.
func (pr *pollRule) applyLabeled(items []item, complete bool) {
	rc := pr.repoCursor()
	if rc.Labeled == nil {
		if complete {
			pr.baselineLabeled(rc, items)
		}
		return
	}
	if rc.Labeled.Items == nil {
		rc.Labeled.Items = map[int]*labelEntry{}
	}
	slices.SortFunc(items, func(a, b item) int { return cmp.Compare(a.number, b.number) })
	open := 0
	for i := range items {
		if pr.admitsItem(&items[i]) {
			open++
			pr.observeLabel(rc.Labeled, &items[i])
		}
	}
	if complete {
		pr.pruneLabeled(rc.Labeled, open)
	}
}

// observeLabel applies one read row to its item's entry.
func (pr *pollRule) observeLabel(lc *labeledCursor, it *item) {
	e := lc.Items[it.number]
	present := pr.hasLabel(it)
	if !present && it.cut {
		pr.logCut(it)
		if e != nil {
			e.LastSeen = pr.start
		}
		return
	}
	if e == nil {
		e = &labelEntry{HasLabel: labelNo}
	}
	e.LastSeen = pr.start
	switch {
	case !present:
		e.HasLabel = labelNo
	case e.HasLabel != labelNo || pr.enqueueGain(itemPart(pr.ev.Kind), it.number):
		e.HasLabel = labelYes
	case lc.Items[it.number] == nil:
		// Left at the forge with no entry, as an item the cursor has not seen.
		return
	}
	lc.Items[it.number] = e
}

// enqueueGain queues the item's gain, false when the queue is full: the item then keeps has_label false and is
// matched again once the queue has room.
func (pr *pollRule) enqueueGain(part string, number int) bool {
	gain := formatGain(pr.mint())
	repo := foldRepo(pr.feed.Selector)
	_, host := connectionParts(pr.feed.Connection)
	key := labeledKey(pr.rule.ID, repo, part, number, gain)
	in := pr.eventInputs(key, labeledEventID(host, repo, part, number, pr.rule.ID, gain))
	return pr.hold(&queueEntry{
		Key: key, CoalesceKey: itemCoalesceKey(pr.rule.ID, repo, part, number), ObservedAt: pr.start,
		Inputs: pr.itemInputs(in, part, number, pr.ev.Filter.Label),
	})
}

// mint answers this poll's gain instant, minting it at the first gain and recording it as status.last_minted in
// the same write that queues the gain. One poll reads an item once, so two gains of one poll differ by number.
func (pr *pollRule) mint() time.Time {
	if pr.gain.IsZero() {
		pr.gain = mintGain(pr.start, pr.st.raw.Status.LastMinted)
		pr.st.raw.Status.LastMinted = pr.gain
	}
	return pr.gain
}

func (pr *pollRule) logCut(it *item) {
	logLabelsCut(refOf(pr.rule), it.number, it.omitted)
}
