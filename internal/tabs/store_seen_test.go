package tabs

import (
	"sync"
	"testing"
)

func TestSetSeen_AdvancesAndBumpsOnce(t *testing.T) {
	s, _ := newTestStore(t)
	a := mustOpen(t, s, chatSpec("c-a"))
	_, before := s.List()

	v, err := s.SetSeen(t.Context(), a.ID, 3)
	if err != nil || v != before+1 {
		t.Fatalf("SetSeen(%q, 3) = (v%d, %v), want version %d", a.ID, v, err, before+1)
	}
	if open, _ := s.List(); open[0].Seen != 3 {
		t.Errorf("List()[0].Seen = %d, want 3", open[0].Seen)
	}
}

func TestSetSeen_NeverMovesBackward(t *testing.T) {
	cases := []struct {
		desc string
		ack  uint64
	}{
		{desc: "an equal value", ack: 5},
		{desc: "an older value", ack: 2},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			s, _ := newTestStore(t)
			a := mustOpen(t, s, chatSpec("c-a"))
			v1, err := s.SetSeen(t.Context(), a.ID, 5)
			if err != nil {
				t.Fatalf("Setup: SetSeen(5): %v", err)
			}
			v2, err := s.SetSeen(t.Context(), a.ID, tc.ack)
			if err != nil {
				t.Fatalf("SetSeen(%d): %v", tc.ack, err)
			}
			if v2 != v1 {
				t.Errorf("SetSeen(%d) after 5 = version %d, want %d unchanged", tc.ack, v2, v1)
			}
			if open, _ := s.List(); open[0].Seen != 5 {
				t.Errorf("Seen = %d after an ack of %d, want 5 kept", open[0].Seen, tc.ack)
			}
		})
	}
}

func TestSetSeen_UnknownIDWritesNothing(t *testing.T) {
	s, _ := newTestStore(t)
	mustOpen(t, s, chatSpec("c-a"))
	_, before := s.List()

	v, err := s.SetSeen(t.Context(), "not-open", 4)
	if err != nil {
		t.Fatalf("SetSeen on a tab that is not open = %v, want no error", err)
	}
	if v != before {
		t.Errorf("SetSeen on a tab that is not open = version %d, want %d unchanged", v, before)
	}
}

func TestSetSeen_SurvivesReopen(t *testing.T) {
	s, dir := newTestStore(t)
	a := mustOpen(t, s, chatSpec("c-a"))
	if _, err := s.SetSeen(t.Context(), a.ID, 7); err != nil {
		t.Fatalf("Setup: SetSeen(7): %v", err)
	}

	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(%q) again: %v", dir, err)
	}
	open, _ := reopened.List()
	if len(open) != 1 || open[0].Seen != 7 {
		t.Fatalf("after reopen List() = %+v, want one tab with Seen 7", open)
	}
}

func TestSetSeen_ConcurrentAcksConverge(t *testing.T) {
	for range 200 {
		s, _ := newTestStore(t)
		a := mustOpen(t, s, chatSpec("c-a"))
		var wg sync.WaitGroup
		for _, seen := range []uint64{2, 5} {
			wg.Go(func() {
				if _, err := s.SetSeen(t.Context(), a.ID, seen); err != nil {
					t.Errorf("SetSeen(%d): %v", seen, err)
				}
			})
		}
		wg.Wait()
		if open, _ := s.List(); open[0].Seen != 5 {
			t.Fatalf("concurrent acks of 2 and 5 left Seen %d, want 5", open[0].Seen)
		}
	}
}
