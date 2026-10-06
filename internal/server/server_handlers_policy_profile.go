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
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/policyfile"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/webhttp/v3"
)

// POST /api/permissions/profile selects a security posture: it persists the id and recycles
// the utility session, writing no policy file. Customize (seedCustom) is the one write.

// errNoLivePolicy is returned when the profile's own rules cannot be read: an empty Custom
// drops every grant and is indistinguishable from a session not yet started.
var errNoLivePolicy = errors.New("no live policy to read preset rules from")

// profileBody is the request. Seed is valid only when switching TO custom, and refused
// otherwise rather than ignored.
type profileBody struct {
	Profile string `json:"profile"`
	Seed    bool   `json:"seed"`
}

// presetRuleSource is the prefix KAS stamps on a rule resolved from a preset (`preset:<id>`).
// No RPC enumerates a preset's rules, so the live view of a session opened with them is the source.
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
	// Presets ride the session door: recycling the utility session makes GET /api/permissions
	// describe the new profile; chats pick it up at their next session start or load.
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

// presetRulesInForce reads the rules the ACTIVE profile's presets contributed to the live
// policy. Session scope AND a `preset:` source: scope alone would also catch a one-session
// consent. Rules are sanitized on the way through.
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
				"capability", logsafe.Field(r.Capability), "source", logsafe.Field(r.Source), "error", logsafe.Field(sErr.Error()))
			continue
		}
		out = append(out, clean)
	}
	if len(out) == 0 {
		// Empty is indistinguishable from "not started yet": refuse rather than write a blank slate.
		return nil, errNoLivePolicy
	}
	return out, nil
}

// seedCustom is Customize: it copies the preset rules in force into the USER file, then
// persists custom. The read comes first, and a persist failure puts the file back. It writes
// the response on every failure.
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
		slog.Warn("the user permissions file could not be read, so Customize was refused", "path", path, "error", logsafe.Field(err.Error()))
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
	// Durable from the first write: a disconnect must not strand the copied grants.
	ctx := durable.Context(r.Context())
	if err := policyfile.Save(ctx, path, f); err != nil {
		httpreply.InternalError(w, err)
		return false
	}
	if err := s.persistProfile(ctx, policyfile.ProfileCustom); err != nil {
		if rErr := restoreUserFile(ctx, path, existed, before); rErr != nil {
			slog.Error("could not restore the user permissions file after a failed Customize",
				"path", path, "error", logsafe.Field(rErr.Error()))
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

// persistProfile merges the profile id into config.json; an unreadable document refuses.
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

// activeProfile reads the profile in force with the session door's fallback, so the picker
// shows what the sessions opened with.
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
