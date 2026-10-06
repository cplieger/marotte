package agent

import "sync"

// utilityLease owns the lazily-built utility runtime and every access to it. No exported field: every path takes
// the mutex, so a reader that never builds (the orphan-session sweep) is still ordered against the build.
type utilityLease struct {
	// build constructs the runtime. Supplied by the Runtime because its hooks point back into runtime services, an edge
	// that must stay visible at the wiring site.
	build func() *utilityRuntime
	// rt is nil until first use.
	rt *utilityRuntime `wiring:"optional"`
	mu sync.Mutex
}

// get returns the utility runtime, building it on first use. A mutex around a nil check, so readers are ordered too.
func (l *utilityLease) get() *utilityRuntime {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.rt == nil {
		l.rt = l.build()
	}
	return l.rt
}

// peek returns the runtime only if already built, for the idle cull and the session sweep, which would otherwise
// spawn what they inspect.
func (l *utilityLease) peek() *utilityRuntime {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rt
}

// take removes and returns the runtime so the caller can stop it; the next get builds a fresh one.
func (l *utilityLease) take() *utilityRuntime {
	l.mu.Lock()
	defer l.mu.Unlock()
	rt := l.rt
	l.rt = nil
	return rt
}
