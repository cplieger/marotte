package policyfile

import "slices"

// Named security profiles for the Settings -> Permissions picker. A named profile is
// KAS policy presets alone, injected at SESSION scope as `source: preset:<id>`, so it
// writes nothing to disk and cannot drift from upstream's review of which commands
// are safe. Custom has no presets: its policy files are the whole policy. A preset is
// only selected, never enumerated or changed live.

// Preset ids KAS's registry accepts. Pinned because an unknown id is not a
// degraded grant but a failed session: validatePresetIds throws InvalidParamsError
// and `session/new` never completes, so an upstream rename would take every chat
// down at its first prompt rather than quietly granting less.
const (
	PresetReadWorkspace = "read-workspace"
	PresetReadOnlyShell = "read-only-shell"
	PresetReadAll       = "read-all"
	PresetEditWorkspace = "edit-workspace"
	PresetDevShell      = "dev-shell"
	PresetAllowAll      = "allow-all"
)

// Profile ids: marotte's own persisted vocabulary, a POSTURE rather than KAS's preset rule bundles.
// The ladder mirrors KiroCrew's four levels with adjective names: guarded, read-only, trusted,
// unrestricted. The loosest level is not time-boxed, by decision: it stays on until changed.
const (
	ProfileGuarded      = "guarded"
	ProfileReadOnly     = "read-only"
	ProfileTrusted      = "trusted"
	ProfileUnrestricted = "unrestricted"
	// ProfileCustom sends NO presets, so the files are the whole policy and an EMPTY
	// one makes the agent ask even to read a file. The Customize button copies the
	// presets in force into the user file; selecting Custom directly copies nothing.
	ProfileCustom = "custom"
)

// Profile is one entry in the picker.
type Profile struct {
	// ID is the persisted value.
	ID string
	// Presets are the KAS preset ids sent as _meta.kiro.policyPreset. Empty for
	// Custom, which is the difference that makes the files authoritative.
	Presets []string
}

// profiles is the ordered ladder, loosest last. Order is part of the contract:
// the picker renders it in this order, so a reader scans from cautious to
// permissive rather than alphabetically.
var profiles = []Profile{
	{
		// The baseline marotte already ships: reading THIS workspace is free and
		// everything else asks. Named rather than implicit so "no profile" is not
		// a state the picker has to render.
		ID:      ProfileGuarded,
		Presets: []string{PresetReadWorkspace},
	},
	{
		// read-all grants fs_read OUTSIDE the workspace (an SSH key, a sibling project) and is
		// bundled with web access; a read-only profile that prompted for reads would contradict
		// itself, so the picker's description says where it reaches.
		ID:      ProfileReadOnly,
		Presets: []string{PresetReadWorkspace, PresetReadOnlyShell, PresetReadAll},
	},
	{
		// Edit inside the workspace and run ordinary development commands. The
		// destructive and irreversible ones still ask, which is upstream's line
		// rather than ours.
		ID: ProfileTrusted,
		Presets: []string{
			PresetReadWorkspace, PresetReadOnlyShell, PresetReadAll,
			PresetEditWorkspace, PresetDevShell,
		},
	},
	{
		// capability: all. Not silence: the kiro scope still denies writes under
		// ~/.kiro/settings and still asks before writing .git/**, .kiro/agents/**,
		// .kiro/hooks/** and .vscode/**, because deny and ask both beat allow and
		// that scope sits above every file. The UI says so beside the option.
		ID:      ProfileUnrestricted,
		Presets: []string{PresetAllowAll},
	},
	{
		ID:      ProfileCustom,
		Presets: nil,
	},
}

// Profiles returns the ladder in picker order.
func Profiles() []Profile {
	out := make([]Profile, len(profiles))
	copy(out, profiles)
	// Presets is shared with the package copy, so a caller appending to it would
	// rewrite a security posture for every later caller.
	for i := range out {
		out[i].Presets = slices.Clone(out[i].Presets)
	}
	return out
}

// ProfileFor returns the profile with the given id, and whether it exists.
//
// Absence is reported in band rather than through a default, because the two
// callers want opposite things: the settings reader wants to fall back to Normal
// with a logged reason, and the session door wants to send nothing rather than
// guess a posture. A silent default would hand one of them the wrong answer.
func ProfileFor(id string) (Profile, bool) {
	for i := range profiles {
		if profiles[i].ID == id {
			p := profiles[i]
			p.Presets = slices.Clone(p.Presets)
			return p, true
		}
	}
	return Profile{}, false
}

// DefaultProfile is what an unset or unrecognised setting resolves to. Guarded
// rather than Custom: it reproduces the floor marotte ships today, where Custom
// would silently remove the fs_read floor from an instance that never chose to.
const DefaultProfile = ProfileGuarded
