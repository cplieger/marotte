package push

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/liveness"
	"github.com/cplieger/marotte/internal/marotte"
)

// fastBeat shrinks the ledger's beat so a held notice is observed in milliseconds. Serial: deferPoll is a package
// var.
func fastBeat(t *testing.T) {
	t.Helper()
	prev := deferPoll
	deferPoll = 5 * time.Millisecond
	t.Cleanup(func() { deferPoll = prev })
}

// payloadHandler counts arrivals and answers 201.
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

// waitFor polls cond until it holds or the deadline passes, failing closed with the diagnostic.
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

// presentOnly is a filtered service whose one subscription is the present device's, connected with no page.
func presentOnly(t *testing.T) (*Service, *presenceClock, *payloadHandler) {
	t.Helper()
	fastBeat(t)
	h := &payloadHandler{}
	s, clock := filteredService(t, h)
	s.unsubscribe(goneEP)
	s.presence.Observe(connected(tagOf(presentEP)))
	return s, clock, h
}

func TestLedger_APresentProfileWithNoPageIsHeld(t *testing.T) {
	s, _, h := presentOnly(t)

	send(t, s, &marotte.NotificationPayload{PushSubject: marotte.ChatSubject("c1"), Kind: marotte.PushKindPermission, Title: "title", Body: "body", Ask: "a1"})

	if n := len(s.heldNotices()); n != 1 {
		t.Errorf("held notices = %d, want 1", n)
	}
	if h.count() != 0 {
		t.Error("a held notice was pushed while the profile still read present")
	}
}

func TestLedger_HeldNoticeLandsWhenTheProfileFlipsToGone(t *testing.T) {
	s, clock, h := presentOnly(t)

	send(t, s, &marotte.NotificationPayload{PushSubject: marotte.ChatSubject("c1"), Kind: marotte.PushKindPermission, Title: "title", Body: "body", Ask: "a1"})
	time.Sleep(4 * deferPoll)
	if h.count() != 0 {
		t.Fatal("a held notice was pushed while the profile still read present")
	}

	clock.Advance(liveness.AliveWindow + time.Millisecond)
	waitFor(t, "the held push", func() bool { return h.count() == 1 })
	if n := len(s.heldNotices()); n != 0 {
		t.Errorf("held notices after the push = %d, want 0", n)
	}
}

func TestLedger_HeldNoticeIsDroppedAtItsTTL(t *testing.T) {
	s, clock, h := presentOnly(t)

	send(t, s, &marotte.NotificationPayload{PushSubject: marotte.ChatSubject("c1"), Kind: marotte.PushKindPermission, Title: "title", Body: "body", Ask: "a1"})
	// Past the permission TTL the profile also reads gone; the TTL is judged first.
	clock.Advance(ttlPermission + time.Millisecond)
	waitFor(t, "the ledger to empty", func() bool { return len(s.heldNotices()) == 0 })
	time.Sleep(4 * deferPoll)
	if h.count() != 0 {
		t.Error("a notice past its TTL was pushed")
	}
}

func TestLedger_SettleBeforeTheFlipPushesNothing(t *testing.T) {
	s, clock, h := presentOnly(t)

	send(t, s, &marotte.NotificationPayload{PushSubject: marotte.ChatSubject("c1"), Kind: marotte.PushKindPermission, Title: "title", Body: "body", Ask: "a1"})
	s.Settled(marotte.ChatSubject("c1"), "a1")
	if n := len(s.heldNotices()); n != 0 {
		t.Fatalf("held notices after the settle = %d, want 0", n)
	}
	clock.Advance(liveness.AliveWindow + time.Millisecond)
	time.Sleep(4 * deferPoll)
	if h.count() != 0 {
		t.Error("a settled ask was pushed")
	}
}

// A settle names the ask it ends: another ask about the same chat, raised inside the debounce or after it, and a
// notice of another kind stay held, and the profile gets one push per kind when it goes.
func TestLedger_SettlingAnAskLeavesOtherWorkHeld(t *testing.T) {
	for _, tc := range []struct {
		name     string
		answered string
		debounce bool
	}{
		{name: "the earlier ask answered, the later one past the debounce", answered: "1", debounce: true},
		{name: "the earlier ask answered, the later one inside the debounce", answered: "1"},
		{name: "the later ask answered first", answered: "2", debounce: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, clock, h := presentOnly(t)
			ask := func(id string) *marotte.NotificationPayload {
				return &marotte.NotificationPayload{
					PushSubject: marotte.ChatSubject("c1"), Kind: marotte.PushKindPermission, Title: "ask " + id, Body: "body", Ask: id,
				}
			}

			send(t, s, ask("1"))
			if tc.debounce {
				resetDebounce(s)
			}
			send(t, s, ask("2"))
			send(t, s, &marotte.NotificationPayload{
				PushSubject: marotte.ChatSubject("c1"), Kind: marotte.PushKindAgentFinished, Title: "done", Body: "body",
			})
			s.Settled(marotte.ChatSubject("c1"), tc.answered)

			if n := len(s.heldNotices()); n != 2 {
				t.Fatalf("held notices after an answer = %d, want the other ask's and the turn's", n)
			}
			clock.Advance(liveness.AliveWindow + time.Millisecond)
			waitFor(t, "both held pushes", func() bool { return h.count() == 2 })
			time.Sleep(4 * deferPoll)
			if n := h.count(); n != 2 {
				t.Errorf("pushes = %d, want one per held kind", n)
			}
		})
	}
}

// Two asks are two records even inside the debounce, so settling the earlier leaves the later one's payload held.
func TestLedger_ADebouncedAskIsItsOwnRecord(t *testing.T) {
	s, _, _ := presentOnly(t)
	for _, id := range []string{"1", "2"} {
		send(t, s, &marotte.NotificationPayload{
			PushSubject: marotte.ChatSubject("c1"), Kind: marotte.PushKindPermission, Title: "ask " + id, Body: "body", Ask: id,
		})
	}
	s.Settled(marotte.ChatSubject("c1"), "1")

	held := s.heldNotices()
	if len(held) != 1 {
		t.Fatalf("held notices = %d, want the second ask's alone", len(held))
	}
	if got := heldTitle(t, held[0]); got != "ask 2" {
		t.Errorf("held title = %q, want the second ask's", got)
	}
}

// The push body IS the notice: the service worker shows the title, body, subject and id the page would, with
// nothing re-derived on the way.
func TestRaise_ThePushBodyIsTheNotice(t *testing.T) {
	s, _, _ := presentOnly(t)

	want := marotte.NotificationPayload{
		PushSubject: marotte.ChatSubject("c1"), Kind: marotte.PushKindAgentFinished,
		Title: "Fix the parser", Body: "Response complete · Parser fixed",
	}
	send(t, s, &want)
	held := s.heldNotices()
	if len(held) != 1 {
		t.Fatalf("held notices = %d, want 1", len(held))
	}
	var got marotte.NotificationPayload
	if err := json.Unmarshal(held[0].payload, &got); err != nil {
		t.Fatalf("held payload is not the notice: %v", err)
	}
	if len(got.ID) != 22 || strings.Trim(got.ID, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_") != "" {
		t.Errorf("id = %q, want 22 base64url characters", got.ID)
	}
	want.ID = got.ID
	if !reflect.DeepEqual(got, want) {
		t.Errorf("push body = %+v, want %+v", got, want)
	}
}

// One undelivered record per (tag, kind, subject) for a notice announcing no ask: a second one replaces it, so what
// lands is the latest and the ledger cannot grow past the subscription count per kind and subject.
func TestLedger_ASecondNoticeReplacesTheHeldOne(t *testing.T) {
	s, clock, h := presentOnly(t)

	send(t, s, &marotte.NotificationPayload{PushSubject: marotte.ChatSubject("c1"), Kind: marotte.PushKindPermission, Title: "first", Body: "body"})
	resetDebounce(s)
	send(t, s, &marotte.NotificationPayload{PushSubject: marotte.ChatSubject("c1"), Kind: marotte.PushKindPermission, Title: "second", Body: "body"})
	held := s.heldNotices()
	if len(held) != 1 {
		t.Fatalf("held notices after two on one key = %d, want 1", len(held))
	}
	if got := heldTitle(t, held[0]); got != "second" {
		t.Errorf("held title = %q, want the later notice's", got)
	}

	clock.Advance(liveness.AliveWindow + time.Millisecond)
	waitFor(t, "the held push", func() bool { return h.count() == 1 })
}

// The beat stops with the service: a notice held at Close is never pushed.
func TestLedger_CloseStopsTheBeat(t *testing.T) {
	s, clock, h := presentOnly(t)
	send(t, s, &marotte.NotificationPayload{PushSubject: marotte.ChatSubject("c1"), Kind: marotte.PushKindAgentFinished, Title: "t", Body: "b"})

	s.Close()
	clock.Advance(liveness.AliveWindow + time.Millisecond)
	time.Sleep(4 * deferPoll)

	if h.count() != 0 {
		t.Error("a held notice was pushed after Close")
	}
}

func heldTitle(t *testing.T, rec noticeRecord) string {
	t.Helper()
	var p marotte.NotificationPayload
	if err := json.Unmarshal(rec.payload, &p); err != nil {
		t.Fatalf("held payload is not the notice: %v", err)
	}
	return p.Title
}

// heldNotices is the ledger's undelivered records, oldest first.
func (s *Service) heldNotices() []noticeRecord {
	s.ledger.mu.Lock()
	defer s.ledger.mu.Unlock()
	out := make([]noticeRecord, 0, len(s.ledger.records))
	for _, rec := range s.ledger.records {
		out = append(out, *rec)
	}
	slices.SortFunc(out, func(a, b noticeRecord) int { return int(a.seq) - int(b.seq) })
	return out
}
