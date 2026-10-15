package git

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
)

func branchCommits(t *testing.T, workDir string, q url.Values) (int, []string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/git/branch-commits?"+q.Encode(), nil)
	rec := httptest.NewRecorder()
	NewHandler(workDir).handleBranchCommits(rec, req)
	if rec.Code != http.StatusOK {
		return rec.Code, nil
	}
	var resp struct {
		Subjects []string `json:"subjects"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal %q: %v", rec.Body.String(), err)
	}
	if resp.Subjects == nil {
		t.Fatalf("body %q: subjects is null, want a list", rec.Body.String())
	}
	return rec.Code, resp.Subjects
}

func TestHandleBranchCommits(t *testing.T) {
	work := t.TempDir()
	initFixtureRepo(t, work)
	runGit(t, work, "switch", "-q", "-c", "feature")
	writeCommit(t, work, "a.txt", "a\n", "add a")
	runGit(t, work, "switch", "-q", "-c", "side")
	writeCommit(t, work, "b.txt", "b\n", "add b")
	runGit(t, work, "switch", "-q", "feature")
	runGit(t, work, "merge", "-q", "--no-ff", "-m", "merge side", "side")
	writeCommit(t, work, "c.txt", "c\n", "add c")

	t.Run("lists the head's own commits oldest first without merges", func(t *testing.T) {
		code, got := branchCommits(t, work, url.Values{"base": {"main"}, "head": {"feature"}})
		want := []string{"add a", "add b", "add c"}
		if code != http.StatusOK || !slices.Equal(got, want) {
			t.Errorf("main..feature = %d %q, want 200 %q", code, got, want)
		}
	})

	t.Run("an empty head means the checkout", func(t *testing.T) {
		code, got := branchCommits(t, work, url.Values{"base": {"main"}})
		if code != http.StatusOK || len(got) != 3 {
			t.Errorf("main..HEAD = %d %q, want 200 and the 3 commits on feature", code, got)
		}
	})

	t.Run("a head with nothing the base lacks lists nothing", func(t *testing.T) {
		code, got := branchCommits(t, work, url.Values{"base": {"feature"}, "head": {"main"}})
		if code != http.StatusOK || len(got) != 0 {
			t.Errorf("feature..main = %d %q, want 200 []", code, got)
		}
	})

	t.Run("a ref git cannot resolve lists nothing", func(t *testing.T) {
		code, got := branchCommits(t, work, url.Values{"base": {"mai"}, "head": {"feature"}})
		if code != http.StatusOK || len(got) != 0 {
			t.Errorf("mai..feature = %d %q, want 200 []", code, got)
		}
	})

	for name, q := range map[string]url.Values{
		"no base":          {"head": {"feature"}},
		"a range for base": {"base": {"main..feature"}},
		"a flag for head":  {"base": {"main"}, "head": {"--all"}},
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			if code, _ := branchCommits(t, work, q); code != http.StatusBadRequest {
				t.Errorf("%v = %d, want 400", q, code)
			}
		})
	}
}

// The forge compares against origin's base, so a stale local base must not add the commits it
// has not caught up on.
func TestHandleBranchCommits_PrefersOriginsBase(t *testing.T) {
	work := behindRepo(t)
	runGit(t, work, "switch", "-q", "-c", "feature", "origin/main")
	writeCommit(t, work, "f.txt", "f\n", "add f")

	code, got := branchCommits(t, work, url.Values{"base": {"main"}, "head": {"feature"}})
	want := []string{"add f"}
	if code != http.StatusOK || !slices.Equal(got, want) {
		t.Errorf("main..feature behind origin = %d %q, want 200 %q (local main lacks origin's \"second commit\")", code, got, want)
	}
}
