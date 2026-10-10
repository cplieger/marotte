// Package prewarm fills the npm CACHE for every enabled stdio MCP server run via `npx`, at
// container start and when such a server is added or toggled, so the first chat does not pay the
// 5-15s install.
// The cache is the whole mechanism: `npx -y <pkg>` always installs into `<cache>/_npx/<hash>` and
// never runs an installed copy (npm 11.16). So installOne runs --ignore-scripts into a throwaway
// tree it deletes: a boot-time install would otherwise run every dependency's lifecycle scripts
// unattended, as the server. The package's own scripts still run at spawn, with a user present.
package prewarm

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cplieger/marotte/internal/buffer"
	"golang.org/x/sync/semaphore"
)

// ServerInfo is the narrow view of an MCP server that prewarm needs.
type ServerInfo struct {
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

// State describes the phase of a prewarm install for UI surfacing.
type State string

// installing and the following constants define the valid State values for a prewarm install lifecycle.
const (
	installing State = "installing"
	done       State = "done"
	failed     State = "failed"
)

// Runner owns the lifecycle of npx pre-installs.
type Runner struct {
	Lister  ServerLister
	running map[string]struct{}
	sem     *semaphore.Weighted
	// lifetime is the RUNNER's own context, which Stop cancels, distinct from a pass's ctx: an
	// install in flight must die with either. queue composes both.
	lifetime context.Context
	cancel   context.CancelFunc
	OnStatus func(pkg string, state State)
	mu       sync.Mutex
	Disabled atomic.Bool
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
		Lister:   lister,
		running:  make(map[string]struct{}),
		sem:      semaphore.NewWeighted(maxConcurrentInstalls),
		lifetime: ctx,
		cancel:   cancel,
	}
}

// Stop cancels all in-flight and future installs.
func (p *Runner) Stop() {
	p.cancel()
}

// Run enumerates enabled prewarm-flagged npx servers and kicks off a
// background install for each.
//
// ctx bounds THIS pass. The runner's own lifetime is separate and both are
// honoured: see the field and installOne.
func (p *Runner) Run(ctx context.Context) {
	if p.lifetime.Err() != nil {
		return
	}
	if p.Disabled.Load() {
		return
	}
	// The probe's ANSWER is threaded to installOne as argv[0] instead of being
	// discarded and the bare name re-resolved per package: one pass resolves once,
	// and the file that was probed is the file that runs.
	//
	// It does NOT confine. npm is installed by the toolbelt engine into
	// /config/tools/bin, which IS PATH[0], so an absolute pin names the same
	// agent-writable file a PATH lookup finds. Confining it is custody on the
	// toolbelt bin tree — toolbelt's question, not this file's.
	npmBin, err := exec.LookPath("npm")
	if err != nil {
		// npm is opt-in (runtimes.node). It may be installed later in
		// the same process lifetime via the tools UI, so DON'T latch
		// Disabled here — just skip this run. The next Run re-probes
		// and prewarm comes alive once node is enabled.
		slog.Debug("mcp: prewarm skipped this run: npm not on PATH yet", "error", err)
		return
	}

	candidates := p.Lister.EnabledServers(ctx)
	queued := 0
	for i := range candidates {
		pkg := ExtractNpxPackage(candidates[i])
		if pkg == "" {
			continue
		}
		if !p.reserve(pkg) {
			continue
		}
		queued++
		slog.Debug("mcp: prewarm queued", "package", pkg, "position", queued)
		go p.queue(ctx, npmBin, pkg)
	}
	slog.Info("mcp: prewarm pass", "candidates", len(candidates), "queued", queued)
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

func (p *Runner) reserve(pkg string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.running[pkg]; ok {
		return false
	}
	p.running[pkg] = struct{}{}
	return true
}

// release drops pkg from the in-flight set, so the next pass may retry it.
func (p *Runner) release(pkg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.running, pkg)
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

// ctx must already carry both the pass and the runner lifetime (queue merges them). The install
// goes into a per-install THROWAWAY tree with --ignore-scripts: concurrent installs into one
// node_modules would race, while the shared cache underneath is built for it.
func (p *Runner) installOne(ctx context.Context, npmBin, pkg string) {
	defer p.release(pkg)

	installCtx, installCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer installCancel()

	start := time.Now()
	slog.Info("mcp: prewarm install", "package", pkg)
	if p.OnStatus != nil {
		p.OnStatus(pkg, installing)
	}
	fail := func(err error, out []byte) {
		slog.Warn("mcp: prewarm failed",
			"package", pkg,
			"error", err,
			"duration_ms", time.Since(start).Milliseconds(),
			"output", tailOutput(out, 1024))
		if p.OnStatus != nil {
			p.OnStatus(pkg, failed)
		}
	}

	tree, err := stageTree()
	if err != nil {
		fail(err, nil)
		return
	}
	defer removeTree(tree)

	cmd := exec.CommandContext(installCtx, npmBin,
		"install", "--ignore-scripts", "--no-audit", "--no-fund", pkg)
	// The staging tree is the working directory AND carries a package.json, so npm
	// resolves nothing from above it: --prefix alone leaves npm free to find an
	// ancestor manifest and install against somebody else's tree.
	cmd.Dir = tree
	ring := &ringBuffer{Cap: tailLogBytes}
	cmd.Stdout = ring
	cmd.Stderr = ring
	if err := cmd.Run(); err != nil {
		fail(err, ring.Bytes())
		return
	}
	slog.Info("mcp: prewarm done",
		"package", pkg, "duration_ms", time.Since(start).Milliseconds())
	if p.OnStatus != nil {
		p.OnStatus(pkg, done)
	}
}

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
func ExtractNpxPackage(s ServerInfo) string {
	if !s.Prewarm || !s.Enabled {
		return ""
	}
	if !supportedPackageTransports[s.Transport] {
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
