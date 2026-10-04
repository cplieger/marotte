package forges

import (
	"errors"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
)

// An owner the connection stores is one the family's ListMyPRs accepts, and a
// refusal is the library's own answer before any request (ADR-0101), so the
// rule has one home.
func TestOwnerScopes_RefusesWhatTheLibraryRefuses(t *testing.T) {
	owners := []string{"acme", "acme/team", "acme/team/sub", "", "a//b", "../x", "acme team", "acme/", strings.Repeat("a", 256)}
	for _, kind := range []Kind{KindGitHub, KindGitLab, KindGitea, KindCodeberg} {
		for _, owner := range owners {
			want := forgeapi.ValidateOwner(kind.family(), owner)
			_, err := ownerScopes(kind, "", []string{owner})
			if want == nil {
				if err != nil {
					t.Errorf("ownerScopes(%s, %q) = %v, want the owner stored", kind, owner, err)
				}
				continue
			}
			var got, w *forgeapi.Error
			errors.As(want, &w)
			if !errors.As(err, &got) || got.Code != w.Code || got.Kind != w.Kind || got.Message != w.Message {
				t.Errorf("ownerScopes(%s, %q) = %v, want ValidateOwner's refusal %v", kind, owner, err, want)
			}
		}
	}
}
