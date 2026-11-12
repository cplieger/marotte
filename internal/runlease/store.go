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
	"sync"
	"time"

	"github.com/cplieger/go/filesafe"
	"github.com/cplieger/marotte/internal/asidefile"
	"github.com/cplieger/marotte/internal/subject"
)

// FileName is the store's file, beside launch-rules.json: a sibling so a malformed file
// cannot disable both subsystems.
const FileName = "runs.json"

// version is the on-disk format version, and why the file's top-level value is an
// OBJECT rather than the array launch-rules.json uses.
const version = 1

// ErrNotFound means no lease owns the given workflow id.
var ErrNotFound = errors.New("run lease not found")

type file struct {
	Leases  []Lease `json:"leases"`
	Version int     `json:"version"`
}

// Store persists run leases in one 0600 JSON file, rewritten atomically per mutation, and
// owns the live-runs inventory: its `runs` version and the announcement of each row change.
// Build it with NewStore or NewMemory. filesafe verifies the 0600 on the written descriptor,
// so no EnforceFile pass by NAME is needed.
type Store struct {
	leases map[string]Lease
	// versions and announce are what BindInventory binds; announce is nil until then.
	versions *subject.Versions
	announce func(row Row, live bool, version string)
	path     string
	// unread and refuseWrites are immutable after construction (see Unread).
	unread       string
	mu           sync.Mutex
	refuseWrites bool
}

// NewMemory returns an in-memory store that persists nothing.
func NewMemory() *Store { return &Store{leases: map[string]Lease{}, versions: &subject.Versions{}} }

// BindInventory mints the store's `runs` version in v from now on and hands every row change
// to announce with the version it minted, under the store's lock, so announcements arrive in
// version order. Call it once, before the first mutation anyone reads the version of; v is non-nil.
func (s *Store) BindInventory(v *subject.Versions, announce func(row Row, live bool, version string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.versions, s.announce = v, announce
}

// NewStore opens (or starts) the store at <dir>/runs.json. It ALWAYS returns a usable store;
// the error is diagnostic. A lease is derived state, so refusing to open would leave no run
// bounded. A file it cannot fully read is moved aside and the store starts empty with Unread set.
func NewStore(dir string) (*Store, error) {
	s := &Store{path: filepath.Join(dir, FileName), leases: map[string]Lease{}, versions: &subject.Versions{}}
	if err := s.load(); err != nil {
		s.leases = map[string]Lease{}
		err = s.keepAside(err)
		return s, err
	}
	err := s.markEarlierAside(dir)
	return s, err
}

// load reads runs.json; a missing file is a first run. One lease it cannot act on fails the whole
// file: the next write would drop that lease, and its keep holders with it.
func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if asidefile.Absent(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", s.path, err)
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("parse %s: %w", s.path, err)
	}
	if f.Version != version {
		return fmt.Errorf("%s: unsupported version %d (this build writes %d)", s.path, f.Version, version)
	}
	for i := range f.Leases {
		l := f.Leases[i]
		if err := l.check(); err != nil {
			return fmt.Errorf("%s: lease %d: %w", s.path, i, err)
		}
		// A deadline read from disk describes a dead process: park it; the next start re-arms.
		l.Deadline = time.Time{}
		l.KeepChats = validKeepChats(l.WorkflowID, l.KeepChats)
		s.leases[l.WorkflowID] = l
	}
	return nil
}

// List returns every lease, ordered by workflow id so the file is stable.
func (s *Store) List() []Lease {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sortedLocked()
}

// ListStamped is List plus the `runs` version the set is at, read in the same critical
// section so the two cannot disagree. The version is decimal, subject.Unminted until the
// first row change this process.
func (s *Store) ListStamped() (leases []Lease, version string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	version, _ = s.versions.Current(subject.KindRuns, "")
	return s.sortedLocked(), version
}

// rowLocked is a run's inventory row and whether a lease holds the run.
func (s *Store) rowLocked(workflowID string) (Row, bool) {
	l, ok := s.leases[workflowID]
	if !ok {
		return Row{}, false
	}
	return l.Row(), true
}

// commitLocked mints the `runs` version and announces the run's row when the mutation just
// written moved it, so the version counts row changes and every version has its frame. It
// runs before persistLocked: a mutation keeps its in-memory change when the persist fails,
// and ListStamped already serves that row.
func (s *Store) commitLocked(workflowID string, before Row, had bool) {
	after, has := s.rowLocked(workflowID)
	if had == has && before == after {
		return
	}
	version := s.versions.BumpCounter(subject.KindRuns, "")
	if s.announce == nil {
		return
	}
	if has {
		s.announce(after, true, version)
		return
	}
	s.announce(Row{WorkflowID: before.WorkflowID, ChatID: before.ChatID}, false, version)
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
	if err := l.check(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	before, had := s.rowLocked(l.WorkflowID)
	s.leases[l.WorkflowID] = l.clone()
	s.commitLocked(l.WorkflowID, before, had)
	return s.persistLocked(ctx)
}

// Release forgets a run's lease; releasing a gone one is not an error (both the terminal
// frame and the cancel path release).
func (s *Store) Release(ctx context.Context, workflowID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	before, had := s.rowLocked(workflowID)
	if !had {
		return nil
	}
	delete(s.leases, workflowID)
	s.commitLocked(workflowID, before, had)
	return s.persistLocked(ctx)
}

// SetFirstAbsentAt starts or clears a lease's continuous-absence clock.
func (s *Store) SetFirstAbsentAt(ctx context.Context, workflowID string, at time.Time) error {
	return s.update(ctx, workflowID, func(l *Lease) bool {
		if l.FirstAbsentAt.Equal(at) {
			return false
		}
		l.FirstAbsentAt = at
		return true
	})
}

// SetAutoResumeStoppedAt records when marotte stopped resuming a run on its own, or clears it with the zero time;
// ErrNotFound when no lease holds the run.
func (s *Store) SetAutoResumeStoppedAt(ctx context.Context, workflowID string, at time.Time) error {
	return s.update(ctx, workflowID, func(l *Lease) bool {
		if l.AutoResumeStoppedAt.Equal(at) {
			return false
		}
		l.AutoResumeStoppedAt = at
		return true
	})
}

// SetKeepChats adds chats to a lease's KeepChats, sorted; it never removes one. ErrNotFound when no lease holds the
// run.
func (s *Store) SetKeepChats(ctx context.Context, workflowID string, add []string) error {
	return s.update(ctx, workflowID, func(l *Lease) bool { return l.addKeepChats(add) })
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
	before := l.Row()
	l = l.clone()
	if !change(&l) {
		return nil
	}
	s.leases[workflowID] = l
	s.commitLocked(workflowID, before, true)
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
	before := l.Row()
	l.ParentSessionID = session
	s.leases[workflowID] = l
	s.commitLocked(workflowID, before, true)
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
	before := l.Row()
	l.Deadline = deadline
	s.leases[workflowID] = l
	s.commitLocked(workflowID, before, true)
	return s.persistLocked(ctx)
}

func (s *Store) persistLocked(ctx context.Context) error {
	if s.path == "" {
		return nil
	}
	if s.refuseWrites {
		return fmt.Errorf("%w: %s", errWriteRefused, s.unread)
	}
	data, err := json.MarshalIndent(file{Version: version, Leases: s.sortedLocked()}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode run leases: %w", err)
	}
	if _, err := filesafe.WriteFile(ctx, s.path, data,
		filesafe.WithMode(0o600), filesafe.WithMkdirMode(0o700)); err != nil {
		return fmt.Errorf("write %s: %w", s.path, err)
	}
	return nil
}
