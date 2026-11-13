package applogin

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync/atomic"
	"time"

	"github.com/cplieger/access/account"
)

// fenceInterval is how often the fence loop submits its confirm job, and the held log's retry
// interval after a failed append.
var fenceInterval = 30 * time.Second

const (
	kindStoreReset  = "marotte.store_reset"
	kindStoreFenced = "marotte.store_fenced"
)

// fence is set by a non-durable publication and cleared by a durable confirm job, both on the
// writer goroutine; set is the writer's, status everyone else's.
type fence struct {
	since  time.Time
	hooks  atomic.Pointer[fenceHooks]
	tick   <-chan time.Time // tests only: replaces the loop's ticker
	status atomic.Bool
	set    bool
}

type fenceHooks struct{ onUnconfirmed, onConfirmed func() }

// SetFenceHooks binds what runs when a fence is set and when it clears. onUnconfirmed runs once
// per fence, off the writer goroutine; onConfirmed runs once, on the clear, and defaults to
// appending the held entries.
func (s *Store) SetFenceHooks(onUnconfirmed, onConfirmed func()) {
	s.fence.hooks.Store(&fenceHooks{onUnconfirmed: onUnconfirmed, onConfirmed: onConfirmed})
}

// Fenced reports whether sign-in changes are paused until the disk is confirmed.
func (s *Store) Fenced() bool { return s.fence.status.Load() }

func (s *Store) setFence() {
	if s.fence.set {
		return
	}
	s.fence.set = true
	s.fence.since = time.Now().UTC()
	s.fence.status.Store(true)
	slog.Error("access: sign-in data could not be confirmed on disk; sign-in changes are paused")
	s.held.addFenceStart(s.fence.since)
	s.loops.Go(s.fenceLoop)
}

func (s *Store) clearFence() {
	s.fence.set = false
	s.fence.status.Store(false)
}

// fenceLoop confirms the fenced state through the writer, never writing the file itself.
func (s *Store) fenceLoop() {
	h := s.fence.hooks.Load()
	if h != nil && h.onUnconfirmed != nil {
		h.onUnconfirmed()
	}
	tick := s.fence.tick
	if tick == nil {
		t := time.NewTicker(fenceInterval)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case <-tick:
		case <-s.ctx.Done():
			return
		}
		o := s.w.submit(&job{ctx: s.ctx, kind: jobConfirm, fn: func(tx *txView) error {
			tx.rewrite = true
			return nil
		}})
		switch {
		case o.err == nil:
			slog.Info("access: sign-in data is confirmed on disk again")
			if h := s.fence.hooks.Load(); h != nil && h.onConfirmed != nil {
				h.onConfirmed()
			} else {
				s.appendHeld()
			}
			return
		case s.ctx.Err() != nil:
			return
		}
	}
}

// logAppender is account.Service.AppendLog.
type logAppender func(ctx context.Context, tx account.Tx, e account.AppEntry) (account.LogEntry, error)

var errNoLogAppender = errors.New("applogin: no security log appender is bound")

// heldLog holds the reset and fence entries no request's write can carry, until appendOwnEntries
// appends them. Only the writer goroutine touches entries.
type heldLog struct {
	appendLog atomic.Pointer[logAppender]
	entries   []account.AppEntry
}

// bindLog sets the security log appender the held entries go through.
func (s *Store) bindLog(fn logAppender) { s.held.appendLog.Store(&fn) }

func (h *heldLog) add(es []account.AppEntry) { h.entries = append(h.entries, es...) }

// addFenceStart holds the fence's start unless a fence entry is held, so the earliest start not
// yet logged is kept.
func (h *heldLog) addFenceStart(since time.Time) {
	if slices.ContainsFunc(h.entries, func(e account.AppEntry) bool { return e.Kind == kindStoreFenced }) {
		return
	}
	h.entries = append(h.entries, account.AppEntry{Kind: kindStoreFenced, Detail: since.Format(time.RFC3339)})
}

func (h *heldLog) take() []account.AppEntry {
	es := h.entries
	h.entries = nil
	return es
}

func (h *heldLog) putBack(j *job) {
	h.entries = append(j.taken, h.entries...)
	j.taken = nil
}

func resetEntry(r Reset) account.AppEntry {
	return account.AppEntry{Kind: kindStoreReset, Detail: r.At.UTC().Format(time.RFC3339)}
}

// appendHeld queues appendOwnEntries carrying entries on the caller's goroutine, so calls reach
// the writer in call order; the wait for its result and the retry run on a goroutine of their own.
func (s *Store) appendHeld(entries ...account.AppEntry) {
	j := s.appendOwnEntries(entries)
	if !s.w.enqueue(j) {
		return
	}
	s.loops.Go(func() { s.awaitHeld(j) })
}

// appendOwnEntries is the own job that appends every held entry through the bound appender.
func (s *Store) appendOwnEntries(carried []account.AppEntry) *job {
	j := &job{ctx: s.ctx, kind: jobHeld, carried: carried}
	j.fn = func(tx *txView) error {
		j.taken = s.held.take()
		j.kinds = j.kinds[:0]
		for i := range j.taken {
			j.kinds = append(j.kinds, j.taken[i].Kind)
		}
		fn := s.held.appendLog.Load()
		if fn == nil {
			return errNoLogAppender
		}
		for i := range j.taken {
			if _, err := (*fn)(j.ctx, tx, j.taken[i]); err != nil {
				return err
			}
		}
		return nil
	}
	return j
}

// awaitHeld waits for j and, on a failure after it ran, logs once and resubmits every
// fenceInterval until another result or the store's end.
func (s *Store) awaitHeld(j *job) {
	o := <-j.done
	logged := false
	for heldFailed(o) && s.ctx.Err() == nil {
		if !logged {
			slog.Error("access: security log entry not saved", "kinds", j.kinds, "err", o.err)
			logged = true
		}
		t := time.NewTimer(fenceInterval)
		select {
		case <-t.C:
		case <-s.ctx.Done():
			t.Stop()
			return
		}
		j = s.appendOwnEntries(nil)
		o = s.w.submit(j)
	}
}

func heldFailed(o outcome) bool {
	if o.kind == outcomePanicked {
		return true
	}
	return o.err != nil && !errors.Is(o.err, ErrNotDurable) && !errors.Is(o.err, ErrFenced) && !errors.Is(o.err, errStoreClosed)
}
