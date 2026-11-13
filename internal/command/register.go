package command

import (
	"cmp"
	"context"

	"github.com/cplieger/go/keylock"
	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

// RegisterDefaults populates the dispatcher with the standard command
// handlers and returns the membership coordinator it built.
func RegisterDefaults(d *Dispatcher, r *Roles) *Membership {
	d.changes = detachedChange{lifecycle: r.Lifecycle, answerBudget: r.AnswerBudget}
	mem := newMembership(&membershipDeps{
		Chats:    r.Chats,
		Tabs:     r.Tabs,
		Bus:      r.Bus,
		Teardown: r.Teardown,
		CloseChat: func(ctx context.Context, chatID marotte.ChatID, stop *chat.Claim) {
			closeChatTeardown(ctx, r.Perms, r.Stops, r.Teardown, chatID, stop)
		},
		// The delete grade, for a chat the close already erased: everything
		// travels on the captured chain, since the record is gone by now.
		DeleteChat: func(ctx context.Context, chatID marotte.ChatID, sessionChain []string, cause RunStopCause) {
			deleteChatTeardown(ctx, r.Perms, r.Stops, r.Teardown, chatID, sessionChain, cause)
		},
		// Fails toward KEEPING — deliberately not the purge's reader, whose
		// 0-sentinel points the other way.
		Retention: func(ctx context.Context) bool {
			return settings.RetentionEnabled(ctx, r.Workspace.ConfigDir)
		},
		// The same reader prompt.go's auto-create uses, so the two seed sites agree; fails closed
		// to false.
		SupervisedDefault: func(ctx context.Context) bool {
			return supervisedDefaultSetting(ctx, r.Workspace.ConfigDir)
		},
		Runs:      r.Runs,
		Sessions:  r.Sessions,
		Lifecycle: r.Lifecycle,
		Impact:    r.Impact,
		Notices:   r.Notices,
	})

	// The handlers take the narrower chatStore; only the coordinator writes keep facts.
	var chats chatStore = r.Chats
	d.register(marotte.CmdCreateChat, bind1(mem, cmdCreateChat))
	d.register(marotte.CmdResumeSession, bind2(mem, r.StepSessions, cmdResumeSession))
	d.RegisterChange(marotte.CmdCompact, bind1[compactRequester](r.Compactor, cmdCompact))
	d.register(marotte.CmdCancelCompact, bind1[compactRequester](r.Compactor, cmdCancelCompact))

	d.register(marotte.CmdOpenTab, bind1(mem, cmdOpenTab))
	d.register(marotte.CmdCloseTab, bind1(mem, cmdCloseTab))
	d.register(marotte.CmdReorderTabs, bind1(mem, cmdReorderTabs))
	d.register(marotte.CmdPinTab, bind1(mem, cmdPinTab))
	d.register(marotte.CmdReparentTab, bind1(mem, cmdReparentTab))
	d.register(marotte.CmdAckTab, bind1(mem, cmdAckTab))

	d.register(marotte.CmdApproveSpecPhase, bind3(r.SpecApprovals, r.Workspace, r.Bus, cmdApproveSpecPhase))

	d.register(marotte.CmdSetDraft, bind2(chats, r.Bus, cmdSetDraft))
	d.register(marotte.CmdSetAttachments, bind2(chats, r.Bus, cmdSetAttachments))
	d.register(marotte.CmdDeleteChat, bind1(mem, cmdDeleteChat))

	d.register(marotte.CmdPermissionResponse, bind2(r.Perms, r.Profiles, cmdPermission))
	d.register(marotte.CmdElicitationResponse, bind1(r.Perms, cmdElicitationResponse))
	d.register(marotte.CmdUserInputResponse, bind1(r.Perms, cmdUserInputResponse))
	d.RegisterChange(marotte.CmdRewindChat, bind6(r.Bridges, chats, r.Admission, r.Run, r.RunCutter, r.Bus, cmdRewindChat))
	configLocks := keylock.New(cmp.Compare[marotte.ChatID])
	d.RegisterChange(marotte.CmdSetEffort, bind6(r.Bridges, chats, r.Bus, r.Workspace, r.Effort, configLocks, cmdSetEffort))
	d.RegisterChange(marotte.CmdSetThinking, bind3(r.Bridges, chats, configLocks, cmdSetThinking))
	d.RegisterChange(marotte.CmdSetMode,
		bind5(r.Bridges, chats, r.Bus, r.Modes, keylock.New(cmp.Compare[marotte.ChatID]), cmdSetMode))
	d.RegisterChange(marotte.CmdSetSupervisedMode,
		bind3(r.Bridges, chats, keylock.New(cmp.Compare[marotte.ChatID]), cmdSetSupervisedMode))

	d.register(marotte.CmdCancel, bind6(r.Run, r.Bridges, r.Perms, r.Terminals, r.Stops, r.SteerQueue, cmdCancel))
	d.carryStop = func(ctx context.Context, chatID marotte.ChatID, rs RunStop) error {
		_, err := carryOutStop(ctx, r.Run, r.Bridges, r.Perms, r.Terminals, r.Stops, chatID, rs, func() {})
		return err
	}
	d.RegisterChange(marotte.CmdForkChat, bind4(r.Bridges, chats, r.Workspace, mem, cmdForkChat))

	d.register(marotte.CmdQueuePrompt, bind3(r.Run, r.Admission, chatReopener(mem), cmdQueuePrompt))
	d.register(marotte.CmdRenameChat, bind2(chats, r.Renamer, cmdRenameChat))

	prompt := &promptRoles{
		bridges:     r.Bridges,
		chats:       r.Chats,
		bus:         r.Bus,
		workspace:   r.Workspace,
		lifecycle:   r.Lifecycle,
		admission:   r.Admission,
		turnOutcome: r.TurnOutcome,
		steers:      r.Steers,
		queue:       r.SteerQueue,
		jobs:        r.SteerJobs,
		runq:        r.Run,
		sendGate:    r.SendGate,
		auth:        r.AuthReadiness,
		signIn:      r.SignIn,
		reopen:      mem,
	}
	d.prompts = prompt
	d.register(marotte.CmdPrompt, bind1(prompt, cmdPrompt))
	d.register(marotte.CmdUnqueuePrompt, bind1(prompt, cmdUnqueuePrompt))
	d.register(marotte.CmdResumeQueue, bind1(prompt, cmdResumeQueue))
	d.register(marotte.CmdSendQueuedNow, bind5(r.Run, r.Bridges, r.Perms, r.Terminals, r.Stops, cmdSendQueuedNow))
	d.merges = newTangentMerges(prompt, mem, r.Loader)
	d.register(marotte.CmdMergeTangent, bind2(r.Tangents, d.merges, cmdMergeTangent))
	d.register(marotte.CmdSteer, bind1(prompt, cmdSteer))
	d.register(marotte.CmdSteerClear, bind1(prompt, cmdSteerClear))
	d.register(marotte.CmdSteerRemove, bind1(prompt, cmdSteerRemove))
	if r.SteerQueue != nil {
		r.SteerQueue.OnSteerJob(runSteerJobs(prompt))
	}

	d.status = r.Status
	return mem
}

// Fixed arities rather than one variadic binder, so a registration compiles only for the roles its
// handler declared.
func bind1[A any](a A, fn func(context.Context, A, *marotte.ClientCommand) (any, error)) Handler {
	return func(ctx context.Context, cmd *marotte.ClientCommand) (any, error) {
		return fn(ctx, a, cmd)
	}
}

func bind2[A, B any](a A, b B, fn func(context.Context, A, B, *marotte.ClientCommand) (any, error)) Handler {
	return func(ctx context.Context, cmd *marotte.ClientCommand) (any, error) {
		return fn(ctx, a, b, cmd)
	}
}

func bind3[A, B, C any](a A, b B, c C, fn func(context.Context, A, B, C, *marotte.ClientCommand) (any, error)) Handler {
	return func(ctx context.Context, cmd *marotte.ClientCommand) (any, error) {
		return fn(ctx, a, b, c, cmd)
	}
}

func bind4[A, B, C, D any](a A, b B, c C, d D, fn func(context.Context, A, B, C, D, *marotte.ClientCommand) (any, error)) Handler {
	return func(ctx context.Context, cmd *marotte.ClientCommand) (any, error) {
		return fn(ctx, a, b, c, d, cmd)
	}
}

func bind5[A, B, C, D, E any](a A, b B, c C, d D, e E, fn func(context.Context, A, B, C, D, E, *marotte.ClientCommand) (any, error)) Handler {
	return func(ctx context.Context, cmd *marotte.ClientCommand) (any, error) {
		return fn(ctx, a, b, c, d, e, cmd)
	}
}

func bind6[A, B, C, D, E, F any](a A, b B, c C, d D, e E, f F, fn func(context.Context, A, B, C, D, E, F, *marotte.ClientCommand) (any, error)) Handler {
	return func(ctx context.Context, cmd *marotte.ClientCommand) (any, error) {
		return fn(ctx, a, b, c, d, e, f, cmd)
	}
}
