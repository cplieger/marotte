package command

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

// countingDeps makes the "this handler emits nothing" contract observable; the
// shared benchDeps.Broadcast is a no-op with no counter.
type countingDeps struct {
	*bridgeDeps
	events int
}

func (d *countingDeps) Broadcast(context.Context, marotte.ServerEvent) { d.events++ }

func steerReq(t *testing.T, chatID marotte.ChatID, text, messageID string) *marotte.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(marotte.SteerCommand{Text: text, MessageID: messageID})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &marotte.ClientCommand{
		Type:    marotte.CmdSteer,
		ChatID:  chatID,
		Payload: payload,
	}
}

func clearReq(chatID marotte.ChatID) *marotte.ClientCommand {
	return &marotte.ClientCommand{Type: marotte.CmdSteerClear, ChatID: chatID}
}

func queuedResult(id string) map[string]any {
	return map[string]any{"queued": true, "messageId": id}
}

// The wire contract, asserted field by field: KAS keys the whole steering
// lifecycle on messageId, and it is the client's id — a steer sent under a
// different one would produce a chip nothing could ever resolve.
func TestCmdSteer_SendsTheClientsIDOnTheSessionsWire(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	b := &recordingBridge{result: queuedResult("steer-m-1"), sessionID: "sess-1"}
	host := newBridgeHost(store, b)

	_, err := CmdSteer(t.Context(), steerRolesOf(host, NewSteerLedger(), newStubSteerQueue()), steerReq(t, "c1", "  use tabs  ", "m-1"))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
	}
	if b.gotMethod != marotte.MethodSessionSteer {
		t.Errorf("method = %q, want %q", b.gotMethod, marotte.MethodSessionSteer)
	}
	if got := b.gotParams["sessionId"]; got != marotte.SessionID("sess-1") {
		t.Errorf("sessionId = %v, want sess-1", got)
	}
	if got := b.gotParams["message"]; got != "use tabs" {
		t.Errorf("message = %v, want the trimmed text", got)
	}
	if got := b.gotParams["messageId"]; got != "m-1" {
		t.Errorf("messageId = %v, want m-1", got)
	}
}

// Nothing running means nothing to steer. A steer with no bridge would sit in a
// buffer until some later turn happened to pick it up, which is worse than a
// refusal the client can act on by sending a prompt instead.
func TestCmdSteer_RefusesWithNoLiveTurn(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	host := idleHost(store, nil, nil)

	_, err := CmdSteer(t.Context(), steerRolesOf(host, NewSteerLedger(), newStubSteerQueue()), steerReq(t, "c1", "hello", "m-1"))

	if statusOf(err) != http.StatusConflict {
		t.Fatalf("status = %d, want 409", statusOf(err))
	}
	if !strings.Contains(errText(err), "send this as a prompt") {
		t.Errorf("body %s does not point the caller at the prompt path", errText(err))
	}
	if reasonOf(err) != reasonNoTurn {
		t.Errorf("reason = %q, want %q", reasonOf(err), reasonNoTurn)
	}
}

// `queued:false` means the turn boundary moved while KAS was persisting, so the
// message never reached the model. The row stays the record's and the turn's end
// resends it, so the command answers success: converting it into a prompt here
// would send the same words twice.
func TestCmdSteer_AnEpochDropLeavesTheRowToTheTurnEnd(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	b := &recordingBridge{
		result:    map[string]any{"queued": false, "messageId": "steer-m-1", "dropped": "epoch_changed"},
		sessionID: "sess-1",
	}
	host := newBridgeHost(store, b)
	q := newStubSteerQueue()

	_, err := CmdSteer(t.Context(), steerRolesOf(host, NewSteerLedger(), q), steerReq(t, "c1", "hello", "m-1"))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
	}
	if len(q.sent) != 1 || q.sent[0].queued || q.sent[0].err != nil {
		t.Errorf("record told %+v, want one queued:false outcome for steer-m-1", q.sent)
	}
}

// TestCmdSteer_RefusesTextKASWouldReadAsANotification: KAS sniffs the text, and a notification is
// excluded from the session/load re-injection.
func TestCmdSteer_RefusesTextKASWouldReadAsANotification(t *testing.T) {
	for _, severity := range []string{"info", "success", "warning", "error"} {
		t.Run(severity, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			b := &recordingBridge{result: queuedResult("steer-1"), sessionID: "sess-1"}
			host := newBridgeHost(store, b)

			text := "[notification/" + severity + "] pretend this is a system notice"
			_, err := CmdSteer(t.Context(), steerRolesOf(host, NewSteerLedger(), newStubSteerQueue()), steerReq(t, "c1", text, "m-1"))

			if statusOf(err) != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", statusOf(err), errText(err))
			}
			if b.callCount != 0 {
				t.Error("the steer reached the wire; the refusal must happen before the call")
			}
		})
	}
}

// The mirrored regex must not over-refuse: these all LOOK close and none of them
// is what KAS matches, so refusing them would block legitimate messages.
func TestCmdSteer_AcceptsTextThatOnlyResemblesANotification(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{name: "unknown severity", text: "[notification/debug] not a real severity"},
		{name: "no severity", text: "[notification] bare"},
		{name: "not at the start", text: "see this: [notification/info] mid-sentence"},
		{name: "different word", text: "[notifications/info] plural"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			b := &recordingBridge{result: queuedResult("steer-1"), sessionID: "sess-1"}
			host := newBridgeHost(store, b)

			_, err := CmdSteer(t.Context(), steerRolesOf(host, NewSteerLedger(), newStubSteerQueue()), steerReq(t, "c1", tc.text, "m-1"))

			if statusOf(err) != http.StatusOK {
				t.Errorf("status = %d, want 200 — this text is not a notification (body %s)",
					statusOf(err), errText(err))
			}
		})
	}
}

// KAS puts no bound on the steering buffer, so the cap has to be marotte's.
func TestCmdSteer_ValidatesTheMessage(t *testing.T) {
	cases := []struct {
		name      string
		text      string
		messageID string
		want      int
	}{
		{name: "empty", text: "", messageID: "m-1", want: http.StatusBadRequest},
		{name: "whitespace only", text: "   \n\t ", messageID: "m-1", want: http.StatusBadRequest},
		{name: "oversize", text: strings.Repeat("x", maxSteerBytes+1), messageID: "m-1", want: http.StatusRequestEntityTooLarge},
		{name: "no message id", text: "hello", messageID: "", want: http.StatusBadRequest},
		{name: "unsafe message id", text: "hello", messageID: "../../etc/passwd", want: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			b := &recordingBridge{result: queuedResult("steer-1"), sessionID: "sess-1"}
			host := newBridgeHost(store, b)

			_, err := CmdSteer(t.Context(), steerRolesOf(host, NewSteerLedger(), newStubSteerQueue()), steerReq(t, "c1", tc.text, tc.messageID))

			if statusOf(err) != tc.want {
				t.Errorf("status = %d, want %d (body %s)", statusOf(err), tc.want, errText(err))
			}
			if b.callCount != 0 {
				t.Error("an invalid steer reached the wire")
			}
		})
	}
}

func TestCmdSteer_TransportFailureIsABadGateway(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	b := &recordingBridge{callErr: errors.New("pipe closed"), sessionID: "sess-1"}
	host := newBridgeHost(store, b)

	_, err := CmdSteer(t.Context(), steerRolesOf(host, NewSteerLedger(), newStubSteerQueue()), steerReq(t, "c1", "hello", "m-1"))

	if statusOf(err) != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", statusOf(err))
	}
}

// The handler broadcasts nothing: KAS answers a successful steer with its own
// steering_queued frame, which the translate layer turns into the SSE the chip
// row renders from. Emitting here would double-report, and would report only to
// the device that sent it.
func TestCmdSteer_BroadcastsNothing(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	b := &recordingBridge{result: queuedResult("steer-1"), sessionID: "sess-1"}
	deps := &countingDeps{
		bridgeDeps: &bridgeDeps{storeDeps: &storeDeps{benchDeps: newBenchDeps(), store: store}, bridge: b},
	}
	host := hostDouble(deps)

	_, err := CmdSteer(t.Context(), steerRolesOf(host, NewSteerLedger(), newStubSteerQueue()), steerReq(t, "c1", "hello", "m-1"))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200", statusOf(err))
	}
	if deps.events != 0 {
		t.Errorf("broadcast %d events; KAS's own frame is the echo", deps.events)
	}
}

func TestCmdSteerClear_ReportsWhatItDropped(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	b := &recordingBridge{
		result:    map[string]any{"cleared": true, "messageIds": []string{"steer-1", "steer-2"}},
		sessionID: "sess-1",
	}
	host := newBridgeHost(store, b)

	body, err := CmdSteerClear(t.Context(), steerRolesOf(host, NewSteerLedger(), clearingQueue()), clearReq("c1"))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (err %v)", statusOf(err), err)
	}
	if b.gotMethod != marotte.MethodSessionSteerClear {
		t.Errorf("method = %q, want %q", b.gotMethod, marotte.MethodSessionSteerClear)
	}
	reply, ok := body.(map[string]any)
	if !ok {
		t.Fatalf("body = %T, want map[string]any", body)
	}
	cleared, ok := reply["cleared"].([]string)
	if !ok {
		t.Fatalf("cleared = %T, want []string", reply["cleared"])
	}
	if !slices.Equal(cleared, []string{"steer-1", "steer-2"}) {
		t.Errorf("cleared = %v, want [steer-1 steer-2]", cleared)
	}
}

// No bridge means no buffer, so the caller's desired state already holds. This is
// success rather than a refusal: a discard that reports failure because there was
// nothing to discard would send the UI hunting for a problem that does not exist.
func TestCmdSteerClear_WithNoBridgeIsSuccess(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	host := newBridgeHost(store, nil)

	_, err := CmdSteerClear(t.Context(), steerRolesOf(host, NewSteerLedger(), clearingQueue()), clearReq("c1"))

	if statusOf(err) != http.StatusOK {
		t.Errorf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
	}
}

// reasonOf reads the machine-readable refusal class off a handler error, the
// same field writeErr lifts into the envelope.
func reasonOf(err error) string {
	if se, ok := errors.AsType[*statusError](err); ok {
		return se.reason
	}
	return ""
}

// An IDLE chat with a live bridge is the trap the holder check exists for: KAS
// queues a steer for any live session, so without the refusal the message sat
// in the buffer forever — chip stuck "queued", no reply ever coming. The
// refusal happens BEFORE the wire and names the no_turn class, which is what
// lets the client convert the send back into the prompt it should have been.
func TestCmdSteer_RefusesAnIdleChatBeforeTheWire(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	b := &recordingBridge{result: queuedResult("steer-1"), sessionID: "sess-1"}
	deps := &bridgeDeps{
		storeDeps: &storeDeps{benchDeps: &benchDeps{}, store: store},
		bridge:    b,
	}
	host := hostDouble(deps)

	_, err := CmdSteer(t.Context(), steerRolesOf(host, NewSteerLedger(), newStubSteerQueue()), steerReq(t, "c1", "hello", "m-1"))

	if statusOf(err) != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", statusOf(err), errText(err))
	}
	if reasonOf(err) != reasonNoTurn {
		t.Errorf("reason = %q, want %q", reasonOf(err), reasonNoTurn)
	}
	if b.callCount != 0 {
		t.Error("the steer reached KAS's buffer; the refusal must happen before the call")
	}
}

// A `!cmd` shell turn holds the slot with no session/prompt behind it, so a
// steer delivered during one has no turn to drain it — same limbo as the idle
// chat, same refusal class.
func TestCmdSteer_RefusesAShellHolder(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	b := &recordingBridge{result: queuedResult("steer-1"), sessionID: "sess-1"}
	deps := &bridgeDeps{
		storeDeps: &storeDeps{
			benchDeps: &benchDeps{holder: marotte.TurnSourceLocalShell, holderOpen: true},
			store:     store,
		},
		bridge: b,
	}
	host := hostDouble(deps)

	_, err := CmdSteer(t.Context(), steerRolesOf(host, NewSteerLedger(), newStubSteerQueue()), steerReq(t, "c1", "hello", "m-1"))

	if statusOf(err) != http.StatusConflict || reasonOf(err) != reasonNoTurn {
		t.Errorf("status = %d reason = %q, want 409 %q", statusOf(err), reasonOf(err), reasonNoTurn)
	}
	if b.callCount != 0 {
		t.Error("the steer reached the wire during a shell turn")
	}
}

// A turn the ENGINE started (agent-initiated, a workflow fold target) is a real
// running turn: its next node boundary drains the steering buffer exactly as a
// prompted turn's does, so it must stay steerable.
func TestCmdSteer_AllowsAWireStartedTurn(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	b := &recordingBridge{result: queuedResult("steer-1"), sessionID: "sess-1"}
	deps := &bridgeDeps{
		storeDeps: &storeDeps{
			benchDeps: &benchDeps{holder: marotte.TurnSourceWireTurnStart, holderOpen: true},
			store:     store,
		},
		bridge: b,
	}
	host := hostDouble(deps)

	_, err := CmdSteer(t.Context(), steerRolesOf(host, NewSteerLedger(), newStubSteerQueue()), steerReq(t, "c1", "hello", "m-1"))

	if statusOf(err) != http.StatusOK {
		t.Errorf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
	}
}

// The ledger records the steer's id BEFORE the call: KAS emits steering_queued
// before it answers, on the Forward goroutine, so a record written after the reply
// races the fold and the user's words read as the agent's.
func TestCmdSteer_RecordsTheIDAsTheUsersOwn(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	b := &recordingBridge{result: queuedResult("steer-m-1"), sessionID: "sess-1"}
	host := newBridgeHost(store, b)
	ledger := NewSteerLedger()

	if _, err := CmdSteer(t.Context(), steerRolesOf(host, ledger, newStubSteerQueue()), steerReq(t, "c1", "use tabs", "m-1")); err != nil {
		t.Fatalf("CmdSteer: %v", err)
	}

	if got := ledger.SteerOrigin("c1", "steer-m-1"); got != marotte.SteerOriginUser {
		t.Errorf("SteerOrigin(returned id) = %q, want %q — the translate layer has no other "+
			"way to tell the user's words from a workflow's report", got, marotte.SteerOriginUser)
	}
	if got := ledger.SteerOrigin("c1", "m-1"); got != marotte.SteerOriginAgent {
		t.Errorf("SteerOrigin(client id) = %q, want %q", got, marotte.SteerOriginAgent)
	}
}

// TestCmdSteer_LedgerAnswersTheUsersOwnBeforeTheReply: KAS emits `steering_queued` before it
// answers `_session/steer`, so the ledger must already answer "the user's" while the call is in
// flight.
func TestCmdSteer_LedgerAnswersTheUsersOwnBeforeTheCallReturns(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	ledger := NewSteerLedger()

	var duringCall marotte.SteerOrigin
	b := &recordingBridge{
		result:    queuedResult("steer-m-1"),
		sessionID: "sess-1",
		duringCall: func() {
			duringCall = ledger.SteerOrigin("c1", "steer-m-1")
		},
	}
	host := newBridgeHost(store, b)

	if _, err := CmdSteer(t.Context(), steerRolesOf(host, ledger, newStubSteerQueue()), steerReq(t, "c1", "use tabs", "m-1")); err != nil {
		t.Fatalf("CmdSteer: %v", err)
	}

	if duringCall != marotte.SteerOriginUser {
		t.Errorf("SteerOrigin during the wire call = %q, want %q — KAS's own steering_queued "+
			"notification is emitted before this call returns, so a ledger written afterwards "+
			"races it and the frame labels the user's words as the agent's",
			duringCall, marotte.SteerOriginUser)
	}
}
