// Package launchrule owns the launch rules, which start a recipe on a clock or on a forge event, and the files
// that hold them.
package launchrule

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/go/filesafe"
)

// rulesFileName is the definitions file, beside mcp.json in the config dir.
const rulesFileName = "launch-rules.json"

// idBytes is a rule id's random width before base64url.
const idBytes = 16

// ErrNotFound means no rule has the given id.
var ErrNotFound = errors.New("launch rule not found")

// Store persists the operator's rules in one 0600 JSON file, rewritten atomically per change, in creation order,
// and each rule's state in a file of its own under launch-state (0700), written under that rule's own mutex.
type Store struct {
	states map[string]*ruleState
	now    func() time.Time
	dir    string
	path   string
	rules  []Rule
	mu     sync.Mutex
}

// NewStore opens (or starts) the store at <dir>/launch-rules.json. A file that does not parse, or holds a rule
// the store would refuse, is an error rather than a silent reset of the operator's rules.
func NewStore(dir string) (*Store, error) {
	s := &Store{dir: dir, path: filepath.Join(dir, rulesFileName), now: time.Now}
	if err := s.loadRules(); err != nil {
		return nil, err
	}
	s.loadStates()
	return s, nil
}

func (s *Store) loadRules() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", s.path, err)
	}
	if err := json.Unmarshal(data, &s.rules); err != nil {
		return fmt.Errorf("parse %s: %w", s.path, err)
	}
	seen := make(map[string]bool, len(s.rules))
	for i := range s.rules {
		r := &s.rules[i]
		if !validID(r.ID) || seen[r.ID] {
			return fmt.Errorf("%s: rule %d has a malformed or repeated id", s.path, i)
		}
		seen[r.ID] = true
		if err := r.Normalize(); err != nil {
			return fmt.Errorf("%s: rule %s: %w", s.path, r.ID, err)
		}
	}
	return nil
}

// List returns a copy of every rule, in creation order.
func (s *Store) List() []Rule {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Rule, len(s.rules))
	for i := range s.rules {
		out[i] = s.rules[i].clone()
	}
	return out
}

// Get returns a copy of the rule id names, false when none does.
func (s *Store) Get(id string) (Rule, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.indexLocked(id); i >= 0 {
		return s.rules[i].clone(), true
	}
	return Rule{}, false
}

// Create validates in, mints its id (any id in carries is ignored) and stores it, returning the stored rule. A
// clock rule is anchored at its creation, so it does not fire for every slot since the epoch. A refusal is a
// *ValidationError and writes nothing.
func (s *Store) Create(ctx context.Context, in *Rule) (Rule, error) {
	r := in.clone()
	if err := r.Normalize(); err != nil {
		return Rule{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r.ID = s.mintIDLocked()
	rs := &ruleState{state: State{Population: population(&r)}}
	if r.When.Clock != nil {
		// Written before the rule, so no load finds the rule without its anchor.
		st := State{Status: Status{Anchor: s.now()}}
		if err := s.writeStateLocked(ctx, r.ID, rs, &st); err != nil {
			return Rule{}, err
		}
	}
	next := append(slices.Clip(s.rules), r)
	if err := s.persistLocked(ctx, next); err != nil {
		if r.When.Clock != nil {
			s.removeState(r.ID)
		}
		return Rule{}, err
	}
	s.rules = next
	s.states[r.ID] = rs
	return r.clone(), nil
}

// Replace validates in and stores it in place of the rule id names, keeping that id. An edit that changes an
// event rule's connection, repo or kind starts its state over (cursor, queue, launched, superseded) and keeps its
// status; while that would drop held events it is refused with a *DropError unless confirmDrop is their count.
// ErrNotFound when no rule has the id; a refusal is a *ValidationError or *DropError and writes nothing.
func (s *Store) Replace(ctx context.Context, id string, in *Rule, confirmDrop int) (Rule, error) {
	r := in.clone()
	if err := r.Normalize(); err != nil {
		return Rule{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.indexLocked(id)
	if i < 0 {
		return Rule{}, ErrNotFound
	}
	r.ID = id
	rs := s.states[id]
	rebaseline := population(&s.rules[i]) != population(&r)
	if rebaseline {
		if held := heldOf(rs); held > 0 && held != confirmDrop {
			return Rule{}, &DropError{Held: held}
		}
	}
	next := slices.Clone(s.rules)
	next[i] = r
	if err := s.persistLocked(ctx, next); err != nil {
		return Rule{}, err
	}
	s.rules = next
	if rebaseline {
		s.rebaselineLocked(ctx, &r, rs)
	}
	if r.When.Clock != nil {
		s.dropStaleWaitLocked(ctx, &r)
	}
	return r.clone(), nil
}

func heldOf(rs *ruleState) int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.unreadable != nil {
		return 0
	}
	return rs.state.Held()
}

// rebaselineLocked starts r's state over for its new population; a file that would not load stays untouched. A
// failed write is logged, since the definitions already hold the edit.
func (s *Store) rebaselineLocked(ctx context.Context, r *Rule, rs *ruleState) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.unreadable != nil {
		return
	}
	st := State{Status: rs.state.Status, Population: population(r), Attempts: slices.Clone(rs.state.Attempts)}
	if err := s.writeStateLocked(ctx, r.ID, rs, &st); err != nil {
		slog.Error("launch rules: an edited rule's held events were not dropped", "rule", r.ID, "error", err)
	}
}

// dropStaleWaitLocked clears a clock rule's wait on the engine unless the edited rule, enabled, still waits for
// that same due slot. A failed write is logged: the stale wait names a slot the edited rule no longer reaches.
func (s *Store) dropStaleWaitLocked(ctx context.Context, r *Rule) {
	rs := s.states[r.ID]
	rs.mu.Lock()
	defer rs.mu.Unlock()
	wait := rs.state.Status.WaitingFor
	if rs.unreadable != nil || wait.IsZero() {
		return
	}
	if due, err := NextRun(*r.When.Clock, rs.state.Status.Anchor); r.Enabled && err == nil && due.Equal(wait) {
		return
	}
	st := rs.state.clone()
	st.Status.WaitingFor = time.Time{}
	if err := s.writeStateLocked(ctx, r.ID, rs, &st); err != nil {
		slog.Warn("launch rules: an edited rule's wait on the engine was not cleared", "rule", r.ID, "error", err)
	}
}

// Delete removes the rule id names, then its state file; ErrNotFound when no rule has it. A state file that will
// not go is logged and removed at the next load, so the rule's deletion still stands.
func (s *Store) Delete(ctx context.Context, id string) error {
	if err := s.deleteRule(ctx, id); err != nil {
		return err
	}
	s.removeState(id)
	return nil
}

func (s *Store) deleteRule(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.indexLocked(id)
	if i < 0 {
		return ErrNotFound
	}
	next := slices.Delete(slices.Clone(s.rules), i, i+1)
	if err := s.persistLocked(ctx, next); err != nil {
		return err
	}
	s.rules = next
	if rs := s.states[id]; rs != nil {
		rs.mu.Lock()
		rs.gone = true
		rs.mu.Unlock()
	}
	delete(s.states, id)
	return nil
}

func (s *Store) indexLocked(id string) int {
	return slices.IndexFunc(s.rules, func(r Rule) bool { return r.ID == id })
}

// validID reports whether id is one mintIDLocked could return, which also keeps it one plain file name.
func validID(id string) bool {
	b, err := base64.RawURLEncoding.DecodeString(id)
	return err == nil && len(b) == idBytes && base64.RawURLEncoding.EncodeToString(b) == id
}

// mintIDLocked returns 16 random bytes in base64url, unused by any rule.
func (s *Store) mintIDLocked() string {
	for {
		var b [idBytes]byte
		_, _ = rand.Read(b[:])
		if id := base64.RawURLEncoding.EncodeToString(b[:]); s.indexLocked(id) < 0 {
			return id
		}
	}
}

func (s *Store) persistLocked(ctx context.Context, rules []Rule) error {
	if rules == nil {
		rules = []Rule{}
	}
	data, err := json.MarshalIndent(rules, "", "  ")
	if err != nil {
		return fmt.Errorf("encode launch rules: %w", err)
	}
	if _, err := filesafe.WriteFile(ctx, s.path, data,
		filesafe.WithMode(0o600), filesafe.WithMkdirMode(0o700)); err != nil {
		return fmt.Errorf("write %s: %w", s.path, err)
	}
	return nil
}
