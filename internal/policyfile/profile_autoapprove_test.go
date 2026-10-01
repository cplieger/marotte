package policyfile

import "testing"

// TestProfiles_AutoApprovePerRung is the per-rung posture table for an MCP
// server's own `auto_approve` list, and it is EXHAUSTIVE over the ladder in both
// directions on purpose: a rung added later fails this test rather than silently
// inheriting whichever posture its position in the slice happens to give it.
//
// The two halves of the ladder answer differently for different reasons. Guarded
// and read-only SUSPEND, because the list is a standing per-server grant of the
// same size as a profile's own, authored in the MCP panel by a user who may never
// have opened the picker — so honouring it would put a hole in the rung the picker
// says is in force. Trusted and custom HONOUR it, the first because that rung
// already grants ordinary development commands and the second because custom means
// the user's own declarations ARE the policy. Unrestricted honours it too, and
// there the flag is not about widening at all: `all: allow` already covers the
// `mcp` capability, so the value exists so the panel does not render a suspension
// notice on the one rung that suspends nothing.
func TestProfiles_AutoApprovePerRung(t *testing.T) {
	want := map[string]bool{
		ProfileGuarded:      false,
		ProfileReadOnly:     false,
		ProfileTrusted:      true,
		ProfileUnrestricted: true,
		ProfileCustom:       true,
	}

	ladder := Profiles()
	if len(ladder) != len(want) {
		t.Fatalf("the ladder has %d rungs and this table has %d; a rung was added or removed without deciding its auto-approve posture",
			len(ladder), len(want))
	}
	for _, p := range ladder {
		w, listed := want[p.ID]
		if !listed {
			t.Errorf("rung %q is in the ladder and not in this table; decide whether it honours an MCP auto-approve list before shipping it",
				p.ID)
			continue
		}
		if p.HonourAutoApprove != w {
			t.Errorf("Profiles()[%q].HonourAutoApprove = %t, want %t",
				p.ID, p.HonourAutoApprove, w)
		}
	}
	for id := range want {
		if _, ok := ProfileFor(id); !ok {
			t.Errorf("this table names %q and the ladder does not carry it", id)
		}
	}
}

// TestHonoursAutoApprove_ResolvesEveryRung pins the exported resolver against the
// ladder it reads, so the two cannot disagree about one rung.
func TestHonoursAutoApprove_ResolvesEveryRung(t *testing.T) {
	for _, p := range Profiles() {
		if got := HonoursAutoApprove(p.ID); got != p.HonourAutoApprove {
			t.Errorf("HonoursAutoApprove(%q) = %t, want the rung's own %t",
				p.ID, got, p.HonourAutoApprove)
		}
	}
}

// TestHonoursAutoApprove_UnknownIDTakesTheDefaultRung: an unset or mistyped
// setting resolves to DefaultProfile, the same fallback the presets resolver
// applies, rather than to a bespoke false. The two readers describe one posture, so
// a typo must not put them on different answers — and stating the RESOLUTION rather
// than the answer is what keeps this true if the default rung ever moves.
func TestHonoursAutoApprove_UnknownIDTakesTheDefaultRung(t *testing.T) {
	def, ok := ProfileFor(DefaultProfile)
	if !ok {
		t.Fatalf("ProfileFor(%q) missing; DefaultProfile does not name a rung", DefaultProfile)
	}
	for _, id := range []string{"", "guarded-ish", "GUARDED", "unrestricted "} {
		if got := HonoursAutoApprove(id); got != def.HonourAutoApprove {
			t.Errorf("HonoursAutoApprove(%q) = %t, want the %q rung's %t",
				id, got, DefaultProfile, def.HonourAutoApprove)
		}
	}
}
