package agent

import (
	"testing"
	"time"
)

func gateOn(clk *attnClock) *modelGate {
	g := newModelGate()
	g.setClock(clk.now)
	return g
}

// Starts are spaced 10 s apart, and a held start learns when the gate opens for it.
func TestModelGate_SpacesStarts(t *testing.T) {
	t.Parallel()
	clk := &attnClock{t: time.Unix(1_800_000_000, 0)}
	g := gateOn(clk)
	if ok, _ := g.admit("a"); !ok {
		t.Fatal("the first start was held")
	}
	clk.advance(9 * time.Second)
	ok, openAt := g.admit("b")
	if ok {
		t.Fatal("a start 9 s after another was admitted")
	}
	if want := clk.now().Add(time.Second); !openAt.Equal(want) {
		t.Errorf("openAt = %v, want %v", openAt, want)
	}
	clk.advance(time.Second)
	if ok, _ := g.admit("b"); !ok {
		t.Error("a start 10 s after the last was held")
	}
}

// A throttled turn's end holds every start for 30 s.
func TestModelGate_AThrottleHoldsStartsFor30s(t *testing.T) {
	t.Parallel()
	clk := &attnClock{t: time.Unix(1_800_000_000, 0)}
	g := gateOn(clk)
	g.sawThrottle()
	clk.advance(29 * time.Second)
	if ok, _ := g.admit("a"); ok {
		t.Fatal("a start 29 s after a throttle was admitted")
	}
	clk.advance(time.Second)
	if ok, _ := g.admit("a"); !ok {
		t.Error("a start 30 s after a throttle was held")
	}
}

// An entry held 5 minutes by a steady throttle starts anyway; a newer one is still held.
func TestModelGate_AnEntryHeldFiveMinutesStarts(t *testing.T) {
	t.Parallel()
	clk := &attnClock{t: time.Unix(1_800_000_000, 0)}
	g := gateOn(clk)
	g.sawThrottle()
	if ok, openAt := g.admit("a"); ok || !openAt.Equal(clk.now().Add(modelGateQuiet)) {
		t.Fatalf("admit = %v, %v; want held until the quiet ends", ok, openAt)
	}
	for range 9 {
		clk.advance(modelGateQuiet)
		g.sawThrottle()
		if ok, _ := g.admit("a"); ok {
			t.Fatal("a start inside a steady throttle was admitted before 5 minutes")
		}
	}
	clk.advance(modelGateQuiet)
	g.sawThrottle()
	if ok, _ := g.admit("b"); ok {
		t.Error("a newly held entry was admitted inside the throttle")
	}
	if ok, _ := g.admit("a"); !ok {
		t.Error("an entry held 5 minutes was still held")
	}
}
