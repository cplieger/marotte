package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/cplieger/marotte/internal/bot"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/workspace"
)

// botRoleNotLoaded is the Bot's own mode_not_applied text: a Bot's pill has no mode menu, so the regular chat's "pick
// the mode again" is advice it cannot follow.
const botRoleNotLoaded = "This Bot's role did not load. Its next message retries."

// errBotRoleNotLoaded fails the generation; its text is the reason a failed prompt shows (rpcerr.Text).
var errBotRoleNotLoaded = errors.New(botRoleNotLoaded) //nolint:staticcheck // ST1005: the text is the user's sentence.

// botRoleSetTimeout bounds one session/set_mode to the role, and one context-file edit.
const botRoleSetTimeout = 10 * time.Second

// holdBotDoor runs a started Bot's door steps on its just-opened session, the role then the memory file; any other
// chat's door has none, since only a started Bot's door sends the role (chatSessionOpts).
func (bc *bridgeCoordinator) holdBotDoor(ctx context.Context, rec *marotte.Chat, session engineSession, loaded bool) error {
	if !rec.IsStartedBot() {
		return nil
	}
	if err := bc.holdBotRole(ctx, marotte.ChatID(rec.ID), session); err != nil {
		return err
	}
	bc.holdBotMemory(ctx, rec, session, loaded)
	return nil
}

// holdBotRole puts a Bot's just-opened session on its role before any of its facts are written, sending
// session/set_mode when the door reported another mode. A refusal fails the generation with the Bot's own message,
// so a Bot never runs a turn without its role.
func (bc *bridgeCoordinator) holdBotRole(ctx context.Context, chatID marotte.ChatID, session engineSession) error {
	if session.CurrentMode() == marotte.BotRoleID {
		return nil
	}
	err := bc.setBotRole(ctx, chatID, session)
	if err == nil {
		return nil
	}
	slog.Error("a Bot's session did not take its role; the open fails",
		"chat_id", chatID, "reported", session.CurrentMode(), "error", err)
	bc.broadcast(ctx, marotte.NewEvent(marotte.EventError, chatID, marotte.ErrorPayload{
		Code: marotte.ErrCodeModeNotApplied, Message: botRoleNotLoaded,
	}))
	return errBotRoleNotLoaded
}

var errBotRoleMissing = errors.New("KAS reported the Bot role absent from its session")

// setBotRole sends the session to the role. KAS answers set_mode to a mode the session does not hold with {} after
// sending _kiro/customAgent/not_found for it and falling back, so the refusal is read off the lane up to the reply.
func (bc *bridgeCoordinator) setBotRole(ctx context.Context, chatID marotte.ChatID, s acpSessionCaller) error {
	sb := bc.bridgeFor(chatID)
	if sb != nil {
		// A not_found folded before this call (replayed on a load, or from a fallback since accepted) is stale.
		sb.takeRoleMissing()
	}
	cctx, cancel := context.WithTimeout(ctx, botRoleSetTimeout)
	defer cancel()
	_, seq, err := s.CallAt(cctx, marotte.MethodSetMode, map[string]any{"sessionId": s.SessionID(), "modeId": marotte.BotRoleID})
	if err != nil {
		return err
	}
	if !bc.turns.awaitPosition(cctx, chatID, seq) {
		return fmt.Errorf("%w: the session's frames up to the reply were not read", errBotRoleMissing)
	}
	if sb != nil && sb.takeRoleMissing() {
		return errBotRoleMissing
	}
	return nil
}

// noteBotRoleMissing wraps the not_found handler to note KAS reporting the Bot role absent, which setBotRole reads.
func (bc *bridgeCoordinator) noteBotRoleMissing(next chatHandler) chatHandler {
	return func(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
		next(ctx, chatID, msg)
		var p struct {
			Requested string `json:"requestedAgent"`
		}
		if json.Unmarshal(msg.Params, &p) != nil || p.Requested != marotte.BotRoleID {
			return
		}
		if sb := bc.bridgeFor(chatID); sb != nil {
			sb.markRoleMissing()
		}
	}
}

// botMemoryNotLoaded is the Bot's warning when its memory file did not load: the Bot still answers, without its notes.
const botMemoryNotLoaded = "This Bot's memory did not load, so it is working without its notes."

var errBotMemoryRefused = errors.New("the session refused the context file")

// botPaths are the directories a Bot's role and memory file live under.
func botPaths() bot.Paths {
	return bot.Paths{KiroHome: workspace.KiroHome()}
}

// holdBotMemory makes a Bot's just-opened session load its memory file as a context file while memory is on, and drop
// it on a load while memory is off: KAS keeps a session's context files across loads, and a new session has none. A
// call is tried twice; a failed add warns in the chat, and the open goes on either way.
func (bc *bridgeCoordinator) holdBotMemory(ctx context.Context, rec *marotte.Chat, session engineSession, loaded bool) {
	chatID := marotte.ChatID(rec.ID)
	file, ok := bot.MemoryFile(rec, botPaths())
	params := marotte.SessionContextParams{SessionID: session.SessionID(), Subcommand: marotte.SessionContextAdd, Path: file, Force: true}
	switch {
	case !rec.Bot.Memory && !loaded:
		return
	case !rec.Bot.Memory && !ok:
		slog.Warn("a Bot's session kept its memory file after memory was turned off: no path under an absolute "+
			"KIRO_HOME names it to remove", "chat_id", chatID)
		return
	case !rec.Bot.Memory:
		params.Subcommand, params.Force = marotte.SessionContextRemove, false
	case !ok:
		bc.warnBotMemory(ctx, chatID, errors.New("no memory file under an absolute KIRO_HOME"))
		return
	}
	err := callSessionContext(ctx, session, &params)
	if err != nil {
		err = callSessionContext(ctx, session, &params)
	}
	switch {
	case err == nil:
	case params.Subcommand == marotte.SessionContextRemove:
		slog.Warn("a Bot's session kept its memory file after memory was turned off", "chat_id", chatID, "error", err)
	default:
		bc.warnBotMemory(ctx, chatID, err)
	}
}

func (bc *bridgeCoordinator) warnBotMemory(ctx context.Context, chatID marotte.ChatID, err error) {
	slog.Warn("a Bot's session opened without its memory file", "chat_id", chatID, "error", err)
	bc.broadcast(ctx, marotte.NewEvent(marotte.EventError, chatID, marotte.ErrorPayload{
		Code: marotte.ErrCodeMemoryNotLoaded, Message: botMemoryNotLoaded,
	}))
}

// callSessionContext sends one context-file edit; KAS answers a refusal it handled as {success: false}.
func callSessionContext(ctx context.Context, s acpSessionCaller, params *marotte.SessionContextParams) error {
	cctx, cancel := context.WithTimeout(ctx, botRoleSetTimeout)
	defer cancel()
	resp, err := s.Call(cctx, marotte.MethodSessionContext, params)
	if err != nil {
		return err
	}
	var r struct {
		Success bool `json:"success"`
	}
	if json.Unmarshal(resp.Result, &r) != nil || !r.Success {
		return fmt.Errorf("%w: %s", errBotMemoryRefused, resp.Result)
	}
	return nil
}

// healBotRole wraps the mode handler to note a Bot's session moving off its role, which reassertBotRole undoes once
// the turn ends: a set_mode inside the turn would cut across the agent's own switch.
func (bc *bridgeCoordinator) healBotRole(next sessionUpdateHandler) sessionUpdateHandler {
	return func(ctx context.Context, chatID marotte.ChatID, raw json.RawMessage, attr translate.FrameAttribution) {
		next(ctx, chatID, raw, attr)
		if !attr.ChatOwned() {
			return
		}
		var p struct {
			ModeID string `json:"currentModeId"`
		}
		if json.Unmarshal(raw, &p) != nil || p.ModeID == "" || p.ModeID == marotte.BotRoleID {
			return
		}
		sb := bc.bridgeFor(chatID)
		if sb == nil {
			return
		}
		if rec, ok := bc.chatStore.Get(ctx, chatID); ok && rec.Bot != nil {
			sb.markRoleLeft()
		}
	}
}

// reassertBotRole sends a Bot's session back to its role after a turn that moved it off. On a failure the session
// stays off the role, so lc's next door reloads it and that door's hold restores the role or fails the open.
func (bc *bridgeCoordinator) reassertBotRole(ctx context.Context, lc *chatLifecycle) {
	sb := bc.bridgeFor(lc.chatID)
	if sb == nil || !sb.takeRoleLeft() {
		return
	}
	err := sb.use(ctx, func(ctx context.Context, s engineSession) error { return bc.setBotRole(ctx, lc.chatID, s) })
	switch {
	case errors.Is(err, marotte.ErrChatClosedDuringCall):
		slog.Debug("Bot role re-assert stood down: the chat closed", "chat_id", lc.chatID)
	case err != nil:
		lc.markReload(sb)
		slog.Warn("a Bot's session left its role and the re-assert failed; its next message reloads the session",
			"chat_id", lc.chatID, "error", err)
	}
}
