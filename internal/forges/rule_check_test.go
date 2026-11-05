package forges

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/marotte/internal/launchrule"
)

// ruleCheckCore answers an event rule's save-time reads: affordances from aff or affErr, every list read from
// listErr, recording which list was read with which options.
type ruleCheckCore struct {
	forgeapi.Core
	affErr  error
	listErr error
	lists   []string
	opts    [][]forgeapi.ListOption
	aff     forgeapi.RepoAffordances
	mu      sync.Mutex
}

func (*ruleCheckCore) Close() {}

func (c *ruleCheckCore) RepoAffordances(context.Context, forgeapi.RepoRef) (forgeapi.RepoAffordances, error) {
	return c.aff, c.affErr
}

func (c *ruleCheckCore) listed(name string, opts []forgeapi.ListOption) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lists = append(c.lists, name)
	c.opts = append(c.opts, opts)
	return c.listErr
}

func (c *ruleCheckCore) ListIssues(_ context.Context, _ forgeapi.RepoRef, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Issue], error) {
	return forgeapi.Page[forgeapi.Issue]{}, c.listed("issues", opts)
}

func (c *ruleCheckCore) ListPRs(_ context.Context, _ forgeapi.RepoRef, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.PullRequest], error) {
	return forgeapi.Page[forgeapi.PullRequest]{}, c.listed("prs", opts)
}

func (c *ruleCheckCore) ListRuns(_ context.Context, _ forgeapi.RepoRef, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Run], error) {
	return forgeapi.Page[forgeapi.Run]{}, c.listed("runs", opts)
}

func eventSpec(kind launchrule.Kind) *launchrule.EventSpec {
	return &launchrule.EventSpec{Connection: MakeID(KindGitHub, "github.com"), Repo: "cplieger/animap", Kind: kind}
}

// refusal is CheckEventRule's answer as a *launchrule.ForgeCheckError, failing the test on any other.
func refusal(t *testing.T, err error) *launchrule.ForgeCheckError {
	t.Helper()
	fe, ok := errors.AsType[*launchrule.ForgeCheckError](err)
	if !ok {
		t.Fatalf("CheckEventRule = %v, want a *launchrule.ForgeCheckError", err)
	}
	return fe
}

func TestSaveRefusesUnreadableRepo(t *testing.T) {
	for name, affErr := range map[string]error{
		"not found":   &forgeapi.Error{Op: "RepoAffordances", Kind: forgeapi.KindNotFound, Status: 404},
		"forbidden":   &forgeapi.Error{Op: "RepoAffordances", Kind: forgeapi.KindForbidden, Status: 403},
		"not visible": &forgeapi.Error{Op: "RepoAffordances", Kind: forgeapi.KindUpstream, Code: forgeapi.CodeRepoOrPRNotVisible},
	} {
		t.Run(name, func(t *testing.T) {
			core := &ruleCheckCore{affErr: affErr}
			fe := refusal(t, recordManager(t, core).CheckEventRule(t.Context(), eventSpec(launchrule.KindRunConcluded)))
			if fe.Unavailable || fe.Text != "github.com can't read cplieger/animap" {
				t.Errorf("refusal = %+v, want github.com can't read cplieger/animap", fe)
			}
			if len(core.lists) != 0 {
				t.Errorf("lists read after the repository refused: %v", core.lists)
			}
		})
	}
	t.Run("the kind's list read refuses", func(t *testing.T) {
		core := &ruleCheckCore{
			aff:     forgeapi.RepoAffordances{HasIssues: forgeapi.SupportYes},
			listErr: &forgeapi.Error{Op: "ListRuns", Kind: forgeapi.KindForbidden, Status: 403, Message: "Resource not accessible by personal access token"},
		}
		fe := refusal(t, recordManager(t, core).CheckEventRule(t.Context(), eventSpec(launchrule.KindRunConcluded)))
		if fe.Unavailable || fe.Text != "github.com answered: Resource not accessible by personal access token" {
			t.Errorf("refusal = %+v, want the forge's sentence", fe)
		}
	})
	t.Run("a Gitea that lists runs undated", func(t *testing.T) {
		core := &ruleCheckCore{listErr: &forgeapi.Error{Op: "ListRuns", Kind: forgeapi.KindUpstream, Code: forgeapi.CodeRunUndated}}
		fe := refusal(t, recordManager(t, core).CheckEventRule(t.Context(), eventSpec(launchrule.KindRunConcluded)))
		if fe.Unavailable || fe.Text != "This Gitea lists runs with no creation time, so marotte cannot watch them for a finished run" {
			t.Errorf("refusal = %+v, want the undated-runs sentence", fe)
		}
	})
	t.Run("a credential that needs a reconnect", func(t *testing.T) {
		core := &ruleCheckCore{affErr: &forgeapi.Error{Kind: forgeapi.KindUnauthorized, Code: forgeapi.CodeReconnectRequired}}
		fe := refusal(t, recordManager(t, core).CheckEventRule(t.Context(), eventSpec(launchrule.KindPROpened)))
		if fe.Unavailable || fe.Text != "github.com asks you to reconnect in Sources" {
			t.Errorf("refusal = %+v, want the reconnect sentence", fe)
		}
	})
}

func TestSaveRefusesIssuesOff(t *testing.T) {
	core := &ruleCheckCore{aff: forgeapi.RepoAffordances{HasIssues: forgeapi.SupportNo}}
	m := recordManager(t, core)
	for _, kind := range []launchrule.Kind{launchrule.KindIssueLabeled, launchrule.KindIssueOpened} {
		fe := refusal(t, m.CheckEventRule(t.Context(), eventSpec(kind)))
		if fe.Unavailable || fe.Text != "cplieger/animap has issues turned off" {
			t.Errorf("%s: refusal = %+v, want issues turned off", kind, fe)
		}
	}
	if len(core.lists) != 0 {
		t.Errorf("lists read for a repository with issues off: %v", core.lists)
	}
	t.Run("a pull-request rule does not need issues", func(t *testing.T) {
		if err := m.CheckEventRule(t.Context(), eventSpec(launchrule.KindPRLabeled)); err != nil {
			t.Errorf("CheckEventRule(pr_labeled) = %v, want nil", err)
		}
	})
}

func TestSaveRateLimitedAnswers503(t *testing.T) {
	for name, core := range map[string]*ruleCheckCore{
		"rate-limited affordances": {affErr: &forgeapi.Error{Kind: forgeapi.KindRateLimited, Status: 429, Retryable: true}},
		"transient list read":      {listErr: &forgeapi.Error{Kind: forgeapi.KindTransient, Retryable: true}},
		"a server error":           {affErr: &forgeapi.Error{Kind: forgeapi.KindUpstream, Status: 502, Retryable: true}},
		"the library's deadline":   {listErr: context.DeadlineExceeded},
	} {
		t.Run(name, func(t *testing.T) {
			fe := refusal(t, recordManager(t, core).CheckEventRule(t.Context(), eventSpec(launchrule.KindRunConcluded)))
			if !fe.Unavailable {
				t.Errorf("refusal = %+v, want one a later save may pass", fe)
			}
		})
	}
}

func TestSaveRefusesSignedOutConnection(t *testing.T) {
	t.Run("no such connection", func(t *testing.T) {
		ev := eventSpec(launchrule.KindRunConcluded)
		ev.Connection = MakeID(KindGitHub, "ghe.example.test")
		fe := refusal(t, recordManager(t, &ruleCheckCore{}).CheckEventRule(t.Context(), ev))
		if fe.Unavailable || fe.Text != "Connect ghe.example.test in Sources first" {
			t.Errorf("refusal = %+v, want Connect ghe.example.test in Sources first", fe)
		}
	})
	t.Run("persistence unusable", func(t *testing.T) {
		m := recordManager(t, &ruleCheckCore{})
		widenStore(t, m)
		fe := refusal(t, m.CheckEventRule(t.Context(), eventSpec(launchrule.KindRunConcluded)))
		if fe.Unavailable || fe.Text != "Connect github.com in Sources first" {
			t.Errorf("refusal = %+v, want Connect github.com in Sources first", fe)
		}
	})
}

// Each kind reads one page of the list its poll walks; a run rule's page is bounded to the last hour.
func TestSaveReadsOnePageOfTheKindsList(t *testing.T) {
	core := &ruleCheckCore{aff: forgeapi.RepoAffordances{HasIssues: forgeapi.SupportYes}}
	m := recordManager(t, core)
	start := time.Now()
	for _, kind := range []launchrule.Kind{
		launchrule.KindIssueLabeled, launchrule.KindPROpened, launchrule.KindRunConcluded,
	} {
		if err := m.CheckEventRule(t.Context(), eventSpec(kind)); err != nil {
			t.Fatalf("CheckEventRule(%s) = %v, want nil", kind, err)
		}
	}
	if want := []string{"issues", "prs", "runs"}; !slices.Equal(core.lists, want) {
		t.Fatalf("lists read = %v, want %v", core.lists, want)
	}
	for i, opts := range core.opts {
		set, err := forgeapi.ResolveList(opts...)
		if err != nil || set.PageBound != 1 {
			t.Errorf("%s read with page bound %d (%v), want 1", core.lists[i], set.PageBound, err)
		}
		if core.lists[i] == "runs" && (set.Since.Before(start.Add(-time.Hour-time.Minute)) || set.Since.After(time.Now().Add(-time.Hour))) {
			t.Errorf("runs read since %v, want an hour before the save", set.Since)
		}
	}
}
