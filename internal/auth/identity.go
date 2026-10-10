package auth

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/procout"
)

const identityProbeTTL = time.Minute

// Identity tracks the account that owns live agent sessions. Safe for concurrent use; the zero value is inert.
// Every observation acts on a change, so no shared baseline can consume another path's signal.
type Identity struct {
	retire func()
	probe  func(context.Context) (string, error)
	now    func() time.Time

	lastProbe   time.Time
	fingerprint string
	mu          sync.Mutex
	// absentProbes counts consecutive empty readings after a known identity. One is transient (a credential refresh can
	// drop `email` for a reading); the second retires.
	absentProbes int
}

// NewIdentity returns an identity registrar backed by kiro-cli whoami. retire must be non-nil, or live bridges stay
// on the previous account.
func NewIdentity(cliPath func() string, env func() []string, retire func()) *Identity {
	if retire == nil {
		panic("auth: identity retire callback is nil")
	}
	return &Identity{
		retire: retire,
		probe: func(ctx context.Context) (string, error) {
			return probeIdentity(ctx, cliPath, env)
		},
		now: time.Now,
	}
}

// observe adopts fp and retires live sessions when a known identity changes. Empty never replaces the baseline and
// retires only when the next probe is empty too, so one transient reading does not stop every bridge and step.
func (id *Identity) observe(fp string) {
	if id == nil {
		return
	}
	changed := false
	id.mu.Lock()
	switch {
	case fp == "":
		if id.fingerprint != "" {
			id.absentProbes++
			changed = id.absentProbes == 2
		}
	case id.fingerprint == "":
		id.fingerprint = fp
		id.absentProbes = 0
	case id.fingerprint == fp:
		id.absentProbes = 0
	default:
		id.fingerprint = fp
		id.absentProbes = 0
		changed = true
	}
	retire := id.retire
	id.mu.Unlock()
	if changed && retire != nil {
		retire()
	}
}

// signedOut records a sign-out marotte performed: there is no reading to debounce, so live sessions retire at once.
// Absent still never becomes the baseline.
func (id *Identity) signedOut() {
	if id == nil {
		return
	}
	id.mu.Lock()
	changed := id.fingerprint != "" && id.absentProbes < 2
	if id.fingerprint != "" {
		id.absentProbes = 2
	}
	retire := id.retire
	id.mu.Unlock()
	if changed && retire != nil {
		retire()
	}
}

// EnsureCurrent probes at most once per identityProbeTTL and fails open when
// whoami cannot supply a readable identity.
func (id *Identity) EnsureCurrent(ctx context.Context) {
	if id == nil {
		return
	}
	nowFn := id.now
	if nowFn == nil {
		return
	}
	now := nowFn()
	id.mu.Lock()
	if !id.lastProbe.IsZero() && now.Sub(id.lastProbe) < identityProbeTTL {
		id.mu.Unlock()
		return
	}
	id.lastProbe = now
	probe := id.probe
	id.mu.Unlock()
	if probe == nil {
		return
	}

	fp, err := probe(ctx)
	if err != nil {
		slog.Debug("identity probe failed open", "error", err)
		return
	}
	id.observe(fp)
}

func probeIdentity(ctx context.Context, cliPath func() string, env func() []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, DefaultConfig.WhoamiTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, cliPath(), "whoami", "--format", "json") //nolint:gosec // G204: binary path from config
	boundChild(cmd)
	if env != nil {
		cmd.Env = env()
	}
	stdout := procout.NewBuffer(whoamiMaxOutput)
	cmd.Stdout = stdout
	cmd.Stderr = procout.NewBuffer(stderrCap)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("run whoami: %w", err)
	}
	info, err := whoamiInfo(stdout.Bytes())
	if err != nil {
		return "", fmt.Errorf("parse whoami: %w", err)
	}
	return identityFingerprint(&info), nil
}

// identityFingerprint hashes only the allowlisted identity fields, so an upstream addition cannot start retiring
// bridges; none of them fingerprints as absent. A pointer because the struct is 112 bytes (gocritic hugeParam).
func identityFingerprint(info *WhoamiResponse) string {
	if info.Email == "" && info.AccountType == "" && info.StartURL == "" && info.Region == "" {
		return ""
	}
	allowed := "email\x00" + info.Email + "\x00" +
		"account_type\x00" + info.AccountType + "\x00" +
		"start_url\x00" + info.StartURL + "\x00" +
		"region\x00" + info.Region + "\x00"
	sum := sha256.Sum256([]byte(allowed))
	return string(sum[:])
}
