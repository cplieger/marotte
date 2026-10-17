package bridge

// A frame that fits but fails to decode is settled by what its head names: a request is answered by its id, a response
// fails its one request, and anything else fails nothing.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/slogx/capture"
)

// tooDeep is a JSON value nested past encoding/json's 10,000-level limit: valid JSON that Unmarshal refuses.
var tooDeep = strings.Repeat("[", 10_001) + "1" + strings.Repeat("]", 10_001)

// parseLoop is a live read loop whose background work runs inline, so draining the engine lane also settles every
// answer the loop started.
func parseLoop(t *testing.T) (*Engine, *captureWriter, func(frames ...string)) {
	t.Helper()
	e, w := liveReadLoop(t)
	e.background = func(fn func()) bool {
		fn()
		return true
	}
	out := &captureWriter{}
	e.stdin.Store(&stdinPipe{w: out})
	return e, out, func(frames ...string) {
		t.Helper()
		writeFrames(t, w, frames...)
		drainEngineLane(t, e, w)
	}
}

// answerFrame is one frame marotte wrote to kiro-cli's stdin.
type answerFrame struct {
	Error  *marotte.RPCErrorOut `json:"error"`
	Result *struct {
		Outcome struct {
			Outcome string `json:"outcome"`
		} `json:"outcome"`
	} `json:"result"`
	ID int64 `json:"id"`
}

// writtenAnswers waits up to two seconds for a first write, then decodes every frame written so far.
func writtenAnswers(t *testing.T, out *captureWriter) []answerFrame {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !out.wrote() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	out.mu.Lock()
	written := strings.TrimSpace(out.buf.String())
	out.mu.Unlock()
	if written == "" {
		return nil
	}
	var frames []answerFrame
	for line := range strings.SplitSeq(written, "\n") {
		var f answerFrame
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			t.Fatalf("decode the answer %q: %v", line, err)
		}
		frames = append(frames, f)
	}
	return frames
}

func noWrite(t *testing.T, out *captureWriter, what string) {
	t.Helper()
	out.mu.Lock()
	defer out.mu.Unlock()
	if out.writes > 0 {
		t.Errorf("%s: wrote %q, want nothing written", what, out.buf.String())
	}
}

func TestReadLoop_UndecodableRequestIsAnsweredByItsID(t *testing.T) {
	_ = capture.Default(t)
	_, out, send := parseLoop(t)
	send(`{"jsonrpc":"2.0","id":41,"method":"_kiro/test","params":` + tooDeep + "}\n")
	got := writtenAnswers(t, out)
	if len(got) != 1 {
		t.Fatalf("wrote %d frames, want exactly one answer", len(got))
	}
	if got[0].ID != 41 || got[0].Error == nil || got[0].Error.Code != marotte.RPCCodeParseError {
		t.Errorf("answer = %+v, want a -32700 error for request 41", got[0])
	}
}

func TestReadLoop_UndecodablePermissionIsAnsweredCancelled(t *testing.T) {
	_ = capture.Default(t)
	_, out, send := parseLoop(t)
	send(`{"jsonrpc":"2.0","id":42,"method":"session/request_permission","params":` + tooDeep + "}\n")
	got := writtenAnswers(t, out)
	if len(got) != 1 {
		t.Fatalf("wrote %d frames, want exactly one answer", len(got))
	}
	if got[0].Error != nil {
		t.Errorf("answered with error %+v: KAS's turn approval treats an error as approval", got[0].Error)
	}
	if got[0].ID != 42 || got[0].Result == nil || got[0].Result.Outcome.Outcome != string(marotte.StopReasonCancelled) {
		t.Errorf("answer = %+v, want outcome cancelled for request 42", got[0])
	}
}

func TestReadLoop_UndecodableResponseFailsOnlyItsRequest(t *testing.T) {
	_ = capture.Default(t)
	e, out, send := parseLoop(t)
	mine, other := pendingCall(e, 7, ""), pendingCall(e, 8, "")
	send(`{"jsonrpc":"2.0","id":7,"result":` + tooDeep + "}\n")
	gotReply(t, mine, undecodedResp, "the request the undecodable response answered")
	noReply(t, other, "another pending request")
	noWrite(t, out, "a response is never answered")
	if _, _, err := replyOutcome(pendingReply{resp: undecodedResp}); err == nil {
		t.Error("the undecodable-response reply reads as success to its caller")
	}
}

func TestReadLoop_UndecodableNotificationAnswersAndFailsNothing(t *testing.T) {
	_ = capture.Default(t)
	e, out, send := parseLoop(t)
	s := newTestSession()
	s.engine = e
	e.lanesMu.Lock()
	e.sessions[s.id] = s
	e.lanesMu.Unlock()
	pending := pendingCall(e, 7, s.id)
	send(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"` + string(s.id) + `","update":` + tooDeep + "}}\n")
	noReply(t, pending, "the session's own request, for one undecodable update")
	noWrite(t, out, "a notification is never answered")
}

func TestReadLoop_StringIDRequestIsDroppedUnanswered(t *testing.T) {
	_ = capture.Default(t)
	e, out, send := parseLoop(t)
	pending := pendingCall(e, 0, "")
	send(`{"jsonrpc":"2.0","id":"abc","method":"_kiro/test","params":{}}` + "\n")
	noWrite(t, out, "a request with no integer id has nothing to be answered under")
	noReply(t, pending, "the request marotte holds under id 0")
}
