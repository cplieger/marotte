package agent

// The session is the only CreateTerminalRequest field that can name a workflow step, so
// the create is where the link enters the registry for the run bounds.

import (
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

func TestTermCreate_RecordsTheRequestsOwnSession(t *testing.T) {
	cases := []struct {
		name        string
		params      map[string]any
		wantSession string
	}{{
		name:        "a create naming its session",
		params:      map[string]any{"command": "true", "sessionId": "step-session-1"},
		wantSession: "step-session-1",
	}, {
		// An absent sessionId is the zero value, not a parse failure: the terminal is created and bounded.
		name:        "a create omitting it",
		params:      map[string]any{"command": "true"},
		wantSession: "",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, br := hubForFSTest(t, t.TempDir())
			id := int64(5001)

			h.translateACPEvent("c1", h.originOf("c1"), &marotte.RPCResponse{
				ID: &id, Method: methodTermCreate, Params: mustJSON(t, tc.params),
			})

			select {
			case <-br.done:
			case <-time.After(5 * time.Second):
				t.Fatal("terminal/create got no response")
			}
			br.respMu.Lock()
			got := br.response
			br.respMu.Unlock()
			if got.err != nil {
				t.Fatalf("terminal/create answered %v, want the terminal it created", got.err)
			}

			term := singleTerm(t, h)
			waitClosed(t, term.done, "terminal")
			if term.session != tc.wantSession {
				t.Errorf("the registry recorded session %q, want %q", term.session, tc.wantSession)
			}
		})
	}
}

// The session is an opaque id compared by equality, never parsed or rendered.
func TestTermCreate_TheRecordedSessionIsVerbatim(t *testing.T) {
	h, br := hubForFSTest(t, t.TempDir())
	id := int64(5002)
	const weird = "  step/../session with spaces\tand a tab  "

	h.translateACPEvent("c1", h.originOf("c1"), &marotte.RPCResponse{
		ID: &id, Method: methodTermCreate,
		Params: mustJSON(t, map[string]any{"command": "true", "sessionId": weird}),
	})

	select {
	case <-br.done:
	case <-time.After(5 * time.Second):
		t.Fatal("terminal/create got no response")
	}
	term := singleTerm(t, h)
	waitClosed(t, term.done, "terminal")
	if term.session != weird {
		t.Errorf("recorded session = %q, want it verbatim (%q)", term.session, weird)
	}
}
