package command

// The ledger is the ONLY evidence anywhere that a mid-turn steer carries the
// user's own words, so what these tests pin is the shape of its answers: total,
// keyed by the id KAS returned, and biased toward the agent whenever it does not
// know.

import (
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

func TestSteerLedger_RecordedIDIsTheUsers(t *testing.T) {
	l := NewSteerLedger()
	l.RecordUserSteer("c1", "steer-m-1", nil)

	if got := l.SteerOrigin("c1", "steer-m-1"); got != marotte.SteerOriginUser {
		t.Errorf("SteerOrigin(recorded) = %q, want %q", got, marotte.SteerOriginUser)
	}
}

// Everything the ledger has not seen is the agent's — a workflow's report, a
// run-completion nudge, a steer another device sent before this process started.
func TestSteerLedger_UnknownIDIsTheAgents(t *testing.T) {
	l := NewSteerLedger()
	l.RecordUserSteer("c1", "steer-m-1", nil)

	for _, tc := range []struct {
		name    string
		chat    marotte.ChatID
		steerID string
	}{
		{"never recorded", "c1", "notify-wf-9"},
		{"recorded for another chat", "c2", "steer-m-1"},
		{"empty id", "c1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := l.SteerOrigin(tc.chat, tc.steerID); got != marotte.SteerOriginAgent {
				t.Errorf("SteerOrigin = %q, want %q", got, marotte.SteerOriginAgent)
			}
		})
	}
}

// An empty id is not recordable either: KAS answered without one, so there is no
// name a later frame could arrive under.
func TestSteerLedger_EmptyIDIsNotRecorded(t *testing.T) {
	l := NewSteerLedger()
	l.RecordUserSteer("c1", "", nil)

	if n := len(l.sent); n != 0 {
		t.Errorf("recorded %d entries for an empty id, want 0", n)
	}
}

func TestSteerLedger_ExpiredIDIsTheAgents(t *testing.T) {
	l := NewSteerLedger()
	now := time.Now()
	l.now = func() time.Time { return now }
	l.RecordUserSteer("c1", "steer-m-1", nil)

	l.now = func() time.Time { return now.Add(steerTTL + time.Second) }
	if got := l.SteerOrigin("c1", "steer-m-1"); got != marotte.SteerOriginAgent {
		t.Errorf("SteerOrigin(expired) = %q, want %q", got, marotte.SteerOriginAgent)
	}
}

// A boundary resend is recorded with the dropped steers it replaces, and the record
// answers them for the steer's whole ledger life: an unknown or expired id has none.
func TestSteerLedger_ResendsFollowTheRecordedSteer(t *testing.T) {
	l := NewSteerLedger()
	now := time.Now()
	l.now = func() time.Time { return now }
	l.RecordUserSteer("c1", "steer-m-2", []string{"steer-m-1"})
	l.RecordUserSteer("c1", "steer-m-3", nil)

	if got, want := l.SteerResends("c1", "steer-m-2"), []string{"steer-m-1"}; !slices.Equal(got, want) {
		t.Errorf("SteerResends(resend) = %v, want %v", got, want)
	}
	if got := l.SteerResends("c1", "steer-m-3"); got != nil {
		t.Errorf("SteerResends(plain steer) = %v, want nil", got)
	}
	if got := l.SteerResends("c1", "steer-m-9"); got != nil {
		t.Errorf("SteerResends(unknown) = %v, want nil", got)
	}
	l.now = func() time.Time { return now.Add(steerTTL + time.Second) }
	if got := l.SteerResends("c1", "steer-m-2"); got != nil {
		t.Errorf("SteerResends(expired) = %v, want nil", got)
	}
}

func TestSteerLedger_ForgetChatDropsOnlyThatChat(t *testing.T) {
	l := NewSteerLedger()
	l.RecordUserSteer("c1", "steer-a", nil)
	l.RecordUserSteer("c2", "steer-b", nil)

	l.ForgetChat("c1")

	if got := l.SteerOrigin("c1", "steer-a"); got != marotte.SteerOriginAgent {
		t.Errorf("c1 after ForgetChat = %q, want %q", got, marotte.SteerOriginAgent)
	}
	if got := l.SteerOrigin("c2", "steer-b"); got != marotte.SteerOriginUser {
		t.Errorf("c2 after forgetting c1 = %q, want %q — a sibling chat's steers are untouched",
			got, marotte.SteerOriginUser)
	}
}

// The bound is what stops a pathological producer growing the map without limit,
// and it evicts the entry closest to expiry rather than clearing the map: losing
// one record mislabels one note, losing all of them mislabels every later one.
func TestSteerLedger_BoundedByEvictingTheOldest(t *testing.T) {
	l := NewSteerLedger()
	l.maxN = 4
	now := time.Now()
	l.now = func() time.Time { return now }

	// Distinct expiries, so "closest to expiry" is well defined.
	for i, id := range []string{"a", "b", "c", "d", "e", "f"} {
		l.ttl = steerTTL + time.Duration(i)*time.Minute
		l.RecordUserSteer("c1", id, nil)
	}

	// The sweep runs before the insert, so the bound is an inclusive ceiling.
	if n := len(l.sent); n > l.maxN {
		t.Errorf("held %d entries with maxN %d", n, l.maxN)
	}
	if got := l.SteerOrigin("c1", "a"); got != marotte.SteerOriginAgent {
		t.Errorf("the oldest entry survived eviction: %q", got)
	}
	if got := l.SteerOrigin("c1", "f"); got != marotte.SteerOriginUser {
		t.Errorf("the newest entry = %q, want %q", got, marotte.SteerOriginUser)
	}
}

// A nil ledger answers rather than panicking, because the alternative is a
// nil-receiver crash on the first steer of a build whose wiring missed it.
func TestSteerLedger_NilAnswersAgent(t *testing.T) {
	var l *SteerLedger
	l.RecordUserSteer("c1", "steer-m-1", nil)
	if got := l.SteerOrigin("c1", "steer-m-1"); got != marotte.SteerOriginAgent {
		t.Errorf("SteerOrigin on a nil ledger = %q, want %q", got, marotte.SteerOriginAgent)
	}
	l.ForgetChat("c1")
}
