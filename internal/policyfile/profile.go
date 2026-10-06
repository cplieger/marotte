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

// Profile ids. marotte's own vocabulary, persisted as a setting, so free to
// differ from KAS's preset names — and they do differ, because a profile is a
// POSTURE while a preset is a rule bundle.
//
// The ladder mirrors KiroCrew's four levels (its picker offers normal /
// trust_reads / trust / yolo) but deliberately not its NAMES. Each of ours is an
// adjective describing the agent's standing, which keeps one part of speech across
// the set and makes the ordering readable: guarded, read-only, trusted,
// unrestricted. "Normal" named a default rather than a behaviour, "reads" was a
// verb doing an adjective's job, and "yolo" is a joke in a security control — the
// honest word for it is what it does, which is remove every restriction marotte
// can remove.
//
// Crew time-boxes its loosest level with a duration and a one-time
// acknowledgement. Ours deliberately does NOT, by decision: it stays on until it
// is changed, matching the switch it replaces.
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
	// HonourAutoApprove says whether this rung lets an MCP server's own
	// `auto_approve` list reach the agent. TRUE on trusted, unrestricted and
	// custom.
	//
	// The list is a per-server standing grant: a tool named in it is meant to run
	// with no permission request at all. That is a widening the SAME SIZE as a
	// profile's own, authored in a different place — the MCP panel — by a user who
	// may never have opened the picker. So the restrictive rungs SUSPEND it rather
	// than honour it: a chip cannot be a hole in the rung the picker says is in
	// force, and the whole defect being fixed is a grant nobody could see.
	//
	// Trusted honours it because that rung already grants ordinary development
	// commands, and custom honours it because custom means the user's own
	// declarations ARE the policy.
	//
	// UNRESTRICTED needs no code to WIDEN it — `all: allow` already covers the
	// `mcp` capability, so rendering the list changes nothing there — and it still
	// carries the flag TRUE, because the flag is also what the panel reads to
	// decide whether to say "suspended". False on the loosest rung would render a
	// suspension notice on the one rung that suspends nothing, which is the same
	// class of untruth as the grant this fix removes.
	//
	// Nothing infers a grant from an MCP tool's own annotations. A server declares
	// its own tools' annotations, so reading one as consent would let the thing
	// being authorised author its own authorisation.
	HonourAutoApprove bool
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
		// Read any file on the machine, run read-only commands, reach the web.
		// Every write and every other command still asks.
		//
		// read-all is what separates this from guarded, and it is worth stating
		// plainly because the name could imply otherwise: it grants fs_read
		// OUTSIDE the workspace, so an SSH key or a sibling project is readable
		// without a prompt. The presets are atomic, so this cannot be split from
		// the web access bundled with it. A profile called read-only that still
		// prompted for reads would contradict itself, so the grant stays and the
		// picker's description says where it reaches.
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
		HonourAutoApprove: true,
	},
	{
		// capability: all. Not silence: the kiro scope still denies writes under
		// ~/.kiro/settings and still asks before writing .git/**, .kiro/agents/**,
		// .kiro/hooks/** and .vscode/**, because deny and ask both beat allow and
		// that scope sits above every file. The UI says so beside the option.
		ID:                ProfileUnrestricted,
		Presets:           []string{PresetAllowAll},
		HonourAutoApprove: true,
	},
	{
		ID:                ProfileCustom,
		Presets:           nil,
		HonourAutoApprove: true,
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

// HonoursAutoApprove answers whether the rung named by id lets an MCP server's
// own `auto_approve` list reach the agent. See [Profile.HonourAutoApprove] for
// which rungs do and why.
//
// An UNKNOWN or empty id resolves to [DefaultProfile] rather than to false, which
// is the same fallback [ProfileFor]'s settings-reading caller applies — the answer
// has to agree with the posture actually in force, and a typo that dropped the
// fs_read floor to guarded while reporting a bespoke auto-approve answer would be
// two readers disagreeing about one setting. It lands on false either way today,
// because guarded is the default rung; stating the resolution rather than the
// answer is what keeps that true if the default ever moves.
func HonoursAutoApprove(id string) bool {
	p, ok := ProfileFor(id)
	if !ok {
		p, _ = ProfileFor(DefaultProfile)
	}
	return p.HonourAutoApprove
}
