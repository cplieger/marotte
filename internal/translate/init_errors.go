package translate

import (
	"context"
	"errors"
	"log/slog"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
)

// HandleAgentNotFound handles _kiro/customAgent/not_found: the requested agent (a mode id on
// v3) does not exist. It persists the fallback mode ("vibe") and broadcasts a typed error.
func (t *Translator) HandleAgentNotFound(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	p, ok := unmarshalParams[struct {
		Requested string `json:"requestedAgent"`
		Fallback  string `json:"fallbackAgent"`
	}](msg, string(marotte.ErrCodeAgentNotFound))
	if !ok {
		return
	}
	if p.Fallback != "" && chatID != "" {
		_, err := t.chats.Mutate(ctx, chatID, func(c *marotte.Chat, ex bool) bool {
			if !ex {
				return false
			}
			c.CurrentModeID = p.Fallback
			return true
		})
		if errors.Is(err, chat.ErrTombstoned) {
			return
		}
		if err != nil {
			slog.Error("agent_not_found: persist fallback", "error", err)
		}
	}
	t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventError, chatID, marotte.ErrorPayload{
		Code:    marotte.ErrCodeAgentNotFound,
		Message: "\"" + displayText(p.Requested) + "\" not found, using \"" + displayText(p.Fallback) + "\"",
	}))
}

// HandleAgentConfigError handles the _kiro/customAgent/config_error
// notification ({path, error}); the extra v3 sessionId is ignored.
func (t *Translator) HandleAgentConfigError(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	p, ok := unmarshalParams[struct {
		Path  string `json:"path"`
		Error string `json:"error"`
	}](msg, "agent_config_error")
	if !ok {
		return
	}
	t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventError, chatID, marotte.ErrorPayload{
		Code:    marotte.ErrCodeAgentConfigError,
		Message: displayText(t.relPath(p.Path)) + ": " + displayText(p.Error),
	}))
}

// HandleRateLimit handles _kiro/error/rate_limit ({message}) as a typed error.
func (t *Translator) HandleRateLimit(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	p, ok := unmarshalParams[struct {
		Message string `json:"message"`
	}](msg, "rate_limit")
	if !ok {
		return
	}
	t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventError, chatID, marotte.ErrorPayload{
		Code:    marotte.ErrCodeRateLimit,
		Message: displayText(p.Message),
	}))
}

// HandleSystemNotify handles _kiro/system/notify ({level, message}). The frame carries
// no sessionId, so chatID is the bridge that received it. The message is forwarded
// verbatim and never parsed: KAS sends several unrelated texts on this one method.
func (t *Translator) HandleSystemNotify(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	p, ok := unmarshalParams[struct {
		Level   string `json:"level"`
		Message string `json:"message"`
	}](msg, "system/notify")
	if !ok || p.Message == "" {
		return
	}
	payload := marotte.SystemNoticePayload{
		Level:   noticeLevel(p.Level),
		Message: displayText(p.Message),
	}
	if c, ok := t.chats.Get(ctx, chatID); ok {
		payload.ChatName = c.Name
	} else if name, ok := t.chats.DepartedName(chatID); ok {
		payload.ChatName = name
	}
	t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventSystemNotice, chatID, payload))
}

// noticeLevel folds a wire level onto the closed vocabulary; an unknown one is info.
func noticeLevel(raw string) marotte.NoticeLevel {
	switch l := marotte.NoticeLevel(raw); l {
	case marotte.NoticeInfo, marotte.NoticeWarning, marotte.NoticeError:
		return l
	}
	slog.Debug("system/notify: unrecognised level, shown as info", "level", logsafe.Field(raw))
	return marotte.NoticeInfo
}
