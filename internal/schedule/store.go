package schedule

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
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
	// StatusCompleted means the run finished on its own terms.
	StatusCompleted status = "completed"
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
	Source     string    `json:"source"`
	Name       string    `json:"name,omitempty"`
	LastStatus status    `json:"last_status,omitempty"`
	LastReason string    `json:"last_reason,omitempty"`
	// LastRunID is the workflow the last fire launched: the only run whose outcome may land on the row.
	LastRunID string `json:"last_run_id,omitempty"`
	// Model and Effort are sent at each fire as the run's marotte.RunDefaults; empty sends none.
	Model   string `json:"model,omitempty"`
	Effort  string `json:"effort,omitempty"`
	Spec    Spec   `json:"spec"`
	Enabled bool   `json:"enabled"`
}

// ID addresses the schedule: a digest of Source, so a recipe owns at most one row and the id
// is one canonical URL path segment whatever the source spells (a path's "/" and "..", "://").
func (e *Entry) ID() string {
	sum := sha256.Sum256([]byte(e.Source))
	return base64.RawURLEncoding.EncodeToString(sum[:16])
}

// Run names one launch of a schedule: the row and the workflow run its fire started.
type Run struct {
	ScheduleID string
	WorkflowID string
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
		s.entries[e.ID()] = e
	}
	return s, nil
}

// List returns every schedule, ordered by source so the UI and tests see a stable
// sequence out of the map.
func (s *Store) List() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sortedLocked()
}

func (s *Store) sortedLocked() []Entry {
	return slices.SortedFunc(maps.Values(s.entries), func(a, b Entry) int {
		return strings.Compare(a.Source, b.Source)
	})
}

// Put inserts or replaces a schedule, validating the spec first so the runner
// never reads one it cannot compute.
func (s *Store) Put(ctx context.Context, e *Entry) error {
	if e.Source == "" {
		return errors.New("schedule source is required")
	}
	if err := e.Spec.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.entries[e.ID()]; ok {
		// Preserve run history across an edit; the client does not send it.
		e.LastRunAt, e.LastStatus, e.LastReason = prev.LastRunAt, prev.LastStatus, prev.LastReason
		e.LastRunID = prev.LastRunID
		if e.Anchor.IsZero() {
			e.Anchor = prev.Anchor
		}
	}
	if e.Anchor.IsZero() {
		e.Anchor = time.Now()
	}
	s.entries[e.ID()] = *e
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
// than now, so the schedule cannot drift by the tick's latency. runID is the run the fire
// launched, "" for none; a row already tracking it keeps its outcome, since a fast run can end
// (RecordEnding) before its launch returns.
func (s *Store) recordFire(ctx context.Context, id string, due time.Time, runID string, o Outcome) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[id]
	if !ok {
		return errNotFound
	}
	e.Anchor = due
	e.LastRunAt = due
	if runID == "" || e.LastRunID != runID {
		e.LastStatus, e.LastReason, e.LastRunID = o.Status, o.Reason, runID
	}
	s.entries[id] = e
	return s.persistLocked(ctx)
}

// skipTo advances the anchor WITHOUT recording a run, for a slot missed while
// the container was down or refused by a run whose bound ends it at that slot (BusyError.boundBy).
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

// RecordStart marks run as the row's live run, before the run can execute, so its ending has a
// row to land on whenever it comes.
func (s *Store) RecordStart(ctx context.Context, run Run) error {
	return s.recordRun(ctx, run, func(e *Entry) bool {
		e.LastRunID, e.LastStatus, e.LastReason = run.WorkflowID, statusStarted, ""
		return true
	})
}

// RecordOutcome overwrites the outcome of run, the row's live run, after it started (a late
// failure such as an unattended denial, or a stop marotte made). An outcome of any other run is
// dropped: a later fire owns the row, with its own run or a refusal. It leaves the anchor: moving it would shift the next run
// by however long the failure took.
func (s *Store) RecordOutcome(ctx context.Context, run Run, o Outcome) error {
	return s.recordRun(ctx, run, func(e *Entry) bool {
		if e.LastRunID != run.WorkflowID {
			return false
		}
		e.LastStatus, e.LastReason = o.Status, o.Reason
		return true
	})
}

// RecordEnding records how run ended on its own, only while the row still reads it started: an
// outcome RecordOutcome wrote first (a denial, a bound stop) names the cause, and an ending says
// less.
func (s *Store) RecordEnding(ctx context.Context, run Run, o Outcome) error {
	return s.recordRun(ctx, run, func(e *Entry) bool {
		if e.LastRunID != run.WorkflowID || e.LastStatus != statusStarted {
			return false
		}
		e.LastStatus, e.LastReason = o.Status, o.Reason
		return true
	})
}

// recordRun applies set to run's row and persists when set reports a change.
func (s *Store) recordRun(ctx context.Context, run Run, set func(e *Entry) bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[run.ScheduleID]
	if !ok {
		return errNotFound
	}
	if !set(&e) {
		return nil
	}
	s.entries[run.ScheduleID] = e
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
