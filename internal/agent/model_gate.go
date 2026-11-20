package agent

import (
	"log/slog"
	"sync"
	"time"
)

// The model gate's bounds. A turn marotte starts on its own waits modelGateQuiet after any turn that ended on a model
// throttle, so it adds no turn inside a burst the account is in, and modelGateSpacing after the last such start, so a
// restart's wakes into many chats do not land in one second. modelGateCap lets an entry held that long start anyway,
// so a steady throttle cannot starve a wake.
const (
	modelGateQuiet   = 30 * time.Second
	modelGateSpacing = 10 * time.Second
	modelGateCap     = 5 * time.Minute
)

// modelGate spaces the turns marotte starts on its own (run-attention wakes) under the account's model rate limit,
// one per runtime and shared by every chat. Its mutex is a leaf: admit runs under a chat's lc.mu.
type modelGate struct {
	now func() time.Time
	// held is when each entry the gate holds was first held, keyed by entry id.
	held         map[string]time.Time
	lastThrottle time.Time
	lastStart    time.Time
	mu           sync.Mutex
}

func newModelGate() *modelGate {
	return &modelGate{now: time.Now, held: map[string]time.Time{}}
}

func (g *modelGate) setClock(now func() time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.now = now
}

// clock is the gate's now, which an open time is measured against.
func (g *modelGate) clock() time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.now()
}

// sawThrottle records a turn, a chat's or a workflow step's, that ended on a model throttle. kiro-cli's in-turn retry
// notices are not fed: they arrive inside turns that may still succeed, and a steady stream would hold the gate shut.
func (g *modelGate) sawThrottle() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lastThrottle = g.now()
}

// admit reports whether entry id may start its turn now, recording the start when it may; otherwise openAt is when
// the gate next opens for it.
func (g *modelGate) admit(id string) (ok bool, openAt time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	since, held := g.held[id]
	if !held {
		since = now
	}
	open := later(g.lastThrottle.Add(modelGateQuiet), g.lastStart.Add(modelGateSpacing))
	capped := now.Sub(since) >= modelGateCap
	if now.Before(open) && !capped {
		g.held[id] = since
		g.pruneLocked(now)
		return false, earlier(open, since.Add(modelGateCap))
	}
	if now.Before(open) {
		slog.Info("a run-attention wake was held at the model gate for 5 minutes; starting it anyway",
			"entry", id, "held", len(g.held))
	}
	delete(g.held, id)
	g.lastStart = now
	return true, time.Time{}
}

// pruneLocked forgets entries held past twice the cap, which left the list without a take.
func (g *modelGate) pruneLocked(now time.Time) {
	for id, since := range g.held {
		if now.Sub(since) > 2*modelGateCap {
			delete(g.held, id)
		}
	}
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
