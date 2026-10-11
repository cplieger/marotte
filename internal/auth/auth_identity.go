package auth

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/procout"
)

// identityTTL is how long a cached identity is served before a read refreshes it behind the answer. Sign-in and
// sign-out publish directly, so this catches outside changes: expiring credentials, `kiro-cli logout` in a terminal.
const identityTTL = time.Minute

// Reasons for the unavailable arm: server-authored constants, since the client renders them in a banner.
const (
	reasonNotRead    = "identity not read yet"
	reasonTimedOut   = "kiro-cli timed out"
	reasonCLIMissing = "kiro-cli is not installed"
	reasonCLIFailed  = "kiro-cli failed"
	reasonUnreadable = "kiro-cli output was not recognisable"
)

// signedOutIdentity is the answer for a working kiro-cli reporting nobody signed in.
func signedOutIdentity() WhoamiResponse {
	return WhoamiResponse{State: WhoamiSignedOut}
}

// unavailableIdentity is the answer when kiro-cli could not be asked or read. The reason goes through identityText
// here so no caller can skip it.
func unavailableIdentity(reason string) WhoamiResponse {
	return WhoamiResponse{State: WhoamiUnavailable, Reason: identityText(reason)}
}

// identityCache is the identity /api/whoami answers from: stale-while-revalidate, never blocking a reader on the
// multi-second kiro-cli fork. The held value survives invalidation, so a revalidating poll still gets the last
// known identity.
type identityCache struct {
	// read performs one identity read; a field so tests can drive staleness and coalescing.
	read func(context.Context) WhoamiResponse
	at   time.Time
	resp WhoamiResponse
	// budget is the wall clock one read gets. After the strings, for fieldalignment.
	budget time.Duration
	// gen counts published identities; rebuild drops its answer if gen moved, so a read begun before a logout cannot
	// republish the old identity.
	gen  uint64
	mu   sync.Mutex
	busy bool
}

// newIdentityCache returns a cache seeded with the unavailable arm (unknown is not signed out). budget is captured
// here because a reader may be a timer with no request.
func newIdentityCache(read func(context.Context) WhoamiResponse, budget time.Duration) *identityCache {
	return &identityCache{read: read, budget: budget, resp: unavailableIdentity(reasonNotRead)}
}

// snapshot returns the current identity immediately, refreshing in the background when stale or never read.
func (c *identityCache) snapshot() WhoamiResponse {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.busy && (c.at.IsZero() || time.Since(c.at) >= identityTTL) {
		c.busy = true
		go c.rebuild()
	}
	return c.resp
}

// The generation bump beats an in-flight read, or a fork started before a logout republishes
// signed_in.
func (c *identityCache) publish(resp *WhoamiResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resp = *resp
	c.at = time.Now()
	c.gen++
}

// invalidate marks the entry stale but keeps it, so the next read revalidates while answering the last identity.
// No generation bump: an in-flight read should land. This lets the login window converge within one poll.
func (c *identityCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = time.Time{}
}

// refresh reads and publishes the identity in the calling goroutine, joining a refresh already in flight.
func (c *identityCache) refresh() {
	c.mu.Lock()
	if c.busy {
		c.mu.Unlock()
		return
	}
	c.busy = true
	c.mu.Unlock()
	c.rebuild()
}

// Entered with busy claimed. Its context is detached so a page-load refresh outlives the request;
// the budget and boundChild's group kill bound it. A read predating a publish is discarded, and
// busy is cleared either way.
func (c *identityCache) rebuild() {
	c.mu.Lock()
	gen := c.gen
	budget := c.budget
	read := c.read
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	resp := read(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.busy = false
	if c.gen != gen {
		return
	}
	c.resp = resp
	c.at = time.Now()
}

// Run keeps the cached identity warm until ctx is done: one read now so the first load after a restart sees a real
// identity, then one per identityTTL. Synchronous; the caller owns the goroutine.
func (h *Handler) Run(ctx context.Context) {
	h.identity.refresh()
	t := time.NewTicker(identityTTL)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.identity.refresh()
		}
	}
}

// readIdentity runs `kiro-cli whoami --format json` and maps the outcome onto one of the three arms; every failure
// is an arm. A parsed payload decides: kiro-cli exits 1 with `{"account":null}` when signed out (2.21.2, 2.21.4).
// The error classifies only reads with nothing readable.
func (h *Handler) readIdentity(ctx context.Context) WhoamiResponse {
	// h.cliPath is the install manager's active version, never user input.
	cmd := exec.CommandContext(ctx, h.cliPath(), "whoami", "--format", "json") //nolint:gosec // G204: binary path from config
	boundChild(cmd)
	stderr := procout.NewBuffer(stderrCap)
	stdout := procout.NewBuffer(whoamiMaxOutput)
	cmd.Stderr = stderr
	cmd.Stdout = stdout
	runErr := cmd.Run()
	out := stdout.Bytes()
	info, err := whoamiInfo(out)
	if err != nil {
		if runErr != nil {
			return h.identityReadFailure(ctx, runErr, stderr, len(out))
		}
		slog.Warn("whoami: cli output not parseable as json",
			"error", err, "stdout_bytes", len(out))
		return unavailableIdentity(reasonUnreadable)
	}
	// Unread identities are withheld from the registrar, or a whoami timeout would retire every live bridge. Signed out
	// is an answer, so it is observed.
	h.registrar.observe(identityFingerprint(&info))
	return info
}

// cliMissing reports whether err is an ENOENT naming the CLI itself. os/exec opens os.DevNull for a nil Stdin, so a
// missing /dev/null fails Start with fs.ErrNotExist while the binary is present.
func cliMissing(err error, cliPath string) bool {
	var pe *fs.PathError
	if !errors.As(err, &pe) {
		return false
	}
	return errors.Is(pe, fs.ErrNotExist) && pe.Path == cliPath
}

func (h *Handler) identityReadFailure(
	ctx context.Context, err error, stderr *procout.Buffer, outBytes int,
) WhoamiResponse {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		attrs := make([]any, 0, 4)
		attrs = append(attrs, "timeout", h.cfg.WhoamiTimeout)
		attrs = append(attrs, stderrAttr(stderr)...)
		slog.Warn("whoami: kiro-cli timed out", attrs...)
		return unavailableIdentity(reasonTimedOut)
	case errors.Is(err, exec.ErrNotFound), cliMissing(err, h.cliPath()):
		// Warn: a fresh volume has no kiro-cli until the install manager finishes, and the timer asks every minute.
		slog.Warn("whoami: kiro-cli binary not found",
			"cli_path", h.cliPath(), "error", err)
		return unavailableIdentity(reasonCLIMissing)
	default:
		// Details stay server-side; the client gets the arm, never raw CLI output.
		attrs := make([]any, 0, 6)
		attrs = append(attrs, "error", err, "stdout_bytes", outBytes)
		attrs = append(attrs, stderrAttr(stderr)...)
		slog.Warn("whoami: kiro-cli invocation failed", attrs...)
		return unavailableIdentity(reasonCLIFailed)
	}
}
