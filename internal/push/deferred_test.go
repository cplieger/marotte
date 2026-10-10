package push

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/liveness"
	"github.com/cplieger/marotte/internal/marotte"
)

// deferredOn holds the switch on for one test, whatever its default, and shrinks
// the re-judge poll so a held delivery is observed in milliseconds. Serial: both
// are package vars.
func deferredOn(t *testing.T) {
	t.Helper()
	prevOn, prevPoll := deferSuppressedSends, deferPoll
	deferSuppressedSends, deferPoll = true, 5*time.Millisecond
	t.Cleanup(func() { deferSuppressedSends, deferPoll = prevOn, prevPoll })
}

// deferredOff holds the switch off for one test: the plain drop.
func deferredOff(t *testing.T) {
	t.Helper()
	prev := deferSuppressedSends
	deferSuppressedSends = false
	t.Cleanup(func() { deferSuppressedSends = prev })
}

// payloadHandler records the decrypted-side envelope size is not observable, so it
// records arrivals and the request count; the payload identity rides the title
// through a plaintext side channel the test does not have. It answers 201.
type payloadHandler struct {
	mu   sync.Mutex
	hits int
}

func (h *payloadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	h.mu.Lock()
	h.hits++
	h.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
}

func (h *payloadHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hits
}

// waitFor polls cond until it holds or the deadline passes, failing closed with
// the diagnostic.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// As shipped, a suppressed send is held: the hold is the default, not a test mode.
func TestDeferred_DefaultHoldsASuppressedSend(t *testing.T) {
	h := &payloadHandler{}
	s, _ := filteredService(t, h)
	s.unsubscribe(goneEP)
	s.presence.Observe(connected(tagOf(presentEP)))

	s.Send(t.Context(), &marotte.NotificationPayload{PushSubject: marotte.ChatSubject("c1"), Kind: marotte.PushKindPermission, Title: "title", Body: "body"})

	if n := s.heldCount(); n != 1 {
		t.Errorf("held deliveries with the default switch = %d, want 1", n)
	}
	if h.count() != 0 {
		t.Error("a held delivery landed while the profile still read present")
	}
}

// With the switch off a suppressed send holds nothing: the plain drop.
func TestDeferred_OffHoldsNothing(t *testing.T) {
	deferredOff(t)
	h := &payloadHandler{}
	s, _ := filteredService(t, h)
	s.unsubscribe(goneEP)
	s.presence.Observe(connected(tagOf(presentEP)))

	s.Send(t.Context(), &marotte.NotificationPayload{PushSubject: marotte.ChatSubject("c1"), Kind: marotte.PushKindPermission, Title: "title", Body: "body"})

	if n := s.heldCount(); n != 0 {
		t.Errorf("held deliveries with the switch off = %d, want 0", n)
	}
}

func TestDeferred_HeldDeliveryLandsWhenTheProfileFlipsToGone(t *testing.T) {
	deferredOn(t)
	h := &payloadHandler{}
	s, clock := filteredService(t, h)
	s.unsubscribe(goneEP)
	s.presence.Observe(connected(tagOf(presentEP)))

	s.Send(t.Context(), &marotte.NotificationPayload{PushSubject: marotte.ChatSubject("c1"), Kind: marotte.PushKindPermission, Title: "title", Body: "body"})
	if n := s.heldCount(); n != 1 {
		t.Fatalf("held deliveries = %d, want 1", n)
	}
	time.Sleep(4 * deferPoll)
	if h.count() != 0 {
		t.Fatal("a held delivery landed while the profile still read present")
	}

	clock.Advance(liveness.AliveWindow + time.Millisecond)
	waitFor(t, "the held delivery", func() bool { return h.count() == 1 })
	if n := s.heldCount(); n != 0 {
		t.Errorf("held deliveries after the release = %d, want 0", n)
	}
}

func TestDeferred_HeldDeliveryIsDroppedAtItsTTL(t *testing.T) {
	deferredOn(t)
	h := &payloadHandler{}
	s, clock := filteredService(t, h)
	s.unsubscribe(goneEP)
	s.presence.Observe(connected(tagOf(presentEP)))

	s.Send(t.Context(), &marotte.NotificationPayload{PushSubject: marotte.ChatSubject("c1"), Kind: marotte.PushKindPermission, Title: "title", Body: "body"})
	// Past the permission TTL the profile also reads gone; the TTL is judged first.
	clock.Advance(ttlPermission + time.Millisecond)
	waitFor(t, "the held set to empty", func() bool { return s.heldCount() == 0 })
	time.Sleep(4 * deferPoll)
	if h.count() != 0 {
		t.Error("a delivery past its TTL was sent")
	}
}

func TestDeferred_RetractionBeforeTheFlipDeliversNothing(t *testing.T) {
	deferredOn(t)
	h := &payloadHandler{}
	s, clock := filteredService(t, h)
	s.unsubscribe(goneEP)
	s.presence.Observe(connected(tagOf(presentEP)))

	s.Send(t.Context(), &marotte.NotificationPayload{PushSubject: marotte.ChatSubject("c1"), Kind: marotte.PushKindPermission, Title: "title", Body: "body"})
	s.Retract(marotte.ChatSubject("c1"))
	if n := s.heldCount(); n != 0 {
		t.Fatalf("held deliveries after the retraction = %d, want 0", n)
	}
	clock.Advance(liveness.AliveWindow + time.Millisecond)
	time.Sleep(4 * deferPoll)
	if h.count() != 0 {
		t.Error("a retracted delivery was sent")
	}
}

// The envelope IS the notification: the service worker shows the title, body and subject the
// page would, with nothing re-derived on the way.
func TestSend_TheEnvelopeIsTheNotification(t *testing.T) {
	deferredOn(t)
	h := &payloadHandler{}
	s, _ := filteredService(t, h)
	s.unsubscribe(goneEP)
	s.presence.Observe(connected(tagOf(presentEP)))

	want := marotte.NotificationPayload{
		PushSubject: marotte.ChatSubject("c1"), Kind: marotte.PushKindAgentFinished,
		Title: "Fix the parser", Body: "Response complete · Parser fixed",
	}
	s.Send(t.Context(), &want)
	key := heldKey{tag: tagOf(presentEP), kind: marotte.PushKindAgentFinished, subject: "c1"}
	s.deferred.mu.Lock()
	held := s.deferred.held[key]
	s.deferred.mu.Unlock()
	var got marotte.NotificationPayload
	if err := json.Unmarshal(held.payload, &got); err != nil {
		t.Fatalf("held payload is not the envelope: %v", err)
	}
	if got != want {
		t.Errorf("envelope = %+v, want %+v", got, want)
	}
}

// One entry per (tag, kind, subject): a second event replaces the held payload, so
// what lands is the latest, and the queue cannot grow past the subscription count
// per kind and subject.
func TestDeferred_ASecondEventReplacesTheHeldPayload(t *testing.T) {
	deferredOn(t)
	h := &payloadHandler{}
	s, clock := filteredService(t, h)
	s.unsubscribe(goneEP)
	s.presence.Observe(connected(tagOf(presentEP)))

	s.Send(t.Context(), &marotte.NotificationPayload{PushSubject: marotte.ChatSubject("c1"), Kind: marotte.PushKindPermission, Title: "first", Body: "body"})
	resetDebounce(s)
	s.Send(t.Context(), &marotte.NotificationPayload{PushSubject: marotte.ChatSubject("c1"), Kind: marotte.PushKindPermission, Title: "second", Body: "body"})
	if n := s.heldCount(); n != 1 {
		t.Fatalf("held deliveries after two events on one key = %d, want 1", n)
	}
	key := heldKey{tag: tagOf(presentEP), kind: marotte.PushKindPermission, subject: "c1"}
	s.deferred.mu.Lock()
	held := s.deferred.held[key]
	s.deferred.mu.Unlock()
	var p marotte.NotificationPayload
	if err := json.Unmarshal(held.payload, &p); err != nil {
		t.Fatalf("held payload is not the envelope: %v", err)
	}
	if p.Title != "second" {
		t.Errorf("held title = %q, want the later event's", p.Title)
	}

	clock.Advance(liveness.AliveWindow + time.Millisecond)
	waitFor(t, "the held delivery", func() bool { return h.count() == 1 })
}

// heldCount is how many pushes the service holds back.
func (s *Service) heldCount() int {
	s.deferred.mu.Lock()
	defer s.deferred.mu.Unlock()
	return len(s.deferred.held)
}
