package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/bridge"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/memlimit"
)

// errEngineUnavailable is the readiness answer while no kiro-cli is installed.
var errEngineUnavailable = errors.New("kiro-cli is not available yet: marotte is still installing it, or the install failed (see /api/health)")

const (
	respawnBaseDelay = time.Second
	respawnMaxDelay  = 60 * time.Second
	// respawnResetAfter: an engine that stayed ready this long resets the backoff, so a later crash retries at once.
	respawnResetAfter = 5 * time.Minute
	// restartDoorWait bounds how long a restart waits for session doors and engine calls already in flight.
	restartDoorWait = 10 * time.Second
)

// engineSpec is what one spawn resolves: the options and the fingerprint of everything in them that is not live.
type engineSpec struct {
	opts        *marotte.EngineOpts
	fingerprint map[marotte.EngineReason]string
}

type restartKind int

// The restart kinds.
const (
	restartAuto restartKind = iota + 1
	restartConfirmed
)

type restartCause struct {
	Reasons []marotte.EngineReason
	Kind    restartKind
}

// engineHooks are the runtime's parts of the engine's life, each wired once at construction.
type engineHooks struct {
	// resolve reads the current engine options and fingerprint; spawned runs before it, so the vault's promotion is
	// in the fingerprint.
	resolve func(ctx context.Context, path string) engineSpec
	spawned func()
	// options are the bridge options of one generation (router, exit hook, detach handler); attach starts the
	// engine lane's consumer before Start writes initialize.
	options func(gen uint64) []bridge.Option
	attach  func(h engineHandle, gen uint64)
	// interrupt records what an end of generation gen interrupts (memory only, never blocking); held keeps a
	// restart's snapshot, which the end grows with start evidence read after it.
	interrupt func(gen uint64, held *interruptSet) interruptSet
	// claim marks each snapshotted chat's open turns interrupted by cause before the engine stops.
	claim func(set interruptSet, cause string)
	// resume drives the snapshotted runs once the next engine is ready.
	resume func(ctx context.Context, set interruptSet)
	// busy is what is running now: quiescence, the status's busy list and the exit line read it.
	busy func() engineBusy
	// publish broadcasts a status change.
	publish func(*marotte.EngineStatus)
	// ready runs once a generation is ready, on the supervisor, so it must not block; ended once a generation ended
	// and was reaped.
	ready func(gen uint64)
	ended func(gen uint64)
}

// engineBusy is what a restart now would interrupt, and whether anything else holds quiescence.
type engineBusy struct {
	turns []marotte.EngineBusyTurn
	runs  []marotte.EngineBusyRun
	// unsettled is true while a run mutation is in flight or unsettled.
	unsettled bool
	sessions  int
}

func (b *engineBusy) quiet() bool { return len(b.turns) == 0 && len(b.runs) == 0 && !b.unsettled }

// interruptSet is what an engine end interrupted: the runs to resume and the chats whose open turns it ended.
type interruptSet struct {
	runs  []string
	chats []marotte.ChatID
}

// engineOwner owns the one agent engine: it spawns it, gates every session door and engine call on its readiness,
// respawns it after a crash, restarts it to apply engine-wide settings, and resumes the runs an end interrupted.
type engineOwner struct {
	hooks       engineHooks
	since       time.Time
	engine      engineHandle `wiring:"optional"`
	startErr    error        `wiring:"optional"`
	source      EngineSource
	held        *interruptSet `wiring:"optional"`
	spawnedWith map[marotte.EngineReason]string
	ready       chan struct{}
	// retired closes when exited clears the engine of the generation that made it; one per ready generation.
	retired chan struct{} `wiring:"optional"`
	// firstReady closes when the first generation is ready and never reopens.
	firstReady  chan struct{}
	lt          *lifetime
	doorsIdle   chan struct{}
	wake        chan struct{}
	restartWait chan struct{}
	cause       *restartCause `wiring:"optional"`
	// sleep waits d or until ctx ends, reporting false on the end; a seam for the backoff tests.
	sleep    func(ctx context.Context, d time.Duration) bool
	pending  map[uint64]interruptSet
	lastExit *marotte.EngineExit `wiring:"optional"`
	oomKill  func() (uint64, bool)
	now      func() time.Time
	state    marotte.EngineState
	// reasons is the last computed restart-pending set; nil when none.
	reasons     []marotte.EngineReason
	liveLock    surfaceLock
	live        engineLive
	oomBaseline oomBaseline
	gen         uint64
	boot        int64
	statusSeq   uint64
	doors       int
	firstOnce   sync.Once
	mu          sync.Mutex
	restartMu   sync.Mutex
	// statusMu orders whole snapshots, the busy read included, so a higher statusSeq never carries older state.
	statusMu sync.Mutex
	// unavailable: no kiro-cli at construction and no managed install to wait for, so the engine never starts.
	unavailable bool
}

func newEngineOwner(source EngineSource, lt *lifetime, hooks *engineHooks) *engineOwner {
	o := &engineOwner{
		source:     source,
		lt:         lt,
		hooks:      *hooks,
		oomKill:    memlimit.OOMKills,
		sleep:      sleepCtx,
		now:        time.Now,
		ready:      make(chan struct{}),
		firstReady: make(chan struct{}),
		wake:       make(chan struct{}, 1),
		pending:    map[uint64]interruptSet{},
		state:      marotte.EngineStarting,
	}
	o.since = o.now()
	o.boot = o.since.UnixMilli()
	o.unavailable = source.Installed() == nil && source.ChatPath() == ""
	if o.unavailable {
		o.state = marotte.EngineDown
	}
	return o
}

// start runs the supervisor under the runtime's loops; an unavailable runtime never starts one.
func (o *engineOwner) start() {
	if o.unavailable {
		return
	}
	o.lt.loops.Go(o.supervise)
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (o *engineOwner) supervise() {
	ctx := o.lt.shutdownCtx
	if inst := o.source.Installed(); inst != nil {
		select {
		case <-inst:
		case <-ctx.Done():
			return
		}
	}
	attempt := 0
	for o.supervising(ctx) {
		var failed bool
		attempt, failed = o.runGeneration(ctx, attempt)
		if !o.supervising(ctx) {
			return
		}
		if attempt > 0 || failed {
			if !o.backoff(ctx, attempt) {
				return
			}
			attempt++
			continue
		}
		attempt = 1
	}
}

func (o *engineOwner) supervising(ctx context.Context) bool {
	return ctx.Err() == nil && !o.lt.draining.Load()
}

// runGeneration spawns one generation and waits out its life, reporting the attempt count the backoff continues from
// (0 after a restart or a generation that stayed up respawnResetAfter) and whether its start failed.
func (o *engineOwner) runGeneration(ctx context.Context, attempt int) (next int, failed bool) {
	h, gen, err := o.spawn(ctx)
	if err != nil {
		o.startFailed(h, gen, attempt, err)
		return attempt, true
	}
	readyAt := o.now()
	<-h.Done()
	if o.exited(h, gen) || o.now().Sub(readyAt) >= respawnResetAfter {
		return 0, false
	}
	return attempt, false
}

// backoffDelay is the wait before respawn attempt n (1-based): 1 s doubling to 60 s.
func backoffDelay(n int) time.Duration {
	d := respawnBaseDelay
	for i := 1; i < n && d < respawnMaxDelay; i++ {
		d *= 2
	}
	return min(d, respawnMaxDelay)
}

// backoff waits before a respawn; a confirmed restart wakes it at once.
func (o *engineOwner) backoff(ctx context.Context, attempt int) bool {
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-o.wake:
			cancel()
		case <-wctx.Done():
		}
	}()
	o.sleep(wctx, backoffDelay(max(attempt, 1)))
	return ctx.Err() == nil
}

// spawn starts one generation. Start's failure is returned with its handle, which the caller reaps.
func (o *engineOwner) spawn(ctx context.Context) (engineHandle, uint64, error) {
	path := o.source.ChatPath()
	o.mu.Lock()
	o.gen++
	gen := o.gen
	o.state = marotte.EngineStarting
	o.mu.Unlock()
	o.publish()
	if path == "" {
		return nil, gen, errEngineUnavailable
	}
	if o.hooks.spawned != nil {
		o.hooks.spawned()
	}
	spec := o.hooks.resolve(ctx, path)
	opts := o.hooks.options(gen)
	var h engineHandle
	if sp, ok := o.source.(engineSpawner); ok {
		h = sp.spawnEngine(gen, path, opts...)
	} else {
		h = bridgeEngine{bridge.NewEngine(path, o.lt.workDir,
			append(opts, bridge.WithEnv(o.source.Env()), bridge.WithEnvAllow(o.source.EnvAllow()))...)}
	}
	if o.hooks.attach != nil {
		o.hooks.attach(h, gen)
	}
	oomAtSpawn, oomKnown := o.oomKill()
	if err := h.Start(ctx, spec.opts); err != nil {
		return h, gen, err
	}
	files, ms := h.StartLive()
	o.mu.Lock()
	o.engine = h
	o.retired = make(chan struct{})
	o.spawnedWith = spec.fingerprint
	o.reasons = nil
	o.startErr = nil
	o.state = marotte.EngineReady
	o.since = o.now()
	o.oomBaseline = oomBaseline{count: oomAtSpawn, known: oomKnown}
	close(o.ready)
	held := o.pending
	o.pending = map[uint64]interruptSet{}
	o.mu.Unlock()
	o.liveLock.lock(context.Background())
	o.live = engineLive{ignoreFiles: slices.Clone(files), terminalTimeoutMs: ms, terminalKnown: true}
	o.liveLock.unlock()
	o.publish()
	// ready only starts tracked work, so firstReady closing after it means that work is already in flight.
	if o.hooks.ready != nil {
		o.hooks.ready(gen)
	}
	o.firstOnce.Do(func() { close(o.firstReady) })
	if o.hooks.resume != nil {
		for _, set := range held {
			o.lt.goUnlessDraining(func() { o.hooks.resume(o.lt.shutdownCtx, set) })
		}
	}
	return h, gen, nil
}

// startFailed reaps a generation whose Start failed; the gate stays closed and callers keep waiting.
func (o *engineOwner) startFailed(h engineHandle, gen uint64, attempt int, err error) {
	logDir := ""
	if h != nil {
		logDir = h.KASLogDir()
		h.Stop()
		<-h.Done()
	}
	o.mu.Lock()
	o.startErr = err
	o.state = marotte.EngineDown
	o.lastExit = &marotte.EngineExit{Reason: "initialize_failed", At: o.now()}
	o.mu.Unlock()
	slog.Error("engine start failed", "generation", gen, "attempt", attempt+1,
		"kas_log_dir", logsafe.Field(logDir), "error", err)
	o.publish()
}

// exited records a generation's end: the gate closes, one engine exited line logs, and the status moves. It reports
// whether a restart ended it, which respawns with no backoff.
func (o *engineOwner) exited(h engineHandle, gen uint64) (restarted bool) {
	info := h.Exit()
	busy := o.busyNow()
	o.mu.Lock()
	cause := exitCause(&info, o.cause, o.lt.draining.Load())
	restarted = o.cause != nil
	o.cause = nil
	o.held = nil
	if o.engine == h {
		o.engine = nil
		close(o.retired)
	}
	// A restart already closed the gate on a fresh channel its waiters hold.
	select {
	case <-o.ready:
		o.ready = make(chan struct{})
	default:
	}
	o.state = marotte.EngineDown
	if restarted {
		o.state = marotte.EngineRestarting
	}
	o.lastExit = &marotte.EngineExit{Reason: cause, At: o.now()}
	base := o.oomBaseline
	if o.restartWait != nil {
		close(o.restartWait)
		o.restartWait = nil
	}
	o.mu.Unlock()
	o.logExit(&info, gen, cause, base, &busy, logsafe.Field(h.KASLogDir()))
	if o.hooks.ended != nil {
		o.hooks.ended(gen)
	}
	o.publish()
	return restarted
}

// oomBaseline is the container's oom_kill count at a generation's spawn.
type oomBaseline struct {
	count uint64
	known bool
}

func exitCause(info *bridge.ExitInfo, restart *restartCause, draining bool) string {
	switch info.Reason {
	case bridge.ExitStopped:
		switch {
		case restart != nil && restart.Kind == restartConfirmed:
			return "restart_confirmed"
		case restart != nil:
			return "restart_auto"
		case draining:
			return "shutdown"
		}
		return "stopped"
	case bridge.ExitStdinStalled:
		return "stdin_stalled"
	case bridge.ExitParseBreaker:
		return "parse_breaker"
	case bridge.ExitLaneOverflow:
		return "lane_overflow"
	}
	return "crash"
}

// logExit writes the one engine exited line per generation; the Loki engine-crash alerts match its message and
// ended_itself verbatim. container_oom_kills counts every process in the container the kernel killed for memory
// during the engine's life, so it is coincident evidence, not proof the engine was the victim.
func (o *engineOwner) logExit(info *bridge.ExitInfo, gen uint64, cause string, base oomBaseline, busy *engineBusy, logDir any) {
	attrs := []any{
		"ended_itself", info.EndedItself(),
		"uptime_s", int64(info.Uptime.Seconds()),
		"generation", gen,
		"sessions", busy.sessions,
		"open_turns", len(busy.turns),
		"executing_runs", len(busy.runs),
		"kas_log_dir", logDir,
		"cause", cause,
		"sidecar_exit", info.SidecarExit,
		"js_heap_oom", info.JSHeapOOM,
		"relay_write_timeout", info.RelayWriteTimeout,
	}
	if now, ok := o.oomKill(); ok && base.known && now >= base.count {
		attrs = append(attrs, "container_oom_kills", now-base.count)
	}
	level := slog.LevelInfo
	if info.EndedItself() {
		level = slog.LevelWarn
	}
	slog.Log(context.Background(), level, "engine exited", attrs...)
}

// onExit is the engine's exit hook: it runs on the read loop before any pending request fails or lane closes, so
// the snapshot sees nothing the end changed. A restart's own snapshot is kept and grown.
func (o *engineOwner) onExit(gen uint64) {
	if o.hooks.interrupt == nil {
		return
	}
	o.mu.Lock()
	held := o.held
	o.mu.Unlock()
	set := o.hooks.interrupt(gen, held)
	o.mu.Lock()
	o.pending[gen] = set
	o.mu.Unlock()
}

// acquire waits for the engine to be ready, bounded by ctx, and counts the caller as an in-flight door until release.
func (o *engineOwner) acquire(ctx context.Context) (h engineHandle, gen uint64, release func(), err error) {
	return o.acquireAfter(ctx, 0)
}

// acquireAfter is acquire for a generation later than after: a ready engine of an earlier one has ended, and is
// waited out until the supervisor retires it.
func (o *engineOwner) acquireAfter(ctx context.Context, after uint64) (h engineHandle, gen uint64, release func(), err error) {
	for {
		var w acquireWait
		h, gen, w, err = o.tryAcquire(after)
		switch {
		case err != nil:
			return nil, 0, nil, err
		case h != nil:
			return h, gen, o.releaseDoor, nil
		}
		select {
		case <-w.ch:
		case <-ctx.Done():
			if w.startErr != nil {
				return nil, 0, nil, fmt.Errorf("%w: %w", marotte.ErrBridgeExited, w.startErr)
			}
			return nil, 0, nil, fmt.Errorf("%w: the agent engine is not ready: %w", marotte.ErrBridgeExited, ctx.Err())
		}
	}
}

// acquireWait is what an acquire that found no usable engine waits on: the readiness gate, or the retirement of a
// ready generation no later than the caller's floor.
type acquireWait struct {
	ch       <-chan struct{}
	startErr error
}

// tryAcquire takes a door on the ready engine of a generation later than after, or says what to wait on.
func (o *engineOwner) tryAcquire(after uint64) (h engineHandle, gen uint64, w acquireWait, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	ready := o.engine != nil && o.state == marotte.EngineReady
	switch {
	case o.unavailable:
		return nil, 0, w, &marotte.TransportError{Err: errEngineUnavailable, Retryable: true}
	case ready && o.gen <= after:
		return nil, 0, acquireWait{ch: o.retired}, nil
	case ready:
		o.doors++
		return o.engine, o.gen, w, nil
	}
	return nil, 0, acquireWait{ch: o.ready, startErr: o.startErr}, nil
}

// tryDoor takes a door on the ready engine without waiting, for a caller that answers "no engine" at once; a restart
// waits for the door as for any other.
func (o *engineOwner) tryDoor() (h engineHandle, release func(), ok bool) {
	h, _, _, err := o.tryAcquire(0)
	if err != nil || h == nil {
		return nil, nil, false
	}
	return h, o.releaseDoor, true
}

func (o *engineOwner) releaseDoor() {
	o.mu.Lock()
	o.doors--
	var zero chan struct{}
	if o.doors == 0 && o.doorsIdle != nil {
		zero, o.doorsIdle = o.doorsIdle, nil
	}
	o.mu.Unlock()
	if zero != nil {
		close(zero)
	}
	o.poke()
}

// openSession creates a session on the engine under an id it mints; see engineHandle.NewSession for attach.
func (o *engineOwner) openSession(ctx context.Context, so *marotte.SessionOpts, attach func(engineSession)) (engineSession, error) {
	h, _, release, err := o.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return h.NewSession(ctx, so, attach)
}

// loadSession loads so.SessionID on the engine.
func (o *engineOwner) loadSession(ctx context.Context, so *marotte.SessionOpts, attach func(engineSession)) error {
	_, _, err := o.loadSessionAfter(ctx, 0, so, attach)
	return err
}

// loadSessionAfter loads so.SessionID on a generation later than after, reporting the generation it ran on.
func (o *engineOwner) loadSessionAfter(
	ctx context.Context, after uint64, so *marotte.SessionOpts, attach func(engineSession),
) (session engineSession, gen uint64, err error) {
	h, gen, release, err := o.acquireAfter(ctx, after)
	if err != nil {
		return nil, 0, err
	}
	defer release()
	s, err := h.LoadSession(ctx, so, attach)
	return s, gen, err
}

// callAt is one connection-level request on the engine lane.
func (o *engineOwner) callAt(ctx context.Context, method string, params any) (*marotte.RPCResponse, drainPoint, error) {
	h, gen, release, err := o.acquire(ctx)
	if err != nil {
		return nil, drainPoint{}, err
	}
	defer release()
	resp, seq, err := h.CallAt(ctx, method, params)
	return resp, drainPoint{gen: gen, seq: seq}, err
}

// current is the ready engine and its generation, or nil when none is.
func (o *engineOwner) current() (h engineHandle, gen uint64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.state != marotte.EngineReady {
		return nil, o.gen
	}
	return o.engine, o.gen
}

// stop ends the engine at shutdown and waits for its reap, bounded by ctx; the supervisor sees draining and stops.
func (o *engineOwner) stop(ctx context.Context) error {
	o.mu.Lock()
	h := o.engine
	o.mu.Unlock()
	if h == nil {
		return nil
	}
	return awaitBounded(ctx, "agent engine teardown", h.Stop)
}

// reconcile recomputes restart-pending against the running engine's spawn and restarts at once when quiescent.
func (o *engineOwner) reconcile(ctx context.Context) {
	path := o.source.ChatPath()
	o.mu.Lock()
	spawned := o.spawnedWith
	o.mu.Unlock()
	if spawned == nil || o.hooks.resolve == nil {
		return
	}
	now := o.hooks.resolve(ctx, path).fingerprint
	reasons := diffReasons(spawned, now)
	o.mu.Lock()
	changed := !slices.Equal(o.reasons, reasons)
	o.reasons = reasons
	o.mu.Unlock()
	if changed {
		o.publish()
	}
	o.poke()
}

// diffReasons is every key whose value differs, in a stable order.
func diffReasons(spawned, now map[marotte.EngineReason]string) []marotte.EngineReason {
	var out []marotte.EngineReason
	for _, k := range slices.Sorted(maps.Keys(now)) {
		if spawned[k] != now[k] {
			out = append(out, k)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(spawned)) {
		if _, ok := now[k]; !ok {
			out = append(out, k)
		}
	}
	return out
}

// poke is the edge-triggered quiescence check: every edge that can end the last running thing calls it.
func (o *engineOwner) poke() {
	o.mu.Lock()
	pending := len(o.reasons) > 0
	ready := o.state == marotte.EngineReady
	doors := o.doors
	reasons := slices.Clone(o.reasons)
	o.mu.Unlock()
	if !pending || !ready || doors > 0 {
		if pending {
			o.publish()
		}
		return
	}
	if busy := o.busyNow(); !busy.quiet() {
		o.publish()
		return
	}
	o.requestRestart(restartCause{Kind: restartAuto, Reasons: reasons})
}

func (o *engineOwner) quiescent() bool {
	o.mu.Lock()
	doors := o.doors
	o.mu.Unlock()
	busy := o.busyNow()
	return doors == 0 && busy.quiet()
}

func (o *engineOwner) busyNow() engineBusy {
	if o.hooks.busy == nil {
		return engineBusy{}
	}
	return o.hooks.busy()
}

// requestRestart admits a restart onto the runtime's lifetime and returns at once; it never runs under the caller's
// context. One restart runs at a time: a request while one runs, or once shutdown began, is refused.
func (o *engineOwner) requestRestart(cause restartCause) (admitted bool) {
	return o.lt.goClaimed(func() func() {
		if !o.restartMu.TryLock() {
			return nil
		}
		return func() {
			defer o.restartMu.Unlock()
			o.restart(&cause)
		}
	})
}

// restart: close the gate, let doors in flight finish, snapshot, claim the interrupt on open turns, then stop the
// engine; the supervisor respawns and resumes. No session/cancel and no permission answer is written: a cancelled
// permission would let KAS run on with a refused tool, and a resumed step would carry the refusal.
func (o *engineOwner) restart(cause *restartCause) {
	o.mu.Lock()
	h, gen := o.engine, o.gen
	if h == nil || o.state != marotte.EngineReady {
		o.mu.Unlock()
		select {
		case o.wake <- struct{}{}:
		default:
		}
		return
	}
	if cause.Kind == restartAuto && len(o.reasons) == 0 {
		o.mu.Unlock()
		return
	}
	o.state = marotte.EngineRestarting
	o.cause = cause
	o.ready = make(chan struct{})
	idle := o.doorsIdleLocked()
	done := make(chan struct{})
	o.restartWait = done
	o.mu.Unlock()
	o.publish()
	t := time.NewTimer(restartDoorWait)
	select {
	case <-idle:
	case <-t.C:
	}
	t.Stop()
	if cause.Kind == restartAuto && !o.quiescent() {
		o.cancelRestart()
		return
	}
	if o.hooks.interrupt != nil {
		set := o.hooks.interrupt(gen, nil)
		o.mu.Lock()
		o.held = &set
		o.mu.Unlock()
		if o.hooks.claim != nil {
			o.hooks.claim(set, engineInterruptCause(cause))
		}
	}
	slog.Info("engine restarting", "generation", gen, "confirmed", cause.Kind == restartConfirmed,
		"reasons", cause.Reasons)
	h.Stop()
	<-done
}

// cancelRestart reopens the gate of an automatic restart whose premise no longer holds.
func (o *engineOwner) cancelRestart() {
	o.mu.Lock()
	o.state = marotte.EngineReady
	o.cause = nil
	close(o.ready)
	o.restartWait = nil
	o.mu.Unlock()
	o.publish()
}

func (o *engineOwner) doorsIdleLocked() <-chan struct{} {
	if o.doors == 0 {
		c := make(chan struct{})
		close(c)
		return c
	}
	if o.doorsIdle == nil {
		o.doorsIdle = make(chan struct{})
	}
	return o.doorsIdle
}

// engineInterruptCause is the text an interrupted turn records for an engine end.
func engineInterruptCause(cause *restartCause) string {
	if cause == nil {
		return "The agent engine stopped unexpectedly before the turn finished"
	}
	names := make([]string, 0, len(cause.Reasons))
	for _, r := range cause.Reasons {
		names = append(names, engineReasonLabel(r))
	}
	if len(names) == 0 {
		return "The agent engine restarted"
	}
	return "The agent engine restarted to apply: " + strings.Join(names, ", ")
}

// status is the engine's state as GET /api/engine and engine_status carry it.
func (o *engineOwner) status() marotte.EngineStatus {
	o.statusMu.Lock()
	defer o.statusMu.Unlock()
	busy := o.busyNow()
	o.mu.Lock()
	defer o.mu.Unlock()
	o.statusSeq++
	st := marotte.EngineStatus{
		Version: marotte.EngineStatusVersion{BootMS: o.boot, Seq: o.statusSeq},
		State:   o.state,
		Since:   o.since,
		Restart: marotte.EngineRestart{Pending: len(o.reasons) > 0, Reasons: slices.Clone(o.reasons)},
		Busy:    marotte.EngineBusy{Turns: busy.turns, Runs: busy.runs},
	}
	if st.Restart.Reasons == nil {
		st.Restart.Reasons = []marotte.EngineReason{}
	}
	if st.Busy.Turns == nil {
		st.Busy.Turns = []marotte.EngineBusyTurn{}
	}
	if st.Busy.Runs == nil {
		st.Busy.Runs = []marotte.EngineBusyRun{}
	}
	if o.lastExit != nil {
		e := *o.lastExit
		st.LastExit = &e
	}
	return st
}

func (o *engineOwner) publish() {
	if o.hooks.publish != nil {
		st := o.status()
		o.hooks.publish(&st)
	}
}
