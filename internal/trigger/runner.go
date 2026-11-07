package trigger

import (
	"context"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/settings"
)

// Runner polls every enabled event rule's feed on its own interval, whoever is present.
type Runner struct {
	poller *poller
	c      *Confirmer
	now    func() time.Time
}

// NewRunner returns the runner of c's rules, reading feeds through src. It installs on ll, the launcher of c's
// rules, the fleet caps read from configDir's config.json at each launch; ll runs on its own, never through the
// runner.
func NewRunner(c *Confirmer, ll *LaunchLoop, src FeedSource, configDir string) *Runner {
	ll.setCaps(&fleetCaps{leases: c.leases, limits: func(ctx context.Context) (int, int) {
		return settings.TriggerCaps(ctx, configDir)
	}})
	return &Runner{poller: newPoller(src, c), c: c, now: time.Now}
}

// Run polls until ctx ends, then returns once every poll in flight returned. A feed polls at the shortest
// interval of its enabled rules, timed from the end of its last poll, and the feeds known when Run starts are
// staggered across their first interval.
func (rn *Runner) Run(ctx context.Context) {
	var wg sync.WaitGroup
	defer wg.Wait()
	ended := make(chan string)
	sched := newFeedSchedule()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		for _, fr := range sched.due(groupFeeds(rn.c.store.List()), rn.now()) {
			wg.Go(func() {
				rn.poller.poll(ctx, fr)
				select {
				case ended <- fr.Feed.key():
				case <-ctx.Done():
				}
			})
		}
		timer.Reset(sched.wait(rn.now()))
		select {
		case key := <-ended:
			sched.ended(key, rn.now())
		case <-timer.C:
		case <-ctx.Done():
			return
		}
	}
}
