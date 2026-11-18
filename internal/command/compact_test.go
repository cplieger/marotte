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
	out       CompactOutcome
}

func (f *fakeCompactor) RequestCompact(_ context.Context, chatID marotte.ChatID) (CompactOutcome, error) {
	f.requested = append(f.requested, chatID)
	return f.out, f.err
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

// The press is answered once its call is under way: the outcome comes later, as kiro-cli's compaction entry or one
// toast, so the answer claims neither.
func TestCmdCompact_AnswersOnceTheCallIsUnderWay(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  CompactOutcome
		log  string
	}{
		{name: "started", out: CompactStarted, log: `msg="compact started"`},
		{name: "one already running", out: CompactRunning, log: `msg="compact already running"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureLogs(t)
			f := &fakeCompactor{out: tc.out}

			body, err := cmdCompact(t.Context(), f, compactReq("c1"))
			if err != nil {
				t.Fatalf("CmdCompact: %v", err)
			}
			if m, _ := body.(map[string]any); m["ok"] != true || len(m) != 1 {
				t.Errorf("body = %v, want only ok", body)
			}
			if got := buf.String(); !strings.Contains(got, tc.log) {
				t.Errorf("log = %q, want %s", got, tc.log)
			}
			if len(f.requested) != 1 || f.requested[0] != "c1" {
				t.Errorf("requests = %v, want [c1]", f.requested)
			}
		})
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

func TestCmdCompact_ARequestErrorIsBadGateway(t *testing.T) {
	_, err := cmdCompact(t.Context(), &fakeCompactor{err: errors.New("pipe closed")}, compactReq("c1"))
	if got := statusOf(err); got != 502 {
		t.Errorf("status = %d, want 502", got)
	}
}
