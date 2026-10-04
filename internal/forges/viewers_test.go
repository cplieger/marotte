package forges

// The viewer set and the cadence it holds: a client tag views while it last said
// watching and the presence table does not judge it gone, and while one views the
// next cycle is PRPollInterval after the last whatever the poller tracks.

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// livePresence judges every tag gone except the ones set live, as the presence
// table judges an unseen tag.
type livePresence struct {
	live map[string]bool
	mu   sync.Mutex
}

func newLivePresence(tags ...string) *livePresence {
	p := &livePresence{live: make(map[string]bool)}
	for _, tag := range tags {
		p.live[tag] = true
	}
	return p
}

func (p *livePresence) Gone(tag string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.live[tag]
}

func (p *livePresence) set(tag string, live bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.live[tag] = live
}

func viewerPoller(src PRSource, g *fakeGate, presence presenceReader) *PRStatusPoller {
	return NewPRStatusPoller(src, &fakeNotifier{}, g.Open, WithViewers(presence))
}

func TestViewers_GoneTagStopsViewing(t *testing.T) {
	pres := newLivePresence("tab-a")
	p := viewerPoller(&fakeSource{}, &fakeGate{}, pres)
	p.viewers.watch("tab-a", "", true)
	if !p.viewers.any() {
		t.Fatal("Setup: a live tag that said watching is not a viewer")
	}

	pres.set("tab-a", false)
	if p.viewers.any() {
		t.Error("a tag the presence table judges gone is still a viewer")
	}
	pres.set("tab-a", true)
	if p.viewers.any() {
		t.Error("a tag that went and came back is a viewer without saying watching again; " +
			"want it to have left the set when it went")
	}
}

func TestViewers_WatchingFalseLeaves(t *testing.T) {
	p := viewerPoller(&fakeSource{}, &fakeGate{}, newLivePresence("tab-a", "tab-b"))
	p.viewers.watch("tab-a", "", true)
	p.viewers.watch("tab-b", "", true)

	p.viewers.watch("tab-a", "", false)
	if !p.viewers.any() {
		t.Error("tab-a stopped watching and tab-b, still watching, is no viewer")
	}
	p.viewers.watch("tab-b", "", false)
	if p.viewers.any() {
		t.Error("both tags said watching: false and a viewer remains")
	}
}

func TestViewers_AFloodOfGoneTagsHoldsNoMemory(t *testing.T) {
	p := viewerPoller(&fakeSource{}, &fakeGate{}, newLivePresence("tab-a"))
	p.viewers.watch("tab-a", "", true)
	for i := range 1000 {
		p.viewers.watch("gone-"+strconv.Itoa(i), "", true)
	}

	if n := len(p.viewers.tags); n > 2 || !p.viewers.any() {
		t.Errorf("after 1000 watches by tags no stream holds, the set holds %d tags and viewing is %v; "+
			"want at most the live tag and the newest gone one, and the live tag still viewing", n, p.viewers.any())
	}
}

func TestPollerInterval_ViewerForcesTheFastCycle(t *testing.T) {
	p := viewerPoller(&fakeSource{}, &fakeGate{push: true}, newLivePresence("tab-a"))
	p.sweep(t.Context())
	if got := p.nextDelay(); got != PRDiscoveryInterval {
		t.Fatalf("Setup: with nothing tracked and no viewer the poller arms %v, want discovery %v", got, PRDiscoveryInterval)
	}

	p.viewers.watch("tab-a", "", true)
	if got := p.nextDelay(); got != PRPollInterval {
		t.Errorf("with nothing tracked and one viewer the poller arms %v, want the active interval %v", got, PRPollInterval)
	}
}

func TestPollerInterval_NoViewerKeepsDiscovery(t *testing.T) {
	p := viewerPoller(&fakeSource{}, &fakeGate{push: true}, newLivePresence("tab-a"))
	p.viewers.watch("unseen", "", true)
	if got := p.nextDelay(); got != PRDiscoveryInterval {
		t.Errorf("a watch by a tag no stream holds arms %v, want discovery %v", got, PRDiscoveryInterval)
	}
	p.viewers.watch("tab-a", "", true)
	p.viewers.watch("tab-a", "", false)
	if got := p.nextDelay(); got != PRDiscoveryInterval {
		t.Errorf("after the viewer said watching: false the poller arms %v, want discovery %v", got, PRDiscoveryInterval)
	}

	bare := NewPRStatusPoller(&fakeSource{}, &fakeNotifier{}, (&fakeGate{push: true}).Open)
	bare.viewers.watch("tab-a", "", true)
	if got := bare.nextDelay(); got != PRDiscoveryInterval {
		t.Errorf("a poller with no presence table arms %v after a watch, want discovery %v: "+
			"with no table nothing is known to receive the stream", got, PRDiscoveryInterval)
	}
}

func TestPollerRun_AViewerArrivingRetimesTheWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		src := newHeldSource(false)
		p := viewerPoller(src, &fakeGate{present: true}, newLivePresence("tab-a"))
		p.tick, p.discovery = time.Minute, time.Hour
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			p.Run(ctx)
			close(done)
		}()
		defer func() {
			cancel()
			<-done
		}()
		reads := func(at string, want int) {
			t.Helper()
			synctest.Wait()
			if got := len(src.reads()); got != want {
				t.Errorf("%s: %d sweeps, want %d", at, got, want)
			}
		}

		time.Sleep(2 * time.Minute)
		reads("two minutes in, nothing tracked, no viewer", 0)
		p.viewers.watch("tab-a", "", true)
		reads("a viewer arriving two minutes after the last wait began", 1)
		time.Sleep(10 * time.Second)
		p.viewers.watch("tab-a", "", true)
		reads("a second watch ten seconds after the sweep", 1)
		time.Sleep(49 * time.Second)
		reads("59 seconds after the sweep", 1)
		time.Sleep(2 * time.Second)
		reads("61 seconds after the sweep, the minute counted from the sweep and not from the watch", 2)
		p.viewers.watch("tab-a", "", false)
		time.Sleep(61 * time.Second)
		reads("after the viewer left, the wait armed while it viewed", 3)
		time.Sleep(10 * time.Minute)
		reads("ten minutes later with no viewer and nothing tracked", 3)
	})
}
