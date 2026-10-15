package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/webhttp/v3"
)

// errDefaultsRefused is a pick the served catalog does not offer: KAS accepts any id at launch
// and fails the step at its first model call, so the refusal happens before a save or a launch.
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
	tiers := tiersIn(models, d.Model)
	switch {
	case tiers.known == tiersUnknown:
		return fmt.Errorf("%w: the model %q is not offered to this account", errDefaultsRefused, d.Model)
	case d.Effort == "":
		return nil
	case tiers.known == tiersNone:
		return fmt.Errorf("%w: the model %q offers no effort levels", errDefaultsRefused, d.Model)
	}
	// The pill's rule for a tier the model does not run, so the two cannot disagree.
	if landed := landMove(&effortInput{to: d.Model, target: tiers}, d.Effort); landed.unoffered != "" {
		return fmt.Errorf("%w: the model %q does not offer the effort level %q", errDefaultsRefused, d.Model, d.Effort)
	}
	return nil
}

// writeDefaultsErr answers a checkRunDefaults error, reporting whether err was one.
func writeDefaultsErr(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, errDefaultsRefused):
		httpreply.BadRequest(w, err.Error())
	case errors.Is(err, errDefaultsUncheckable):
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSON(err.Error()))
	default:
		return false
	}
	return true
}
