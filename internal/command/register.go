package command

import (
	"context"

	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/marotte/internal/marotte"
)

// RegisterDefaults populates the dispatcher with the standard command
// handlers and returns the membership coordinator it built.
func RegisterDefaults(d *Dispatcher, r *Roles) *Membership {
	mem := NewMembership(&MembershipDeps{
		Chats:    r.Chats,
		Tabs:     r.Tabs,
		Bus:      r.Bus,
		Teardown: r.Teardown,
		// The chat-tab teardown, composed from the three roles it needs.
		CloseChat: func(ctx context.Context, chatID marotte.ChatID) {
			closeChatTeardown(ctx, r.Bridges, r.Perms, r.Teardown, chatID)
		},
		// The delete grade, for a chat the close already erased: everything
		// travels on the captured chain, since the record is gone by now.
		DeleteChat: func(ctx context.Context, chatID marotte.ChatID, sessionChain []string) {
			deleteChatTeardown(ctx, r.Bridges, r.Perms, r.Teardown, chatID, sessionChain)
		},
		// Fails toward KEEPING — deliberately not the purge's reader, whose
		// 0-sentinel points the other way.
		Retention: func(ctx context.Context) bool {
			return settings.RetentionEnabled(ctx, r.Workspace.ConfigDir)
		},
		// The SAME reader prompt.go's auto-create branch uses, so the two seed
		// sites cannot disagree about what the setting says. Fails closed to
		// false.
		SupervisedDefault: func(ctx context.Context) bool {
			return supervisedDefaultSetting(ctx, r.Workspace.ConfigDir)
		},
		Runs: r.Runs,
	})

	d.Register(marotte.CmdCreateChat, bind1(mem, CmdCreateChat))
	d.Register(marotte.CmdResumeSession, bind1(mem, CmdResumeSession))
	d.Register(marotte.CmdCompact, bind1(r.Bridges, CmdCompact))
	d.Register(marotte.CmdSteer, bind3(r.Bridges, r.TurnOutcome, r.Steers, CmdSteer))
	d.Register(marotte.CmdSteerClear, bind1(r.Bridges, CmdSteerClear))
	d.Register(marotte.CmdCreateHook, bind1(r.Workspace, CmdCreateHook))

	d.Register(marotte.CmdOpenTab, bind1(mem, CmdOpenTab))
	d.Register(marotte.CmdCloseTab, bind1(mem, CmdCloseTab))
	d.Register(marotte.CmdReorderTabs, bind1(mem, CmdReorderTabs))
	d.Register(marotte.CmdPinTab, bind1(mem, CmdPinTab))

	d.Register(marotte.CmdSetDraft, bind2(r.Chats, r.Bus, CmdSetDraft))
	d.Register(marotte.CmdSetAttachments, bind2(r.Chats, r.Bus, CmdSetAttachments))
	d.Register(marotte.CmdDeleteChat, bind1(mem, CmdDeleteChat))

	d.Register(marotte.CmdPermissionResponse, bind2(r.Bridges, r.Perms, CmdPermission))
	d.Register(marotte.CmdElicitationResponse, bind2(r.Bridges, r.Perms, CmdElicitationResponse))
	d.Register(marotte.CmdUserInputResponse, bind2(r.Bridges, r.Perms, CmdUserInputResponse))
	d.Register(marotte.CmdRewindChat, bind2(r.Bridges, r.Chats, CmdRewindChat))
	d.Register(marotte.CmdSetEffort, bind4(r.Bridges, r.Chats, r.Bus, r.Workspace, CmdSetEffort))
	d.Register(marotte.CmdSetMode, bind3(r.Bridges, r.Chats, r.Bus, CmdSetMode))
	d.Register(marotte.CmdSetSupervisedMode, bind2(r.Bridges, r.Chats, CmdSetSupervisedMode))

	d.Register(marotte.CmdCancel, bind3(r.Bridges, r.Perms, r.Terminals, CmdCancel))
	d.Register(marotte.CmdForkChat, bind4(r.Bridges, r.Chats, r.Workspace, mem, CmdForkChat))

	d.Register(marotte.CmdPrompt, bind1(&promptRoles{
		bridges:     r.Bridges,
		chats:       r.Chats,
		bus:         r.Bus,
		workspace:   r.Workspace,
		lifecycle:   r.Lifecycle,
		mcp:         r.MCP,
		turnOutcome: r.TurnOutcome,
		auth:        r.AuthReadiness,
	}, CmdPrompt))

	d.status = r.Status
	return mem
}

// bind1 adapts a one-role handler into the Handler signature. Fixed arities
// rather than one variadic binder, so a registration compiles only for the
// roles its handler declared.
func bind1[A any](a A, fn func(context.Context, A, *marotte.ClientCommand) (any, error)) Handler {
	return func(ctx context.Context, cmd *marotte.ClientCommand) (any, error) {
		return fn(ctx, a, cmd)
	}
}

// bind2 adapts a two-role handler into the Handler signature.
func bind2[A, B any](a A, b B, fn func(context.Context, A, B, *marotte.ClientCommand) (any, error)) Handler {
	return func(ctx context.Context, cmd *marotte.ClientCommand) (any, error) {
		return fn(ctx, a, b, cmd)
	}
}

// bind3 adapts a three-role handler into the Handler signature.
func bind3[A, B, C any](a A, b B, c C, fn func(context.Context, A, B, C, *marotte.ClientCommand) (any, error)) Handler {
	return func(ctx context.Context, cmd *marotte.ClientCommand) (any, error) {
		return fn(ctx, a, b, c, cmd)
	}
}

// bind4 adapts a four-role handler into the Handler signature.
func bind4[A, B, C, D any](a A, b B, c C, d D, fn func(context.Context, A, B, C, D, *marotte.ClientCommand) (any, error)) Handler {
	return func(ctx context.Context, cmd *marotte.ClientCommand) (any, error) {
		return fn(ctx, a, b, c, d, cmd)
	}
}
