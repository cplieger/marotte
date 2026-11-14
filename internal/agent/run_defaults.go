package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
)

// errDefaultsRefused is a run pick the served catalog does not offer: KAS would launch it and fail
// every step inheriting it at its first model call, so the refusal happens before a save or a launch.
var errDefaultsRefused = errors.New("model pick refused")

// errDefaultsUncheckable means no model list could be read, so a pick cannot be checked.
var errDefaultsUncheckable = errors.New("the model list could not be read, so the model pick cannot be checked; try again")

// checkRunDefaults refuses a pick GET /api/config-template would not offer. The zero value is
// always taken.
func (rs *Runs) checkRunDefaults(ctx context.Context, d marotte.RunDefaults) error {
	if d.Model == "" {
		if d.Effort != "" {
			return fmt.Errorf("%w: an effort needs a model", errDefaultsRefused)
		}
		return nil
	}
	models, ok := rs.servedModels(ctx)
	if !ok {
		return errDefaultsUncheckable
	}
	if why := pickRefusal(models, d.Model, d.Effort); why != "" {
		return fmt.Errorf("%w: %s", errDefaultsRefused, why)
	}
	return nil
}

// pickRefusal is why models do not offer model run at effort ("" for the model's own default), or "" when
// they do.
func pickRefusal(models []marotte.SessionModel, model, effort string) string {
	tiers := tiersIn(models, model)
	switch {
	case tiers.known == tiersUnknown:
		return fmt.Sprintf("the model %q is not offered to this account", model)
	case effort == "":
		return ""
	case tiers.known == tiersNone:
		return fmt.Sprintf("the model %q offers no effort levels", model)
	}
	// The pill's rule for a tier the model does not run, so the two cannot disagree.
	if landed := landMove(&effortInput{to: model, target: tiers}, effort); landed.unoffered != "" {
		return fmt.Sprintf("the model %q does not offer the effort level %q", model, effort)
	}
	return ""
}

// writeDefaultsErr answers a checkRunDefaults error, reporting whether err was one.
func writeDefaultsErr(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, errDefaultsRefused):
		httpreply.BadRequest(w, err.Error())
	case errors.Is(err, errDefaultsUncheckable):
		writeTryLater(w, err.Error(), modelListUnreadableCode)
	default:
		return false
	}
	return true
}
