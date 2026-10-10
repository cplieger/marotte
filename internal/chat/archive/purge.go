package archive

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/parallel"
	"github.com/cplieger/marotte/internal/subject"
)

type purgeEntry struct {
	name string
	path string
}

type purgeOutcome int

const (
	purgeSkipped purgeOutcome = iota // already gone; counted as neither
	purgeKept
	purgePurged
	purgeErr
)

// PurgeResult reports one pass; the scheduler times its next wake-up from it. NextDeadline is the
// earliest instant a chat kept ON AGE becomes purgeable, zero when none. An exempt chat contributes
// none: its age deadline is already past, so a timer aimed at it would spin.
type PurgeResult struct {
	NextDeadline time.Time
	Purged       int
	Kept         int
	Errors       int
}

// Purge deletes chats whose last activity is older than maxAge. Live and expired chats share the
// main directory; only the age test and the exemptions separate them.
func (s *Service) Purge(ctx context.Context, maxAge time.Duration) PurgeResult {
	dir := s.store.Dir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Error("chat purge: readdir",
				"dir", dir, "error", err)
		}
		return PurgeResult{}
	}
	valid := collectPurgeEntries(entries, dir)
	if len(valid) == 0 {
		return PurgeResult{}
	}

	cutoff := time.Now().Add(-maxAge)
	const maxWorkers = 8
	// Per-index slots, reduced serially below, so the fan-out needs no lock.
	outcomes := make([]purgeOutcome, len(valid))
	deadlines := make([]time.Time, len(valid))
	parallel.Bounded(ctx, valid, maxWorkers, func(i int, entry purgeEntry) {
		outcomes[i], deadlines[i] = s.purgeOne(ctx, entry, cutoff, maxAge)
	})

	var res PurgeResult
	for i, outcome := range outcomes {
		switch outcome {
		case purgePurged:
			res.Purged++
		case purgeKept:
			res.Kept++
		case purgeErr:
			res.Errors++
		case purgeSkipped:
		}
		if d := deadlines[i]; !d.IsZero() && (res.NextDeadline.IsZero() || d.Before(res.NextDeadline)) {
			res.NextDeadline = d
		}
	}
	logPurgeResult(res, maxAge)
	return res
}

// The path is the header inside, whose mtime is the age fallback for a chat that cannot be read.
func collectPurgeEntries(entries []os.DirEntry, dir string) []purgeEntry {
	var valid []purgeEntry
	for _, e := range entries {
		if !e.IsDir() || !ids.ValidChatID(e.Name()) {
			continue
		}
		valid = append(valid, purgeEntry{name: e.Name(), path: filepath.Join(dir, e.Name(), headerFileName)})
	}
	return valid
}

// purgeOne removes a single chat when its last activity is older than cutoff. The deadline is
// non-zero only for a chat kept by age. Holds the per-chat mutex across stat+remove so a concurrent
// mutate cannot race the delete.
func (s *Service) purgeOne(ctx context.Context, entry purgeEntry, cutoff time.Time, maxAge time.Duration) (purgeOutcome, time.Time) {
	if s.isLive != nil && s.isLive(marotte.ChatID(entry.name)) {
		return purgeKept, time.Time{}
	}
	// Checked BEFORE the record lock: the coordinator's operation lock precedes a chat record lock
	// everywhere else.
	if s.hasOpenTab != nil && s.hasOpenTab(marotte.ChatID(entry.name)) {
		return purgeKept, time.Time{}
	}
	m := s.store.Lock(marotte.ChatID(entry.name))
	m.Lock()
	info, err := os.Stat(entry.path)
	if err != nil {
		m.Unlock()
		if errors.Is(err, os.ErrNotExist) {
			return purgeSkipped, time.Time{}
		}
		slog.Warn("chat purge: stat", "name", entry.name, "error", err)
		return purgeErr, time.Time{}
	}
	// Capture the chain BEFORE the remove: onPurge fires once the file is gone and
	// the session ids are no longer readable.
	refTime, chain, drafting := s.purgeReferenceTime(entry, info.ModTime())
	// Store.SetDraft does not stamp UpdatedAt, so a draft is invisible to the age test.
	if drafting {
		m.Unlock()
		return purgeKept, time.Time{}
	}
	if !refTime.Before(cutoff) {
		m.Unlock()
		return purgeKept, refTime.Add(maxAge)
	}
	chatsVersion, err := s.store.Remove(marotte.ChatID(entry.name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		m.Unlock()
		slog.Warn("chat purge: remove", "chat_id", entry.name, "error", err)
		return purgeErr, time.Time{}
	}
	m.Unlock()
	if s.broadcast != nil {
		chatID := marotte.ChatID(entry.name)
		frame := marotte.NewEvent(marotte.EventChatDeleted, chatID, marotte.ChatDeletedPayload{ID: entry.name})
		frame.Subject = marotte.NewSubjectStamp(string(subject.KindChats), "", chatsVersion)
		s.broadcast(ctx, frame)
	}
	if s.onPurge != nil {
		s.onPurge(marotte.ChatID(entry.name), chain)
	}
	return purgePurged, time.Time{}
}

// Caller holds the per-chat mutex. Falls back to the file mtime only for an unreadable chat: mtime
// moves for reasons that are not activity.
func (s *Service) purgeReferenceTime(entry purgeEntry, mtime time.Time) (refTime time.Time, sessionChain []string, drafting bool) {
	h, err := s.store.LoadRetentionHeader(marotte.ChatID(entry.name))
	if err != nil {
		return mtime, nil, false
	}
	if h.UpdatedAt <= 0 {
		return mtime, h.SessionChain, h.Drafting
	}
	return time.UnixMilli(h.UpdatedAt), h.SessionChain, h.Drafting
}

func logPurgeResult(res PurgeResult, maxAge time.Duration) {
	if res.Errors > 0 {
		slog.Warn("chat purge: pass complete with errors",
			"purged", res.Purged, "kept", res.Kept, "errors", res.Errors,
			"max_age", maxAge)
		return
	}
	slog.Info("chat purge: pass complete",
		"purged", res.Purged, "kept", res.Kept,
		"max_age", maxAge)
}

// PurgeScheduler owns the retention-purge lifecycle: one goroutine with a
// trigger channel, so concurrent triggers collapse. Holds no context; Start
// takes it and threads it down.
type PurgeScheduler struct {
	svc       *Service
	retention func() time.Duration
	triggerCh chan struct{}
	stopCh    chan struct{}
	done      chan struct{}
	// sidePasses run on every wake whatever the chat retention says, each reading
	// its own setting; their deadlines and counts merge into the chat pass's.
	sidePasses []func(context.Context) PurgeResult
	// idleWait is the back-off for a pass with nothing to wait for. Owned by the
	// loop goroutine alone, so it needs no lock.
	idleWait time.Duration
	once     sync.Once
	mu       sync.Mutex
	started  bool
}

// NewPurgeScheduler builds a scheduler that purges against retention(), plus any
// side passes run on the same wake-ups.
func NewPurgeScheduler(svc *Service, retention func() time.Duration, sidePasses ...func(context.Context) PurgeResult) *PurgeScheduler {
	return &PurgeScheduler{
		svc:        svc,
		retention:  retention,
		sidePasses: sidePasses,
		triggerCh:  make(chan struct{}, 1),
		stopCh:     make(chan struct{}),
		done:       make(chan struct{}),
	}
}

// Start launches the scheduler goroutine under ctx and runs an initial
// evaluation. The loop returns when ctx is cancelled or Stop is called.
func (p *PurgeScheduler) Start(ctx context.Context) {
	p.mu.Lock()
	p.started = true
	p.mu.Unlock()
	go p.loop(ctx)
	p.Trigger()
}

// Stop signals the scheduler goroutine to exit and waits for it to finish.
func (p *PurgeScheduler) Stop() {
	p.once.Do(func() { close(p.stopCh) })
	p.mu.Lock()
	started := p.started
	p.mu.Unlock()
	if started {
		<-p.done
	}
}

// Trigger requests a purge evaluation. Safe to call from any goroutine;
// concurrent calls collapse into a single pending evaluation.
func (p *PurgeScheduler) Trigger() {
	select {
	case <-p.stopCh:
		return
	default:
	}
	select {
	case p.triggerCh <- struct{}{}:
	default:
	}
}

func (p *PurgeScheduler) loop(ctx context.Context) {
	defer close(p.done)
	var timer *time.Timer
	var timerC <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			stopTimer(timer)
			return
		case <-p.stopCh:
			stopTimer(timer)
			return
		case <-p.triggerCh:
		case <-timerC:
		}
		stopTimer(timer)
		timer, timerC = p.purgeAndReschedule(ctx)
	}
}

// stopTimer stops t if it is non-nil, for the nil timer the loop starts with.
func stopTimer(t *time.Timer) {
	if t != nil {
		t.Stop()
	}
}

// A pass is one projected read plus at most one unlink per chat, so overrunning it means the
// filesystem is wedged and re-evaluating beats waiting.
const purgeBudget = 5 * time.Minute

// purgeAndReschedule runs one purge pass (the chat pass when retention is positive, every side pass
// always) and always returns an armed timer: a nil timer channel would leave Trigger as the loop's
// only wake-up.
func (p *PurgeScheduler) purgeAndReschedule(ctx context.Context) (timer *time.Timer, timerC <-chan time.Time) {
	retention := p.retention()
	var res PurgeResult
	purgeCtx, purgeCancel := context.WithTimeout(ctx, purgeBudget)
	if retention > 0 {
		res = p.svc.Purge(purgeCtx, retention)
	}
	for _, pass := range p.sidePasses {
		res = mergePurgeResults(res, pass(purgeCtx))
	}
	purgeCancel()
	wait := p.armWait(retention, res)
	slog.Debug("chat purge scheduled", "in", wait, "retention", retention,
		"purged", res.Purged, "kept", res.Kept, "has_deadline", !res.NextDeadline.IsZero())
	t := time.NewTimer(wait)
	return t, t.C
}

// Wait bounds. maxWait is how stale an armed wake-up can be after a retention
// change (the settings path does not Trigger), and the re-check interval when
// retention is off. idleBase doubles per consecutive idle pass, up to maxWait.
const (
	minWait  = 5 * time.Second
	maxWait  = 1 * time.Hour
	idleBase = 1 * time.Minute
)

// armWait is how long the loop sleeps before its next pass. Two rules keep it
// from spinning: the wake-up comes from the PASS, never from the directory (an
// exempt chat's mtime-derived deadline is permanently past), and an idle pass
// backs off, since only an unobserved change can answer it differently.
func (p *PurgeScheduler) armWait(retention time.Duration, res PurgeResult) time.Duration {
	if !res.NextDeadline.IsZero() {
		p.idleWait = 0
		return min(max(time.Until(res.NextDeadline), minWait), maxWait)
	}
	if retention <= 0 {
		// Keep-forever: re-check on the ceiling so turning retention back on takes
		// effect within one interval.
		p.idleWait = 0
		return maxWait
	}
	if res.Purged > 0 {
		p.idleWait = 0
	}
	p.idleWait = nextIdleWait(p.idleWait)
	return p.idleWait
}

func mergePurgeResults(a, b PurgeResult) PurgeResult {
	out := PurgeResult{
		Purged: a.Purged + b.Purged,
		Kept:   a.Kept + b.Kept,
		Errors: a.Errors + b.Errors,
	}
	switch {
	case a.NextDeadline.IsZero():
		out.NextDeadline = b.NextDeadline
	case b.NextDeadline.IsZero() || a.NextDeadline.Before(b.NextDeadline):
		out.NextDeadline = a.NextDeadline
	default:
		out.NextDeadline = b.NextDeadline
	}
	return out
}

func nextIdleWait(current time.Duration) time.Duration {
	if current <= 0 {
		return idleBase
	}
	return min(current*2, maxWait)
}
