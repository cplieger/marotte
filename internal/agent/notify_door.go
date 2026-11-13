package agent

import (
	"context"
	"fmt"
	"slices"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/notify"
)

// notifyDoor delivers the notify route's messages into chats; its fields are the runtime's, as Notifier binds them.
type notifyDoor struct {
	enter       func() (release func(), ok bool)
	engineReady func() bool
	chats       func(ctx context.Context) []marotte.ChatHeader
	deliver     func(ctx context.Context, chatID marotte.ChatID, text string, from *marotte.Caller) (command.NotifyDelivery, error)
}

var _ notify.Deliverer = notifyDoor{}

// Notifier is the notify route's way into this runtime's chats.
func (rt *Runtime) Notifier() notify.Deliverer {
	return notifyDoor{
		enter:       rt.lifecycle.enterUnlessDraining,
		engineReady: rt.engine.isReady,
		chats:       rt.chatStore.List,
		deliver:     rt.dispatcher.Notify,
	}
}

// Deliver holds the drain gate for the whole delivery, so a shutdown waits for it rather than cutting it.
func (d notifyDoor) Deliver(ctx context.Context, session, text, source string) (string, error) {
	release, ok := d.enter()
	if !ok {
		return "", notify.ErrUnavailable
	}
	defer release()
	if !d.engineReady() {
		return "", notify.ErrUnavailable
	}
	chatID, ok := chatForSession(d.chats(ctx), session)
	if !ok {
		return "", notify.ErrUnknownSession
	}
	got, err := d.deliver(ctx, chatID, text, &marotte.Caller{Label: source})
	if err != nil {
		return "", notifyError(err)
	}
	switch got {
	case command.NotifySteer:
		return notify.DeliveredSteer, nil
	case command.NotifyPrompt:
		return notify.DeliveredPrompt, nil
	case command.NotifyQueued:
		return notify.DeliveredQueued, nil
	}
	return "", fmt.Errorf("notify: unknown delivery %q", got)
}

// chatForSession answers the chat whose current session is session, else the one chat whose earlier sessions hold
// it; two such chats answer none rather than a guess.
func chatForSession(headers []marotte.ChatHeader, session string) (marotte.ChatID, bool) {
	var (
		owner  marotte.ChatID
		owners int
	)
	for i := range headers {
		h := &headers[i]
		if h.ACPSessionID == session {
			return marotte.ChatID(h.ID), true
		}
		if slices.Contains(h.PriorACPSessionIDs, session) {
			owner = marotte.ChatID(h.ID)
			owners++
		}
	}
	return owner, owners == 1
}

func notifyError(err error) error {
	switch command.ClassifyNotifyError(err) {
	case command.NotifyUnknownChat:
		return notify.ErrUnknownSession
	case command.NotifyRetryLater:
		return notify.ErrUnavailable
	case command.NotifyBadText:
		return &notify.TextRefusedError{Reason: err.Error()}
	case command.NotifyFailed:
	}
	return err
}
