package policyfile

import (
	"context"
	"fmt"
	"slices"
)

// Named security profiles for the Settings default and each chat's shield pill. A
// named profile is KAS policy presets alone, injected at SESSION scope as
// `source: preset:<id>`, so it writes nothing to disk and cannot drift from
// upstream's review of which commands are safe. Custom has no presets: its policy
// files are the whole policy. KAS seeds a session's presets when the session is
// built and has no live setter for them.

// Preset ids KAS's registry accepts. Pinned because an unknown id is not a
// degraded grant but a failed session: validatePresetIds throws InvalidParamsError
// and `session/new` never completes, so an upstream rename would take every chat
// down at its first prompt rather than quietly granting less.
const (
	PresetReadWorkspace = "read-workspace"
	presetReadOnlyShell = "read-only-shell"
	presetReadAll       = "read-all"
	presetEditWorkspace = "edit-workspace"
	presetDevShell      = "dev-shell"
	presetApproveForMe  = "approve-for-me"
	PresetAllowAll      = "allow-all"
)

// Profile ids: marotte's own persisted vocabulary, a POSTURE rather than KAS's preset rule bundles.
// The ladder mirrors KiroCrew's four levels with adjective names: guarded, read-only, trusted,
// unrestricted, plus delegated, which hands Trusted's asks to KAS's trust classifier. The loosest
// level is not time-boxed, by decision: it stays on until changed.
const (
	ProfileGuarded      = "guarded"
	ProfileReadOnly     = "read-only"
	ProfileTrusted      = "trusted"
	ProfileDelegated    = "delegated"
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
		// Reading THIS workspace is free and everything else asks.
		ID:      ProfileGuarded,
		Presets: []string{PresetReadWorkspace},
	},
	{
		// read-all grants fs_read OUTSIDE the workspace (an SSH key, a sibling project) and is
		// bundled with web access, so the picker's description says where it reaches. No shell
		// preset: read-only-shell's commands can write through a redirect or git --output and
		// run a repository's core.fsmonitor, so every command asks.
		ID:      ProfileReadOnly,
		Presets: []string{PresetReadWorkspace, presetReadAll},
	},
	{
		// Edit inside the workspace and run ordinary development commands. The
		// destructive and irreversible ones still ask, which is upstream's line
		// rather than ours.
		ID: ProfileTrusted,
		Presets: []string{
			PresetReadWorkspace, presetReadOnlyShell, presetReadAll,
			presetEditWorkspace, presetDevShell,
		},
	},
	{
		// Trusted plus approve-for-me (evaluate all): KAS's trust classifier answers the requests no rule
		// settles; an ask rule (the kiro scope's .git/** and .kiro/agents/** writes) still asks. Allow beats
		// evaluate within one evaluation, so Trusted's grants never become classifier calls. The engine's KIRO_TRUST_CLASSIFIER_MODE=enforce row is what lets the preset load.
		ID: ProfileDelegated,
		Presets: []string{
			PresetReadWorkspace, presetReadOnlyShell, presetReadAll,
			presetEditWorkspace, presetDevShell, presetApproveForMe,
		},
	},
	{
		// capability: all. The kiro scope still denies file-tool writes under ~/.kiro/settings
		// and asks before file-tool writes to .git/**, .kiro/agents/**, .kiro/hooks/** and
		// .vscode/**, because deny and ask both beat allow. KAS never checks a shell redirect's
		// target against that scope, so a shell command writes any of them unprompted.
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
// Absence is reported in band rather than through a default, so each caller picks
// its answer.
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

// DelegatesToSafetyCheck reports whether a session opened with presets hands the requests no rule settles to
// Kiro's safety check (KAS's trust classifier) instead of asking a person.
func DelegatesToSafetyCheck(presets []string) bool {
	return slices.Contains(presets, presetApproveForMe)
}

// RulesGrant reports whether the file KAS reads for path holds an allow or evaluate rule, which a session's
// presets cannot take away. Nothing to read grants nothing; a file Load refuses is an error.
func RulesGrant(ctx context.Context, path string) (bool, error) {
	data, name, err := readSource(ctx, path)
	if err != nil {
		return false, err
	}
	_, f, err := parse(data, name)
	if err != nil {
		return false, err
	}
	for i := range f.Rules {
		if e := f.Rules[i].Effect; e == EffectAllow || e == EffectEvaluate {
			return true, nil
		}
	}
	return false, nil
}

// presetStatusApplies is the status KAS gives a referenced preset a session loads; needs-classifier and unknown,
// and any status KAS adds later, load nothing.
const presetStatusApplies = "applies"

// PresetStore is one row of KAS's _kiro/permissions/presets/stores reply: a permissions file a session over the
// asked roots reads (the user file, then one per workspace root), at the path KAS resolved.
type PresetStore struct {
	Scope         string          `json:"scope"`
	WorkspaceRoot string          `json:"workspaceRoot,omitempty"`
	File          string          `json:"file"`
	Read          PresetStoreRead `json:"read"`
}

// PresetStoreRead is KAS's loader verdict on a store's file: OK false when a fatal error made KAS skip the whole
// file, else the presets its `policies` list names.
type PresetStoreRead struct {
	Error   string            `json:"error,omitempty"`
	Presets []PresetReference `json:"presets,omitempty"`
	OK      bool              `json:"ok"`
}

// PresetReference is one id a file's `policies` list names, with KAS's status for it.
type PresetReference struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// Grants reports whether the store's file grants anything a session's presets cannot take away: a preset KAS
// applies from it, or an allow or evaluate rule (RulesGrant). A file KAS skipped grants nothing; a row naming no
// file, or a file RulesGrant cannot parse, is an error.
func (s PresetStore) Grants(ctx context.Context) (bool, error) {
	if !s.Read.OK {
		return false, nil
	}
	for _, p := range s.Read.Presets {
		if p.Status == presetStatusApplies {
			return true, nil
		}
	}
	if s.File == "" {
		return false, fmt.Errorf("the %s preset store names no file", s.Scope)
	}
	return RulesGrant(ctx, s.File)
}

// DefaultProfile is what a chat's unset or unrecognised setting resolves to, so a stored choice
// is never moved by it. Never Custom, which would remove the fs_read floor from an
// instance that never chose to.
const DefaultProfile = ProfileDelegated

// DefaultRunProfile is what a parentless run's host opens on while no known profile is saved. It
// never delegates: scheduled and triggered runs are parentless, and handing an unattended run's
// unsettled requests to Kiro's safety check is consent that must be chosen rather than inherited
// (settings.KeyScheduledAutoApprove).
const DefaultRunProfile = ProfileTrusted
