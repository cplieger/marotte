package forges

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
)

// marshalObject encodes v and decodes it back as a generic object, so a test
// reads the wire's keys rather than the Go struct's fields.
func marshalObject(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal(%T) = %v", v, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return out
}

func TestPRWire_EveryActionMemberIsRequired(t *testing.T) {
	for f := range reflect.TypeFor[PRAction]().Fields() {
		if strings.Contains(f.Tag.Get("json"), "omitempty") {
			t.Errorf("PRAction.%s is tagged %q; every action member is required", f.Name, f.Tag.Get("json"))
		}
	}
	if f, _ := reflect.TypeFor[PR]().FieldByName("Action"); strings.Contains(f.Tag.Get("json"), "omitempty") {
		t.Errorf("PR.Action is tagged %q; the action object is required", f.Tag.Get("json"))
	}

	row := marshalObject(t, prWire(&forgeapi.PullRequest{}))
	action, ok := row["action"].(map[string]any)
	if !ok {
		t.Fatalf("a zero row encodes action as %v, want an object", row["action"])
	}
	want := map[string]any{
		"mergeable": "unknown", "checks": "unknown", "auto_merge_armed": "unknown",
		"queue_state": "unknown", "queue_position": float64(-1), "merge_blocked": "unknown",
		"checks_passing": float64(0), "checks_failing": float64(0), "checks_pending": float64(0),
		"checks_neutral": float64(0), "checks_unknown": float64(0), "checks_total": float64(0),
	}
	if !reflect.DeepEqual(action, want) {
		t.Errorf("a zero ActionState encodes action = %v, want %v", action, want)
	}
}

func TestPRWire_SpellingsAreTheLibraryStrings(t *testing.T) {
	support := map[forgeapi.Support]string{
		forgeapi.SupportUnknown: "unknown", forgeapi.SupportYes: "yes", forgeapi.SupportNo: "no",
	}
	for member, want := range support {
		got := prWire(&forgeapi.PullRequest{Action: forgeapi.ActionState{Mergeable: member, AutoMergeArmed: member}})
		if got.Action.Mergeable != want || got.Action.AutoMergeArmed != want {
			t.Errorf("Support %d: mergeable %q, auto_merge_armed %q, want %q for both",
				member, got.Action.Mergeable, got.Action.AutoMergeArmed, want)
		}
	}
	checks := map[forgeapi.CheckState]string{
		forgeapi.CheckUnknown: "unknown", forgeapi.CheckPassing: "passing", forgeapi.CheckFailing: "failing",
		forgeapi.CheckPending: "pending", forgeapi.CheckNeutral: "neutral",
	}
	for member, want := range checks {
		if got := prWire(&forgeapi.PullRequest{Action: forgeapi.ActionState{Checks: member}}); got.Action.Checks != want {
			t.Errorf("CheckState %d: checks = %q, want %q", member, got.Action.Checks, want)
		}
	}
	queues := map[forgeapi.QueueState]string{
		forgeapi.QueueUnknown: "unknown", forgeapi.QueueNone: "none", forgeapi.QueueQueued: "queued",
		forgeapi.QueueAwaitingChecks: "awaiting_checks", forgeapi.QueueMergeable: "mergeable",
		forgeapi.QueueUnmergeable: "unmergeable", forgeapi.QueueLocked: "locked",
	}
	for member, want := range queues {
		if got := prWire(&forgeapi.PullRequest{Action: forgeapi.ActionState{QueueState: member}}); got.Action.QueueState != want {
			t.Errorf("QueueState %d: queue_state = %q, want %q", member, got.Action.QueueState, want)
		}
	}
	blocks := map[forgeapi.MergeBlockReason]string{
		forgeapi.MergeBlockUnknown: "unknown", forgeapi.MergeBlockNone: "none", forgeapi.MergeBlockDraft: "draft",
		forgeapi.MergeBlockConflicts: "conflicts", forgeapi.MergeBlockChecksFailing: "checks_failing",
		forgeapi.MergeBlockChecksRunning: "checks_running", forgeapi.MergeBlockBehind: "behind",
		forgeapi.MergeBlockBlocked: "blocked",
	}
	for member, want := range blocks {
		if got := prWire(&forgeapi.PullRequest{Action: forgeapi.ActionState{MergeBlocked: member}}); got.Action.MergeBlocked != want {
			t.Errorf("MergeBlockReason %d: merge_blocked = %q, want %q", member, got.Action.MergeBlocked, want)
		}
	}
	states := map[forgeapi.PRState]string{
		forgeapi.PRStateUnknown: "unknown", forgeapi.PRStateOpen: "open",
		forgeapi.PRStateClosed: "closed", forgeapi.PRStateMerged: "merged",
	}
	for member, want := range states {
		if got := prWire(&forgeapi.PullRequest{State: member, Draft: true}); got.State != want || !got.Draft {
			t.Errorf("PRState %d (draft): state = %q, draft %t, want %q and draft", member, got.State, got.Draft, want)
		}
	}
}

func TestPRWire_QueuePositionNeedsAPlaceInAQueue(t *testing.T) {
	cases := []struct {
		queue    forgeapi.QueueState
		position int
		want     int
	}{
		{queue: forgeapi.QueueUnknown, position: 0, want: -1},
		{queue: forgeapi.QueueNone, position: 0, want: -1},
		{queue: forgeapi.QueueQueued, position: 0, want: 0},
		{queue: forgeapi.QueueQueued, position: 3, want: 3},
		{queue: forgeapi.QueueAwaitingChecks, position: forgeapi.QueuePositionUnknown, want: -1},
	}
	for _, tc := range cases {
		a := forgeapi.ActionState{QueueState: tc.queue, QueuePosition: tc.position}
		if got := prWire(&forgeapi.PullRequest{Action: a}).Action.QueuePosition; got != tc.want {
			t.Errorf("queue %d at position %d: queue_position = %d, want %d", tc.queue, tc.position, got, tc.want)
		}
	}
}

func TestPRWire_CarriesTheRowAndItsRepository(t *testing.T) {
	created := time.UnixMilli(1_700_000_000_123)
	got := prWire(&forgeapi.PullRequest{
		Ref:   forgeapi.PRRef{Number: 7},
		Repo:  forgeapi.RepoRef{Family: forgeapi.FamilyGitHub, Selector: "O/R", DisplayPath: "O/R"},
		Title: "Fix", Body: "b", Author: "alice", SourceBranch: "feat", TargetBranch: "main",
		WebURL: "https://example.com/pr/7", HeadSHA: "abc1234", CreatedAt: created, UpdatedAt: created.Add(time.Second),
		State: forgeapi.PRStateOpen, Partial: &forgeapi.Partial{Reason: forgeapi.PartialLabelsNotApplied, Fetched: 2},
		Action: forgeapi.ActionState{
			Mergeable: forgeapi.SupportYes, Checks: forgeapi.CheckFailing, ChecksPassing: 1, ChecksFailing: 2,
			ChecksPending: 3, ChecksNeutral: 4, ChecksUnknown: 5, ChecksTotal: 15,
			QueueState: forgeapi.QueueNone, MergeBlocked: forgeapi.MergeBlockChecksFailing,
		},
	})
	want := PR{
		Partial: &PartialResult{Reason: "labels_not_applied", Fetched: 2},
		RepoID:  "v1.6f2f72", Repo: "O/R", Title: "Fix", Body: "b", State: "open", Author: "alice",
		SourceBranch: "feat", TargetBranch: "main", URL: "https://example.com/pr/7", HeadSHA: "abc1234",
		Action: PRAction{
			Mergeable: "yes", Checks: "failing", AutoMergeArmed: "unknown", QueueState: "none",
			MergeBlocked: "checks_failing", ChecksPassing: 1, ChecksFailing: 2, ChecksPending: 3,
			ChecksNeutral: 4, ChecksUnknown: 5, ChecksTotal: 15, QueuePosition: -1,
		},
		Number: 7, CreatedAt: 1_700_000_000_123, UpdatedAt: 1_700_000_001_123,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("prWire() = %+v, want %+v", got, want)
	}
}

func TestIssueWire_LabelsAreNames(t *testing.T) {
	created := time.UnixMilli(1_700_000_000_123)
	got := issueWire(&forgeapi.Issue{
		Ref:   forgeapi.IssueRef{Number: 16},
		Repo:  forgeapi.RepoRef{Family: forgeapi.FamilyGitHub, Selector: "o/r", DisplayPath: "o/r"},
		Title: "Seed", Body: "b", Author: "bot", WebURL: "https://example.com/issues/16",
		Labels:    []forgeapi.Label{{Name: "bug", Color: "d73a4a", Description: "broken"}, {Name: "ui"}},
		CreatedAt: created, UpdatedAt: created.Add(time.Second), State: forgeapi.IssueStateOpen,
	})
	want := Issue{
		Title: "Seed", Body: "b", State: "open", Author: "bot", URL: "https://example.com/issues/16",
		Labels: []string{"bug", "ui"}, Number: 16, CreatedAt: 1_700_000_000_123, UpdatedAt: 1_700_000_001_123,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("issueWire() = %+v, want %+v", got, want)
	}
	if labels := marshalObject(t, got)["labels"]; !reflect.DeepEqual(labels, []any{"bug", "ui"}) {
		t.Errorf("an issue encodes labels = %v, want the names [bug ui]", labels)
	}

	states := map[forgeapi.IssueState]string{
		forgeapi.IssueStateUnknown: "unknown", forgeapi.IssueStateOpen: "open", forgeapi.IssueStateClosed: "closed",
	}
	for member, want := range states {
		if got := issueWire(&forgeapi.Issue{State: member}).State; got != want {
			t.Errorf("IssueState %d: state = %q, want %q", member, got, want)
		}
	}
}

func TestChecksWire_CarriesTheFoldedCountsAndContexts(t *testing.T) {
	got := commitChecksWire(&forgeapi.CommitChecks{
		Ref: "main", State: forgeapi.CheckFailing,
		Passing: 1, Failing: 2, Pending: 3, Neutral: 4, Unknown: 5, Total: 15,
		Contexts: []forgeapi.CheckContext{
			{Name: "ci/build", Description: "Build failed", TargetURL: "https://ci.example/1", State: forgeapi.CheckFailing},
			{Name: "lint", State: forgeapi.CheckNeutral},
		},
		Partial:   &forgeapi.Partial{Reason: forgeapi.PartialPaginationCap, Fetched: 2, OmittedAtLeast: 1},
		Successor: &forgeapi.RepoRef{Family: forgeapi.FamilyGitHub, Selector: "o/renamed", DisplayPath: "o/renamed"},
	})
	want := CommitChecks{
		Partial:   &PartialResult{Reason: "pagination_cap", Fetched: 2, OmittedAtLeast: 1},
		Successor: &RepoSuccessor{RepoID: "v1.6f2f72656e616d6564", DisplayPath: "o/renamed"},
		State:     "failing",
		Checks: []Check{
			{Name: "ci/build", State: "failing", Description: "Build failed", URL: "https://ci.example/1"},
			{Name: "lint", State: "neutral"},
		},
		ChecksPassing: 1, ChecksFailing: 2, ChecksPending: 3, ChecksNeutral: 4, ChecksUnknown: 5, ChecksTotal: 15,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("commitChecksWire() = %+v, want %+v", got, want)
	}

	states := map[forgeapi.CheckState]string{
		forgeapi.CheckUnknown: "unknown", forgeapi.CheckPassing: "passing", forgeapi.CheckFailing: "failing",
		forgeapi.CheckPending: "pending", forgeapi.CheckNeutral: "neutral",
	}
	for member, want := range states {
		one := commitChecksWire(&forgeapi.CommitChecks{State: member, Contexts: []forgeapi.CheckContext{{State: member}}})
		if one.State != want || len(one.Checks) != 1 || one.Checks[0].State != want {
			t.Errorf("CheckState %d: state = %q, context %+v, want %q for both", member, one.State, one.Checks, want)
		}
	}

	zero := marshalObject(t, commitChecksWire(&forgeapi.CommitChecks{}))
	wantZero := map[string]any{
		"state": "unknown", "checks": []any{},
		"checks_passing": float64(0), "checks_failing": float64(0), "checks_pending": float64(0),
		"checks_neutral": float64(0), "checks_unknown": float64(0), "checks_total": float64(0),
	}
	if !reflect.DeepEqual(zero, wantZero) {
		t.Errorf("a zero fold encodes %v, want every member present: %v", zero, wantZero)
	}
}

func TestRepoWire_CarriesRepoIDAndAffordances(t *testing.T) {
	got := repoWire(&forgeapi.Repository{
		Ref:         forgeapi.RepoRef{Family: forgeapi.FamilyGitLab, Selector: "group/sub/project", DisplayPath: "group/sub/project"},
		Description: "d", WebURL: "https://gitlab.com/group/sub/project", CloneURL: "https://gitlab.com/group/sub/project.git",
		UpdatedAt: time.UnixMilli(1_700_000_000_000), Private: true, Archived: true, Fork: true,
		Affordances: forgeapi.RepoAffordances{
			MergeStrategies: []string{"merge", "squash"},
			HasIssues:       forgeapi.SupportYes, CanPush: forgeapi.SupportNo,
			Ev: map[forgeapi.Capability]forgeapi.Evidence{
				forgeapi.CapHasIssues: {Source: forgeapi.EvidenceMetadata, Detail: "issues_enabled"},
				forgeapi.CapCanPush:   {Source: forgeapi.EvidenceProbe, Detail: "permissions"},
			},
			DefaultBranch: "main",
		},
	})
	want := Repo{
		RepoID: "v1.67726f75702f7375622f70726f6a656374",
		Owner:  "group/sub", Name: "project", FullName: "group/sub/project", DefaultBranch: "main",
		URL: "https://gitlab.com/group/sub/project", CloneURL: "https://gitlab.com/group/sub/project.git",
		Description: "d", UpdatedAt: 1_700_000_000_000, Private: true, Archived: true, Fork: true,
		Affordances: RepoAffordances{
			MergeStrategies: []string{"merge", "squash"},
			HasIssues:       Affordance{Support: "yes", Source: "metadata", Detail: "issues_enabled"},
			CanPush:         Affordance{Support: "no", Source: "probe", Detail: "permissions"},
			MergeTrain:      Affordance{Support: "unknown", Source: "unknown"},
			DefaultBranch:   "main",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("repoWire() = %+v, want %+v", got, want)
	}

	// A list with no strategies read still encodes an array: the client's
	// decoder requires one, and a null would fail the whole repository list.
	zero := marshalObject(t, repoWire(&forgeapi.Repository{}))
	aff, _ := zero["affordances"].(map[string]any)
	if strategies, ok := aff["merge_strategies"].([]any); !ok || len(strategies) != 0 {
		t.Errorf("a zero repository encodes merge_strategies = %v, want []", aff["merge_strategies"])
	}
	if zero["owner"] != "" || zero["name"] != "" {
		t.Errorf("a zero repository encodes owner %v and name %v, want both empty", zero["owner"], zero["name"])
	}
}
