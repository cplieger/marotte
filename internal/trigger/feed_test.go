package trigger

import (
	"errors"
	"slices"
	"testing"

	"github.com/cplieger/forgeapi/v2"
)

var testFeed = Feed{Connection: "github:github.com", Selector: "cplieger/Animap-Source"}

func issues(numbers ...int) []forgeapi.Issue {
	out := make([]forgeapi.Issue, 0, len(numbers))
	for _, n := range numbers {
		out = append(out, forgeapi.Issue{Ref: forgeapi.IssueRef{Number: n}})
	}
	return out
}

func issueNumbers(items []forgeapi.Issue) []int {
	out := make([]int, 0, len(items))
	for i := range items {
		out = append(out, items[i].Ref.Number)
	}
	return out
}

func TestWalkCompletesInsidePoll(t *testing.T) {
	capped := &forgeapi.Partial{Reason: forgeapi.PartialPaginationCap, Fetched: 3}
	src := &fakeSource{issues: []forgeapi.Page[forgeapi.Issue]{
		// forgeapi's DefaultListPages is 3, so a 4-page list answers its first three pages in one call.
		{Items: issues(1, 2, 3), Next: "c3", Partial: capped},
		{Items: issues(4)},
	}}

	w := walkIssues(t.Context(), src, testFeed)

	if !w.complete() || w.Calls != 2 {
		t.Errorf("walk = complete %v after %d calls, want complete after 2", w.complete(), w.Calls)
	}
	if got := issueNumbers(w.Items); !slices.Equal(got, []int{1, 2, 3, 4}) {
		t.Errorf("walk read %v, want 1 to 4", got)
	}
	calls := src.recorded()
	if len(calls) != 2 || calls[0].after != "" || calls[1].after != "c3" {
		t.Errorf("calls = %+v, want the first page then WithAfter c3", calls)
	}
}

func TestWalkStopsPartialOnOtherReasons(t *testing.T) {
	for name, page := range map[string]forgeapi.Page[forgeapi.Issue]{
		"budget":        {Items: issues(1), Next: "c1", Partial: &forgeapi.Partial{Reason: forgeapi.PartialBudget}},
		"rate limited":  {Items: issues(1), Next: "c1", Partial: &forgeapi.Partial{Reason: forgeapi.PartialRateLimited}},
		"graphql":       {Items: issues(1), Next: "c1", Partial: &forgeapi.Partial{Reason: forgeapi.PartialGraphQLPartial}},
		"result window": {Items: issues(1), Partial: &forgeapi.Partial{Reason: forgeapi.PartialResultWindow, OmittedAtLeast: 45}},
		"cap, no next":  {Items: issues(1), Partial: &forgeapi.Partial{Reason: forgeapi.PartialPaginationCap}},
	} {
		t.Run(name, func(t *testing.T) {
			src := &fakeSource{issues: []forgeapi.Page[forgeapi.Issue]{page, {Items: issues(2)}}}
			w := walkIssues(t.Context(), src, testFeed)
			if w.complete() || w.Calls != 1 || w.Partial != page.Partial {
				t.Errorf("walk = complete %v, %d calls, partial %+v; want partial after 1 call with the page's marker",
					w.complete(), w.Calls, w.Partial)
			}
			if got := issueNumbers(w.Items); !slices.Equal(got, []int{1}) {
				t.Errorf("walk kept %v, want the rows it read", got)
			}
		})
	}
	t.Run("a continuation that does not move", func(t *testing.T) {
		stuck := forgeapi.Page[forgeapi.Issue]{Items: issues(1), Next: "c1", Partial: &forgeapi.Partial{Reason: forgeapi.PartialPaginationCap}}
		src := &fakeSource{issues: []forgeapi.Page[forgeapi.Issue]{stuck, stuck, stuck}}
		w := walkIssues(t.Context(), src, testFeed)
		if w.complete() || w.Calls != 2 {
			t.Errorf("walk = complete %v after %d calls, want partial after 2", w.complete(), w.Calls)
		}
	})
	t.Run("a call error", func(t *testing.T) {
		boom := errors.New("502")
		capped := forgeapi.Page[forgeapi.Issue]{Items: issues(1), Next: "c1", Partial: &forgeapi.Partial{Reason: forgeapi.PartialPaginationCap}}
		src := &fakeSource{issues: []forgeapi.Page[forgeapi.Issue]{capped}, errs: map[string]error{}}
		w := walk(func(after forgeapi.Cursor) (forgeapi.Page[forgeapi.Issue], error) {
			if after != "" {
				return forgeapi.Page[forgeapi.Issue]{}, boom
			}
			return src.OpenIssues(t.Context(), testFeed, after)
		})
		if w.complete() || !errors.Is(w.Err, boom) || w.Calls != 2 {
			t.Errorf("walk = err %v after %d calls, want the call error after 2", w.Err, w.Calls)
		}
	})
}

func TestFeedKeyFoldsCase(t *testing.T) {
	lower := Feed{Connection: testFeed.Connection, Selector: "cplieger/animap-source"}
	if testFeed.key() != lower.key() || testFeed.repoID() != lower.repoID() {
		t.Errorf("keys %q and %q differ, want one feed for two spellings of one repository", testFeed.key(), lower.key())
	}
	other := Feed{Connection: "github:ghe.example", Selector: "cplieger/animap-source"}
	if other.key() == lower.key() {
		t.Error("two connections share a feed key")
	}
}
