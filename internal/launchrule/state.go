package launchrule

import (
	"cmp"
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
	// that slot; an event rule's last confirmed launch.
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
	// Blocked is why an event rule launches nothing until the operator edits or resumes it; empty when nothing does.
	Blocked string `json:"blocked,omitempty"`
	// RowText is an event rule's launch state as its row reads it, which the trigger confirmer writes.
	RowText string `json:"row_text,omitempty"`
	// LastEvent is the key of an event rule's last confirmed event, which its row names by number or run id.
	LastEvent string `json:"last_event,omitempty"`
	// CheckedAt is the start of an event rule's last applied poll; the trigger confirmer's View sets it and no write
	// persists it.
	CheckedAt time.Time `json:"checked_at,omitzero"`
	// BaselineStop is where and why an event rule's last walk stopped short while no baseline is taken; the trigger
	// confirmer's View sets it and no write persists it.
	BaselineStop string `json:"baseline_stop,omitempty"`
	// Paused is why an event rule's reads wait for a forge or trigger budget, and until when; empty while they run.
	Paused string `json:"paused,omitempty"`
	// Gaps are an event rule's stretches in which runs may have been missed, kept until the operator dismisses them.
	Gaps          []Gap `json:"gaps,omitempty"`
	BoundAbandons int   `json:"bound_abandons,omitempty"`
	// FailedSlots is a clock rule's count of consecutive slots that ended other than completed; a completed slot sets
	// it back to 0. A slot missed while marotte was down records no outcome, so it does not count.
	FailedSlots int `json:"failed_slots,omitempty"`
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
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	// CreatedBefore is an outage's read horizon: a run created before it that ended in the gap was not checked.
	CreatedBefore time.Time `json:"created_before,omitzero"`
	Cause         string    `json:"cause"`
	// RunID names the aged_out run.
	RunID int64 `json:"run_id,omitempty"`
	// OmittedAtLeast is a result_window's lower bound on the runs the forge did not list.
	OmittedAtLeast int `json:"omitted_at_least,omitempty"`
	// QueueFull marks an aged_out run that matched while the queue was full, rather than one still running.
	QueueFull bool `json:"queue_full,omitempty"`
}

// Held is the number of events the rule holds.
func (st *State) Held() int { return len(st.Queue) }

// UnreadableStateError is a rule's state file that would not read or parse at load. Its file stays untouched
// rather than starting over and dropping what it held: the store blocks a clock rule, and reports an event rule's
// to the trigger confirmer, which blocks it and decides when a write replaces the file.
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
	// unreadable is the load error of a state file that would not parse, cleared once a write replaces the file.
	unreadable error
	// refused is the load-time refusal of the rule's definition, cleared by a Replace that stores a valid one.
	refused error
	state   State
	mu      sync.Mutex
	// gone is set, under mu, once the rule is out of the definitions, so no write after it recreates the file
	// Delete removes.
	gone  bool
	event bool
}

// blocked is why the store refuses the rule's state, nil when it serves it; rs.mu is held. An event rule's state is
// the trigger confirmer's, which keeps it through a refused definition and decides on an unreadable file.
func (rs *ruleState) blocked() error {
	if rs.event {
		return nil
	}
	return cmp.Or(rs.refused, rs.unreadable)
}

// removeFile is a test seam for a state file removal that fails.
var removeFile = os.Remove

// Population is the population a fresh state of r carries: an event rule's, empty for a clock rule.
func Population(r *Rule) string {
	ev := r.When.Event
	if ev == nil {
		return ""
	}
	return keyenc.Join(ev.Connection, forgeapi.RepoRef{Selector: ev.Repo}.Encode(), string(ev.Kind))
}

func (s *Store) statePath(ruleID string) string {
	return filepath.Join(s.dir, stateDirName, ruleID+".json")
}

// loadStates reads every rule's state file and blocks each rule refused names.
func (s *Store) loadStates(refused map[string]error) {
	s.states = make(map[string]*ruleState, len(s.rules))
	for i := range s.rules {
		r := &s.rules[i]
		rs := s.readState(r)
		rs.refused = refused[r.ID]
		s.states[r.ID] = rs
	}
}

// Orphans reads the state files whose rule the definitions do not hold, by rule id: a Delete whose removal failed
// leaves one. The store leaves them on disk, since such a file can name runs still to cancel; its removal is the
// StateWriter holder's. A file that does not parse reads as an empty State.
func (s *Store) Orphans() map[string]State {
	entries, err := os.ReadDir(filepath.Join(s.dir, stateDirName))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("launch rules: could not list the state files", "error", err)
		}
		return nil
	}
	out := map[string]State{}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() {
			continue
		}
		if _, err := s.ruleState(id); err == nil {
			continue
		}
		var st State
		if data, err := os.ReadFile(s.statePath(id)); err == nil {
			st, _ = parseState(data)
		}
		out[id] = st
	}
	return out
}

func (s *Store) readState(r *Rule) *ruleState {
	rs := &ruleState{state: State{Population: Population(r)}, event: r.When.Event != nil}
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
// refused at load, an *UnreadableStateError for a rule whose file would not load until a write replaces it.
func (s *Store) State(ruleID string) (State, error) {
	rs, err := s.ruleState(ruleID)
	if err != nil {
		return State{}, err
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if err := cmp.Or(rs.refused, rs.unreadable); err != nil {
		return State{}, err
	}
	return rs.state.clone(), nil
}

// LoadedState is State through a refused definition: the state the rule's earlier runs left, which a refusal does
// not void.
func (s *Store) LoadedState(ruleID string) (State, error) {
	rs, err := s.ruleState(ruleID)
	if err != nil {
		return State{}, err
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.unreadable != nil {
		return State{}, rs.unreadable
	}
	return rs.state.clone(), nil
}

// Refused is why this build refuses the rule's stored definition, nil when it takes it; ErrNotFound for no such rule.
func (s *Store) Refused(ruleID string) error {
	rs, err := s.ruleState(ruleID)
	if err != nil {
		return err
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.refused
}

// StateWriter is the raw write and removal of a rule's state file. NewStore hands out the only one, which the
// trigger confirmer holds, so every write of an event rule's state goes through it.
type StateWriter interface {
	WriteState(ctx context.Context, ruleID string, in *State) error
	RemoveState(ruleID string) error
}

type stateWriter struct{ s *Store }

// WriteState replaces the rule's state file with in, touching no other file. ErrNotFound for no such rule; a clock
// rule refused or with an unreadable file refuses with what State answers. An event rule's is always written: the
// trigger confirmer decides.
func (w stateWriter) WriteState(ctx context.Context, ruleID string, in *State) error {
	s := w.s
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

// RemoveState removes the rule's state file; a file already gone is not an error.
func (w stateWriter) RemoveState(ruleID string) error { return w.s.removeStateFile(ruleID) }

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
	rs.state, rs.unreadable = *st, nil
	return nil
}

func (s *Store) removeStateFile(ruleID string) error {
	if err := removeFile(s.statePath(ruleID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove the state of launch rule %s: %w", ruleID, err)
	}
	return nil
}

// removeState is removeStateFile for a rule already out of the definitions: a failure is logged, and the next load
// reports the file among Orphans.
func (s *Store) removeState(ruleID string) {
	if err := s.removeStateFile(ruleID); err != nil {
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
