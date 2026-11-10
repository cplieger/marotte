package agent

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

// failsafe stops the lone running turn turnID names, listing the compaction and the resume behind it.
func (*runList) failsafe(failsafeFacts) stopPlan { return stopPlan{} }

// usageUnderThePoint re-arms the failsafe: a reading under the point since the last failsafe compaction.
func (*runList) usageUnderThePoint() {}
