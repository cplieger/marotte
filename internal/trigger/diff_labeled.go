package trigger

import "time"

// applyLabeled diffs one walk of the open items against the labeled cursor: the label read present over
// has_label false, or over no entry, is a gain and fires; present over unknown settles it silently; absent sets
// has_label false. An entry the walk did not read is left as it was.
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
	part := itemPart(pr.ev.Kind)
	for i := range items {
		it := &items[i]
		if !pr.admitsItem(it) {
			continue
		}
		e := rc.Labeled.Items[it.number]
		if e == nil {
			e = &labelEntry{HasLabel: labelNo}
			rc.Labeled.Items[it.number] = e
		}
		e.LastSeen = pr.start
		if !pr.hasLabel(it) {
			e.HasLabel = labelNo
			continue
		}
		if e.HasLabel == labelNo {
			pr.enqueueGain(part, it.number)
		}
		e.HasLabel = labelYes
	}
}

func (pr *pollRule) enqueueGain(part string, number int) {
	gain := formatGain(pr.mint())
	repo := foldRepo(pr.feed.Selector)
	_, host := connectionParts(pr.feed.Connection)
	key := labeledKey(pr.rule.ID, repo, part, number, gain)
	in := pr.eventInputs(key, labeledEventID(host, repo, part, number, pr.rule.ID, gain))
	pr.enqueue(&queueEntry{
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
