package command

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rpcerr"
	"github.com/cplieger/marotte/internal/testsupport"
)

// tombstonedChats is a chat store that refuses every write the way the real one
// refuses a write to an id deleted inside the tombstone window.
type tombstonedChats struct{ chatStore }

func (tombstonedChats) Mutate(context.Context, marotte.ChatID, func(*marotte.Chat, bool) bool) (string, error) {
	return "", chat.ErrTombstoned
}

// promptSpy answers every role and records the two things a refused prompt must
// not do: open a bridge, and broadcast anything other than the refusal.
type promptSpy struct {
	hostDouble
	opened int
	events []marotte.ServerEvent
}

// OpenTurn relays the store's tombstone the way the registry does: the turn_open
// is the first write a prompt makes, and a tombstoned chat refuses it.
func (*promptSpy) OpenTurn(context.Context, marotte.ChatID, TurnOpen) (string, error) {
	return "", chat.ErrTombstoned
}

func (s *promptSpy) OpenBridge(context.Context, marotte.ChatID, string) (Bridge, error) {
	s.opened++
	return nil, errors.New("the bridge must not be opened")
}

func (s *promptSpy) Broadcast(_ context.Context, evt marotte.ServerEvent) {
	s.events = append(s.events, evt)
}

func promptReq(t *testing.T, chatID marotte.ChatID, text string) *marotte.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(marotte.PromptCommand{Text: text, MessageID: "m-1"})
	if err != nil {
		t.Fatalf("marshal prompt payload: %v", err)
	}
	return &marotte.ClientCommand{Type: marotte.CmdPrompt, ChatID: chatID, Payload: payload}
}

// TestCmdPrompt_RefusesATombstonedChatBeforeSpawning: a prompt on a tombstoned chat is a 409 before
// the bridge is spawned, because every later step depends on the record the store declined to
// create.
func TestCmdPrompt_RefusesATombstonedChatBeforeSpawningABridge(t *testing.T) {
	spy := &promptSpy{hostDouble: newTestHost(t, tombstonedChats{testsupport.NewInMemoryChatStore()})}
	roles := promptRolesOf(spy)
	roles.bridges = spy
	roles.bus = spy

	_, err := cmdPrompt(t.Context(), roles, promptReq(t, "c1", "do the thing"))

	if err == nil {
		t.Fatal("CmdPrompt on a tombstoned chat returned no error; the turn ran against a chat with no record")
	}
	if got := statusOf(err); got != http.StatusConflict {
		t.Errorf("CmdPrompt on a tombstoned chat = %d, want %d", got, http.StatusConflict)
	}
	if spy.opened != 0 {
		t.Errorf("OpenBridge called %d times on a refused prompt, want 0: a bridge without a chat record breaks the live-bridge invariant", spy.opened)
	}
}

type promptBridgeSpy struct {
	hostDouble
	bridge Bridge
	events []marotte.ServerEvent
}

func (s *promptBridgeSpy) OpenBridge(context.Context, marotte.ChatID, string) (Bridge, error) {
	return s.bridge, nil
}

func (s *promptBridgeSpy) Broadcast(_ context.Context, evt marotte.ServerEvent) {
	s.events = append(s.events, evt)
}

// TestCmdPrompt_AnAuthFailureTravelsAsTheSignInCode: a rejected token travels as
// auth_token_unavailable, the only code the client routes to a Sign in CTA.
func TestCmdPrompt_AnAuthFailureTravelsAsTheSignInCode(t *testing.T) {
	cases := map[string]struct {
		callErr  error
		wantCode marotte.ErrorCode
	}{
		"the token was rejected": {
			callErr:  rpcErr(t, marotte.RPCCodeInternal, "Authentication failed. Please sign in again.", nil),
			wantCode: marotte.ErrCodeAuthTokenUnavailable,
		},
		// The control: every other failure keeps the generic code, or the banner stops meaning
		// "sign in".
		"a refused payload": {
			callErr: rpcErr(t, marotte.RPCCodeInternal, "Internal error", map[string]string{
				"details": "PromptTooLong",
			}),
			wantCode: marotte.ErrCodePromptFailed,
		},
		"an entitlement refusal is not a sign-in problem": {
			callErr: rpcErr(t, marotte.RPCCodeBridgeExited, "this account does not have access to them.", rpcerr.Mapped{
				ErrorType:      "ModelRegistryAccessDeniedError",
				RetryErrorType: "CLIENT_ERROR",
			}),
			wantCode: marotte.ErrCodePromptFailed,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			spy := &promptBridgeSpy{
				hostDouble: newTestHost(t, store),
				bridge:     &recordingBridge{callErr: tc.callErr},
			}
			roles := promptRolesOf(spy)
			roles.bridges = spy
			roles.bus = spy
			join := &promptJoin{}
			roles.lifecycle = join

			if _, err := cmdPrompt(t.Context(), roles, promptReq(t, "c1", "do the thing")); err != nil {
				t.Fatalf("CmdPrompt = %v, want the early ack", err)
			}
			join.join()

			var codes []marotte.ErrorCode
			for _, evt := range spy.events {
				if evt.Type != marotte.EventError {
					continue
				}
				p, ok := evt.Payload.(marotte.ErrorPayload)
				if !ok {
					t.Fatalf("error event payload is %T, want marotte.ErrorPayload", evt.Payload)
				}
				codes = append(codes, p.Code)
			}
			if len(codes) != 1 {
				t.Fatalf("error events = %v, want exactly one", codes)
			}
			if codes[0] != tc.wantCode {
				t.Errorf("error code = %q, want %q", codes[0], tc.wantCode)
			}
		})
	}
}

func TestReportPromptFailure_AuthClassLatchesForReadiness(t *testing.T) {
	cases := map[string]struct {
		callErr error
		want    bool
	}{
		"backend_rejects_credential": {
			callErr: rpcErr(t, marotte.RPCCodeInternal, "Authentication failed. Please sign in again.", nil),
			want:    true,
		},
		"non_auth_failure": {
			callErr: rpcErr(t, marotte.RPCCodeInternal, "Internal error", map[string]string{
				"details": "PromptTooLong",
			}),
			want: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			spy := &promptBridgeSpy{
				hostDouble: newTestHost(t, testsupport.NewInMemoryChatStore()),
				bridge:     &recordingBridge{callErr: tc.callErr},
			}
			readiness := new(AuthReadiness)
			roles := promptRolesOf(spy)
			roles.bridges = spy
			roles.bus = spy
			roles.auth = readiness
			join := &promptJoin{}
			roles.lifecycle = join

			if _, err := cmdPrompt(t.Context(), roles, promptReq(t, "c1", "do the thing")); err != nil {
				t.Fatalf("CmdPrompt = %v, want the early ack", err)
			}
			join.join()

			if got := readiness.Unavailable(); got != tc.want {
				t.Errorf("AuthReadiness.Unavailable() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReportPromptSuccess_ClearsAuthLatch(t *testing.T) {
	spy := &promptBridgeSpy{
		hostDouble: newTestHost(t, testsupport.NewInMemoryChatStore()),
		bridge:     &recordingBridge{},
	}
	readiness := new(AuthReadiness)
	readiness.record(errors.New("backend rejected the credential"))
	roles := promptRolesOf(spy)
	roles.bridges = spy
	roles.bus = spy
	roles.auth = readiness
	join := &promptJoin{}
	roles.lifecycle = join

	if _, err := cmdPrompt(t.Context(), roles, promptReq(t, "c1", "do the thing")); err != nil {
		t.Fatalf("CmdPrompt = %v, want the early ack", err)
	}
	join.join()

	if readiness.Unavailable() {
		t.Error("AuthReadiness stayed unavailable after a completed prompt")
	}
}
