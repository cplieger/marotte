package forges

import (
	"context"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/marotte/internal/marotte"
)

type forkSource struct {
	upstream, source, tracked string
	check                     string
}

func (f *forkSource) Read(context.Context, bool, func(PRConnection, Scope) forgeapi.Cursor) []ConnectionRead {
	row := PR{
		RepoID: repoIDOf(f.upstream), SourceRepoID: f.source, Repo: f.upstream,
		Title: "A fix", Number: 9, Action: PRAction{Checks: f.check},
	}
	clone := CloneRepo{Dir: "loki", ForgeID: testConn.ID, RepoID: repoIDOf(f.tracked)}
	page := scopePage{Scope: authoredScope, Rows: []PR{row}}
	return []ConnectionRead{{Conn: testConn, Clones: []CloneRepo{clone}, Pages: []scopePage{page}}}
}

// A pull request opened from a clone of a fork targets the upstream, which no
// clone tracks, and its head lives in the fork the clone is of, so its flip is
// news under the pull request's own subject.
func TestPoller_ForkPRJoinsACloneOfItsSource(t *testing.T) {
	src := &forkSource{
		upstream: "grafana/loki", source: repoIDOf("cplieger/loki"), tracked: "cplieger/loki", check: checkPending,
	}
	n := &fakeNotifier{}
	p := newTestPoller(src, n, &fakeGate{push: true})
	p.sweep(t.Context())

	src.check = checkFailing
	p.sweep(t.Context())

	want := marotte.PRSubject(testConn.ID, repoIDOf("grafana/loki"), 9)
	if len(n.sent) != 1 || n.sent[0].subject != want {
		t.Errorf("a fork PR's pending -> failing flip, its source cloned, sent %+v, want one notice under %+v", n.sent, want)
	}
}

// A row whose source the read does not name (a deleted fork) and whose target
// no clone tracks joins nothing, and neither does a fork nobody cloned.
func TestPoller_RowJoinsNoCloneOnNeitherRepository(t *testing.T) {
	for name, source := range map[string]string{
		"SourceUnnamed":  "",
		"SourceUncloned": repoIDOf("someone/loki"),
	} {
		t.Run(name, func(t *testing.T) {
			src := &forkSource{upstream: "grafana/loki", source: source, tracked: "cplieger/loki", check: checkPending}
			n := &fakeNotifier{}
			p := newTestPoller(src, n, &fakeGate{push: true})
			p.sweep(t.Context())

			src.check = checkFailing
			p.sweep(t.Context())

			if len(n.sent) != 0 {
				t.Errorf("a flip on a row whose target and source no clone tracks sent %+v, want nothing", n.sent)
			}
		})
	}
}

// The wire row names the repository the head lives in by its canonical id: the
// row's own for a branch inside it, the fork's for a fork, and nothing for the
// zero reference a read leaves where it cannot name it.
func TestPRWire_SourceRepoIDIsTheHeadRepositorysCanonicalID(t *testing.T) {
	target := forgeapi.RepoRef{Family: forgeapi.FamilyGitHub, Selector: "grafana/loki", DisplayPath: "grafana/loki"}
	fork := forgeapi.RepoRef{Family: forgeapi.FamilyGitHub, Selector: "Cplieger/Loki", DisplayPath: "Cplieger/Loki"}
	for name, tc := range map[string]struct {
		source forgeapi.RepoRef
		want   string
	}{
		"SameRepository": {source: target, want: target.Encode()},
		"Fork":           {source: fork, want: repoIDOf("cplieger/loki")},
		"Unnamed":        {source: forgeapi.RepoRef{}, want: ""},
	} {
		t.Run(name, func(t *testing.T) {
			got := prWire(&forgeapi.PullRequest{Repo: target, SourceRepo: tc.source})
			if got.SourceRepoID != tc.want {
				t.Errorf("prWire(SourceRepo %+v).SourceRepoID = %q, want %q", tc.source, got.SourceRepoID, tc.want)
			}
		})
	}
}
