// Package prewarm fills the npm CACHE for every enabled stdio MCP server run via `npx`, at
// container start and when such a server is added or toggled, so the first chat does not pay the
// 5-15s install.
// The cache is the whole mechanism: `npx -y <pkg>` always installs into `<cache>/_npx/<hash>` and
// never runs an installed copy (npm 11.16). So install runs --ignore-scripts into a throwaway
// tree it deletes: a boot-time install would otherwise run every dependency's lifecycle scripts
// unattended, as the server. The package's own scripts still run at spawn, with a user present.
// One install runs per package, for every server that asks for it; each pass reconciles that set
// with the store, and every status frame names the servers it applies to.
package prewarm

import (
	"context"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/buffer"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/procgroup"
	"golang.org/x/sync/semaphore"
)

// ServerInfo is the narrow view of an MCP server that prewarm needs.
type ServerInfo struct {
	ID        string
	Transport string
	Command   string
	Args      []string
	Prewarm   bool
	Enabled   bool
}

// ServerLister provides the list of enabled servers for prewarm evaluation.
type ServerLister interface {
	EnabledServers(ctx context.Context) []ServerInfo
}

const npxCommand = "npx"

const maxConcurrentInstalls = 3

const tailLogBytes = buffer.DefaultOutputCap

var supportedPackageTransports = map[string]bool{"stdio": true, "": true}

// SupportsTransport reports whether marotte can install a package for a server on transport t;
// "" is the default, stdio.
func SupportsTransport(t string) bool { return supportedPackageTransports[t] }

// reservation is one package's install, queued or running, and the servers it is for. ids is
// sorted and duplicate-free, and is replaced on change, never edited in place, so a
// frame may hold it after mu is released.
type reservation struct {
	ids     []string
	started bool
}

// status is one OnStatus call.
type status struct {
	pkg   string
	state marotte.MCPPrewarmState
	ids   []string
}

// Runner owns the lifecycle of npx pre-installs.
type Runner struct {
	Lister ServerLister
	// lifetime is the RUNNER's own context, which Stop cancels, distinct from a pass's ctx: an
	// install in flight must die with either. queue composes both.
	lifetime context.Context
	sem      *semaphore.Weighted
	cancel   context.CancelFunc
	// OnStatus receives every frame: the package, the ids of the servers state applies to (sorted,
	// never empty) and the state. Frames arrive in the order the runner decided them.
	OnStatus     func(pkg string, serverIDs []string, state marotte.MCPPrewarmState)
	reservations map[string]*reservation
	installs     sync.WaitGroup
	// Lock order runMu, emitMu, mu. runMu holds a store read and the reservation change it decides
	// together (a pass's reconcile, finish's split), so the last change read the store last. emitMu is held from a frame's snapshot until
	// OnStatus returns, so frames reach the bus in the order they were decided; OnStatus never
	// runs under mu. mu also orders each installs.Go before Stop's cancel, so none is added once
	// Wait may run.
	runMu  sync.Mutex
	emitMu sync.Mutex
	mu     sync.Mutex
}

// NewRunner returns a runner. Call Run to kick an initial pass at
// container boot; subsequent passes fire from the store's onChange.
//
// ctx is the runner's lifetime and is required — it goes straight to
// context.WithCancel, which refuses a nil one at this single construction site
// rather than defaulting into installs no Stop could reach.
func NewRunner(ctx context.Context, lister ServerLister) *Runner {
	ctx, cancel := context.WithCancel(ctx)
	return &Runner{
		Lister:       lister,
		reservations: make(map[string]*reservation),
		sem:          semaphore.NewWeighted(maxConcurrentInstalls),
		lifetime:     ctx,
		cancel:       cancel,
	}
}

// Stop cancels all in-flight and future installs and waits for those in flight, each of which
// ends within installWaitDelay of its cancel.
func (p *Runner) Stop() {
	p.mu.Lock()
	p.cancel()
	p.mu.Unlock()
	p.installs.Wait()
}

// Run reconciles the in-flight installs with the store: each install is for exactly the servers
// that now ask for its package, a server that stopped asking leaves with a Done frame, one that
// starts asking joins, and a package no install holds is queued.
//
// ctx bounds THIS pass. The runner's own lifetime is separate and both are
// honoured: see the field and installOne.
func (p *Runner) Run(ctx context.Context) {
	if p.lifetime.Err() != nil {
		return
	}
	p.runMu.Lock()
	defer p.runMu.Unlock()
	candidates := p.Lister.EnabledServers(ctx)
	// A dead ctx reads an empty store, which would otherwise make every server leave.
	if ctx.Err() != nil {
		return
	}
	order, want := wanted(candidates)

	// The probe's ANSWER is threaded to installOne as argv[0], so the file that was probed is
	// the file that runs. It does not confine: npm lives in the toolbelt's agent-writable bin
	// tree, whose custody is the toolbelt's. npm is opt-in (runtimes.node) and may arrive later
	// in the process, so a miss only skips new installs this pass; joins and leaves still apply.
	npmBin, err := exec.LookPath("npm")
	if err != nil {
		slog.Debug("mcp: prewarm installs nothing this run: npm not on PATH yet", "error", err)
	}

	p.emitMu.Lock()
	defer p.emitMu.Unlock()
	p.mu.Lock()
	leaves, joins := p.reconcile(want)
	queued := 0
	if err == nil && p.lifetime.Err() == nil {
		for _, pkg := range order {
			if _, ok := p.reservations[pkg]; ok {
				continue
			}
			p.reservations[pkg] = &reservation{ids: want[pkg]}
			p.installs.Go(func() { p.queue(ctx, npmBin, pkg) })
			queued++
			slog.Debug("mcp: prewarm queued", "package", pkg, "position", queued)
		}
	}
	p.mu.Unlock()
	p.emit(leaves...)
	p.emit(joins...)
	slog.Info("mcp: prewarm pass", "candidates", len(candidates), "queued", queued)
}

// wanted maps each package the candidates ask to prewarm to the sorted ids asking, with the
// packages in first-seen order.
func wanted(candidates []ServerInfo) (order []string, want map[string][]string) {
	want = make(map[string][]string)
	for i := range candidates {
		pkg := ExtractNpxPackage(&candidates[i])
		if pkg == "" {
			continue
		}
		if _, ok := want[pkg]; !ok {
			order = append(order, pkg)
		}
		want[pkg] = append(want[pkg], candidates[i].ID)
	}
	for pkg, ids := range want {
		slices.Sort(ids)
		want[pkg] = slices.Compact(ids)
	}
	return order, want
}

// reconcile sets every reservation's ids to want's and returns the frames a started install owes:
// leavers get Done, and a join re-sends Installing for the whole set. A waiting reservation
// changes silently, since start names whoever holds it then. The caller holds mu.
func (p *Runner) reconcile(want map[string][]string) (leaves, joins []status) {
	for _, pkg := range slices.Sorted(maps.Keys(p.reservations)) {
		r := p.reservations[pkg]
		ids := want[pkg]
		left, joined := missing(r.ids, ids), missing(ids, r.ids)
		r.ids = ids
		if !r.started {
			continue
		}
		if len(left) > 0 {
			leaves = append(leaves, status{pkg: pkg, ids: left, state: marotte.MCPPrewarmDone})
		}
		if len(joined) > 0 {
			joins = append(joins, status{pkg: pkg, ids: ids, state: marotte.MCPPrewarmInstalling})
		}
	}
	return leaves, joins
}

// missing returns the ids of sorted a that sorted b lacks.
func missing(a, b []string) []string {
	return slices.DeleteFunc(slices.Clone(a), func(id string) bool {
		_, found := slices.BinarySearch(b, id)
		return found
	})
}

// emit hands each frame naming at least one server to OnStatus. The caller holds emitMu, not mu.
func (p *Runner) emit(frames ...status) {
	if p.OnStatus == nil {
		return
	}
	for _, f := range frames {
		if len(f.ids) > 0 {
			p.OnStatus(f.pkg, f.ids, f.state)
		}
	}
}

// queue waits for one of the maxConcurrentInstalls slots and installs pkg, handing the reservation
// back if the wait is abandoned. It merges the pass ctx and the runner's lifetime once, for wait
// and install. semaphore.Weighted.Acquire never grants a slot to a dead ctx, where a select on a
// slot channel could.
func (p *Runner) queue(ctx context.Context, npmBin, pkg string) {
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(p.lifetime, cancel)
	defer stop()

	if err := p.sem.Acquire(workCtx, 1); err != nil {
		p.release(pkg)
		return
	}
	defer p.sem.Release(1)
	p.installOne(workCtx, npmBin, pkg)
}

// release drops an install that never started, so the next pass may retry it. Nothing was
// badged for it, so it emits nothing.
func (p *Runner) release(pkg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.reservations, pkg)
}

// ringBuffer keeps the last Cap bytes of a stream.
type ringBuffer struct {
	buf []byte
	Cap int // exported for test construction
}

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.Cap {
		r.buf = r.buf[len(r.buf)-r.Cap:]
	}
	return len(p), nil
}

// Bytes returns the buffered content, up to Cap bytes (the most recent tail).
func (r *ringBuffer) Bytes() []byte { return r.buf }

// installOne runs pkg's install between its start and finish frames. ctx must already carry both
// the pass and the runner lifetime (queue merges them).
func (p *Runner) installOne(ctx context.Context, npmBin, pkg string) {
	p.start(pkg)
	p.finish(pkg, p.install(ctx, npmBin, pkg))
}

// start marks pkg's install as running and names its servers.
func (p *Runner) start(pkg string) {
	p.emitMu.Lock()
	defer p.emitMu.Unlock()
	p.mu.Lock()
	var ids []string
	if r, ok := p.reservations[pkg]; ok {
		r.started = true
		ids = r.ids
	}
	p.mu.Unlock()
	p.emit(status{pkg: pkg, ids: ids, state: marotte.MCPPrewarmInstalling})
}

// finish drops pkg's reservation and reports state to the servers it held that the store still
// has asking for pkg; one that stopped asking before the store read gets Done, as a leave does.
// The snapshot and the delete share one critical section: a pass that joins after it finds no
// reservation and queues a fresh install, so no server is left on Installing.
func (p *Runner) finish(pkg string, state marotte.MCPPrewarmState) {
	p.runMu.Lock()
	defer p.runMu.Unlock()
	var want map[string][]string
	if p.lifetime.Err() == nil {
		candidates := p.Lister.EnabledServers(p.lifetime)
		// A dead lifetime reads an empty store; after Stop every holder gets state.
		if p.lifetime.Err() == nil {
			_, want = wanted(candidates)
		}
	}

	p.emitMu.Lock()
	defer p.emitMu.Unlock()
	p.mu.Lock()
	var ids []string
	if r, ok := p.reservations[pkg]; ok {
		ids = r.ids
		delete(p.reservations, pkg)
	}
	p.mu.Unlock()
	var left []string
	if want != nil {
		left = missing(ids, want[pkg])
	}
	p.emit(
		status{pkg: pkg, ids: left, state: marotte.MCPPrewarmDone},
		status{pkg: pkg, ids: missing(ids, left), state: state},
	)
}

// install runs npm into a per-install THROWAWAY tree with --ignore-scripts and reports Done or
// Failed: concurrent installs into one node_modules would race, while the shared cache underneath
// is built for it.
func (p *Runner) install(ctx context.Context, npmBin, pkg string) marotte.MCPPrewarmState {
	installCtx, installCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer installCancel()

	start := time.Now()
	slog.Info("mcp: prewarm install", "package", pkg)
	fail := func(err error, out []byte) marotte.MCPPrewarmState {
		slog.Warn("mcp: prewarm failed",
			"package", pkg,
			"error", err,
			"duration_ms", time.Since(start).Milliseconds(),
			"output", tailOutput(out, 1024))
		return marotte.MCPPrewarmFailed
	}

	tree, err := stageTree()
	if err != nil {
		return fail(err, nil)
	}
	defer removeTree(tree)

	cmd := exec.CommandContext(installCtx, npmBin,
		"install", "--ignore-scripts", "--no-audit", "--no-fund", pkg)
	// npm runs node children that hold the output pipe past a head-only kill.
	procgroup.OwnGroup(cmd, installWaitDelay)
	// The staging tree is the working directory AND carries a package.json, so npm
	// resolves nothing from above it: --prefix alone leaves npm free to find an
	// ancestor manifest and install against somebody else's tree.
	cmd.Dir = tree
	ring := &ringBuffer{Cap: tailLogBytes}
	cmd.Stdout = ring
	cmd.Stderr = ring
	if err := cmd.Run(); err != nil {
		return fail(err, ring.Bytes())
	}
	slog.Info("mcp: prewarm done",
		"package", pkg, "duration_ms", time.Since(start).Milliseconds())
	return marotte.MCPPrewarmDone
}

// installWaitDelay bounds an install's Wait after its cancel or timeout kills the group.
const installWaitDelay = 5 * time.Second

// Private and unversioned, so nothing about it can be mistaken for a publishable package.
const stagingManifest = `{"name":"marotte-prewarm","version":"0.0.0","private":true}` + "\n"

// The caller removes it. A tree that cannot be given its manifest is removed HERE rather than
// handed back, so no caller's failure path has to clean up a partial one.
func stageTree() (string, error) {
	tree, err := os.MkdirTemp("", "marotte-prewarm-")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(tree, "package.json"), []byte(stagingManifest), 0o600); err != nil {
		removeTree(tree)
		return "", err
	}
	return tree, nil
}

// removeTree drops a staging tree, reporting a failure rather than swallowing it:
// a tree left behind is a slow leak of the container's temp space, and nothing
// else ever revisits the path.
func removeTree(tree string) {
	if err := os.RemoveAll(tree); err != nil {
		slog.Warn("mcp: prewarm could not remove its staging tree",
			"path", tree, "error", err)
	}
}

// npmPkgSpecRe accepts conservative npm package specs.
var npmPkgSpecRe = regexp.MustCompile(
	`^(?:@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*` +
		`(?:@[A-Za-z0-9^~><=.+_-][A-Za-z0-9^~><=.+_-]*)?$`,
)

// ExtractNpxPackage returns the npm identifier a stdio server will run
// via `npx -y <pkg>`, or "" if the server's command isn't an npx run.
func ExtractNpxPackage(s *ServerInfo) string {
	if !s.Prewarm || !s.Enabled {
		return ""
	}
	if !SupportsTransport(s.Transport) {
		return ""
	}
	if strings.TrimSpace(s.Command) != npxCommand {
		return ""
	}
	for _, arg := range s.Args {
		a := strings.TrimSpace(arg)
		if a == "" || a == "-y" || a == "--yes" {
			continue
		}
		if strings.HasPrefix(a, "-") {
			return ""
		}
		if !npmPkgSpecRe.MatchString(a) {
			return ""
		}
		return a
	}
	return ""
}

// tailOutput returns the last n bytes of output for a log line.
func tailOutput(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	tail := b[len(b)-n:]
	for len(tail) > 0 && tail[0]&0xC0 == 0x80 {
		tail = tail[1:]
	}
	return "…" + string(tail)
}
