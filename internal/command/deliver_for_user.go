package command

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
)

var errDeliverUnwired = errors.New("the prompt path is not registered")

// DeliverForUser sends text into chatID as the user's own message: a prompt shown under label
// when the chat is idle, its bridge opening as for any prompt, else a steer into the running turn.
// The composer is left alone. It returns once KAS has taken the prompt or queued the steer, or the
// steer row waits in the chat's record for its turn; an error means nothing was delivered.
func (d *Dispatcher) DeliverForUser(ctx context.Context, chatID marotte.ChatID, text, label string) error {
	if d.prompts == nil || d.prompts.queue == nil {
		return errDeliverUnwired
	}
	return deliverForUser(ctx, d.prompts, chatID, text, label)
}

// The second pass covers a turn that ended between the admission read and the steer.
func deliverForUser(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, text, label string) error {
	for range 2 {
		if roles.admission.ReserveTurnForPrompt(ctx, chatID, AdmissionWait) == AdmissionAcquired {
			return promptForUser(ctx, roles, chatID, text, label)
		}
		refuse, err := steerForUser(ctx, roles, chatID, text)
		switch {
		case err != nil:
			return err
		case refuse == "":
			return nil
		case refuse != SteerRefuseNoTurn:
			return fmt.Errorf("the chat refused the steer: %s", refuse)
		}
	}
	return errBusy
}

// KAS's turn_bind is its receipt; a turn that finalizes unbound never reached the agent.
func promptForUser(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, text, label string) error {
	p := &marotte.PromptCommand{Text: text, MessageID: ids.NewMessageID(), DisplayText: displayLabel(label)}
	turnID, err := startPrompt(ctx, roles, chatID, p, launch{awaited: true})
	if err != nil {
		return err
	}
	defer roles.turnOutcome.ReleaseTurn(chatID, turnID)
	bound, err := roles.turnOutcome.AwaitTurnBound(ctx, chatID, turnID)
	switch {
	case err != nil:
		return err
	case !bound:
		return errors.New("the prompt's turn ended before the agent received it")
	}
	return nil
}

func steerForUser(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, text string) (refuse string, err error) {
	text, steerID, err := ValidSteerText(text, ids.NewMessageID())
	if err != nil {
		return "", err
	}
	unlock, err := roles.queue.LockSteerOps(ctx, chatID)
	if err != nil {
		return "", err
	}
	defer unlock()
	refuse, dropped, err := steerInto(ctx, roles.steerTarget(chatID), steerHolder(roles, chatID, ""), steerID, text)
	switch {
	case dropped:
		// KAS's turn ended before marotte saw it (`epoch_changed`): the chat is idle to KAS.
		return SteerRefuseNoTurn, nil
	case refuse == "" && err == nil:
		slog.Info("steer accepted", "chat", chatID, "steer_id", steerID)
	}
	return refuse, err
}
