package agent

import "github.com/cplieger/marotte/internal/marotte"

// compactionFailsafePct is the context use, in percent, at which a running turn is stopped, compacted and resumed:
// KAS's own compaction is off on every chat session, so nothing else saves a turn that crosses the point mid-turn.
const compactionFailsafePct = 95

// compactionFailsafeResumes is how many resumes marotte sends between two turns the reader sent: a task whose every
// step refills the context (a tool call too big) would otherwise stop, compact and resume with no end.
const compactionFailsafeResumes = 1

// resumeText is the prompt marotte sends after a failsafe compaction, so the stopped work continues.
const resumeText = "The context window was nearly full, so marotte stopped your task mid-turn and compacted the " +
	"conversation. Continue the task from where it stopped. A command or file edit that was running when the turn " +
	"stopped may not have finished, so check its result before you rely on it."

// failsafeStopNotice is the stopped turn's close reason, the transcript's account of the stop.
const failsafeStopNotice = "The context was nearly full, so marotte stopped this turn to compact the context " +
	"before it ran out."

// compactFailsafeFailed is the toast for a failsafe compaction that did not run, whose stopped turn is not resumed.
const compactFailsafeFailed = "The compaction did not run (the agent was busy, or the summary failed or the chat " +
	"changed before it finished), so marotte did not continue the stopped turn. Send a message to go on."

// resumeLimitNotice tells the reader a failsafe stop at compactionFailsafeResumes lists no resume.
const resumeLimitNotice = "The context filled up again, so marotte stopped the turn to compact the context. It " +
	"continues a stopped task on its own once per message you send, so send a message to go on."

// resumeRefusedNotice tells the reader the resume's launch was refused, so the stopped turn is not continued.
const resumeRefusedNotice = "marotte could not continue the stopped turn. Send a message to go on."

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
// earlier fire already took over keeps its resume, or the reader's removal of it. A resume already waiting (a turn
// ran ahead of it) moves behind the compaction and keeps its stop's token, so undoing this stop leaves it; at
// compactionFailsafeResumes none is listed and the reader is told.
func (l *runList) takeOverCompaction(f failsafeFacts) {
	c, joined := l.requestCompaction(marotte.RunFromFailsafe, f.turnID)
	if !joined || c.origin != marotte.RunFromFailsafe {
		c.fsPrior, c.fsPriorFor = marotte.RunFromThreshold, f.turnID
		if joined {
			c.fsPrior, c.fsPriorFor = c.origin, c.forTurn
		}
		c.origin, c.fsToken = marotte.RunFromFailsafe, f.token
		r := l.resume()
		switch {
		case r != nil:
			l.remove(r)
		case l.resumed < compactionFailsafeResumes:
			r = &runEntry{
				id: f.resumeID, kind: marotte.RunKindResume, origin: marotte.RunFromFailsafe, state: marotte.RunPending,
				row: &marotte.QueuedPrompt{ID: f.resumeID, Text: resumeText}, fsToken: f.token,
			}
		default:
			l.fx.notices = append(l.fx.notices, resumeLimitNotice)
		}
		if r != nil {
			at, _ := l.find(c.id)
			l.insert(at+1, r)
		}
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

// withdrawFailsafe takes back what the failsafe stop token listed: its waiting resume goes, and the compaction it
// took over gets back its prior origin and turn. A resume an earlier stop listed stays.
func (l *runList) withdrawFailsafe(token uint64) {
	if token == 0 {
		return
	}
	if r := l.resume(); r != nil && r.fsToken == token {
		l.remove(r)
	}
	if c := l.compaction(); c != nil && c.fsToken == token && c.origin == marotte.RunFromFailsafe {
		c.origin, c.forTurn, c.fsToken = c.fsPrior, c.fsPriorFor, 0
		l.touch()
	}
}

// usageUnderThePoint re-arms the failsafe: a reading under the point since the last failsafe compaction.
func (l *runList) usageUnderThePoint() { l.failsafeHeld = false }
