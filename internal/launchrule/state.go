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

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/forgeapi"
	"github.com/cplieger/keyenc"
)

// stateDirName holds one state file per rule, <rule id>.json, beside the definitions file.
const stateDirName = "launch-state"

// State is what the server writes about one rule, in that rule's own file: no client write reaches it.
type State struct {
	Status Status `json:"status"`
	// Population is the event rule's keyenc.Join(connection, RepoRef.Encode(), kind) the state was built for.
	Population string          `json:"population,omitempty"`
	Cursor     json.RawMessage `json:"cursor,omitempty"`
	// Queue holds the rule's held events; each entry is a JSON object with a string "key".
	Queue      []json.RawMessage `json:"queue,omitempty"`
	Launched   json.RawMessage   `json:"launched,omitempty"`
	Superseded json.RawMessage   `json:"superseded,omitempty"`
	Attempts   json.RawMessage   `json:"attempts,omitempty"`
}

// Status is a rule's row state.
type Status struct {
	// Anchor is a clock rule's next-run origin: its creation, then its last fire or skip.
	Anchor time.Time `json:"anchor,omitzero"`
	// LastMinted is an event rule's last label-gain instant minted.
	LastMinted time.Time `json:"last_minted,omitzero"`
	// FailingSince starts an event rule's 24-hour failure clock; zero when nothing is failing.
	FailingSince time.Time `json:"failing_since,omitzero"`
	// LastRunID is the only run whose outcome may land on the row.
	LastRunID     string `json:"last_run_id,omitempty"`
	BoundAbandons int    `json:"bound_abandons,omitempty"`
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
	state      State
	mu         sync.Mutex
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

// loadStates reads every rule's state file and removes those whose rule is gone, which a Delete whose removal
// failed leaves behind.
func (s *Store) loadStates() {
	s.states = make(map[string]*ruleState, len(s.rules))
	for i := range s.rules {
		r := &s.rules[i]
		s.states[r.ID] = s.readState(r)
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

// State returns a copy of the rule's state: ErrNotFound for no such rule, an *UnreadableStateError for a rule
// whose file would not load.
func (s *Store) State(ruleID string) (State, error) {
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

// WriteState replaces the rule's state file with in, touching no other file. ErrNotFound for no such rule; a
// rule whose file would not load refuses with its *UnreadableStateError, so its file is never overwritten.
func (s *Store) WriteState(ctx context.Context, ruleID string, in *State) error {
	rs, err := s.ruleState(ruleID)
	if err != nil {
		return err
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.unreadable != nil {
		return rs.unreadable
	}
	st := in.clone()
	data, err := json.MarshalIndent(&st, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the state of launch rule %s: %w", ruleID, err)
	}
	if _, err := atomicfile.WriteFile(ctx, s.statePath(ruleID), data,
		atomicfile.WithMode(0o600), atomicfile.WithMkdirMode(0o700)); err != nil {
		return fmt.Errorf("write the state of launch rule %s: %w", ruleID, err)
	}
	rs.state = st
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
