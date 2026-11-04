package agent

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/bridge"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/signin"
)

// ownerClock is a settable now.
type ownerClock struct {
	t  time.Time
	mu sync.Mutex
}

func (c *ownerClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func (c *ownerClock) advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// ownerFixture is an engine owner over src with the hooks a test names; resolve defaults to a fixed fingerprint. The
// supervisor runs under its own lifetime, ended at cleanup.
func ownerFixture(t *testing.T, src EngineSource, hooks engineHooks) *engineOwner {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	lt := &lifetime{shutdownCtx: ctx, shutdownCancel: cancel, done: make(chan struct{})}
	if hooks.resolve == nil {
		hooks.resolve = func(context.Context, string) engineSpec {
			return engineSpec{opts: &marotte.EngineOpts{Lifetime: ctx}, fingerprint: map[marotte.EngineReason]string{"k": "v"}}
		}
	}
	if hooks.options == nil {
		hooks.options = func(uint64) []bridge.Option { return nil }
	}
	o := newEngineOwner(src, lt, &hooks)
	t.Cleanup(func() {
		lt.draining.Store(true)
		cancel()
		lt.loops.Wait()
		lt.inflight.Wait()
	})
	return o
}

func waitGenerations(t *testing.T, src *fakeEngineSource, n int) *fakeEngine {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for src.generations() < n {
		if time.Now().After(deadline) {
			t.Fatalf("generations = %d, want %d", src.generations(), n)
		}
		time.Sleep(time.Millisecond)
	}
	return src.current()
}

// waitReady waits for o's generation gen to be ready.
func waitReady(t *testing.T, o *engineOwner, gen uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if h, g := o.current(); h != nil && g == gen {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("generation %d never became ready", gen)
		}
		time.Sleep(time.Millisecond)
	}
}

// managedSource is a fake engine source whose install activates when inst closes.
type managedSource struct {
	*fakeEngineSource
	inst chan struct{}
}

func (m managedSource) Installed() <-chan struct{} { return m.inst }

func TestEngineOwner_ManagedStartsWhenInstalledCloses(t *testing.T) {
	src := managedSource{fakeEngineSource: fakeEngineOf(func() engineSession { return newFakeBridge() }, nil), inst: make(chan struct{})}
	o := ownerFixture(t, src, engineHooks{})
	o.start()
	time.Sleep(20 * time.Millisecond)
	if n := src.generations(); n != 0 {
		t.Fatalf("generations before the install = %d, want 0", n)
	}
	close(src.inst)
	waitGenerations(t, src.fakeEngineSource, 1)
	waitReady(t, o, 1)
}

func TestEngineOwner_UnmanagedStartsAtConstruction(t *testing.T) {
	src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
	o := ownerFixture(t, src, engineHooks{})
	o.start()
	waitGenerations(t, src, 1)
	waitReady(t, o, 1)
}

func TestEngineOwner_UnavailableNeverStartsAndAnswersTheReason(t *testing.T) {
	o := ownerFixture(t, noEngine{}, engineHooks{})
	o.start()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, _, _, err := o.acquire(ctx)
	var te *marotte.TransportError
	if !errors.As(err, &te) || !te.Retryable || !errors.Is(err, errEngineUnavailable) {
		t.Errorf("acquire on a runtime with no kiro-cli = %v, want a retryable transport error naming the install", err)
	}
	if st := o.status(); st.State != marotte.EngineDown {
		t.Errorf("status = %q, want down", st.State)
	}
}

func TestEngineStatus_VersionOrdersEverySnapshot(t *testing.T) {
	before := time.Now().UnixMilli()
	o := ownerFixture(t, noEngine{}, engineHooks{})
	after := time.Now().UnixMilli()
	first, second := o.status().Version, o.status().Version
	if first.BootMS < before || first.BootMS > after {
		t.Errorf("boot = %d, want the owner's construction instant in [%d, %d]", first.BootMS, before, after)
	}
	if second.BootMS != first.BootMS {
		t.Errorf("boots = %d, %d, want one boot per owner", first.BootMS, second.BootMS)
	}
	if second.Seq <= first.Seq {
		t.Errorf("seq = %d then %d, want each snapshot higher than the one before", first.Seq, second.Seq)
	}
}

// A caller asking for a generation past one that ended but is not yet retired waits on the retirement itself, not
// on the ended engine's already-closed Done channel, which would turn the wait into a spin.
func TestEngineOwner_AnAcquirePastAnEndedGenerationWaitsForItsRetirement(t *testing.T) {
	src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
	retire := make(chan struct{})
	src.exitGate = retire
	o := ownerFixture(t, src, engineHooks{})
	o.start()
	first := waitGenerations(t, src, 1)
	waitReady(t, o, 1)
	first.crash()
	<-first.Done()

	h, _, w, err := o.tryAcquire(1)
	if err != nil || h != nil {
		t.Fatalf("tryAcquire(1) on the ended generation = (%v, %v), want a wait", h, err)
	}
	select {
	case <-w.ch:
		t.Fatal("the wait for an ended generation is already over before the owner retired it")
	default:
	}
	close(retire)
	select {
	case <-w.ch:
	case <-time.After(5 * time.Second):
		t.Fatal("the wait did not end when the owner retired the generation")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, gen, release, err := o.acquireAfter(ctx, 1)
	if err != nil {
		t.Fatalf("acquireAfter(1) after the retirement: %v", err)
	}
	release()
	if gen != 2 {
		t.Errorf("acquireAfter(1) took generation %d, want 2", gen)
	}
}

func TestEngineRespawn_BackoffAndReapBeforeSpawn(t *testing.T) {
	src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
	var (
		mu     sync.Mutex
		delays []time.Duration
		reaped []bool
	)
	src.onStart = func() {
		src.mu.Lock()
		prev := slices.Clone(src.engines)
		src.mu.Unlock()
		if len(prev) < 2 {
			return
		}
		select {
		case <-prev[len(prev)-2].done:
			mu.Lock()
			reaped = append(reaped, true)
			mu.Unlock()
		default:
			mu.Lock()
			reaped = append(reaped, false)
			mu.Unlock()
		}
	}
	clock := &ownerClock{t: time.Unix(1_000_000, 0)}
	o := ownerFixture(t, src, engineHooks{})
	o.now = clock.now
	o.sleep = func(_ context.Context, d time.Duration) bool {
		mu.Lock()
		delays = append(delays, d)
		mu.Unlock()
		return true
	}
	o.start()
	for gen := 1; gen <= 10; gen++ {
		waitReady(t, o, uint64(gen))
		if gen == 9 {
			clock.advance(respawnResetAfter)
		}
		waitGenerations(t, src, gen).crash()
	}
	waitReady(t, o, 11)

	mu.Lock()
	defer mu.Unlock()
	want := []time.Duration{
		time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second,
		32 * time.Second, respawnMaxDelay, time.Second,
	}
	if !slices.Equal(delays, want) {
		t.Errorf("backoff delays = %v, want %v: the first crash respawns at once, the delay doubles to 60 s, and an "+
			"engine ready for 5 minutes resets it", delays, want)
	}
	if slices.Contains(reaped, false) {
		t.Errorf("reaped before each respawn = %v, want every previous generation gone first", reaped)
	}
}

// exitLine is the one "engine exited" record the capture holds, failing unless there is exactly one.
func exitLine(t *testing.T, logs *logCapture) map[string]any {
	t.Helper()
	var lines []map[string]any
	for line := range strings.SplitSeq(logs.String(), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil && rec["msg"] == "engine exited" {
			lines = append(lines, rec)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("engine exited lines = %d, want exactly 1: %s", len(lines), logs.String())
	}
	return lines[0]
}

// exitFixture runs one generation with two resident sessions, ends it with info, and returns its exit line.
func exitFixture(t *testing.T, info *bridge.ExitInfo, oom func() (uint64, bool)) map[string]any {
	t.Helper()
	logs := captureLogs(t)
	src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
	o := ownerFixture(t, src, engineHooks{busy: func() engineBusy { return engineBusy{sessions: 2} }})
	if oom != nil {
		o.oomKill = oom
	}
	o.start()
	waitReady(t, o, 1)
	e := waitGenerations(t, src, 1)
	if info == nil {
		e.Stop()
	} else {
		e.end(*info)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), `"msg":"engine exited"`) {
		if time.Now().After(deadline) {
			t.Fatal("no engine exited line")
		}
		time.Sleep(time.Millisecond)
	}
	return exitLine(t, logs)
}

func TestEngineExit_LogsOneLineWithTheSidecarStatus(t *testing.T) {
	line := exitFixture(t, &bridge.ExitInfo{Reason: bridge.ExitEnded, SidecarExit: "exit_code=1"}, nil)
	if line["sidecar_exit"] != "exit_code=1" || line["ended_itself"] != true || line["level"] != "WARN" {
		t.Errorf("exit line = %v, want sidecar_exit=exit_code=1 ended_itself=true at WARN", line)
	}
	if line["sessions"] != float64(2) {
		t.Errorf("exit line sessions = %v, want 2: one line per death, whatever the session count", line["sessions"])
	}

	clean := exitFixture(t, nil, nil)
	if clean["ended_itself"] != false {
		t.Errorf("a stop's exit line = %v, want ended_itself=false", clean)
	}
}

// An engine another process stopped is not a crash, and a crash stays one: a non-zero exit of its own, a SIGSEGV or a
// SIGABRT, or a SIGKILL with the kernel's OOM killer at work in the container.
func TestEngineExit_ACauseForAStopMarotteDidNotSend(t *testing.T) {
	noOOM := func() (uint64, bool) { return 0, true }
	for name, tc := range map[string]struct {
		info bridge.ExitInfo
		oom  func() (uint64, bool)
		want string
	}{
		"SIGTERM":                    {info: bridge.ExitInfo{Reason: bridge.ExitEnded, SidecarSignal: syscall.SIGTERM}, want: "external_stop"},
		"SIGINT":                     {info: bridge.ExitInfo{Reason: bridge.ExitEnded, SidecarSignal: syscall.SIGINT}, want: "external_stop"},
		"SIGHUP":                     {info: bridge.ExitInfo{Reason: bridge.ExitEnded, SidecarSignal: syscall.SIGHUP}, want: "external_stop"},
		"SIGKILL":                    {info: bridge.ExitInfo{Reason: bridge.ExitEnded, SidecarSignal: syscall.SIGKILL}, oom: noOOM, want: "external_stop"},
		"SIGKILL beside an OOM kill": {info: bridge.ExitInfo{Reason: bridge.ExitEnded, SidecarSignal: syscall.SIGKILL}, oom: oomStep(), want: "crash"},
		"SIGSEGV":                    {info: bridge.ExitInfo{Reason: bridge.ExitEnded, SidecarSignal: syscall.SIGSEGV}, want: "crash"},
		"SIGABRT":                    {info: bridge.ExitInfo{Reason: bridge.ExitEnded, SidecarSignal: syscall.SIGABRT}, want: "crash"},
		"a non-zero exit":            {info: bridge.ExitInfo{Reason: bridge.ExitEnded, SidecarExit: "exit_code=1"}, want: "crash"},
	} {
		t.Run(name, func(t *testing.T) {
			line := exitFixture(t, &tc.info, tc.oom)
			if line["cause"] != tc.want || line["ended_itself"] != true {
				t.Errorf("exit line = %v, want cause=%s ended_itself=true", line, tc.want)
			}
		})
	}
}

// oomStep reads 0 container OOM kills at the spawn and 1 at the exit.
func oomStep() func() (uint64, bool) {
	var mu sync.Mutex
	n := uint64(0)
	return func() (uint64, bool) {
		mu.Lock()
		defer mu.Unlock()
		v := n
		n = 1
		return v, true
	}
}

func TestEngineExit_AKilledNodeChildIsNotLabelledOOM(t *testing.T) {
	line := exitFixture(t, &bridge.ExitInfo{Reason: bridge.ExitEnded, SidecarExit: "exit_code=1"},
		func() (uint64, bool) { return 3, true })
	if line["js_heap_oom"] != false || line["container_oom_kills"] != float64(0) {
		t.Errorf("exit line = %v, want js_heap_oom=false container_oom_kills=0", line)
	}
	for k, v := range line {
		if s, ok := v.(string); ok && strings.Contains(strings.ToLower(s), "oom") {
			t.Errorf("exit line field %s = %q names OOM for a death nothing measured as one", k, s)
		}
	}
}

func TestEngineExit_HeapOOMLineSetsJSHeapOOM(t *testing.T) {
	line := exitFixture(t, &bridge.ExitInfo{Reason: bridge.ExitEnded, SidecarExit: "exit_code=1", JSHeapOOM: true}, nil)
	if line["js_heap_oom"] != true {
		t.Errorf("exit line = %v, want js_heap_oom=true", line)
	}
}

func TestEngineExit_ContainerOOMKillsIsTheGenerationsDelta(t *testing.T) {
	var mu sync.Mutex
	count := uint64(3)
	line := exitFixture(t, &bridge.ExitInfo{Reason: bridge.ExitEnded}, func() (uint64, bool) {
		mu.Lock()
		defer mu.Unlock()
		n := count
		count = 4
		return n, true
	})
	if line["container_oom_kills"] != float64(1) {
		t.Errorf("exit line = %v, want container_oom_kills=1 (3 at spawn, 4 at exit)", line)
	}
	unreadable := exitFixture(t, &bridge.ExitInfo{Reason: bridge.ExitEnded}, func() (uint64, bool) { return 0, false })
	if _, ok := unreadable["container_oom_kills"]; ok {
		t.Errorf("exit line = %v, want no container_oom_kills when memory.events is unreadable", unreadable)
	}
}

func TestEngineExit_CarriesTheMemoryPeakAndHeapLimit(t *testing.T) {
	line := exitFixture(t, &bridge.ExitInfo{Reason: bridge.ExitEnded, RSSPeakMiB: 3364, HeapLimitMiB: 4144}, nil)
	if line["rss_peak_mib"] != float64(3364) || line["heap_limit_mib"] != float64(4144) {
		t.Errorf("exit line = %v, want rss_peak_mib=3364 heap_limit_mib=4144", line)
	}
}

func TestEngineExit_RelayWriteTimeoutLineIsFlagged(t *testing.T) {
	line := exitFixture(t, &bridge.ExitInfo{Reason: bridge.ExitEnded, RelayWriteTimeout: true}, nil)
	if line["relay_write_timeout"] != true {
		t.Errorf("exit line = %v, want relay_write_timeout=true", line)
	}
}

// The account's promotion runs before the fingerprint is read, so the new engine's own spawn is not pending.
func TestEngineSpawn_SpawnedWithIsComputedAfterPromotion(t *testing.T) {
	var mu sync.Mutex
	pending := true
	src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
	var o *engineOwner
	o = ownerFixture(t, src, engineHooks{
		spawned: func() { mu.Lock(); pending = false; mu.Unlock() },
		resolve: func(ctx context.Context, _ string) engineSpec {
			mu.Lock()
			defer mu.Unlock()
			return engineSpec{
				opts:        &marotte.EngineOpts{Lifetime: o.lt.shutdownCtx},
				fingerprint: map[marotte.EngineReason]string{marotte.EngineReasonAccountSwitch: strconv.FormatBool(pending)},
			}
		},
	})
	o.start()
	waitReady(t, o, 1)
	o.reconcile(t.Context())
	if o.restartPending() {
		t.Errorf("restart pending after a spawn that promoted the account = true, want false: reasons %v", o.status().Restart.Reasons)
	}
}

// declBackend is a sign-in backend that declares callback and spawnArgs and answers every ask unauthenticated;
// held, when set, parks that answer.
type declBackend struct {
	held     chan struct{}
	callback string
	args     []string
}

func (b *declBackend) Declaration() signin.Declaration {
	return signin.Declaration{Backend: "test", HostCallback: b.callback, SpawnArgs: b.args}
}

func (*declBackend) Begin(context.Context, signin.FlowParams) (signin.DeviceStart, error) {
	return signin.DeviceStart{}, errors.New("no flows")
}

func (*declBackend) Poll(context.Context, signin.DeviceStart) (signin.Credential, error) {
	return signin.Credential{}, errors.New("no flows")
}

func (*declBackend) Refresh(_ context.Context, c signin.Credential) (signin.Credential, error) {
	return c, nil
}
func (*declBackend) Validate(signin.Credential) error           { return nil }
func (*declBackend) Label(signin.Credential) signin.Identity    { return signin.Identity{} }
func (*declBackend) LiveSwitchable(_, _ signin.Credential) bool { return true }
func (*declBackend) Fresh(signin.Credential, time.Time) bool    { return true }
func (*declBackend) Servable(signin.Credential, time.Time) bool { return true }
func (*declBackend) Result(signin.Credential, time.Time) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

func (b *declBackend) Refusal(signin.Refusal) *marotte.RPCError {
	if b.held != nil {
		<-b.held
	}
	return &marotte.RPCError{Code: -32000, Message: "unauthenticated"}
}

// declRuntime is a runtime whose sign-in backend is b.
func declRuntime(t *testing.T, b *declBackend) (*Runtime, *fakeEngineSource) {
	t.Helper()
	src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
	cs := newTestChatStore()
	vault := openVault(t, t.TempDir(), b)
	h := newRuntimeOn(context.Background(), "/tmp/work", src, cs, WithSignIn(vault, "test"))
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	settleBoot(t, h)
	return h, src
}

func TestEngineSpawn_UsesTheDeclarationNotTheBackendID(t *testing.T) {
	_, src := declRuntime(t, &declBackend{callback: "_test/auth/token", args: []string{"--auth-method", "client"}})
	e := src.current()
	e.mu.Lock()
	args := slices.Clone(e.opts.AuthArgs)
	e.mu.Unlock()
	if !slices.Equal(args, []string{"--auth-method", "client"}) {
		t.Errorf("the spawn's auth argv = %v, want the declaration's SpawnArgs once", args)
	}
}

// scriptedSpec is a resolver answering a fingerprint {"k": value} the test moves.
type scriptedSpec struct {
	o     *engineOwner
	value string
	mu    sync.Mutex
}

func (s *scriptedSpec) set(v string) { s.mu.Lock(); s.value = v; s.mu.Unlock() }

func (s *scriptedSpec) resolve(context.Context, string) engineSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return engineSpec{
		opts:        &marotte.EngineOpts{Lifetime: s.o.lt.shutdownCtx},
		fingerprint: map[marotte.EngineReason]string{"k": s.value},
	}
}

// spawnedK is the running generation's fingerprint value for k.
func spawnedK(o *engineOwner) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.spawnedWith["k"]
}

// signalOnStart makes every engine Start signal the returned channel and wait on the returned gate.
func signalOnStart(src *fakeEngineSource) (entered <-chan struct{}, gate chan struct{}) {
	ch := make(chan struct{}, 8)
	gate = make(chan struct{})
	src.mu.Lock()
	src.onStart = func() { ch <- struct{}{} }
	src.startGate = gate
	src.mu.Unlock()
	return ch, gate
}

func awaitStart(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no engine Start was entered within 5s")
	}
}

// Two reconciles store their diffs in resolve order, so the older one finishing last cannot drop the newer's reason.
func TestEngineOwner_AnOlderResolveFinishingLastDropsNoReason(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var calls atomic.Int32
		src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
		o := ownerFixture(t, src, engineHooks{resolve: func(context.Context, string) engineSpec {
			if calls.Add(1) == 1 {
				<-release
				return engineSpec{fingerprint: map[marotte.EngineReason]string{"k": "v0"}}
			}
			return engineSpec{fingerprint: map[marotte.EngineReason]string{"k": "v2"}}
		}})
		o.mu.Lock()
		o.spawnedWith = map[marotte.EngineReason]string{"k": "v0"}
		o.mu.Unlock()

		go o.reconcile(t.Context())
		synctest.Wait()
		go o.reconcile(t.Context())
		synctest.Wait()
		close(release)
		synctest.Wait()

		if got := o.status().Restart.Reasons; !slices.Equal(got, []marotte.EngineReason{"k"}) {
			t.Errorf("reasons = %v, want [k]: the newer resolve moved k", got)
		}
	})
}

func TestEngineOwner_AValueMovedDuringTheFirstStartIsQueuedAndApplied(t *testing.T) {
	src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
	spec := &scriptedSpec{value: "v1"}
	o := ownerFixture(t, src, engineHooks{resolve: spec.resolve})
	spec.o = o
	entered, gate := signalOnStart(src)
	o.start()
	awaitStart(t, entered)

	spec.set("v2")
	o.reconcile(t.Context())
	close(gate)

	waitGenerations(t, src, 2)
	waitReady(t, o, 2)
	if got := spawnedK(o); got != "v2" {
		t.Errorf("generation 2 started on k=%q, want the value moved during generation 1's Start, v2", got)
	}
	if o.restartPending() {
		t.Errorf("reasons = %v after generation 2, want none", o.status().Restart.Reasons)
	}
}

func TestEngineOwner_AValueMovedDuringARespawnsStartIsQueuedAndApplied(t *testing.T) {
	src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
	spec := &scriptedSpec{value: "v1"}
	o := ownerFixture(t, src, engineHooks{
		resolve:   spec.resolve,
		interrupt: func(uint64, *interruptSet) interruptSet { return interruptSet{} },
	})
	spec.o = o
	o.sleep = func(context.Context, time.Duration) bool { return true }
	src.exitHook = o.onExit
	o.start()
	waitReady(t, o, 1)
	entered, gate := signalOnStart(src)

	src.current().crash()
	awaitStart(t, entered)
	spec.set("v2")
	o.reconcile(t.Context())
	close(gate)

	waitGenerations(t, src, 3)
	waitReady(t, o, 3)
	if got := spawnedK(o); got != "v2" {
		t.Errorf("generation 3 started on k=%q, want the value moved during generation 2's Start, v2", got)
	}
	if o.restartPending() {
		t.Errorf("reasons = %v after generation 3, want none", o.status().Restart.Reasons)
	}
}

// configModel stands in for the configDoc owner over a file the test breaks and repairs: every read sets the verdict
// from the file, and configHeld answers the verdict without reading, so it goes stale until the next read. Its resolver
// answers {"k": value}, a live key, with the source a broken file gives.
type configModel struct {
	o         *engineOwner
	gate      chan struct{}
	value     string
	published []marotte.EngineState
	entries   int
	mu        sync.Mutex
	broken    bool
	held      bool
}

func (m *configModel) resolve(context.Context, string) engineSpec {
	m.mu.Lock()
	m.held = m.broken
	src := fromDocument
	if m.broken {
		src = fromLastReadable
	}
	m.entries++
	gate, v := m.gate, m.value
	m.mu.Unlock()
	if gate != nil {
		<-gate
	}
	return engineSpec{opts: &marotte.EngineOpts{Lifetime: m.o.lt.shutdownCtx}, fingerprint: map[marotte.EngineReason]string{"k": v}, source: src}
}

func (m *configModel) readable(context.Context) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.held = m.broken
	return !m.broken
}

func (m *configModel) isHeld() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.held }

func (m *configModel) record(st *marotte.EngineStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.published = append(m.published, st.State)
}

func (m *configModel) set(f func(m *configModel)) { m.mu.Lock(); f(m); m.mu.Unlock() }

func (m *configModel) restartingPublished() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Contains(m.published, marotte.EngineRestarting)
}

// heldOwner is a ready generation 1 over a configModel on {"k": "v1"}.
func heldOwner(t *testing.T) (*engineOwner, *fakeEngineSource, *configModel) {
	t.Helper()
	src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
	m := &configModel{value: "v1"}
	o := ownerFixture(t, src, engineHooks{resolve: m.resolve, configReadable: m.readable, configHeld: m.isHeld, publish: m.record})
	m.o = o
	o.start()
	waitReady(t, o, 1)
	return o, src, m
}

func holdDoor(t *testing.T, o *engineOwner) func() {
	t.Helper()
	_, _, release, err := o.acquire(t.Context())
	if err != nil {
		t.Fatalf("Setup: acquire: %v", err)
	}
	return release
}

// driveRestart runs one restart synchronously, as requestRestart's goroutine does.
func driveRestart(o *engineOwner, kind restartKind) {
	o.restartMu.Lock()
	defer o.restartMu.Unlock()
	o.restart(&restartCause{Kind: kind, Reasons: []marotte.EngineReason{"k"}})
}

func assertGenerationOneKeepsItsQueue(t *testing.T, o *engineOwner, src *fakeEngineSource, after string) {
	t.Helper()
	if h, gen := o.current(); h == nil || gen != 1 || src.generations() != 1 {
		t.Errorf("after %s the engine is generation %d (ready %v, %d spawned), want generation 1 ready", after, gen, h != nil, src.generations())
	}
	if got := o.status().Restart.Reasons; !slices.Equal(got, []marotte.EngineReason{"k"}) {
		t.Errorf("after %s reasons = %v, want [k] kept", after, got)
	}
}

func TestEngineOwner_AnUnreadableSpecQueuesReasonsAndHoldsEveryRestart(t *testing.T) {
	t.Run("held_edges_and_restarts_keep_the_engine", func(t *testing.T) {
		o, src, m := heldOwner(t)
		release := holdDoor(t, o)
		m.set(func(m *configModel) { m.broken, m.value = true, "v2" })
		o.reconcile(t.Context())
		release()
		assertGenerationOneKeepsItsQueue(t, o, src, "a held reconcile and a door release")
		if m.restartingPublished() {
			t.Error("a held edge published restarting")
		}
		if !o.status().Restart.ConfigUnreadable {
			t.Error("status does not say config.json cannot be read")
		}
		driveRestart(o, restartAuto)
		assertGenerationOneKeepsItsQueue(t, o, src, "an automatic restart")
		driveRestart(o, restartConfirmed)
		assertGenerationOneKeepsItsQueue(t, o, src, "a confirmed restart with a queue")
	})
	t.Run("a_stale_verdict_is_caught_before_the_gate_closes", func(t *testing.T) {
		o, src, m := heldOwner(t)
		release := holdDoor(t, o)
		m.set(func(m *configModel) { m.value = "v2" })
		o.reconcile(t.Context())
		m.set(func(m *configModel) { m.broken = true })
		release()
		assertGenerationOneKeepsItsQueue(t, o, src, "a release over a file that broke since the last read")
		if m.restartingPublished() {
			t.Error("the restart closed the gate before it saw the file is broken")
		}
		if !o.status().Restart.ConfigUnreadable {
			t.Error("status does not say config.json cannot be read")
		}
	})
	t.Run("the_repair_recovers_at_the_next_edge_once", func(t *testing.T) {
		o, src, m := heldOwner(t)
		release := holdDoor(t, o)
		m.set(func(m *configModel) { m.broken, m.value = true, "v2" })
		o.reconcile(t.Context())
		gate := make(chan struct{})
		m.set(func(m *configModel) { m.broken, m.gate, m.entries = false, gate, 0 })
		release()
		for range 10 {
			holdDoor(t, o)()
		}
		close(gate)
		waitGenerations(t, src, 2)
		waitReady(t, o, 2)
		time.Sleep(quietWindow)
		m.mu.Lock()
		entries := m.entries
		m.mu.Unlock()
		// One recovery reconcile, the restart's own re-read, and generation 2's spawn.
		if entries != 3 {
			t.Errorf("resolves after the repair = %d, want 3: ten edges launch one recovery", entries)
		}
	})
	t.Run("a_confirmed_restart_with_nothing_queued_respawns_on_what_runs", func(t *testing.T) {
		o, src, m := heldOwner(t)
		m.set(func(m *configModel) { m.broken = true })
		o.reconcile(t.Context())
		if err := o.confirmRestart(t.Context()); err != nil {
			t.Fatalf("confirmRestart with nothing queued = %v, want nil", err)
		}
		waitGenerations(t, src, 2)
		waitReady(t, o, 2)
		if got := spawnedK(o); got != "v1" {
			t.Errorf("generation 2 started on k=%q, want what generation 1 ran, v1", got)
		}
	})
	t.Run("the_status_wire", func(t *testing.T) {
		o, _, m := heldOwner(t)
		m.set(func(m *configModel) { m.broken = true })
		o.reconcile(t.Context())
		for _, tc := range []struct {
			want   string
			broken bool
		}{{`"config_unreadable":true`, true}, {`"config_unreadable":false`, false}} {
			m.set(func(m *configModel) { m.broken = tc.broken })
			o.configReadable(t.Context())
			raw, err := json.Marshal(o.status())
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), tc.want) {
				t.Errorf("status with the file broken=%v = %s, want %s", tc.broken, raw, tc.want)
			}
		}
	})
}

// scriptRead is one config.json read the scripted cache settles: a generation, its heap marker and its verdict.
type scriptRead struct {
	gen      uint64
	heap     int
	readable bool
}

// scriptedCache wires the owner's hooks to a real lastReadable with the production expressions: every read settles
// the next scripted arrival (the last one again once the script is spent), configHeld is held, and the resolver answers
// {"heap": the settled heap, "live": a live key the test moves}.
type scriptedCache struct {
	o         *engineOwner
	live      string
	script    []scriptRead
	last      scriptRead
	published []marotte.EngineState
	l         lastReadable
	mu        sync.Mutex
}

func (c *scriptedCache) push(reads ...scriptRead) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.script = append(c.script, reads...)
}

func (c *scriptedCache) next() (configDocument, docSource) {
	c.mu.Lock()
	if len(c.script) > 0 {
		c.last, c.script = c.script[0], c.script[1:]
	}
	r := c.last
	c.mu.Unlock()
	return c.l.settle(heapDoc(r.heap), r.gen, r.readable)
}

func (c *scriptedCache) setLive(v string) { c.mu.Lock(); c.live = v; c.mu.Unlock() }

func (c *scriptedCache) resolve(context.Context, string) engineSpec {
	doc, src := c.next()
	c.mu.Lock()
	live := c.live
	c.mu.Unlock()
	return engineSpec{
		opts:        &marotte.EngineOpts{Lifetime: c.o.lt.shutdownCtx},
		fingerprint: map[marotte.EngineReason]string{"heap": strconv.Itoa(doc.engine.heapMiB), "live": live},
		source:      src,
	}
}

func (c *scriptedCache) readable(context.Context) bool {
	_, src := c.next()
	return src == fromDocument
}

func (c *scriptedCache) record(st *marotte.EngineStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.published = append(c.published, st.State)
}

// scriptedOwner is a ready generation 1 whose spawn read boot.
func scriptedOwner(t *testing.T, boot scriptRead) (*engineOwner, *fakeEngineSource, *scriptedCache) {
	t.Helper()
	src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
	c := &scriptedCache{live: "a", script: []scriptRead{boot}}
	o := ownerFixture(t, src, engineHooks{resolve: c.resolve, configReadable: c.readable, configHeld: c.l.held, publish: c.record})
	c.o = o
	o.start()
	waitReady(t, o, 1)
	return o, src, c
}

func reasonsOf(o *engineOwner) []marotte.EngineReason { return o.status().Restart.Reasons }

func TestEngineOwner_TheHoldFollowsTheCacheVerdict(t *testing.T) {
	t.Run("an_older_readable_read_during_an_unordered_failure_admits_no_restart", func(t *testing.T) {
		o, src, c := scriptedOwner(t, scriptRead{5, 2048, true})
		release := holdDoor(t, o)
		c.setLive("b")
		o.reconcile(t.Context())
		c.l.settle(&configDocument{}, 0, false)
		c.push(scriptRead{4, 1024, true})
		release()
		o.restartMu.Lock()
		o.restart(&restartCause{Kind: restartAuto, Reasons: []marotte.EngineReason{"live"}})
		o.restartMu.Unlock()
		if h, gen := o.current(); h == nil || gen != 1 || src.generations() != 1 {
			t.Errorf("after a late older readable read the engine is generation %d (%d spawned), want generation 1", gen, src.generations())
		}
		if got := reasonsOf(o); !slices.Equal(got, []marotte.EngineReason{"live"}) || !c.l.held() {
			t.Errorf("reasons = %v, held %v, want [live] kept and the hold on", got, c.l.held())
		}
		c.push(scriptRead{5, 2048, true})
		o.poke()
		waitGenerations(t, src, 2)
		waitReady(t, o, 2)
	})
	t.Run("an_unordered_failure_admits_only_a_restart_with_nothing_queued", func(t *testing.T) {
		o, src, c := scriptedOwner(t, scriptRead{5, 2048, true})
		c.push(scriptRead{0, 0, false})
		if err := o.confirmRestart(t.Context()); err != nil {
			t.Fatalf("confirmRestart with nothing queued = %v, want nil", err)
		}
		waitGenerations(t, src, 2)
		waitReady(t, o, 2)
		o.mu.Lock()
		spawned := maps.Clone(o.spawnedWith)
		o.mu.Unlock()
		if want := map[marotte.EngineReason]string{"heap": "2048", "live": "a"}; !maps.Equal(spawned, want) {
			t.Errorf("generation 2 started on %v, want generation 1's %v", spawned, want)
		}
		c.setLive("b")
		if err := o.confirmRestart(t.Context()); !errors.Is(err, errRestartHeld) {
			t.Errorf("confirmRestart with a queue over an unordered failure = %v, want errRestartHeld", err)
		}
	})
}

// A queue diffed on the last readable values is re-diffed on the repaired document before anything applies it.
func TestEngineOwner_AQueueComputedHeldIsReDiffedBeforeItApplies(t *testing.T) {
	setup := func(t *testing.T) (*engineOwner, *fakeEngineSource, *scriptedCache) {
		t.Helper()
		o, src, c := scriptedOwner(t, scriptRead{5, 0, true})
		c.push(scriptRead{6, 1024, true})
		o.refresh(t.Context())
		c.push(scriptRead{7, 0, false})
		o.refresh(t.Context())
		if got := reasonsOf(o); !slices.Equal(got, []marotte.EngineReason{"heap"}) {
			t.Fatalf("Setup: reasons = %v, want [heap]", got)
		}
		// Another cached reader (a session door) reads the repair, which reverted the heap.
		c.l.settle(heapDoc(0), 8, true)
		c.push(scriptRead{8, 0, true})
		return o, src, c
	}
	t.Run("poke_re_diffs_first", func(t *testing.T) {
		o, src, c := setup(t)
		o.poke()
		deadline := time.Now().Add(5 * time.Second)
		for {
			o.mu.Lock()
			done := !o.recovering && len(o.reasons) == 0
			o.mu.Unlock()
			if done {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the recovery reconcile never re-diffed the queue")
			}
			time.Sleep(time.Millisecond)
		}
		time.Sleep(quietWindow)
		c.mu.Lock()
		restarting := slices.Contains(c.published, marotte.EngineRestarting)
		c.mu.Unlock()
		if restarting || src.generations() != 1 {
			t.Errorf("restarting published %v, %d generations, want no restart for a reverted value", restarting, src.generations())
		}
	})
	t.Run("restart_re_diffs_first", func(t *testing.T) {
		o, src, _ := setup(t)
		o.restartMu.Lock()
		o.restart(&restartCause{Kind: restartAuto, Reasons: []marotte.EngineReason{"heap"}})
		o.restartMu.Unlock()
		time.Sleep(quietWindow)
		if h, gen := o.current(); h == nil || gen != 1 || src.generations() != 1 || len(reasonsOf(o)) != 0 {
			t.Errorf("after the restart the engine is generation %d (%d spawned), reasons %v, want generation 1 and none",
				gen, src.generations(), reasonsOf(o))
		}
	})
}

// A confirmed restart whose request was cancelled still waits for the resolve lock and re-reads before it decides.
func TestEngineOwner_ACancelledConfirmedRestartStillReReads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
		var l lastReadable
		release := make(chan struct{})
		var calls atomic.Int32
		o := ownerFixture(t, src, engineHooks{
			configHeld: l.held,
			resolve: func(context.Context, string) engineSpec {
				var src docSource
				if calls.Add(1) == 1 {
					<-release
					_, src = l.settle(&configDocument{}, 6, false)
				} else {
					_, src = l.settle(heapDoc(0), 7, true)
				}
				return engineSpec{fingerprint: map[marotte.EngineReason]string{"k": "v2"}, source: src}
			},
		})
		o.mu.Lock()
		o.state = marotte.EngineReady
		o.spawnedWith = map[marotte.EngineReason]string{"k": "v1"}
		o.latest = map[marotte.EngineReason]string{"k": "v2"}
		o.latestHeld = true
		o.reasons = []marotte.EngineReason{"k"}
		o.mu.Unlock()
		l.settle(heapDoc(0), 5, true)
		l.settle(&configDocument{}, 6, false)

		go o.refresh(t.Context())
		synctest.Wait()
		cancelled, cancel := context.WithCancel(t.Context())
		cancel()
		errc := make(chan error, 1)
		go func() { errc <- o.confirmRestart(cancelled) }()
		synctest.Wait()
		close(release)
		synctest.Wait()

		if err := <-errc; err != nil {
			t.Errorf("confirmRestart over a repaired file = %v, want nil: its refresh must re-read", err)
		}
	})
}

// A run the ended engine interrupted is busy on the next one, so the post-spawn poke publishes instead of restarting.
func TestEngineOwner_ThePostSpawnPokeWaitsForAResumedRun(t *testing.T) {
	src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
	spec := &scriptedSpec{value: "v1"}
	var busy atomic.Bool
	resumed := make(chan []string, 4)
	o := ownerFixture(t, src, engineHooks{
		resolve:   spec.resolve,
		interrupt: func(uint64, *interruptSet) interruptSet { return interruptSet{runs: []string{"wf_1"}} },
		resume:    func(_ context.Context, set interruptSet) { resumed <- set.runs },
		busy: func() engineBusy {
			if busy.Load() {
				return engineBusy{runs: []marotte.EngineBusyRun{{WorkflowID: "wf_1"}}}
			}
			return engineBusy{}
		},
	})
	spec.o = o
	o.sleep = func(context.Context, time.Duration) bool { return true }
	src.exitHook = o.onExit
	o.start()
	waitReady(t, o, 1)
	entered, gate := signalOnStart(src)
	busy.Store(true)

	src.current().crash()
	awaitStart(t, entered)
	spec.set("v2")
	o.reconcile(t.Context())
	close(gate)
	waitReady(t, o, 2)

	select {
	case runs := <-resumed:
		if !slices.Equal(runs, []string{"wf_1"}) {
			t.Errorf("resumed %v, want [wf_1]", runs)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the interrupted run was never resumed")
	}
	time.Sleep(quietWindow)
	if n := src.generations(); n != 2 {
		t.Fatalf("generations = %d while the resumed run is busy, want 2", n)
	}
	busy.Store(false)
	o.poke()
	waitGenerations(t, src, 3)
	waitReady(t, o, 3)
	if got := spawnedK(o); got != "v2" {
		t.Errorf("generation 3 started on k=%q, want v2", got)
	}
}

// waitDown waits for generation n's Start to have failed.
func waitDown(t *testing.T, o *engineOwner, src *fakeEngineSource, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		o.mu.Lock()
		down := o.state == marotte.EngineDown
		o.mu.Unlock()
		if down && src.generations() == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("generation %d's Start never failed", n)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestEngineOwner_AFailedStartKeepsTheQueue(t *testing.T) {
	src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
	spec := &scriptedSpec{value: "v1"}
	o := ownerFixture(t, src, engineHooks{resolve: spec.resolve})
	spec.o = o
	step := make(chan struct{})
	o.sleep = func(ctx context.Context, _ time.Duration) bool {
		select {
		case <-step:
			return true
		case <-ctx.Done():
			return false
		}
	}
	o.start()
	waitReady(t, o, 1)
	state := func() (spawned, latest string, reasons []marotte.EngineReason) {
		o.mu.Lock()
		defer o.mu.Unlock()
		return o.spawnedWith["k"], o.latest["k"], slices.Clone(o.reasons)
	}
	failNext := func(on bool) {
		src.mu.Lock()
		defer src.mu.Unlock()
		src.startErr = nil
		if on {
			src.startErr = errors.New("initialize failed")
		}
	}

	spec.set("v2")
	o.refresh(t.Context())
	failNext(true)
	src.current().crash()
	waitDown(t, o, src, 2)
	if spawned, latest, reasons := state(); spawned != "v1" || latest != "v2" || !slices.Equal(reasons, []marotte.EngineReason{"k"}) {
		t.Errorf("after a failed Start: spawnedWith k=%q, latest k=%q, reasons %v; want v1, v2, [k]", spawned, latest, reasons)
	}

	failNext(false)
	step <- struct{}{}
	waitReady(t, o, 3)
	if spawned, _, reasons := state(); spawned != "v2" || len(reasons) != 0 {
		t.Errorf("after the next spawn: spawnedWith k=%q, reasons %v; want v2 and none", spawned, reasons)
	}

	spec.set("v3")
	o.refresh(t.Context())
	spec.set("v2")
	failNext(true)
	src.current().crash()
	step <- struct{}{}
	waitDown(t, o, src, 4)
	if _, latest, reasons := state(); latest != "v2" || len(reasons) != 0 {
		t.Errorf("after a failed Start that resolved the running value: latest k=%q, reasons %v; want v2 and none", latest, reasons)
	}
}
