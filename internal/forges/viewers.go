package forges

import "sync"

type presenceReader interface {
	Gone(tag string) bool
}

// Pages of one browser profile share the stream tag the presence table keys on, so the page names
// itself; an empty page is a client that does not.
type viewer struct {
	tag, page string
}

// viewers are the pages showing the pull-request view: a page is one from the
// watch that says so until it says otherwise or the presence table judges its
// tag gone. Safe for concurrent use.
type viewers struct {
	// presence is nil when no table was wired, and then no tag is known to
	// receive the stream.
	presence presenceReader
	tags     map[viewer]struct{}
	// arrived rings Run's loop on a watch, so a wait armed at the discovery rate
	// is timed again.
	arrived chan struct{}
	mu      sync.Mutex
}

func newViewers() *viewers {
	return &viewers{tags: make(map[viewer]struct{}), arrived: make(chan struct{}, 1)}
}

// WithViewers judges the clients showing the pull-request view against
// presence, the table the stream feeds.
func WithViewers(presence presenceReader) PollerOption {
	return func(p *PRStatusPoller) {
		p.viewers.presence = presence
	}
}

// Every watch drops the pages whose tag went, so the set holds the live pages and the newest one
// whatever is posted.
func (v *viewers) watch(tag, page string, watching bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	key := viewer{tag: tag, page: page}
	if !watching {
		delete(v.tags, key)
		return
	}
	v.pruneLocked()
	v.tags[key] = struct{}{}
	select {
	case v.arrived <- struct{}{}:
	default:
	}
}

func (v *viewers) any() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.pruneLocked()
	return len(v.tags) > 0
}

func (v *viewers) pruneLocked() {
	for key := range v.tags {
		if v.presence == nil || v.presence.Gone(key.tag) {
			delete(v.tags, key)
		}
	}
}
