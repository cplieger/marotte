package command

import (
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

func compactReq(chatID marotte.ChatID) *marotte.ClientCommand {
	return &marotte.ClientCommand{Type: marotte.CmdCompact, ChatID: chatID}
}

// TestCmdCompact_ReportsAcceptanceNotCompaction asserts that `{success: true}` covers five outcomes, three of
// which compact nothing, so the log line must not claim the chat was compacted.
func TestCmdCompact_ReportsAcceptanceNotCompaction(t *testing.T) {
	buf := captureLogs(t)
	b := &recordingBridge{result: map[string]any{"success": true}, sessionID: "sess-1"}

	if _, err := CmdCompact(t.Context(), newBridgeHost(testsupport.NewInMemoryChatStore(), b), compactReq("c1")); err != nil {
		t.Fatalf("CmdCompact: %v", err)
	}

	if got := buf.String(); !strings.Contains(got, `msg="compact accepted"`) {
		t.Errorf("log did not report acceptance; got %q", got)
	}
}

// No test asserts this handler records nothing: its BridgeAccess parameter exposes no store or
// broadcaster, so the narrow type is the assertion.

// TestCmdCompact_SendsTheSessionsWire pins the verb and its params.
func TestCmdCompact_SendsTheSessionsWire(t *testing.T) {
	b := &recordingBridge{result: map[string]any{"success": true}, sessionID: "sess-1"}

	if _, err := CmdCompact(t.Context(), newBridgeHost(testsupport.NewInMemoryChatStore(), b), compactReq("c1")); err != nil {
		t.Fatalf("CmdCompact: %v", err)
	}

	if b.gotMethod != marotte.MethodSessionCompact {
		t.Errorf("method = %q, want %q", b.gotMethod, marotte.MethodSessionCompact)
	}
	if got := b.gotParams["sessionId"]; got != b.sessionID {
		t.Errorf("sessionId = %v, want %v", got, b.sessionID)
	}
}

// TestCmdCompact_RefusalIs409 pins the branch whose message the release notes
// record as misdirecting across seven refusal causes, so a reword lands against
// a test rather than into a vacuum.
func TestCmdCompact_RefusalIs409(t *testing.T) {
	b := &recordingBridge{result: map[string]any{"success": false}, sessionID: "sess-1"}

	_, err := CmdCompact(t.Context(), newBridgeHost(testsupport.NewInMemoryChatStore(), b), compactReq("c1"))
	if err == nil {
		t.Fatal("a refused compaction reported success")
	}
	if got := statusOf(err); got != 409 {
		t.Errorf("status = %d, want 409", got)
	}
}
