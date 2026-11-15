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
	errRunSecurityProfile     = errors.New("a run's security profile cannot be changed")
)

// cmdSetSecurityProfile records the chat's security profile and applies the move to that chat alone: presets ride the
// session door and KAS has no live setter.
func cmdSetSecurityProfile(ctx context.Context, moves profileMover, cmd *marotte.ClientCommand) (any, error) {
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
	found, changed, err := moves.MoveChatProfile(ctx, cmd.ChatID, profile.ID)
	if err != nil {
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	if !found {
		return nil, StatusError(http.StatusNotFound, ErrChatNotFound)
	}
	if changed {
		slog.Info("chat security profile set", "chat_id", cmd.ChatID, "profile", profile.ID)
	}
	return responseWith(map[string]any{"profile": profile.ID}), nil
}
