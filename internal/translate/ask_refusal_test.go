package translate

import (
	"context"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// TestAskHandlers_AnswerAnUndecodableRequest pins an answer on the frame's id for each ask
// handler, with no rpcErr (turn approval fails OPEN on one) and the kind's own fail-closed
// value; a permission's names no option.
func TestAskHandlers_AnswerAnUndecodableRequest(t *testing.T) {
	// Well-formed JSON whose TYPES mismatch the decode struct: an upstream shape change.
	cases := map[string]struct {
		params map[string]any
		call   func(tr *Translator, chatID marotte.ChatID, origin AskOrigin, msg *marotte.RPCResponse)
		want   any
	}{
		"permission: options is not an array": {
			params: map[string]any{"sessionId": "s", "options": 7},
			call: func(tr *Translator, chatID marotte.ChatID, origin AskOrigin, msg *marotte.RPCResponse) {
				tr.HandlePermissionRequest(t.Context(), chatID, origin, msg)
			},
			want: marotte.PermissionOutcomeCancelled(),
		},
		"permission: a decorative meta field changed shape": {
			// _meta.kiro.consent shares the struct with the routing fields, so a change to it breaks the ask.
			params: map[string]any{
				"sessionId": "s",
				"_meta":     map[string]any{"kiro": map[string]any{"consent": true}},
			},
			call: func(tr *Translator, chatID marotte.ChatID, origin AskOrigin, msg *marotte.RPCResponse) {
				tr.HandlePermissionRequest(t.Context(), chatID, origin, msg)
			},
			want: marotte.PermissionOutcomeCancelled(),
		},
		"elicitation: the body is not an object": {
			params: map[string]any{"sessionId": "s", "elicitation": "not-an-object"},
			call: func(tr *Translator, chatID marotte.ChatID, origin AskOrigin, msg *marotte.RPCResponse) {
				tr.HandleElicitationCreate(t.Context(), chatID, origin, msg)
			},
			want: marotte.ElicitationResult{Action: marotte.ElicitationActionCancel},
		},
		"user input: options is not an array": {
			params: map[string]any{"sessionId": "s", "question": "Which?", "options": 7},
			call: func(tr *Translator, chatID marotte.ChatID, origin AskOrigin, msg *marotte.RPCResponse) {
				tr.HandleUserInput(t.Context(), chatID, origin, msg)
			},
			want: marotte.UserInputResult{Action: marotte.UserInputActionDismissed},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			deps := newBaseDeps()
			tr := New(rolesOf(deps))
			id := int64(4242)

			tc.call(tr, "c1", deps.origin(), &marotte.RPCResponse{ID: &id, Params: mustJSON(t, tc.params)})

			if len(deps.asked) != 1 {
				t.Fatalf("got %d answers, want 1 — an unanswered request wedges the tool batch", len(deps.asked))
			}
			got := deps.asked[0]
			if got.requestID != id {
				t.Errorf("answered request_id = %d, want %d", got.requestID, id)
			}
			if got.rpcErr != nil {
				t.Errorf("answered with rpcErr = %v, want nil — an RPC error on a turn_approval frame auto-approves the turn", got.rpcErr)
			}
			gotJSON, wantJSON := string(mustJSON(t, got.result)), string(mustJSON(t, tc.want))
			if gotJSON != wantJSON {
				t.Errorf("answer = %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

// labelledOrigin is a distinct bridge, so a registration can be told apart from any other.
type labelledOrigin struct{ name string }

func (labelledOrigin) Respond(context.Context, int64, any, error) error { return nil }

// TestAskHandlers_RegisterTheAskWithTheBridgeItArrivedOn pins the binding the answer and the
// bridge-end retirement both read.
func TestAskHandlers_RegisterTheAskWithTheBridgeItArrivedOn(t *testing.T) {
	cases := map[string]func(tr *Translator, origin AskOrigin, id *int64){
		"permission": func(tr *Translator, origin AskOrigin, id *int64) {
			tr.HandlePermissionRequest(t.Context(), "c1", origin, &marotte.RPCResponse{ID: id, Params: mustJSON(t, map[string]any{
				"sessionId": "s", "toolCall": map[string]any{"toolCallId": "tc", "title": "Write"},
			})})
		},
		"elicitation": func(tr *Translator, origin AskOrigin, id *int64) {
			tr.HandleElicitationCreate(t.Context(), "c1", origin, &marotte.RPCResponse{ID: id, Params: mustJSON(t, map[string]any{
				"sessionId": "s", "elicitation": map[string]any{"mode": "form", "message": "Token?"},
			})})
		},
		"user input": func(tr *Translator, origin AskOrigin, id *int64) {
			tr.HandleUserInput(t.Context(), "c1", origin, userInputMsg(t, id, map[string]any{"question": "Which?"}))
		},
	}
	for name, ask := range cases {
		t.Run(name, func(t *testing.T) {
			deps := &pendingCaptureDeps{baseDeps: newBaseDeps()}
			tr := New(rolesOf(deps))
			id := int64(5)
			origin := labelledOrigin{name: "arrived-on"}

			ask(tr, origin, &id)

			if len(deps.pendingOrigins) != 1 || deps.pendingOrigins[0] != AskOrigin(origin) {
				t.Errorf("registered origins = %v, want [%v]: the answer and the end both read it", deps.pendingOrigins, origin)
			}
		})
	}
}

// TestHandlePermissionRequest_RefusalNamesNoOption pins that cancelled selects nothing (a
// fabricated optionId would apply an unoffered choice).
func TestHandlePermissionRequest_RefusalNamesNoOption(t *testing.T) {
	deps := newBaseDeps()
	tr := New(rolesOf(deps))
	id := int64(7)

	tr.HandlePermissionRequest(t.Context(), "c1", deps.origin(), &marotte.RPCResponse{
		ID:     &id,
		Params: mustJSON(t, map[string]any{"options": 7}),
	})

	if len(deps.asked) != 1 {
		t.Fatalf("got %d answers, want 1", len(deps.asked))
	}
	out, ok := deps.asked[0].result.(*marotte.PermissionOutcome)
	if !ok {
		t.Fatalf("answer = %T, want *marotte.PermissionOutcome", deps.asked[0].result)
	}
	if out.Outcome.OptionID != "" {
		t.Errorf("OptionID = %q, want empty — the refusal must name no option", out.Outcome.OptionID)
	}
	if out.Outcome.Outcome != string(marotte.StopReasonCancelled) {
		t.Errorf("outcome = %q, want %q", out.Outcome.Outcome, marotte.StopReasonCancelled)
	}
}

// TestAskHandlers_DecodedFrameStillReachesTheTracker pins that the ordinary path still
// registers the ask for reconnect replay.
func TestAskHandlers_DecodedFrameStillReachesTheTracker(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	id := int64(11)

	tr.HandlePermissionRequest(t.Context(), "c1", deps.origin(), &marotte.RPCResponse{
		ID: &id,
		Params: mustJSON(t, map[string]any{
			"sessionId": "s",
			"toolCall":  map[string]any{"toolCallId": "tc-1", "title": "Write", "kind": "edit"},
			"options":   []map[string]any{{"optionId": "allow", "name": "Allow", "kind": "allow_once"}},
		}),
	})

	if _, ok := findPermissionNeeded(t, events); !ok {
		t.Error("no permission_needed event broadcast for a well-formed ask")
	}
	if len(deps.asked) != 0 {
		t.Errorf("answered a well-formed ask on the wire (%d times); the user's reply is the answer", len(deps.asked))
	}
}
