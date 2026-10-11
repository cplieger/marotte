package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/atomicfile/v4"
)

// fileName is the store's file, beside mcp.json in the config dir.
const fileName = "schedules.json"

// errNotFound means no schedule owns the given id.
var errNotFound = errors.New("schedule not found")

// status is how a schedule's last slot ended, as far as the server knows.
type status string

const (
	// statusStarted means the launch took and nothing has reported an ending yet.
	statusStarted status = "started"
	// StatusFailed means the launch or the run failed; the reason says why.
	StatusFailed status = "failed"
	// StatusUnknown means the run went away without a terminal signal.
	StatusUnknown status = "unknown"
)

// Outcome is one slot's ending. Reason is plain text a person reads, never a
// prefix-coded value: status alone carries the classification.
type Outcome struct {
	Status status
	Reason string
}

// Entry is one scheduled workflow. Source is the recipe launch key that
// Launch takes; it is re-validated at launch time rather than trusted here,
// because it looks like a path.
type Entry struct {
	// Anchor is what nextRun measures from: the last fire (or skip), falling
	// back to creation so a new schedule does not immediately fire for every
	// slot since the epoch.
	Anchor time.Time `json:"anchor"`
	// LastRunAt, LastStatus and LastReason are for display only; the run's own
	// record is the durable history.
	LastRunAt  time.Time `json:"last_run_at,omitzero"`
	ID         string    `json:"id"`
	Source     string    `json:"source"`
	Name       string    `json:"name,omitempty"`
	LastStatus status    `json:"last_status,omitempty"`
	LastReason string    `json:"last_reason,omitempty"`
	Spec       Spec      `json:"spec"`
	Enabled    bool      `json:"enabled"`
}

// Writes never emit it.
type storedEntry struct {
	LegacyResult string `json:"last_result,omitempty"`
	Entry
}

// legacyOutcome splits an old last_result by the prefixes its writers used.
// Text with no known prefix was a failure sentence written whole.
func legacyOutcome(result string) Outcome {
	if result == string(statusStarted) {
		return Outcome{Status: statusStarted}
	}
	if reason, ok := strings.CutPrefix(result, "failed: "); ok {
		return Outcome{Status: StatusFailed, Reason: reason}
	}
	if reason, ok := strings.CutPrefix(result, "unknown: "); ok {
		return Outcome{Status: StatusUnknown, Reason: reason}
	}
	if rest, ok := strings.CutPrefix(result, "stopped: "); ok {
		return Outcome{Status: StatusFailed, Reason: "stopped because " + rest}
	}
	return Outcome{Status: StatusFailed, Reason: result}
}

// Store persists schedules in one 0600 JSON file, rewritten atomically per mutation.
type Store struct {
	entries map[string]Entry
	path    string
	mu      sync.Mutex
}

// NewStore opens (or starts) the store at <dir>/schedules.json. A malformed file is a hard
// error rather than a silent reset of the user's schedules.
func NewStore(dir string) (*Store, error) {
	s := &Store{path: filepath.Join(dir, fileName), entries: map[string]Entry{}}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.path, err)
	}
	var list []storedEntry
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.path, err)
	}
	for i := range list {
		e := list[i].Entry
		if e.LastStatus == "" && list[i].LegacyResult != "" {
			o := legacyOutcome(list[i].LegacyResult)
			e.LastStatus, e.LastReason = o.Status, o.Reason
		}
		s.entries[e.ID] = e
	}
	return s, nil
}

// List returns every schedule, ordered by id so the UI and tests see a stable
// sequence out of the map.
func (s *Store) List() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sortedLocked()
}

func (s *Store) sortedLocked() []Entry {
	return slices.SortedFunc(maps.Values(s.entries), func(a, b Entry) int {
		return strings.Compare(a.ID, b.ID)
	})
}

// Put inserts or replaces a schedule, validating the spec first so the runner
// never reads one it cannot compute.
func (s *Store) Put(ctx context.Context, e *Entry) error {
	if e.ID == "" {
		return errors.New("schedule id is required")
	}
	if e.Source == "" {
		return errors.New("schedule source is required")
	}
	if err := e.Spec.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.entries[e.ID]; ok {
		// Preserve run history across an edit; the client does not send it.
		e.LastRunAt, e.LastStatus, e.LastReason = prev.LastRunAt, prev.LastStatus, prev.LastReason
		if e.Anchor.IsZero() {
			e.Anchor = prev.Anchor
		}
	}
	if e.Anchor.IsZero() {
		e.Anchor = time.Now()
	}
	s.entries[e.ID] = *e
	return s.persistLocked(ctx)
}

// Delete removes a schedule. Deleting one that is gone is not an error.
func (s *Store) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[id]; !ok {
		return nil
	}
	delete(s.entries, id)
	return s.persistLocked(ctx)
}

// recordFire advances a schedule's anchor after a fire or a skip, to the DUE time rather
// than now, so the schedule cannot drift by the tick's latency.
func (s *Store) recordFire(ctx context.Context, id string, due time.Time, o Outcome) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[id]
	if !ok {
		return errNotFound
	}
	e.Anchor = due
	e.LastRunAt = due
	e.LastStatus, e.LastReason = o.Status, o.Reason
	s.entries[id] = e
	return s.persistLocked(ctx)
}

// skipTo advances the anchor WITHOUT recording a run, for a slot missed while
// the container was down.
func (s *Store) skipTo(ctx context.Context, id string, to time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[id]
	if !ok {
		return errNotFound
	}
	e.Anchor = to
	s.entries[id] = e
	return s.persistLocked(ctx)
}

// RecordOutcome overwrites a schedule's last outcome after its run has started (a late
// failure, such as an unattended permission denial). It leaves the anchor: moving it would
// shift the next run by however long the failure took.
func (s *Store) RecordOutcome(ctx context.Context, id string, o Outcome) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[id]
	if !ok {
		return errNotFound
	}
	e.LastStatus, e.LastReason = o.Status, o.Reason
	s.entries[id] = e
	return s.persistLocked(ctx)
}

func (s *Store) persistLocked(ctx context.Context) error {
	data, err := json.MarshalIndent(s.sortedLocked(), "", "  ")
	if err != nil {
		return fmt.Errorf("encode schedules: %w", err)
	}
	if _, err := atomicfile.WriteFile(ctx, s.path, data,
		atomicfile.WithMode(0o600), atomicfile.WithMkdirMode(0o700)); err != nil {
		return fmt.Errorf("write %s: %w", s.path, err)
	}
	return nil
}
