package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

type seedStoreCLI struct {
	store     map[string]string
	writes    [][2]string
	mu        sync.Mutex
	listFails bool
}

var _ cliRunner = (*seedStoreCLI)(nil)

func (f *seedStoreCLI) Run(_ context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case len(args) == 2 && args[0] == "settings":
		v, ok := f.store[args[1]]
		if !ok {
			return nil, errors.New("seedStoreCLI: no value")
		}
		return []byte(v + " (global)"), nil
	case len(args) == 3 && args[0] == "settings":
		if f.store == nil {
			f.store = map[string]string{}
		}
		f.store[args[1]] = args[2]
		f.writes = append(f.writes, [2]string{args[1], args[2]})
		return nil, nil
	}
	return nil, fmt.Errorf("seedStoreCLI: unexpected Run args %v", args)
}

func (f *seedStoreCLI) RunStdoutCapped(_ context.Context, _ int, args ...string) ([]byte, bool, error) {
	if !slices.Equal(args, settingsListArgs) {
		return nil, false, fmt.Errorf("seedStoreCLI: unexpected RunStdoutCapped args %v", args)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listFails {
		return nil, false, errors.New("seedStoreCLI: list refused")
	}
	doc := make(map[string]json.RawMessage, len(f.store))
	for k, v := range f.store {
		doc[k] = json.RawMessage(v)
	}
	out, err := json.Marshal(doc)
	return out, false, err
}

func (f *seedStoreCLI) value(key string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.store[key]
	return v, ok
}

func (f *seedStoreCLI) wrote(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.ContainsFunc(f.writes, func(w [2]string) bool { return w[0] == key })
}

var wantKiroSeeds = map[string]string{
	"chat.enableKnowledge":                   "true",
	"chat.enableSubagent":                    "true",
	"chat.enablePromptHints":                 "true",
	"hooks.showStatus":                       "true",
	"telemetry.enabled":                      "false",
	"chat.disableInheritingDefaultResources": "false",
}

func opposite(v string) string {
	if v == "true" {
		return "false"
	}
	return "true"
}

func TestSeedKiroSettings_KeepsAUserValueOppositeToTheSeed(t *testing.T) {
	for key, seed := range wantKiroSeeds {
		t.Run(key, func(t *testing.T) {
			user := opposite(seed)
			cli := &seedStoreCLI{store: map[string]string{key: user}}
			s := &Server{cliRunner: cli, cliTimeouts: defaultCLITimeouts()}

			s.SeedKiroSettings(t.Context())

			if got, _ := cli.value(key); got != user {
				t.Errorf("after SeedKiroSettings %s = %q, want the user's %q", key, got, user)
			}
			if cli.wrote(key) {
				t.Errorf("SeedKiroSettings wrote %s, which already held the user's value", key)
			}
		})
	}
}

func TestSeedKiroSettings_SeedsEveryAbsentKey(t *testing.T) {
	cli := &seedStoreCLI{store: map[string]string{"unrelated.key": "1"}}
	s := &Server{cliRunner: cli, cliTimeouts: defaultCLITimeouts()}

	s.SeedKiroSettings(t.Context())

	for key, want := range wantKiroSeeds {
		if got, ok := cli.value(key); !ok || got != want {
			t.Errorf("after SeedKiroSettings on an unset %s it holds (%q, set=%v), want %q", key, got, ok, want)
		}
	}
	if got, _ := cli.value("unrelated.key"); got != "1" {
		t.Errorf("SeedKiroSettings changed a key it does not own: unrelated.key = %q, want \"1\"", got)
	}
}

func TestSeedKiroSettings_SeedsNothingWhenTheListFails(t *testing.T) {
	cli := &seedStoreCLI{listFails: true, store: map[string]string{"hooks.showStatus": "false"}}
	s := &Server{cliRunner: cli, cliTimeouts: defaultCLITimeouts()}

	s.SeedKiroSettings(t.Context())

	if len(cli.writes) != 0 {
		t.Errorf("SeedKiroSettings with an unlistable store wrote %v; an unknown state must not be overwritten", cli.writes)
	}
}

func TestSeedKiroSettings_LeavesALockedKeyToItsLock(t *testing.T) {
	const key = "telemetry.enabled"
	lock := fakeLocks{marotte.LockTelemetry: {Value: true, Source: marotte.LockSourceOrganization}}
	t.Run("seed_under_a_standing_lock", func(t *testing.T) {
		cli := &seedStoreCLI{}
		s := &Server{cliRunner: cli, cliTimeouts: defaultCLITimeouts(), governance: lock}

		s.SeedKiroSettings(t.Context())
		if cli.wrote(key) {
			t.Errorf("SeedKiroSettings wrote the locked %s; the lock owns it", key)
		}
		s.ApplyGovernanceLocks(t.Context())
		if got, _ := cli.value(key); got != "true" {
			t.Errorf("after seed then lock %s = %q, want the lock's \"true\"", key, got)
		}
	})
	t.Run("lock_then_seed", func(t *testing.T) {
		cli := &seedStoreCLI{}
		s := &Server{cliRunner: cli, cliTimeouts: defaultCLITimeouts(), governance: lock}

		s.ApplyGovernanceLocks(t.Context())
		s.SeedKiroSettings(t.Context())
		if got, _ := cli.value(key); got != "true" {
			t.Errorf("after lock then seed %s = %q, want the lock's \"true\"", key, got)
		}
	})
	t.Run("lock_after_the_seed_restores_it_on_lift", func(t *testing.T) {
		cli := &seedStoreCLI{}
		gov := &liveLocks{}
		s := &Server{cliRunner: cli, cliTimeouts: defaultCLITimeouts(), governance: gov}

		s.SeedKiroSettings(t.Context())
		gov.set(lock)
		s.ApplyGovernanceLocks(t.Context())
		if got, _ := cli.value(key); got != "true" {
			t.Fatalf("under the lock %s = %q, want the lock's \"true\"", key, got)
		}
		gov.set(nil)
		s.ApplyGovernanceLocks(t.Context())
		if got, _ := cli.value(key); got != "false" {
			t.Errorf("after the lock lifted %s = %q, want the seeded \"false\" back", key, got)
		}
	})
}

func TestSeedKiroSettings_ReconcilesOnlyAfterAWrite(t *testing.T) {
	generation := 0
	eng := &fakeEngine{sessionSettings: func(context.Context) string { return fmt.Sprint(generation) }}
	cli := &seedStoreCLI{}
	s := &Server{cliRunner: cli, cliTimeouts: defaultCLITimeouts(), agent: eng}

	generation = 1
	s.SeedKiroSettings(t.Context())
	if eng.reopens != 1 {
		t.Fatalf("after a seeding pass reopens = %d, want 1", eng.reopens)
	}
	generation = 2
	s.SeedKiroSettings(t.Context())
	if eng.reopens != 1 {
		t.Errorf("after a pass that seeded nothing reopens = %d, want still 1", eng.reopens)
	}
}

func TestAllowedKiroSettings_EverySeedIsAValueTheKeyAccepts(t *testing.T) {
	for key, meta := range allowedKiroSettings {
		if got := safeKiroSettingValueFor(meta.Seed, meta.Kind); got == "" || got != meta.Seed {
			t.Errorf("allowedKiroSettings[%q].Seed = %q, which its kind refuses", key, meta.Seed)
		}
	}
}
