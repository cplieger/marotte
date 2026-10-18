package agent

// kasExec is marotte's view of KAS's execution state for one chat's session, and the only writer of it: step applies
// KAS's activity reports and marotte's own hold actions through kasTransitions, and may answers what marotte may
// send in each state. KAS sends one session/prompt beside a running execution of the same session to the slot
// queue or aborts the run, so each permission below follows from whether KAS reports a run.

// kasState is one row of kasTransitions.
type kasState uint8

const (
	// kasIdle: KAS reports no execution or hold for the session, and no cancel-grace close holds admission.
	kasIdle kasState = iota
	// kasRunning: KAS reports an execution or a hold (focus_update activity.turnActive), whatever turn marotte has open.
	kasRunning
	// kasHeldRunning: marotte's cancel grace closed turn holdSeq and KAS reports a run, which may be that turn's.
	kasHeldRunning
	// kasHeldIdle: as kasHeldRunning with KAS reporting idle, while turn holdSeq's closer runs its session calls.
	kasHeldIdle
	nKASStates
)

// kasInput is one column of kasTransitions. The hold inputs carry the closed turn's open sequence; one naming
// another turn than the hold's reads as its Other column, so an earlier close never lifts a later close's hold.
type kasInput uint8

const (
	kasReportActive kasInput = iota // focus_update activity.turnActive true
	kasReportIdle                   // focus_update activity.turnActive false
	// kasForwardReset: the forward attached or exited, so no execution of the old process streams to this chat.
	kasForwardReset
	kasGraceClaim     // marotte's cancel grace claimed a turn KAS may still run
	kasReleaseOwn     // the hold's closer finished its session calls and asks to drain
	kasReleaseOther   // a closer owning no hold asks to drain
	kasStandDownOwn   // the hold's closer stood down: a turn opened, a teardown re-fenced the chat, or shutdown
	kasStandDownOther // a closer owning no hold stood down
	nKASInputs
)

// kasCell is one transition: the next state, and for a release whether the closer may go on to its drain.
type kasCell struct {
	next  kasState
	drain bool
}

// kasTransitions is the whole state machine. Every row lists every input: an omitted cell reads as {next: kasIdle}
// and would lift a hold. A release while KAS reports a run answers no drain, and the closer waits again. KAS's turn
// brackets, prompt replies, cancels, re-wakes and queued reports move nothing here: KAS's activity count already
// counts each run and hold they imply.
var kasTransitions = [nKASStates][nKASInputs]kasCell{
	kasIdle: {
		kasReportActive: {next: kasRunning}, kasReportIdle: {next: kasIdle}, kasForwardReset: {next: kasIdle},
		kasGraceClaim: {next: kasHeldIdle}, kasReleaseOwn: {next: kasIdle, drain: true},
		kasReleaseOther: {next: kasIdle, drain: true}, kasStandDownOwn: {next: kasIdle}, kasStandDownOther: {next: kasIdle},
	},
	kasRunning: {
		kasReportActive: {next: kasRunning}, kasReportIdle: {next: kasIdle}, kasForwardReset: {next: kasIdle},
		kasGraceClaim: {next: kasHeldRunning}, kasReleaseOwn: {next: kasRunning}, kasReleaseOther: {next: kasRunning},
		kasStandDownOwn: {next: kasRunning}, kasStandDownOther: {next: kasRunning},
	},
	kasHeldRunning: {
		kasReportActive: {next: kasHeldRunning}, kasReportIdle: {next: kasHeldIdle}, kasForwardReset: {next: kasHeldIdle},
		kasGraceClaim: {next: kasHeldRunning}, kasReleaseOwn: {next: kasHeldRunning},
		kasReleaseOther: {next: kasHeldRunning}, kasStandDownOwn: {next: kasRunning},
		kasStandDownOther: {next: kasHeldRunning},
	},
	kasHeldIdle: {
		kasReportActive: {next: kasHeldRunning}, kasReportIdle: {next: kasHeldIdle}, kasForwardReset: {next: kasHeldIdle},
		kasGraceClaim: {next: kasHeldIdle}, kasReleaseOwn: {next: kasIdle, drain: true},
		kasReleaseOther: {next: kasHeldIdle, drain: true}, kasStandDownOwn: {next: kasIdle},
		kasStandDownOther: {next: kasHeldIdle},
	},
}

// kasPermits is what marotte may do in one state.
type kasPermits struct {
	// admit: take an admission reservation, which opens a turn, or send an admitted prompt's session/prompt.
	admit bool
	// sessionCall: a closer's model switch or compaction, or a parked model pick's apply.
	sessionCall bool
	// steerHolder: an admission holder may still take a prompt as a steer; under a hold the turn it follows is closed.
	steerHolder bool
	// holdRows: a queued row appended with no turn live is kept for the hold's drain.
	holdRows bool
}

var kasPermitTable = [nKASStates]kasPermits{
	kasIdle:        {admit: true, sessionCall: true, steerHolder: true},
	kasRunning:     {steerHolder: true},
	kasHeldRunning: {holdRows: true},
	kasHeldIdle:    {sessionCall: true, holdRows: true},
}

// kasExec is guarded by the owning chatLifecycle's mutex.
type kasExec struct {
	// holdRows are the queued rows appended during a hold with no turn live: refused as no turn, the client would
	// have sent them as prompts, so they stay owed past a newer close's hold or a turn opening, until the first
	// release that drains with no hold left, which may send one of them despite a cancelled close.
	holdRows []string
	// holdSeq is the open sequence of the closed turn whose hold this is, zero for none.
	holdSeq uint64
	state   kasState
}

// step applies one input, answering whether the state changed (the caller wakes its waiters), and for a release
// whether the closer may drain and the owed hold rows it takes. Caller holds the lifecycle's mutex.
func (k *kasExec) step(in kasInput, seq uint64) (changed, drain bool, rows []string) {
	if seq != k.holdSeq {
		in = otherHold(in)
	}
	cell := kasTransitions[k.state][in]
	changed = cell.next != k.state
	k.state = cell.next
	switch {
	case in == kasGraceClaim:
		k.holdSeq, changed = seq, true
	case !k.held():
		k.holdSeq = 0
		if cell.drain {
			rows, k.holdRows = k.holdRows, nil
		}
	}
	return changed, cell.drain, rows
}

// otherHold maps an own-hold input to the column for a closer owning no hold.
func otherHold(in kasInput) kasInput {
	switch in {
	case kasReleaseOwn:
		return kasReleaseOther
	case kasStandDownOwn:
		return kasStandDownOther
	case kasReportActive, kasReportIdle, kasForwardReset, kasGraceClaim, kasReleaseOther, kasStandDownOther, nKASInputs:
	}
	return in
}

func (k *kasExec) held() bool { return k.state == kasHeldRunning || k.state == kasHeldIdle }

func (k *kasExec) may() kasPermits { return kasPermitTable[k.state] }

// keepHoldRows records rows appended under the hold with no turn live; with no hold nothing keeps them.
func (k *kasExec) keepHoldRows(ids []string) {
	if k.may().holdRows {
		k.holdRows = append(k.holdRows, ids...)
	}
}
