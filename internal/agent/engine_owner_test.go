package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
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
	vault := signin.Open(t.Context(), t.TempDir(), b)
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
