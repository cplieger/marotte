package command

import (
	"context"
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

	if _, err := cmdCompact(t.Context(), newBridgeHost(testsupport.NewInMemoryChatStore(), b), nil, compactReq("c1")); err != nil {
		t.Fatalf("CmdCompact: %v", err)
	}

	if got := buf.String(); !strings.Contains(got, `msg="compact accepted"`) {
		t.Errorf("log did not report acceptance; got %q", got)
	}
}

// No test asserts this handler records nothing: its bridgeAccess parameter exposes no store or
// broadcaster, so the narrow type is the assertion.

// TestCmdCompact_SendsTheSessionsWire pins the verb and its params.
func TestCmdCompact_SendsTheSessionsWire(t *testing.T) {
	b := &recordingBridge{result: map[string]any{"success": true}, sessionID: "sess-1"}

	if _, err := cmdCompact(t.Context(), newBridgeHost(testsupport.NewInMemoryChatStore(), b), nil, compactReq("c1")); err != nil {
		t.Fatalf("CmdCompact: %v", err)
	}

	if b.gotMethod != marotte.MethodSessionCompact {
		t.Errorf("method = %q, want %q", b.gotMethod, marotte.MethodSessionCompact)
	}
	if got := b.gotParams["sessionId"]; got != b.sessionID {
		t.Errorf("sessionId = %v, want %v", got, b.sessionID)
	}
}

// TestCmdCompact_NoBridgeSaysNothingWasCompacted: the client shows this text after "Compact
// failed: ", so it must be prose, never an internal code.
func TestCmdCompact_NoBridgeSaysNothingWasCompacted(t *testing.T) {
	_, err := cmdCompact(t.Context(), newBridgeHost(testsupport.NewInMemoryChatStore(), nil), nil, compactReq("c1"))
	if err == nil {
		t.Fatal("a compaction with no bridge reported success")
	}
	if got := statusOf(err); got != 409 {
		t.Errorf("status = %d, want 409", got)
	}
	if got, want := errText(err), "this chat's agent session could not be started, so the conversation was not compacted"; got != want {
		t.Errorf("refusal = %q, want %q", got, want)
	}
}

// TestCmdCompact_RefusalNamesBothClasses: every {success: false} gets this one text, whether KAS
// refused to start or a started summary did not commit, so the text must name both.
func TestCmdCompact_RefusalNamesBothClasses(t *testing.T) {
	b := &recordingBridge{result: map[string]any{"success": false}, sessionID: "sess-1"}

	_, err := cmdCompact(t.Context(), newBridgeHost(testsupport.NewInMemoryChatStore(), b), nil, compactReq("c1"))
	if err == nil {
		t.Fatal("a refused compaction reported success")
	}
	if got := statusOf(err); got != 409 {
		t.Errorf("status = %d, want 409", got)
	}
	text := errText(err)
	for _, want := range []string{"busy", "summary failed"} {
		if !strings.Contains(text, want) {
			t.Errorf("refusal %q does not contain %q", text, want)
		}
	}
}

// positionedBridge answers its call at a fixed read loop position.
type positionedBridge struct {
	*recordingBridge
	seq uint64
}

func (b *positionedBridge) CallAt(ctx context.Context, method string, params any) (*marotte.RPCResponse, uint64, error) {
	resp, err := b.Call(ctx, method, params)
	return resp, b.seq, err
}

type readLoopLog struct {
	chats []marotte.ChatID
	seqs  []uint64
}

func (l *readLoopLog) AwaitReadLoop(_ context.Context, chatID marotte.ChatID, seq uint64) bool {
	l.chats = append(l.chats, chatID)
	l.seqs = append(l.seqs, seq)
	return true
}

// TestCmdCompact_AnswersOnceTheFramesBeforeItsReplyFolded pins the wait on the reply's position:
// KAS writes the compaction's summary frame before its reply, and the next prompt must find it folded.
func TestCmdCompact_AnswersOnceTheFramesBeforeItsReplyFolded(t *testing.T) {
	b := &positionedBridge{recordingBridge: &recordingBridge{result: map[string]any{"success": true}, sessionID: "sess-1"}, seq: 7}
	folds := &readLoopLog{}

	if _, err := cmdCompact(t.Context(), newBridgeHost(testsupport.NewInMemoryChatStore(), b), folds, compactReq("c1")); err != nil {
		t.Fatalf("CmdCompact: %v", err)
	}

	if len(folds.seqs) != 1 || folds.chats[0] != "c1" || folds.seqs[0] != 7 {
		t.Errorf("read loop waits = %v at %v, want one for c1 at the reply's position 7", folds.chats, folds.seqs)
	}
}
