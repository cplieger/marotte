package command

import (
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// marotte's notice reaches the session's buffer in the shape KAS files as a warning notification, under a fresh id.
func TestSteerWarningNotice_SendsANotificationShapedSteer(t *testing.T) {
	b := newSteerBridge()
	if err := SteerWarningNotice(t.Context(), b, "the run stopped"); err != nil {
		t.Fatalf("SteerWarningNotice: %v", err)
	}
	calls := b.callsOf(marotte.MethodSessionSteer)
	if len(calls) != 1 {
		t.Fatalf("steer calls = %d, want 1", len(calls))
	}
	p := calls[0].params
	if got := p["message"]; got != "[notification/warning] the run stopped" {
		t.Errorf("message = %q, want the notification prefix before the text", got)
	}
	if !notificationPrefix.MatchString(p["message"].(string)) {
		t.Error("the message does not match KAS's notification pattern")
	}
	if id, _ := p["messageId"].(string); id == "" || strings.HasPrefix(id, marotte.SteerIDPrefix) {
		t.Errorf("messageId = %q, want a fresh id outside the user's steer- space", id)
	}
	if got := p[keySessionID]; got != b.SessionID() {
		t.Errorf("sessionId = %v, want the bridge's", got)
	}
}

// A notice KAS answers unqueued did not reach the buffer, so the caller hears it failed.
func TestSteerWarningNotice_ADroppedNoticeIsAnError(t *testing.T) {
	b := newSteerBridge()
	b.results[marotte.MethodSessionSteer] = map[string]any{"queued": false, "dropped": "epoch_changed"}
	err := SteerWarningNotice(t.Context(), b, "the run stopped")
	if err == nil || !strings.Contains(err.Error(), "epoch_changed") {
		t.Errorf("SteerWarningNotice on a dropped steer = %v, want an error naming the drop", err)
	}
}
