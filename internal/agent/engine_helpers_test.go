package agent

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/bridge"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
)

// sessionFactory hands out the session each door opens; a factory returning one fake shares it across every door.
type sessionFactory func() engineSession

// fakeDoor is a test session that runs its own door; a session without one opens as handed out.
type fakeDoor interface {
	door(ctx context.Context, so *marotte.SessionOpts, eo *marotte.EngineOpts, load bool) error
}

// fakeEngineSource is an EngineSource whose every generation is a fakeEngine over one session factory. lane answers
// the engine's connection-level calls; observer serves observer passes, so a pass never closes a chat's shared fake.
type fakeEngineSource struct {
	factory  sessionFactory
	lane     *fakeBridge
	observer sessionFactory
	// startErr fails every engine Start; startGate holds it open.
	startErr  error
	startGate chan struct{}
	// afterInitialize runs inside Start between the shell timeout's two reads; onStart at its top, before the gate.
	afterInitialize func()
	onStart         func()
	// stopHook runs at the top of every engine Stop, before anything ends.
	stopHook func()
	// exitGate, when set, holds every Exit until closed: the owner retires an ended generation only after it.
	exitGate chan struct{}
	// callGate, when set, sees every engine-lane call's scripted answer and returns the one the caller gets.
	callGate func(ctx context.Context, method string, resp *marotte.RPCResponse, err error) (*marotte.RPCResponse, error)
	// withheld counts lane frames toward the position and never delivers them: a wedged engine lane.
	withheld bool
	rt       *Runtime
	engines  []*fakeEngine
	mu       sync.Mutex
}

var (
	_ EngineSource  = (*fakeEngineSource)(nil)
	_ engineSpawner = (*fakeEngineSource)(nil)
	_ engineHandle  = (*fakeEngine)(nil)
)

// fakeEngineOf is a source over factory whose engine lane is lane, or a fresh fake when lane is nil.
func fakeEngineOf(factory sessionFactory, lane *fakeBridge) *fakeEngineSource {
	if lane == nil {
		lane = newFakeBridge()
	}
	return &fakeEngineSource{factory: factory, lane: lane}
}

func (*fakeEngineSource) ChatPath() string              { return "/fake/kiro-cli-chat" }
func (*fakeEngineSource) Installed() <-chan struct{}    { return nil }
func (*fakeEngineSource) Env() []string                 { return nil }
func (*fakeEngineSource) EnvAllow() map[string]struct{} { return nil }

func (s *fakeEngineSource) spawnEngine(gen uint64, _ string, _ ...bridge.Option) engineHandle {
	e := &fakeEngine{src: s, gen: gen, done: make(chan struct{}), notifCh: make(chan marotte.Notification, 16)}
	s.mu.Lock()
	s.engines = append(s.engines, e)
	s.mu.Unlock()
	return e
}

func (s *fakeEngineSource) withholdLane(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.withheld = on
}

// setFactory replaces the factory later doors open sessions from.
func (s *fakeEngineSource) setFactory(f sessionFactory) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.factory = f
}

func (s *fakeEngineSource) sessions() sessionFactory {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.factory
}

// fakeSourceOf is the fake engine source h was built on.
func fakeSourceOf(tb testing.TB, h *Runtime) *fakeEngineSource {
	tb.Helper()
	src, ok := h.engine.source.(*fakeEngineSource)
	if !ok {
		tb.Fatalf("Setup: the runtime runs on %T, not a fake engine", h.engine.source)
	}
	return src
}

// current is the newest generation's fake, nil before the first spawn.
func (s *fakeEngineSource) current() *fakeEngine {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.engines) == 0 {
		return nil
	}
	return s.engines[len(s.engines)-1]
}

// generations counts the engines spawned.
func (s *fakeEngineSource) generations() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.engines)
}

// fakeEngine is one generation: its sessions come from the source's factory and its connection calls go to the
// source's lane. Stop runs the runtime's exit hook first, as the read loop does, then ends every session.
type fakeEngine struct {
	src      *fakeEngineSource
	opts     *marotte.EngineOpts
	done     chan struct{}
	notifCh  chan marotte.Notification
	sessions []engineSession
	exit     bridge.ExitInfo
	files    []string
	ms       int
	gen      uint64
	seq      uint64
	ended    bool
	sendMu   sync.RWMutex
	stopOnce sync.Once
	mu       sync.Mutex
}

func (e *fakeEngine) Start(ctx context.Context, o *marotte.EngineOpts) error {
	e.src.mu.Lock()
	startErr, gate, after, entered := e.src.startErr, e.src.startGate, e.src.afterInitialize, e.src.onStart
	e.src.mu.Unlock()
	if entered != nil {
		entered()
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if startErr != nil {
		return startErr
	}
	var files []string
	if o.IgnoreFiles != nil {
		files = o.IgnoreFiles(ctx)
	}
	ms := 0
	if o.TerminalTimeout != nil {
		ms = o.TerminalTimeout(ctx)
		if after != nil {
			after()
		}
		if again := o.TerminalTimeout(ctx); again != ms && e.src.lane.startSendLands(marotte.MethodTerminalSettingsChanged) {
			ms = again
		}
	}
	if len(files) == 0 || !e.src.lane.startSendLands(marotte.MethodPolicyIgnoreFilesChanged) {
		files = nil
	}
	e.mu.Lock()
	e.opts, e.files, e.ms = o, files, ms
	e.mu.Unlock()
	// The lifetime bounds the process, as the real engine's does.
	if o.Lifetime != nil {
		context.AfterFunc(o.Lifetime, func() { e.end(bridge.ExitInfo{Reason: bridge.ExitStopped}) })
	}
	return nil
}

// Stop ends the generation as a shutdown or a restart does.
func (e *fakeEngine) Stop() {
	e.src.mu.Lock()
	hook := e.src.stopHook
	e.src.mu.Unlock()
	if hook != nil {
		hook()
	}
	e.end(bridge.ExitInfo{Reason: bridge.ExitStopped})
}

// crash ends the generation as the engine ending itself does.
func (e *fakeEngine) crash() { e.end(bridge.ExitInfo{}) }

func (e *fakeEngine) end(info bridge.ExitInfo) {
	e.stopOnce.Do(func() {
		e.src.mu.Lock()
		rt := e.src.rt
		e.src.mu.Unlock()
		if rt != nil {
			rt.engine.onExit(e.gen)
		}
		e.mu.Lock()
		e.exit = info
		sessions := slices.Clone(e.sessions)
		e.mu.Unlock()
		for _, s := range sessions {
			if f, ok := s.(interface{ end() }); ok {
				f.end()
			}
		}
		e.sendMu.Lock()
		e.mu.Lock()
		e.ended = true
		e.mu.Unlock()
		close(e.notifCh)
		e.sendMu.Unlock()
		close(e.done)
	})
}

func (e *fakeEngine) Done() <-chan struct{} { return e.done }

func (e *fakeEngine) Exit() bridge.ExitInfo {
	e.src.mu.Lock()
	gate := e.src.exitGate
	e.src.mu.Unlock()
	if gate != nil {
		<-gate
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.exit
}

func (e *fakeEngine) EngineNotifCh() <-chan marotte.Notification { return e.notifCh }

func (e *fakeEngine) Call(ctx context.Context, method string, params any) (*marotte.RPCResponse, error) {
	resp, err := e.src.lane.callScript(ctx, method, params, e.deliver)
	e.src.mu.Lock()
	gate := e.src.callGate
	e.src.mu.Unlock()
	if gate != nil {
		return gate(ctx, method, resp, err)
	}
	return resp, err
}

// CallAt answers from the lane's script; the frames the call carries arrive on the engine lane first.
func (e *fakeEngine) CallAt(ctx context.Context, method string, params any) (*marotte.RPCResponse, uint64, error) {
	resp, err := e.Call(ctx, method, params)
	e.mu.Lock()
	seq := e.seq
	e.mu.Unlock()
	return resp, seq, err
}

// deliver stamps the next engine-lane position and pushes msg, as the read loop does; nothing after the end.
func (e *fakeEngine) deliver(msg *marotte.RPCResponse) {
	e.sendMu.RLock()
	defer e.sendMu.RUnlock()
	e.mu.Lock()
	if e.ended {
		e.mu.Unlock()
		return
	}
	e.seq++
	n := marotte.Notification{Msg: msg, Seq: e.seq}
	e.mu.Unlock()
	e.src.mu.Lock()
	withheld := e.src.withheld
	e.src.mu.Unlock()
	if withheld {
		return
	}
	e.notifCh <- n
}

func (e *fakeEngine) Notify(ctx context.Context, method string, params any) error {
	return e.src.lane.Notify(ctx, method, params)
}

func (e *fakeEngine) Respond(ctx context.Context, id int64, result any, err error) error {
	return e.src.lane.Respond(ctx, id, result, err)
}

func (e *fakeEngine) NewSession(ctx context.Context, o *marotte.SessionOpts, attach func(engineSession)) (engineSession, error) {
	return e.open(ctx, o, attach, false)
}

func (e *fakeEngine) LoadSession(ctx context.Context, o *marotte.SessionOpts, attach func(engineSession)) (engineSession, error) {
	return e.open(ctx, o, attach, true)
}

func (e *fakeEngine) open(ctx context.Context, o *marotte.SessionOpts, attach func(engineSession), load bool) (engineSession, error) {
	select {
	case <-e.done:
		return nil, marotte.ErrBridgeExited
	default:
	}
	factory := e.src.sessions()
	if o.Role == marotte.SessionRoleObserver {
		factory = e.src.observerFactory()
	}
	s := factory()
	if attach != nil {
		attach(s)
	}
	e.mu.Lock()
	eo := e.opts
	e.mu.Unlock()
	if d, ok := s.(fakeDoor); ok {
		if err := d.door(ctx, o, eo, load); err != nil {
			// A failed door's lane closes, so its attached forward ends.
			if f, ok := s.(interface{ end() }); ok {
				f.end()
			}
			return nil, err
		}
	}
	e.assertAtDoor(ctx, s, eo)
	e.mu.Lock()
	e.sessions = append(e.sessions, s)
	e.mu.Unlock()
	return s, nil
}

// setObserver replaces the factory later observer passes open sessions from.
func (s *fakeEngineSource) setObserver(f sessionFactory) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observer = f
}

// observerFactory serves observer passes from their own fake unless the test named one.
func (s *fakeEngineSource) observerFactory() sessionFactory {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.observer == nil {
		s.observer = func() engineSession { return newFakeBridge() }
	}
	return s.observer
}

// assertAtDoor is the door's content-collection assert: written on the session, recorded on the engine, which holds
// the value for every session.
func (e *fakeEngine) assertAtDoor(ctx context.Context, s engineSession, eo *marotte.EngineOpts) {
	if eo == nil || eo.ContentCollection == nil {
		return
	}
	enabled, _ := eo.ContentCollection(ctx)
	fails := false
	if f, ok := s.(interface{ fake() *fakeBridge }); ok {
		fails = f.fake().doorAssertFails()
	}
	lane := e.src.lane
	lane.mu.Lock()
	lane.contentCollectionApplied, lane.contentCollectionKnown = enabled, !fails
	lane.mu.Unlock()
}

func (e *fakeEngine) StartLive() (ignoreFiles []string, terminalTimeoutMs int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.files), e.ms
}

// RefreshContentCollection asserts the resolver's value on the lane unless the engine confirmed it, recording each
// write on the lane fake.
func (e *fakeEngine) RefreshContentCollection(ctx context.Context) (bool, error) {
	e.mu.Lock()
	o := e.opts
	e.mu.Unlock()
	if o == nil || o.ContentCollection == nil {
		return false, nil
	}
	enabled, readable := o.ContentCollection(ctx)
	lane := e.src.lane
	lane.mu.Lock()
	applied, known := lane.contentCollectionApplied, lane.contentCollectionKnown
	lane.mu.Unlock()
	if !readable || (known && applied == enabled) {
		return enabled, nil
	}
	params := marotte.ContentCollectionParams("", enabled)
	_, err := e.Call(ctx, marotte.MethodSetConfigOption, params)
	lane.mu.Lock()
	lane.contentCollectionWrites = append(lane.contentCollectionWrites, params["value"].(string))
	lane.contentCollectionApplied, lane.contentCollectionKnown = enabled, err == nil
	lane.mu.Unlock()
	return enabled, err
}

func (*fakeEngine) KASLogDir() string { return "" }

// newRuntimeOn builds a runtime over src and binds src's exit hook to it; Shutdown it from the test. A first engine
// that is coming up is waited for with its boot work (awaitBoot), since that work reads the fakes a test then edits.
func newRuntimeOn(ctx context.Context, workDir string, src *fakeEngineSource, chats chatRecords, opts ...Option) *Runtime {
	h := New(ctx, workDir, src, chats, opts...)
	src.mu.Lock()
	src.rt = h
	src.mu.Unlock()
	// Park the cancel-retry ladder: its untracked timers outlive the test.
	h.runs.cancelRetryBase = time.Hour
	if bootComing(h) {
		if err := awaitBoot(h); err != nil {
			panic(err)
		}
	}
	return h
}

// laneOf is s as the engine lane when it is a plain fake, so one fake answers session and connection calls alike.
func laneOf(s engineSession) *fakeBridge {
	if f, ok := s.(*fakeBridge); ok {
		return f
	}
	return nil
}

// awaitEngine waits until h's engine is ready, or fails after 5 s.
func awaitEngine(h *Runtime) bool {
	select {
	case <-h.EngineReady():
		return true
	case <-time.After(5 * time.Second):
		return false
	}
}

// hostSec is a launch's recorded host security for the Custom profile, so a test lease loads its host session.
func hostSec() *runlease.HostSecurity { return &runlease.HostSecurity{Presets: []string{}} }

func countCalls(b *fakeBridge, method string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, m := range b.calls {
		if m == method {
			n++
		}
	}
	return n
}

// startSendLands reports whether a connection notification the engine sends at start would land on b.
func (b *fakeBridge) startSendLands(method string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.notifyErrs[method] == nil
}

// oneObserver makes every later observer pass of h open on one fake, which a door reopens after each pass's close.
func oneObserver(t testing.TB, h *Runtime) *fakeBridge {
	t.Helper()
	o := newFakeBridge()
	fakeSourceOf(t, h).setObserver(func() engineSession { return o })
	return o
}

// restartPending reports whether the running engine's spawn differs from what the settings now resolve to.
func (o *engineOwner) restartPending() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.reasons) > 0
}
