package command

// Mid-turn steering, via KAS's own buffer. `_session/steer` appends to a
// per-session steering buffer the graph consumes at the next node boundary as an
// ordinary human turn; nothing is cancelled and nothing is discarded. So marotte
// keeps no queue: idle sends are prompts, mid-turn sends are steers.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

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
	// errSteerDropped maps KAS's `{queued: false}`.
	errSteerDropped = errors.New("the turn ended before this could be delivered — send it as a prompt instead")
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

// reasonFull is the 409 refusal class for a steer the chat's parked set cannot
// hold. Not `no_turn`, deliberately: the client converts that one into a prompt,
// and a prompt would meet the same running turn.
const reasonFull = "full"

// errSteerFull is the refusal for a parked steer that would be the 65th.
var errSteerFull = errors.New("too many messages are waiting for this turn to start — wait for it, then send again")

// steerReply is KAS's answer to one _session/steer.
type steerReply struct {
	MessageID string `json:"messageId"`
	Dropped   string `json:"dropped"`
	Queued    bool   `json:"queued"`
}

// sendSteer issues one _session/steer for the steer steerID names. KAS prefixes
// the messageId it is sent into the steer's own id, so the wire carries the id
// with marotte.SteerIDPrefix removed and the reply carries steerID back.
func sendSteer(ctx context.Context, sb sessionCaller, steerID, text string) (steerReply, error) {
	var reply steerReply
	resp, err := sb.Call(ctx, marotte.MethodSessionSteer, SessionParams(sb, map[string]any{
		"message":   text,
		"messageId": strings.TrimPrefix(steerID, marotte.SteerIDPrefix),
	}))
	if err != nil {
		return reply, err
	}
	if resp != nil && resp.Result != nil {
		_ = json.Unmarshal(resp.Result, &reply)
	}
	return reply, nil
}

// CmdSteer delivers a message into the running turn. It requires a holder whose
// turn drains the steering buffer: KAS queues a steer for any live session, so one
// sent to an idle chat or a `!cmd` shell turn would sit unread with the chip stuck
// "queued". A prompt whose bridge is still spawning, or whose parked steers are
// still being delivered, PARKS the steer for the prompt goroutine instead.
func CmdSteer(
	ctx context.Context,
	bridges BridgeAccess,
	admission TurnAdmission,
	steers SteerRecorder,
	cmd *marotte.ClientCommand,
) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	text, steerID, resends, err := steerText(cmd)
	if err != nil {
		return nil, err
	}

	source, held := admission.AdmissionHolderSource(cmd.ChatID)
	if !held || source == marotte.TurnSourceLocalShell {
		return nil, StatusErrorReason(http.StatusConflict, reasonNoTurn, errSteerNoTurn)
	}

	if source.PromptClass() && (!bridges.BridgeLive(cmd.ChatID) || steers.HasParkedSteers(cmd.ChatID)) {
		// Both park conditions and the drain's exit test are one locked read on the
		// ledger, so a steer arriving mid-drain joins the tail rather than reaching
		// KAS ahead of a row typed before it.
		return parkSteer(steers, cmd.ChatID, ParkedSteer{ID: steerID, Text: text, Resends: resends})
	}
	bridge := bridges.Bridge(cmd.ChatID)
	if bridge == nil {
		return nil, StatusErrorReason(http.StatusConflict, reasonNoTurn, errSteerNoTurn)
	}

	// RECORDED BEFORE THE CALL: KAS emits `steering_queued` before it answers this
	// RPC and the fold runs on the bridge's Forward goroutine with nothing
	// serializing the two, so a ledger written after the response races the fold
	// and SteerOrigin answers `agent` for the user's own words (measured: 9 of 18).
	// A refused steer leaves a stale entry, deliberately unswept: nothing else can
	// carry a `steer-` id, so it mislabels nothing, and the TTL reclaims it.
	steers.RecordUserSteer(cmd.ChatID, steerID, resends)

	result, err := sendSteer(ctx, bridge, steerID, text)
	if err != nil {
		// KAS throws (rather than answering) for an unknown session and
		// an empty message, both already ruled out — so an error here is
		// a transport or session-liveness failure.
		steers.ForgetUserSteer(cmd.ChatID, steerID)
		slog.Warn("steer: bridge call failed", "chat", cmd.ChatID, keyError, err)
		return nil, StatusError(http.StatusBadGateway, err)
	}
	if !result.Queued {
		// `dropped: "epoch_changed"` means the turn boundary moved while
		// KAS was persisting: the message never reached the model. 409
		// rather than 502 — the client's answer is to send it as an
		// ordinary prompt.
		steers.ForgetUserSteer(cmd.ChatID, steerID)
		slog.Info("steer dropped", "chat", cmd.ChatID, "reason", result.Dropped)
		return nil, StatusErrorReason(http.StatusConflict, reasonNoTurn, errSteerDropped)
	}

	// The id KAS RETURNED. Normally identical to the pre-call record above, so this
	// is an idempotent second write; it is kept so a KAS that ever returns an id we
	// did not derive still gets that steer labelled as the user's.
	steers.RecordUserSteer(cmd.ChatID, result.MessageID, resends)
	if result.MessageID != steerID {
		// The derivation above is what lets the ledger be written before the call,
		// so a disagreement retires that reasoning rather than merely logging an
		// oddity. An empty id is the sharper case: RecordUserSteer no-ops on it, so
		// only the pre-call entry stands and a later frame carrying some other id
		// resolves as the agent's.
		slog.Warn("steer id is not the derived one; the pre-call ledger entry may not match later frames",
			"chat", cmd.ChatID,
			"returned", result.MessageID,
			"derived", steerID)
	}

	// No event is broadcast here: KAS answers a successful steer with its
	// own `steering_queued` frame, which the translate layer turns into
	// the SSE the chip row renders from.
	slog.Info("steer queued", "chat", cmd.ChatID, "steer_id", result.MessageID)
	return responseWith(map[string]any{"steer_id": result.MessageID}), nil
}

// steerText validates the steer payload and answers its trimmed text, the id KAS
// will stamp on it and the dropped steers it re-sends. The id is derivable
// (marotte.SteerIDFor) because KAS prefixes the messageId we send and stamps that
// on both the reply and the notification; the two shapes that would make it wrong
// are refused here: an empty id, and a notification-prefixed text KAS would file
// under `notify-`.
func steerText(cmd *marotte.ClientCommand) (text, steerID string, resends []string, err error) {
	var p marotte.SteerCommand
	if json.Unmarshal(cmd.Payload, &p) != nil {
		return "", "", nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	text = strings.TrimSpace(p.Text)
	switch {
	case text == "":
		return "", "", nil, StatusError(http.StatusBadRequest, errEmptyPrompt)
	case len(text) > maxSteerBytes:
		return "", "", nil, StatusError(http.StatusRequestEntityTooLarge, errPromptTooLong)
	case !ValidMessageID(p.MessageID):
		return "", "", nil, StatusError(http.StatusBadRequest, errMissingMessageID)
	case notificationPrefix.MatchString(text):
		return "", "", nil, StatusError(http.StatusBadRequest, errSteerLooksLikeNotification)
	}
	return text, marotte.SteerIDFor(p.MessageID), p.Resends, nil
}

// parkSteer holds the steer for the prompt goroutine to issue once the turn is
// live, in arrival order behind the rows already parked.
func parkSteer(steers SteerRecorder, chatID marotte.ChatID, steer ParkedSteer) (any, error) {
	steers.RecordUserSteer(chatID, steer.ID, steer.Resends)
	if !steers.ParkSteer(chatID, steer.ID, steer.Text, steer.Resends) {
		steers.ForgetUserSteer(chatID, steer.ID)
		return nil, StatusErrorReason(http.StatusConflict, reasonFull, errSteerFull)
	}
	slog.Info("steer parked", "chat", chatID, "steer_id", steer.ID)
	return responseWith(map[string]any{"steer_id": steer.ID}), nil
}

// CmdSteerClear drops every steer still queued for the chat's session. Does
// not cancel the turn — only the unread steers go away.
func CmdSteerClear(ctx context.Context, bridges BridgeAccess, cmd *marotte.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	bridge := bridges.Bridge(cmd.ChatID)
	if bridge == nil {
		// Nothing to clear without a session, so this is success rather
		// than a refusal: the caller's desired state holds.
		return responseWith(map[string]any{"cleared": []string{}}), nil
	}

	resp, err := bridge.Call(ctx, marotte.MethodSessionSteerClear, SessionParams(bridge))
	if err != nil {
		slog.Warn("steer_clear: bridge call failed", "chat", cmd.ChatID, keyError, err)
		return nil, StatusError(http.StatusBadGateway, err)
	}
	var result struct {
		MessageIDs []string `json:"messageIds"`
	}
	if resp != nil && resp.Result != nil {
		_ = json.Unmarshal(resp.Result, &result)
	}

	// As with a steer, the visible effect arrives as KAS's own
	// `steering_cleared` frame; the ids come back only so the caller knows
	// what it dropped.
	slog.Info("steers cleared", "chat", cmd.ChatID, "count", len(result.MessageIDs))
	return responseWith(map[string]any{"cleared": result.MessageIDs}), nil
}
