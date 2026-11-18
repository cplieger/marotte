package ruleengine

import (
	"maps"
	"slices"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// chatDepths is the chain depths the engine keeps for one chat's live session: its live turns', its last closed
// turn's, its last prompt's, and the lease depths of the ended runs whose wake KAS still owes it.
type chatDepths struct {
	live   map[string]int
	wakes  map[string]pendingWake
	closed int
	// prompt is the depth of the last prompt that opened a turn: a rule's caller's, 0 for anyone else's.
	prompt int
}

// pendingWake is an ended run's lease depth, kept until a wake turn takes it or until passes.
type pendingWake struct {
	until time.Time
	depth int
}

// depthsLocked answers chatID's depths, creating them. Caller holds mu.
func (e *Engine) depthsLocked(chatID marotte.ChatID) *chatDepths {
	d := e.depths[chatID]
	if d == nil {
		d = &chatDepths{live: map[string]int{}, wakes: map[string]pendingWake{}}
		e.depths[chatID] = d
	}
	return d
}

// openDepthLocked records turnID's depth and answers the waking lease's depth (§9 guard 2): a rule's prompt
// carries its caller's depth and a person's 0; a wake turn takes the highest pending lease depth, one deeper when
// that is 1 or more; any other prompt-less turn continues the last prompt's. Caller holds mu.
func (d *chatDepths) openDepthLocked(turnID string, source marotte.TurnOpenSource, caller *marotte.Caller, wake bool,
	now time.Time,
) (leaseDepth int) {
	turnDepth := d.prompt
	switch {
	case caller != nil && caller.Kind == marotte.CallerKindRule:
		turnDepth = caller.Depth
		d.prompt = turnDepth
	case source.PromptClass():
		turnDepth = 0
		d.prompt = 0
	case wake:
		if lease, took := d.takeWake(now); took {
			leaseDepth, turnDepth = lease, 0
			if lease >= 1 {
				turnDepth = lease + 1
			}
		}
	}
	d.live[turnID] = turnDepth
	return leaseDepth
}

// takeWake removes and answers the highest lease depth still pending at now, dropping those whose hold passed.
func (d *chatDepths) takeWake(now time.Time) (int, bool) {
	d.pruneWakes(now)
	if len(d.wakes) == 0 {
		return 0, false
	}
	ids := slices.Sorted(maps.Keys(d.wakes))
	top := ids[0]
	for _, id := range ids[1:] {
		if d.wakes[id].depth > d.wakes[top].depth {
			top = id
		}
	}
	depth := d.wakes[top].depth
	delete(d.wakes, top)
	return depth, true
}

func (d *chatDepths) pruneWakes(now time.Time) {
	maps.DeleteFunc(d.wakes, func(_ string, w pendingWake) bool { return !now.Before(w.until) })
}

// closeDepthLocked moves turnID from live to last closed. Caller holds mu.
func (d *chatDepths) closeDepthLocked(turnID string) {
	if depth, ok := d.live[turnID]; ok {
		delete(d.live, turnID)
		d.closed = depth
	}
}

// launchDepth is the depth a run launched now from the chat takes: its deepest live turn's, else its last closed
// turn's.
func (d *chatDepths) launchDepth() int {
	if len(d.live) == 0 {
		return d.closed
	}
	return slices.Max(slices.Collect(maps.Values(d.live)))
}

// LaunchDepth answers the chain depth a run chatID's agent launches now takes: its live turn's, else that of the
// last turn that closed in the chat's live session, else 0.
func (e *Engine) LaunchDepth(chatID marotte.ChatID) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if d := e.depths[chatID]; d != nil {
		return d.launchDepth()
	}
	return 0
}

// RunEnded is the run-end tap for a terminal run: parent's next wake turn may take depth, the run's lease depth,
// for as long as Deps.WakeHold. A run with no parent chat is woken into none.
func (e *Engine) RunEnded(parent marotte.ChatID, workflowID string, depth int) {
	if parent == "" {
		return
	}
	now := time.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	d := e.depthsLocked(parent)
	d.pruneWakes(now)
	d.wakes[workflowID] = pendingWake{depth: depth, until: now.Add(e.deps.WakeHold)}
}

// SessionEnded drops what the engine keeps of chatID's live session: its turns' depths and its pending wakes.
func (e *Engine) SessionEnded(chatID marotte.ChatID) {
	e.mu.Lock()
	delete(e.depths, chatID)
	e.mu.Unlock()
}
