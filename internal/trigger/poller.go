package trigger

import (
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
)

// rescanEvery bounds the runner's sleep, so a rule created or enabled meanwhile is polled within it.
const rescanEvery = time.Minute

// feedSchedule is when each feed polls next.
type feedSchedule struct {
	feeds map[string]*feedClock
	// started is set once the first due call staggered the feeds it found.
	started bool
}

// feedClock is one feed's poll timing.
type feedClock struct {
	// next is when the feed polls: once a poll ended, its end plus the feed's current interval, so an edited
	// interval applies at once.
	next    time.Time
	lastEnd time.Time
	polling bool
}

func newFeedSchedule() *feedSchedule {
	return &feedSchedule{feeds: map[string]*feedClock{}}
}

// due answers the feeds to poll at now, marking each polling; a feed whose poll is in flight is never due. The first
// call staggers its feeds across their first interval (feed index × interval ÷ feed count), so a restart does not
// read every feed at once; a feed found later polls at once. A feed no rule names any more is forgotten.
func (s *feedSchedule) due(feeds []feedRules, now time.Time) []feedRules {
	seen := make(map[string]bool, len(feeds))
	var out []feedRules
	for i := range feeds {
		key := feeds[i].Feed.key()
		seen[key] = true
		every := feedInterval(feeds[i].Rules)
		fc := s.feeds[key]
		switch {
		case fc == nil && !s.started:
			fc = &feedClock{next: now.Add(time.Duration(i) * every / time.Duration(len(feeds)))}
			s.feeds[key] = fc
		case fc == nil:
			fc = &feedClock{next: now}
			s.feeds[key] = fc
		case !fc.lastEnd.IsZero():
			fc.next = fc.lastEnd.Add(every)
		}
		if !fc.polling && !now.Before(fc.next) {
			fc.polling = true
			out = append(out, feeds[i])
		}
	}
	s.started = true
	for key, fc := range s.feeds {
		if !seen[key] && !fc.polling {
			delete(s.feeds, key)
		}
	}
	return out
}

// ended records the end of the feed's poll at now.
func (s *feedSchedule) ended(key string, now time.Time) {
	if fc := s.feeds[key]; fc != nil {
		fc.polling, fc.lastEnd = false, now
	}
}

// wait is how long until the next feed is due, at most rescanEvery; it reads the times the last due call set.
func (s *feedSchedule) wait(now time.Time) time.Duration {
	wait := rescanEvery
	for _, fc := range s.feeds {
		if fc.polling {
			continue
		}
		wait = min(wait, max(fc.next.Sub(now), 0))
	}
	return wait
}

// feedInterval is the shortest poll interval of the feed's rules.
func feedInterval(rules []launchrule.Rule) time.Duration {
	every := pollInterval(&rules[0])
	for i := range rules[1:] {
		every = min(every, pollInterval(&rules[i+1]))
	}
	return every
}
