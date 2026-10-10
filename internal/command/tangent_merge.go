package command

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
)

var (
	// ErrNotATangent is TangentAccess's answer for a chat no session of which was forked.
	ErrNotATangent = errors.New("this chat has no parent chat to merge into")
	// ErrTangentParentGone is TangentAccess's answer when the forked-from session belongs to no
	// chat any more.
	ErrTangentParentGone = errors.New("the chat this tangent came from no longer exists")

	errMergeTangentBusy = errors.New("this tangent is still working; merge it once its turn ends")
	errMergeListFailed  = errors.New("couldn't read the session list to find the chat this tangent came from; try again")
)

// TangentAccess is what a merge reads beyond the prompt path.
type TangentAccess interface {
	// TangentParent answers the chat the tangent chatID was forked from, ErrNotATangent or
	// ErrTangentParentGone, or another error when KAS's session list could not be read.
	TangentParent(ctx context.Context, chatID marotte.ChatID) (marotte.ChatID, error)
	// TurnReply answers the main-lane text the agent wrote in one turn.
	TurnReply(ctx context.Context, chatID marotte.ChatID, turnID string) (string, error)
}

// Wording follows the TUI's `/tangent merge`.
const (
	tangentSummaryLabel  = "Summarizing this tangent for merge"
	tangentSummaryPrompt = "Summarize this tangent so its findings can be merged back into the conversation it " +
		"branched from. Write a self-contained summary for a reader who has not seen this tangent: what was " +
		"asked, what you found or decided and why, and anything still open, keeping the file paths, commands " +
		"and facts that conversation needs. Do not use any tools; answer from this conversation only."
)

// cmdMergeTangent starts a merge of the tangent cmd.ChatID: it answers once the summary turn is
// admitted and finishes in the background, ending in tangent_merged or a tangent_merge_failed
// error on the tangent, either one carrying the command's op_id. A repeat of an admitted op_id
// answers the merge's current state instead of starting another; one still being admitted
// answers 409 in_progress.
func cmdMergeTangent(ctx context.Context, tangents TangentAccess, merges *tangentMerges, cmd *marotte.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var req marotte.MergeTangentCommand
	if len(cmd.Payload) > 0 && json.Unmarshal(cmd.Payload, &req) != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if req.OpID == "" || !validIdent(req.OpID) {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	key := mergeKey{tangent: cmd.ChatID, op: req.OpID}
	if !merges.claim(key) {
		if s := merges.status(ctx, key); s.State != mergeAdmitting && s.State != mergeAbsent {
			return mergeReply(s), nil
		}
		return nil, StatusErrorReason(http.StatusConflict, reasonInProgress, errMergeInProgress)
	}
	if rec, ok := merges.record(ctx, key); ok {
		if rec.State == marotte.TangentMergeRunning {
			merges.resume(ctx, key, &rec)
		} else {
			merges.release(key)
		}
		return mergeReply(mergeStatusOf(&rec)), nil
	}
	parent, messageID, err := admitMerge(ctx, tangents, merges, key)
	if err != nil {
		merges.release(key)
		return nil, err
	}
	reply := mergeReply(mergeStatus{State: string(marotte.TangentMergeRunning), ParentChatID: parent})
	reply["message_id"] = messageID
	return reply, nil
}

func mergeReply(s mergeStatus) map[string]any {
	extra := map[string]any{"state": s.State}
	if s.ParentChatID != "" {
		extra["parent_chat_id"] = s.ParentChatID
	}
	if s.Message != "" {
		extra["message"] = s.Message
	}
	return responseWith(extra)
}

// admitMerge hands key's claim to the background half once the summary turn is admitted.
func admitMerge(ctx context.Context, tangents TangentAccess, merges *tangentMerges, key mergeKey) (marotte.ChatID, string, error) {
	roles := merges.roles
	tangent, ok := roles.chats.Get(ctx, key.tangent)
	if !ok {
		return "", "", StatusError(http.StatusNotFound, ErrChatNotFound)
	}
	parent, err := tangents.TangentParent(ctx, key.tangent)
	switch {
	case errors.Is(err, ErrNotATangent):
		return "", "", StatusError(http.StatusConflict, ErrNotATangent)
	case errors.Is(err, ErrTangentParentGone):
		return "", "", StatusError(http.StatusNotFound, ErrTangentParentGone)
	case err != nil:
		slog.Warn("tangent merge: the parent could not be resolved", "chat_id", key.tangent, keyError, err)
		return "", "", StatusError(http.StatusServiceUnavailable, errMergeListFailed)
	}
	// Like the TUI, a busy tangent is refused rather than queued: the summary must be the
	// next answer.
	if !roles.admission.TryReserveTurn(key.tangent, marotte.TurnSourcePrompt) {
		return "", "", StatusErrorReason(http.StatusConflict, reasonBusy, errMergeTangentBusy)
	}
	deliveryID := ids.NewMessageID()
	if admitErr := merges.admit(ctx, key, parent, deliveryID); admitErr != nil {
		roles.admission.ReleaseTurnReservation(key.tangent)
		slog.Error("tangent merge: the merge could not be recorded", "chat_id", key.tangent, keyError, admitErr)
		return "", "", StatusError(http.StatusInternalServerError, admitErr)
	}
	// Registered before the summary's runner, which can finish first: a drain between the two
	// would find nothing in flight while the merge is still owed.
	roles.lifecycle.InflightAdd(1)
	p := &marotte.PromptCommand{Text: tangentSummaryPrompt, MessageID: ids.NewMessageID(), DisplayText: tangentSummaryLabel}
	turnID, err := startPrompt(ctx, roles, key.tangent, p, launch{awaited: true})
	if err != nil {
		roles.lifecycle.InflightDone()
		if aErr := merges.abandon(ctx, key); aErr != nil {
			slog.Error("tangent merge: a refused merge's record could not be removed", "chat_id", key.tangent, keyError, aErr)
		}
		return "", "", err
	}
	m := &tangentMerge{merges: merges, tangents: tangents, key: key, parent: parent, deliveryID: deliveryID, title: tangent.Name}
	mergeCtx, cancel := roles.lifecycle.TurnContext(ctx)
	go func() {
		defer roles.lifecycle.InflightDone()
		defer cancel()
		defer merges.release(key)
		m.finish(mergeCtx, turnID)
	}()
	return parent, p.MessageID, nil
}

const (
	reasonBusy = "busy"
	// reasonInProgress: this op_id's first attempt is still being admitted, so a retry can still
	// meet its answer.
	reasonInProgress = "in_progress"
)

var errMergeInProgress = errors.New("this merge is still starting")

type tangentMerge struct {
	merges     *tangentMerges
	tangents   TangentAccess
	key        mergeKey
	parent     marotte.ChatID
	deliveryID string
	title      string
}

// finish owns releasing the awaited hold startPrompt took on turnID. A shutdown before KAS's
// receipt is known leaves the record running for the next process to resume.
func (m *tangentMerge) finish(ctx context.Context, turnID string) {
	turns := m.merges.roles.turnOutcome
	result, err := turns.AwaitTurn(ctx, m.key.tangent, turnID)
	turns.ReleaseTurn(m.key.tangent, turnID)
	failure := "The tangent's summary did not finish, so nothing was merged."
	if err == nil && result.Stop == marotte.StopReasonEndTurn && result.Reason == "" && !result.EmittedNothing {
		var settled bool
		if failure, settled = m.merge(ctx, turnID); !settled {
			return
		}
	}
	m.merges.conclude(durable.Context(ctx), m.key, m.parent, failure)
}

// merge hands the summary of turnID to the parent under the recorded delivery id.
func (m *tangentMerge) merge(ctx context.Context, turnID string) (failure string, settled bool) {
	summary, err := m.tangents.TurnReply(durable.Context(ctx), m.key.tangent, turnID)
	if err != nil || summary == "" {
		slog.Warn("tangent merge: the summary could not be read", "chat_id", m.key.tangent, "turn", turnID, keyError, err)
		return "The tangent's summary could not be read, so nothing was merged.", true
	}
	return m.merges.deliver(ctx, m.parent, &marotte.PromptCommand{
		Text:        mergedFindingsText(m.title, summary),
		MessageID:   m.deliveryID,
		DisplayText: displayLabel(`Merging findings from tangent "` + m.title + `"`),
	})
}

func mergedFindingsText(title, summary string) string {
	return `Merged findings from tangent "` + title + `":` + "\n\n" + summary + "\n\n" +
		"Absorb these findings into this conversation. Do not act on them unless I ask you to."
}
