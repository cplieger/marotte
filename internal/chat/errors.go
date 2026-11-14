package chat

import (
	"errors"
	"fmt"

	"github.com/cplieger/marotte/internal/marotte"
)

// ErrTombstoned reports a write declined because the chat id was deleted within the tombstone window: nothing reached
// disk or the wire. Applied writes and no-ops both return nil, so callers that must not proceed check errors.Is.
var ErrTombstoned = errors.New("chat: id was recently deleted")

func errInvalidChatID(id marotte.ChatID) error {
	return fmt.Errorf("invalid chat id: %q", id)
}

// errChatIDMismatch reports a chat whose own ID disagrees with the file it was about to be written to. Never
// retargeted: the destination and its mutex come from the requested id.
func errChatIDMismatch(want marotte.ChatID, got string) error {
	return fmt.Errorf("chat %q holds id %q: refusing to write it over another chat's file", want, got)
}

const errMsgChatNotFound = "chat not found"
