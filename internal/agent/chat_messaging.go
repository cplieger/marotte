package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
)

// chatMessagingOn is whether rec's session takes part in messages between chats: the Settings switch, unless the
// chat chose out.
func chatMessagingOn(settingOn bool, rec *marotte.Chat) bool {
	return settingOn && !rec.MessagingOff
}

// ApplyChatMessaging converges chatID's resident session on its messaging choice in the background, past the
// caller's return; a turn running now leaves it to the turn's close.
func (rt *Runtime) ApplyChatMessaging(ctx context.Context, chatID marotte.ChatID) {
	rt.lifecycle.goUnlessDraining(func() {
		actx, cancel := context.WithCancel(durable.Context(ctx))
		defer cancel()
		stop := context.AfterFunc(rt.lifecycle.shutdownCtx, cancel)
		defer stop()
		rt.ensureChatMessaging(actx, chatID)
	})
}

// ensureChatMessaging sets an idle chat's resident session to the messaging value its record and the Settings switch
// make now. It opens no session: one that is not resident takes the value at its door (chatSessionOpts). A busy chat,
// or a refusal, changes nothing and is retried at the next turn close (betweenTurns), since KAS refuses the option
// while a turn runs.
func (rt *Runtime) ensureChatMessaging(ctx context.Context, chatID marotte.ChatID) {
	unlock, err := rt.coord.messagingLocks.Lock(ctx, chatID)
	if err != nil {
		return
	}
	defer unlock()
	if rt.coord.turns.live(chatID) || !rt.coord.turns.kasAllowsSessionCall(chatID) {
		return
	}
	sb := rt.coord.bridgeFor(chatID)
	if sb == nil || !sb.open() {
		return
	}
	rec, ok := rt.chatStore.Get(ctx, chatID)
	if !ok {
		return
	}
	on := chatMessagingOn(rt.readSessionSettings(ctx).Features.Messaging, rec)
	err = sb.use(ctx, func(ctx context.Context, s engineSession) error { return s.EnsureMessaging(ctx, on) })
	switch {
	case err == nil:
	case errors.Is(err, marotte.ErrChatClosedDuringCall), messagingRefusedBusy(err):
		slog.Debug("messaging between chats not applied yet; the next turn close retries",
			"chat_id", chatID, "enabled", on, "error", err)
	default:
		slog.Warn("messaging between chats not applied; the next turn close retries",
			"chat_id", chatID, "enabled", on, "error", err)
	}
}

// kasRefusedMidTurnCode is KAS's -32000, which set_config_option answers for sessionToolsMessaging while a turn runs.
const kasRefusedMidTurnCode = -32000

// messagingRefusedBusy reports a refusal because the session was running: a turn KAS started won the race with the
// idle check.
func messagingRefusedBusy(err error) bool {
	var rpc *marotte.RPCError
	return errors.Is(err, marotte.ErrSessionBusy) || errors.As(err, &rpc) && rpc.Code == kasRefusedMidTurnCode
}

// syncChatMessaging converges every resident chat session on its messaging value, so a Settings flip reaches open
// chats live. A run's host session is not a chat and keeps its door's value.
func (rt *Runtime) syncChatMessaging(ctx context.Context) {
	for chatID := range rt.bridge.mgr.all() {
		if marotte.IsRunHostChat(chatID) {
			continue
		}
		rt.ensureChatMessaging(ctx, chatID)
	}
}

// observeMessaging hands the sessionToolsMessaging value a chat's own config_option_update reports to its session,
// which compares against it. It never asserts: the frame can arrive mid-turn, where KAS refuses the option.
func (bc *bridgeCoordinator) observeMessaging(next sessionUpdateHandler) sessionUpdateHandler {
	return func(ctx context.Context, chatID marotte.ChatID, raw json.RawMessage, attr translate.FrameAttribution) {
		next(ctx, chatID, raw, attr)
		if !attr.ChatOwned() {
			return
		}
		value, offered := marotte.ConfigOptionValue(raw, marotte.ConfigOptionSessionMessaging)
		if !offered {
			return
		}
		if sb := bc.bridgeFor(chatID); sb != nil {
			sb.observeMessaging(value)
		}
	}
}
