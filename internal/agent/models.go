package agent

import (
	"context"
	"strings"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/modeltext"
)

const modelAuto = marotte.ModelAuto

// cheapestModel returns the lowest-RateMultiplier eligible model id, or "". "auto" and every
// modelExcluded model are skipped; with no rates the first eligible entry wins.
func cheapestModel(_ context.Context, catalog []marotte.SessionModel) string {
	var bestID string
	var bestRate float64
	for _, m := range catalog {
		if m.ID == "" || m.ID == modelAuto {
			continue
		}
		if modelExcluded(m.Name) || modelExcluded(m.Description) {
			continue
		}
		switch {
		case bestID == "":
			bestID, bestRate = m.ID, m.RateMultiplier
		case m.RateMultiplier > 0 && (bestRate == 0 || m.RateMultiplier < bestRate):
			bestID, bestRate = m.ID, m.RateMultiplier
		}
	}
	return bestID
}

// excludedTags disqualify a model from ambient tasks: the hidden set plus [internal]/[experimental]/[eol].
var excludedTags = append(modeltext.HiddenTags(), "[internal]", "[experimental]", "[eol]")

// experimentalPrefix marks a preview labelled in prose rather than tagged.
const experimentalPrefix = "experimental preview"

func modelExcluded(text string) bool {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(text)), experimentalPrefix) {
		return true
	}
	return modeltext.HasAnyTag(text, excludedTags)
}
