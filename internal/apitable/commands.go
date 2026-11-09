package apitable

import (
	"reflect"

	"github.com/cplieger/marotte/internal/marotte"
)

// Command is one member of POST /api/command's request union: the type tag and the struct its
// payload decodes as, nil for a command that carries none.
type Command struct {
	Payload reflect.Type
	Type    marotte.CommandType
}

func cmd[P any](t marotte.CommandType) Command {
	return Command{Type: t, Payload: reflect.TypeFor[P]()}
}

// Commands is every command type with its payload, in commands.go's order.
func Commands() []Command {
	return []Command{
		cmd[marotte.CreateChatCommand](marotte.CmdCreateChat),
		cmd[marotte.ResumeSessionCommand](marotte.CmdResumeSession),
		cmd[marotte.ForkChatCommand](marotte.CmdForkChat),
		cmd[marotte.PromptCommand](marotte.CmdPrompt),
		cmd[marotte.CancelCommand](marotte.CmdCancel),
		cmd[marotte.CloseTabCommand](marotte.CmdDeleteChat),
		cmd[marotte.SwitchModelCommand](marotte.CmdSwitchModel),
		cmd[marotte.PermissionResponseCommand](marotte.CmdPermissionResponse),
		cmd[marotte.ElicitationResponseCommand](marotte.CmdElicitationResponse),
		cmd[marotte.UserInputResponseCommand](marotte.CmdUserInputResponse),
		cmd[marotte.RewindChatCommand](marotte.CmdRewindChat),
		{Type: marotte.CmdCompact},
		{Type: marotte.CmdCancelCompact},
		cmd[marotte.SetEffortCommand](marotte.CmdSetEffort),
		cmd[marotte.SetThinkingCommand](marotte.CmdSetThinking),
		cmd[marotte.SetDraftCommand](marotte.CmdSetDraft),
		cmd[marotte.SetAttachmentsCommand](marotte.CmdSetAttachments),
		cmd[marotte.SetModeCommand](marotte.CmdSetMode),
		cmd[marotte.SetSupervisedModeCommand](marotte.CmdSetSupervisedMode),
		cmd[marotte.SteerCommand](marotte.CmdSteer),
		{Type: marotte.CmdSteerClear},
		cmd[marotte.QueuePromptCommand](marotte.CmdQueuePrompt),
		cmd[marotte.UnqueuePromptCommand](marotte.CmdUnqueuePrompt),
		cmd[marotte.SendQueuedNowCommand](marotte.CmdSendQueuedNow),
		cmd[marotte.SetInterruptModeCommand](marotte.CmdSetInterruptMode),
		cmd[marotte.RenameChatCommand](marotte.CmdRenameChat),
		cmd[marotte.SteerRemoveCommand](marotte.CmdSteerRemove),
		cmd[marotte.OpenTabCommand](marotte.CmdOpenTab),
		cmd[marotte.CloseTabCommand](marotte.CmdCloseTab),
		cmd[marotte.ReorderTabsCommand](marotte.CmdReorderTabs),
		cmd[marotte.PinTabCommand](marotte.CmdPinTab),
		cmd[marotte.ReparentTabCommand](marotte.CmdReparentTab),
		cmd[marotte.AckTabCommand](marotte.CmdAckTab),
		cmd[marotte.ApproveSpecPhaseCommand](marotte.CmdApproveSpecPhase),
		cmd[marotte.MergeTangentCommand](marotte.CmdMergeTangent),
	}
}
