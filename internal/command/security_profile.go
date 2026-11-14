package command

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/policyfile"
)

var (
	errUnknownSecurityProfile = errors.New("unknown security profile")
	errRunSecurityProfile     = errors.New("a run follows the security profile of the chat that launched it")
)

// cmdSetSecurityProfile records the chat's security profile and reloads only that chat's resident session on it:
// presets ride the session door and KAS has no live setter.
func cmdSetSecurityProfile(
	ctx context.Context, chats chatStore, reloads profileReloader, cmd *marotte.ClientCommand,
) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var p marotte.SetSecurityProfileCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if strings.HasPrefix(string(cmd.ChatID), marotte.RunSubjectPrefix) {
		return nil, StatusError(http.StatusBadRequest, errRunSecurityProfile)
	}
	profile, ok := policyfile.ProfileFor(p.Profile)
	if !ok {
		return nil, StatusError(http.StatusBadRequest, errUnknownSecurityProfile)
	}
	found, changed := false, false
	if _, err := chats.Mutate(ctx, cmd.ChatID, func(c *marotte.Chat, exists bool) bool {
		if !exists {
			return false
		}
		found = true
		if c.SecurityProfile == profile.ID {
			return false
		}
		c.SecurityProfile = profile.ID
		changed = true
		return true
	}); err != nil {
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	if !found {
		return nil, StatusError(http.StatusNotFound, ErrChatNotFound)
	}
	if changed {
		reloads.ReloadChatProfile(cmd.ChatID)
		slog.Info("chat security profile set", "chat_id", cmd.ChatID, "profile", profile.ID)
	}
	return responseWith(map[string]any{"profile": profile.ID}), nil
}
