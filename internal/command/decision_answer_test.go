package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cplieger/go/slogx/capture"
	"github.com/cplieger/marotte/internal/marotte"
)

// Respond is the only method these handlers use; the rest satisfies the interface.
type countingBridge struct {
	recordingBridge
	responds []int64
}

func (b *countingBridge) Respond(_ context.Context, id int64, _ any, _ error) error {
	b.responds = append(b.responds, id)
	return nil
}

type takeDeps struct {
	*benchDeps
	bridge Bridge
	takes  []int64
	// takeChats is the chat each claim named, so a handler that drops it is
	// visible: an id-only claim can retire another chat's card.
	takeChats []marotte.ChatID
	// takeKinds is the decision kind each claim named.
	takeKinds []marotte.DecisionKind
	// elicitation is the pending elicitation ask PendingElicitation reads, nil for none.
	elicitation *marotte.ElicitationNeededPayload
	takeOK      bool
}

func (d *takeDeps) PendingElicitation(marotte.ChatID, int64) (marotte.ElicitationNeededPayload, bool) {
	if d.elicitation == nil {
		return marotte.ElicitationNeededPayload{}, false
	}
	return *d.elicitation, true
}

// bridgeReply answers a claimed ask on the bridge the claim handed back, as the tracker's reply
// answers on the ask's origin.
type bridgeReply struct {
	b  Bridge
	id int64
}

func (r bridgeReply) Respond(ctx context.Context, result any) (AnswerOutcome, error) {
	if err := r.b.Respond(ctx, r.id, result, nil); err != nil {
		return AnswerReopened, err
	}
	return AnswerDelivered, nil
}

func (d *takeDeps) TakePendingPerm(chatID marotte.ChatID, requestID int64, kind marotte.DecisionKind, _ marotte.SettledBy) (AskReply, bool) {
	d.takes = append(d.takes, requestID)
	d.takeKinds = append(d.takeKinds, kind)
	d.takeChats = append(d.takeChats, chatID)
	if !d.takeOK {
		return nil, false
	}
	return bridgeReply{b: d.bridge, id: requestID}, true
}

func (d *takeDeps) TakePendingPermissionOption(chatID marotte.ChatID, requestID int64, _ string, _ marotte.SettledBy) (AskReply, bool, bool) {
	d.takes = append(d.takes, requestID)
	d.takeChats = append(d.takeChats, chatID)
	if !d.takeOK {
		return nil, false, false
	}
	return bridgeReply{b: d.bridge, id: requestID}, true, true
}

func decisionCommand(t *testing.T, typ marotte.CommandType, payload any) *marotte.ClientCommand {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return &marotte.ClientCommand{Type: typ, ChatID: "c1", Payload: raw}
}

const decisionRequestID = int64(7)

func decisionCases(t *testing.T) []struct {
	name string
	run  func(hostDouble) (any, error)
} {
	t.Helper()
	perm := decisionCommand(t, marotte.CmdPermissionResponse,
		marotte.PermissionResponseCommand{RequestID: decisionRequestID, OptionID: "allow_once"})
	elicit := decisionCommand(t, marotte.CmdElicitationResponse,
		marotte.ElicitationResponseCommand{RequestID: decisionRequestID, Action: marotte.ElicitationActionDecline})
	input := decisionCommand(t, marotte.CmdUserInputResponse,
		marotte.UserInputResponseCommand{RequestID: decisionRequestID, Action: marotte.UserInputActionDismissed})
	return []struct {
		name string
		run  func(hostDouble) (any, error)
	}{
		{name: "permission", run: func(host hostDouble) (any, error) {
			return cmdPermission(t.Context(), host, host, perm)
		}},
		{name: "elicitation", run: func(host hostDouble) (any, error) {
			return cmdElicitationResponse(t.Context(), host, elicit)
		}},
		{name: "user_input", run: func(host hostDouble) (any, error) {
			return cmdUserInputResponse(t.Context(), host, input)
		}},
	}
}

// TestDecisionHandlers_LostClaimIsRefusedAndNotAnswered: another surface
// already answered, so the handler must send nothing to kiro-cli and say so
// with a 409 the client can act on.
func TestDecisionHandlers_LostClaimIsRefusedAndNotAnswered(t *testing.T) {
	for _, tc := range decisionCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			bridge := &countingBridge{}
			deps := &takeDeps{benchDeps: newBenchDeps(), bridge: bridge, takeOK: false}

			_, err := tc.run(deps)

			if got := statusOf(err); got != http.StatusConflict {
				t.Errorf("status = %d, want %d", got, http.StatusConflict)
			}
			if err == nil || !strings.Contains(err.Error(), "already_answered") {
				t.Errorf("error = %v, want the already_answered code", err)
			}
			if len(bridge.responds) != 0 {
				t.Errorf("answered kiro-cli %d times after losing the claim, want 0", len(bridge.responds))
			}
			if len(deps.takes) != 1 || deps.takes[0] != decisionRequestID {
				t.Errorf("claims = %v, want exactly [%d]", deps.takes, decisionRequestID)
			}
		})
	}
}

// TestDecisionHandlers_WonClaimAnswersOnce is the other half: the winner does
// reach the wire, on the request id it claimed.
func TestDecisionHandlers_WonClaimAnswersOnce(t *testing.T) {
	for _, tc := range decisionCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			bridge := &countingBridge{}
			deps := &takeDeps{benchDeps: newBenchDeps(), bridge: bridge, takeOK: true}

			_, err := tc.run(deps)

			if got := statusOf(err); got != http.StatusOK {
				t.Errorf("status = %d, want %d (err %v)", got, http.StatusOK, err)
			}
			if len(bridge.responds) != 1 || bridge.responds[0] != decisionRequestID {
				t.Errorf("answers = %v, want exactly [%d]", bridge.responds, decisionRequestID)
			}
			// The claim names the command's own chat, so an answer posted under another chat
			// cannot settle this one's ask.
			if !slices.Equal(deps.takeChats, []marotte.ChatID{"c1"}) {
				t.Errorf("claimed chats = %v, want [c1]", deps.takeChats)
			}
		})
	}
}

// A claim names the kind its answer is shaped for, so the tracker can refuse an answer naming
// another kind's ask.
func TestDecisionHandlers_ClaimNamesTheAnswersKind(t *testing.T) {
	want := map[string]marotte.DecisionKind{"elicitation": marotte.DecisionKindElicitation, "user_input": marotte.DecisionKindUserInput}
	for _, tc := range decisionCases(t) {
		kind, ok := want[tc.name]
		if !ok {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			deps := &takeDeps{benchDeps: newBenchDeps(), bridge: &countingBridge{}, takeOK: true}
			if _, err := tc.run(deps); err != nil {
				t.Fatalf("run: %v", err)
			}
			if !slices.Equal(deps.takeKinds, []marotte.DecisionKind{kind}) {
				t.Errorf("claimed kinds = %v, want [%s]", deps.takeKinds, kind)
			}
		})
	}
}

// liveCtxBridge refuses an answer on a done context, as Bridge.Respond does.
type liveCtxBridge struct {
	countingBridge
}

func (b *liveCtxBridge) Respond(ctx context.Context, id int64, result any, err error) error {
	if cErr := ctx.Err(); cErr != nil {
		return cErr
	}
	return b.countingBridge.Respond(ctx, id, result, err)
}

// A claimed ask whose answer never reaches kiro-cli wedges the turn while every surface shows it settled.
func TestDecisionHandlers_AClaimedAnswerOutlivesTheRequest(t *testing.T) {
	gone, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		run  func(pendingPermAccess, profileSwitcher) (any, error)
		name string
	}{
		{name: "permission", run: func(p pendingPermAccess, s profileSwitcher) (any, error) {
			return cmdPermission(gone, p, s, decisionCommand(t, marotte.CmdPermissionResponse,
				marotte.PermissionResponseCommand{RequestID: decisionRequestID, OptionID: "allow_once"}))
		}},
		{name: "elicitation", run: func(p pendingPermAccess, _ profileSwitcher) (any, error) {
			return cmdElicitationResponse(gone, p, decisionCommand(t, marotte.CmdElicitationResponse,
				marotte.ElicitationResponseCommand{RequestID: decisionRequestID, Action: marotte.ElicitationActionDecline}))
		}},
		{name: "user_input", run: func(p pendingPermAccess, _ profileSwitcher) (any, error) {
			return cmdUserInputResponse(gone, p, decisionCommand(t, marotte.CmdUserInputResponse,
				marotte.UserInputResponseCommand{RequestID: decisionRequestID, Action: marotte.UserInputActionDismissed}))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bridge := &liveCtxBridge{}
			deps := &takeDeps{benchDeps: newBenchDeps(), bridge: bridge, takeOK: true}

			if _, err := tc.run(deps, deps); err != nil {
				t.Fatalf("%s on a request whose client left = %v", tc.name, err)
			}

			if len(bridge.responds) != 1 {
				t.Errorf("%s answered kiro-cli %d times after its claim, want 1", tc.name, len(bridge.responds))
			}
		})
	}
}

// resultBridge captures the answer VALUE, so a test can assert what a choice
// carried rather than only that an answer went out.
type resultBridge struct {
	recordingBridge
	results []any
}

func (b *resultBridge) Respond(_ context.Context, _ int64, result any, _ error) error {
	b.results = append(b.results, result)
	return nil
}

// Values travel only with the action that supplied them. A declined or
// cancelled form has no filled fields, so forwarding the ones the client sent
// anyway would hand kiro-cli values the user withdrew.
func TestCmdElicitationResponse_ContentTravelsOnlyOnAccept(t *testing.T) {
	filled := map[string]any{"branch": "main"}
	cases := []struct {
		wantContent map[string]any
		name        string
		action      string
	}{
		{name: "accept forwards the filled form", action: marotte.ElicitationActionAccept, wantContent: filled},
		{name: "decline forwards no content", action: marotte.ElicitationActionDecline},
		{name: "cancel forwards no content", action: marotte.ElicitationActionCancel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bridge := &resultBridge{}
			deps := &takeDeps{
				benchDeps: newBenchDeps(), bridge: bridge, takeOK: true,
				elicitation: formAsk(map[string]marotte.ElicitationPropertySchema{"branch": {Type: "string"}}),
			}
			cmd := decisionCommand(t, marotte.CmdElicitationResponse, marotte.ElicitationResponseCommand{
				RequestID: decisionRequestID,
				Action:    tc.action,
				Content:   filled,
			})

			if _, err := cmdElicitationResponse(t.Context(), deps, cmd); err != nil {
				t.Fatalf("CmdElicitationResponse(%s) = %v", tc.action, err)
			}

			if len(bridge.results) != 1 {
				t.Fatalf("got %d answers, want 1", len(bridge.results))
			}
			result, ok := bridge.results[0].(marotte.ElicitationResult)
			if !ok {
				t.Fatalf("answer = %T, want marotte.ElicitationResult", bridge.results[0])
			}
			if result.Action != tc.action {
				t.Errorf("action = %q, want %q", result.Action, tc.action)
			}
			if !reflect.DeepEqual(result.Content, tc.wantContent) {
				t.Errorf("content for %q = %v, want %v", tc.action, result.Content, tc.wantContent)
			}
		})
	}
}

// kiro-cli answers nothing to a stdin line holding a lone surrogate escape or an
// invalid UTF-8 byte, so an answer relayed byte for byte would leave the turn
// waiting on an ask marotte already settled.
func TestCmdElicitationResponse_ContentReachesTheEngineAsValidUTF8(t *testing.T) {
	cases := map[string][]byte{
		"lone surrogate escape": []byte(`{"request_id":7,"action":"accept","content":{"name":"a\ud800b"}}`),
		"invalid byte":          []byte("{\"request_id\":7,\"action\":\"accept\",\"content\":{\"name\":\"a\xffb\"}}"),
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			bridge := &resultBridge{}
			deps := &takeDeps{
				benchDeps: newBenchDeps(), bridge: bridge, takeOK: true,
				elicitation: formAsk(map[string]marotte.ElicitationPropertySchema{"name": {Type: "string"}}),
			}
			cmd := &marotte.ClientCommand{Type: marotte.CmdElicitationResponse, ChatID: "c1", Payload: payload}

			if _, err := cmdElicitationResponse(t.Context(), deps, cmd); err != nil {
				t.Fatalf("cmdElicitationResponse = %v", err)
			}
			if len(bridge.results) != 1 {
				t.Fatalf("got %d answers, want 1", len(bridge.results))
			}
			frame, err := json.Marshal(bridge.results[0])
			if err != nil {
				t.Fatalf("marshal answer: %v", err)
			}
			if !utf8.Valid(frame) {
				t.Errorf("answer %q is not valid UTF-8", frame)
			}
			if lower := strings.ToLower(string(frame)); strings.Contains(lower, `\ud8`) || strings.Contains(lower, `\udc`) {
				t.Errorf("answer %s carries a surrogate escape", frame)
			}
			var back struct {
				Content map[string]string `json:"content"`
			}
			if err := json.Unmarshal(frame, &back); err != nil || back.Content["name"] != "a\uFFFDb" {
				t.Errorf("answer content = %v (%v), want name a\\uFFFDb", back.Content, err)
			}
		})
	}
}

// The MCP ElicitResult content is an object; anything else is refused at the
// door rather than relayed for the engine to reject.
func TestCmdElicitationResponse_NonObjectContentIsRefused(t *testing.T) {
	for _, content := range []string{`[1,2]`, `"x"`} {
		t.Run(content, func(t *testing.T) {
			bridge := &resultBridge{}
			deps := &takeDeps{benchDeps: newBenchDeps(), bridge: bridge, takeOK: true}
			payload := []byte(`{"request_id":7,"action":"accept","content":` + content + `}`)
			cmd := &marotte.ClientCommand{Type: marotte.CmdElicitationResponse, ChatID: "c1", Payload: payload}

			_, err := cmdElicitationResponse(t.Context(), deps, cmd)

			if got := statusOf(err); got != http.StatusBadRequest || !errors.Is(err, ErrInvalidPayload) {
				t.Errorf("status = %d, err = %v, want 400 ErrInvalidPayload", got, err)
			}
			if len(deps.takes) != 0 || len(bridge.results) != 0 {
				t.Errorf("takes = %v, answers = %d, want none", deps.takes, len(bridge.results))
			}
		})
	}
}

func formAsk(props map[string]marotte.ElicitationPropertySchema, required ...string) *marotte.ElicitationNeededPayload {
	return &marotte.ElicitationNeededPayload{
		Mode:            "form",
		RequestID:       decisionRequestID,
		RequestedSchema: &marotte.ElicitationRequestSchema{Type: "object", Properties: props, Required: required},
	}
}

// A refused form answer must not claim the ask: the card stays live for a corrected resubmit.
func TestCmdElicitationResponse_AnInvalidAnswerLeavesTheAskPending(t *testing.T) {
	bridge := &resultBridge{}
	deps := &takeDeps{
		benchDeps: newBenchDeps(), bridge: bridge, takeOK: true,
		elicitation: formAsk(map[string]marotte.ElicitationPropertySchema{"branch": {Type: "string"}}, "branch"),
	}
	cmd := &marotte.ClientCommand{
		Type: marotte.CmdElicitationResponse, ChatID: "c1",
		Payload: []byte(`{"request_id":7,"action":"accept","content":{}}`),
	}

	_, err := cmdElicitationResponse(t.Context(), deps, cmd)

	if got := statusOf(err); got != http.StatusBadRequest || !errors.Is(err, errElicitationContentInvalid) {
		t.Errorf("status = %d, err = %v, want 400 elicitation_content_invalid", got, err)
	}
	if len(deps.takes) != 0 || len(bridge.results) != 0 {
		t.Errorf("takes = %v, answers = %d, want none", deps.takes, len(bridge.results))
	}
}

// With no pending elicitation to validate against, an accept must not claim anything.
func TestCmdElicitationResponse_AnAcceptOnAnAskNotPendingIsAlreadyAnswered(t *testing.T) {
	bridge := &resultBridge{}
	deps := &takeDeps{benchDeps: newBenchDeps(), bridge: bridge, takeOK: true}
	cmd := decisionCommand(t, marotte.CmdElicitationResponse, marotte.ElicitationResponseCommand{
		RequestID: decisionRequestID, Action: marotte.ElicitationActionAccept,
	})

	_, err := cmdElicitationResponse(t.Context(), deps, cmd)

	if got := statusOf(err); got != http.StatusConflict || !errors.Is(err, errAlreadyAnswered) {
		t.Errorf("status = %d, err = %v, want 409 already_answered", got, err)
	}
	if len(deps.takes) != 0 || len(bridge.results) != 0 {
		t.Errorf("takes = %v, answers = %d, want none", deps.takes, len(bridge.results))
	}
}

// MCP's URL-mode ElicitResult carries no content, whatever the client sent.
func TestCmdElicitationResponse_URLAcceptCarriesNoContent(t *testing.T) {
	bridge := &resultBridge{}
	deps := &takeDeps{
		benchDeps: newBenchDeps(), bridge: bridge, takeOK: true,
		elicitation: &marotte.ElicitationNeededPayload{Mode: marotte.ElicitationModeURL, URL: "https://example.com/connect", RequestID: decisionRequestID},
	}
	cmd := decisionCommand(t, marotte.CmdElicitationResponse, marotte.ElicitationResponseCommand{
		RequestID: decisionRequestID, Action: marotte.ElicitationActionAccept, Content: map[string]any{"x": 1.0},
	})

	if _, err := cmdElicitationResponse(t.Context(), deps, cmd); err != nil {
		t.Fatalf("cmdElicitationResponse = %v", err)
	}
	if len(bridge.results) != 1 {
		t.Fatalf("got %d answers, want 1", len(bridge.results))
	}
	result, ok := bridge.results[0].(marotte.ElicitationResult)
	if !ok {
		t.Fatalf("answer = %T, want marotte.ElicitationResult", bridge.results[0])
	}
	if result.Action != marotte.ElicitationActionAccept || result.Content != nil {
		t.Errorf("answer = %+v, want accept with no content", result)
	}
}

// The same rule on the user-input wire: a dismissed question carries no answer,
// because a dismissal is the user declining to give one.
func TestCmdUserInputResponse_AnswerTravelsOnlyWhenAnswered(t *testing.T) {
	const typed = "use the second option"
	cases := []struct {
		name       string
		action     string
		wantAnswer string
	}{
		{name: "answered forwards the text", action: marotte.UserInputActionAnswered, wantAnswer: typed},
		{name: "dismissed forwards no text", action: marotte.UserInputActionDismissed, wantAnswer: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bridge := &resultBridge{}
			deps := &takeDeps{benchDeps: newBenchDeps(), bridge: bridge, takeOK: true}
			cmd := decisionCommand(t, marotte.CmdUserInputResponse, marotte.UserInputResponseCommand{
				RequestID: decisionRequestID,
				Action:    tc.action,
				Answer:    typed,
			})

			if _, err := cmdUserInputResponse(t.Context(), deps, cmd); err != nil {
				t.Fatalf("CmdUserInputResponse(%s) = %v", tc.action, err)
			}

			if len(bridge.results) != 1 {
				t.Fatalf("got %d answers, want 1", len(bridge.results))
			}
			result, ok := bridge.results[0].(marotte.UserInputResult)
			if !ok {
				t.Fatalf("answer = %T, want marotte.UserInputResult", bridge.results[0])
			}
			if result.Action != tc.action {
				t.Errorf("action = %q, want %q", result.Action, tc.action)
			}
			if result.Answer != tc.wantAnswer {
				t.Errorf("answer for %q = %q, want %q", tc.action, result.Answer, tc.wantAnswer)
			}
		})
	}
}

// captureLogs swaps the slog default to a buffer-backed debug handler for the test; tests using it
// must not run in parallel.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	capture.SwapDefault(t, slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return buf
}

// An answer kiro-cli accepted must leave no failure line behind. A log that
// reports a failure the code did not have is worse than no log at all: it sends
// whoever reads it after an incident looking at the wrong handler.
func TestDecisionHandlers_AnAcceptedAnswerLogsNoFailure(t *testing.T) {
	for _, tc := range decisionCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			deps := &takeDeps{benchDeps: newBenchDeps(), bridge: &countingBridge{}, takeOK: true}

			if _, err := tc.run(deps); err != nil {
				t.Fatalf("%s = %v, want it to succeed", tc.name, err)
			}

			for _, level := range []string{"level=ERROR", "level=WARN"} {
				if strings.Contains(logs.String(), level) {
					t.Errorf("a successful answer logged %s: %s", level, logs.String())
				}
			}
		})
	}
}
