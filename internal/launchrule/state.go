package launchrule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/go/filesafe"
	"github.com/cplieger/go/keyenc"
)

// stateDirName holds one state file per rule, <rule id>.json, beside the definitions file.
const stateDirName = "launch-state"

// State is what the server writes about one rule, in that rule's own file: no client write reaches it.
type State struct {
	// Population is the event rule's keyenc.Join(connection, RepoRef.Encode(), kind) the state was built for.
	Population string          `json:"population,omitempty"`
	Cursor     json.RawMessage `json:"cursor,omitempty"`
	// Queue holds the rule's held events; each entry is a JSON object with a string "key".
	Queue      []json.RawMessage `json:"queue,omitempty"`
	Launched   json.RawMessage   `json:"launched,omitempty"`
	Superseded json.RawMessage   `json:"superseded,omitempty"`
	Attempts   json.RawMessage   `json:"attempts,omitempty"`
	Status     Status            `json:"status"`
}

// Status is a rule's row state.
type Status struct {
	// Anchor is a clock rule's next-run origin: its creation, then its last fire or skip.
	Anchor time.Time `json:"anchor,omitzero"`
	// LastRunAt is a clock rule's last slot's due time, or its run's start (RecordStart) until the runner records
	// that slot.
	LastRunAt time.Time `json:"last_run_at,omitzero"`
	// WaitingFor is a clock rule's due slot whose launch is retried until the agent engine answers; zero when none.
	// It is persisted so a restart resumes the wait instead of skipping the slot as missed while offline.
	WaitingFor time.Time `json:"waiting_for,omitzero"`
	// LastMinted is an event rule's last label-gain instant minted.
	LastMinted time.Time `json:"last_minted,omitzero"`
	// FailingSince starts an event rule's 24-hour failure clock; zero when nothing is failing.
	FailingSince time.Time `json:"failing_since,omitzero"`
	// LastRunID is the only run whose outcome may land on the row.
	LastRunID  string        `json:"last_run_id,omitempty"`
	LastStatus OutcomeStatus `json:"last_status,omitempty"`
	LastReason string        `json:"last_reason,omitempty"`
	// Gaps are an event rule's stretches in which runs may have been missed, kept until the operator dismisses them.
	Gaps          []Gap `json:"gaps,omitempty"`
	BoundAbandons int   `json:"bound_abandons,omitempty"`
}

// The causes of a Gap.
const (
	GapOutage       = "outage"
	GapAgedOut      = "aged_out"
	GapResultWindow = "result_window"
)

// Gap is one stretch in which an event rule may have missed a run: no complete check for longer than the read
// window reaches (outage), a run that reached the read horizon before it was seen to end or launched (aged_out),
// or more runs created in one second than the forge lists (result_window).
type Gap struct {
	From  time.Time `json:"from"`
	To    time.Time `json:"to"`
	Cause string    `json:"cause"`
	// RunID names the aged_out run.
	RunID int64 `json:"run_id,omitempty"`
	// OmittedAtLeast is a result_window's lower bound on the runs the forge did not list.
	OmittedAtLeast int `json:"omitted_at_least,omitempty"`
	// QueueFull marks an aged_out run that matched while the queue was full, rather than one still running.
	QueueFull bool `json:"queue_full,omitempty"`
}

// Held is the number of events the rule holds.
func (st *State) Held() int { return len(st.Queue) }

// UnreadableStateError is a rule's state file that would not read or parse at load. The rule stays blocked, and
// its file untouched, rather than starting over and dropping what the file held.
type UnreadableStateError struct {
	Err    error
	RuleID string
}

func (e *UnreadableStateError) Error() string {
	return fmt.Sprintf("could not read %s/%s.json: %v", stateDirName, e.RuleID, e.Err)
}

func (e *UnreadableStateError) Unwrap() error { return e.Err }

// ruleState is one rule's state and the mutex its file's writes run under.
type ruleState struct {
	unreadable error
	// refused is the load-time refusal of the rule's definition, cleared by a Replace that stores a valid one.
	refused error
	state   State
	mu      sync.Mutex
	// gone is set, under mu, once the rule is out of the definitions, so no write after it recreates the file
	// Delete removes.
	gone bool
}

// blocked is why the rule may neither fire nor have its state read or written, nil when it may; rs.mu is held.
func (rs *ruleState) blocked() error {
	if rs.refused != nil {
		return rs.refused
	}
	return rs.unreadable
}

// removeFile is a test seam for a state file removal that fails.
var removeFile = os.Remove

// population is the population a fresh state of r carries: an event rule's, empty for a clock rule.
func population(r *Rule) string {
	ev := r.When.Event
	if ev == nil {
		return ""
	}
	return keyenc.Join(ev.Connection, forgeapi.RepoRef{Selector: ev.Repo}.Encode(), string(ev.Kind))
}

func (s *Store) statePath(ruleID string) string {
	return filepath.Join(s.dir, stateDirName, ruleID+".json")
}

// loadStates reads every rule's state file, blocks each rule refused names, and removes the files whose rule is
// gone, which a Delete whose removal failed leaves behind.
func (s *Store) loadStates(refused map[string]error) {
	s.states = make(map[string]*ruleState, len(s.rules))
	for i := range s.rules {
		r := &s.rules[i]
		rs := s.readState(r)
		rs.refused = refused[r.ID]
		s.states[r.ID] = rs
	}
	entries, err := os.ReadDir(filepath.Join(s.dir, stateDirName))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("launch rules: could not list the state files", "error", err)
		}
		return
	}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() || s.states[id] != nil {
			continue
		}
		s.removeState(id)
	}
}

func (s *Store) readState(r *Rule) *ruleState {
	rs := &ruleState{state: State{Population: population(r)}}
	data, err := os.ReadFile(s.statePath(r.ID))
	if errors.Is(err, os.ErrNotExist) {
		return rs
	}
	if err == nil {
		rs.state, err = parseState(data)
	}
	if err != nil {
		rs.unreadable = &UnreadableStateError{RuleID: r.ID, Err: err}
		slog.Error("launch rule blocked: its state file is unreadable", "rule", r.ID, "error", rs.unreadable)
	}
	return rs
}

func parseState(data []byte) (State, error) {
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}, err
	}
	for i, raw := range st.Queue {
		var entry struct {
			Key *string `json:"key"`
		}
		if json.Unmarshal(raw, &entry) != nil || entry.Key == nil {
			return State{}, fmt.Errorf("queue entry %d has no string key", i)
		}
	}
	return st, nil
}

// State returns a copy of the rule's state: ErrNotFound for no such rule, the refusal of a rule this build
// refused at load, an *UnreadableStateError for a rule whose file would not load.
func (s *Store) State(ruleID string) (State, error) {
	rs, err := s.ruleState(ruleID)
	if err != nil {
		return State{}, err
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if err := rs.blocked(); err != nil {
		return State{}, err
	}
	return rs.state.clone(), nil
}

// StateWriter is the raw write and removal of a rule's state file, which the trigger confirmer persists event
// rules through.
type StateWriter interface {
	WriteState(ctx context.Context, ruleID string, in *State) error
	RemoveState(ruleID string) error
}

var _ StateWriter = (*Store)(nil)

// WriteState replaces the rule's state file with in, touching no other file. ErrNotFound for no such rule; a
// blocked rule refuses with what State answers, so its file is never overwritten.
//
//deadset:ignore DS1102 -- test seam until the forge event runner writes state through it
func (s *Store) WriteState(ctx context.Context, ruleID string, in *State) error {
	rs, err := s.ruleState(ruleID)
	if err != nil {
		return err
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.gone {
		return ErrNotFound
	}
	if err := rs.blocked(); err != nil {
		return err
	}
	st := in.clone()
	return s.writeStateLocked(ctx, ruleID, rs, &st)
}

// writeStateLocked writes st as the rule's state file and, once it lands, as rs's state; rs.mu is held.
func (s *Store) writeStateLocked(ctx context.Context, ruleID string, rs *ruleState, st *State) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the state of launch rule %s: %w", ruleID, err)
	}
	if _, err := filesafe.WriteFile(ctx, s.statePath(ruleID), data,
		filesafe.WithMode(0o600), filesafe.WithMkdirMode(0o700)); err != nil {
		return fmt.Errorf("write the state of launch rule %s: %w", ruleID, err)
	}
	rs.state = *st
	return nil
}

// RemoveState removes the rule's state file; a file already gone is not an error.
func (s *Store) RemoveState(ruleID string) error {
	if err := removeFile(s.statePath(ruleID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove the state of launch rule %s: %w", ruleID, err)
	}
	return nil
}

// removeState is RemoveState for a rule already out of the definitions: a failure is logged, and the next load,
// finding the file's rule gone, removes it again.
func (s *Store) removeState(ruleID string) {
	if err := s.RemoveState(ruleID); err != nil {
		slog.Warn("launch rules: a deleted rule's state file stays until the next start", "rule", ruleID, "error", err)
	}
}

func (s *Store) ruleState(ruleID string) (*ruleState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, ok := s.states[ruleID]
	if !ok {
		return nil, ErrNotFound
	}
	return rs, nil
}

func (st *State) clone() State {
	c := *st
	c.Status.Gaps = slices.Clone(st.Status.Gaps)
	c.Cursor = slices.Clone(st.Cursor)
	c.Queue = slices.Clone(st.Queue)
	for i := range c.Queue {
		c.Queue[i] = slices.Clone(c.Queue[i])
	}
	c.Launched = slices.Clone(st.Launched)
	c.Superseded = slices.Clone(st.Superseded)
	c.Attempts = slices.Clone(st.Attempts)
	return c
}
