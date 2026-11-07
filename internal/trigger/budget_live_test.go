package trigger

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/forgeapi/v2/github"
)

var prListCostProbe = flag.Bool("prlist-cost-probe", false,
	"run TestProbePRListCost: read one PRList page per probed repository with MAROTTE_LIVE_GITHUB_TOKEN and rewrite "+prListCostPath)

const (
	prListCostPath       = "testdata/prlist-cost.json"
	prListCostRegenerate = "MAROTTE_LIVE_GITHUB_TOKEN=<token> go test ./internal/trigger/ -run TestProbePRListCost -prlist-cost-probe"
	prListProbeFirst     = 50
)

var prListProbeRepos = []string{"cplieger/animap", "cplieger/marotte"}

type liveToken string

func (t liveToken) Token(context.Context) (string, error) { return string(t), nil }
func (liveToken) Kind() forgeapi.CredKind                 { return forgeapi.CredKindStaticPAT }
func (liveToken) State() forgeapi.CredState               { return forgeapi.CredValid }

// TestProbePRListCost records the graphql points GitHub charges for one open-PR page of forgeapi's PRList document,
// as the document's own rateLimit.cost reports it through BudgetState().LastCost. It reads GitHub with a real token,
// so it needs -prlist-cost-probe and MAROTTE_LIVE_GITHUB_TOKEN.
func TestProbePRListCost(t *testing.T) {
	if !*prListCostProbe {
		t.Skip("-prlist-cost-probe not set")
	}
	token := os.Getenv("MAROTTE_LIVE_GITHUB_TOKEN")
	if token == "" {
		t.Skip("MAROTTE_LIVE_GITHUB_TOKEN is empty")
	}
	c, err := github.New(forgeapi.Connection{WebBaseURL: "https://github.com"},
		forgeapi.WithCredentialSource(liveToken(token)), forgeapi.WithListPages(1), forgeapi.WithReadCache(0))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	type repoCost struct {
		Repo string `json:"repo"`
		Rows int    `json:"rows"`
		Cost int    `json:"cost"`
	}
	out := struct {
		Meta  map[string]string `json:"_meta"`
		First int               `json:"first"`
		Repos []repoCost        `json:"repos"`
	}{
		Meta: map[string]string{
			"date":       time.Now().UTC().Format(time.DateOnly),
			"recorded":   "live",
			"regenerate": prListCostRegenerate,
		},
		First: prListProbeFirst,
	}
	for _, sel := range prListProbeRepos {
		ref := forgeapi.RepoRef{Family: forgeapi.FamilyGitHub, Selector: sel}
		ref.ID = ref.Encode()
		page, err := c.ListPRs(t.Context(), ref, forgeapi.WithState(forgeapi.ListStateOpen), forgeapi.WithPageBound(prListProbeFirst))
		if err != nil {
			t.Fatalf("%s: %v", sel, err)
		}
		out.Repos = append(out.Repos, repoCost{Repo: sel, Rows: len(page.Items), Cost: c.BudgetState().LastCost})
	}
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prListCostPath, append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
