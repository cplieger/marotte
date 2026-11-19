package agent

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/policyfile"
	"github.com/cplieger/marotte/internal/settings"
)

// capturedTurnReview is the supervised_refused capture's turn review params.
func capturedTurnReview(t *testing.T) string {
	t.Helper()
	for _, l := range loadKASCorpusLines(t, "supervised_refused") {
		f := l.frame(t)
		if l.Dir != "in" || f.Method != marotte.MethodRequestPermission {
			continue
		}
		var p struct {
			Meta struct {
				Kiro struct {
					Type string `json:"type"`
				} `json:"kiro"`
			} `json:"_meta"`
		}
		if err := json.Unmarshal(f.Params, &p); err != nil {
			t.Fatal(err)
		}
		if p.Meta.Kiro.Type == "turn_approval" {
			return string(f.Params)
		}
	}
	t.Fatal("the capture holds no turn review")
	return ""
}

// A supervised Bot turn's review is an ask like any other (Bots design §6): the floor rejects KAS's recorded review
// with the Bot turn's note by default, and with the switch on accepts it once.
func TestBotFloor_TheCapturedTurnReviewIsAnsweredLikeAnyAsk(t *testing.T) {
	t.Parallel()
	review := capturedTurnReview(t)
	for _, tc := range []struct {
		name     string
		keys     map[string]any
		option   string
		withNote bool
	}{
		{name: "switch_off", keys: map[string]any{}, option: "reject", withNote: true},
		{name: "switch_on", keys: map[string]any{settings.KeyBotAutoApprove: true}, option: "accept"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, br := floorHub(t, tc.keys)
			openedWith(t, h, policyfile.ProfileTrusted)
			openRuleTurn(t, h, 1)
			sendFloorAsk(t, h, 7101, review)
			o := floorAnswer(t, br)
			if o.Outcome.OptionID != tc.option {
				t.Errorf("optionId = %q, want %q", o.Outcome.OptionID, tc.option)
			}
			if got := rejectionReasonOf(o); (got == botTurnRejectionReason) != tc.withNote {
				t.Errorf("rejectionReason = %q, want the Bot turn's note: %v", got, tc.withNote)
			}
		})
	}
}
