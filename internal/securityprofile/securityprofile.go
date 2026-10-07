// Package securityprofile selects the security profile every session opens with and runs
// Customize, the one profile change that writes a policy file. The profile id lives in
// config.json; Customize copies the presets in force into the USER permissions file, then
// persists custom. Callers own what follows a change: recycling sessions and telling clients.
package securityprofile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/policyfile"
	"github.com/cplieger/marotte/internal/settings"
)

// Customize's refusals. Each leaves both files as they were, except ErrRestoreFailed.
var (
	// ErrNoLivePolicy: the profile's own rules could not be read. An empty Custom drops every
	// grant and is indistinguishable from a session not yet started, so nothing is written.
	ErrNoLivePolicy = errors.New("no live policy to read preset rules from")
	// ErrUserFileUnreadable: the user permissions file exists and does not parse.
	ErrUserFileUnreadable = errors.New("the user permissions file could not be read")
	// ErrUserFileFull: the user permissions file is at its rule limit.
	ErrUserFileFull = errors.New("the user permissions file is at its rule limit")
	// ErrRestoreFailed: persisting custom failed AND the copied rules could not be taken back
	// out, so the user file holds them under the old profile.
	ErrRestoreFailed = errors.New("the copied rules could not be taken back out of the user permissions file")
)

// presetRuleSource is the prefix KAS stamps on a rule resolved from a preset (`preset:<id>`).
// No RPC enumerates a preset's rules, so the live view of a session opened with them is the source.
const presetRuleSource = "preset:"

// Store resolves and changes the profile for one workspace. List reads the live policy of a
// session opened with the active profile; nil means no live policy.
type Store struct {
	List      func(ctx context.Context, scope string) ([]marotte.PolicyRule, error)
	ConfigDir string
	WorkDir   string
}

// Active is the profile in force with the session door's fallback, so a reader shows what the
// sessions opened with.
func (s *Store) Active(ctx context.Context) string {
	var id string
	if !settings.FieldInto(ctx, s.ConfigDir, settings.KeySecurityProfile, &id) || id == "" {
		return policyfile.DefaultProfile
	}
	if _, ok := policyfile.ProfileFor(id); !ok {
		return policyfile.DefaultProfile
	}
	return id
}

// Select persists id into config.json; an unreadable document refuses. It writes no policy file.
func (s *Store) Select(ctx context.Context, id string) error {
	raw, err := json.Marshal(id)
	if err != nil {
		return err
	}
	_, err = settings.Update(ctx, s.ConfigDir, func(doc map[string]json.RawMessage) error {
		doc[settings.KeySecurityProfile] = raw
		return nil
	})
	return err
}

// customizeMu serializes every switch to Custom in the process, whichever Store runs it: each
// is a read-modify-write of the user file, and one that loaded the file before KAS wrote an
// always answer's rule into it would erase that rule when it saved.
var customizeMu sync.Mutex

// Customize copies the preset rules in force into the user file, then persists custom. The
// read comes first, and a failed persist puts the file back. It is durable from its first
// write: a caller walking away must not strand the copied grants. On Custom it refuses with
// ErrNoLivePolicy, since Custom carries no presets and a stale request could still read a
// session on the old profile and save over a rule KAS wrote since the switch.
func (s *Store) Customize(ctx context.Context) error {
	customizeMu.Lock()
	defer customizeMu.Unlock()
	if s.Active(ctx) == policyfile.ProfileCustom {
		return ErrNoLivePolicy
	}
	return s.customize(ctx)
}

// EnsureCustom runs Customize unless the profile already is Custom, deciding under the same
// lock, so of two concurrent callers only the first switches. switched reports whether this
// call did.
func (s *Store) EnsureCustom(ctx context.Context) (switched bool, err error) {
	customizeMu.Lock()
	defer customizeMu.Unlock()
	if s.Active(ctx) == policyfile.ProfileCustom {
		return false, nil
	}
	if err := s.customize(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) customize(ctx context.Context) error {
	seeded, err := s.presetRulesInForce(ctx)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path, err := policyfile.PathFor(policyfile.ScopeUser, policyfile.Roots{Home: home, WorkDir: s.WorkDir})
	if err != nil {
		return err
	}
	_, statErr := os.Stat(path)
	existed := statErr == nil
	f, err := policyfile.Load(path)
	if err != nil {
		slog.Warn("the user permissions file could not be read, so Customize was refused", "path", path, "error", logsafe.Field(err.Error()))
		return fmt.Errorf("%w: %v", ErrUserFileUnreadable, err)
	}
	before := slices.Clone(f.Rules)
	for i := range seeded {
		if _, uErr := f.Upsert(&seeded[i]); uErr != nil {
			return ErrUserFileFull
		}
	}
	ctx = durable.Context(ctx)
	if err := policyfile.Save(ctx, path, f); err != nil {
		return err
	}
	if err := s.Select(ctx, policyfile.ProfileCustom); err != nil {
		if rErr := restoreUserFile(ctx, path, existed, before); rErr != nil {
			slog.Error("could not restore the user permissions file after a failed Customize",
				"path", path, "error", logsafe.Field(rErr.Error()))
			return fmt.Errorf("%w: %w", ErrRestoreFailed, err)
		}
		return err
	}
	return nil
}

// presetRulesInForce reads the rules the ACTIVE profile's presets contributed to the live
// policy. Session scope AND a source naming one of those presets: scope alone would also catch
// a one-session consent, and a session still on another profile would hand over its grants.
// Rules are sanitized on the way through.
func (s *Store) presetRulesInForce(ctx context.Context) ([]policyfile.Rule, error) {
	if s.List == nil {
		return nil, ErrNoLivePolicy
	}
	profile, _ := policyfile.ProfileFor(s.Active(ctx))
	live, err := s.List(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoLivePolicy, err)
	}
	out := make([]policyfile.Rule, 0, len(live))
	for i := range live {
		r := &live[i]
		preset, fromPreset := strings.CutPrefix(r.Source, presetRuleSource)
		if r.Scope != "session" || !fromPreset || !slices.Contains(profile.Presets, preset) {
			continue
		}
		clean, sErr := policyfile.SanitizeRule(&policyfile.Rule{
			Capability: r.Capability, Effect: r.Effect,
			Match: r.Match, Exclude: r.Exclude,
		})
		if sErr != nil {
			slog.Warn("skipping a preset rule that the file format cannot hold",
				"capability", logsafe.Field(r.Capability), "source", logsafe.Field(r.Source), "error", logsafe.Field(sErr.Error()))
			continue
		}
		out = append(out, clean)
	}
	if len(out) == 0 {
		return nil, ErrNoLivePolicy
	}
	return out, nil
}

func restoreUserFile(ctx context.Context, path string, existed bool, before []policyfile.Rule) error {
	if !existed {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return policyfile.Save(ctx, path, &policyfile.File{Rules: before})
}
