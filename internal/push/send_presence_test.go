package push

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/liveness"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/slogx/capture"
)

const (
	presentEP = "https://fcm.googleapis.com/fcm/send/present-device"
	goneEP    = "https://fcm.googleapis.com/fcm/send/gone-device"
)

// filteredService is a Service on the in-memory test server with a presence table
// on a fake clock, holding a subscription for each of the two endpoints above.
func filteredService(t *testing.T, h http.Handler) (*Service, *presenceClock) {
	t.Helper()
	clock := &presenceClock{at: time.Unix(1_700_000_000, 0)}
	presence := newPresenceAt(clock)
	srv := httptest.NewTestServer(t, h)
	client := srv.Client()
	s := New(context.Background(), t.TempDir(), testSubject, WithPresence(presence))
	t.Cleanup(s.Close)
	s.client = client
	s.Subscribe(pushSubscriptionWithValidKeys(t, presentEP))
	s.Subscribe(pushSubscriptionWithValidKeys(t, goneEP))
	return s, clock
}

// resetDebounce lets a second Send about the same subject through the 5 s window
// so a test can observe two consecutive decisions.
func resetDebounce(s *Service) {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.lastPush)
}

func TestSend_SkipsAPresentProfileAndPushesTheGoneOne(t *testing.T) {
	h := &recordingHandler{}
	s, _ := filteredService(t, h)
	s.presence.Observe(connected(TagOf(presentEP)))
	capLog := capture.Default(t)

	s.Send(t.Context(), "title", "body", marotte.PushKindPermission, marotte.ChatSubject("c1"), "")

	got := h.snapshot()
	if len(got) != 1 || !strings.HasSuffix(goneEP, got[0].path) {
		t.Fatalf("deliveries = %+v, want exactly one, to the gone device", got)
	}
	if n := s.Suppressed(marotte.PushKindPermission); n != 1 {
		t.Errorf("Suppressed(permission) = %d, want 1", n)
	}
	if n := capLog.CountExact("push: delivered"); n != 1 {
		t.Errorf("Info %q lines = %d, want exactly one", "push: delivered", n)
	}
	if tag, _ := capLog.AttrValue("push: delivered", "tag"); tag != TagOf(goneEP) {
		t.Errorf("delivered line tag = %q, want %q", tag, TagOf(goneEP))
	}
	if tag, _ := capLog.AttrValue("push: suppressed, profile present", "tag"); tag != TagOf(presentEP) {
		t.Errorf("suppressed line tag = %q, want %q", tag, TagOf(presentEP))
	}
	if kind, _ := capLog.AttrValue("push: suppressed, profile present", "kind"); kind != string(marotte.PushKindPermission) {
		t.Errorf("suppressed line kind = %q, want %q", kind, marotte.PushKindPermission)
	}
	for _, r := range capLog.Records() {
		r.Attrs(func(a slog.Attr) bool {
			if strings.Contains(a.Value.String(), "fcm.googleapis.com") {
				t.Errorf("log attr %s=%q names an endpoint", a.Key, a.Value.String())
			}
			return true
		})
	}
}

func TestSend_EveryProfilePresentSendsNothing(t *testing.T) {
	h := &recordingHandler{}
	s, _ := filteredService(t, h)
	s.presence.Observe(connected(TagOf(presentEP)))
	s.presence.Observe(connected(TagOf(goneEP)))

	s.Send(t.Context(), "title", "body", marotte.PushKindAgentFinished, marotte.ChatSubject("c1"), "")

	if got := h.snapshot(); len(got) != 0 {
		t.Errorf("deliveries = %+v, want none", got)
	}
	if n := s.Suppressed(marotte.PushKindAgentFinished); n != 2 {
		t.Errorf("Suppressed(agent_finished) = %d, want 2", n)
	}
	if !s.HasSubscribers() {
		t.Error("a suppressed send pruned the subscriptions")
	}
}

// With the hold off, suppression is a drop: a profile that flips to gone afterwards is
// not sent the event it missed; the next event is.
func TestSend_SuppressionIsADropNotADelay(t *testing.T) {
	deferredOff(t)
	h := &recordingHandler{}
	s, clock := filteredService(t, h)
	s.Unsubscribe(goneEP)
	s.presence.Observe(connected(TagOf(presentEP)))

	s.Send(t.Context(), "first", "body", marotte.PushKindPermission, marotte.ChatSubject("c1"), "")
	if got := h.snapshot(); len(got) != 0 {
		t.Fatalf("deliveries while present = %+v, want none", got)
	}

	clock.Advance(liveness.AliveWindow + time.Millisecond)
	if !s.presence.Gone(TagOf(presentEP)) {
		t.Fatal("the profile did not read gone after the window; the fixture is wrong")
	}
	if got := h.snapshot(); len(got) != 0 {
		t.Fatalf("deliveries after the flip with no new event = %+v, want none", got)
	}

	resetDebounce(s)
	s.Send(t.Context(), "second", "body", marotte.PushKindPermission, marotte.ChatSubject("c1"), "")
	if got := h.snapshot(); len(got) != 1 {
		t.Errorf("deliveries after the flip and a new event = %+v, want exactly one", got)
	}
}

// The PR poller calls Send directly; the filter sits inside Send. The kind is switched
// on first because it defaults off and the preference gate would drop it earlier.
func TestSend_FiltersThePollersKindToo(t *testing.T) {
	h := &recordingHandler{}
	s, _ := filteredService(t, h)
	s.SetPreferences(map[marotte.PushKind]bool{marotte.PushKindPRStatus: true})
	s.presence.Observe(connected(TagOf(presentEP)))

	s.Send(t.Context(), "checks", "green", marotte.PushKindPRStatus, marotte.PushSubject{Key: "pr:x"}, "")

	got := h.snapshot()
	if len(got) != 1 || !strings.HasSuffix(goneEP, got[0].path) {
		t.Errorf("deliveries = %+v, want exactly one, to the gone device", got)
	}
	if n := s.Suppressed(marotte.PushKindPRStatus); n != 1 {
		t.Errorf("Suppressed(pr_status) = %d, want 1", n)
	}
}

// Without a table every subscription is pushed: the fail-open direction.
func TestSend_WithoutPresenceSendsToEveryone(t *testing.T) {
	h := &recordingHandler{}
	s, _ := newServiceOnTestServer(t, h)
	s.Subscribe(pushSubscriptionWithValidKeys(t, presentEP))
	s.Subscribe(pushSubscriptionWithValidKeys(t, goneEP))

	s.Send(t.Context(), "title", "body", marotte.PushKindPermission, marotte.ChatSubject("c1"), "")

	if got := h.snapshot(); len(got) != 2 {
		t.Errorf("deliveries = %+v, want both", got)
	}
	if n := s.Suppressed(marotte.PushKindPermission); n != 0 {
		t.Errorf("Suppressed(permission) with no table = %d, want 0", n)
	}
}
