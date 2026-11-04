package bridge

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
)

// releaseRetry are the delays between custody's re-sends of a released session's close; the last one repeats. Each
// send re-runs kiro-cli's quiesce, which waits for the session's admitted calls. A var for tests.
var releaseRetry = []time.Duration{30 * time.Second, 2 * time.Minute, 5 * time.Minute}

// releaseLeakAfter is how long a released session, or an abandoned door's kept handle, may stay unconfirmed before it
// is reported held. A var for tests.
var releaseLeakAfter = 10 * time.Minute

const (
	heldMsgText     = "a closed session is still held by the agent engine; restarting the agent engine from Settings releases it"
	releasedMsgText = "a held session was released"
)

// Release lets go of the session for a caller that holds nothing afterwards, and returns at once. From the call the
// lane is closed, so the session's consumer gets what was queued and its channel ends, and every later frame for the
// id reaches the engine lane. The session never reopens: custody re-sends session/close (releaseRetry) until kiro-cli
// confirms it or the engine ends, and reports it held once after releaseLeakAfter. A session still opening is released
// by its door once kiro-cli answers; a holder's Close under way hands it to custody when its send settles unconfirmed.
func (s *Session) Release() {
	s.mu.Lock()
	switch {
	case s.state == stateClosed, s.released:
		s.mu.Unlock()
		return
	case s.state == stateOpening:
		s.closeAsked = true
		s.released = true
		s.mu.Unlock()
		return
	}
	holderSending := s.state == stateClosing
	s.state = stateClosing
	s.released = true
	s.mu.Unlock()
	s.lane.close()
	if !holderSending {
		s.startCustody(false)
	}
}

// Released reports that no caller holds the session any more: Release ran, or a holder's Close gave up on it.
func (s *Session) Released() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.released
}

// releaseNow releases a session the door itself lets go of, reaping its files once settled when reap is set.
func (s *Session) releaseNow(reap bool) {
	s.mu.Lock()
	s.state = stateClosing
	s.released = true
	s.reapOnSettle = reap
	s.mu.Unlock()
	s.lane.close()
	s.startCustody(false)
}

// startCustody arms the held clock and runs custody; sent means a close for this release is already on the wire.
func (s *Session) startCustody(sent bool) {
	s.armHeldClock("no reply")
	s.engine.goBackground(func() { s.custody(sent) })
}

// custody re-sends session/close until a result settles the session, the engine ends or the lifetime does.
func (s *Session) custody(sent bool) {
	sends := 0
	if sent {
		sends = 1
	}
	for {
		if sends > 0 && !s.awaitRetry(sends) {
			return
		}
		if s.sendRelease() {
			return
		}
		sends++
	}
}

// awaitRetry waits out the delay after the sends-th send; false means the session settled or the lifetime ended.
func (s *Session) awaitRetry(sends int) bool {
	var fire <-chan time.Time
	if len(releaseRetry) > 0 {
		timer := time.NewTimer(releaseRetry[min(sends, len(releaseRetry))-1])
		defer timer.Stop()
		fire = timer.C
	}
	select {
	case <-fire:
		return true
	case <-s.closeDone:
	case <-s.engine.lifecycleCtx.Done():
	}
	return false
}

// sendRelease writes one session/close for a released session; true means custody is over.
func (s *Session) sendRelease() bool {
	cctx, cancel := context.WithTimeout(s.engine.lifecycleCtx, closeBudget)
	defer cancel()
	_, _, err := s.engine.callAt(cctx, s.lane, s.id, methodSessionClose,
		map[string]any{marotte.KeySessionID: s.id}, s.lateRelease)
	switch {
	case err == nil:
		s.settle()
		return true
	case errors.Is(err, errBridgeExited), errors.Is(err, errWriteFenced), errors.Is(err, context.Canceled):
		// The engine's end settles the session (engineEnded).
		return true
	default:
		s.noteMiss(err)
	}
	slog.Debug("a released session's close is not confirmed yet; it is sent again",
		"session_id", s.id, "role", s.role, "error", err)
	return false
}

// lateRelease settles a session whose close result came after its budget; a late refusal leaves custody to re-send.
func (s *Session) lateRelease(resp *marotte.RPCResponse) {
	if resp.Error == nil {
		s.settle()
	}
}

// noteMiss records custody's latest miss for the held report.
func (s *Session) noteMiss(err error) {
	text := err.Error()
	if errors.Is(err, context.DeadlineExceeded) {
		text = "no reply"
	}
	s.mu.Lock()
	s.lastErr = text
	s.mu.Unlock()
}

// settle ends a released session kiro-cli confirmed: it leaves the table, a reported one logs its release, and an
// abandoned session/new is reaped.
func (s *Session) settle() {
	s.mu.Lock()
	reported, reap := s.reported, s.reapOnSettle
	s.mu.Unlock()
	if !s.closed() {
		return
	}
	if reported {
		slog.Info(releasedMsgText, "session_id", s.id, "role", s.role)
	}
	if reap && s.engine.reapSession != nil {
		s.engine.reapSession(s.id)
	}
}

// armHeldClock starts the held report's clock unless one runs or the handle already reported; noReply is the
// last_error the report names when no send was answered.
func (s *Session) armHeldClock(noReply string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.heldTimer != nil || s.reported || s.state == stateClosed {
		return
	}
	if s.lastErr == "" {
		s.lastErr = noReply
	}
	s.heldGen++
	gen := s.heldGen
	s.heldTimer = time.AfterFunc(releaseLeakAfter, func() { s.declareHeld(gen) })
}

// stopHeldClockLocked stops the clock; a callback already running sees the generation move and does nothing. Caller
// holds s.mu.
func (s *Session) stopHeldClockLocked() {
	if s.heldTimer == nil {
		return
	}
	s.heldTimer.Stop()
	s.heldTimer = nil
	s.heldGen++
}

// declareHeld reports a session kiro-cli still has not confirmed, once per handle, and fails the doors waiting on it.
func (s *Session) declareHeld(gen uint64) {
	s.mu.Lock()
	if gen != s.heldGen || s.reported || s.state == stateClosed {
		s.mu.Unlock()
		return
	}
	s.reported = true
	s.heldTimer = nil
	lastErr := s.lastErr
	s.mu.Unlock()
	slog.Error(heldMsgText, "session_id", s.id, "role", s.role, "last_error", logsafe.Field(lastErr))
	close(s.held)
}
