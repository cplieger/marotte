package command

// A tangent keeps the conversation and opens another beside it (a rewind edits the one you are in).
// It forks at the head, or after a named turn (a branch), and `session/fork` carries KAS's own
// context; the parent record must survive until the tangent is minted. Only a head fork opens fresh
// when KAS refuses: a branch answers the refusal and creates nothing.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rpcerr"
	"github.com/cplieger/marotte/internal/translate"
)

var errForkParentUnknown = errors.New("the chat this tangent came from no longer exists")

// errForkParentIsSelf guards forking a chat into itself, which would rebind
// its own session id through RecordSession and retire the session it is
// still using.
var errForkParentIsSelf = errors.New("a tangent cannot fork the chat it opens into")

func forkPayload(cmd *marotte.ClientCommand) (marotte.ForkChatCommand, error) {
	var p marotte.ForkChatCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return p, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	// One sanitized title serves the record and KAS, so the two cannot disagree.
	p.Title = translate.SanitizeTitle(p.Title)
	if !ids.ValidChatID(string(p.ParentChatID)) || userNameTooLong(p.Title) {
		return p, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if !validIdent(p.OpID) || (p.MessageID != "" && !validMessageID(p.MessageID)) {
		return p, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	return p, nil
}

// cmdForkChat opens a tangent off another chat and returns the chat it
// created plus the tab it opened for it. The new chat's id is minted here
// when the envelope carries none.
func cmdForkChat(ctx context.Context, bridges bridgeAccess, chats chatStore, ws Workspace, mem *Membership, cmd *marotte.ClientCommand) (any, error) {
	p, err := forkPayload(cmd)
	if err != nil {
		return nil, err
	}

	// Read the ledger before session/fork: a retry that already produced a
	// chat must not ask KAS to fork again. A READ rather than the
	// coordinator's resolve, because the fork round trip must not happen
	// under the operation lock — a bridge Call has no client-side timeout.
	chatID, replay := mem.resolvedChat(p.OpID)
	if cmd.ChatID != "" {
		chatID, replay = cmd.ChatID, false
	}
	if chatID != "" && chatID == p.ParentChatID {
		return nil, StatusError(http.StatusBadRequest, errForkParentIsSelf)
	}
	if replay {
		if resp, done, replayErr := replayedFork(ctx, bridges, chats, mem, p, chatID); done {
			return resp, replayErr
		}
		// The op was recorded but its chat is not there: the first attempt
		// reserved the id and then failed. Fall through and fork for real.
	}

	parent, ok := chats.Get(ctx, p.ParentChatID)
	if !ok {
		return nil, StatusError(http.StatusNotFound, errForkParentUnknown)
	}

	atMessage, err := branchPoint(ctx, chats, p)
	if err != nil {
		return nil, err
	}
	// The parent's model and mode ride along so the tangent's answers come
	// from the same agent that produced the conversation it inherited.
	sessionID, err := forkSession(ctx, bridges, ws, p, parent, atMessage)
	if err != nil {
		return nil, err
	}

	opened, err := mem.CreateChatAndOpen(ctx, forkCreate(p, chatID, parent, sessionID))
	if err != nil {
		return nil, err
	}
	if opened.Replay && sessionID != "" && opened.Chat.ACPSessionID != sessionID {
		slog.Warn("tangent: a concurrent attempt of this op already opened the chat, so this attempt's forked session is bound to nothing",
			"chat", opened.Chat.ID, "parent", p.ParentChatID, "orphaned_session", sessionID)
	}

	var inherited bool
	opened.Chat, inherited = loadForkedHistory(ctx, bridges, chats, opened.Chat)
	outcome := forkOutcomeOf(opened.Chat.ACPSessionID, inherited)

	slog.Info("tangent opened",
		"chat", opened.Chat.ID, "parent", p.ParentChatID, "at_message", p.MessageID,
		"outcome", outcome, "acp_session", opened.Chat.ACPSessionID, "tab", opened.Subject.ID)
	return openedResponse(&opened, map[string]any{
		"outcome":    outcome,
		"session_id": opened.Chat.ACPSessionID,
	}), nil
}

// replayedFork answers a repeated op from the chat it already created; done is false when that chat is not there.
func replayedFork(ctx context.Context, bridges bridgeAccess, chats chatStore, mem *Membership,
	p marotte.ForkChatCommand, chatID marotte.ChatID,
) (resp any, done bool, err error) {
	c, exists := chats.Get(ctx, chatID)
	if !exists {
		return nil, false, nil
	}
	// Through the coordinator even on the replay: the first attempt
	// can have created the chat and then failed its tab write, and
	// this finishes that. Open is idempotent, so the ordinary
	// replay costs one scan and emits nothing.
	opened, err := mem.CreateChatAndOpen(ctx, forkCreate(p, "", c, c.ACPSessionID))
	if err != nil {
		return nil, true, err
	}
	// The repeat finishes a load the first attempt never STARTED, and
	// skips one whose session that attempt already lost.
	var inherited bool
	opened.Chat, inherited = loadForkedHistory(ctx, bridges, chats, opened.Chat)
	outcome := forkOutcomeOf(opened.Chat.ACPSessionID, inherited)
	slog.Info("tangent: repeat op resolved to the chat it already opened",
		"chat", chatID, "parent", p.ParentChatID, "outcome", outcome)
	return openedResponse(&opened, map[string]any{
		"outcome":    outcome,
		"session_id": opened.Chat.ACPSessionID,
	}), true, nil
}

// loadForkedHistory gives the tangent the transcript KAS already holds for it and answers the
// RE-READ record, plus whether it still names the session the fork produced: the fork binds its
// session inside the create, so a non-empty retirement chain means that session was retired and the
// tangent inherited nothing. It refuses nothing, and a failure does not heal itself.
func loadForkedHistory(ctx context.Context, bridges bridgeAccess, chats chatStore, c *marotte.Chat) (*marotte.Chat, bool) {
	if c == nil || c.ACPSessionID == "" {
		return c, false
	}
	// Deliberately not a comparison against the id THIS attempt forked: a losing
	// concurrent attempt's record names the WINNER's session, and that one inherited.
	if len(c.PriorACPSessionIDs) > 0 {
		return c, false
	}
	want := c.ACPSessionID
	id := marotte.ChatID(c.ID)
	if turns, _ := chats.TurnCount(ctx, id); turns == 0 {
		resumeForkedSession(ctx, bridges, id, want)
		if refreshed, exists := chats.Get(ctx, id); exists {
			c = refreshed
		}
	}
	return c, c.ACPSessionID == want
}

// Its own bridge rather than the parent's, which is already resumed: the projection is keyed by
// chat, so a load issued on the parent's bridge would ingest the fork's history into the PARENT's
// transcript.
func resumeForkedSession(ctx context.Context, bridges bridgeAccess, chatID marotte.ChatID, want string) {
	// durable: the fork's answer reports whether the resume held, which a client leaving must not cut short.
	bridge, err := bridges.OpenBridge(durable.Context(ctx), chatID, "")
	if err != nil || bridge == nil {
		slog.Warn("tangent: the forked session could not be resumed, so the tangent opens empty",
			"chat", chatID, "acp_session", want, keyError, err)
		return
	}
	if string(bridge.SessionID()) != want {
		slog.Warn("tangent: the resume fell through to a fresh session, so the tangent lost its inherited context",
			"chat", chatID, "want", want, "got", bridge.SessionID())
	}
}

// forkOutcomeOf reads a tangent's path off what it ended up with: only a chat still
// bound to the session the fork produced was forked, so a refused fork AND a resume
// that fell through to a fresh session both report fresh.
func forkOutcomeOf(sessionID string, inherited bool) string {
	if sessionID == "" || !inherited {
		return marotte.ForkOutcomeFresh
	}
	return marotte.ForkOutcomeForked
}

// forkCreate is the tangent's create request: one builder for both call
// sites (replay and fresh fork) so the record's shape cannot drift between
// them. The tab hangs under the parent's tab, which is what makes a tangent
// read as a tangent; a parent with no open tab promotes it to top level.
func forkCreate(p marotte.ForkChatCommand, chatID marotte.ChatID, parent *marotte.Chat, sessionID string) *ChatCreate {
	return &ChatCreate{
		OpID:        p.OpID,
		ChatID:      chatID,
		RequireChat: p.ParentChatID,
		ParentChat:  p.ParentChatID,
		Init: func(c *marotte.Chat) {
			c.Name = marotte.DefaultChatName
			// Only a session KAS forked carries the parent link a merge resolves through.
			c.Tangent = sessionID != ""
			// KAS latches a tangent's fork title as the user's, so the record does too.
			if p.Title != "" {
				c.Name = p.Title
				c.NameSetByUser = true
			}
			c.Model = parent.Model
			c.CurrentModeID = parent.CurrentModeID
			// The level in force: KAS's fork drops it, so the tangent's load must assert it.
			c.Effort = cmp.Or(parent.Effort, parent.EffortActive)
			// Inherited like the model, mode and effort: dropping the review gate would let a
			// supervised chat's tangent write files without asking.
			c.SupervisedMode = parent.SupervisedMode
			if sessionID != "" {
				// RecordSession, not assignment: it is the sanctioned writer
				// of this field and keeps the reaper's keep-list chain
				// invariant.
				c.RecordSession(sessionID)
			}
		},
	}
}

// branchPoint is the KAS id a branch forks after, "" for a head fork. A target with no bind on the
// current session falls back to the prompt id, as a rewind does.
func branchPoint(ctx context.Context, chats chatStore, p marotte.ForkChatCommand) (string, error) {
	if p.MessageID == "" {
		return "", nil
	}
	target, found, err := chats.PromptTarget(ctx, p.ParentChatID, p.MessageID)
	if err != nil {
		slog.Error("tangent: resolve the branch point", "parent", p.ParentChatID, keyError, err)
		return "", StatusError(http.StatusInternalServerError, err)
	}
	if !found {
		return "", StatusError(http.StatusBadRequest, errBranchTargetNotFound)
	}
	return cmp.Or(target.KASMessageID, p.MessageID), nil
}

// forkSession asks KAS to fork the parent's session, after atMessage when it is set, and returns
// the new session id. A head fork answers "" and no error on every refusal, so the tangent opens
// fresh; a branch answers the refusal instead, because a fresh chat is not the conversation the
// user asked for.
func forkSession(ctx context.Context, bridges bridgeAccess, ws Workspace, p marotte.ForkChatCommand, parent *marotte.Chat, atMessage string) (string, error) {
	branch := atMessage != ""
	// Read before the resume, which can fall through to session/new and retire this id.
	want := parent.ACPSessionID
	if branch && want == "" {
		return "", StatusError(http.StatusConflict, errBranchNoSession)
	}
	bridge := parentBridge(ctx, bridges, p.ParentChatID)
	if bridge == nil {
		if branch {
			return "", StatusError(http.StatusBadGateway, errBranchNoBridge)
		}
		return "", nil
	}
	if branch && string(bridge.SessionID()) != want {
		// A resume that fell through to a fresh session holds none of the turns to branch from.
		slog.Warn("tangent: the parent's original session was not resumed, so the branch was not attempted",
			"parent", p.ParentChatID, "want", want, "got", bridge.SessionID())
		return "", StatusError(http.StatusConflict, errBranchSessionNotResumed)
	}

	meta := map[string]any{"createdReason": marotte.CreatedReasonTangent}
	if p.Title != "" {
		meta["title"] = p.Title
	}
	if branch {
		meta["messageId"] = atMessage
	}
	cctx, cancel := bridges.CallBudget(ctx, engineChangeBudget)
	defer cancel()
	resp, err := bridge.Call(cctx, marotte.MethodSessionFork, sessionParams(bridge, map[string]any{
		"cwd":   ws.Dir,
		"_meta": map[string]any{"kiro": meta},
	}))
	if err != nil {
		slog.Warn("tangent: session/fork failed",
			"parent", p.ParentChatID, "at_message", atMessage, keyError, err)
		return "", branchRefusal(branch, err)
	}
	var out struct {
		SessionID string `json:"sessionId"`
	}
	if resp != nil && resp.Result != nil {
		_ = json.Unmarshal(resp.Result, &out)
	}
	if !ids.ValidSessionID(out.SessionID) {
		// A reply with no usable session id is a refusal however it is
		// spelled. Validated because the value reaches a filesystem path
		// inside KAS and marotte's own reaper keep-list.
		slog.Warn("tangent: session/fork returned no usable session id",
			"parent", p.ParentChatID, "at_message", atMessage)
		if branch {
			return "", StatusError(http.StatusBadGateway, errBranchNotReturned)
		}
		return "", nil
	}
	return out.SessionID, nil
}

// parentBridge is the parent chat's bridge with a session, opened when it is cold, or nil. A
// session/fork is addressed through the parent chat's own bridge, so a cold parent's opens first;
// the empty model keeps the parent's own.
func parentBridge(ctx context.Context, bridges bridgeAccess, parentID marotte.ChatID) Bridge {
	if bridge := bridges.Bridge(parentID); bridge != nil && bridge.SessionID() != "" {
		return bridge
	}
	bridge, err := bridges.OpenBridge(ctx, parentID, "")
	if err != nil || bridge == nil || bridge.SessionID() == "" {
		slog.Warn("tangent: parent bridge unavailable", "parent", parentID, keyError, err)
		return nil
	}
	return bridge
}

// branchRefusal is a failed session/fork's answer: none for a head fork, KAS's own reason for a
// refused branch point, and a plain refusal for a transport failure, whose text names internals.
func branchRefusal(branch bool, err error) error {
	if !branch {
		return nil
	}
	if _, ok := errors.AsType[*marotte.RPCError](err); ok {
		return StatusError(http.StatusConflict, fmt.Errorf("%s: %w", rpcerr.Text(err), errBranchTurnUnknown))
	}
	return StatusError(http.StatusBadGateway, errBranchNotReturned)
}
