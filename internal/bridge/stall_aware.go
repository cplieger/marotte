package bridge

import (
	"context"
	"sync"
	"time"
)

// stallQuiet is how long the engine must have read no frame for to count as silent: a reply to any session means its
// event loop runs, so a call it has not answered is slow, not stalled. A var for tests.
var stallQuiet = 2 * time.Second

// stallRecheck is how often an extended deadline re-reads the engine's CPU. A var for tests.
var stallRecheck = 2 * time.Second

// stallSample is the gap between Stalled's two CPU readings: long enough for a busy engine to accrue clock ticks
// (100 Hz), short enough to block a timer goroutine briefly. A var for tests.
var stallSample = 250 * time.Millisecond

// stallExtendCeiling caps how far a stall extends a deadline, above the 81.6 s event-loop stall measured on kiro-cli
// 2.37 (kirodotdev/Kiro#12261). A var for tests.
var stallExtendCeiling = 120 * time.Second

// Stalled reports whether the engine is in an event-loop stall (kirodotdev/Kiro#12261): it read no frame for
// stallQuiet, and node's CPU advanced across stallSample, for which Stalled blocks. An ended engine, or a CPU probe
// that cannot read, is never stalled.
func (e *Engine) Stalled() bool {
	stalled, _ := e.probeStall(stallQuiet, stallSample)
	return stalled
}

// probeStall is Stalled's reading, and the last frame's time read once it ends: a frame read during the sample is
// liveness, and shows in that time however the reading ended.
func (e *Engine) probeStall(quiet, sample time.Duration) (stalled bool, lastFrame int64) {
	last := e.lastFrame.Load()
	if last != 0 && time.Since(time.Unix(0, last)) < quiet {
		return false, last
	}
	// Notes node's group while it is unknown, as during initialize; Stalled never runs under writeMu.
	e.engineLeader()
	cpuBefore, ok := e.cpuTicks()
	if !ok {
		return false, e.lastFrame.Load()
	}
	gap := time.NewTimer(sample)
	defer gap.Stop()
	select {
	case <-gap.C:
	case <-e.done:
		return false, e.lastFrame.Load()
	}
	cpuAfter, ok := e.cpuTicks()
	after := e.lastFrame.Load()
	return ok && cpuAfter > cpuBefore && after == last, after
}

// StallAware is context.WithTimeout(ctx, d) for a call to this engine, except that an expiry finding the engine
// Stalled waits for the stall to end, re-checking every stallRecheck, and a stall the engine ends by reading a frame
// starts d over, for the calls a door makes after the late answer; all up to d+stallExtendCeiling. Its Err after an
// expiry is context.DeadlineExceeded, as is that of every context derived from it.
func (e *Engine) StallAware(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	c := &stallCtx{
		parent: ctx, ceiling: time.Now().Add(d + stallExtendCeiling), done: make(chan struct{}),
		budget: d, quiet: stallQuiet, sample: stallSample, recheck: stallRecheck,
	}
	cancel := func() { c.end(context.Canceled) }
	if err := ctx.Err(); err != nil {
		c.err = err
		close(c.done)
		return c, cancel
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopParent = context.AfterFunc(ctx, func() { c.end(ctx.Err()) })
	c.timer = time.AfterFunc(d, func() { e.extendWhileStalled(c) })
	return c, cancel
}

func (e *Engine) extendWhileStalled(c *stallCtx) {
	if c.Err() != nil {
		return
	}
	if !time.Now().Before(c.ceiling) {
		c.end(context.DeadlineExceeded)
		return
	}
	stalled, lastFrame := e.probeStall(c.quiet, c.sample)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return
	}
	next := c.recheck
	switch {
	// Ahead of the stalled arm: a late re-check can find the answer older than quiet while the CPU still burns.
	case c.extended && lastFrame != c.stallFrame:
		next = c.budget
	case stalled:
	default:
		c.endLocked(context.DeadlineExceeded)
		return
	}
	c.extended, c.stallFrame = stalled, lastFrame
	c.timer = time.AfterFunc(min(next, time.Until(c.ceiling)), func() { e.extendWhileStalled(c) })
}

// stallCtx is StallAware's context. It is not built on context.WithCancelCause: that context and every child of it
// report context.Canceled on a cancel, where callers of an expired call test for context.DeadlineExceeded.
type stallCtx struct {
	parent     context.Context
	err        error // guarded by mu
	done       chan struct{}
	timer      *time.Timer // guarded by mu
	stopParent func() bool // guarded by mu
	ceiling    time.Time
	// extended is whether the last check found a stall, and stallFrame the engine's last frame time as that check
	// ended; guarded by mu.
	extended   bool
	stallFrame int64
	// The budget and tunables read when the context was made: its timer goroutines can outlive the caller's cancel.
	budget, quiet, sample, recheck time.Duration
	mu                             sync.Mutex
}

func (c *stallCtx) end(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.endLocked(err)
}

func (c *stallCtx) endLocked(err error) {
	if c.err != nil {
		return
	}
	c.err = err
	close(c.done)
	c.timer.Stop()
	c.stopParent()
}

// Deadline is the latest the context can end: the parent's deadline or the extension ceiling, whichever is earlier.
func (c *stallCtx) Deadline() (time.Time, bool) {
	if d, ok := c.parent.Deadline(); ok && d.Before(c.ceiling) {
		return d, true
	}
	return c.ceiling, true
}

func (c *stallCtx) Done() <-chan struct{} { return c.done }

func (c *stallCtx) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *stallCtx) Value(key any) any { return c.parent.Value(key) }
