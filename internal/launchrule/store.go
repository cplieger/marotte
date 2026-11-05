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
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/cplieger/atomicfile/v4"
)

// rulesFileName is the definitions file, beside mcp.json in the config dir.
const rulesFileName = "launch-rules.json"

// ErrNotFound means no rule has the given id.
var ErrNotFound = errors.New("launch rule not found")

// Store persists the operator's rules in one 0600 JSON file, rewritten atomically per change, in creation order.
type Store struct {
	path  string
	rules []Rule
	mu    sync.Mutex
}

// NewStore opens (or starts) the store at <dir>/launch-rules.json. A file that does not parse, or holds a rule
// the store would refuse, is an error rather than a silent reset of the operator's rules.
func NewStore(dir string) (*Store, error) {
	s := &Store{path: filepath.Join(dir, rulesFileName)}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.path, err)
	}
	if err := json.Unmarshal(data, &s.rules); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.path, err)
	}
	seen := make(map[string]bool, len(s.rules))
	for i := range s.rules {
		r := &s.rules[i]
		if r.ID == "" || seen[r.ID] {
			return nil, fmt.Errorf("%s: rule %d has a missing or repeated id", s.path, i)
		}
		seen[r.ID] = true
		if err := r.normalize(); err != nil {
			return nil, fmt.Errorf("%s: rule %s: %w", s.path, r.ID, err)
		}
	}
	return s, nil
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
// refusal is a *ValidationError and writes nothing.
func (s *Store) Create(ctx context.Context, in *Rule) (Rule, error) {
	r := in.clone()
	if err := r.normalize(); err != nil {
		return Rule{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r.ID = s.mintIDLocked()
	next := append(slices.Clip(s.rules), r)
	if err := s.persistLocked(ctx, next); err != nil {
		return Rule{}, err
	}
	s.rules = next
	return r.clone(), nil
}

// Replace validates in and stores it in place of the rule id names, keeping that id. ErrNotFound when no rule has
// it; a refusal is a *ValidationError and writes nothing.
func (s *Store) Replace(ctx context.Context, id string, in *Rule) (Rule, error) {
	r := in.clone()
	if err := r.normalize(); err != nil {
		return Rule{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.indexLocked(id)
	if i < 0 {
		return Rule{}, ErrNotFound
	}
	r.ID = id
	next := slices.Clone(s.rules)
	next[i] = r
	if err := s.persistLocked(ctx, next); err != nil {
		return Rule{}, err
	}
	s.rules = next
	return r.clone(), nil
}

// Delete removes the rule id names; ErrNotFound when no rule has it.
func (s *Store) Delete(ctx context.Context, id string) error {
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
	return nil
}

func (s *Store) indexLocked(id string) int {
	return slices.IndexFunc(s.rules, func(r Rule) bool { return r.ID == id })
}

// mintIDLocked returns 16 random bytes in base64url, unused by any rule.
func (s *Store) mintIDLocked() string {
	for {
		var b [16]byte
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
	if _, err := atomicfile.WriteFile(ctx, s.path, data,
		atomicfile.WithMode(0o600), atomicfile.WithMkdirMode(0o700)); err != nil {
		return fmt.Errorf("write %s: %w", s.path, err)
	}
	return nil
}
