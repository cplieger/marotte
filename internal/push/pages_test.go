package push

import (
	"testing"
	"time"

	"github.com/cplieger/go/sse/v2"
	"github.com/cplieger/marotte/internal/liveness"
	"github.com/cplieger/marotte/internal/marotte"
)

func pagesAt(t *testing.T) (*Presence, *presenceClock) {
	t.Helper()
	clock := &presenceClock{at: time.Unix(1_700_000_000, 0)}
	p := newPresenceAt(clock)
	p.Observe(connected("t1"))
	return p, clock
}

func onChat(ref string) *marotte.TabRef { return &marotte.TabRef{Kind: marotte.TabKindChat, Ref: ref} }

func TestPresenceView_ATagWithNoStreamStoresNoPage(t *testing.T) {
	p, _ := pagesAt(t)
	p.View("unseen", marotte.PageView{Page: "P1", Visible: true})
	if _, ok := p.offerTarget("unseen"); ok {
		t.Error("offerTarget(unseen) found a page, want none: a tag with no stream has no live page")
	}
}

func TestPresenceView_AClosedReportDeletesThePage(t *testing.T) {
	p, _ := pagesAt(t)
	p.View("t1", marotte.PageView{Page: "P1", Visible: true, Tab: onChat("c1")})
	p.View("t1", marotte.PageView{Page: "P1", Closed: true})
	if _, ok := p.offerTarget("t1"); ok {
		t.Error("offerTarget found the closed page")
	}
	if p.pageOnTab("t1", marotte.TabKindChat, "c1") {
		t.Error("pageOnTab read the closed page as on c1")
	}
}

func TestPresenceView_PageOnTab(t *testing.T) {
	p, clock := pagesAt(t)
	p.View("t1", marotte.PageView{Page: "P1", Visible: true, Tab: onChat("c1")})
	p.View("t1", marotte.PageView{Page: "P2", Visible: false, Tab: onChat("c2")})
	p.View("t1", marotte.PageView{Page: "P3", Visible: true})

	t.Run("a visible page on the tab", func(t *testing.T) {
		if !p.pageOnTab("t1", marotte.TabKindChat, "c1") {
			t.Error("pageOnTab(c1) = false, want true")
		}
	})
	t.Run("a hidden page on the tab", func(t *testing.T) {
		if p.pageOnTab("t1", marotte.TabKindChat, "c2") {
			t.Error("pageOnTab(c2) = true for a hidden page")
		}
	})
	t.Run("the same ref of another kind", func(t *testing.T) {
		if p.pageOnTab("t1", marotte.TabKindRun, "c1") {
			t.Error("pageOnTab(run c1) = true")
		}
	})
	t.Run("another profile's page", func(t *testing.T) {
		p.Observe(connected("t2"))
		if p.pageOnTab("t2", marotte.TabKindChat, "c1") {
			t.Error("pageOnTab(t2, c1) = true from t1's page")
		}
	})
	t.Run("a visible row silent past the alive window", func(t *testing.T) {
		clock.Advance(liveness.AliveWindow + time.Millisecond)
		p.Alive("t1")
		if p.pageOnTab("t1", marotte.TabKindChat, "c1") {
			t.Error("pageOnTab(c1) = true for a page that stopped re-reporting")
		}
	})
}

func TestPresenceView_OfferTargetOrder(t *testing.T) {
	t.Run("the focused visible page first", func(t *testing.T) {
		p, clock := pagesAt(t)
		p.View("t1", marotte.PageView{Page: "P1", Visible: true, Focused: true})
		clock.Advance(time.Second)
		p.View("t1", marotte.PageView{Page: "P2", Visible: true})
		if got, _ := p.offerTarget("t1"); got != "P1" {
			t.Errorf("offerTarget = %q, want P1", got)
		}
	})
	t.Run("else a page on screen over one that just hid", func(t *testing.T) {
		p, clock := pagesAt(t)
		p.View("t1", marotte.PageView{Page: "P1", Visible: true})
		clock.Advance(time.Second)
		p.View("t1", marotte.PageView{Page: "P2", Visible: true})
		clock.Advance(time.Second)
		p.View("t1", marotte.PageView{Page: "P2"})
		clock.Advance(time.Second)
		p.View("t1", marotte.PageView{Page: "P3"})
		if got, _ := p.offerTarget("t1"); got != "P1" {
			t.Errorf("offerTarget = %q, want P1, still on screen", got)
		}
	})
	t.Run("else among hidden pages the one visible most recently", func(t *testing.T) {
		p, clock := pagesAt(t)
		p.View("t1", marotte.PageView{Page: "P1", Visible: true})
		clock.Advance(time.Second)
		p.View("t1", marotte.PageView{Page: "P2", Visible: true})
		clock.Advance(time.Second)
		p.View("t1", marotte.PageView{Page: "P1"})
		clock.Advance(time.Second)
		p.View("t1", marotte.PageView{Page: "P2"})
		clock.Advance(time.Second)
		p.View("t1", marotte.PageView{Page: "P3"})
		if got, _ := p.offerTarget("t1"); got != "P2" {
			t.Errorf("offerTarget = %q, want P2, hidden last", got)
		}
	})
	t.Run("else the one that reported most recently", func(t *testing.T) {
		p, clock := pagesAt(t)
		p.View("t1", marotte.PageView{Page: "P1"})
		clock.Advance(time.Second)
		p.View("t1", marotte.PageView{Page: "P2"})
		if got, _ := p.offerTarget("t1"); got != "P2" {
			t.Errorf("offerTarget = %q, want P2", got)
		}
	})
}

func TestPresenceView_AGoneTagLosesItsPages(t *testing.T) {
	p, clock := pagesAt(t)
	p.View("t1", marotte.PageView{Page: "P1", Visible: true, Tab: onChat("c1")})
	p.Observe(disconnected("t1", sse.PresenceClosed))
	clock.Advance(liveness.ReconnectDelay)
	if _, ok := p.offerTarget("t1"); ok {
		t.Error("offerTarget found a page of a gone tag")
	}
	p.Observe(connected("t1"))
	if _, ok := p.offerTarget("t1"); ok {
		t.Error("a reconnect revived the gone tag's page rows")
	}
}

func TestPresenceView_ASilentTagOffersNothingUntilItsAcksResume(t *testing.T) {
	p, clock := pagesAt(t)
	p.View("t1", marotte.PageView{Page: "P1"})
	clock.Advance(liveness.AliveWindow + time.Millisecond)
	if _, ok := p.offerTarget("t1"); ok {
		t.Error("offerTarget found a page of a silent tag")
	}
	p.Alive("t1")
	if got, ok := p.offerTarget("t1"); !ok || got != "P1" {
		t.Errorf("offerTarget after the acks resumed = %q, %v, want P1", got, ok)
	}
}

func TestPresenceView_DropPage(t *testing.T) {
	p, _ := pagesAt(t)
	p.View("t1", marotte.PageView{Page: "P1"})
	p.dropPage("t1", "P1")
	if _, ok := p.offerTarget("t1"); ok {
		t.Error("offerTarget found the dropped page")
	}
}

func TestPresenceView_EvictsTheOldestPastTheCap(t *testing.T) {
	p, clock := pagesAt(t)
	for i := range maxPagesPerTag + 1 {
		p.View("t1", marotte.PageView{Page: string(rune('a' + i))})
		clock.Advance(time.Millisecond)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	pages := p.rows["t1"].pages
	if len(pages) != maxPagesPerTag {
		t.Errorf("pages = %d, want %d", len(pages), maxPagesPerTag)
	}
	if _, ok := pages["a"]; ok {
		t.Error("the oldest page survived the cap")
	}
}
