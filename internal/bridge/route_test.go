package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

func TestNewSession_SendsTheMintedOverrideAndRegistersTheLaneFirst(t *testing.T) {
	k := newFakeKAS(t)
	done := make(chan error, 1)
	go func() {
		_, err := k.e.NewSession(t.Context(), &marotte.SessionOpts{}, nil)
		done <- err
	}()
	f := k.nextMethod(methodSessionNew)
	minted := f.overrideID()
	if !strings.HasPrefix(string(minted), "sess_") {
		t.Fatalf("session/new carried overrideSessionId %q, want a minted sess_ id:\n%s", minted, f.raw)
	}
	k.e.lanesMu.Lock()
	_, registered := k.e.sessions[minted]
	k.e.lanesMu.Unlock()
	if !registered {
		t.Error("session/new was written before the minted id's lane was registered")
	}
	k.reply(f.ID, map[string]any{"sessionId": minted})
	if err := <-done; err != nil {
		t.Fatalf("NewSession: %v", err)
	}
}

// TestRoute_FramesDuringSessionNewReachTheNewSessionsLane: KAS sends a request and a notification naming the new id
// before its reply; both reach the new lane, and the request is answered by that session's own consumer, which is what
// lets the door finish.
func TestRoute_FramesDuringSessionNewReachTheNewSessionsLane(t *testing.T) {
	k := newFakeKAS(t)
	got := make(chan marotte.Notification, 4)
	attach := func(s *Session) {
		go func() {
			for n := range s.NotifCh() {
				got <- n
				if n.Msg.ID != nil {
					_ = s.Respond(context.Background(), *n.Msg.ID, map[string]any{"shellType": "bash"}, nil)
				}
			}
		}()
	}
	done := make(chan error, 1)
	go func() {
		_, err := k.e.NewSession(t.Context(), &marotte.SessionOpts{}, attach)
		done <- err
	}()
	f := k.nextMethod(methodSessionNew)
	minted := f.overrideID()
	k.notify("_kiro/customAgent/config_error", map[string]any{"sessionId": minted, "path": "/a.json"})
	k.request(901, "_kiro/terminal/shell_type", map[string]any{"sessionId": minted})
	answer := k.next()
	if answer.ID == nil || *answer.ID != 901 {
		t.Fatalf("the request during session/new was not answered first; the engine wrote:\n%s", answer.raw)
	}
	k.reply(f.ID, map[string]any{"sessionId": minted})
	if err := <-done; err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	for _, want := range []string{"_kiro/customAgent/config_error", "_kiro/terminal/shell_type"} {
		select {
		case n := <-got:
			if n.Msg.Method != want {
				t.Errorf("the new lane got %q, want %q in pipe order", n.Msg.Method, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s never reached the new session's lane", want)
		}
	}
}

func TestNewSession_MismatchedIDClosesTheReturnedSessionAndFails(t *testing.T) {
	var reaped []marotte.SessionID
	reapedCh := make(chan struct{})
	k := newFakeKAS(t)
	k.e.reapSession = func(id marotte.SessionID) {
		reaped = append(reaped, id)
		close(reapedCh)
	}
	done := make(chan error, 1)
	go func() {
		_, err := k.e.NewSession(t.Context(), &marotte.SessionOpts{}, nil)
		done <- err
	}()
	f := k.nextMethod(methodSessionNew)
	minted := f.overrideID()
	const kasMinted = "sess_00000000-0000-4000-8000-00000000000b"
	k.reply(f.ID, map[string]any{"sessionId": kasMinted})
	if err := <-done; !errors.Is(err, marotte.ErrSessionMetaRefused) {
		t.Fatalf("NewSession with a reply naming another id = %v, want ErrSessionMetaRefused", err)
	}
	closeFrame := k.nextMethod(methodSessionClose)
	if got := closeFrame.param(marotte.KeySessionID); got != kasMinted {
		t.Errorf("session/close went to %q, want the id KAS created, %q", got, kasMinted)
	}
	k.reply(closeFrame.ID, map[string]any{})
	<-reapedCh
	if len(reaped) != 1 || reaped[0] != kasMinted {
		t.Errorf("reaped %v, want the id KAS created", reaped)
	}
	k.e.lanesMu.Lock()
	_, still := k.e.sessions[minted]
	k.e.lanesMu.Unlock()
	if still {
		t.Error("the minted id's lane stayed registered after the door failed")
	}
}

func TestRoute_FramesReachTheirSessionLaneInPipeOrder(t *testing.T) {
	k := newFakeKAS(t)
	a, b := k.openedSession(nil), k.openedSession(nil)
	for i := range 20 {
		for _, s := range []*Session{a, b} {
			k.notify(marotte.MethodSessionUpdate, map[string]any{"sessionId": s.SessionID(), "n": i})
		}
	}
	for _, s := range []*Session{a, b} {
		got := drain(s.NotifCh(), 300*time.Millisecond)
		if len(got) != 20 {
			t.Fatalf("session %s got %d frames, want its 20", s.SessionID(), len(got))
		}
		for i, n := range got {
			var p struct {
				N int `json:"n"`
			}
			if json.Unmarshal(n.Msg.Params, &p) != nil || p.N != i || n.Seq != uint64(i+1) {
				t.Errorf("session %s frame %d = %s at position %d, want n=%d at position %d", s.SessionID(), i, n.Msg.Params, n.Seq, i, i+1)
			}
		}
	}
}

// TestRoute_ResponseSeqIsTheLanesPosition pins that a reply's position counts the caller's lane, not every frame
// the engine read: positions are comparable within one lane only.
func TestRoute_ResponseSeqIsTheLanesPosition(t *testing.T) {
	k := newFakeKAS(t)
	mine, other := k.openedSession(nil), k.openedSession(nil)
	type result struct {
		seq uint64
		err error
	}
	done := make(chan result, 1)
	go func() {
		_, seq, err := mine.PromptAt(t.Context(), map[string]any{"sessionId": mine.SessionID()}, nil)
		done <- result{seq, err}
	}()
	prompt := k.nextMethod(marotte.MethodPrompt)
	for range 3 {
		k.notify(marotte.MethodSessionUpdate, map[string]any{"sessionId": mine.SessionID()})
	}
	for range 5 {
		k.notify(marotte.MethodSessionUpdate, map[string]any{"sessionId": other.SessionID()})
	}
	k.reply(prompt.ID, map[string]any{"stopReason": "end_turn"})
	got := <-done
	if got.err != nil {
		t.Fatalf("CallAt: %v", got.err)
	}
	if got.seq != 3 {
		t.Errorf("reply position = %d, want 3: the frames on the caller's own lane, not the 8 the engine read", got.seq)
	}
}
