package agent

// The completion condition a `session/load` replay closes on, shared by load_projection.go and
// step_replay.go; each keeps its own map, mutex and effects.

// The condition: the consumer has folded every frame preceding the `session/load` result
// (`observed >= loadSeq`). A frame after the result has a higher position and cannot close early.

// drainPoint is a read-loop position plus its consumer attachment, a struct so gen and seq cannot be transposed.
type drainPoint struct {
	// gen is the attachment: a position is only comparable within one.
	gen uint64
	// seq is a frame's own Seq, or for a response the count already delivered.
	seq uint64
}

// replayDrain is one replay's progress against the condition. Guarded by the owner's mutex.
type replayDrain struct {
	// gen is the attachment both positions belong to; a new one invalidates the load position.
	gen uint64
	// observed is how far the consumer has folded on that attachment.
	observed uint64
	// loadSeq is where the `session/load` response arrived; read only when loaded.
	loadSeq uint64
	// loaded, not `loadSeq != 0`: the sequence pre-increments, so 0 is a legal position.
	loaded bool
}

func (d *replayDrain) noteConsumed(at drainPoint) {
	if at.gen < d.gen {
		return // a straggler from an attachment already replaced
	}
	if at.gen > d.gen {
		d.reattach(at.gen)
	}
	if at.seq > d.observed {
		d.observed = at.seq
	}
}

func (d *replayDrain) markLoadedAt(at drainPoint) {
	if at.gen < d.gen {
		return // a load whose attachment is already gone bounds nothing
	}
	if at.gen > d.gen {
		d.reattach(at.gen)
	}
	d.loadSeq, d.loaded = at.seq, true
}

// reattach adopts a new attachment, whose sequence restarts at zero, dropping both positions.
func (d *replayDrain) reattach(gen uint64) {
	d.gen, d.observed, d.loadSeq, d.loaded = gen, 0, 0, false
}

// `sealed` (bridge exit) bypasses the position, never the load: a load that never returned is
// discarded.
func (d *replayDrain) complete(gen uint64, sealed bool) bool {
	if gen != d.gen {
		return false // this caller is not the attachment the positions describe
	}
	return d.loaded && (sealed || d.observed >= d.loadSeq)
}
