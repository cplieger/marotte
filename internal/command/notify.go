package command

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
)

// NotifyDelivery is how Notify delivered a message.
type NotifyDelivery string

// The deliveries, spelled as the notify route answers them.
const (
	NotifyPrompt NotifyDelivery = "prompt"
	NotifySteer  NotifyDelivery = "steer"
)

// errNotifyMention refuses a context-mention token: the prompt path would read the named resource into the chat on
// an outside caller's word.
var errNotifyMention = errors.New(`a "#[[kind:query]]" context-mention token is not accepted from an outside caller`)

// Notify delivers text from an outside caller into chatID the way the page's Send does: one prompt, which the chat's
// run order steers into a steerable turn, starts, or queues. from must be non-nil: a caller-sent prompt neither ends
// the chat's waiting_on_user claim nor touches its composer. A refusal is a 400 StatusError for text that reads as a
// system notice or holds a context-mention token, else the prompt's StatusError.
func (d *Dispatcher) Notify(ctx context.Context, chatID marotte.ChatID, text string, from *marotte.Caller) (NotifyDelivery, error) {
	switch {
	case notificationPrefix.MatchString(text):
		return "", StatusError(http.StatusBadRequest, errSteerLooksLikeNotification)
	case mentionRe.MatchString(text):
		return "", StatusError(http.StatusBadRequest, errNotifyMention)
	}
	payload, err := json.Marshal(map[string]string{"text": text, "message_id": ids.NewMessageID()})
	if err != nil {
		return "", err
	}
	fn, ok := d.handler(marotte.CmdPrompt)
	if !ok {
		return "", StatusError(http.StatusInternalServerError, errors.New("unknown command: "+string(marotte.CmdPrompt)))
	}
	out, err := fn(inProcess(ctx), &marotte.ClientCommand{Type: marotte.CmdPrompt, ChatID: chatID, Payload: payload, From: from})
	if err != nil {
		return "", err
	}
	if ack, ok := out.(promptAck); ok && ack.As == submitSteered {
		return NotifySteer, nil
	}
	return NotifyPrompt, nil
}

// NotifyRefusal is what a Notify error means to its outside caller.
type NotifyRefusal int

// The refusal classes; NotifyFailed is any other fault, which the notify route answers 500.
const (
	NotifyFailed NotifyRefusal = iota
	NotifyUnknownChat
	// NotifyRetryLater is a refusal the same message can pass shortly: a chat starting, closing or busy again, a full
	// steer buffer, or an engine call that did not land.
	NotifyRetryLater
	NotifyBadText
)

// ClassifyNotifyError classes err, a Notify error.
func ClassifyNotifyError(err error) NotifyRefusal {
	if errors.Is(err, ErrChatNotFound) {
		return NotifyUnknownChat
	}
	code, _, _ := StatusOf(err)
	switch code {
	case http.StatusBadRequest:
		return NotifyBadText
	case http.StatusConflict, http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
		return NotifyRetryLater
	}
	return NotifyFailed
}
