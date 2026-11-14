// Package securityprofile selects the security profile every session opens with and runs
// Customize, the one profile change that writes a policy file. The profile id lives in
// config.json; Customize copies the presets in force into the USER permissions file, then
// persists custom. A write's settings event goes out through Store.Publish; callers own the rest
// of what follows a change: recycling sessions and the permissions event.
package securityprofile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/policyfile"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/marotte/internal/workspace"
)

// Customize's refusals. Each leaves both files as they were, except ErrRestoreFailed.
var (
	// ErrNoLivePolicy: the profile's own rules could not be read. An empty Custom drops every
	// grant and is indistinguishable from a session not yet started, so nothing is written.
	ErrNoLivePolicy = errors.New("no live policy to read preset rules from")
	// ErrUserFileUnreadable: the user permissions file exists and cannot be read or parsed.
	ErrUserFileUnreadable = errors.New("the user permissions file could not be read")
	// ErrUserFileFull: the user permissions file is at its rule limit.
	ErrUserFileFull = errors.New("the user permissions file is at its rule limit")
	// ErrRestoreFailed: persisting custom failed AND the copied rules could not be taken back
	// out, so the user file holds them under the old profile.
	ErrRestoreFailed = errors.New("the copied rules could not be taken back out of the user permissions file")
)

// Store resolves and changes the profile for one workspace. List reads the live policy of a
// session opened with the active profile; nil means no live policy. Publish gets the
// settings_updated event of each profile write (settings.Update's publish); nil sends none.
type Store struct {
	List      func(ctx context.Context, scope string) ([]marotte.PolicyRule, error)
	Publish   func(context.Context, marotte.ServerEvent)
	ConfigDir string
	WorkDir   string
}

// Active is the Settings profile a new chat starts on, with the session door's fallback, so a
// reader shows what that chat's door sends.
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
	}, s.Publish)
	return err
}

// switchMu serializes every switch to Custom in the process, whichever Store runs it: the
// decision reads the profile from config.json and then writes the user file and config.json, so
// of two concurrent switches only the first may copy the presets.
var switchMu sync.Mutex

// Seams over Customize's two writes, so a test can fail the persist or make the user file busy.
var (
	persistCustom = (*Store).Select
	mutatePolicy  = policyfile.Mutate
)

// Customize copies the preset rules in force into the user file, then persists custom. The
// read comes first, and a failed persist takes back out exactly the rules it copied, keeping any
// rule written meanwhile; a user file that kept changing is policyfile.ErrBusy. It is durable
// from its first write: a caller walking away must not strand the copied grants. On Custom it
// refuses with ErrNoLivePolicy, since Custom carries no presets and a stale request could still
// read a session on the old profile and save over a rule KAS wrote since the switch.
func (s *Store) Customize(ctx context.Context) error {
	switchMu.Lock()
	defer switchMu.Unlock()
	if s.Active(ctx) == policyfile.ProfileCustom {
		return ErrNoLivePolicy
	}
	return s.customize(ctx)
}

func (s *Store) customize(ctx context.Context) error {
	seeded, err := s.presetRulesInForce(ctx)
	if err != nil {
		return err
	}
	kiroHome, err := workspace.KiroHomeDir()
	if err != nil {
		return err
	}
	path, err := policyfile.PathFor(policyfile.ScopeUser, policyfile.Roots{KiroHome: kiroHome, WorkDir: s.WorkDir})
	if err != nil {
		return err
	}
	ctx = durable.Context(ctx)
	var added []policyfile.Rule
	if err := mutatePolicy(ctx, path, seedEdit(seeded, &added)); err != nil {
		if errors.Is(err, policyfile.ErrUnreadable) {
			slog.Warn("the user permissions file could not be read, so Customize was refused", "path", path, "error", logsafe.Field(err.Error()))
			return fmt.Errorf("%w: %v", ErrUserFileUnreadable, err)
		}
		return fmt.Errorf("copy the preset rules into the user permissions file: %w", err)
	}
	if err := persistCustom(s, ctx, policyfile.ProfileCustom); err != nil {
		if rErr := mutatePolicy(ctx, path, removeEdit(added)); rErr != nil {
			slog.Error("could not take the copied rules back out of the user permissions file after a failed Customize",
				"path", path, "error", logsafe.Field(rErr.Error()))
			return fmt.Errorf("%w: %w", ErrRestoreFailed, err)
		}
		return err
	}
	return nil
}

// seedEdit is Customize's edit of the user file: it adds each seeded rule the file lacks and
// records in added exactly the rules this call added, so a retried attempt records only its own.
func seedEdit(seeded []policyfile.Rule, added *[]policyfile.Rule) func(*policyfile.File) (bool, error) {
	return func(f *policyfile.File) (bool, error) {
		*added = nil
		for i := range seeded {
			changed, err := f.Upsert(&seeded[i])
			if err != nil {
				return false, ErrUserFileFull
			}
			if changed {
				*added = append(*added, seeded[i])
			}
		}
		return len(*added) > 0, nil
	}
}

// removeEdit takes rules back out of whatever the file holds now, keeping every other rule.
func removeEdit(rules []policyfile.Rule) func(*policyfile.File) (bool, error) {
	return func(f *policyfile.File) (bool, error) {
		changed := false
		for i := range rules {
			changed = f.Remove(&rules[i]) || changed
		}
		return changed, nil
	}
}

// presetRulesInForce reads the rules the ACTIVE profile's presets contributed to the live
// policy; no RPC enumerates a preset's rules, so a session opened with them is the source.
// Session scope AND a source naming one of those presets: scope alone would also catch a
// one-session consent, and a session still on another profile would hand over its grants.
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
		preset, fromPreset := strings.CutPrefix(r.Source, policyfile.PresetSourcePrefix)
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
