package command

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
)

// notifyAdmitWait is how long a notify waits out a held close of its chat: one that commits at once is absorbed, and
// a longer one answers ErrSubmitStarting, which the caller retries, rather than holding the answer.
const notifyAdmitWait = 50 * time.Millisecond

// errNotifyMention refuses a context-mention token: the prompt path would read the named resource into the chat on
// an outside caller's word.
var errNotifyMention = errors.New(`a "#[[kind:query]]" context-mention token is not accepted from an outside caller`)

// Notify places text from an outside caller on chatID's run order as the notify door's entry (RunFromNotify) and
// returns once it is placed, with the id of the run entry holding the text; the run order delivers it after, as a
// steer, a turn of its own or in turn. A caller's earlier entry still waiting takes the text instead, and one kiro-cli
// holds unread covers it. from must be non-nil: a caller's message neither ends the chat's waiting_on_user claim nor
// touches its composer. A refusal is a 400 StatusError for text that reads as a system notice or holds a
// context-mention token, else Submit's refusal as HTTP.
func (d *Dispatcher) Notify(ctx context.Context, chatID marotte.ChatID, text string, from *marotte.Caller) (string, error) {
	switch {
	case notificationPrefix.MatchString(text):
		return "", StatusError(http.StatusBadRequest, errSteerLooksLikeNotification)
	case mentionRe.MatchString(text):
		return "", StatusError(http.StatusBadRequest, errNotifyMention)
	}
	roles := d.prompts
	if err := refuseBotDraft(ctx, roles.chats, chatID); err != nil {
		return "", err
	}
	// Before the submit, as a prompt's: a message into an idle chat starts a turn, whose tab must exist first.
	stamp, err := roles.door.BeginMessage(ctx, chatID, true, roles.admission.LifeStamp)
	if err != nil {
		return "", err
	}
	s := Submission{
		Row:    marotte.QueuedPrompt{ID: ids.NewMessageID(), Text: text, Caller: from},
		Origin: marotte.RunFromNotify, Kind: marotte.SendPlain,
		AdmitWait: notifyAdmitWait, Retry: !kasClaimsPromptText(text),
	}
	a, err := roles.runq.Submit(ctx, chatID, stamp, &s)
	if err != nil {
		return "", submitRefusal(err)
	}
	return a.MessageID, nil
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
