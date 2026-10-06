package command

import (
	"context"
	"encoding/json"

	"github.com/cplieger/marotte/internal/marotte"
)

// dischargeVerdict says whether one command is the user ANSWERING a question the
// chat's own agent asked, which is what discharges a retained waiting_on_user status.
type dischargeVerdict int

const (
	// dischargeNo: this command answers no question the agent asked. The zero value,
	// so an unclassified command is silent rather than wrong; the completeness test is
	// what catches it.
	dischargeNo dischargeVerdict = iota
	// dischargeYes: the dispatcher discharges once the handler has succeeded.
	dischargeYes
	// dischargeBySource: the TURN's source decides (marotte.TurnOpenSource.UserAnswered),
	// because one command covers both a prompt and a `!cmd`.
	dischargeBySource
	// dischargeByAnswer: the command's own PAYLOAD decides, because one command type
	// carries both the user supplying an answer and the user walking away from the
	// card. answersAgent owns that rule and answers false for anything it cannot read
	// as an answer, so an absent, malformed or unknown payload keeps the claim.
	dischargeByAnswer
)

// commandDischarges classifies every command in marotte's vocabulary. The line the
// table applies: does the event carry the user ANSWERING this agent — their own prose,
// or a decision supplied through one of the structured channels the agent asks on? The
// prompt's own source then decides which turns it opened count, and the three
// structured channels defer to their payload, because each carries a walk-away as well
// as an answer. Total by test: TestCommandDischarges_ClassifiesEveryCommand reads the
// constant block and fails for a command that is not listed.
var commandDischarges = map[marotte.CommandType]dischargeVerdict{
	marotte.CmdPrompt: dischargeBySource,
	marotte.CmdSteer:  dischargeYes,
	// A message held for the turn's end is the user's prose as squarely as a steer.
	marotte.CmdQueuePrompt: dischargeYes,
	// The agent's OWN structured question (_kiro/userInput): an agent that asks through a card and
	// gets an answer is not still waiting.
	marotte.CmdUserInputResponse: dischargeByAnswer,
	// An authorization or a file review rather than a question — but it is one of the
	// channels an agent that declared waiting_on_user asks on, and selecting an option
	// the request itself advertised is the user deciding.
	marotte.CmdPermissionResponse: dischargeByAnswer,
	// An MCP server asked, mid-tool-call, not the agent — reached anyway, because the
	// agent's own tool call is what raised the form and the user filling it in is the
	// answer that unblocks the turn.
	marotte.CmdElicitationResponse: dischargeByAnswer,
	// A rewind or a compact removes the claim's QUESTION from history rather than
	// answering it, so both leave a stale-TRUE the next prompt discharges.
	marotte.CmdRewindChat: dischargeNo,
	marotte.CmdCompact:    dischargeNo,

	marotte.CmdCreateChat:        dischargeNo,
	marotte.CmdResumeSession:     dischargeNo,
	marotte.CmdForkChat:          dischargeNo,
	marotte.CmdCancel:            dischargeNo,
	marotte.CmdDeleteChat:        dischargeNo,
	marotte.CmdSwitchModel:       dischargeNo,
	marotte.CmdSetEffort:         dischargeNo,
	marotte.CmdSetThinking:       dischargeNo,
	marotte.CmdSetDraft:          dischargeNo,
	marotte.CmdSetAttachments:    dischargeNo,
	marotte.CmdSetMode:           dischargeNo,
	marotte.CmdCreateHook:        dischargeNo,
	marotte.CmdSetSupervisedMode: dischargeNo,
	marotte.CmdSteerClear:        dischargeNo,
	marotte.CmdUnqueuePrompt:     dischargeNo,
	marotte.CmdSetInterruptMode:  dischargeNo,
	marotte.CmdRenameChat:        dischargeNo,
	marotte.CmdSteerRemove:       dischargeNo,
	marotte.CmdOpenTab:           dischargeNo,
	marotte.CmdCloseTab:          dischargeNo,
	marotte.CmdReorderTabs:       dischargeNo,
	marotte.CmdPinTab:            dischargeNo,
	marotte.CmdReparentTab:       dischargeNo,
	// An approval is a statement ABOUT a document, not an answer to a question the
	// agent asked: the spec gate's own ask travels on CmdUserInputResponse, so a phase
	// approved from the spec tab leaves any waiting claim standing.
	marotte.CmdApproveSpecPhase: dischargeNo,
}

// ChatStatus is the one thing a command handler needs of the agent's self-declared
// chat status: end the retained waiting_on_user claim, because this command IS the
// user answering. Write-only, like SteerRecorder.
type ChatStatus interface {
	DischargeWaiting(ctx context.Context, chatID marotte.ChatID)
}

// answersAgent reports whether cmd's payload states the user SUPPLYING an answer on a structured
// channel the agent asks on. It FAILS TOWARD KEEPING the claim: an absent or malformed payload, an
// unknown action and a walk-away all answer false, because a wrongly-cleared claim hides that the
// agent needs somebody while a wrongly-kept one is cleared by the next prompt. Read after the
// handler succeeded; the checks tell an answer from the walk-away the same handler accepts.
func answersAgent(cmd *marotte.ClientCommand) bool {
	switch cmd.Type {
	case marotte.CmdUserInputResponse:
		// A dismissal advances the agent to its next phase without the user answering,
		// so whether they still owe one is exactly the ambiguity that keeps the claim.
		var p marotte.UserInputResponseCommand
		if json.Unmarshal(cmd.Payload, &p) != nil {
			return false
		}
		return p.Action == marotte.UserInputActionAnswered && p.Answer != ""
	case marotte.CmdElicitationResponse:
		// accept is the only action carrying the form's values; decline and cancel
		// resolve the request having answered nothing it asked.
		var p marotte.ElicitationResponseCommand
		if json.Unmarshal(cmd.Payload, &p) != nil {
			return false
		}
		return p.Action == marotte.ElicitationActionAccept
	case marotte.CmdPermissionResponse:
		// The option's KIND is on the REQUEST, never on this reply, so allow and reject
		// are indistinguishable here — and both are the user deciding, which is what
		// ends the wait. What the reply does prove is that a selection was made: the
		// handler refuses an id the request did not advertise, so a non-empty option id
		// past that gate is one of the agent's own options.
		var p marotte.PermissionResponseCommand
		if json.Unmarshal(cmd.Payload, &p) != nil {
			return false
		}
		return p.OptionID != ""
	}
	return false
}

// discharges reads the table's verdict for cmd, deferring to the turn source (which the
// prompt path applies at StartTurn, not here) and to the payload where the verdict says
// so. False for a command the table does not classify, which is the same fail-toward-
// keeping direction dischargeNo's zero value carries.
func discharges(cmd *marotte.ClientCommand) bool {
	switch commandDischarges[cmd.Type] {
	case dischargeYes:
		return true
	case dischargeByAnswer:
		return answersAgent(cmd)
	}
	return false
}

// noteAnswer discharges the chat's waiting_on_user claim when this command answered the agent;
// called only after a handler succeeded. The run verbs are not commands: a parked step's question
// belongs to another agent's session.
func (d *Dispatcher) noteAnswer(ctx context.Context, cmd *marotte.ClientCommand) {
	if d.status == nil || cmd.ChatID == "" {
		return
	}
	if discharges(cmd) {
		d.status.DischargeWaiting(ctx, cmd.ChatID)
	}
}
