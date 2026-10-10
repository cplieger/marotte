package forges

import (
	"strings"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/marotte/internal/marotte"
)

// Detection is the forge kind whose API answers at an address.
type Detection struct {
	Kind Kind `json:"kind"`
}

// PartialResult says a list or a row is incomplete: why, how much arrived, and
// at least how much did not.
type PartialResult struct {
	Reason         string `json:"reason"`
	Fetched        int    `json:"fetched"`
	OmittedAtLeast int    `json:"omitted_at_least"`
}

// RepoSuccessor is the repository a stale one moved to.
type RepoSuccessor struct {
	RepoID      string `json:"repo_id"`
	DisplayPath string `json:"display_path"`
}

// PRAction is what one pull request's controls read. Every member is required,
// so an absent field can never read as a verdict.
type PRAction struct {
	// Mergeable is "yes", "no" or "unknown".
	Mergeable string `json:"mergeable"`
	// Checks is "unknown", "passing", "failing", "pending" or "neutral".
	Checks string `json:"checks"`
	// AutoMergeArmed is "yes", "no" or "unknown".
	AutoMergeArmed string `json:"auto_merge_armed"`
	QueueState     string `json:"queue_state"`
	// MergeBlocked is "unknown", "none", "draft", "conflicts",
	// "checks_failing", "checks_running", "behind" or "blocked".
	MergeBlocked  string `json:"merge_blocked"`
	ChecksPassing int    `json:"checks_passing"`
	ChecksFailing int    `json:"checks_failing"`
	ChecksPending int    `json:"checks_pending"`
	ChecksNeutral int    `json:"checks_neutral"`
	ChecksUnknown int    `json:"checks_unknown"`
	ChecksTotal   int    `json:"checks_total"`
	// QueuePosition is -1 unless QueueState places the row in a queue.
	QueuePosition int `json:"queue_position"`
}

// FieldFill is why an inventory row holds the value it does for one field its
// family's list does not carry.
type FieldFill struct {
	// Field is "checks", "check_counts" (the per-state counts and whether the
	// fold stopped short), "mergeable", "merge_blocked", "auto_merge_armed",
	// "head_sha", "source_branch" or "target_branch".
	Field string `json:"field"`
	// Reason is "filled" (by a read of the row), "not_on_list" (no read yet),
	// "unread" (the last read failed) or "not_supplied" (not even a read
	// supplies it on this family).
	Reason string `json:"reason"`
	// AsOf is the filling read's unix ms, present when filled.
	AsOf int64 `json:"as_of,omitempty"`
}

// PR is one pull request row.
type PR struct {
	Partial *PartialResult `json:"partial,omitempty"`
	// RepoID is the canonical repository id the row's routes take.
	RepoID string `json:"repo_id"`
	// SourceRepoID is the canonical id of the repository the head branch lives
	// in: RepoID's for a branch inside it, the fork's for a fork, and absent
	// where the read does not name it (a deleted fork).
	SourceRepoID string `json:"source_repo_id,omitempty"`
	// Repo is the repository's display path.
	Repo  string `json:"repo"`
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	// State is "open", "closed", "merged" or "unknown"; a draft says so in
	// Draft, never here.
	State        string `json:"state"`
	Author       string `json:"author,omitempty"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	URL          string `json:"url,omitempty"`
	// HeadSHA is the commit a merge pins itself to.
	HeadSHA string `json:"head_sha,omitempty"`
	// Fill is present on an inventory row of a family whose list lacks fields,
	// one entry per such field.
	Fill      []FieldFill `json:"fill,omitempty"`
	Action    PRAction    `json:"action"`
	Number    int         `json:"number"`
	CreatedAt int64       `json:"created_at,omitempty"`
	UpdatedAt int64       `json:"updated_at,omitempty"`
	Draft     bool        `json:"draft,omitempty"`
}

// Affordance is whether one thing can be done, and what said so: something a
// repository allows, or a capability of a connection or of its credential.
type Affordance struct {
	// Support is "yes", "no" or "unknown".
	Support string `json:"support"`
	Source  string `json:"source"`
	Detail  string `json:"detail"`
}

// RepoAffordances is what a repository allows.
type RepoAffordances struct {
	HasIssues     Affordance `json:"has_issues"`
	CanPush       Affordance `json:"can_push"`
	MergeTrain    Affordance `json:"merge_train"`
	DefaultBranch string     `json:"default_branch"`
	// MergeStrategies are the family's own spellings, empty when none was read.
	MergeStrategies []string `json:"merge_strategies"`
}

// Capabilities is what a connection's instance and its credential can do, by
// capability. Each scope carries every capability forgeapi names for it, so a
// verdict nothing answered arrives as "unknown" rather than as an absent key.
type Capabilities struct {
	Connection map[string]Affordance `json:"connection"`
	Grant      map[string]Affordance `json:"grant"`
}

// Repo is a repository the connection reaches.
type Repo struct {
	// RepoID is the canonical repository id the repository's routes take.
	RepoID string `json:"repo_id"`
	// Owner is everything before the display path's last "/", a namespace
	// path on GitLab.
	Owner string `json:"owner"`
	Name  string `json:"name"`
	// FullName is the display path.
	FullName      string          `json:"full_name"`
	DefaultBranch string          `json:"default_branch,omitempty"`
	URL           string          `json:"url,omitempty"`
	CloneURL      string          `json:"clone_url,omitempty"`
	Description   string          `json:"description,omitempty"`
	Affordances   RepoAffordances `json:"affordances"`
	UpdatedAt     int64           `json:"updated_at,omitempty"`
	Private       bool            `json:"private,omitempty"`
	Archived      bool            `json:"archived,omitempty"`
	Fork          bool            `json:"fork,omitempty"`
}

// RepoList is one page of a connection's repositories. Next is the opaque
// cursor `?after=` takes, absent when the list is complete.
type RepoList struct {
	Partial *PartialResult `json:"partial,omitempty"`
	Next    string         `json:"next,omitempty"`
	Repos   []Repo         `json:"repos"`
}

// PRList is one page of a repository's pull requests. Successor names where a
// moved repository went.
type PRList struct {
	Partial   *PartialResult `json:"partial,omitempty"`
	Successor *RepoSuccessor `json:"successor,omitempty"`
	Next      string         `json:"next,omitempty"`
	PRs       []PR           `json:"prs"`
}

// Issue is one issue row.
type Issue struct {
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	// State is "open", "closed" or "unknown".
	State  string `json:"state"`
	Author string `json:"author,omitempty"`
	URL    string `json:"url,omitempty"`
	// Labels are the label names.
	Labels    []string `json:"labels,omitempty"`
	Number    int      `json:"number"`
	CreatedAt int64    `json:"created_at,omitempty"`
	UpdatedAt int64    `json:"updated_at,omitempty"`
}

// IssueList is one page of a repository's issues.
type IssueList struct {
	Partial   *PartialResult `json:"partial,omitempty"`
	Successor *RepoSuccessor `json:"successor,omitempty"`
	Next      string         `json:"next,omitempty"`
	Issues    []Issue        `json:"issues"`
}

// Check is one commit-status context.
type Check struct {
	Name string `json:"name"`
	// State is "unknown", "passing", "failing", "pending" or "neutral".
	State       string `json:"state"`
	Description string `json:"description,omitempty"`
	URL         string `json:"url,omitempty"`
}

// CommitChecks is a commit's folded CI verdict and the contexts it was folded
// from. The verdict and every count are required, as on PRAction.
type CommitChecks struct {
	Partial   *PartialResult `json:"partial,omitempty"`
	Successor *RepoSuccessor `json:"successor,omitempty"`
	// State is the fold, spelled as Check.State.
	State         string  `json:"state"`
	Checks        []Check `json:"checks"`
	ChecksPassing int     `json:"checks_passing"`
	ChecksFailing int     `json:"checks_failing"`
	ChecksPending int     `json:"checks_pending"`
	ChecksNeutral int     `json:"checks_neutral"`
	ChecksUnknown int     `json:"checks_unknown"`
	ChecksTotal   int     `json:"checks_total"`
}

// PRDetail is one pull request as a person opens it: the row with its
// description, and the folded checks of its head commit. Checks is absent
// exactly when the row names no head commit.
type PRDetail struct {
	Checks *CommitChecks `json:"checks,omitempty"`
	PR     PR            `json:"pr"`
}

// CloneRepo is one workspace clone joined to the connection that serves it.
type CloneRepo struct {
	Dir     string `json:"dir"`
	ForgeID string `json:"forge_id"`
	// RepoID is the canonical id of the clone's repository on that connection.
	RepoID string `json:"repo_id"`
}

// InventoryError is the last failed list call of an entry's cycle, in the
// error envelope's terms.
type InventoryError struct {
	Code        string `json:"code"`
	Kind        string `json:"kind"`
	DiagID      string `json:"diag_id,omitempty"`
	RetryAfterS int64  `json:"retry_after_s,omitempty"`
}

// InventoryBudget is a connection's request budget as its client last read it.
type InventoryBudget struct {
	// Remaining is -1 where the family reports none.
	Remaining int `json:"remaining"`
	// Reset is unix ms, absent where the family reports none.
	Reset    int64 `json:"reset,omitempty"`
	LastCost int   `json:"last_cost"`
}

// InventoryScope is one list a connection's cycle reads. Next is present while
// its walk has more pages; Partial is the last page's own.
type InventoryScope struct {
	Partial *PartialResult `json:"partial,omitempty"`
	// Scope is "owner", "authored" or "added".
	Scope string `json:"scope"`
	Owner string `json:"owner,omitempty"`
	Next  string `json:"next,omitempty"`
	// Rows carry no body.
	Rows []PR `json:"rows"`
}

// InventoryEntry is one connection's pull requests as the poller's last
// present cycle read them.
type InventoryEntry struct {
	Error   *InventoryError  `json:"error,omitempty"`
	Budget  *InventoryBudget `json:"budget,omitempty"`
	ForgeID string           `json:"forge_id"`
	// State is "loading", "ready", "partial" or "failed".
	State string `json:"state"`
	// CycleID is the decimal id of the cycle that produced the entry, "0" while
	// loading. Cycles count from 1.
	CycleID string `json:"cycle_id"`
	// Credential is "unknown", "valid", "refresh_due", "refreshing" or
	// "reconnect_required".
	Credential string           `json:"credential"`
	Scopes     []InventoryScope `json:"scopes"`
	Clones     []CloneRepo      `json:"clones"`
	FetchedAt  int64            `json:"fetched_at"`
}

// InventoryList is every connected connection's entry, ordered by connection
// id, and one forge_inventory stamp per entry in the same order.
type InventoryList struct {
	Entries []InventoryEntry        `json:"entries"`
	Subject []*marotte.SubjectStamp `json:"subject"`
	// Viewing is a client showing the pull-request view, which holds the cycle
	// at PRPollInterval.
	Viewing bool `json:"viewing"`
}

// InventoryRefresh names the cycle that answers a refresh: an entry whose
// cycle id is at or past it was read after the press.
type InventoryRefresh struct {
	CycleID string `json:"cycle_id"`
}

// OwnerScopes is the owners a present cycle reads beside a connection's own
// pull requests, as stored.
type OwnerScopes struct {
	Owners []string `json:"owners"`
}

// ProbeResult is a probe's answer: the row as the probe left it, whether that
// row is connected, and the failure's sentence when the identity read failed.
// A temporary failure on a connected row answers its sentence beside connected
// true.
type ProbeResult struct {
	Forge     *ConfiguredForge `json:"forge,omitempty"`
	Error     string           `json:"error,omitempty"`
	Connected bool             `json:"connected"`
}

// InventoryChangedPayload is the forge_inventory event: one connection's entry
// as the cycle that wrote it left it.
type InventoryChangedPayload struct {
	Entry InventoryEntry `json:"entry"`
}

// Release is one tagged release.
type Release struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name,omitempty"`
	Body        string `json:"body,omitempty"`
	URL         string `json:"url,omitempty"`
	PublishedAt int64  `json:"published_at,omitempty"`
	Draft       bool   `json:"draft,omitempty"`
	Prerelease  bool   `json:"prerelease,omitempty"`
}

// ReleaseList is one page of a repository's releases.
type ReleaseList struct {
	Partial   *PartialResult `json:"partial,omitempty"`
	Successor *RepoSuccessor `json:"successor,omitempty"`
	Next      string         `json:"next,omitempty"`
	Releases  []Release      `json:"releases"`
}

// Label is a label a repository defines.
type Label struct {
	Name        string `json:"name"`
	Color       string `json:"color,omitempty"`
	Description string `json:"description,omitempty"`
}

// LabelList is one page of a repository's labels.
type LabelList struct {
	Partial   *PartialResult `json:"partial,omitempty"`
	Successor *RepoSuccessor `json:"successor,omitempty"`
	Next      string         `json:"next,omitempty"`
	Labels    []Label        `json:"labels"`
}

// MergeOutcome is what happened to one merge request. Code is set on the two
// states that carry no verdict yet, accepted and in_flight, which the merge
// status read follows up.
type MergeOutcome struct {
	State         string `json:"state"`
	Code          string `json:"code,omitempty"`
	QueueState    string `json:"queue_state"`
	QueuePosition int    `json:"queue_position"`
}

// MergeResult is the merge route's answer.
type MergeResult struct {
	// CycleID names the cycle that reads the merge, as PRChanged's does.
	CycleID string       `json:"cycle_id"`
	Outcome MergeOutcome `json:"outcome"`
}

// PRChanged is the pull request a close or a reopen answered, and the cycle
// that reads the change: an inventory entry at or past it was read after it,
// while an earlier one may have read the row before it.
type PRChanged struct {
	CycleID string `json:"cycle_id"`
	PR      PR     `json:"pr"`
}

// MergeStatus is one pull request's merge state as read back after a merge.
type MergeStatus struct {
	Successor  *RepoSuccessor `json:"successor,omitempty"`
	Merged     string         `json:"merged"`
	QueueState string         `json:"queue_state"`
	WebURL     string         `json:"web_url,omitempty"`
}

func mergeOutcomeWire(o *forgeapi.MergeOutcome) MergeOutcome {
	return MergeOutcome{
		State: o.State.String(), Code: o.Code, QueueState: o.QueueState.String(), QueuePosition: o.QueuePosition,
	}
}

func mergeStatusWire(s *forgeapi.MergeStatus) MergeStatus {
	return MergeStatus{
		Successor: successorOf(s.Successor), Merged: s.Merged.String(), QueueState: s.Queue.String(), WebURL: s.WebURL,
	}
}

func partialOf(p *forgeapi.Partial) *PartialResult {
	if p == nil {
		return nil
	}
	return &PartialResult{Reason: p.Reason.String(), Fetched: p.Fetched, OmittedAtLeast: p.OmittedAtLeast}
}

// successorOf is the wire form of a moved repository's new reference, with its
// id derived again so the client receives the canonical spelling.
func successorOf(r *forgeapi.RepoRef) *RepoSuccessor {
	if r == nil {
		return nil
	}
	return &RepoSuccessor{RepoID: r.Encode(), DisplayPath: r.DisplayPath}
}

// queuePosition reads the position only where the state places the row in a
// queue, so a zero ActionState cannot claim the head of one.
func queuePosition(a *forgeapi.ActionState) int {
	switch a.QueueState {
	case forgeapi.QueueUnknown, forgeapi.QueueNone:
		return forgeapi.QueuePositionUnknown
	}
	return a.QueuePosition
}

func actionOf(a *forgeapi.ActionState) PRAction {
	return PRAction{
		Mergeable:      a.Mergeable.String(),
		Checks:         a.Checks.String(),
		AutoMergeArmed: a.AutoMergeArmed.String(),
		QueueState:     a.QueueState.String(),
		MergeBlocked:   a.MergeBlocked.String(),
		ChecksPassing:  a.ChecksPassing,
		ChecksFailing:  a.ChecksFailing,
		ChecksPending:  a.ChecksPending,
		ChecksNeutral:  a.ChecksNeutral,
		ChecksUnknown:  a.ChecksUnknown,
		ChecksTotal:    a.ChecksTotal,
		QueuePosition:  queuePosition(a),
	}
}

// unixMilli is t in unix milliseconds, 0 for the zero time so an omitempty
// field leaves it out.
func unixMilli(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func prWire(p *forgeapi.PullRequest) PR {
	return PR{
		Partial: partialOf(p.Partial), RepoID: p.Repo.Encode(), SourceRepoID: repoIDOrEmpty(&p.SourceRepo),
		Repo: p.Repo.DisplayPath, Title: p.Title, Body: p.Body, State: p.State.String(), Author: p.Author,
		SourceBranch: p.SourceBranch, TargetBranch: p.TargetBranch, URL: p.WebURL, HeadSHA: p.HeadSHA,
		Action: actionOf(&p.Action), Number: p.Ref.Number,
		CreatedAt: unixMilli(p.CreatedAt), UpdatedAt: unixMilli(p.UpdatedAt), Draft: p.Draft,
	}
}

// repoIDOrEmpty is r's canonical id, or "" for the zero reference a read leaves
// where it does not name the repository.
func repoIDOrEmpty(r *forgeapi.RepoRef) string {
	if r.Selector == "" {
		return ""
	}
	return r.Encode()
}

func affordanceOf(s forgeapi.Support, ev forgeapi.Evidence) Affordance {
	return Affordance{Support: s.String(), Source: ev.Source.String(), Detail: ev.Detail}
}

// The capabilities forgeapi names for each scope.
var (
	connectionScope = []forgeapi.Capability{forgeapi.CapRerunChecks}
	grantScope      = []forgeapi.Capability{forgeapi.CapReadMergeState}
)

func capabilitiesOf(conn forgeapi.ConnectionCaps, grant forgeapi.GrantCaps) Capabilities {
	return Capabilities{
		Connection: scopeOf(connectionScope, conn.Caps, conn.Ev),
		Grant:      scopeOf(grantScope, grant.Caps, grant.Ev),
	}
}

// scopeOf is one scope's verdicts: every capability named for it and any other
// the family answered, a missing verdict being the library's unknown.
func scopeOf(named []forgeapi.Capability, caps map[forgeapi.Capability]forgeapi.Support,
	ev map[forgeapi.Capability]forgeapi.Evidence,
) map[string]Affordance {
	out := make(map[string]Affordance, len(named)+len(caps))
	add := func(c forgeapi.Capability) { out[string(c)] = affordanceOf(caps[c], ev[c]) }
	for _, c := range named {
		add(c)
	}
	for c := range caps {
		add(c)
	}
	for c := range ev {
		add(c)
	}
	return out
}

func affordancesOf(a *forgeapi.RepoAffordances) RepoAffordances {
	return RepoAffordances{
		MergeStrategies: append(make([]string, 0, len(a.MergeStrategies)), a.MergeStrategies...),
		HasIssues:       affordanceOf(a.HasIssues, a.Ev[forgeapi.CapHasIssues]),
		CanPush:         affordanceOf(a.CanPush, a.Ev[forgeapi.CapCanPush]),
		MergeTrain:      affordanceOf(a.MergeTrain, a.Ev[forgeapi.CapMergeTrain]),
		DefaultBranch:   a.DefaultBranch,
	}
}

func repoWire(r *forgeapi.Repository) Repo {
	path := r.Ref.DisplayPath
	owner, name := "", path
	if before, after, found := strings.CutLast(path, "/"); found {
		owner, name = before, after
	}
	return Repo{
		RepoID: r.Ref.Encode(), Owner: owner, Name: name, FullName: path,
		DefaultBranch: r.Affordances.DefaultBranch, URL: r.WebURL, CloneURL: r.CloneURL,
		Description: r.Description, Affordances: affordancesOf(&r.Affordances),
		UpdatedAt: unixMilli(r.UpdatedAt), Private: r.Private, Archived: r.Archived, Fork: r.Fork,
	}
}

// rowsOf converts every item, answering an empty slice rather than nil so a
// list encodes [] and never null.
func rowsOf[T, W any](items []T, conv func(*T) W) []W {
	rows := make([]W, 0, len(items))
	for i := range items {
		rows = append(rows, conv(&items[i]))
	}
	return rows
}

func prListWire(page *forgeapi.Page[forgeapi.PullRequest]) PRList {
	return PRList{
		Partial: partialOf(page.Partial), Successor: successorOf(page.Successor),
		Next: string(page.Next), PRs: rowsOf(page.Items, prWire),
	}
}

func repoListWire(page *forgeapi.Page[forgeapi.Repository]) RepoList {
	return RepoList{Partial: partialOf(page.Partial), Next: string(page.Next), Repos: rowsOf(page.Items, repoWire)}
}

func issueWire(is *forgeapi.Issue) Issue {
	labels := make([]string, 0, len(is.Labels))
	for _, l := range is.Labels {
		labels = append(labels, l.Name)
	}
	return Issue{
		Title: is.Title, Body: is.Body, State: is.State.String(), Author: is.Author, URL: is.WebURL,
		Labels: labels, Number: is.Ref.Number, CreatedAt: unixMilli(is.CreatedAt), UpdatedAt: unixMilli(is.UpdatedAt),
	}
}

func issueListWire(page *forgeapi.Page[forgeapi.Issue]) IssueList {
	return IssueList{
		Partial: partialOf(page.Partial), Successor: successorOf(page.Successor),
		Next: string(page.Next), Issues: rowsOf(page.Items, issueWire),
	}
}

func checkWire(c *forgeapi.CheckContext) Check {
	return Check{Name: c.Name, State: c.State.String(), Description: c.Description, URL: c.TargetURL}
}

func commitChecksWire(c *forgeapi.CommitChecks) CommitChecks {
	return CommitChecks{
		Partial: partialOf(c.Partial), Successor: successorOf(c.Successor),
		State: c.State.String(), Checks: rowsOf(c.Contexts, checkWire),
		ChecksPassing: c.Passing, ChecksFailing: c.Failing, ChecksPending: c.Pending,
		ChecksNeutral: c.Neutral, ChecksUnknown: c.Unknown, ChecksTotal: c.Total,
	}
}

func releaseWire(r *forgeapi.Release) Release {
	return Release{
		TagName: r.TagName, Name: r.Name, Body: r.Body, URL: r.WebURL,
		PublishedAt: unixMilli(r.PublishedAt), Draft: r.Draft, Prerelease: r.Prerelease,
	}
}

func releaseListWire(page *forgeapi.Page[forgeapi.Release]) ReleaseList {
	return ReleaseList{
		Partial: partialOf(page.Partial), Successor: successorOf(page.Successor),
		Next: string(page.Next), Releases: rowsOf(page.Items, releaseWire),
	}
}

func labelWire(l *forgeapi.Label) Label {
	return Label{Name: l.Name, Color: l.Color, Description: l.Description}
}

func labelListWire(page *forgeapi.Page[forgeapi.Label]) LabelList {
	return LabelList{
		Partial: partialOf(page.Partial), Successor: successorOf(page.Successor),
		Next: string(page.Next), Labels: rowsOf(page.Items, labelWire),
	}
}
