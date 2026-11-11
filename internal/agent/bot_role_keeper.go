package agent

import "context"

// roleState is where a Bot chat's session stands against its role (botRoleKeeper).
type roleState uint8

const (
	// roleUnkept: the session is not a started Bot's, so nothing is held.
	roleUnkept roleState = iota
	roleOn
	// roleOff: KAS reported another mode; the role goes back at the next idle edge.
	roleOff
	// roleReasserting: one session/set_mode to the role is in flight.
	roleReasserting
	// roleFailed: KAS refused it; the chat is marked for reload, whose door resets the keeper.
	roleFailed
	nRoleStates
)

// roleInput is one column of roleTransitions.
type roleInput uint8

const (
	roleResetBot    roleInput = iota // a door opened a started Bot's session
	roleResetOther                   // a door opened any other chat's session
	roleReportRole                   // KAS reported the role, or a door left the session on it
	roleReportOther                  // KAS reported another mode
	roleIdle                         // the session is idle (roleIdleLocked)
	roleAccepted                     // KAS accepted the set_mode
	roleRefused                      // KAS refused it
	roleCut                          // the set_mode was never answered: the lease was refused or the life ended
	nRoleInputs
)

// roleTransitions is the whole table (design §7.1 "The role keeper"). Every row lists every input: an omitted cell
// reads as roleUnkept, which would let a prompt go out off the role.
var roleTransitions = [nRoleStates][nRoleInputs]roleState{
	roleUnkept: {
		roleResetBot: roleOff, roleResetOther: roleUnkept, roleReportRole: roleUnkept, roleReportOther: roleUnkept,
		roleIdle: roleUnkept, roleAccepted: roleUnkept, roleRefused: roleUnkept, roleCut: roleUnkept,
	},
	roleOn: {
		roleResetBot: roleOff, roleResetOther: roleUnkept, roleReportRole: roleOn, roleReportOther: roleOff,
		roleIdle: roleOn, roleAccepted: roleOn, roleRefused: roleOn, roleCut: roleOn,
	},
	roleOff: {
		roleResetBot: roleOff, roleResetOther: roleUnkept, roleReportRole: roleOn, roleReportOther: roleOff,
		roleIdle: roleReasserting, roleAccepted: roleOff, roleRefused: roleOff, roleCut: roleOff,
	},
	roleReasserting: {
		roleResetBot: roleOff, roleResetOther: roleUnkept, roleReportRole: roleReasserting,
		roleReportOther: roleReasserting, roleIdle: roleReasserting, roleAccepted: roleOn, roleRefused: roleFailed,
		roleCut: roleOff,
	},
	roleFailed: {
		roleResetBot: roleOff, roleResetOther: roleUnkept, roleReportRole: roleOn, roleReportOther: roleFailed,
		roleIdle: roleFailed, roleAccepted: roleFailed, roleRefused: roleFailed, roleCut: roleFailed,
	},
}

// botRoleKeeper holds a Bot chat's resident session on its role between doors. Guarded by the owning lifecycle's
// mutex; only keepRoleLocked changes it.
type botRoleKeeper struct {
	// sb is the session the keeper holds, nil for none: an input naming another session is dropped.
	sb *sharedBridge
	// mode is the mode KAS last reported on sb.
	mode  string
	state roleState
}

// keepRoleLocked applies one input for sb and carries out its row's effects: the claim from off starts the one
// set_mode (sendRoleLocked), a refusal marks sb for reload, and a cut detaches sb so nothing is sent on it again. It
// answers whether a refusal failed the keeper. Caller holds mu and wakes.
func (lc *chatLifecycle) keepRoleLocked(sb *sharedBridge, in roleInput) (failed bool) {
	k := &lc.role
	if in != roleResetBot && in != roleResetOther && (sb == nil || sb != k.sb) {
		return false
	}
	prev := k.state
	k.state = roleTransitions[prev][in]
	switch {
	case in == roleResetBot || in == roleResetOther:
		k.sb, k.mode = sb, ""
	case prev == roleOff && k.state == roleReasserting:
		lc.sendRoleLocked(sb)
	case prev == roleReasserting && k.state == roleFailed:
		if !lc.ended {
			lc.reloadFor = sb
		}
		return true
	case prev == roleReasserting && in == roleCut:
		k.sb = nil
	}
	return false
}

// roleIdleLocked is the one predicate for "a set_mode cuts nothing on the chat's session": KAS 3.1.0's
// setSessionModeAdmitted (acp-server.js@15823080) switches the profile with nothing ordering it against a running
// turn, a hosted step's sub-agents or a KAS run. A prompt not yet sent is not work. Caller holds mu.
func (lc *chatLifecycle) roleIdleLocked() bool {
	return !lc.ended && unsentOrNone(lc.own) && unsentOrNone(lc.pending) && len(lc.steps) == 0 &&
		lc.kas.state == kasIdle && len(lc.tails) == 0 && lc.work[workDoor] == 0 && lc.closing == nil
}

func unsentOrNone(t *activeTurn) bool { return t == nil || t.awaitsSend() }

// feedRoleIdleLocked is the idle edge, run by every wakeLocked: a keeper off its role claims the set_mode the moment
// the session is idle. Caller holds mu.
func (lc *chatLifecycle) feedRoleIdleLocked() {
	if lc.role.state == roleOff && lc.role.sb != nil && lc.reg != nil && lc.reg.spawnRole != nil && lc.roleIdleLocked() {
		lc.keepRoleLocked(lc.role.sb, roleIdle)
	}
}

// sendRoleLocked starts the claimed set_mode on sb through the runtime's drain gate, as the life's counted work, so a
// teardown and Shutdown both wait for its answer. Caller holds mu.
func (lc *chatLifecycle) sendRoleLocked(sb *sharedBridge) {
	// roleIdleLocked held in this section, so the life stands and the count is taken.
	ctx, done, _ := lc.beginLocked(workRole, nil, nil)
	if lc.reg.spawnRole(func() {
		defer done()
		lc.reg.sendRole(ctx, lc, sb)
	}) {
		return
	}
	// Shutdown began: nothing is sent, so the count is undone here (done takes mu) and the keeper lets sb go, as a cut.
	lc.work[workRole]--
	lc.role.state, lc.role.sb = roleOff, nil
}

// resetRole is a door's reset of the keeper onto sb, its new session: held for a started Bot, else unkept.
func (lc *chatLifecycle) resetRole(sb *sharedBridge, bot bool) {
	in := roleResetOther
	if bot {
		in = roleResetBot
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	lc.keepRoleLocked(sb, in)
	lc.wakeLocked()
}

// reportRole applies KAS reporting mode on sb.
func (lc *chatLifecycle) reportRole(sb *sharedBridge, mode string, onRole bool) {
	in := roleReportOther
	if onRole {
		in = roleReportRole
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if sb != nil && sb == lc.role.sb {
		lc.role.mode = mode
	}
	lc.keepRoleLocked(sb, in)
	lc.wakeLocked()
}

// answerRole applies the set_mode's answer on sb, answering whether KAS's refusal failed the keeper and the mode it
// last reported.
func (lc *chatLifecycle) answerRole(sb *sharedBridge, in roleInput) (failed bool, mode string) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	mode = lc.role.mode
	failed = lc.keepRoleLocked(sb, in)
	lc.wakeLocked()
	return failed, mode
}

// roleSendStepLocked is the send gate's reading of the keeper (markSentLocked), its only reader. Caller holds mu.
func (lc *chatLifecycle) roleSendStepLocked() sendStep {
	switch lc.role.state {
	case roleOff, roleReasserting:
		return sendWait
	case roleFailed:
		return sendRoleFailed
	}
	return sendNow
}

// roleSender is the registry's hook that runs a claimed set_mode and answers it (answerRole).
type roleSender func(ctx context.Context, lc *chatLifecycle, sb *sharedBridge)
