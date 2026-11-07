package prewarm

// Runner.Run orchestration, NewRunner, and installOne's two arms that matter, driven through a fake
// npm script since npmBin is a parameter: its argv (no lifecycle scripts) and its throwaway tree.

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// fakeLister is a ServerLister that returns a fixed set, standing in
// for mcp.Store (importing it here would be an import cycle).
type fakeLister struct{ servers []ServerInfo }

func (f fakeLister) EnabledServers(context.Context) []ServerInfo { return f.servers }

// npxServer is the canonical prewarm candidate: enabled, stdio, npx.
func npxServer() ServerInfo {
	return ServerInfo{
		ID: "srv-1", Transport: "stdio", Command: "npx",
		Args: []string{"-y", "@scope/pkg"}, Enabled: true, Prewarm: true,
	}
}

// withSleepingNpm puts an npm on PATH whose install outlives the test until Stop kills it, so a
// queued install's reservation is still held when the test reads the table.
func withSleepingNpm(t *testing.T) {
	npm := writeFakeNpm(t, "exec sleep 60\n")
	t.Setenv("PATH", filepath.Dir(npm))
}

func reservationCount(p *Runner) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.reservations)
}

// TestRun_DedupsAgainstInFlight: a package already reserved for the same server is neither
// queued again nor changed.
func TestRun_DedupsAgainstInFlight(t *testing.T) {
	withSleepingNpm(t)
	p := NewRunner(t.Context(), fakeLister{servers: []ServerInfo{npxServer()}})
	rec := record(p, scopedPkg)
	seed(p, scopedPkg, true, "srv-1")

	p.Run(t.Context())
	n := reservationCount(p)
	ids, _ := reservedIDs(p, scopedPkg)
	p.Stop()

	if n != 1 {
		t.Errorf("reservations after Run = %d, want 1", n)
	}
	if !slices.Equal(ids, []string{"srv-1"}) {
		t.Errorf("reservation ids = %v, want [srv-1]", ids)
	}
	if got := rec.got(); len(got) != 0 {
		t.Errorf("Run re-announced an unchanged install: %v", got)
	}
}

// TestRun_IgnoresServersWithoutNpxPackage: disabled servers, non-npx
// commands, and HTTP-transport servers never schedule installs.
func TestRun_IgnoresServersWithoutNpxPackage(t *testing.T) {
	withSleepingNpm(t)
	p := NewRunner(t.Context(), fakeLister{servers: []ServerInfo{
		{
			ID: "off", Transport: "stdio", Command: "npx",
			Args: []string{"-y", "@off/pkg"}, Enabled: false, Prewarm: true,
		},
		{
			ID: "node", Transport: "stdio", Command: "node",
			Args: []string{"server.js"}, Enabled: true, Prewarm: true,
		},
		{
			ID: "http", Transport: "http", Command: "", Enabled: true, Prewarm: true,
		},
	}})

	p.Run(t.Context())
	n := reservationCount(p)
	p.Stop()

	if n != 0 {
		t.Errorf("Run scheduled %d installs for servers with no npx package", n)
	}
}

// TestNewRunner_initialisesEmpty: non-nil map invariant (a nil reservations
// map would panic on the first write).
func TestNewRunner_initialisesEmpty(t *testing.T) {
	lister := fakeLister{}

	p := NewRunner(t.Context(), lister)

	if p.Lister == nil {
		t.Error("lister not wired through")
	}
	if p.reservations == nil {
		t.Error("reservations map is nil (nil map would panic on write)")
	}
	if len(p.reservations) != 0 {
		t.Errorf("new runner has %d in-flight entries, want 0", len(p.reservations))
	}
}

// npm is opt-in (runtimes.node) and can be installed mid-process via the tools UI: a Run with no
// npm queues nothing, and a later Run once npm is on PATH queues the install.
func TestRun_SkipsWhenNpmMissingButDoesNotLatch(t *testing.T) {
	t.Setenv("PATH", "")
	p := NewRunner(t.Context(), fakeLister{servers: []ServerInfo{npxServer()}})

	p.Run(t.Context())
	if n := reservationCount(p); n != 0 {
		t.Errorf("Run with no npm queued %d installs, want 0", n)
	}

	withSleepingNpm(t)
	p.Run(t.Context())
	n := reservationCount(p)
	p.Stop()
	if n != 1 {
		t.Errorf("Run after npm arrived queued %d installs, want 1: the missing npm latched", n)
	}
}

// TestQueue_DeadContextNeverTakesASlot: a dead context must not acquire an install slot even with
// every slot free, which a select over a slot channel could not hold.
func TestQueue_DeadContextNeverTakesASlot(t *testing.T) {
	const attempts = 64

	p := NewRunner(t.Context(), fakeLister{})
	var installs atomic.Int64
	p.OnStatus = func(_ string, _ []string, state marotte.MCPPrewarmState) {
		if state == marotte.MCPPrewarmInstalling {
			installs.Add(1)
		}
	}

	dead, cancel := context.WithCancel(t.Context())
	cancel()

	for i := range attempts {
		pkg := "@scope/pkg" + strconv.Itoa(i)
		seed(p, pkg, false, "a")
		p.queue(dead, unspawnableNpm, pkg)
	}

	if got := installs.Load(); got != 0 {
		t.Errorf("queue started %d installs under a cancelled context, want 0", got)
	}
	// The reservation is handed back, so a later pass with a live context
	// retries the package instead of treating it as permanently in flight.
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.reservations) != 0 {
		t.Errorf("abandoned waits left %d in-flight reservations, want 0: %v", len(p.reservations), p.reservations)
	}
}

// unspawnableNpm is an absolute path no exec can resolve, so an install fails
// before a process starts. Tests that only exercise queue's slot accounting pass
// it as argv[0] rather than clearing PATH: Run threads its own probe's answer to
// installOne, so PATH does not decide what gets spawned.
const unspawnableNpm = "/nonexistent/npm"

// TestQueue_ReleasesEverySlot: every slot is given back, so a runner that has put twice its cap
// through queue can still take the cap at once.
func TestQueue_ReleasesEverySlot(t *testing.T) {
	p := NewRunner(t.Context(), fakeLister{})
	for i := range maxConcurrentInstalls * 2 {
		pkg := "@scope/pkg" + strconv.Itoa(i)
		seed(p, pkg, false, "a")
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		p.queue(ctx, unspawnableNpm, pkg)
		cancel()
	}

	if !p.sem.TryAcquire(maxConcurrentInstalls) {
		t.Errorf("the runner cannot take its %d-slot cap after %d installs, so a slot leaked",
			maxConcurrentInstalls, maxConcurrentInstalls*2)
	}
}

// TestInstallOne_WarmsTheCacheWithoutRunningLifecycleScripts asserts that prewarm must not execute a package's
// code; `--ignore-scripts` is what stops it.
func TestInstallOne_WarmsTheCacheWithoutRunningLifecycleScripts(t *testing.T) {
	log := filepath.Join(t.TempDir(), "argv")
	// Records argv and, separately, what the working directory looked like — the
	// tree's identity and its manifest are both properties installOne owes npm.
	npm := writeFakeNpm(t, `printf '%s\n' "$@" > `+shellQuote(log)+`
printf 'CWD=%s\n' "$PWD" >> `+shellQuote(log)+`
printf 'MANIFEST=%s\n' "$(cat package.json 2>/dev/null)" >> `+shellQuote(log)+`
`)

	var states []marotte.MCPPrewarmState
	p := NewRunner(t.Context(), fakeLister{servers: []ServerInfo{npxFor("a", "@scope/pkg")}})
	p.OnStatus = func(_ string, _ []string, s marotte.MCPPrewarmState) { states = append(states, s) }
	seed(p, "@scope/pkg", false, "a")
	p.installOne(t.Context(), npm, "@scope/pkg")

	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("fake npm wrote no log, so installOne never spawned it: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	var args []string
	var cwd, manifest string
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "CWD="):
			cwd = strings.TrimPrefix(line, "CWD=")
		case strings.HasPrefix(line, "MANIFEST="):
			manifest = strings.TrimPrefix(line, "MANIFEST=")
		default:
			args = append(args, line)
		}
	}

	if !slices.Contains(args, "--ignore-scripts") {
		t.Errorf("npm argv = %q, want --ignore-scripts — prewarm would run the tree's lifecycle scripts at boot", args)
	}
	if slices.Contains(args, "-g") || slices.Contains(args, "--global") {
		t.Errorf("npm argv = %q, want no global flag — `npx -y` never reads the global tree, so it is cost with no benefit", args)
	}
	if len(args) == 0 || args[len(args)-1] != "@scope/pkg" {
		t.Errorf("npm argv = %q, want the package last", args)
	}
	if !strings.Contains(manifest, `"private":true`) {
		t.Errorf("working directory's package.json = %q, want the private staging manifest — without one npm resolves an ancestor's tree", manifest)
	}
	if _, err := os.Stat(cwd); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("staging tree %q still exists after installOne returned (stat err = %v), so every pass leaks one", cwd, err)
	}
	if want := []marotte.MCPPrewarmState{marotte.MCPPrewarmInstalling, marotte.MCPPrewarmDone}; !slices.Equal(states, want) {
		t.Errorf("states = %v, want %v", states, want)
	}
}

// TestInstallOne_ReportsFailedWhenNpmFails pins the other arm: a non-zero npm is
// reported rather than swallowed, and the staging tree still goes.
func TestInstallOne_ReportsFailedWhenNpmFails(t *testing.T) {
	log := filepath.Join(t.TempDir(), "cwd")
	npm := writeFakeNpm(t, `printf '%s\n' "$PWD" > `+shellQuote(log)+`
exit 1
`)

	var states []marotte.MCPPrewarmState
	p := NewRunner(t.Context(), fakeLister{servers: []ServerInfo{npxFor("a", "bad-pkg")}})
	p.OnStatus = func(_ string, _ []string, s marotte.MCPPrewarmState) { states = append(states, s) }
	seed(p, "bad-pkg", false, "a")
	p.installOne(t.Context(), npm, "bad-pkg")

	if want := []marotte.MCPPrewarmState{marotte.MCPPrewarmInstalling, marotte.MCPPrewarmFailed}; !slices.Equal(states, want) {
		t.Fatalf("states = %v, want %v", states, want)
	}
	cwd, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("fake npm wrote no cwd: %v", err)
	}
	tree := strings.TrimSpace(string(cwd))
	if _, err := os.Stat(tree); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("staging tree %q survived a failed install (stat err = %v)", tree, err)
	}
}

// A script rather than a compiled helper because installOne only ever reads argv, the working
// directory and the exit status from it.
func writeFakeNpm(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "npm")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatalf("stage fake npm: %v", err)
	}
	return path
}

// shellQuote wraps s for the fake npm's single-quoted shell context. t.TempDir
// paths carry the test name, which can hold characters a bare word would split.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Stop ends an install in flight and returns only once it has handed its
// reservation back; a pass after Stop queues nothing.
func TestStop_WaitsForAnInstallInFlight(t *testing.T) {
	bin := t.TempDir()
	started := filepath.Join(t.TempDir(), "started")
	script := "#!/bin/sh\n: > " + started + "\nexec sleep 60\n"
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	p := NewRunner(t.Context(), fakeLister{servers: []ServerInfo{npxServer()}})
	var failures atomic.Int32
	p.OnStatus = func(_ string, _ []string, s marotte.MCPPrewarmState) {
		if s == marotte.MCPPrewarmFailed {
			failures.Add(1)
		}
	}

	p.Run(t.Context())
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the fake npm never started")
		}
	}
	p.Stop()

	p.mu.Lock()
	inFlight := len(p.reservations)
	p.mu.Unlock()
	if inFlight != 0 {
		t.Errorf("reservations held when Stop returned = %d, want 0 (the install was still running)", inFlight)
	}
	if got := failures.Load(); got != 1 {
		t.Errorf("failed reports when Stop returned = %d, want 1", got)
	}
	p.Run(t.Context())
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.reservations) != 0 {
		t.Errorf("a pass after Stop reserved %v", p.reservations)
	}
}

// frame is one OnStatus call as the bus would receive it.
type frame struct {
	pkg   string
	ids   []string
	state marotte.MCPPrewarmState
}

func (f frame) equal(o frame) bool {
	return f.pkg == o.pkg && f.state == o.state && slices.Equal(f.ids, o.ids)
}

// recorder is an OnStatus that keeps every frame and signals each terminal frame for watch.
type recorder struct {
	terminal chan struct{}
	watch    string
	frames   []frame
	mu       sync.Mutex
}

func record(p *Runner, watch string) *recorder {
	r := &recorder{watch: watch, terminal: make(chan struct{}, 16)}
	p.OnStatus = func(pkg string, ids []string, state marotte.MCPPrewarmState) {
		r.mu.Lock()
		r.frames = append(r.frames, frame{pkg: pkg, ids: slices.Clone(ids), state: state})
		r.mu.Unlock()
		if pkg == r.watch && state != marotte.MCPPrewarmInstalling {
			select {
			case r.terminal <- struct{}{}:
			default:
			}
		}
	}
	return r
}

func (r *recorder) got() []frame {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.frames)
}

// waitTerminal blocks until watch's install reports done or failed; Stop before that cancels it.
func (r *recorder) waitTerminal(t *testing.T) {
	t.Helper()
	select {
	case <-r.terminal:
	case <-time.After(10 * time.Second):
		t.Fatalf("no terminal frame for %q within 10s; frames so far: %v", r.watch, r.got())
	}
}

func assertFrames(t *testing.T, got, want []frame) {
	t.Helper()
	if !slices.EqualFunc(got, want, frame.equal) {
		t.Errorf("frames = %v, want %v", got, want)
	}
}

// seed stores a reservation as Run would leave it, started or still waiting for a slot.
func seed(p *Runner, pkg string, started bool, ids ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reservations[pkg] = &reservation{ids: append([]string{}, ids...), started: started}
}

// reservedIDs reports the ids pkg's reservation holds, and whether it exists.
func reservedIDs(p *Runner, pkg string) ([]string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.reservations[pkg]
	if !ok {
		return nil, false
	}
	return slices.Clone(r.ids), true
}

func npxFor(id, pkg string) ServerInfo {
	return ServerInfo{
		ID: id, Transport: "stdio", Command: "npx",
		Args: []string{"-y", pkg}, Enabled: true, Prewarm: true,
	}
}

// withoutNpm leaves Run unable to queue an install, so only joins and leaves are observable.
func withoutNpm(t *testing.T) { t.Setenv("PATH", t.TempDir()) }

func withSucceedingNpm(t *testing.T) {
	npm := writeFakeNpm(t, "exit 0\n")
	t.Setenv("PATH", filepath.Dir(npm))
}

const scopedPkg = "@scope/pkg"

func TestRun_NamesEveryServerAPackageIsInstalledFor(t *testing.T) {
	withSucceedingNpm(t)
	unticked := npxFor("c", scopedPkg)
	unticked.Prewarm = false
	disabled := npxFor("d", scopedPkg)
	disabled.Enabled = false
	p := NewRunner(t.Context(), fakeLister{servers: []ServerInfo{
		npxFor("a", scopedPkg), npxFor("b", scopedPkg), unticked, disabled,
	}})
	rec := record(p, scopedPkg)

	p.Run(t.Context())
	rec.waitTerminal(t)
	p.Stop()

	assertFrames(t, rec.got(), []frame{
		{scopedPkg, []string{"a", "b"}, marotte.MCPPrewarmInstalling},
		{scopedPkg, []string{"a", "b"}, marotte.MCPPrewarmDone},
	})
}

func TestRun_JoinsAServerToAnInstallInFlight(t *testing.T) {
	withoutNpm(t)
	p := NewRunner(t.Context(), fakeLister{servers: []ServerInfo{npxFor("a", scopedPkg), npxFor("b", scopedPkg)}})
	rec := record(p, scopedPkg)
	seed(p, scopedPkg, true, "a")

	p.Run(t.Context())

	assertFrames(t, rec.got(), []frame{{scopedPkg, []string{"a", "b"}, marotte.MCPPrewarmInstalling}})
	if ids, _ := reservedIDs(p, scopedPkg); !slices.Equal(ids, []string{"a", "b"}) {
		t.Errorf("reservation ids = %v, want [a b]", ids)
	}
	p.mu.Lock()
	n := len(p.reservations)
	p.mu.Unlock()
	if n != 1 {
		t.Errorf("reservations = %d, want 1: a join must not queue a second install", n)
	}

	p.finish(scopedPkg, marotte.MCPPrewarmDone)

	assertFrames(t, rec.got()[1:], []frame{{scopedPkg, []string{"a", "b"}, marotte.MCPPrewarmDone}})
	if _, ok := reservedIDs(p, scopedPkg); ok {
		t.Error("finish left the reservation in place, so the next pass never retries the package")
	}
}

func TestRun_AWaitingReservationJoinsSilently(t *testing.T) {
	withoutNpm(t)
	p := NewRunner(t.Context(), fakeLister{servers: []ServerInfo{npxFor("a", scopedPkg), npxFor("b", scopedPkg)}})
	rec := record(p, scopedPkg)
	seed(p, scopedPkg, false, "a")

	p.Run(t.Context())
	if got := rec.got(); len(got) != 0 {
		t.Fatalf("a join to an install not yet started emitted %v, want nothing", got)
	}

	p.start(scopedPkg)

	assertFrames(t, rec.got(), []frame{{scopedPkg, []string{"a", "b"}, marotte.MCPPrewarmInstalling}})

	// Once start has run, a join is announced.
	p.Lister = fakeLister{servers: []ServerInfo{
		npxFor("a", scopedPkg), npxFor("b", scopedPkg), npxFor("c", scopedPkg),
	}}
	p.Run(t.Context())

	got := rec.got()
	if want := (frame{scopedPkg, []string{"a", "b", "c"}, marotte.MCPPrewarmInstalling}); len(got) == 0 || !got[len(got)-1].equal(want) {
		t.Errorf("frames = %v, want the last to be %v", got, want)
	}
}

func TestRun_AMovedServerLeavesBeforeItJoins(t *testing.T) {
	withoutNpm(t)
	p := NewRunner(t.Context(), fakeLister{servers: []ServerInfo{
		npxFor("a", "pkg-y"), npxFor("b", "pkg-x"), npxFor("c", "pkg-y"),
	}})
	rec := record(p, "pkg-y")
	seed(p, "pkg-x", true, "a", "b")
	seed(p, "pkg-y", true, "c")

	p.Run(t.Context())

	assertFrames(t, rec.got(), []frame{
		{"pkg-x", []string{"a"}, marotte.MCPPrewarmDone},
		{"pkg-y", []string{"a", "c"}, marotte.MCPPrewarmInstalling},
	})
}

// A reservation every server left emits nothing at start or finish: a frame names at least one.
func TestRun_AnInstallNoServerAsksForSendsNoFrame(t *testing.T) {
	withoutNpm(t)
	a := npxFor("a", scopedPkg)
	a.Prewarm = false
	p := NewRunner(t.Context(), fakeLister{servers: []ServerInfo{a}})
	rec := record(p, scopedPkg)
	seed(p, scopedPkg, false, "a")

	p.Run(t.Context())
	p.start(scopedPkg)
	p.finish(scopedPkg, marotte.MCPPrewarmDone)

	if got := rec.got(); len(got) != 0 {
		t.Errorf("frames = %v, want none", got)
	}
}

// The store stops asking between the save and its pass's reconcile, and npm exits in that gap.
func TestFinish_AServerThatStoppedAskingGetsDoneNotTheResult(t *testing.T) {
	withoutNpm(t)
	p := NewRunner(t.Context(), fakeLister{servers: []ServerInfo{npxFor("b", scopedPkg)}})
	rec := record(p, scopedPkg)
	seed(p, scopedPkg, true, "a", "b")

	p.finish(scopedPkg, marotte.MCPPrewarmFailed)
	p.Run(t.Context())

	got := rec.got()
	assertFrames(t, got, []frame{
		{scopedPkg, []string{"a"}, marotte.MCPPrewarmDone},
		{scopedPkg, []string{"b"}, marotte.MCPPrewarmFailed},
	})
	if slices.ContainsFunc(got, func(f frame) bool {
		return f.state == marotte.MCPPrewarmFailed && slices.Contains(f.ids, "a")
	}) {
		t.Errorf("frames = %v: a, which stopped asking, was told the install failed", got)
	}
}

func TestRun_AServerEditedOntoAnotherPackageLeavesItsOldInstall(t *testing.T) {
	withSucceedingNpm(t)
	p := NewRunner(t.Context(), fakeLister{servers: []ServerInfo{npxFor("a", "pkg-y"), npxFor("b", "pkg-x")}})
	rec := record(p, "pkg-y")
	seed(p, "pkg-x", true, "a", "b")

	p.Run(t.Context())
	rec.waitTerminal(t)

	got := rec.got()
	left := slices.IndexFunc(got, frame{"pkg-x", []string{"a"}, marotte.MCPPrewarmDone}.equal)
	joined := slices.IndexFunc(got, frame{"pkg-y", []string{"a"}, marotte.MCPPrewarmInstalling}.equal)
	if left < 0 || joined < 0 || left > joined {
		t.Errorf("frames = %v, want (pkg-x [a] done) before (pkg-y [a] installing)", got)
	}
	if ids, _ := reservedIDs(p, "pkg-x"); !slices.Equal(ids, []string{"b"}) {
		t.Errorf("pkg-x reservation ids = %v, want [b]", ids)
	}

	p.finish("pkg-x", marotte.MCPPrewarmFailed)
	p.Stop()

	if last := rec.got()[len(rec.got())-1]; !last.equal(frame{"pkg-x", []string{"b"}, marotte.MCPPrewarmFailed}) {
		t.Errorf("pkg-x's result = %v, want it to name only b", last)
	}
}

func TestRun_AServerThatStopsAskingLeavesItsInstall(t *testing.T) {
	cases := []struct {
		stop func(*ServerInfo)
		name string
	}{
		{name: "prewarm unticked", stop: func(s *ServerInfo) { s.Prewarm = false }},
		{name: "disabled", stop: func(s *ServerInfo) { s.Enabled = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withoutNpm(t)
			a := npxFor("a", scopedPkg)
			tc.stop(&a)
			p := NewRunner(t.Context(), fakeLister{servers: []ServerInfo{a, npxFor("b", scopedPkg)}})
			rec := record(p, scopedPkg)
			seed(p, scopedPkg, true, "a", "b")

			p.Run(t.Context())

			assertFrames(t, rec.got(), []frame{{scopedPkg, []string{"a"}, marotte.MCPPrewarmDone}})
			if ids, _ := reservedIDs(p, scopedPkg); !slices.Equal(ids, []string{"b"}) {
				t.Errorf("reservation ids = %v, want [b]", ids)
			}
		})
	}
}

// A pass whose ctx died reads an empty store, which must not read as every server leaving.
func TestRun_ADeadPassPrunesNothing(t *testing.T) {
	withoutNpm(t)
	p := NewRunner(t.Context(), fakeLister{})
	rec := record(p, scopedPkg)
	seed(p, scopedPkg, true, "a")
	dead, cancel := context.WithCancel(t.Context())
	cancel()

	p.Run(dead)

	if got := rec.got(); len(got) != 0 {
		t.Errorf("a dead pass emitted %v, want nothing", got)
	}
	if ids, _ := reservedIDs(p, scopedPkg); !slices.Equal(ids, []string{"a"}) {
		t.Errorf("reservation ids after a dead pass = %v, want [a]", ids)
	}

	// A live pass after the dead one still runs, so the dead one released every lock.
	p.Lister = fakeLister{servers: []ServerInfo{npxFor("a", scopedPkg), npxFor("b", scopedPkg)}}
	ran := make(chan struct{})
	go func() {
		p.Run(t.Context())
		close(ran)
	}()
	select {
	case <-ran:
	case <-time.After(10 * time.Second):
		t.Fatal("a live pass after a dead one never returned: the dead pass kept a lock")
	}
	assertFrames(t, rec.got(), []frame{{scopedPkg, []string{"a", "b"}, marotte.MCPPrewarmInstalling}})
}

// gatedLister holds its first read until gate closes, then answers with the store as it was.
type gatedLister struct {
	entered chan struct{}
	gate    chan struct{}
	first   []ServerInfo
	later   []ServerInfo
	calls   atomic.Int32
}

func (g *gatedLister) EnabledServers(context.Context) []ServerInfo {
	if g.calls.Add(1) == 1 {
		close(g.entered)
		<-g.gate
		return g.first
	}
	return g.later
}

func TestRun_PassesReconcileInTheOrderTheyReadTheStore(t *testing.T) {
	withoutNpm(t)
	unticked := npxFor("a", scopedPkg)
	unticked.Prewarm = false
	lister := &gatedLister{
		entered: make(chan struct{}), gate: make(chan struct{}),
		first: []ServerInfo{npxFor("a", scopedPkg)}, later: []ServerInfo{unticked},
	}
	p := NewRunner(t.Context(), lister)
	rec := record(p, scopedPkg)
	seed(p, scopedPkg, true, "a")

	pass1, pass2 := make(chan struct{}), make(chan struct{})
	go func() { p.Run(t.Context()); close(pass1) }()
	<-lister.entered
	go func() { p.Run(t.Context()); close(pass2) }()
	// The later read must not reconcile ahead of the earlier one; give it the chance to.
	select {
	case <-pass2:
	case <-time.After(200 * time.Millisecond):
	}
	close(lister.gate)
	for _, done := range []chan struct{}{pass1, pass2} {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("a pass never returned")
		}
	}

	got := rec.got()
	var lastA *frame
	for i := len(got) - 1; i >= 0; i-- {
		if slices.Contains(got[i].ids, "a") {
			lastA = &got[i]
			break
		}
	}
	if lastA == nil || lastA.state != marotte.MCPPrewarmDone {
		t.Errorf("frames = %v, want the last frame naming a to be done (a unticked after the earlier read)", got)
	}
	if ids, _ := reservedIDs(p, scopedPkg); slices.Contains(ids, "a") {
		t.Errorf("reservation ids = %v, want no a: the store's latest read has it unticked", ids)
	}
}

// finish starts while the join's installing frame is still inside OnStatus, the one interleaving
// in which a late join could reach the bus after the terminal frame.
func TestFinish_AJoinNeverReachesTheBusAfterTheTerminalFrame(t *testing.T) {
	withoutNpm(t)
	p := NewRunner(t.Context(), fakeLister{servers: []ServerInfo{npxFor("a", scopedPkg), npxFor("b", scopedPkg)}})
	var mu sync.Mutex
	var frames []frame
	joining := make(chan struct{})
	p.OnStatus = func(name string, ids []string, state marotte.MCPPrewarmState) {
		if state == marotte.MCPPrewarmInstalling {
			close(joining)
			time.Sleep(20 * time.Millisecond) // a slow bus write, so a racing done would land first
		}
		mu.Lock()
		frames = append(frames, frame{name, slices.Clone(ids), state})
		mu.Unlock()
	}
	seed(p, scopedPkg, true, "a")

	var wg sync.WaitGroup
	wg.Go(func() { p.Run(t.Context()) })
	wg.Go(func() {
		<-joining
		p.finish(scopedPkg, marotte.MCPPrewarmDone)
	})
	wg.Wait()

	assertFrames(t, frames, []frame{
		{scopedPkg, []string{"a", "b"}, marotte.MCPPrewarmInstalling},
		{scopedPkg, []string{"a", "b"}, marotte.MCPPrewarmDone},
	})
}
