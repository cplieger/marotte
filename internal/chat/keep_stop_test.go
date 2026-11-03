package chat

import (
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/chat/archive"
	"github.com/cplieger/marotte/internal/marotte"
)

func TestSwapTabForStop_TheRecordNeverHoldsNeither(t *testing.T) {
	s, _ := newTestStore(t)
	seedChat(t, s, "c1")
	if _, err := s.Hold(t.Context(), "c1", marotte.KeyTab, KeepDurable); err != nil {
		t.Fatal(err)
	}
	s.markReconciled("c1")

	hold, swapped := s.SwapTabForStop(t.Context(), "c1")

	c := readChat(t, s, "c1")
	if !swapped {
		t.Error("a readable record's swap reported not swapped")
	}
	if _, ok := c.Keep[marotte.KeyTab]; ok {
		t.Errorf("keep = %v: the tab fact survived the close", c.Keep)
	}
	if f := c.Keep[marotte.KeyStop]; f.Token != hold.Token || hold.Token == "" || !f.Process || f.Boot != s.Boot() {
		t.Errorf("stop fact = %+v, claim %+v; want this boot's process fact under the claim's token", f, hold)
	}
	v, err := s.PurgeIdle(t.Context(), "c1", time.Now().Add(time.Hour))
	if err != nil || v.Outcome != archive.PurgeKept || v.KeptBy != archive.KeptByStop {
		t.Errorf("PurgeIdle past every cutoff = %+v, %v; want kept by the stop hold", v, err)
	}
}

func TestSwapTabForStop_AFailedWritePendsTheHoldAndKeepsTheTab(t *testing.T) {
	s, _ := newTestStore(t)
	seedChat(t, s, "c1")
	if _, err := s.Hold(t.Context(), "c1", marotte.KeyTab, KeepDurable); err != nil {
		t.Fatal(err)
	}
	repair := failHeaderWrites(t, "c1")

	hold, swapped := s.SwapTabForStop(t.Context(), "c1")

	if swapped {
		t.Error("a failed write reported swapped, so the close would settle its owed tab release")
	}
	if _, ok := readChat(t, s, "c1").Keep[marotte.KeyTab]; !ok {
		t.Error("the tab fact left the record although the swap's write failed")
	}
	if !s.hasPending("c1") {
		t.Fatal("the stop hold was not pended")
	}
	repair()
	if err := s.Release(t.Context(), "c1", marotte.KeyTab, EvidenceTabClosed); err != nil {
		t.Fatal(err)
	}
	c := readChat(t, s, "c1")
	if _, ok := c.Keep[marotte.KeyTab]; ok {
		t.Errorf("keep = %v after the owed release", c.Keep)
	}
	if f := c.Keep[marotte.KeyStop]; f.Token != hold.Token {
		t.Errorf("stop fact = %+v, want the pended hold %q flushed by the owed release's write", f, hold.Token)
	}
}

func TestEndStop_ReleasesOnlyItsOwnHold(t *testing.T) {
	s, _ := newTestStore(t)
	seedChat(t, s, "c1")
	first, _ := s.SwapTabForStop(t.Context(), "c1")
	second, _ := s.SwapTabForStop(t.Context(), "c1")

	s.EndStop(t.Context(), &first)

	if f, ok := readChat(t, s, "c1").Keep[marotte.KeyStop]; !ok || f.Token != second.Token {
		t.Errorf("stop fact = %+v, %v after the first close's end; want the second close's hold", f, ok)
	}
	s.EndStop(t.Context(), &second)
	if f, ok := readChat(t, s, "c1").Keep[marotte.KeyStop]; ok {
		t.Errorf("stop fact = %+v after both ends, want none", f)
	}
}

func TestEndStop_DropsItsPendedHold(t *testing.T) {
	s, _ := newTestStore(t)
	seedChat(t, s, "c1")
	repair := failHeaderWrites(t, "c1")
	hold, _ := s.SwapTabForStop(t.Context(), "c1")
	repair()

	s.EndStop(t.Context(), &hold)

	if s.hasPending("c1") {
		t.Error("the ended stop's pended hold is still pending, so a later write would flush it")
	}
	if _, ok := readChat(t, s, "c1").Keep[marotte.KeyStop]; ok {
		t.Error("the ended stop's hold reached the record")
	}
}
