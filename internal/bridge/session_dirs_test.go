package bridge

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// KAS adopts additionalDirectories per call and a load that omits them drops them, so both verbs carry the
// list, and an empty list sends no key.
func TestSessionVerbs_CarryAdditionalDirectories(t *testing.T) {
	for _, method := range []string{"session/new", "session/load"} {
		t.Run(strings.ReplaceAll(method, "/", " "), func(t *testing.T) {
			opts := &marotte.SessionOpts{Model: "m", AdditionalDirectories: []string{"/data", "/srv/media"}}
			if method == "session/load" {
				opts.SessionID = "sess_resume_door"
			}
			line := captureRequest(t, method, opts)
			var req struct {
				Params struct {
					AdditionalDirectories []string `json:"additionalDirectories"`
				} `json:"params"`
			}
			if err := json.Unmarshal([]byte(line), &req); err != nil {
				t.Fatalf("decode %s: %v\n%s", method, err, line)
			}
			if want := []string{"/data", "/srv/media"}; !slices.Equal(req.Params.AdditionalDirectories, want) {
				t.Errorf("%s additionalDirectories = %v, want %v:\n%s", method, req.Params.AdditionalDirectories, want, line)
			}

			opts.AdditionalDirectories = nil
			if line := captureRequest(t, method, opts); strings.Contains(line, `"additionalDirectories"`) {
				t.Errorf("%s carried additionalDirectories with none granted:\n%s", method, line)
			}
		})
	}
}
