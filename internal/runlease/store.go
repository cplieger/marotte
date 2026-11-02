package runlease

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/cplieger/atomicfile/v4"
)

// FileName is the store's file, beside schedules.json: a sibling so a malformed file
// cannot disable both subsystems.
const FileName = "runs.json"

// version is the on-disk format version, and why the file's top-level value is an
// OBJECT rather than the array schedules.json uses.
const version = 1

// ErrNotFound means no lease owns the given workflow id.
var ErrNotFound = errors.New("run lease not found")

type file struct {
	Leases  []Lease `json:"leases"`
	Version int     `json:"version"`
}

// Store persists run leases in one 0600 JSON file, rewritten atomically per mutation.
// The zero value is usable and persists nothing. atomicfile verifies the 0600 on the
// written descriptor, so no EnforceFile pass by NAME is needed.
type Store struct {
	leases map[string]Lease
	path   string
	// version is the collection's version, bumped under mu by every mutation that
	// changed the set; ListStamped pairs it with the set it describes.
	version uint64
	mu      sync.Mutex
}

// NewMemory returns an in-memory store that persists nothing.
func NewMemory() *Store { return &Store{leases: map[string]Lease{}} }

// NewStore opens (or starts) the store at <dir>/runs.json. It ALWAYS returns a usable
// store; the error is diagnostic. A lease is derived state, so refusing to open would leave
// no run bounded; a malformed or unknown-version file is discarded and rewritten.
func NewStore(dir string) (*Store, error) {
	s := &Store{path: filepath.Join(dir, FileName), leases: map[string]Lease{}}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("read %s: %w", s.path, err)
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return s, fmt.Errorf("parse %s: %w", s.path, err)
	}
	if f.Version != version {
		return s, fmt.Errorf("%s: unsupported version %d (this build writes %d)", s.path, f.Version, version)
	}
	for i := range f.Leases {
		l := f.Leases[i]
		if l.WorkflowID == "" || !l.Origin.valid() {
			continue
		}
		// A deadline read from disk describes a dead process: park it; the next start re-arms.
		l.Deadline = time.Time{}
		l.KeepChats = validKeepChats(l.WorkflowID, l.KeepChats)
		s.leases[l.WorkflowID] = l
	}
	return s, nil
}

// List returns every lease, ordered by workflow id so the file is stable.
func (s *Store) List() []Lease {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sortedLocked()
}

// ListStamped is List plus the collection version the set is at, read in the same
// critical section so the two cannot disagree. The version is decimal, starting at
// "0" for a store nothing has mutated this process.
func (s *Store) ListStamped() (leases []Lease, version string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sortedLocked(), strconv.FormatUint(s.version, 10)
}

func (s *Store) sortedLocked() []Lease {
	out := make([]Lease, 0, len(s.leases))
	for _, id := range slices.Sorted(maps.Keys(s.leases)) {
		l := s.leases[id]
		out = append(out, l.clone())
	}
	return out
}

// Get returns one lease.
func (s *Store) Get(workflowID string) (Lease, bool) {
	if workflowID == "" {
		return Lease{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.leases[workflowID]
	return l.clone(), ok
}

// Put grants a lease, replacing any held for the run. On a persist failure the lease is
// KEPT in memory: the run is on the wire either way.
func (s *Store) Put(ctx context.Context, l *Lease) error {
	if l.WorkflowID == "" {
		return errors.New("lease workflow id is required")
	}
	if !l.Origin.valid() {
		return fmt.Errorf("lease origin %q is not one of scheduled/manual/agent", l.Origin)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.leases[l.WorkflowID] = l.clone()
	s.version++
	return s.persistLocked(ctx)
}

// ReleaseOutcome is what Release did.
type ReleaseOutcome int

const (
	// ReleaseAbsent means no lease held the run.
	ReleaseAbsent ReleaseOutcome = iota
	// Released means the lease was the life named and is gone.
	Released
	// ReleaseSuperseded means a later grant holds the run, so its lease stays.
	ReleaseSuperseded
)

// Release forgets a run's lease when it is still the life named, in one compare-and-delete: a release reads the
// life it ends, and a grant that replaced it since is another life's. Releasing a gone lease is not an error.
func (s *Store) Release(ctx context.Context, workflowID, life string) (ReleaseOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.leases[workflowID]
	switch {
	case !ok:
		return ReleaseAbsent, nil
	case l.Life != life:
		return ReleaseSuperseded, nil
	}
	delete(s.leases, workflowID)
	s.version++
	return Released, s.persistLocked(ctx)
}

// SetKeepChats adds chats to a lease's KeepChats, sorted; it never removes one. ErrNotFound when no lease holds the
// run.
func (s *Store) SetKeepChats(ctx context.Context, workflowID string, add []string) error {
	return s.update(ctx, workflowID, func(l *Lease) bool { return l.addKeepChats(add) })
}

// Renew starts a new life on a held lease, adding chats to its KeepChats; ErrNotFound when no lease holds the run.
func (s *Store) Renew(ctx context.Context, workflowID, life string, add []string) error {
	return s.update(ctx, workflowID, func(l *Lease) bool {
		grew := l.addKeepChats(add)
		if l.Life == life {
			return grew
		}
		l.Life = life
		return true
	})
}

// SetUnattributed sets or clears a lease's Unattributed mark; ErrNotFound when no lease holds the run.
func (s *Store) SetUnattributed(ctx context.Context, workflowID string, unattributed bool) error {
	return s.update(ctx, workflowID, func(l *Lease) bool {
		if l.Unattributed == unattributed {
			return false
		}
		l.Unattributed = unattributed
		return true
	})
}

// update applies change to a held lease and persists it when change reports a difference.
func (s *Store) update(ctx context.Context, workflowID string, change func(l *Lease) bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.leases[workflowID]
	if !ok {
		return ErrNotFound
	}
	l = l.clone()
	if !change(&l) {
		return nil
	}
	s.leases[workflowID] = l
	s.version++
	return s.persistLocked(ctx)
}

// SetParentSessionID records the run's parent session; ErrNotFound when no lease holds the run.
func (s *Store) SetParentSessionID(ctx context.Context, workflowID, session string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.leases[workflowID]
	if !ok {
		return ErrNotFound
	}
	if l.ParentSessionID == session {
		return nil
	}
	l.ParentSessionID = session
	s.leases[workflowID] = l
	s.version++
	return s.persistLocked(ctx)
}

// SetDeadline re-stamps a lease's deadline, or parks it with the zero time.
// The error reports DURABILITY, not the mutation: when the lease exists the in-memory
// deadline is set regardless. ErrNotFound is the one error meaning nothing was stored.
func (s *Store) SetDeadline(ctx context.Context, workflowID string, deadline time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.leases[workflowID]
	if !ok {
		return ErrNotFound
	}
	if l.Deadline.Equal(deadline) {
		return nil
	}
	l.Deadline = deadline
	s.leases[workflowID] = l
	s.version++
	return s.persistLocked(ctx)
}

func (s *Store) persistLocked(ctx context.Context) error {
	if s.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(file{Version: version, Leases: s.sortedLocked()}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode run leases: %w", err)
	}
	if _, err := atomicfile.WriteFile(ctx, s.path, data,
		atomicfile.WithMode(0o600), atomicfile.WithMkdirMode(0o700)); err != nil {
		return fmt.Errorf("write %s: %w", s.path, err)
	}
	return nil
}
