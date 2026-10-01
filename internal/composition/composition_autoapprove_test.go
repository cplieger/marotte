package composition

import (
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/policyfile"
	"github.com/cplieger/marotte/internal/settings"
)

// TestHonoursAutoApproveFor_ResolvesEveryRung drives the WIRE, not either end of
// it: a real config.json on disk in, the render's own bool out. Both ends are
// already pinned — policyfile tables HonourAutoApprove per rung and the renderer
// is tested per bool — and both are pinned against injected values, so inverting
// this one line left all four packages green while `guarded` honoured an
// auto-approve list and `trusted` suspended one.
//
// It iterates the ladder rather than naming rungs, so a rung added later is
// covered here too, and a fresh dir per row keeps the settings read off the
// previous row's cached parse.
func TestHonoursAutoApproveFor_ResolvesEveryRung(t *testing.T) {
	ctx := t.Context()
	for _, p := range policyfile.Profiles() {
		t.Run(p.ID, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, filepath.Join(dir, settings.Filename),
				`{"`+settings.KeySecurityProfile+`":"`+p.ID+`"}`)

			if got := honoursAutoApproveFor(ctx, dir); got != p.HonourAutoApprove {
				t.Errorf("honoursAutoApproveFor(%q) = %v, want %v: the rung in force and what reaches KAS's mcp.json must agree",
					p.ID, got, p.HonourAutoApprove)
			}
		})
	}
}

// TestHonoursAutoApproveFor_AnUnsetKeyTakesTheDefaultRung is the row the ladder
// cannot supply: an instance that has never opened the picker has no
// security_profile at all, and the answer has to be the default rung's rather
// than the zero value's. It lands on the same false today, which is why the
// assertion reads DefaultProfile's posture instead of a literal.
func TestHonoursAutoApproveFor_AnUnsetKeyTakesTheDefaultRung(t *testing.T) {
	def, ok := policyfile.ProfileFor(policyfile.DefaultProfile)
	if !ok {
		t.Fatalf("policyfile.DefaultProfile = %q names no rung", policyfile.DefaultProfile)
	}

	dir := t.TempDir()
	writeConfig(t, filepath.Join(dir, settings.Filename), `{}`)

	if got := honoursAutoApproveFor(t.Context(), dir); got != def.HonourAutoApprove {
		t.Errorf("honoursAutoApproveFor with no %s = %v, want %v (%s's posture)",
			settings.KeySecurityProfile, got, def.HonourAutoApprove, policyfile.DefaultProfile)
	}
}
