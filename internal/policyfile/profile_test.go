package policyfile

import (
	"path/filepath"
	"slices"
	"testing"
)

// TestPresetIDs_MatchKAS pins every profile's preset ids against KAS's registry: KAS's
// validatePresetIds throws InvalidParamsError on an unknown id, failing the session door.
func TestPresetIDs_MatchKAS(t *testing.T) {
	want := []string{
		"allow-all",
		"approve-for-me",
		"dev-shell",
		"edit-workspace",
		"read-all",
		"read-only-shell",
		"read-workspace",
	}
	got := []string{
		PresetAllowAll, presetApproveForMe, presetDevShell, presetEditWorkspace,
		presetReadAll, presetReadOnlyShell, PresetReadWorkspace,
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("preset ids = %v, want %v (KAS 2.37.1 preset registry)", got, want)
	}
}

// TestProfiles_OnlyNameKnownPresets is the same guard applied to the ladder: a
// profile naming a preset id that is not in the registry is a profile that cannot
// start a session at all.
func TestProfiles_OnlyNameKnownPresets(t *testing.T) {
	known := []string{
		PresetAllowAll, presetApproveForMe, presetDevShell, presetEditWorkspace,
		presetReadAll, presetReadOnlyShell, PresetReadWorkspace,
	}
	for _, p := range Profiles() {
		for _, id := range p.Presets {
			if !slices.Contains(known, id) {
				t.Errorf("profile %q names unknown preset %q; session/new would fail outright", p.ID, id)
			}
		}
	}
}

// TestProfiles_AreAMonotonicLadder pins the property the picker's ORDER claims:
// each named profile's presets are a superset of the one before it, so moving down
// the list only ever widens.
//
// Yolo is the deliberate exception and the assertion says so rather than skipping
// it: `allow-all` is a single alias that subsumes every other preset's effect
// without naming any of them, so a set comparison is the wrong test there and the
// right one is that it grants the umbrella.
func TestProfiles_AreAMonotonicLadder(t *testing.T) {
	order := []string{ProfileGuarded, ProfileReadOnly, ProfileTrusted, ProfileDelegated}
	var prev []string
	for _, id := range order {
		p, ok := ProfileFor(id)
		if !ok {
			t.Fatalf("ProfileFor(%q) missing; the ladder is incomplete", id)
		}
		for _, earlier := range prev {
			if !slices.Contains(p.Presets, earlier) {
				t.Errorf("profile %q drops preset %q that a stricter profile grants; the ladder is not monotonic", id, earlier)
			}
		}
		if len(p.Presets) <= len(prev) {
			t.Errorf("profile %q grants %d presets, no more than the stricter %d; it adds nothing", id, len(p.Presets), len(prev))
		}
		prev = p.Presets
	}
	if p, _ := ProfileFor(ProfileDelegated); !slices.Contains(p.Presets, presetApproveForMe) {
		t.Errorf("delegated = %v, want approve-for-me, the preset that hands asks to the classifier", p.Presets)
	}
	yolo, ok := ProfileFor(ProfileUnrestricted)
	if !ok {
		t.Fatal("ProfileFor(unrestricted) missing")
	}
	if !slices.Contains(yolo.Presets, PresetAllowAll) {
		t.Errorf("unrestricted = %v, want the allow-all umbrella", yolo.Presets)
	}
}

// TestProfileReadOnly_SendsNoShellPreset keeps every shell command asking under Read-only.
// KAS's read-only-shell allows `git status > <path>` (a redirect is never checked against
// fs_write), `git log --output=<path>`, a redirect into ~/.kiro/agents/ past the kiro scope,
// and `git status` in a repo whose core.fsmonitor runs a program: each writes or executes.
func TestProfileReadOnly_SendsNoShellPreset(t *testing.T) {
	p, ok := ProfileFor(ProfileReadOnly)
	if !ok {
		t.Fatal("ProfileFor(read-only) missing")
	}
	for _, shell := range []string{presetReadOnlyShell, presetDevShell, PresetAllowAll} {
		if slices.Contains(p.Presets, shell) {
			t.Errorf("read-only sends %v; preset %q auto-allows shell commands that write files and run repo-configured programs", p.Presets, shell)
		}
	}
}

// TestProfileCustom_SendsNoPresets is what makes the files authoritative in Custom.
// A single preset here would silently keep a floor the editable table does not
// show, so the table would stop being the whole policy it claims to be.
func TestProfileCustom_SendsNoPresets(t *testing.T) {
	p, ok := ProfileFor(ProfileCustom)
	if !ok {
		t.Fatal("ProfileFor(custom) missing")
	}
	if len(p.Presets) != 0 {
		t.Errorf("custom sends %v; it must send nothing, or the table is not the whole policy", p.Presets)
	}
}

// TestProfileFor_ReportsAbsenceInBand keeps the two callers honest. A default
// returned for an unknown id would hand the session door a posture nobody chose;
// the settings reader wants the fallback, and it applies it itself with a log line.
func TestProfileFor_ReportsAbsenceInBand(t *testing.T) {
	if _, ok := ProfileFor("nonexistent"); ok {
		t.Error("ProfileFor reported an unknown id as found")
	}
	if p, ok := ProfileFor(DefaultProfile); !ok || p.ID != ProfileTrusted {
		t.Errorf("DefaultProfile %q does not resolve to trusted", DefaultProfile)
	}
}

// TestProfiles_ReturnsAnIsolatedCopy: the value being handed out is a security
// posture, so a caller appending to its Presets must not rewrite the profile for
// every later caller.
func TestProfiles_ReturnsAnIsolatedCopy(t *testing.T) {
	first := Profiles()
	for i := range first {
		first[i].Presets = append(first[i].Presets, "tampered")
	}
	for _, p := range Profiles() {
		if slices.Contains(p.Presets, "tampered") {
			t.Fatalf("profile %q kept a caller's append; Profiles() shares its backing array", p.ID)
		}
	}
	if p, _ := ProfileFor(ProfileUnrestricted); slices.Contains(p.Presets, "tampered") {
		t.Error("ProfileFor shares its backing array with the package copy")
	}
}

// TestDelegatesToSafetyCheck_OnlyTheDelegatedProfile: only approve-for-me hands asks to Kiro's safety check, so only
// Delegated reports it; Unrestricted's allow-all settles every request itself, and nil is Custom.
func TestDelegatesToSafetyCheck_OnlyTheDelegatedProfile(t *testing.T) {
	for _, p := range Profiles() {
		if got, want := DelegatesToSafetyCheck(p.Presets), p.ID == ProfileDelegated; got != want {
			t.Errorf("DelegatesToSafetyCheck(%s %v) = %v, want %v", p.ID, p.Presets, got, want)
		}
	}
	if !DelegatesToSafetyCheck([]string{"approve-for-me"}) {
		t.Error("DelegatesToSafetyCheck missed KAS's approve-for-me preset id")
	}
}

// TestHoldsGrant_NamesWhatAFileGrantsOnItsOwn: the chat pill warns that a profile does not take away
// what the files grant, so a grant missed here is a chat that looks tighter than it is.
func TestHoldsGrant_NamesWhatAFileGrantsOnItsOwn(t *testing.T) {
	cases := []struct {
		name, file, body string
		want             bool
	}{
		{"no file", "", "", false},
		{"blank file", filename, "\n", false},
		{
			"only deny and ask rules", filename,
			"rules:\n  - capability: shell\n    effect: deny\n    match: [\"rm *\"]\n" +
				"  - capability: fs_write\n    effect: ask\n", false,
		},
		{"an allow rule", filename, "rules:\n  - capability: fs_write\n    effect: allow\n", true},
		{"an evaluate rule", filename, "rules:\n  - capability: shell\n    effect: evaluate\n", true},
		{"a preset reference", filename, "policies:\n  - dev-shell\nrules: []\n", true},
		{"an empty preset list", filename, "policies: []\n", false},
		{
			"the json sibling KAS reads while the yaml is missing", jsonFilename,
			`{"policies":["read-all"],"rules":[]}`, true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.file != "" {
				writeFixture(t, filepath.Join(dir, tc.file), tc.body)
			}
			got, err := HoldsGrant(t.Context(), filepath.Join(dir, filename))
			if err != nil {
				t.Fatalf("HoldsGrant: %v", err)
			}
			if got != tc.want {
				t.Errorf("HoldsGrant = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHoldsGrant_AnUnparseableFileIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), filename)
	writeFixture(t, path, "rules: [\n")
	if _, err := HoldsGrant(t.Context(), path); err == nil {
		t.Error("HoldsGrant read an unparseable file as granting nothing; the caller cannot tell it from an empty one")
	}
}
