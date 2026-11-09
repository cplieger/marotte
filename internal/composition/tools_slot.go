package composition

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"

	"github.com/cplieger/go/filesafe"
	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/ownedfile"
	"github.com/cplieger/marotte/internal/steering"
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

// toolsSlot owns the tools engine for the app's lifetime and is tools.json's one writer while the
// engine is down. A refusal marotte survives (an unfit managed root, a tools.json toolbelt
// rejects) leaves it down with a reason, and the next valid tools.json write starts it. Safe for
// concurrent use.
type toolsSlot struct {
	build    func() (*toolbelt.Engine, error)
	engine   *toolbelt.Engine
	down     error
	manifest string
	// seen is the read of tools.json the slot last acted on; markSeen is its one setter.
	seen   ownedfile.Read
	gate   ownedfile.Gate
	mu     sync.Mutex
	closed bool
}

var _ filebrowse.Owner = (*toolsSlot)(nil)

func newToolsSlot(manifest string, build func() (*toolbelt.Engine, error)) *toolsSlot {
	return &toolsSlot{build: build, manifest: manifest}
}

// start brings the engine up. It returns an error only for a failure the boot must stop on; a
// survivable one is logged and leaves the engine down with its reason.
func (s *toolsSlot) start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, _, _ := readManifest(s.manifest)
	err := s.startLocked()
	s.markSeen(r)
	return err
}

func (s *toolsSlot) startLocked() error {
	if s.closed || s.engine != nil {
		return nil
	}
	engine, err := s.build()
	if err != nil && !errors.Is(err, toolbelt.ErrRootIntegrity) && !errors.Is(err, errToolsDirUnusable) {
		if r, _, _ := readManifest(s.manifest); r.Kind != ownedfile.Unreadable {
			// A repair that landed after toolbelt.New read the file gets one more try.
			engine, err = s.build()
		}
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

// installedTools is the steering generator's tools snapshot: ok false while the engine is down or
// its inventory fails.
func (s *toolsSlot) installedTools() (steering.ToolsSnapshot, bool) {
	engine, err := s.Engine()
	if err != nil {
		return steering.ToolsSnapshot{}, false
	}
	inv, err := engine.Inventory()
	if err != nil {
		return steering.ToolsSnapshot{}, false
	}
	return toolsSnapshot(inv), true
}

// toolsSnapshot keeps the rows the live probe reports installed and the system commands present.
// A disabled row is left out: its Installed means only that the engine still owns files the next
// reconcile removes.
func toolsSnapshot(inv *toolbelt.Inventory) steering.ToolsSnapshot {
	var out steering.ToolsSnapshot
	for i := range inv.Tools {
		t := &inv.Tools[i]
		if !t.Installed || t.Disabled {
			continue
		}
		out.Tools = append(out.Tools, steering.InstalledTool{
			Version: t.InstalledVersion, Commands: t.Commands,
			Apt: strings.HasPrefix(t.Source, toolbelt.SourceApt+":"),
		})
	}
	for _, sys := range inv.System {
		if sys.Installed {
			out.System = append(out.System, sys.Name)
		}
	}
	return out
}

// Replace is the editor's save of tools.json: through the engine's store lock while it is up,
// and written by the slot, which then starts the engine, while it is down. A document toolbelt
// refuses, or one that drops an essential row while the engine is up, is a SaveRefusalError.
func (s *toolsSlot) Replace(_ context.Context, content []byte, check func([]byte) error) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.engine != nil {
		if err := s.engine.ReplaceManifest(content, check); err != nil {
			return nil, manifestSaveError(err)
		}
		s.wroteLocked(content)
		return content, nil
	}
	if check != nil {
		_, current, _ := readManifest(s.manifest)
		if err := check(current); err != nil {
			return nil, err
		}
	}
	if err := toolbelt.WriteManifest(filepath.Dir(s.manifest), content); err != nil {
		return nil, manifestSaveError(err)
	}
	s.wroteLocked(content)
	if err := s.startLocked(); err != nil {
		slog.Error("tools: engine still down after a tools.json save", "error", logsafe.Field(err.Error()))
	} else if s.engine != nil {
		slog.Info("tools: engine started after a tools.json save")
	}
	return content, nil
}

// wroteLocked records the slot's own write, so a hold from before it never counts toward a later one.
func (s *toolsSlot) wroteLocked(content []byte) {
	s.gate.Reset()
	s.markSeen(ownedfile.Written(content))
}

func manifestSaveError(err error) error {
	switch {
	case errors.Is(err, toolbelt.ErrManifestInvalid):
		return filebrowse.SaveRefusalError{Err: fmt.Errorf("tools.json was not saved: %w", err)}
	case errors.Is(err, toolbelt.ErrEssential):
		name := strings.TrimPrefix(err.Error(), toolbelt.ErrEssential.Error()+": ")
		return filebrowse.SaveRefusalError{
			Err: fmt.Errorf("tools.json was not saved: %s is essential to this application; disable it instead", name),
		}
	}
	return err
}

// tick is one pass of the tools.json watch: it acts on a read ownedfile.Gate lets through and the
// slot has not acted on yet, through the engine's store while up and by starting it while down.
func (s *toolsSlot) tick() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	r, data, problem := readManifest(s.manifest)
	if !s.gate.Observe(r) || r == s.seen {
		return
	}
	if s.engine != nil {
		s.syncLocked(r, data, problem)
		return
	}
	s.manifestChangedLocked(r, problem)
}

// syncLocked hands an acting read to the engine's store: an unreadable one takes the engine down
// with its reason; an absent one's error is a seed write that failed, and the engine stays up.
func (s *toolsSlot) syncLocked(r ownedfile.Read, data []byte, problem error) {
	defer s.markSeen(r)
	_, err := s.engine.SyncManifest(data, seenKind(r.Kind))
	switch {
	case r.Kind == ownedfile.Unreadable && err != nil:
		s.engine.Close()
		s.engine = nil
		s.down = refuseManifest(s.manifest, problem)
	case err != nil:
		slog.Warn("tools: tools.json change not applied", "path", s.manifest, "error", logsafe.Field(err.Error()))
	}
}

// manifestChangedLocked is the down engine's answer to an acting read: a valid or absent file
// starts it (toolbelt.New seeds an absent one), an unreadable one keeps it down with its reason.
func (s *toolsSlot) manifestChangedLocked(r ownedfile.Read, problem error) {
	defer s.markSeen(r)
	if r.Kind == ownedfile.Unreadable {
		s.down = refuseManifest(s.manifest, problem)
		return
	}
	if err := s.startLocked(); err != nil {
		slog.Error("tools: engine still down after a tools.json write", "error", logsafe.Field(err.Error()))
	} else if s.engine != nil {
		slog.Info("tools: engine started after a tools.json write")
	}
}

func (s *toolsSlot) markSeen(r ownedfile.Read) { s.seen = r }

func seenKind(k ownedfile.Kind) toolbelt.SeenKind {
	switch k {
	case ownedfile.Valid:
		return toolbelt.SeenValid
	case ownedfile.Absent:
		return toolbelt.SeenAbsent
	}
	return toolbelt.SeenUnreadable
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
// unusable tools.json are both volume state the operator repairs from inside the container; an
// unusable tools dir is a setting the operator changes.
func survivableToolsFailure(err error, manifest string) error {
	if errors.Is(err, errToolsDirUnusable) {
		slog.Error("tools engine disabled: marotte is running without the tools subsystem",
			"error", logsafe.Field(err.Error()))
		return err
	}
	if errors.Is(err, toolbelt.ErrRootIntegrity) {
		logRootIntegrityRefusal(err)
		return errToolsRootUnfit
	}
	// The file is read again for the fault's own words, and to name a read error, which carries no sentinel.
	if r, _, problem := readManifest(manifest); r.Kind == ownedfile.Unreadable {
		return refuseManifest(manifest, problem)
	}
	if errors.Is(err, toolbelt.ErrManifestInvalid) || errors.Is(err, filesafe.ErrNotRegular) {
		return refuseManifest(manifest, manifestProblem(err, errors.Is(err, toolbelt.ErrManifestInvalid)))
	}
	return nil
}

func refuseManifest(path string, problem error) error {
	slog.Error("tools engine disabled: tools.json is unusable; marotte is running without the tools subsystem",
		"path", path, "error", logsafe.Field(problem.Error()),
		"hint", "fix it in Settings -> Tools -> Advanced configuration -> tools.json; a valid save there starts the engine")
	return fmt.Errorf("%w. Saving a valid tools.json starts the tools engine", problem)
}

// readManifest classifies tools.json as the engine reads it (following a final symlink, never
// blocking on a FIFO). problem is nil unless the read is Unreadable, and then says why for the
// person reading the Tools tab. data holds a regular file's bytes and is nil for any other read.
func readManifest(path string) (r ownedfile.Read, data []byte, problem error) {
	r, data, _, err := ownedfile.Classify(path, ownedfile.Options{FollowFinalSymlink: true}, func(b []byte) error {
		_, err := toolbelt.ParseManifest(b)
		return err
	})
	if r.Kind == ownedfile.Unreadable {
		problem = manifestProblem(err, data != nil)
	}
	return r, data, problem
}

// manifestProblem words why tools.json is unusable: invalid when its bytes were refused.
func manifestProblem(err error, invalid bool) error {
	if invalid {
		return fmt.Errorf("tools.json is invalid: %w", err)
	}
	return fmt.Errorf("tools.json cannot be read: %w", err)
}
