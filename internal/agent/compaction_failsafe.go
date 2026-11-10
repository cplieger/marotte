package agent

import "github.com/cplieger/marotte/internal/marotte"

// compactionFailsafePct is the context use, in percent, at which a running turn is stopped, compacted and resumed:
// KAS's own compaction is off on every chat session, so nothing else saves a turn that crosses the point mid-turn.
const compactionFailsafePct = 95

// resumeText is the prompt marotte sends after a failsafe compaction, so the stopped work continues.
const resumeText = "The context window was nearly full, so your last turn was stopped and the conversation was " +
	"compacted. Continue the task from where you stopped. A command or file edit that was running when the turn " +
	"stopped may not have finished, so check its result before you rely on it."

// failsafeStopNotice is the stopped turn's close reason, the transcript's account of the stop.
const failsafeStopNotice = "The context was 95% full, so marotte stopped this turn to compact it before it ran out. " +
	"After the compaction, marotte asks the agent to continue."

// compactFailsafeFailed is the toast for a failsafe compaction that did not run, whose stopped turn is not resumed.
const compactFailsafeFailed = "The compaction did not run (the agent was busy, or the summary failed or the chat " +
	"changed before it finished), so marotte did not continue the stopped turn. Send a message to go on."

// failsafeFacts name one failsafe fire: the turn the reading belongs to, the resume's message id and the stop's token.
type failsafeFacts struct {
	turnID   string
	resumeID string
	token    uint64
}

// failsafe stops the lone running turn turnID names, as the reader's Cancel does, and lists its compaction (taking
// over the one already listed) with the resume directly behind it. It fires once per turn, never in a turn whose
// compaction the reader declined, and not again after an accepted failsafe compaction until usageUnderThePoint. A
// fold-opened wire turn is left alone: its compaction is taken beside it at once and closes it (compactionStart).
func (l *runList) failsafe(f failsafeFacts) stopPlan {
	if f.turnID == "" || f.turnID == l.failsafeFor || f.turnID == l.declined || l.failsafeHeld || l.liveLen() != 1 {
		return stopPlan{}
	}
	h := l.entries[0]
	if h.turnID != f.turnID || !h.kind.TurnKind() || h.state != marotte.RunRunning ||
		(h.wire() && h.yields) {
		return stopPlan{}
	}
	l.failsafeFor, l.crossed = f.turnID, f.turnID
	l.takeOverCompaction(f)
	plan := l.stopHead(h.id, f.token)
	h.fsStopped = true
	return plan
}

// takeOverCompaction makes the chat's compaction the failsafe's and lists the resume behind it. A compaction an
// earlier fire already took over keeps its resume, or the reader's removal of it.
func (l *runList) takeOverCompaction(f failsafeFacts) {
	c, joined := l.requestCompaction(marotte.RunFromFailsafe, f.turnID)
	if !joined || c.origin != marotte.RunFromFailsafe {
		c.fsPrior, c.fsPriorFor = marotte.RunFromThreshold, f.turnID
		if joined {
			c.fsPrior, c.fsPriorFor = c.origin, c.forTurn
		}
		c.origin, c.fsToken = marotte.RunFromFailsafe, f.token
		at, _ := l.find(c.id)
		l.insert(at+1, &runEntry{
			id: f.resumeID, kind: marotte.RunKindResume, origin: marotte.RunFromFailsafe, state: marotte.RunPending,
			row: &marotte.QueuedPrompt{ID: f.resumeID, Text: resumeText}, fsToken: f.token,
		})
	}
	c.forTurn = f.turnID
	l.touch()
}

// resume is the failsafe's resume waiting on the list, or nil.
func (l *runList) resume() *runEntry {
	for _, e := range l.entries {
		if e.kind == marotte.RunKindResume && !e.live() {
			return e
		}
	}
	return nil
}

// dropResume removes the waiting resume: the stopped work is not continued.
func (l *runList) dropResume() {
	if r := l.resume(); r != nil {
		l.remove(r)
	}
}

// usageUnderThePoint re-arms the failsafe: a reading under the point since the last failsafe compaction.
func (l *runList) usageUnderThePoint() { l.failsafeHeld = false }
