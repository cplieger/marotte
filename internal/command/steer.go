package command

// `_session/steer` appends to KAS's per-session steering buffer, read at the next node boundary as
// an ordinary human turn; nothing is cancelled.

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
const maxSteerBytes = MaxPromptBytes

// notificationPrefix mirrors KAS's NOTIFICATION_PREFIX_RE. KAS classifies a steer as a system
// notification by sniffing its text, and a notification is skipped by the session/load
// re-injection, so a match changes whether the message survives a reload.
var notificationPrefix = regexp.MustCompile(`^\s*\[notification/(info|success|warning|error)\]`)

var (
	errSteerNoTurn = errors.New("nothing is running to steer, so send this as a prompt instead")
	// errSteerLooksLikeNotification refuses that collision: escaping would alter the user's text,
	// passing through would drop it on resume.
	errSteerLooksLikeNotification = errors.New(
		`a message starting with "[notification/...]" is read as a system notice, not as your words. Start it with anything else`,
	)
)

// reasonNoTurn is the 409 refusal class for a steer with no turn to join.
// The client branches on the value and retries the text as an ordinary
// prompt.
const reasonNoTurn = "no_turn"

// reasonFull is the 409 refusal class for a steer or a follow-up the chat has no
// room for. Not `no_turn`, deliberately: the client converts that one into a
// prompt, and a prompt would meet the same running turn.
const reasonFull = "full"

var errSteerFull = errors.New("too many unread messages are waiting in this chat. Wait for the agent to read them, then send again")

// steerReply is KAS's answer to one _session/steer.
type steerReply struct {
	Dropped string `json:"dropped"`
	Queued  bool   `json:"queued"`
}

// KAS prefixes the messageId it is sent into the steer's own id, so the wire carries the id with
// marotte.SteerIDPrefix removed.
func sendSteer(ctx context.Context, sb SessionCaller, steerID, text string) (steerReply, error) {
	resp, err := sb.Call(ctx, marotte.MethodSessionSteer, steerParams(sb, steerID, text))
	return decodeSteerReply(resp), err
}

// SteerTarget is one steering buffer and what reaches it: a chat's own session, or a run step's
// session on its run's carrier. Key names the buffer's record in Queue and Ledger. Caller is nil
// when no live process holds the session.
type SteerTarget struct {
	Caller SessionCaller
	Queue  SteerQueue
	Ledger SteerRecorder
	Key    marotte.ChatID
}

func (r *promptRoles) steerTarget(chatID marotte.ChatID) SteerTarget {
	t := SteerTarget{Queue: r.queue, Ledger: r.steers, Key: chatID}
	if sb := r.bridges.Bridge(chatID); sb != nil {
		t.Caller = sb
	}
	return t
}

func steerParams(sb sessionScoped, steerID, text string) map[string]any {
	return sessionParams(sb, map[string]any{
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

// cmdSteer delivers a message into the running turn. It requires a holder whose turn drains the
// buffer: a steer to an idle chat or a `!cmd` turn would sit unread. Every steer command takes the
// chat's steer lock, so a delete's clear and resubmit never interleave with a new steer.
func cmdSteer(ctx context.Context, roles *promptRoles, cmd *marotte.ClientCommand) (any, error) {
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
	switch refuse, sendErr := steerOne(ctx, roles, cmd.ChatID, steerID, text, ""); {
	case refuse == SteerRefuseFull:
		return nil, StatusErrorReason(http.StatusConflict, reasonFull, errSteerFull)
	case refuse != "":
		return nil, StatusErrorReason(http.StatusConflict, reasonNoTurn, errSteerNoTurn)
	case sendErr != nil:
		// Unknown session and empty message are ruled out, so this is a transport or liveness
		// failure; the row stays where KAS may hold it.
		return nil, StatusError(http.StatusBadGateway, sendErr)
	}
	slog.Info("steer accepted", "chat", cmd.ChatID, "steer_id", steerID)
	return steerAccepted(steerID), nil
}

// The error is the send carrying key. turn is the prompt turn a delivery is aimed at, empty for a
// new steer.
func steerOne(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, key, text, turn string) (refuse string, err error) {
	source, held := roles.admission.AdmissionHolderSource(chatID)
	h := SteerHolder{
		Held:        held && source != marotte.TurnSourceLocalShell,
		PromptClass: source.PromptClass(),
		Live:        roles.bridges.BridgeLive(chatID),
		Turn:        turn,
		Delivering:  turn != "",
	}
	return SteerInto(ctx, roles.steerTarget(chatID), h, key, text)
}

// SteerInto routes one steer row into t's buffer under the holder h and sends what the record
// answers. The caller holds t's steer lock. refuse is the record's refusal class; err is the
// failed send carrying key.
func SteerInto(ctx context.Context, t SteerTarget, h SteerHolder, key, text string) (refuse string, err error) {
	sends, refuse := t.Queue.RouteSteer(t.Key, key, text, h)
	if refuse != "" {
		return refuse, nil
	}
	return "", issueSteers(ctx, t, sends, key, func(s SteerSend, queued bool, err error) {
		t.Queue.SteerSent(t.Key, s, queued, err)
	})
}

// SendPlanned sends rows the record already routed (a flush job's plan) into t. The caller holds t's
// steer lock.
func SendPlanned(ctx context.Context, t SteerTarget, sends []SteerSend) {
	_ = issueSteers(ctx, t, sends, "", func(s SteerSend, queued bool, err error) {
		t.Queue.SteerSent(t.Key, s, queued, err)
	})
}

// issueSteers writes the ledger BEFORE each call: KAS emits steering_queued before
// it answers, on the Forward goroutine, so a record written after the reply races
// the fold.
func issueSteers(ctx context.Context, t SteerTarget, sends []SteerSend, key string, done func(SteerSend, bool, error)) error {
	var keyErr error
	for _, s := range sends {
		t.Ledger.RecordUserSteer(t.Key, s.ID)
		if t.Caller == nil {
			done(s, false, nil)
			continue
		}
		reply, err := sendSteer(ctx, t.Caller, s.ID, s.Text)
		done(s, reply.Queued, err)
		switch {
		case err != nil:
			slog.Warn("steer: bridge call failed", "chat", t.Key, "steer_id", s.ID, keyError, err)
			if s.ID == key || slices.Contains(s.Keys, key) {
				keyErr = err
			}
		case !reply.Queued:
			slog.Info("steer dropped", "chat", t.Key, "steer_id", s.ID, "reason", reply.Dropped)
		}
	}
	return keyErr
}

func steerText(cmd *marotte.ClientCommand) (text, steerID string, err error) {
	var p marotte.SteerCommand
	if json.Unmarshal(cmd.Payload, &p) != nil {
		return "", "", StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	return ValidSteerText(p.Text, p.MessageID)
}

// ValidSteerText answers a steer's trimmed text and the id KAS will stamp on it (marotte.SteerIDFor),
// or a StatusError. It refuses the two shapes that break that derivation: an empty id, and text KAS
// would file under `notify-`.
func ValidSteerText(raw, messageID string) (text, steerID string, err error) {
	text = strings.TrimSpace(raw)
	switch {
	case text == "":
		return "", "", StatusError(http.StatusBadRequest, errEmptyPrompt)
	case len(text) > maxSteerBytes:
		return "", "", StatusError(http.StatusRequestEntityTooLarge, errPromptTooLong)
	case !validMessageID(messageID):
		return "", "", StatusError(http.StatusBadRequest, errMissingMessageID)
	case notificationPrefix.MatchString(text):
		return "", "", StatusError(http.StatusBadRequest, errSteerLooksLikeNotification)
	}
	return text, marotte.SteerIDFor(messageID), nil
}

func steerAccepted(steerID string) map[string]any {
	return responseWith(map[string]any{"steer_id": steerID})
}

// cmdSteerClear is Discard all. It does not cancel the turn, and a row the agent
// read before the clear landed keeps its read entry rather than a deleted one.
func cmdSteerClear(ctx context.Context, roles *promptRoles, cmd *marotte.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	unlock, err := roles.queue.LockSteerOps(ctx, cmd.ChatID)
	if err != nil {
		return nil, StatusError(http.StatusServiceUnavailable, err)
	}
	defer unlock()
	cleared, err := ClearSteersIn(ctx, roles.steerTarget(cmd.ChatID), func(opID string) {
		endSteerOp(ctx, roles, cmd.ChatID, opID)
	})
	if err != nil {
		return nil, err
	}
	slog.Info("steers discarded", "chat", cmd.ChatID, "cleared", len(cleared))
	return responseWith(map[string]any{"cleared": cleared}), nil
}

// ClearSteersIn discards every row of t's buffer. endOp ends the op under the steer lock the
// caller holds; it runs only when a clear was issued.
func ClearSteersIn(ctx context.Context, t SteerTarget, endOp func(opID string)) ([]string, error) {
	opID := ids.NewMessageID()
	needsClear, refuse := t.Queue.BeginDiscard(t.Key, opID)
	if refuse != "" {
		return nil, steerRefusal(refuse)
	}
	cleared := []string{}
	if needsClear {
		rpcCtx, cancel := context.WithTimeout(durable.Context(ctx), steerRemoveBudget)
		defer cancel()
		defer endOp(opID)
		names, landed := clearSteerBuffer(rpcCtx, t)
		if res := t.Queue.DiscardCleared(t.Key, opID, landed); res.Reason != "" {
			return nil, steerRefusal(res.Reason)
		}
		cleared = names
	}
	return cleared, nil
}

// clearSteerBuffer issues one _session/steer/clear and waits until every frame KAS wrote before its
// reply has folded, so the reply's ids and the frames agree. landed false means the outcome is
// unknown: no reply, or a read loop that never reached it.
func clearSteerBuffer(ctx context.Context, t SteerTarget) (cleared []string, landed bool) {
	if t.Caller == nil {
		return nil, true
	}
	resp, seq, err := t.Caller.CallAt(ctx, marotte.MethodSessionSteerClear, sessionParams(t.Caller))
	if err != nil {
		slog.Warn("steer clear: bridge call failed", "chat", t.Key, keyError, err)
		return nil, false
	}
	if !t.Queue.AwaitReadLoop(ctx, t.Key, seq) {
		slog.Warn("steer clear: the frames before its reply never folded", "chat", t.Key)
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
