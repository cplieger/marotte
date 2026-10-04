package marotte_test

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// TestPRSubject_KeysOnRepoID pins the key the poller mints and the PRs tab
// rebuilds for its own rows (push-subject.ts prIdentity): the connection id, the
// repository's canonical id and the number, in that order and with those
// separators, because the two halves compare it byte for byte.
func TestPRSubject_KeysOnRepoID(t *testing.T) {
	cases := []struct {
		name    string
		forgeID string
		repoID  string
		want    string
		number  int
	}{
		{
			name:    "GitHub",
			forgeID: "github:github.com",
			repoID:  "v1.63706c69656765722f6d61726f747465",
			number:  42,
			want:    "pr:github:github.com:v1.63706c69656765722f6d61726f747465#42",
		},
		{
			name:    "GitLabSubgroupOnAPortedHost",
			forgeID: "gitlab:gitlab.example:8443",
			repoID:  "v1.67726f75702f7375622f70726f6a656374",
			number:  7,
			want:    "pr:gitlab:gitlab.example:8443:v1.67726f75702f7375622f70726f6a656374#7",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := marotte.PRSubject(tc.forgeID, tc.repoID, tc.number)
			if got.Key != tc.want {
				t.Errorf("PRSubject(%q, %q, %d).Key = %q, want %q", tc.forgeID, tc.repoID, tc.number, got.Key, tc.want)
			}
			if got.ChatID != "" {
				t.Errorf("PRSubject(%q, %q, %d).ChatID = %q, want empty", tc.forgeID, tc.repoID, tc.number, got.ChatID)
			}
		})
	}
}
