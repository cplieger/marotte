package trigger

import (
	"encoding/json"
	"os"
	"testing"
)

// prListCostFixture is testdata/prlist-cost.json, which TestProbePRListCost writes.
type prListCostFixture struct {
	First int `json:"first"`
	Repos []struct {
		Repo string `json:"repo"`
		Rows int    `json:"rows"`
		Cost int    `json:"cost"`
	} `json:"repos"`
}

func readPRListCostFixture(t *testing.T) prListCostFixture {
	t.Helper()
	raw, err := os.ReadFile(prListCostPath)
	if err != nil {
		t.Fatalf("%v (record it: %s)", err, prListCostRegenerate)
	}
	var f prListCostFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("%s: %v", prListCostPath, err)
	}
	return f
}

func TestPRListPagePointsMatchesFixture(t *testing.T) {
	f := readPRListCostFixture(t)
	if f.First != prListProbeFirst {
		t.Fatalf("the fixture priced a page of %d rows, want %d", f.First, prListProbeFirst)
	}
	seen := map[string]bool{}
	for _, r := range f.Repos {
		seen[r.Repo] = true
		if r.Cost != prListPagePoints {
			t.Errorf("%s: a PRList page cost %d points, prListPagePoints is %d (re-record: %s)",
				r.Repo, r.Cost, prListPagePoints, prListCostRegenerate)
		}
	}
	for _, repo := range prListProbeRepos {
		if !seen[repo] {
			t.Errorf("the fixture records no page of %s", repo)
		}
	}
}
