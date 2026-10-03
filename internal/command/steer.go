package command

// Mid-turn steering, via KAS's own buffer. `_session/steer` appends to a
// per-session steering buffer the graph consumes at the next node boundary as an
// ordinary human turn; nothing is cancelled. Idle sends are prompts, mid-turn
// sends are steers.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
)

// maxSteerBytes caps a steer's text — deliberately the same cap as a
// prompt's, since a steer is a prompt as far as the user is concerned.
const maxSteerBytes = maxPromptBytes

// notificationPrefix mirrors KAS's NOTIFICATION_PREFIX_RE. KAS decides
// whether a steer is a user message or a system notification by sniffing
// the text against this pattern, not by a parameter, and a notification is
// skipped by the session/load re-injection that carries real steers across
// a resume — so the reclassification changes whether the message survives
// a reload.
var notificationPrefix = regexp.MustCompile(`^\s*\[notification/(info|success|warning|error)\]`)

var (
	// errSteerNoTurn is the refusal for a steer with no turn to join.
	errSteerNoTurn = errors.New("nothing is running to steer — send this as a prompt instead")
	// errSteerLooksLikeNotification refuses the sniffing collision above,
	// rather than escaping (would alter what the user wrote) or passing
	// through (would file it as a system notification and drop it on
	// resume).
	errSteerLooksLikeNotification = errors.New(
		`a message starting with "[notification/...]" is read as a system notice, not as your words — start it with anything else`,
	)
)

// reasonNoTurn is the 409 refusal class for a steer with no turn to join.
// The client branches on the value and retries the text as an ordinary
// prompt.
const reasonNoTurn = "no_turn"

// reasonFull is the 409 refusal class for a steer the chat's record cannot hold.
// Not `no_turn`, deliberately: the client converts that one into a prompt, and a
// prompt would meet the same running turn.
const reasonFull = "full"

// errSteerFull is the refusal for a steer that would be the chat's 65th unread
// message, whether parked, queued or waiting.
var errSteerFull = errors.New("too many messages are waiting for the agent — wait for it to read some, then send again")

// steerReply is KAS's answer to one _session/steer.
type steerReply struct {
	Dropped string `json:"dropped"`
	Queued  bool   `json:"queued"`
}

// sendSteer issues one _session/steer for the steer steerID names. KAS prefixes
// the messageId it is sent into the steer's own id, so the wire carries the id
// with marotte.SteerIDPrefix removed.
func sendSteer(ctx context.Context, sb sessionCaller, steerID, text string) (steerReply, error) {
	resp, err := sb.Call(ctx, marotte.MethodSessionSteer, steerParams(sb, steerID, text))
	return decodeSteerReply(resp), err
}

func steerParams(sb sessionScoped, steerID, text string) map[string]any {
	return SessionParams(sb, map[string]any{
		"message":   text,
		"messageId": strings.TrimPrefix(steerID, marotte.SteerIDPrefix),
	})
}

func decodeSteerReply(resp *marotte.RPCResponse) steerReply {
	var reply steerReply
	if resp != nil && resp.Result != nil {
		_ = json.Unmarshal(resp.Result, &reply)
	}
	return reply
}

// CmdSteer delivers a message into the running turn. It requires a holder whose
// turn drains the steering buffer: KAS queues a steer for any live session, so one
// sent to an idle chat or a `!cmd` shell turn would sit unread with the chip stuck
// "queued". Every steer command takes the chat's steer lock, so a delete's clear
// and its resubmit never interleave with a new steer.
func CmdSteer(ctx context.Context, roles *promptRoles, cmd *marotte.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	text, steerID, err := steerText(cmd)
	if err != nil {
		return nil, err
	}
	unlock, err := roles.queue.LockSteerOps(ctx, cmd.ChatID)
	if err != nil {
		return nil, StatusError(http.StatusServiceUnavailable, err)
	}
	defer unlock()
	switch refuse, sendErr := steerOne(ctx, roles, cmd.ChatID, steerID, text, false); {
	case refuse == SteerRefuseFull:
		return nil, StatusErrorReason(http.StatusConflict, reasonFull, errSteerFull)
	case refuse != "":
		return nil, StatusErrorReason(http.StatusConflict, reasonNoTurn, errSteerNoTurn)
	case sendErr != nil:
		// KAS throws (rather than answering) for an unknown session and an empty
		// message, both ruled out, so this is a transport or liveness failure. The
		// row stays where KAS may hold it, and its fate is observed.
		return nil, StatusError(http.StatusBadGateway, sendErr)
	}
	slog.Info("steer accepted", "chat", cmd.ChatID, "steer_id", steerID)
	return steerAccepted(steerID), nil
}

// steerOne is the one place a user steer is routed, new or re-routed; the error is
// the send carrying key.
func steerOne(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, key, text string, delivering bool) (refuse string, err error) {
	source, held := roles.admission.AdmissionHolderSource(chatID)
	h := SteerHolder{
		Held:        held && source != marotte.TurnSourceLocalShell,
		PromptClass: source.PromptClass(),
		Live:        roles.bridges.BridgeLive(chatID),
		Delivering:  delivering,
	}
	sends, refuse := roles.queue.RouteSteer(chatID, key, text, h)
	if refuse != "" {
		return refuse, nil
	}
	return "", issueSteers(ctx, roles, chatID, sends, key, func(s SteerSend, queued bool, err error) {
		roles.queue.SteerSent(chatID, s, queued, err)
	})
}

// issueSteers writes the ledger BEFORE each call: KAS emits steering_queued before
// it answers, on the Forward goroutine, so a record written after the reply races
// the fold.
func issueSteers(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, sends []SteerSend, key string, done func(SteerSend, bool, error)) error {
	sb := roles.bridges.Bridge(chatID)
	var keyErr error
	for _, s := range sends {
		roles.steers.RecordUserSteer(chatID, s.ID, s.Keys)
		if sb == nil {
			done(s, false, nil)
			continue
		}
		reply, err := sendSteer(ctx, sb, s.ID, s.Text)
		done(s, reply.Queued, err)
		switch {
		case err != nil:
			slog.Warn("steer: bridge call failed", "chat", chatID, "steer_id", s.ID, keyError, err)
			if s.ID == key || slices.Contains(s.Keys, key) {
				keyErr = err
			}
		case !reply.Queued:
			// The epoch moved while KAS persisted it: the turn end decides the rows.
			slog.Info("steer dropped", "chat", chatID, "steer_id", s.ID, "reason", reply.Dropped)
		}
	}
	return keyErr
}

// steerText validates the steer payload and answers its trimmed text and the id
// KAS will stamp on it. The id is derivable (marotte.SteerIDFor) because KAS
// prefixes the messageId we send; the two shapes that would make it wrong are
// refused here: an empty id, and a notification-prefixed text KAS would file
// under `notify-`.
func steerText(cmd *marotte.ClientCommand) (text, steerID string, err error) {
	var p marotte.SteerCommand
	if json.Unmarshal(cmd.Payload, &p) != nil {
		return "", "", StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	text = strings.TrimSpace(p.Text)
	switch {
	case text == "":
		return "", "", StatusError(http.StatusBadRequest, errEmptyPrompt)
	case len(text) > maxSteerBytes:
		return "", "", StatusError(http.StatusRequestEntityTooLarge, errPromptTooLong)
	case !ValidMessageID(p.MessageID):
		return "", "", StatusError(http.StatusBadRequest, errMissingMessageID)
	case notificationPrefix.MatchString(text):
		return "", "", StatusError(http.StatusBadRequest, errSteerLooksLikeNotification)
	}
	return text, marotte.SteerIDFor(p.MessageID), nil
}

func steerAccepted(steerID string) map[string]any {
	return responseWith(map[string]any{"steer_id": steerID})
}

// CmdSteerClear is Discard all. It does not cancel the turn, and a row the agent
// read before the clear landed keeps its read entry rather than a deleted one.
func CmdSteerClear(ctx context.Context, roles *promptRoles, cmd *marotte.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	unlock, err := roles.queue.LockSteerOps(ctx, cmd.ChatID)
	if err != nil {
		return nil, StatusError(http.StatusServiceUnavailable, err)
	}
	defer unlock()
	opID := ids.NewMessageID()
	needsClear, refuse := roles.queue.BeginDiscard(cmd.ChatID, opID)
	if refuse != "" {
		return nil, steerRefusal(refuse)
	}
	cleared := []string{}
	if needsClear {
		rpcCtx, cancel := context.WithTimeout(durable.Context(ctx), steerRemoveBudget)
		defer cancel()
		defer endSteerOp(rpcCtx, roles, cmd.ChatID, opID)
		names, landed := clearSteerBuffer(rpcCtx, roles, cmd.ChatID)
		if res := roles.queue.DiscardCleared(cmd.ChatID, opID, landed); res.Reason != "" {
			return nil, steerRefusal(res.Reason)
		}
		cleared = names
	}
	slog.Info("steers discarded", "chat", cmd.ChatID, "cleared", len(cleared))
	return responseWith(map[string]any{"cleared": cleared}), nil
}

// clearSteerBuffer issues one _session/steer/clear and waits until every frame KAS
// wrote before its reply has folded, so the reply's ids and the frames agree.
// landed false is a clear whose outcome is unknown: no reply, or a read loop that
// never reached the reply, where an unfolded read would let a delivered row be
// classified deleted. No bridge means no buffer, which a clear would find empty.
func clearSteerBuffer(ctx context.Context, roles *promptRoles, chatID marotte.ChatID) (cleared []string, landed bool) {
	sb := roles.bridges.Bridge(chatID)
	if sb == nil {
		return nil, true
	}
	resp, seq, err := sb.CallAt(ctx, marotte.MethodSessionSteerClear, SessionParams(sb))
	if err != nil {
		slog.Warn("steer clear: bridge call failed", "chat", chatID, keyError, err)
		return nil, false
	}
	if !roles.queue.AwaitReadLoop(ctx, chatID, seq) {
		slog.Warn("steer clear: the frames before its reply never folded", "chat", chatID)
		return nil, false
	}
	var result struct {
		MessageIDs []string `json:"messageIds"`
	}
	if resp != nil && resp.Result != nil {
		_ = json.Unmarshal(resp.Result, &result)
	}
	return result.MessageIDs, true
}
