package command

import (
	"context"

	"github.com/cplieger/marotte/internal/chatlock"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

// RegisterDefaults populates the dispatcher with the standard command
// handlers and returns the membership coordinator it built.
func RegisterDefaults(d *Dispatcher, r *Roles) *Membership {
	mem := newMembership(&membershipDeps{
		Chats:    r.Chats,
		Tabs:     r.Tabs,
		Bus:      r.Bus,
		Teardown: r.Teardown,
		CloseChat: func(ctx context.Context, chatID marotte.ChatID) {
			closeChatTeardown(ctx, r.Bridges, r.Stops, r.Teardown, chatID)
		},
		// The delete grade, for a chat the close already erased: everything
		// travels on the captured chain, since the record is gone by now.
		DeleteChat: func(ctx context.Context, chatID marotte.ChatID, sessionChain []string) {
			deleteChatTeardown(ctx, r.Bridges, r.Stops, r.Teardown, chatID, sessionChain)
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
		Runs:     r.Runs,
		Sessions: r.Sessions,
	})

	d.Register(marotte.CmdCreateChat, bind1(mem, cmdCreateChat))
	d.Register(marotte.CmdResumeSession, bind1(mem, cmdResumeSession))
	d.Register(marotte.CmdCompact, bind1(r.Bridges, cmdCompact))

	d.Register(marotte.CmdOpenTab, bind1(mem, cmdOpenTab))
	d.Register(marotte.CmdCloseTab, bind1(mem, cmdCloseTab))
	d.Register(marotte.CmdReorderTabs, bind1(mem, cmdReorderTabs))
	d.Register(marotte.CmdPinTab, bind1(mem, cmdPinTab))
	d.Register(marotte.CmdReparentTab, bind1(mem, cmdReparentTab))

	d.Register(marotte.CmdApproveSpecPhase, bind3(r.SpecApprovals, r.Workspace, r.Bus, cmdApproveSpecPhase))

	d.Register(marotte.CmdSetDraft, bind2(r.Chats, r.Bus, cmdSetDraft))
	d.Register(marotte.CmdSetAttachments, bind2(r.Chats, r.Bus, cmdSetAttachments))
	d.Register(marotte.CmdDeleteChat, bind1(mem, cmdDeleteChat))

	d.Register(marotte.CmdPermissionResponse, bind2(r.Perms, r.Profiles, cmdPermission))
	d.Register(marotte.CmdElicitationResponse, bind1(r.Perms, cmdElicitationResponse))
	d.Register(marotte.CmdUserInputResponse, bind1(r.Perms, cmdUserInputResponse))
	d.Register(marotte.CmdRewindChat, bind5(r.Bridges, r.Chats, r.Admission, r.RunCutter, r.Bus, cmdRewindChat))
	configLocks := chatlock.New()
	d.Register(marotte.CmdSetEffort, bind6(r.Bridges, r.Chats, r.Bus, r.Workspace, r.Effort, configLocks, cmdSetEffort))
	d.Register(marotte.CmdSetThinking, bind3(r.Bridges, r.Chats, configLocks, cmdSetThinking))
	d.Register(marotte.CmdSetMode, bind4(r.Bridges, r.Chats, r.Bus, r.Modes, cmdSetMode))
	d.Register(marotte.CmdSetSupervisedMode, bind2(r.Bridges, r.Chats, cmdSetSupervisedMode))

	d.Register(marotte.CmdCancel, bind5(r.Bridges, r.Perms, r.Terminals, r.Stops, r.SteerQueue, cmdCancel))
	d.Register(marotte.CmdForkChat, bind4(r.Bridges, r.Chats, r.Workspace, mem, cmdForkChat))

	d.Register(marotte.CmdQueuePrompt, bind1(r.Queue, cmdQueuePrompt))
	d.Register(marotte.CmdSetInterruptMode, bind1[chatMutator](r.Chats, cmdSetInterruptMode))
	d.Register(marotte.CmdRenameChat, bind3(r.Bridges, r.Chats, r.Renamer, cmdRenameChat))

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
		followups:   r.Queue,
		compactor:   r.Compactor,
		auth:        r.AuthReadiness,
	}
	d.prompts = prompt
	d.Register(marotte.CmdPrompt, bind1(prompt, cmdPrompt))
	d.Register(marotte.CmdUnqueuePrompt, bind1(prompt, cmdUnqueuePrompt))
	d.merges = newTangentMerges(prompt, mem, r.Loader)
	d.Register(marotte.CmdMergeTangent, bind2(r.Tangents, d.merges, cmdMergeTangent))
	d.Register(marotte.CmdSteer, bind1(prompt, cmdSteer))
	d.Register(marotte.CmdSteerClear, bind1(prompt, cmdSteerClear))
	d.Register(marotte.CmdSteerRemove, bind1(prompt, cmdSteerRemove))
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
