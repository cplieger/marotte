package forges

// Tests for the repository affordances route: what one repository allows, read
// through the cache under the repository's canonical id, so a mutation of that
// repository evicts it with the repository's lists.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"

	"github.com/cplieger/forgeapi"
)

// affordedRepo is what o/r allows, as the library answers it.
func affordedRepo(strategies ...string) forgeapi.RepoAffordances {
	return forgeapi.RepoAffordances{
		MergeStrategies: strategies,
		HasIssues:       forgeapi.SupportYes, CanPush: forgeapi.SupportNo, MergeTrain: forgeapi.SupportUnknown,
		Ev: map[forgeapi.Capability]forgeapi.Evidence{
			forgeapi.CapCanPush: {Source: forgeapi.EvidenceProbe, Detail: "permissions"},
		},
		DefaultBranch: "main",
	}
}

func decodeAffordances(t *testing.T, rec *httptest.ResponseRecorder) RepoAffordances {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET affordances = %d %s, want 200", rec.Code, rec.Body)
	}
	var got RepoAffordances
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("affordances body %q: %v", rec.Body, err)
	}
	return got
}

// affordancesPath is the affordances route of the GitHub row's o/r.
var affordancesPath = githubRepoPath("v1.6f2f72", "affordances")

func TestAffordancesRoute_CarriesStrategiesAndCanPush(t *testing.T) {
	core := &pagedCore{aff: affordedRepo("merge", "squash")}
	mux := repoRoutes(recordManager(t, core))

	got := decodeAffordances(t, getRoute(t, mux, affordancesPath))
	want := RepoAffordances{
		MergeStrategies: []string{"merge", "squash"},
		HasIssues:       Affordance{Support: "yes", Source: "unknown"},
		CanPush:         Affordance{Support: "no", Source: "probe", Detail: "permissions"},
		MergeTrain:      Affordance{Support: "unknown", Source: "unknown"},
		DefaultBranch:   "main",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GET affordances = %+v, want %+v", got, want)
	}
	asked, _, _ := core.calls()
	wantAsked := []forgeapi.RepoRef{{ID: "v1.6f2f72", Family: forgeapi.FamilyGitHub, Selector: "o/r", DisplayPath: "o/r"}}
	if !reflect.DeepEqual(asked, wantAsked) {
		t.Errorf("the library was asked for %+v, want %+v", asked, wantAsked)
	}
}

func TestAffordancesRoute_SecondReadIsCached(t *testing.T) {
	core := &pagedCore{aff: affordedRepo("squash")}
	mux := repoRoutes(recordManager(t, core))

	first := getRoute(t, mux, affordancesPath)
	second := getRoute(t, mux, affordancesPath)
	if second.Code != http.StatusOK || second.Body.String() != first.Body.String() {
		t.Errorf("second GET affordances = %d %s, want 200 and the first answer %s", second.Code, second.Body, first.Body)
	}
	if asked, _, _ := core.calls(); len(asked) != 1 {
		t.Errorf("two reads of one repository reached the library %d times, want 1 (%+v)", len(asked), asked)
	}
}

func TestAffordancesRoute_RefreshReadsTheForge(t *testing.T) {
	core := &pagedCore{aff: affordedRepo("squash")}
	mux := repoRoutes(recordManager(t, core))
	if got := decodeAffordances(t, getRoute(t, mux, affordancesPath)); !slices.Equal(got.MergeStrategies, []string{"squash"}) {
		t.Fatalf("Setup: GET affordances strategies = %v, want [squash]", got.MergeStrategies)
	}
	core.answerAffordances(affordedRepo("rebase"), nil)

	steps := []struct {
		path string
		want []string
	}{
		{path: affordancesPath, want: []string{"squash"}},
		{path: affordancesPath + "?refresh=1", want: []string{"rebase"}},
		{path: affordancesPath, want: []string{"rebase"}},
	}
	for i, s := range steps {
		if got := decodeAffordances(t, getRoute(t, mux, s.path)); !slices.Equal(got.MergeStrategies, s.want) {
			t.Errorf("step %d GET %s strategies = %v, want %v", i, s.path, got.MergeStrategies, s.want)
		}
	}
	if asked, _, _ := core.calls(); len(asked) != 2 {
		t.Errorf("the reads reached the library %d times, want 2: only the refresh reads the forge again", len(asked))
	}
}

func TestAffordancesRoute_MutationEvictsIt(t *testing.T) {
	row := githubMergeRow()
	other := "v1.6f2f73"
	otherPath := githubRepoPath(other, "affordances")
	for _, m := range movingMutations {
		t.Run(m.op, func(t *testing.T) {
			core := &mutationCore{}
			mux := mergeMux(t, row, releasingMutationCore{mutationCore: core})
			for _, path := range []string{row.repoPath("affordances"), otherPath} {
				if rec := getRoute(t, mux, path); rec.Code != http.StatusOK {
					t.Fatalf("Setup: GET %s = %d %s", path, rec.Code, rec.Body)
				}
			}
			if rec := sendRoute(t, mux, http.MethodPost, row.repoPath(m.tail), m.body); rec.Code != http.StatusOK {
				t.Fatalf("POST %s = %d %s, want 200", m.tail, rec.Code, rec.Body)
			}
			for _, path := range []string{row.repoPath("affordances"), otherPath} {
				if rec := getRoute(t, mux, path); rec.Code != http.StatusOK {
					t.Fatalf("GET %s after %s = %d %s, want 200", path, m.op, rec.Code, rec.Body)
				}
			}
			if got := core.affordanceReads(row.repoID); got != 2 {
				t.Errorf("the mutated repository's affordances reached the library %d times across %s, want 2: "+
					"the mutation evicts them", got, m.op)
			}
			if got := core.affordanceReads(other); got != 1 {
				t.Errorf("another repository's affordances reached the library %d times across %s, want 1: "+
					"a mutation evicts its own repository only", got, m.op)
			}
		})
	}
}

// One repository has one identifier: an id that is not the canonical encoding
// of its selector is refused before the library or the cache (ADR-0100).
func TestAffordancesRoute_NonCanonicalIDIsRefused(t *testing.T) {
	core := &pagedCore{aff: affordedRepo("squash")}
	mux := repoRoutes(recordManager(t, core))

	if rec := getRoute(t, mux, affordancesPath); rec.Code != http.StatusOK {
		t.Fatalf("Setup: GET the canonical id = %d %s", rec.Code, rec.Body)
	}
	for name, seg := range map[string]string{"upper-case hex": "v1.6F2F72", "a mixed-case selector": "v1.4f2f52"} {
		rec := getRoute(t, mux, githubRepoPath(seg, "affordances"))
		if rec.Code != http.StatusBadRequest || decodeBody(t, rec)["code"] != forgeapi.CodeRepoRefInvalid {
			t.Errorf("%s: GET = %d %s, want 400 %s", name, rec.Code, rec.Body, forgeapi.CodeRepoRefInvalid)
		}
	}
	if asked, _, _ := core.calls(); len(asked) != 1 {
		t.Errorf("the library was asked %d times for one repository, want 1 (%+v)", len(asked), asked)
	}
}

func TestAffordancesRoute_AFailedReadIsNotCached(t *testing.T) {
	core := &pagedCore{affErr: &forgeapi.Error{Op: "RepoAffordances", Kind: forgeapi.KindNotFound, Message: "gone"}}
	mux := repoRoutes(recordManager(t, core))

	if rec := getRoute(t, mux, affordancesPath); rec.Code != http.StatusNotFound || decodeBody(t, rec)["kind"] != "not_found" {
		t.Fatalf("GET affordances = %d %s, want the library's refusal as 404 kind not_found", rec.Code, rec.Body)
	}
	core.answerAffordances(affordedRepo("squash"), nil)
	if got := decodeAffordances(t, getRoute(t, mux, affordancesPath)); !slices.Equal(got.MergeStrategies, []string{"squash"}) {
		t.Errorf("GET affordances after a refusal strategies = %v, want [squash]: a refusal is not cached", got.MergeStrategies)
	}
}

func TestAffordancesRoute_OnlyGETReads(t *testing.T) {
	core := &pagedCore{aff: affordedRepo("squash")}
	mux := repoRoutes(recordManager(t, core))

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := sendRoute(t, mux, method, affordancesPath, "")
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodGet {
			t.Errorf("%s affordances = %d Allow %q, want 405 Allow GET", method, rec.Code, rec.Header().Get("Allow"))
		}
	}
	if asked, _, _ := core.calls(); len(asked) != 0 {
		t.Errorf("a refused method reached the library %+v, want nothing", asked)
	}
}
