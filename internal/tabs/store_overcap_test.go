package tabs

import (
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func fillStore(t *testing.T, s *Store, n int) {
	t.Helper()
	for i := range n {
		if _, _, _, err := s.Open(t.Context(), chatSpec("c-"+strconv.Itoa(i))); err != nil {
			t.Fatalf("Setup: Open number %d of %d: %v", i+1, n, err)
		}
	}
}

// TestOpen_OverCapOpensPastTheProductLimit pins the boot heal's way past the 48-tab cap: a lost Bot tab must reopen
// whatever else is open, and every open that does not carry the mark still meets the cap.
func TestOpen_OverCapOpensPastTheProductLimit(t *testing.T) {
	s, dir := newTestStore(t)
	fillStore(t, s, MaxOpenTabs)

	healed, created, _, err := s.Open(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-bot", Bot: true, OverCap: true})
	if err != nil || !created {
		t.Fatalf("Open(OverCap) at %d tabs = (created %v, %v), want the tab opened", MaxOpenTabs, created, err)
	}
	if !healed.Bot {
		t.Error("an over-cap open dropped the Bot fact")
	}
	if doc := onDisk(t, dir); len(doc.Tabs) != MaxOpenTabs+1 {
		t.Errorf("tabs.json holds %d tabs, want %d", len(doc.Tabs), MaxOpenTabs+1)
	}

	_, created, version, err := s.Open(t.Context(), chatSpec("c-ordinary"))
	if !errors.Is(err, ErrTooMany) || created || version != 0 {
		t.Errorf("Open without OverCap at %d tabs = (created %v, v%d, %v), want ErrTooMany", MaxOpenTabs+1, created, version, err)
	}
}

// TestOpen_OverCapStillRefusesAtTheDecodeBound pins that the heal's mark lifts the product limit only: MaxTabs is
// what a load keeps, so a tab past it would be lost at the next start.
func TestOpen_OverCapStillRefusesAtTheDecodeBound(t *testing.T) {
	s, _ := newTestStore(t)
	full := make([]marotte.TabSubject, MaxTabs)
	for i := range full {
		full[i] = marotte.TabSubject{ID: "t-" + strconv.Itoa(i), Kind: marotte.TabKindChat, Ref: "c-" + strconv.Itoa(i), Owns: true}
	}
	s.publish(&state{tabs: full, version: 1})

	_, created, version, err := s.Open(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-bot", Bot: true, OverCap: true})

	if !errors.Is(err, ErrTooMany) || created || version != 0 {
		t.Errorf("Open(OverCap) at %d tabs = (created %v, v%d, %v), want ErrTooMany", MaxTabs, created, version, err)
	}
	if tabs, _ := s.List(); len(tabs) != MaxTabs {
		t.Errorf("List() = %d tabs, want %d: the refusal must apply nothing", len(tabs), MaxTabs)
	}
}

// TestOpenTab_OverCapIsNeverDecoded pins that no client can state the heal's mark: it is server-only.
func TestOpenTab_OverCapIsNeverDecoded(t *testing.T) {
	var spec marotte.OpenTab
	if err := json.Unmarshal([]byte(`{"kind":"chat","ref":"c-a","OverCap":true,"over_cap":true}`), &spec); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if spec.OverCap {
		t.Error("a decoded OpenTab carries OverCap; a client could open past the tab limit")
	}
}
