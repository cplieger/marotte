package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/policyfile"
	"github.com/cplieger/marotte/internal/securityprofile"
	"github.com/cplieger/webhttp/v3"
)

// profileBody is the request. Seed is valid only when switching TO custom, and refused
// otherwise rather than ignored.
type profileBody struct {
	Profile string `json:"profile"`
	Seed    bool   `json:"seed"`
}

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
	store := s.profiles()
	if body.Seed {
		if err := store.Customize(r.Context()); err != nil {
			s.writeCustomizeError(r.Context(), w, err)
			return
		}
	} else if err := store.Select(r.Context(), profile.ID); err != nil {
		httpreply.InternalError(w, err)
		return
	}
	slog.Info("security profile selected", "profile", profile.ID,
		"presets", profile.Presets, "seeded", body.Seed)
	webhttp.Ok(w)
	if s.policyReload != nil {
		s.policyReload.SecurityProfileChanged(durable.Context(r.Context()))
	}
}

func (s *Server) writeCustomizeError(ctx context.Context, w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, securityprofile.ErrNoLivePolicy):
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable,
			httpreply.ErrorJSON("the current profile's rules could not be read to copy them, so nothing was changed"))
	case errors.Is(err, securityprofile.ErrUserFileUnreadable):
		webhttp.WriteJSONStatus(w, http.StatusConflict, httpreply.ErrorJSON(
			"the user permissions file could not be read, so nothing was changed. Fix the file by hand and try again",
		))
	case errors.Is(err, securityprofile.ErrUserFileFull):
		httpreply.BadRequest(w,
			"the user permissions file is at its rule limit. Remove a rule from the table and try again")
	case errors.Is(err, securityprofile.ErrRestoreFailed):
		s.agent.Broadcast(durable.Context(ctx), marotte.NewEvent(marotte.EventPermissionsChanged, "",
			marotte.PermissionsChangedPayload{Status: "failed"}))
		webhttp.WriteJSONStatus(w, http.StatusInternalServerError, httpreply.ErrorJSON(
			"the profile could not be applied and the copied rules could not be taken back out. "+
				"Check permissions.yaml under ~/.kiro/settings",
		))
	default:
		httpreply.InternalError(w, err)
	}
}

func (s *Server) profiles() *securityprofile.Store {
	store := &securityprofile.Store{ConfigDir: s.configDir, WorkDir: s.workDir}
	if s.policy != nil {
		store.List = s.policy.PolicyList
	}
	return store
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

func (s *Server) activeProfile(ctx context.Context) string {
	return s.profiles().Active(ctx)
}
