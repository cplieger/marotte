package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/policyfile"
	"github.com/cplieger/marotte/internal/settings"
)

// fakeEngine records what was broadcast, so a test can assert the client was told.
type fakeEngine struct {
	events         []marotte.ServerEvent
	pushes         int
	terminalPushes int
	ccPushes       int
}

func (f *fakeEngine) RegisterRoutes(*http.ServeMux) {}
func (f *fakeEngine) Broadcast(_ context.Context, evt marotte.ServerEvent) {
	f.events = append(f.events, evt)
}
func (f *fakeEngine) Shutdown(context.Context) error { return nil }
func (f *fakeEngine) Epoch() string                  { return "fake-epoch" }

// PushAgentIgnoreFiles counts agent-ignore fan-outs for the settings PATCH test.
func (f *fakeEngine) PushAgentIgnoreFiles(context.Context) { f.pushes++ }

func (f *fakeEngine) PushTerminalSettings(context.Context) { f.terminalPushes++ }

func (f *fakeEngine) PushContentCollection(context.Context) { f.ccPushes++ }

// fakeReload records whether the profile change asked for a session recycle.
type fakeReload struct{ restarts int }

func (f *fakeReload) RestartUtilitySession() { f.restarts++ }

// profileFixture stages a HOME and a workspace and returns the server plus both policy paths.
// t.Setenv because policyfile.PathFor reads os.UserHomeDir, so this file is not parallel.
func profileFixture(t *testing.T, live []marotte.PolicyRule) (*Server, *fakeEngine, *fakeReload, string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	eng := &fakeEngine{}
	reload := &fakeReload{}
	s := &Server{
		policy:       &fakePolicy{rules: live},
		policyReload: reload,
		agent:        eng,
		workDir:      work,
		configDir:    t.TempDir(),
	}
	userPath, err := policyfile.PathFor(policyfile.ScopeUser, policyfile.Roots{Home: home, WorkDir: work})
	if err != nil {
		t.Fatalf("user path: %v", err)
	}
	wsPath, err := policyfile.PathFor(policyfile.ScopeWorkspace, policyfile.Roots{Home: home, WorkDir: work})
	if err != nil {
		t.Fatalf("workspace path: %v", err)
	}
	return s, eng, reload, userPath, wsPath
}

func postProfile(t *testing.T, s *Server, body profileBody) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/permissions/profile", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	s.handlePolicyProfile(rec, req)
	return rec
}

// seedRule is a bare session-scope rule as KAS reports one it resolved from a
// preset. The source prefix is what materialisation keys on.
func seedRule(capability, preset string) marotte.PolicyRule {
	return marotte.PolicyRule{
		Capability: capability, Effect: "allow",
		Scope: "session", Source: "preset:" + preset,
	}
}

func loadRules(t *testing.T, path string) []policyfile.Rule {
	t.Helper()
	f, err := policyfile.Load(path)
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
	}
	return f.Rules
}

// ruleCapabilities is the sorted capability projection of a rule set.
func ruleCapabilities(rules []policyfile.Rule) []string {
	out := make([]string, 0, len(rules))
	for i := range rules {
		out = append(out, rules[i].Capability)
	}
	slices.Sort(out)
	return out
}

// readBytes returns a file's bytes, or nil when it does not exist.
func readBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

// TestPolicyProfile_SelectionWritesNoPolicyFile pins that no selection touches either
// permissions file (bare allows survive byte for byte; a malformed byte is never read).
func TestPolicyProfile_SelectionWritesNoPolicyFile(t *testing.T) {
	staged := []policyfile.Rule{
		{Capability: "all", Effect: policyfile.EffectAllow},
		{Capability: "sandbox_network", Effect: policyfile.EffectAllow},
		{Capability: "shell", Effect: policyfile.EffectAllow},
	}
	for _, id := range []string{
		policyfile.ProfileGuarded, policyfile.ProfileReadOnly, policyfile.ProfileTrusted,
		policyfile.ProfileUnrestricted, policyfile.ProfileCustom,
	} {
		t.Run(id+"/staged files are left byte for byte", func(t *testing.T) {
			s, eng, reload, userPath, wsPath := profileFixture(t, []marotte.PolicyRule{seedRule("fs_read", "read-workspace")})
			for _, path := range []string{userPath, wsPath} {
				if err := policyfile.Save(t.Context(), path, &policyfile.File{Rules: staged}); err != nil {
					t.Fatalf("Setup: stage %s: %v", path, err)
				}
			}
			before := map[string][]byte{userPath: readBytes(t, userPath), wsPath: readBytes(t, wsPath)}

			if rec := postProfile(t, s, profileBody{Profile: id}); rec.Code != http.StatusOK {
				t.Fatalf("selecting %q: status = %d, body %s", id, rec.Code, rec.Body)
			}
			for path, want := range before {
				if got := readBytes(t, path); !bytes.Equal(got, want) {
					t.Errorf("selecting %q rewrote %s:\n%s\nwant it untouched:\n%s", id, path, got, want)
				}
			}
			var persisted string
			if !settings.FieldInto(t.Context(), s.configDir, settings.KeySecurityProfile, &persisted) || persisted != id {
				t.Errorf("selecting %q persisted %q", id, persisted)
			}
			if reload.restarts != 1 {
				t.Errorf("selecting %q: utility restarts = %d, want 1", id, reload.restarts)
			}
			if !slices.ContainsFunc(eng.events, func(e marotte.ServerEvent) bool {
				return e.Type == marotte.EventPermissionsChanged
			}) {
				t.Errorf("selecting %q broadcast no permissions_changed", id)
			}
		})
		t.Run(id+"/absent files stay absent", func(t *testing.T) {
			s, _, _, userPath, wsPath := profileFixture(t, nil)
			if rec := postProfile(t, s, profileBody{Profile: id}); rec.Code != http.StatusOK {
				t.Fatalf("selecting %q: status = %d, body %s", id, rec.Code, rec.Body)
			}
			for _, path := range []string{userPath, wsPath} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("selecting %q: os.Stat(%s) = %v, want no file created", id, path, err)
				}
			}
		})
	}
	t.Run("an unparseable user file is not read", func(t *testing.T) {
		s, _, _, userPath, _ := profileFixture(t, nil)
		malformed := []byte("rules: [ this is not a rule list\n")
		if err := os.MkdirAll(filepath.Dir(userPath), 0o700); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		if err := os.WriteFile(userPath, malformed, 0o600); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		if rec := postProfile(t, s, profileBody{Profile: policyfile.ProfileTrusted}); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body %s", rec.Code, rec.Body)
		}
		if got := readBytes(t, userPath); !bytes.Equal(got, malformed) {
			t.Errorf("the malformed file became %q, want it untouched", got)
		}
	})
}

// TestPolicyProfile_SeedMaterialisesTheProfileInForce pins Customize: preset rules from the
// live view, the user's rules beside them, nothing in the workspace file.
func TestPolicyProfile_SeedMaterialisesTheProfileInForce(t *testing.T) {
	live := []marotte.PolicyRule{
		seedRule("fs_read", "read-workspace"),
		seedRule("shell", "dev-shell"),
		// Neither is the profile's: a session consent and a baseline scope.
		{Capability: "mcp", Effect: "allow", Scope: "session", Source: "consent"},
		{Capability: "fs_write", Effect: "ask", Scope: "kiro", Source: "kiro-scope"},
	}
	s, _, reload, userPath, wsPath := profileFixture(t, live)
	if err := policyfile.Save(t.Context(), userPath, &policyfile.File{Rules: []policyfile.Rule{
		{Capability: "power", Effect: policyfile.EffectAsk},
	}}); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	if rec := postProfile(t, s, profileBody{Profile: policyfile.ProfileCustom, Seed: true}); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}

	if got := ruleCapabilities(loadRules(t, userPath)); !slices.Equal(got, []string{"fs_read", "power", "shell"}) {
		t.Errorf("Customize left %v in the user file, want the preset rules beside the user's own (fs_read, power, shell)", got)
	}
	if _, err := os.Stat(wsPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("os.Stat(%s) = %v, want no workspace file", wsPath, err)
	}
	var persisted string
	if !settings.FieldInto(t.Context(), s.configDir, settings.KeySecurityProfile, &persisted) ||
		persisted != policyfile.ProfileCustom {
		t.Errorf("persisted %q, want %q", persisted, policyfile.ProfileCustom)
	}
	if reload.restarts != 1 {
		t.Errorf("utility restarts = %d, want 1", reload.restarts)
	}
}

// blockConfigDir points the config dir inside a regular file so persistProfile's mkdir fails
// (uid-independent, unlike a 0500 dir).
func blockConfigDir(t *testing.T, s *Server) {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("Setup: stage the blocker: %v", err)
	}
	s.configDir = filepath.Join(blocker, "config")
}

// TestPolicyProfile_SeedPersistFailureRestoresTheUserFile pins that a failed persist takes
// the copied preset rules back out.
func TestPolicyProfile_SeedPersistFailureRestoresTheUserFile(t *testing.T) {
	s, _, reload, userPath, _ := profileFixture(t, []marotte.PolicyRule{seedRule("all", "allow-all")})
	if err := policyfile.Save(t.Context(), userPath, &policyfile.File{Rules: []policyfile.Rule{
		{Capability: "shell", Effect: policyfile.EffectAllow},
	}}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	blockConfigDir(t, s)

	rec := postProfile(t, s, profileBody{Profile: policyfile.ProfileCustom, Seed: true})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body %s", rec.Code, rec.Body)
	}
	if got := ruleCapabilities(loadRules(t, userPath)); !slices.Equal(got, []string{"shell"}) {
		t.Errorf("the user file holds %v after a failed Customize, want only the staged shell rule", got)
	}
	if reload.restarts != 0 {
		t.Errorf("utility restarts = %d; a selection that failed must not recycle a session", reload.restarts)
	}
}

// TestPolicyProfile_SeedPersistFailureLeavesAnAbsentUserFileAbsent: restoring means
// the state Customize found, so a file that did not exist is removed again rather
// than left behind empty.
func TestPolicyProfile_SeedPersistFailureLeavesAnAbsentUserFileAbsent(t *testing.T) {
	s, _, _, userPath, _ := profileFixture(t, []marotte.PolicyRule{seedRule("all", "allow-all")})
	blockConfigDir(t, s)

	rec := postProfile(t, s, profileBody{Profile: policyfile.ProfileCustom, Seed: true})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(userPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("os.Stat(%s) = %v after a failed Customize, want the file absent as before", userPath, err)
	}
}

// cancelOnceCopied reads as cancelled from the moment the user file holds a copied
// `all` rule: a client that walks away right after Customize's first write lands.
type cancelOnceCopied struct {
	context.Context
	done chan struct{}
	path string
	once sync.Once
}

func (c *cancelOnceCopied) cancelled() bool {
	f, err := policyfile.Load(c.path)
	if err != nil {
		return false
	}
	for i := range f.Rules {
		if f.Rules[i].Capability == "all" {
			c.once.Do(func() { close(c.done) })
			return true
		}
	}
	return false
}

func (c *cancelOnceCopied) Done() <-chan struct{} {
	c.cancelled()
	return c.done
}

func (c *cancelOnceCopied) Err() error {
	if c.cancelled() {
		return context.Canceled
	}
	return nil
}

// TestPolicyProfile_SeedSurvivesADisconnectAfterTheFirstWrite: Customize's two files
// must agree whenever the client leaves. Both written, or the user file as it was.
func TestPolicyProfile_SeedSurvivesADisconnectAfterTheFirstWrite(t *testing.T) {
	s, _, _, userPath, _ := profileFixture(t, []marotte.PolicyRule{seedRule("all", "allow-all")})
	if err := policyfile.Save(t.Context(), userPath, &policyfile.File{Rules: []policyfile.Rule{
		{Capability: "shell", Effect: policyfile.EffectAllow},
	}}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	b, _ := json.Marshal(profileBody{Profile: policyfile.ProfileCustom, Seed: true})
	ctx := &cancelOnceCopied{Context: t.Context(), done: make(chan struct{}), path: userPath}
	req := httptest.NewRequest(http.MethodPost, "/api/permissions/profile", bytes.NewReader(b)).WithContext(ctx)
	rec := httptest.NewRecorder()
	s.handlePolicyProfile(rec, req)

	profile := s.activeProfile(t.Context())
	rules := ruleCapabilities(loadRules(t, userPath))
	both := profile == policyfile.ProfileCustom && slices.Equal(rules, []string{"all", "shell"})
	neither := profile != policyfile.ProfileCustom && slices.Equal(rules, []string{"shell"})
	if !both && !neither {
		t.Errorf("after a disconnect the profile is %q and the user file holds %v (status %d), "+
			"want custom with [all shell] or the old profile with [shell]", profile, rules, rec.Code)
	}
}

// TestPolicyProfile_NamedPersistFailureAnswers500: a named selection has nothing on
// disk to compensate, so a failed persist is a 500 that recycles nothing.
func TestPolicyProfile_NamedPersistFailureAnswers500(t *testing.T) {
	s, _, reload, _, _ := profileFixture(t, nil)
	blockConfigDir(t, s)
	if rec := postProfile(t, s, profileBody{Profile: policyfile.ProfileTrusted}); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body %s", rec.Code, rec.Body)
	}
	if reload.restarts != 0 {
		t.Errorf("utility restarts = %d; a selection that failed must not recycle a session", reload.restarts)
	}
}

// TestPolicyProfile_SeedIntoAFullUserFileIsTheCallersProblem pins 400 and no write for a user
// file at the rule cap (the staged comment makes a rewrite visible).
func TestPolicyProfile_SeedIntoAFullUserFileIsTheCallersProblem(t *testing.T) {
	s, _, reload, userPath, _ := profileFixture(t, []marotte.PolicyRule{seedRule("fs_read", "read-workspace")})
	full := make([]policyfile.Rule, 0, 512)
	for i := range 512 {
		full = append(full, policyfile.Rule{Capability: "cap-" + strconv.Itoa(i), Effect: policyfile.EffectAsk})
	}
	if err := policyfile.Save(t.Context(), userPath, &policyfile.File{Rules: full}); err != nil {
		t.Fatalf("Setup: stage a full user file: %v", err)
	}
	staged := append([]byte("# hand-edited; a comment Save cannot reproduce\n"), readBytes(t, userPath)...)
	if err := os.WriteFile(userPath, staged, 0o600); err != nil {
		t.Fatalf("Setup: stage the commented file: %v", err)
	}

	rec := postProfile(t, s, profileBody{Profile: policyfile.ProfileCustom, Seed: true})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a file at the rule cap, body %s", rec.Code, rec.Body)
	}
	if got := readBytes(t, userPath); !bytes.Equal(got, staged) {
		t.Errorf("the user file was rewritten after a refusal (%d bytes, comment kept: %t), want it untouched",
			len(got), bytes.Contains(got, []byte("# hand-edited")))
	}
	if reload.restarts != 0 {
		t.Errorf("utility restarts = %d; a refused selection must not recycle a session", reload.restarts)
	}
}

// TestPolicyProfile_SeedRefusesAnUnparseableUserFile: Customize must read the user
// file to add to it, so a hand-edit marotte cannot parse is refused with 409 and left
// on disk for the user to fix.
func TestPolicyProfile_SeedRefusesAnUnparseableUserFile(t *testing.T) {
	s, _, reload, userPath, _ := profileFixture(t, []marotte.PolicyRule{seedRule("fs_read", "read-workspace")})
	malformed := []byte("rules: [ this is not a rule list\n")
	if err := os.MkdirAll(filepath.Dir(userPath), 0o700); err != nil {
		t.Fatalf("Setup: stage the directory: %v", err)
	}
	if err := os.WriteFile(userPath, malformed, 0o600); err != nil {
		t.Fatalf("Setup: stage the malformed file: %v", err)
	}

	rec := postProfile(t, s, profileBody{Profile: policyfile.ProfileCustom, Seed: true})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body %s", rec.Code, rec.Body)
	}
	if got := readBytes(t, userPath); !bytes.Equal(got, malformed) {
		t.Errorf("the unparseable file was rewritten as %q, want it left byte-for-byte alone", got)
	}
	var persisted string
	if settings.FieldInto(t.Context(), s.configDir, settings.KeySecurityProfile, &persisted) && persisted != "" {
		t.Errorf("persisted %q on a refused selection", persisted)
	}
	if reload.restarts != 0 {
		t.Errorf("utility restarts = %d; a refused selection recycled a session", reload.restarts)
	}
}

// TestPolicyProfile_SeedFailsClosed pins that a Customize unable to read the profile leaves
// everything alone.
func TestPolicyProfile_SeedFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		live []marotte.PolicyRule
	}{
		// Nothing preset-sourced is indistinguishable from a session not yet started.
		{"no preset rules in force", []marotte.PolicyRule{
			{Capability: "fs_write", Effect: "ask", Scope: "kiro", Source: "kiro-scope"},
		}},
		{"no live policy at all", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, reload, userPath, _ := profileFixture(t, tc.live)
			staged := []policyfile.Rule{{Capability: "shell", Effect: "allow"}}
			if err := policyfile.Save(t.Context(), userPath, &policyfile.File{Rules: staged}); err != nil {
				t.Fatalf("stage: %v", err)
			}

			rec := postProfile(t, s, profileBody{Profile: policyfile.ProfileCustom, Seed: true})
			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("status = %d, want 503", rec.Code)
			}
			if got := loadRules(t, userPath); len(got) != 1 || got[0].Capability != "shell" {
				t.Errorf("the staged policy was disturbed: %v", got)
			}
			var persisted string
			if settings.FieldInto(t.Context(), s.configDir, settings.KeySecurityProfile, &persisted) && persisted != "" {
				t.Errorf("persisted %q on a refused switch", persisted)
			}
			if reload.restarts != 0 {
				t.Errorf("recycled a session for a switch that did not happen")
			}
		})
	}
}

// TestPolicyProfile_Refusals pins 400 for an unknown id and for seed on a named profile.
func TestPolicyProfile_Refusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		body profileBody
	}{
		{"unknown profile", profileBody{Profile: "yolo"}},
		{"empty profile", profileBody{}},
		{"seed on a named profile", profileBody{Profile: policyfile.ProfileTrusted, Seed: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, reload, userPath, _ := profileFixture(t, []marotte.PolicyRule{seedRule("fs_read", "read-workspace")})
			if rec := postProfile(t, s, tc.body); rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			if _, err := os.Stat(userPath); err == nil {
				t.Error("a refused request wrote a policy file")
			}
			if reload.restarts != 0 {
				t.Error("a refused request recycled a session")
			}
		})
	}
}

// TestPolicyProfile_PersistKeepsSiblingSettings pins that the profile write merges.
func TestPolicyProfile_PersistKeepsSiblingSettings(t *testing.T) {
	s, _, _, _, _ := profileFixture(t, nil)
	path := filepath.Join(s.configDir, settings.Filename)
	if err := os.WriteFile(path, []byte(`{"last_model":"m-keep","supervised_default":true}`), 0o600); err != nil {
		t.Fatalf("stage settings: %v", err)
	}

	if rec := postProfile(t, s, profileBody{Profile: policyfile.ProfileReadOnly}); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var model string
	if !settings.FieldInto(t.Context(), s.configDir, settings.KeyLastModel, &model) || model != "m-keep" {
		t.Errorf("last_model = %q, want it carried over", model)
	}
	var profile string
	settings.FieldInto(t.Context(), s.configDir, settings.KeySecurityProfile, &profile)
	if profile != policyfile.ProfileReadOnly {
		t.Errorf("profile = %q, want %q", profile, policyfile.ProfileReadOnly)
	}
}

// TestPolicyView_CarriesTheLadderAndTheActiveProfile: the picker renders from this,
// so a stale or reordered ladder here is a picker that offers the wrong postures.
func TestPolicyView_CarriesTheLadderAndTheActiveProfile(t *testing.T) {
	s, _, _, _, _ := profileFixture(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/permissions", nil)
	rec := httptest.NewRecorder()
	s.handlePolicyView(rec, req)

	var view marotte.PolicyView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := policyfile.Profiles()
	if len(view.Profiles) != len(want) {
		t.Fatalf("profiles = %d, want %d", len(view.Profiles), len(want))
	}
	for i := range want {
		if view.Profiles[i].ID != want[i].ID {
			t.Errorf("profile %d = %q, want %q; the ladder's order is part of the payload", i, view.Profiles[i].ID, want[i].ID)
		}
		if !slices.Equal(view.Profiles[i].Presets, want[i].Presets) {
			t.Errorf("profile %q presets = %v, want %v", want[i].ID, view.Profiles[i].Presets, want[i].Presets)
		}
	}
	// Unset resolves to the default, or the picker would open on no selection.
	if view.Profile != policyfile.DefaultProfile {
		t.Errorf("active profile = %q, want the default %q", view.Profile, policyfile.DefaultProfile)
	}
}
