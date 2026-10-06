package agent

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// TestTerminalResponders_AnswerAnUndecodableRequest pins a fail-closed answer for every
// declined request: KAS awaits these with no timeout, so a bare return wedges the batch.
func TestTerminalResponders_AnswerAnUndecodableRequest(t *testing.T) {
	for _, method := range []string{
		methodTermOutput,
		methodTermRelease,
		methodTermWaitForExit,
		methodTermKill,
		// The sibling that has always answered.
		methodTermCreate,
	} {
		t.Run(method, func(t *testing.T) {
			h, br := hubForFSTest(t, t.TempDir())
			id := int64(4711)

			// Valid JSON with the wrong type, so the frame fails at the responder's decode; an upstream
			// shape change is what makes this reachable.
			h.translateACPEvent("c1", &marotte.RPCResponse{
				Method: method,
				ID:     &id,
				Params: json.RawMessage(`{"terminalId":42,"command":42}`),
			})

			select {
			case <-br.done:
			case <-time.After(2 * time.Second):
				t.Fatalf("%s with undecodable params got no response: an unanswered request "+
					"wedges the tool batch until process teardown", method)
			}

			br.respMu.Lock()
			got := br.response
			br.respMu.Unlock()

			if got.id != id {
				t.Errorf("%s answered id %d, want %d", method, got.id, id)
			}
			if got.err == nil {
				t.Errorf("%s answered with a success result (%v), want an error: the params did "+
					"not decode, so there is nothing to succeed at", method, got.result)
			}
		})
	}
}

// TestTerminalResponders_UseTheRequestsOwnChatID pins the chat id respondErr resolves the
// reply bridge by; an empty one drops the refusal.
func TestTerminalResponders_UseTheRequestsOwnChatID(t *testing.T) {
	h, br := hubForFSTest(t, t.TempDir())
	id := int64(4712)

	h.translateACPEvent("", &marotte.RPCResponse{
		Method: methodTermOutput,
		ID:     &id,
		Params: json.RawMessage(`{"terminalId":42}`),
	})

	select {
	case <-br.done:
		t.Fatal("a chat with no bridge got a response, so this test cannot tell a delivered " +
			"refusal from a dropped one")
	case <-time.After(200 * time.Millisecond):
	}

	// The same frame on the chat that owns the bridge lands.
	h.translateACPEvent("c1", &marotte.RPCResponse{
		Method: methodTermOutput,
		ID:     &id,
		Params: json.RawMessage(`{"terminalId":42}`),
	})
	select {
	case <-br.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the refusal did not reach the request's own bridge")
	}
}
