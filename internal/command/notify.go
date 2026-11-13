package command

import (
	"context"
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
	NotifyQueued NotifyDelivery = "queued"
)

// errNotifyMention refuses a context-mention token: the prompt path would read the named resource into the chat on
// an outside caller's word.
var errNotifyMention = errors.New(`a "#[[kind:query]]" context-mention token is not accepted from an outside caller`)

// Notify places text from an outside caller on chatID's run order as the notify door's entry (RunFromNotify) and
// answers where it went: steered into the running turn, started, or queued, which includes a caller's earlier entry
// still waiting taking the text. from must be non-nil: a caller's message neither ends the chat's waiting_on_user
// claim nor touches its composer. A refusal is a 400 StatusError for text that reads as a system notice or holds a
// context-mention token, else Submit's refusal as HTTP.
func (d *Dispatcher) Notify(ctx context.Context, chatID marotte.ChatID, text string, from *marotte.Caller) (NotifyDelivery, error) {
	switch {
	case notificationPrefix.MatchString(text):
		return "", StatusError(http.StatusBadRequest, errSteerLooksLikeNotification)
	case mentionRe.MatchString(text):
		return "", StatusError(http.StatusBadRequest, errNotifyMention)
	}
	roles := d.prompts
	s := Submission{
		Row:    marotte.QueuedPrompt{ID: ids.NewMessageID(), Text: text, Caller: from},
		Origin: marotte.RunFromNotify, Kind: marotte.SendPlain,
		Wait: true, Retry: !kasClaimsPromptText(text),
	}
	ans, err := roles.runq.Submit(ctx, chatID, roles.admission.LifeStamp(), &s)
	if err != nil {
		return "", submitRefusal(err)
	}
	switch ans.As {
	case SubmitStarted:
		return NotifyPrompt, nil
	case SubmitSteered:
		return NotifySteer, nil
	case SubmitQueued:
	}
	return NotifyQueued, nil
}

// NotifyRefusal is what a Notify error means to its outside caller.
type NotifyRefusal int

// The refusal classes; NotifyFailed is any other fault, which the notify route answers 500.
const (
	NotifyFailed NotifyRefusal = iota
	NotifyUnknownChat
	// NotifyRetryLater is a refusal the same message can pass shortly: a chat starting, closing or busy again, a full
	// steer buffer, an engine call that did not land, or a message past the draft cap that would wait behind a running
	// turn.
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
	case http.StatusConflict, http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout,
		http.StatusRequestEntityTooLarge:
		return NotifyRetryLater
	}
	return NotifyFailed
}
