package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/policyfile"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/webhttp/v3"
)

// POST /api/permissions/profile — select the named security posture. A named profile
// is presets alone and Custom's rules are already in the files, so a selection writes
// no policy file: it persists the id, re-renders KAS's MCP config and recycles the
// utility session. Customize is the one write (seedCustom). Its own endpoint, because
// a settings PATCH doing all this would surprise.

// errNoLivePolicy is returned when the profile's own rules cannot be read, which
// is the one condition that must not degrade into a silent blank slate: an empty
// Custom drops every grant, and it is indistinguishable on the wire from a session
// that simply has not started yet.
var errNoLivePolicy = errors.New("no live policy to read preset rules from")

// profileBody is the request. Seed is only meaningful when switching TO custom,
// and the handler refuses it otherwise rather than ignoring it: a caller asking to
// seed a named profile has misunderstood which way the materialisation runs, and
// answering 200 would hide that.
type profileBody struct {
	Profile string `json:"profile"`
	Seed    bool   `json:"seed"`
}

// presetRuleSource is the prefix KAS stamps on a rule it resolved from a policy
// preset (`preset:<id>`). Materialisation keys on it because there is no RPC that
// enumerates a preset's rules: the only place a profile's own rules are readable
// is the live policy view of a session that opened with them.
const presetRuleSource = "preset:"

func (s *Server) handlePolicyProfile(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body profileBody
	if !decodeBody(w, r, &body) {
		return
	}
	profile, ok := policyfile.ProfileFor(body.Profile)
	if !ok {
		httpreply.BadRequest(w, "unknown security profile: "+body.Profile)
		return
	}
	if body.Seed && profile.ID != policyfile.ProfileCustom {
		httpreply.BadRequest(w, "seed applies only when switching to the custom profile")
		return
	}
	if body.Seed {
		if !s.seedCustom(w, r) {
			return
		}
	} else if err := s.persistProfile(r.Context(), profile.ID); err != nil {
		httpreply.InternalError(w, err)
		return
	}
	// After persistProfile, because the renderer reads the setting. durable: the
	// profile has landed, so a closed tab must not leave mcp.json on the outgoing
	// rung's autoApprove grants.
	s.renderMCPForProfile(durable.Context(r.Context()), profile.ID)
	// The presets ride the session door, so the sessions already running still
	// carry the OLD profile. Recycling the utility session is what makes GET
	// /api/permissions describe the new one; chat bridges pick it up when their
	// session next starts or loads, which is the documented limit rather than an
	// oversight (KAS exposes no way to change a live session's policy).
	if s.policyReload != nil {
		s.policyReload.RestartUtilitySession()
	}
	slog.Info("security profile selected", "profile", profile.ID,
		"presets", profile.Presets, "seeded", body.Seed)
	webhttp.Ok(w)
	s.agent.Broadcast(r.Context(), marotte.NewEvent(marotte.EventSettingsUpdated, "", marotte.SettingsUpdatedPayload{}))
	s.agent.Broadcast(r.Context(), marotte.NewEvent(marotte.EventPermissionsChanged, "",
		marotte.PermissionsChangedPayload{Status: "success"}))
}

// renderMCPForProfile re-renders KAS's MCP config so the rung's auto-approve posture
// reaches running chats. Call it AFTER the profile is persisted: the renderer reads
// the setting. A failure logs and the selection still answers 200, because the
// profile HAS changed; the old posture stands until the next MCP change or restart.
func (s *Server) renderMCPForProfile(ctx context.Context, profileID string) {
	if s.mcpRender == nil {
		return
	}
	if err := s.mcpRender.RenderKASConfig(ctx); err != nil {
		slog.Error("security profile: re-rendering the MCP config failed; the previous profile's auto-approve posture stands until the next MCP change or restart",
			"profile", profileID, "error", err)
	}
}

// presetRulesInForce reads the rules the ACTIVE profile's presets contributed to
// the live policy, as writable file rules.
//
// Session scope plus a `preset:` source is the whole filter, and both halves are
// needed: session scope alone would also catch a consent the user granted for one
// session, and the source prefix alone would be a claim about a scope KAS could
// change. Rules are sanitized on the way through, so a pattern KAS accepts but the
// file format bounds is refused here rather than written and rejected on reload.
func (s *Server) presetRulesInForce(ctx context.Context) ([]policyfile.Rule, error) {
	if s.policy == nil {
		return nil, errNoLivePolicy
	}
	live, err := s.policy.PolicyList(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]policyfile.Rule, 0, len(live))
	for i := range live {
		r := &live[i]
		if r.Scope != "session" || !strings.HasPrefix(r.Source, presetRuleSource) {
			continue
		}
		clean, sErr := policyfile.SanitizeRule(&policyfile.Rule{
			Capability: r.Capability, Effect: r.Effect,
			Match: r.Match, Exclude: r.Exclude,
		})
		if sErr != nil {
			slog.Warn("skipping a preset rule that the file format cannot hold",
				"capability", r.Capability, "source", r.Source, "error", sErr)
			continue
		}
		out = append(out, clean)
	}
	if len(out) == 0 {
		// An empty result is indistinguishable from "the session has not started
		// yet", and the two want opposite outcomes, so refuse rather than write an
		// empty file that reads as a deliberate blank slate.
		return nil, errNoLivePolicy
	}
	return out, nil
}

// seedCustom is Customize: it copies the preset rules in force into the USER file as
// Custom's starting table, then persists custom. The read comes first so a profile
// that cannot be seen is left in place rather than landing on an empty Custom, which
// drops every grant. A persist failure puts the file back, so a copied grant never
// outlives the profile it described. It writes the response on every failure.
func (s *Server) seedCustom(w http.ResponseWriter, r *http.Request) bool {
	seeded, err := s.presetRulesInForce(r.Context())
	if err != nil {
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable,
			httpreply.ErrorJSON("the current profile's rules could not be read to copy them, so nothing was changed"))
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		httpreply.InternalError(w, err)
		return false
	}
	path, err := policyfile.PathFor(policyfile.ScopeUser, policyfile.Roots{Home: home, WorkDir: s.workDir})
	if err != nil {
		httpreply.InternalError(w, err)
		return false
	}
	_, statErr := os.Stat(path)
	existed := statErr == nil
	f, err := policyfile.Load(path)
	if err != nil {
		slog.Warn("the user permissions file could not be read, so Customize was refused", "path", path, "error", err)
		webhttp.WriteJSONStatus(w, http.StatusConflict, httpreply.ErrorJSON(
			"the user permissions file could not be read, so nothing was changed. Fix the file by hand and try again",
		))
		return false
	}
	before := slices.Clone(f.Rules)
	for i := range seeded {
		if _, uErr := f.Upsert(&seeded[i]); uErr != nil {
			httpreply.BadRequest(w,
				"the user permissions file is at its rule limit. Remove a rule from the table and try again")
			return false
		}
	}
	// Durable from the first write on: a disconnect between the two files must not
	// cancel the persist or the compensation and strand the copied grants.
	ctx := durable.Context(r.Context())
	if err := policyfile.Save(ctx, path, f); err != nil {
		httpreply.InternalError(w, err)
		return false
	}
	if err := s.persistProfile(ctx, policyfile.ProfileCustom); err != nil {
		if rErr := restoreUserFile(ctx, path, existed, before); rErr != nil {
			slog.Error("could not restore the user permissions file after a failed Customize",
				"path", path, "error", rErr)
			s.agent.Broadcast(ctx, marotte.NewEvent(marotte.EventPermissionsChanged, "",
				marotte.PermissionsChangedPayload{Status: "failed"}))
			webhttp.WriteJSONStatus(w, http.StatusInternalServerError, httpreply.ErrorJSON(
				"the profile could not be applied and the copied rules could not be taken back out. "+
					"Check permissions.yaml under ~/.kiro/settings",
			))
			return false
		}
		httpreply.InternalError(w, err)
		return false
	}
	return true
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

// persistProfile writes the profile id into config.json, merging rather than
// replacing so it cannot drop a sibling preference. A document that cannot be
// read refuses the write.
func (s *Server) persistProfile(ctx context.Context, id string) error {
	raw, err := json.Marshal(id)
	if err != nil {
		return err
	}
	_, err = settings.Update(ctx, s.configDir, func(doc map[string]json.RawMessage) error {
		doc[settings.KeySecurityProfile] = raw
		return nil
	})
	return err
}

// securityProfileCatalog projects policyfile's ladder onto the wire, order intact.
func securityProfileCatalog() []marotte.SecurityProfile {
	src := policyfile.Profiles()
	out := make([]marotte.SecurityProfile, 0, len(src))
	for i := range src {
		out = append(out, marotte.SecurityProfile{ID: src[i].ID, Presets: src[i].Presets})
	}
	return out
}

// activeProfile reads the profile in force, falling back the same way the session
// door does. One rule, two readers: a picker showing a different profile from the
// one the sessions actually opened with would be the read-back lie this panel has
// already been through once.
func (s *Server) activeProfile(ctx context.Context) string {
	var id string
	if !settings.FieldInto(ctx, s.configDir, settings.KeySecurityProfile, &id) || id == "" {
		return policyfile.DefaultProfile
	}
	if _, ok := policyfile.ProfileFor(id); !ok {
		return policyfile.DefaultProfile
	}
	return id
}
