// Package auth serves /api/whoami, /api/login and /api/logout by running the bundled kiro-cli, and reports every
// identity it reads to the registrar that owns live agent sessions. It persists no state.
package auth

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/cplieger/marotte/internal/buffer"
	"github.com/cplieger/marotte/internal/procout"
	"github.com/cplieger/marotte/internal/sanitize"
)

// Config holds per-instance timeouts, supplied through WithConfig.
type Config struct {
	LoginURLTimeout time.Duration
	LoginTimeout    time.Duration
	LogoutTimeout   time.Duration
	WhoamiTimeout   time.Duration
}

// DefaultConfig is the production configuration.
var DefaultConfig = Config{
	LoginURLTimeout: 10 * time.Second,
	LoginTimeout:    16 * time.Minute,
	LogoutTimeout:   10 * time.Second,
	WhoamiTimeout:   5 * time.Second,
}

// Scanner caps for scanLoginOutput: the per-line limit absorbs debug dumps, maxLoginLines bounds total memory.
const (
	maxScanLineBytes = 256 * 1024
	maxLoginLines    = 200
)

// Subprocess stdout caps: whoami emits about 150 bytes, so these stop a hostile kiro-cli replacement OOMing the
// container.
const (
	whoamiMaxOutput = buffer.DefaultOutputCap
	logoutMaxOutput = 1 << 20 // 1 MiB

	// stderrCap bounds stderr capture on every endpoint, for the same reason.
	stderrCap = 32 * 1024
)

// Maximum byte lengths for login request fields; 2048 matches browser URL limits.
const (
	maxProviderLen = 2048
	maxRegionLen   = 32
)

// childWaitDelay bounds Wait past the context deadline. A backstop: boundChild kills the process group, so only an
// escaped or uninterruptible descendant reaches it.
const childWaitDelay = time.Second

// awsRegionRe matches AWS region ids in every partition (us-east-1, cn-north-1, us-gov-west-1, us-isob-east-1).
// Non-empty segments also reject flags, shell metacharacters, whitespace, uppercase and `us--east-1`.
var awsRegionRe = regexp.MustCompile(`^[a-z]{2}(?:-[a-z]+)+-\d+$`)

// Handler is the /api/whoami, /api/login and /api/logout bundle. loginSem serialises logins for the whole
// device-flow lifetime and is released when cmd.Wait returns, so a second POST after the first URL still gets 409.
type Handler struct {
	loginSem chan struct{}
	// identity is what /api/whoami answers from, so page loads and SSE reconnects never trigger a read.
	identity *identityCache
	// registrar is told every identity this package reads, so an account change reaches live sessions. nil observes
	// nothing.
	registrar *Identity
	// cliPath resolves the binary per call: the install manager picks and may switch the active version after the
	// listener binds, and a first boot has nothing installed.
	cliPath func() string
	// managedSettingsPath is the administrator's file the sign-in controls come from.
	managedSettingsPath string
	// trusted is the reverse-proxy set for webhttp.ClientIP in the login/logout audit logs. Nil logs the socket peer.
	trusted []*net.IPNet
	cfg     Config
}

// Option configures a Handler at construction time.
type Option func(*Handler)

// WithConfig overrides the default timeout configuration.
func WithConfig(cfg Config) Option {
	return func(h *Handler) { h.cfg = cfg }
}

// WithTrustedProxies sets the reverse-proxy networks trusted when resolving the audit-log client IP. Empty trusts
// nothing, so the socket peer is logged.
func WithTrustedProxies(trusted []*net.IPNet) Option {
	return func(h *Handler) { h.trusted = trusted }
}

// WithIdentity feeds every identity this package reads into registrar. A nil registrar is refused.
func WithIdentity(registrar *Identity) Option {
	if registrar == nil {
		panic("auth: identity registrar is nil")
	}
	return func(h *Handler) { h.registrar = registrar }
}

// NewHandler returns an auth handler that shells out to whatever binary cliPath
// resolves to. The resolver is consulted per call, never cached.
func NewHandler(cliPath func() string, opts ...Option) *Handler {
	h := &Handler{cliPath: cliPath, loginSem: make(chan struct{}, 1), cfg: DefaultConfig, managedSettingsPath: managedSettingsPath}
	for _, o := range opts {
		o(h)
	}
	// After the options: the cache captures WithConfig's read budget.
	h.identity = newIdentityCache(h.readIdentity, h.cfg.WhoamiTimeout)
	return h
}

// RegisterRoutes wires GET /api/whoami, POST /api/login and POST /api/logout
// onto mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/whoami", h.handleWhoami)
	mux.HandleFunc("/api/login", h.handleLogin)
	mux.HandleFunc("/api/login/options", h.handleLoginOptions)
	mux.HandleFunc("/api/logout", h.handleLogout)
}

// stderrAttr returns slog attributes for captured stderr, omitting the key when empty so it does not read as
// captured-and-empty.
func stderrAttr(stderr *procout.Buffer) []any {
	s := sanitize.Output(strings.TrimSpace(stderr.String()))
	if s == "" {
		return nil
	}
	return []any{"stderr", s}
}

// boundChild makes a subprocess honour its context's deadline. CommandContext SIGKILLs only the parent, and kiro-cli
// forks helpers holding the pipes, so Wait blocks on the last descendant. Group kill and WaitDelay are both needed:
// against `sleep 10` under a 50ms context, either alone took 10.0s or 1.05s, the pair 50ms.
func boundChild(cmd *exec.Cmd) {
	setProcGroup(cmd)
	cmd.Cancel = func() error {
		err := killGroup(cmd)
		// exec ignores os.ErrProcessDone from Cancel, so mapping ESRCH like os.Process.Kill leaves Wait's error untouched.
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = childWaitDelay
}

// loginReap is what handleLogin hands the reap goroutine, which then owns ctx cancellation, cmd.Wait, stderrBuf
// and the waitDone close. It waits for stdoutDone first: exec.Cmd forbids Wait before pipe reads complete.
type loginReap struct {
	ctx        context.Context
	cancel     context.CancelFunc
	cmd        *exec.Cmd
	stderrBuf  *procout.Buffer
	stdoutDone <-chan struct{}
	waitDone   chan struct{}
}

// lineRing holds the first N and last N lines pushed into it, each capped at
// perLineCap bytes.
type lineRing struct {
	first      []string
	last       []string
	halfCap    int
	perLineCap int
}

func newLineRing(halfCap, perLineCap int) *lineRing {
	return &lineRing{
		first:      make([]string, 0, halfCap),
		last:       make([]string, 0, halfCap),
		halfCap:    halfCap,
		perLineCap: perLineCap,
	}
}

// Push appends line, truncated at perLineCap so a hostile CLI cannot blow up one log attribute.
func (r *lineRing) Push(line string) {
	if len(line) > r.perLineCap {
		line = line[:r.perLineCap]
	}
	switch {
	case len(r.first) < r.halfCap:
		r.first = append(r.first, line)
	case len(r.last) == r.halfCap:
		r.last = append(r.last[1:], line)
	default:
		r.last = append(r.last, line)
	}
}

// Sample returns the first-N and last-N lines joined, for one slog attribute.
func (r *lineRing) Sample() []string {
	return slices.Concat(r.first, r.last)
}
