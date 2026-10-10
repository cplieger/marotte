package agent

// channelState is what the record knows of KAS's read cursor for the chat's own turn. NONE means no turn bound,
// not no own turn: a prompt turn is own from StartTurn, before its bracket.
type channelState uint8

const (
	chanNone channelState = iota
	// chanOpen: no clear in this execution, or the last probe was read.
	chanOpen
	// chanUnconfirmed: a clear happened and nothing is outstanding. No row waits.
	chanUnconfirmed
	// chanProbing: one combined probe is outstanding, or owed (probe == "").
	chanProbing
	nChannelStates
)

// rowState is one user row's state. rowRead and rowDone are terminal: a read row is kept while an op owns it,
// to tell read-before-my-clear from never-there; a done row leaves at the next prune.
type rowState uint8

const (
	rowParked rowState = iota
	rowQueued
	rowOutstanding
	rowWaiting
	rowCleared
	rowUnsent
	rowRead
	rowDone
	nRowStates
)

func (s rowState) inKAS() bool { return s == rowQueued || s == rowOutstanding }

func (s rowState) terminal() bool { return s == rowRead || s == rowDone }

type steerEvent uint8

const (
	evBind          steerEvent = iota // a turn becomes own
	evRevise                          // own moves to another turn on the same execution
	evSteer                           // a user steer is routed
	evDeleteQueued                    // delete a row KAS holds, alone or as a probe member
	evDeleteWaiting                   // delete a row waiting behind the probe
	evDeleteIdle                      // delete a parked, cleared or unsent row
	evDiscard                         // discard every row
	evInjectedProbe                   // the probe was read
	evInjectedOther                   // any other id was read
	evAckProbe                        // a delegate's acknowledgement read the probe
	evTurnEnd                         // natural end, error or cancel
	evTurnDeath                       // the bridge died with the turn
	evForeignClear                    // a steering_cleared no op of ours issued
	evTeardown                        // the chat is closed or deleted
	evRefused                         // a send answered queued:false
	evPostLoadClear                   // the clear before a reloaded session's first send
	evJobWake                         // a flush or owed-probe job runs
	nSteerEvents
)

// chanRule is a channel cell. unhandled is the sentinel no cell may answer;
// impossible is a cell no reachable sequence produces.
type chanRule uint8

const (
	cUnhandled chanRule = iota
	cImpossible
	cKeep
	cOpen
	cNone
	cUnconfirmed
	cProbing
	// cResubmit: the op's remaining unread rows become one probe (PROBING), or
	// UNCONFIRMED when none remain.
	cResubmit
	// cOwed: un-owned waiting rows become an owed probe (PROBING), else UNCONFIRMED.
	cOwed
	// cFlush: send the waiting rows, as the probe when it is owed.
	cFlush
)

type rowRule uint8

const (
	rUnhandled rowRule = iota
	rImpossible
	rKeep
	rRead
	rCleared
	rDeleted
	rDiscarded
	rCollect
	// A kept row is read if the barrier shows it, else joins the new probe.
	rResubmit
	rJoinProbe
	rRoute
	rDrop
	// rPark: back to parked, for the drain that follows to send in order.
	rPark
)

func channelRule(c channelState, ev steerEvent) chanRule { //nolint:gocyclo // one flat case per event
	switch ev {
	case evBind:
		return pick(c, cOpen, cImpossible, cImpossible, cImpossible)
	case evRevise:
		return pick(c, cImpossible, cKeep, cKeep, cKeep)
	case evSteer:
		return pick(c, cKeep, cKeep, cProbing, cKeep)
	case evDeleteQueued:
		return pick(c, cKeep, cResubmit, cResubmit, cResubmit)
	case evDeleteWaiting:
		return pick(c, cImpossible, cKeep, cImpossible, cOwed)
	case evDeleteIdle:
		return cKeep
	case evDiscard:
		return pick(c, cKeep, cUnconfirmed, cUnconfirmed, cUnconfirmed)
	case evInjectedProbe:
		return pick(c, cKeep, cImpossible, cImpossible, cOpen)
	case evInjectedOther:
		return cKeep
	case evAckProbe:
		return pick(c, cKeep, cImpossible, cImpossible, cOwed)
	case evTurnEnd, evTurnDeath, evTeardown:
		return cNone
	case evForeignClear:
		return pick(c, cKeep, cOwed, cKeep, cOwed)
	case evRefused:
		return pick(c, cKeep, cUnconfirmed, cKeep, cUnconfirmed)
	case evPostLoadClear:
		return pick(c, cKeep, cImpossible, cImpossible, cImpossible)
	case evJobWake:
		return pick(c, cKeep, cFlush, cKeep, cFlush)
	case nSteerEvents:
	}
	return cUnhandled
}

func pick(c channelState, none, open, unconfirmed, probing chanRule) chanRule {
	switch c {
	case chanNone:
		return none
	case chanOpen:
		return open
	case chanUnconfirmed:
		return unconfirmed
	case chanProbing:
		return probing
	case nChannelStates:
	}
	return cUnhandled
}

func rowRuleFor(s rowState, ev steerEvent) rowRule { //nolint:gocyclo // one flat case per event
	if s.terminal() {
		return rKeep
	}
	switch ev {
	case evBind, evRevise:
		return rKeep
	case evSteer:
		// A turn end re-routes a cleared row into a turn the prompt holds; an unsent one goes via parked.
		return cell(s, rRoute, rKeep, rKeep, rRoute, rRoute, rKeep)
	case evDeleteQueued:
		return cell(s, rKeep, rResubmit, rResubmit, rJoinProbe, rJoinProbe, rKeep)
	case evDeleteWaiting:
		return cell(s, rKeep, rKeep, rKeep, rDeleted, rKeep, rKeep)
	case evDeleteIdle:
		return cell(s, rDeleted, rKeep, rKeep, rKeep, rDeleted, rDeleted)
	case evDiscard:
		return rDiscarded
	case evInjectedProbe:
		return cell(s, rKeep, rKeep, rRead, rKeep, rKeep, rKeep)
	case evAckProbe:
		return cell(s, rKeep, rKeep, rRead, rJoinProbe, rKeep, rKeep)
	case evInjectedOther:
		return cell(s, rKeep, rRead, rImpossible, rKeep, rKeep, rRead)
	case evTurnEnd, evTurnDeath:
		return cell(s, rCollect, rCollect, rCollect, rCollect, rCollect, rKeep)
	case evForeignClear:
		return cell(s, rKeep, rCleared, rCleared, rKeep, rKeep, rKeep)
	case evTeardown:
		return rDrop
	case evRefused:
		return cell(s, rImpossible, rCleared, rCleared, rCleared, rKeep, rKeep)
	case evPostLoadClear:
		// Sent during the MCP wait; nothing bound could have made it a probe.
		return cell(s, rKeep, rPark, rImpossible, rImpossible, rKeep, rKeep)
	case evJobWake:
		return cell(s, rKeep, rKeep, rKeep, rRoute, rKeep, rKeep)
	case nSteerEvents:
	}
	return rUnhandled
}

func cell(s rowState, parked, queued, outstanding, waiting, cleared, unsent rowRule) rowRule {
	switch s {
	case rowParked:
		return parked
	case rowQueued:
		return queued
	case rowOutstanding:
		return outstanding
	case rowWaiting:
		return waiting
	case rowCleared:
		return cleared
	case rowUnsent:
		return unsent
	case rowRead, rowDone, nRowStates:
	}
	return rUnhandled
}
