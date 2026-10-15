package agent

import "sync"

// utilityLease owns the lazily-built utility session and every access to it. No exported field: every path takes
// the mutex, so a reader that never builds (the orphan-session sweep) is still ordered against the build.
type utilityLease struct {
	// build constructs the session. Supplied by the Runtime because its hooks point back into runtime services, an edge
	// that must stay visible at the wiring site.
	build func() *utilitySession
	// reconcile runs before every get and outside mu, because it may take a session spawned under settings that have
	// since moved: no writer sees a hand edit, so the utility session must check at use the way a chat open does.
	reconcile func()
	// session is nil until first use.
	session *utilitySession `wiring:"optional"`
	mu      sync.Mutex
}

// get returns the utility session, building it on first use. A mutex around a nil check, so readers are ordered too.
func (l *utilityLease) get() *utilitySession {
	if l.reconcile != nil {
		l.reconcile()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.session == nil {
		l.session = l.build()
	}
	return l.session
}

// peek returns the session only if already built, for the idle cull and the session sweep, which would otherwise
// spawn what they inspect.
func (l *utilityLease) peek() *utilitySession {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.session
}

// take removes and returns the session so the caller can stop it; the next get builds a fresh one.
func (l *utilityLease) take() *utilitySession {
	l.mu.Lock()
	defer l.mu.Unlock()
	us := l.session
	l.session = nil
	return us
}
