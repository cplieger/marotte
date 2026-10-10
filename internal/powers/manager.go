package powers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cplieger/marotte/internal/procgroup"
	"github.com/cplieger/runesafe/v2"
)

const (
	// cliTimeout bounds one `kiro-cli powers` run; a measured install took 4 s.
	cliTimeout = 3 * time.Minute
	// maxCLIOutput bounds what is kept of a run's output for its error message.
	maxCLIOutput = 64 << 10
)

// ErrUnknownPower is an install of a name the catalogue does not carry.
var ErrUnknownPower = errors.New("no such power in the catalogue")

// ErrPolicyUnknown is an install refused because the administrator rules have not
// been read yet, so whether Powers are allowed is not known.
var ErrPolicyUnknown = errors.New("powers: the administrator rules are not known yet")

// ErrPowersLocked is an install refused because an administrator's powers lock
// stands when the install takes the Manager's write lock.
var ErrPowersLocked = errors.New("powers: an administrator lock forbids installing powers")

// CLIError is a `kiro-cli powers` run that exited non-zero. Message is the
// run's last output line, sanitized for display.
type CLIError struct {
	Message string
}

func (e *CLIError) Error() string { return "kiro-cli powers: " + e.Message }

// RenderError is an install or removal kiro-cli completed whose legacy block
// could not be rewritten: the change on disk stands, its MCP servers do not.
type RenderError struct {
	Err error
}

func (e *RenderError) Error() string { return e.Err.Error() }

func (e *RenderError) Unwrap() error { return e.Err }

// scanError is one installed Power whose mcp.json could not be read; its servers
// are left out of the legacy block.
type scanError struct {
	Err   error
	Power string
}

func (e *scanError) Error() string { return "power " + e.Power + ": " + e.Err.Error() }

func (e *scanError) Unwrap() error { return e.Err }

// PartialError is a legacy block written with the healthy Powers' servers and
// without the servers of the Powers named in Failed.
type PartialError struct {
	Failed []string
}

func (e *PartialError) Error() string {
	return "powers: MCP servers not configured for " + strings.Join(e.Failed, ", ")
}

// ServersWriter renders the legacy block into KAS's config file; *mcp.Store
// implements it.
type ServersWriter interface {
	WritePowersServers(ctx context.Context, servers map[string]json.RawMessage) (bool, error)
}

// ServersMode says what the legacy block lists.
type ServersMode int

// ModeFunc resolves the ServersMode. The Manager calls it inside the lock that
// serializes its writes, so no render writes a mode sampled before it waited.
type ModeFunc func() ServersMode

const (
	// ServersActive lists every installed legacy Power's MCP servers.
	ServersActive ServersMode = iota
	// ServersSuppressed lists none: an administrator's powers lock stands, so no
	// installed Power's servers may start.
	ServersSuppressed
	// ServersUnresolved lists none and refuses an install: the administrator rules
	// have not been read yet.
	ServersUnresolved
)

// Manager installs and removes Powers and keeps the legacy block in step with
// what is installed.
type Manager struct {
	catalog      *Catalog
	writer       ServersWriter
	cliPath      func() string
	env          func() []string
	installedDir string
	// mu serializes installs, removals and syncs: each rewrites shared files.
	mu sync.Mutex
}

// NewManager wires the catalogue, the legacy-block writer and the kiro-cli
// resolvers (read per run, so a version switch reaches the next one).
// installedDir is `<KiroHome>/powers/installed`.
func NewManager(catalog *Catalog, writer ServersWriter, cliPath func() string, env func() []string, installedDir string) *Manager {
	return &Manager{catalog: catalog, writer: writer, cliPath: cliPath, env: env, installedDir: installedDir}
}

// Catalog returns the official catalogue.
func (m *Manager) Catalog(ctx context.Context) ([]Entry, error) { return m.catalog.catalogEntries(ctx) }

// Servers names the MCP servers a catalogue Power declares; see Catalog.catalogServerNames.
func (m *Manager) Servers(ctx context.Context, name string) (names []string, known bool, err error) {
	e, ok, err := m.catalog.revalidate(ctx, name)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, ErrUnknownPower
	}
	names, known = m.catalog.catalogServerNames(ctx, &e)
	return names, known, nil
}

// Install runs `kiro-cli powers install <name>` for a catalogue Power, then
// renders the legacy block, which the install itself never writes.
func (m *Manager) Install(ctx context.Context, name string, mode ModeFunc) error {
	ok, err := m.catalog.admit(ctx, name)
	if err != nil {
		return err
	}
	if !ok {
		return ErrUnknownPower
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch mode() {
	case ServersUnresolved:
		return ErrPolicyUnknown
	case ServersSuppressed:
		return ErrPowersLocked
	}
	if err := m.run(ctx, "install", name); err != nil {
		return err
	}
	return verbResult(name, m.syncLocked(ctx, mode))
}

// Another Power's skipped servers are the inventory's to report, not this verb's.
func verbResult(name string, err error) error {
	if err == nil {
		return nil
	}
	if p, ok := errors.AsType[*PartialError](err); ok && !slices.Contains(p.Failed, name) {
		return nil
	}
	return &RenderError{Err: err}
}

// Uninstall runs `kiro-cli powers uninstall <name>`, then drops the Power's
// servers from the legacy block. It takes any valid name, because a Power
// installed from a local path is not in the catalogue.
func (m *Manager) Uninstall(ctx context.Context, name string, mode ModeFunc) error {
	if !ValidName(name) {
		return ErrUnknownPower
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.run(ctx, "uninstall", name); err != nil {
		return err
	}
	return verbResult(name, m.syncLocked(ctx, mode))
}

// Sync renders the legacy block from what is installed, so a Power installed
// from a shell gets its servers too. An unchanged block writes nothing. A block
// written without some Power's servers is a *PartialError.
func (m *Manager) Sync(ctx context.Context, mode ModeFunc) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.syncLocked(ctx, mode)
}

func (m *Manager) syncLocked(ctx context.Context, mode ModeFunc) error {
	servers := map[string]json.RawMessage{}
	var failed []string
	var dirErr error
	if mode() == ServersActive {
		var errs []error
		servers, errs = legacyServers(m.installedDir)
		for _, err := range errs {
			slog.Warn("powers: an installed power's servers were not rendered", "error", err)
			if se, ok := errors.AsType[*scanError](err); ok {
				if !slices.Contains(failed, se.Power) {
					failed = append(failed, se.Power)
				}
			} else {
				dirErr = err
			}
		}
	}
	changed, err := m.writer.WritePowersServers(ctx, servers)
	if err != nil {
		return fmt.Errorf("powers: render legacy servers: %w", err)
	}
	if changed {
		slog.Info("powers: legacy mcp servers rendered", "servers", len(servers))
	}
	if dirErr != nil {
		return fmt.Errorf("powers: read installed powers: %w", dirErr)
	}
	if len(failed) > 0 {
		return &PartialError{Failed: failed}
	}
	return nil
}

func (m *Manager) run(ctx context.Context, verb, name string) error {
	cctx, cancel := context.WithTimeout(ctx, cliTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, m.cliPath(), "powers", verb, name) //nolint:gosec // G204: binary path from the install manager; name passed ValidName
	cmd.Env = m.env()
	// kiro-cli runs a tree (tar among it), so a cancel kills the whole group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return procgroup.Kill(cmd.Process, syscall.SIGKILL) }
	// A child still holding the output pipes must not hold Wait past the kill.
	cmd.WaitDelay = 5 * time.Second
	var out cappedBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		if cctx.Err() != nil {
			return fmt.Errorf("kiro-cli powers %s: %w", verb, cctx.Err())
		}
		if _, ok := errors.AsType[*exec.ExitError](err); ok {
			return &CLIError{Message: lastLine(out.String())}
		}
		return fmt.Errorf("kiro-cli powers %s: %w", verb, err)
	}
	slog.Info("powers: kiro-cli run finished", "verb", verb, "power", name)
	return nil
}

// cappedBuffer keeps the LAST maxCLIOutput bytes, where a failing run's reason is.
type cappedBuffer struct{ b bytes.Buffer }

func (c *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	c.b.Write(p)
	if over := c.b.Len() - maxCLIOutput; over > 0 {
		c.b.Next(over)
	}
	return n, nil
}

func (c *cappedBuffer) String() string { return c.b.String() }

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	line := strings.TrimSpace(lines[len(lines)-1])
	if line == "" {
		return "the command failed with no output"
	}
	return runesafe.SanitizeSingleLineBounded(line, maxFieldBytes)
}
