package archive

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/parallel"
)

type purgeOutcome int

const (
	purgeSkipped purgeOutcome = iota // already gone; counted as neither
	purgeKept
	purgePurged
	purgeErr
)

// PurgeResult reports one pass; the scheduler times its next wake-up from it. NextDeadline is the
// earliest instant a chat kept ON AGE becomes purgeable, zero when none. A chat kept by a keep fact
// contributes none: its age deadline is already past, so a timer aimed at it would spin. KeptBy counts
// the chats past retention each reason kept.
type PurgeResult struct {
	NextDeadline time.Time
	KeptBy       map[string]int
	Purged       int
	Kept         int
	Errors       int
}

// Purge deletes the chats PurgeIdle finds idle for longer than maxAge.
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
	valid := collectPurgeEntries(entries)
	cutoff := time.Now().Add(-maxAge)
	const maxWorkers = 8
	// Per-index slots, reduced serially below, so the fan-out needs no lock.
	verdicts := make([]purgeOneResult, len(valid))
	parallel.Bounded(ctx, valid, maxWorkers, func(i int, id marotte.ChatID) {
		verdicts[i] = s.purgeOne(ctx, id, cutoff)
	})

	res := PurgeResult{KeptBy: make(map[string]int, len(keptByKinds))}
	for _, v := range verdicts {
		switch v.outcome {
		case purgePurged:
			res.Purged++
		case purgeKept:
			res.Kept++
		case purgeErr:
			res.Errors++
		case purgeSkipped:
		}
		if v.keptBy != "" {
			res.KeptBy[v.keptBy]++
		}
		if d := v.deadline; !d.IsZero() && (res.NextDeadline.IsZero() || d.Before(res.NextDeadline)) {
			res.NextDeadline = d
		}
	}
	logPurgeResult(res, maxAge)
	return res
}

func collectPurgeEntries(entries []os.DirEntry) []marotte.ChatID {
	var valid []marotte.ChatID
	for _, e := range entries {
		if !e.IsDir() || !ids.ValidChatID(e.Name()) {
			continue
		}
		valid = append(valid, marotte.ChatID(e.Name()))
	}
	return valid
}

type purgeOneResult struct {
	deadline time.Time
	keptBy   string
	outcome  purgeOutcome
}

// purgeOne asks the store for one chat's verdict; the decision and the removal are the store's, under its record lock.
// onPurge runs after that lock is released.
func (s *Service) purgeOne(ctx context.Context, chatID marotte.ChatID, cutoff time.Time) purgeOneResult {
	v, err := s.store.PurgeIdle(ctx, chatID, cutoff)
	if err != nil {
		slog.Warn("chat purge: the chat could not be read; kept", "chat_id", chatID, "error", err)
		return purgeOneResult{outcome: purgeErr}
	}
	switch v.Outcome {
	case PurgePurged:
		s.onPurge(chatID, v.SessionChain)
		return purgeOneResult{outcome: purgePurged}
	case PurgeKept:
		return purgeOneResult{outcome: purgeKept, deadline: v.Deadline, keptBy: v.KeptBy}
	case PurgeAbsent:
	}
	return purgeOneResult{outcome: purgeSkipped}
}

// logPurgeResult logs the pass and its count line. The count line is logged on every pass, zeros included, because it
// is the series an alert rule reads.
func logPurgeResult(res PurgeResult, maxAge time.Duration) {
	counts := make([]any, 0, 2*len(keptByKinds))
	for _, k := range keptByKinds {
		counts = append(counts, k, res.KeptBy[k])
	}
	slog.Info("chat purge: chats past retention kept by a keep fact", counts...)
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

// A pass is one header read plus at most one unlink per chat, so overrunning it means the
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
// from spinning: the wake-up comes from the PASS, never from the directory (a
// chat kept by a keep fact has an age deadline already past), and an idle pass
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
	if len(a.KeptBy)+len(b.KeptBy) > 0 {
		out.KeptBy = make(map[string]int, len(a.KeptBy)+len(b.KeptBy))
		for k, n := range a.KeptBy {
			out.KeptBy[k] += n
		}
		for k, n := range b.KeptBy {
			out.KeptBy[k] += n
		}
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
