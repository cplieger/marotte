package bridge

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/slogx/capture"
)

// shrinkLanes lowers the lane bounds for one test. Not parallel.
func shrinkLanes(t *testing.T, warn, hard int) {
	t.Helper()
	origWarn, origHard := laneWarnAt, laneHardCap
	laneWarnAt, laneHardCap = warn, hard
	t.Cleanup(func() { laneWarnAt, laneHardCap = origWarn, origHard })
}

// TestLane_SlowConsumerDoesNotStallCoTenants: a session whose consumer reads nothing never blocks the read loop, so a
// co-tenant still gets its frames.
func TestLane_SlowConsumerDoesNotStallCoTenants(t *testing.T) {
	k := newFakeKAS(t)
	stuck, live := k.openedSession(nil), k.openedSession(nil)
	for range 200 {
		k.notify(marotte.MethodSessionUpdate, map[string]any{"sessionId": stuck.SessionID()})
	}
	k.notify(marotte.MethodSessionUpdate, map[string]any{"sessionId": live.SessionID(), "mark": true})
	select {
	case n := <-live.NotifCh():
		if n.Msg.Method != marotte.MethodSessionUpdate {
			t.Errorf("the co-tenant got %q", n.Msg.Method)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a session nobody reads stalled its co-tenant's frames")
	}
}

// overflowedRequests sends a session's lane past the hard cap with two requests queued behind the boundary, and
// returns what the engine wrote for each request id.
func overflowedRequests(t *testing.T, k *fakeKAS, s *Session, settle time.Duration) map[int64][]wireFrame {
	t.Helper()
	k.request(501, marotte.MethodRequestPermission, map[string]any{"sessionId": s.SessionID()})
	k.request(502, "fs/read_text_file", map[string]any{"sessionId": s.SessionID(), "path": "/x"})
	for range laneHardCap + 1 {
		k.notify(marotte.MethodSessionUpdate, map[string]any{"sessionId": s.SessionID()})
	}
	answers := map[int64][]wireFrame{}
	deadline := time.After(3 * time.Second)
	for len(answers[501])+len(answers[502]) < 2 {
		select {
		case f := <-k.frames:
			if f.ID != nil && f.Method == "" {
				answers[*f.ID] = append(answers[*f.ID], f)
			}
		case <-deadline:
			t.Fatalf("the overflowed session's queued requests were not both answered: %v", answers)
		}
	}
	// A second answer to either would arrive by now.
	if f, quiet := k.quiet(settle); !quiet && f.ID != nil && f.Method == "" {
		answers[*f.ID] = append(answers[*f.ID], f)
	}
	return answers
}

// overflowAnswers is the detach handler the engine lane gives a session: the unknown-session rows.
func overflowAnswers(e **Engine) Option {
	return WithDetachHandler(func(d DetachedSession) {
		for _, req := range d.Requests {
			if req.Method == marotte.MethodRequestPermission {
				_ = (*e).Respond(context.Background(), *req.ID, marotte.PermissionOutcomeCancelled(), nil)
				continue
			}
			_ = (*e).Respond(context.Background(), *req.ID, nil, &marotte.RPCError{Code: marotte.RPCCodeInternal, Message: "dropped"})
		}
	})
}

func TestSessionLane_OverflowDrainsQueuedRequestsExactlyOnce(t *testing.T) {
	_ = capture.Default(t)
	shrinkLanes(t, 8, 32)
	var e *Engine
	k := newFakeKAS(t, overflowAnswers(&e))
	e = k.e
	s := k.openedSession(nil)
	answers := overflowedRequests(t, k, s, 200*time.Millisecond)
	if len(answers[501]) != 1 || len(answers[502]) != 1 {
		t.Fatalf("answers by id = %d and %d, want exactly one each", len(answers[501]), len(answers[502]))
	}
	if !json.Valid(answers[501][0].Result) || answers[501][0].Error != nil {
		t.Errorf("the permission request got %s, want a cancelled outcome", answers[501][0].raw)
	}
	if answers[502][0].Error == nil {
		t.Errorf("the fs request got %s, want an error", answers[502][0].raw)
	}
}

// TestSessionLane_OverflowRacesThePump runs the overflow while the consumer drains slowly: each request is answered
// exactly once across the consumer and the detach handler, never twice and never not at all.
func TestSessionLane_OverflowRacesThePump(t *testing.T) {
	_ = capture.Default(t)
	shrinkLanes(t, 4, 16)
	for range 1000 {
		var e *Engine
		k := newFakeKAS(t, overflowAnswers(&e))
		e = k.e
		var answered sync.Map
		s := k.openedSession(func(s *Session) {
			go func() {
				for n := range s.NotifCh() {
					if n.Msg.ID != nil {
						answered.Store(*n.Msg.ID, true)
						_ = s.Respond(context.Background(), *n.Msg.ID, map[string]any{}, nil)
					}
					time.Sleep(time.Microsecond)
				}
			}()
		})
		answers := overflowedRequests(t, k, s, 5*time.Millisecond)
		for _, id := range []int64{501, 502} {
			if n := len(answers[id]); n != 1 {
				t.Fatalf("request %d answered %d times, want exactly once", id, n)
			}
		}
	}
}

func TestSessionLane_FramesAfterOverflowReachTheEngineLane(t *testing.T) {
	_ = capture.Default(t)
	shrinkLanes(t, 8, 32)
	k := newFakeKAS(t)
	s := k.openedSession(nil)
	// One more than the cap: the pump holds the first frame outside the queue.
	for range laneHardCap + 2 {
		k.notify(marotte.MethodSessionUpdate, map[string]any{"sessionId": s.SessionID()})
	}
	k.notify("_test/after", map[string]any{"sessionId": s.SessionID()})
	for {
		select {
		case n := <-k.e.EngineNotifCh():
			if n.Msg.Method == "_test/after" {
				return
			}
		case <-time.After(2 * time.Second):
			t.Fatal("a frame naming an overflowed session did not reach the engine lane")
		}
	}
}

// TestLane_OverflowEndsOnlyThatSession: the overflowed session closes; the engine and a co-tenant go on.
func TestLane_OverflowEndsOnlyThatSession(t *testing.T) {
	_ = capture.Default(t)
	shrinkLanes(t, 8, 32)
	k := newFakeKAS(t)
	s, other := k.openedSession(nil), k.openedSession(nil)
	for range laneHardCap + 2 {
		k.notify(marotte.MethodSessionUpdate, map[string]any{"sessionId": s.SessionID()})
	}
	select {
	case <-s.closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("the overflowed session never closed")
	}
	if !other.Open() {
		t.Error("a co-tenant closed when another session's lane overflowed")
	}
	select {
	case <-k.e.Done():
		t.Error("a session lane's overflow ended the engine")
	default:
	}
}

func TestEngineLane_OverflowAnswersARequestOnce(t *testing.T) {
	_ = capture.Default(t)
	shrinkLanes(t, 2, 4)
	k := newFakeKAS(t)
	// Nobody reads the engine lane, so it fills to the cap with requests. Whether the pump already holds one outside
	// the queue is timing, so the last queued request may be past the cap too.
	for i := range laneHardCap + 1 {
		k.request(int64(700+i), "_test/queued", map[string]any{})
	}
	k.request(799, "_test/overflow", map[string]any{})
	answers := map[int64]int{}
	deadline := time.After(2 * time.Second)
	for answers[799] == 0 {
		select {
		case f := <-k.frames:
			if f.ID != nil && f.Method == "" {
				answers[*f.ID]++
				if f.Error == nil || f.Error.Code != marotte.RPCCodeInternal {
					t.Errorf("request %d answered %s, want -32603", *f.ID, f.raw)
				}
			}
		case <-deadline:
			t.Fatal("the request past the engine lane's cap was never answered")
		}
	}
	for {
		f, quiet := k.quiet(200 * time.Millisecond)
		if quiet {
			break
		}
		if f.ID != nil && f.Method == "" {
			answers[*f.ID]++
		}
	}
	for id, n := range answers {
		if n != 1 {
			t.Errorf("request %d answered %d times, want once", id, n)
		}
		if id < int64(700+laneHardCap) && id != 799 {
			t.Errorf("request %d, inside the cap, was answered; it should stay queued", id)
		}
	}
	select {
	case <-k.e.Done():
		t.Error("a request past the engine lane's cap ended the engine; only a notification does")
	default:
	}
}

func TestEngineLane_NotificationOverflowEndsTheEngine(t *testing.T) {
	_ = capture.Default(t)
	shrinkLanes(t, 2, 4)
	k := newFakeKAS(t)
	for range laneHardCap {
		k.notify("_kiro/configuration/state", map[string]any{})
	}
	// The frame past the cap ends the engine and closes its stdout, so a write after it may fail: unchecked.
	frame := []byte(`{"jsonrpc":"2.0","method":"_kiro/configuration/state","params":{}}` + "\n")
	for range 2 {
		k.mu.Lock()
		_, _ = k.outW.Write(frame)
		k.mu.Unlock()
	}
	select {
	case <-k.e.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("a notification past the engine lane's cap did not end the engine")
	}
	if got := k.e.Exit().Reason; got != ExitLaneOverflow {
		t.Errorf("exit reason = %q, want %q", got, ExitLaneOverflow)
	}
}
