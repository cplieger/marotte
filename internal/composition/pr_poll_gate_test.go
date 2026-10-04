package composition

// The PR-status poller's gate, driven through the real presence table, the real push
// service and the real poller. The defect these cover: the gate asked only whether a
// push subscription existed, and a browser that once opted into notifications stays
// subscribed with every tab closed, so the poller listed once a minute for the life of
// the process while the pull-request notice it exists to send sat at its default OFF
// and Send dropped every result.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/marotte/internal/forges"
	"github.com/cplieger/marotte/internal/liveness"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/push"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/sse"
)

// countingSource answers no pull requests and counts how often it was asked, which
// is the forge work the gate exists to withhold, and how often a client was present.
type countingSource struct{ calls, present atomic.Int32 }

func (s *countingSource) Read(_ context.Context, present bool, _ func(forges.PRConnection, forges.Scope) forgeapi.Cursor) []forges.ConnectionRead {
	s.calls.Add(1)
	if present {
		s.present.Add(1)
	}
	return nil
}

type gateFixture struct {
	clientConnected bool
	prStatusOn      bool
	subscribed      bool
}

// listingsInOneDiscoveryInterval runs the poller behind prPollGate for exactly one
// discovery interval of synthetic time and answers how often it listed (1 when the
// gate was open at the sweep, 0 when it was closed) and how many of those were
// present cycles.
func listingsInOneDiscoveryInterval(t *testing.T, f gateFixture) (listed, present int32) {
	t.Helper()
	dir := t.TempDir()
	if f.prStatusOn {
		doc, err := json.Marshal(map[string]bool{settings.KeyNotifyPRStatus: true})
		if err != nil {
			t.Fatalf("Setup: marshal config.json: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.json"), doc, 0o600); err != nil {
			t.Fatalf("Setup: write config.json: %v", err)
		}
	}
	synctest.Test(t, func(t *testing.T) {
		presence := push.NewPresence()
		svc := push.New(t.Context(), dir, "mailto:test@example.com", push.WithPresence(presence))
		defer svc.Close()
		if f.subscribed {
			svc.Subscribe(marotte.PushSubscription{Endpoint: "https://fcm.googleapis.com/fcm/send/gate"})
		}

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		if f.clientConnected {
			const tag = "tab-1"
			presence.Observe(&sse.PresenceEvent{Kind: sse.PresenceConnected, Tag: tag})
			go func() {
				tick := time.NewTicker(liveness.Keepalive)
				defer tick.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-tick.C:
						presence.Alive(tag)
					}
				}
			}()
		}

		src := &countingSource{}
		poller := forges.NewPRStatusPoller(src, svc, prPollGate(presence, svc))
		done := make(chan struct{})
		go func() {
			poller.Run(ctx)
			close(done)
		}()
		synctest.Sleep(forges.PRDiscoveryInterval)
		synctest.Wait()
		cancel()
		<-done
		listed, present = src.calls.Load(), src.present.Load()
	})
	return listed, present
}

// TestPollerGate_NoPollWithNobodyConnectedAndPreferenceOff is the defect itself: a
// live subscription with the pull-request notice at its default OFF and no page open.
func TestPollerGate_NoPollWithNobodyConnectedAndPreferenceOff(t *testing.T) {
	if n, _ := listingsInOneDiscoveryInterval(t, gateFixture{subscribed: true}); n != 0 {
		t.Errorf("listings with a subscription, notify_pr_status off and no client connected = %d, want 0", n)
	}
}

// TestPollerGate_PollsWithAClientConnectedAndPreferenceOff is the first arm: someone
// is looking, so every scope is read whatever the notice setting says, with no
// subscription at all.
func TestPollerGate_PollsWithAClientConnectedAndPreferenceOff(t *testing.T) {
	if n, present := listingsInOneDiscoveryInterval(t, gateFixture{clientConnected: true}); n != 1 || present != 1 {
		t.Errorf("listings with a client connected, notify_pr_status off and no subscription = %d (%d present), want 1 present",
			n, present)
	}
}

// TestPollerGate_PollsWithPreferenceOnAndASubscriptionAndNobodyConnected is the
// second arm: the reader asked to be told when nobody is looking, which is the
// feature's one unattended purpose, and costs the authored call alone.
func TestPollerGate_PollsWithPreferenceOnAndASubscriptionAndNobodyConnected(t *testing.T) {
	if n, present := listingsInOneDiscoveryInterval(t, gateFixture{prStatusOn: true, subscribed: true}); n != 1 || present != 0 {
		t.Errorf("listings with notify_pr_status on, a subscription and no client connected = %d (%d present), want 1 push-only",
			n, present)
	}
}
