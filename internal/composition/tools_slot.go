package composition

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"sync"

	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/toolbelt/v3"
)

// Reasons a down engine gives the reader; the server log carries the detail behind each.
var (
	errToolsRootUnfit = errors.New("a managed tools directory failed the integrity check; " +
		"the server log names each path and how to fix it")
	errToolsStartFailed = errors.New("the tools engine could not start; the server log has the cause")
	errToolsClosed      = errors.New("the tools engine is shutting down")
	errToolsNotStarted  = errors.New("the tools engine has not started")
)

// toolsSlot owns the tools engine for the app's lifetime. A refusal marotte survives (an unfit
// managed root, a tools.json toolbelt rejects) leaves it down with a reason, and the next valid
// tools.json write starts it. Safe for concurrent use.
type toolsSlot struct {
	build    func() (*toolbelt.Engine, error)
	engine   *toolbelt.Engine
	down     error
	manifest string
	mu       sync.Mutex
	closed   bool
}

func newToolsSlot(manifest string, build func() (*toolbelt.Engine, error)) *toolsSlot {
	return &toolsSlot{build: build, manifest: manifest}
}

// start brings the engine up. It returns an error only for a failure the boot must stop on; a
// survivable one is logged and leaves the engine down with its reason.
func (s *toolsSlot) start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startLocked()
}

func (s *toolsSlot) startLocked() error {
	if s.closed || s.engine != nil {
		return nil
	}
	engine, err := s.build()
	if err != nil && !errors.Is(err, toolbelt.ErrRootIntegrity) && manifestProblem(s.manifest) == nil {
		// survivableToolsFailure attributes a failure by reading the file again, so a repair landing
		// between the two reads would turn a manifest refusal fatal: the now-valid file gets one more try.
		engine, err = s.build()
	}
	if err == nil {
		s.engine, s.down = engine, nil
		warnIfNoLSPEnabled(engine)
		return nil
	}
	if reason := survivableToolsFailure(err, s.manifest); reason != nil {
		s.down = reason
		return nil
	}
	s.down = errToolsStartFailed
	return fmt.Errorf("tools engine: %w", err)
}

// Engine returns the live engine, or nil and why it is down, worded for the person reading the
// Tools tab. Exactly one of the two is non-nil.
func (s *toolsSlot) Engine() (*toolbelt.Engine, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.engine != nil:
		return s.engine, nil
	case s.closed:
		return nil, errToolsClosed
	case s.down != nil:
		return nil, s.down
	default:
		return nil, errToolsNotStarted
	}
}

// manifestChanged converges on the tools.json now on disk: a file toolbelt would refuse takes the
// engine down with that reason; a valid one gets an install pass while up, another start while down.
func (s *toolsSlot) manifestChanged() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if problem := manifestProblem(s.manifest); problem != nil {
		if s.engine != nil {
			s.engine.Close()
			s.engine = nil
		}
		s.down = refuseManifest(s.manifest, problem)
		return
	}
	if s.engine == nil {
		if err := s.startLocked(); err != nil {
			slog.Error("tools: engine still down after a tools.json write", "error", logsafe.Field(err.Error()))
		} else if s.engine != nil {
			slog.Info("tools: engine started after a tools.json write")
		}
		return
	}
	// Missing, not Full: a hand edit asks for what it changed, never an update pass.
	// Reconcile coalesces past a full queue, so this fails only during shutdown, when
	// the boot reconcile of the next start converges instead.
	if _, _, err := s.engine.Reconcile(toolbelt.ReconcileMissing); err != nil {
		slog.Warn("tools: reconcile after a tools.json write not enqueued",
			"error", logsafe.Field(err.Error()))
	}
}

// Close stops the engine and keeps it from starting again.
func (s *toolsSlot) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.engine != nil {
		s.engine.Close()
		s.engine = nil
	}
}

// survivableToolsFailure returns the reader-facing reason for a toolbelt.New failure marotte
// runs on without tools, having logged it, or nil when the boot must stop. An unfit root and an
// unusable tools.json are both volume state the operator repairs from inside the container.
func survivableToolsFailure(err error, manifest string) error {
	if errors.Is(err, toolbelt.ErrRootIntegrity) {
		logRootIntegrityRefusal(err)
		return errToolsRootUnfit
	}
	// toolbelt.New's manifest errors carry no sentinel, so the file is re-read to attribute one.
	problem := manifestProblem(manifest)
	if problem == nil {
		return nil
	}
	return refuseManifest(manifest, problem)
}

func refuseManifest(path string, problem error) error {
	slog.Error("tools engine disabled: tools.json is unusable; marotte is running without the tools subsystem",
		"path", path, "error", logsafe.Field(problem.Error()),
		"hint", "fix it in Settings -> Tools -> Advanced configuration -> tools.json; a valid save there starts the engine")
	return fmt.Errorf("%w. Saving a valid tools.json starts the tools engine", problem)
}

// manifestProblem reports why the tools.json at path is one toolbelt.New would refuse, or nil
// when it is absent (New seeds it) or valid.
func manifestProblem(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("tools.json cannot be read: %w", err)
	}
	if _, err := toolbelt.ParseManifest(data); err != nil {
		return fmt.Errorf("tools.json is invalid: %w", err)
	}
	return nil
}
