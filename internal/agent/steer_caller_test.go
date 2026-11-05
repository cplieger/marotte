package agent

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// An API caller's steer keeps its attribution from the dock row to the read entry's source.
func TestSteerRecord_KeepsAnAPICallersAttributionToTheRead(t *testing.T) {
	s := newSteerHarness(t)
	s.bind()
	sends, refuse := s.q.RouteSteer(s.chat, "steer-a", "Agent Mail: 1 new message", &marotte.Caller{Label: "Agent Mail"}, promptHolder)
	if refuse != "" {
		t.Fatalf("RouteSteer refused %q", refuse)
	}
	s.kas(sends)

	frames := s.spy.framesFor("steer-a")
	if len(frames) == 0 {
		t.Fatal("the row was never framed to the dock")
	}
	for i, f := range frames {
		if f.Caller == nil || f.Caller.Label != "Agent Mail" {
			t.Errorf("dock frame %d caller = %+v, want Agent Mail", i, f.Caller)
		}
	}
	if read := s.recs.SteerRead(s.chat, s.kasID("steer-a")); read.Caller == nil || read.Caller.Label != "Agent Mail" {
		t.Errorf("SteerRead caller = %+v, want Agent Mail for the read entry", read.Caller)
	}
}

func TestSteerRecord_TheUsersSteerNamesNoCaller(t *testing.T) {
	s := newSteerHarness(t)
	s.bind()
	s.steer("steer-a", "use tabs")
	if read := s.recs.SteerRead(s.chat, s.kasID("steer-a")); read.Caller != nil {
		t.Errorf("SteerRead caller = %+v, want none on the user's steer", read.Caller)
	}
}
