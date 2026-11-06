package command

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func compactReq(chatID marotte.ChatID) *marotte.ClientCommand {
	return &marotte.ClientCommand{Type: marotte.CmdCompact, ChatID: chatID}
}

// fakeCompactor answers RequestCompact with out and err and records every call.
type fakeCompactor struct {
	err       error
	requested []marotte.ChatID
	cancelled []marotte.ChatID
	out       CompactOutcome
}

func (f *fakeCompactor) RequestCompact(_ context.Context, chatID marotte.ChatID) (CompactOutcome, error) {
	f.requested = append(f.requested, chatID)
	return f.out, f.err
}

func (f *fakeCompactor) CancelCompact(_ context.Context, chatID marotte.ChatID) error {
	f.cancelled = append(f.cancelled, chatID)
	return f.err
}

// TestCompact_SendsTheSessionsWire pins the verb and its params.
func TestCompact_SendsTheSessionsWire(t *testing.T) {
	b := &recordingBridge{result: map[string]any{"success": true}, sessionID: "sess-1"}

	accepted, _, err := Compact(t.Context(), b)
	if err != nil || !accepted {
		t.Fatalf("Compact = (%v, %v), want accepted", accepted, err)
	}
	if b.gotMethod != marotte.MethodSessionCompact {
		t.Errorf("method = %q, want %q", b.gotMethod, marotte.MethodSessionCompact)
	}
	if got := b.gotParams["sessionId"]; got != b.sessionID {
		t.Errorf("sessionId = %v, want %v", got, b.sessionID)
	}
}

func TestCompact_SuccessFalseIsNotAccepted(t *testing.T) {
	b := &recordingBridge{result: map[string]any{"success": false}, sessionID: "sess-1"}
	if accepted, _, err := Compact(t.Context(), b); err != nil || accepted {
		t.Errorf("Compact on {success: false} = (%v, %v), want (false, nil)", accepted, err)
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

// TestCompact_AnswersTheReplysPosition: the caller waits for the frames before it, the summary among them.
func TestCompact_AnswersTheReplysPosition(t *testing.T) {
	b := &positionedBridge{recordingBridge: &recordingBridge{result: map[string]any{"success": true}, sessionID: "sess-1"}, seq: 7}
	if _, seq, err := Compact(t.Context(), b); err != nil || seq != 7 {
		t.Errorf("Compact = (seq %d, %v), want the reply's position 7", seq, err)
	}
}

// TestCmdCompact_ReportsAcceptanceNotCompaction asserts that `{success: true}` covers five outcomes, three of
// which compact nothing, so the log line must not claim the chat was compacted.
func TestCmdCompact_ReportsAcceptanceNotCompaction(t *testing.T) {
	buf := captureLogs(t)
	f := &fakeCompactor{out: CompactAccepted}

	body, err := cmdCompact(t.Context(), f, compactReq("c1"))
	if err != nil {
		t.Fatalf("CmdCompact: %v", err)
	}
	if m, _ := body.(map[string]any); m["ok"] != true || m["queued"] != nil {
		t.Errorf("body = %v, want ok and not queued", body)
	}
	if got := buf.String(); !strings.Contains(got, `msg="compact accepted"`) {
		t.Errorf("log did not report acceptance; got %q", got)
	}
	if len(f.requested) != 1 || f.requested[0] != "c1" {
		t.Errorf("requests = %v, want [c1]", f.requested)
	}
}

// TestCmdCompact_AQueuedRequestAnswersQueued: the client shows the queued row from the answer and the header.
func TestCmdCompact_AQueuedRequestAnswersQueued(t *testing.T) {
	body, err := cmdCompact(t.Context(), &fakeCompactor{out: CompactQueued}, compactReq("c1"))
	if err != nil {
		t.Fatalf("CmdCompact: %v", err)
	}
	if m, _ := body.(map[string]any); m["ok"] != true || m["queued"] != true {
		t.Errorf("body = %v, want ok and queued", body)
	}
}

// TestCmdCompact_NoSessionSaysNothingWasCompacted: the client shows this text after "Compact
// failed: ", so it must be prose, never an internal code.
func TestCmdCompact_NoSessionSaysNothingWasCompacted(t *testing.T) {
	_, err := cmdCompact(t.Context(), &fakeCompactor{out: CompactNoSession}, compactReq("c1"))
	if err == nil {
		t.Fatal("a compaction with no session reported success")
	}
	if got := statusOf(err); got != 409 {
		t.Errorf("status = %d, want 409", got)
	}
	if got, want := errText(err), "this chat's agent session could not be started, so the conversation was not compacted"; got != want {
		t.Errorf("refusal = %q, want %q", got, want)
	}
}

// TestCmdCompact_RefusalNamesEveryCause: every {success: false} gets this one text, whether KAS
// refused to start, a started summary did not commit, or a PreCompact hook blocked it.
func TestCmdCompact_RefusalNamesEveryCause(t *testing.T) {
	_, err := cmdCompact(t.Context(), &fakeCompactor{out: CompactRefused}, compactReq("c1"))
	if err == nil {
		t.Fatal("a refused compaction reported success")
	}
	if got := statusOf(err); got != 409 {
		t.Errorf("status = %d, want 409", got)
	}
	text := errText(err)
	for _, want := range []string{"busy", "summary failed", "PreCompact hook"} {
		if !strings.Contains(text, want) {
			t.Errorf("refusal %q does not contain %q", text, want)
		}
	}
}

func TestCmdCompact_ACallErrorIsBadGateway(t *testing.T) {
	_, err := cmdCompact(t.Context(), &fakeCompactor{out: CompactRefused, err: errors.New("pipe closed")}, compactReq("c1"))
	if got := statusOf(err); got != 502 {
		t.Errorf("status = %d, want 502", got)
	}
}

func TestCmdCancelCompact_CancelsTheChatsQueuedCompaction(t *testing.T) {
	f := &fakeCompactor{}
	cmd := &marotte.ClientCommand{Type: marotte.CmdCancelCompact, ChatID: "c1"}
	if _, err := cmdCancelCompact(t.Context(), f, cmd); err != nil {
		t.Fatalf("CmdCancelCompact: %v", err)
	}
	if len(f.cancelled) != 1 || f.cancelled[0] != "c1" {
		t.Errorf("cancels = %v, want [c1]", f.cancelled)
	}
	if _, err := cmdCancelCompact(t.Context(), f, &marotte.ClientCommand{Type: marotte.CmdCancelCompact}); statusOf(err) != 400 {
		t.Errorf("a cancel with no chat = %v, want 400", err)
	}
}
