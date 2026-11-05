package forges

// The view-gated fill: while a client shows the pull-request view, a present
// cycle reads at most ten rows of a connection one by one, pending first, then
// never filled, then moved, then settled rows longest unread first, and every
// such row says per field why it holds the value it does.

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/go/slogx/capture"
)

type fillCore struct {
	*scopeCore
	reads map[string]forgeapi.PullRequest
	errs  map[string]error
	asked []string
}

func newFillCore() *fillCore {
	return &fillCore{scopeCore: newScopeCore(), reads: map[string]forgeapi.PullRequest{}, errs: map[string]error{}}
}

func (c *fillCore) ReadPR(_ context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef) (forgeapi.PullRequest, error) {
	key := readKey(repo.Selector, pr.Number)
	c.asked = append(c.asked, key)
	if err := c.errs[key]; err != nil {
		return forgeapi.PullRequest{}, err
	}
	got, ok := c.reads[key]
	if !ok {
		return forgeapi.PullRequest{}, &forgeapi.Error{Kind: forgeapi.KindNotFound}
	}
	return got, nil
}

func readKey(selector string, number int) string { return selector + "#" + strconv.Itoa(number) }

var fillBase = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

func listed(family forgeapi.Family, selector string, number, minute int) forgeapi.PullRequest {
	pr := prIn(family, selector, number, forgeapi.CheckUnknown)
	pr.UpdatedAt = fillBase.Add(time.Duration(minute) * time.Minute)
	return pr
}

// gitlabRead is GitLab's ReadPR of pr: one scalar pipeline status, no counts, and
// the merge state its list rows lack.
func gitlabRead(pr forgeapi.PullRequest, check forgeapi.CheckState) forgeapi.PullRequest {
	pr.Action.Checks = check
	pr.Action.Mergeable = forgeapi.SupportYes
	pr.Action.MergeBlocked = forgeapi.MergeBlockNone
	pr.Action.AutoMergeArmed = forgeapi.SupportNo
	return pr
}

// giteaRead is the Gitea family's ReadPR of pr: the branches, the head, a complete
// fold with counts and mergeable, and still no block reason or auto-merge.
func giteaRead(pr forgeapi.PullRequest, check forgeapi.CheckState, head string) forgeapi.PullRequest {
	pr.SourceBranch, pr.TargetBranch, pr.HeadSHA = "feature", "main", head
	pr.Action.Checks = check
	pr.Action.ChecksPassing, pr.Action.ChecksTotal = 2, 2
	pr.Action.Mergeable = forgeapi.SupportYes
	return pr
}

const viewerTag = "tab-a"

func fillPoller(t *testing.T, cores map[string]forgeapi.Core, origins []RepoOrigin, recs ...connectionSpec,
) (*PRStatusPoller, *fakeGate, *Manager) {
	t.Helper()
	m := sourceManager(t, cores, recs...)
	g := &fakeGate{present: true}
	src := NewManagerPRSource(m, fixedOrigins(origins...))
	return NewPRStatusPoller(src, &fakeNotifier{}, g.Open, WithViewers(newLivePresence(viewerTag))), g, m
}

func rowIn(t *testing.T, e *InventoryEntry, number int) PR {
	t.Helper()
	for i := range e.Scopes {
		for _, row := range e.Scopes[i].Rows {
			if row.Number == number {
				return row
			}
		}
	}
	t.Fatalf("no row #%d in entry %s", number, e.ForgeID)
	return PR{}
}

func fillsOf(row *PR) map[string]FieldFill {
	out := make(map[string]FieldFill, len(row.Fill))
	for _, f := range row.Fill {
		out[f.Field] = f
	}
	return out
}

func gitlabFills(reason string, asOf int64) map[string]FieldFill {
	return map[string]FieldFill{
		"checks":        {Field: "checks", Reason: reason, AsOf: asOf},
		"mergeable":     {Field: "mergeable", Reason: reason, AsOf: asOf},
		"merge_blocked": {Field: "merge_blocked", Reason: reason, AsOf: asOf},
		"check_counts":  {Field: "check_counts", Reason: "not_supplied"},
	}
}

func asOfOf(row *PR) int64 { return fillsOf(row)["checks"].AsOf }

func unfilledBy(logs *capture.Recorder, msg string) map[string]string {
	out := map[string]string{}
	for _, r := range logs.Records() {
		if r.Message != msg {
			continue
		}
		var forge, unfilled string
		r.Attrs(func(a slog.Attr) bool {
			switch a.Key {
			case "forge":
				forge = a.Value.String()
			case "unfilled":
				unfilled = a.Value.String()
			}
			return true
		})
		out[forge] = unfilled
	}
	return out
}

// Without a viewer only the notice reads, and it reads only the rows it tracks:
// a row in a repository no clone tracks stays as its list answered it.
func TestFill_NoViewerReadsNoUntrackedRow(t *testing.T) {
	core := newFillCore()
	pr := listed(forgeapi.FamilyGitLab, "group/app", 1, 0)
	core.pages[""] = page(pr)
	core.reads[readKey("group/app", 1)] = gitlabRead(pr, forgeapi.CheckPassing)
	rec := gitlabRecord()
	clone := RepoOrigin{Dir: "lib", WebBase: "https://gitlab.com", Slug: "group/lib"}
	p, g, _ := fillPoller(t, map[string]forgeapi.Core{rec.ID: core}, []RepoOrigin{clone}, rec)

	p.sweep(t.Context())

	if len(core.asked) != 0 {
		t.Errorf("a present cycle with no viewer read %q, want no read", core.asked)
	}
	e := entryFor(t, p, rec.ID)
	row := rowIn(t, &e, 1)
	if got, want := fillsOf(&row), gitlabFills("not_on_list", 0); !maps.Equal(got, want) {
		t.Errorf("unread GitLab row fill = %+v, want %+v", got, want)
	}
	if row.Action.Checks != "unknown" || row.Action.Mergeable != "unknown" {
		t.Errorf("unread GitLab row checks %q, mergeable %q; want the list's unknown for both",
			row.Action.Checks, row.Action.Mergeable)
	}

	p.viewers.watch(viewerTag, "", true)
	g.present, g.push = false, true
	p.sweep(t.Context())
	if len(core.asked) != 0 {
		t.Errorf("a push-only cycle with a viewer read %q, want no read", core.asked)
	}
}

func TestFill_BoundIsTenPerConnectionPerCycle(t *testing.T) {
	logs := capture.Default(t)
	gl, gt, cb := newFillCore(), newFillCore(), newFillCore()
	var glRows, gtOwner, gtAuthored, cbRows []forgeapi.PullRequest
	for n := 1; n <= 13; n++ {
		pr := listed(forgeapi.FamilyGitLab, "group/app", n, n)
		glRows = append(glRows, pr)
		gl.reads[readKey("group/app", n)] = gitlabRead(pr, forgeapi.CheckPassing)
	}
	// The Gitea rows alternate between the owner and authored scopes, so only a
	// read order across scopes reads the newest first.
	for n := 1; n <= 12; n++ {
		pr := listed(forgeapi.FamilyGitea, "bob/app", n, n)
		if n%2 == 0 {
			gtOwner = append(gtOwner, pr)
		} else {
			gtAuthored = append(gtAuthored, pr)
		}
		gt.reads[readKey("bob/app", n)] = giteaRead(pr, forgeapi.CheckPassing, "abc")
	}
	// Codeberg has exactly the bound due, so it leaves none.
	for n := 1; n <= 10; n++ {
		pr := listed(forgeapi.FamilyGitea, "carol/site", n, n)
		cbRows = append(cbRows, pr)
		cb.reads[readKey("carol/site", n)] = giteaRead(pr, forgeapi.CheckPassing, "abc")
	}
	gl.pages[""], gt.pages["bob"], gt.pages[""], cb.pages[""] = page(glRows...), page(gtOwner...), page(gtAuthored...), page(cbRows...)
	glRec, gtRec := gitlabRecord(), localGiteaRecord()
	cbRec := connectionSpec{SignIn: testSignIn, ID: "codeberg:codeberg.org", Kind: KindCodeberg, Host: "codeberg.org"}
	cores := map[string]forgeapi.Core{glRec.ID: gl, gtRec.ID: gt, cbRec.ID: cb}
	p, _, _ := fillPoller(t, cores, nil, glRec, gtRec, cbRec)
	p.viewers.watch(viewerTag, "", true)

	p.sweep(t.Context())

	newest := func(sel string, from int) []string {
		out := make([]string, 0, 10)
		for n := from; n > from-10; n-- {
			out = append(out, readKey(sel, n))
		}
		return out
	}
	if want := newest("group/app", 13); !slices.Equal(gl.asked, want) {
		t.Errorf("one viewed cycle read GitLab rows %q, want the ten newest %q", gl.asked, want)
	}
	if want := newest("bob/app", 12); !slices.Equal(gt.asked, want) {
		t.Errorf("one viewed cycle read Gitea rows %q, want the ten newest across both scopes %q", gt.asked, want)
	}
	if want := newest("carol/site", 10); !slices.Equal(cb.asked, want) {
		t.Errorf("one viewed cycle read Codeberg rows %q, want all ten %q", cb.asked, want)
	}
	const msg = "pr fill: the bound left rows unfilled"
	if got, want := unfilledBy(logs, msg), map[string]string{glRec.ID: "3", gtRec.ID: "2"}; !maps.Equal(got, want) {
		t.Errorf("unfilled counts per connection = %v, want %v and none for a connection the bound covers", got, want)
	}

	// The second cycle reads the rows the first left, then settled rows up to
	// the bound; the third starts with the settled rows the second left.
	p.sweep(t.Context())
	if len(gl.asked) < 10 || len(gt.asked) < 10 || len(cb.asked) < 10 {
		t.Fatalf("Setup: the first viewed cycle read %d GitLab, %d Gitea and %d Codeberg rows, want ten each",
			len(gl.asked), len(gt.asked), len(cb.asked))
	}
	second := map[string][]string{
		"GitLab":   gl.asked[10:],
		"Gitea":    gt.asked[10:],
		"Codeberg": cb.asked[10:],
	}
	wantSecond := map[string][]string{
		"GitLab":   slices.Concat(newest("group/app", 3)[:3], newest("group/app", 13)[:7]),
		"Gitea":    slices.Concat(newest("bob/app", 2)[:2], newest("bob/app", 12)[:8]),
		"Codeberg": newest("carol/site", 10),
	}
	for name, want := range wantSecond {
		if !slices.Equal(second[name], want) {
			t.Errorf("second viewed cycle read %s rows %q, want the unfilled rest, then settled rows newest first %q",
				name, second[name], want)
		}
	}
	p.sweep(t.Context())
	if len(gl.asked) != 30 {
		t.Fatalf("three viewed cycles read %d GitLab rows, want 30: the bound each cycle", len(gl.asked))
	}
	if got, want := gl.asked[20:23], newest("group/app", 6)[:3]; !slices.Equal(got, want) {
		t.Errorf("third viewed cycle began with GitLab rows %q, want %q, the settled rows unread since the first cycle", got, want)
	}
	if n := logs.CountExact(msg); n != 2 {
		t.Errorf("%d unfilled lines after cycles that left only settled rows, want the first cycle's 2", n)
	}
}

func TestFill_OrderIsPendingThenUnfilledThenMoved(t *testing.T) {
	const sel = "group/app"
	a, b, c := listed(forgeapi.FamilyGitLab, sel, 1, 1), listed(forgeapi.FamilyGitLab, sel, 2, 3), listed(forgeapi.FamilyGitLab, sel, 3, 0)
	failed := listed(forgeapi.FamilyGitLab, sel, 5, 6)
	core := newFillCore()
	core.pages[""] = page(a, b, c, failed)
	core.reads[readKey(sel, 1)] = gitlabRead(a, forgeapi.CheckPending)
	core.reads[readKey(sel, 2)] = gitlabRead(b, forgeapi.CheckPassing)
	core.reads[readKey(sel, 3)] = gitlabRead(c, forgeapi.CheckPassing)
	core.errs[readKey(sel, 5)] = &forgeapi.Error{Kind: forgeapi.KindTransient}
	rec := gitlabRecord()
	p, _, _ := fillPoller(t, map[string]forgeapi.Core{rec.ID: core}, nil, rec)
	p.viewers.watch(viewerTag, "", true)
	p.sweep(t.Context())
	if len(core.asked) != 4 {
		t.Fatalf("Setup: the first viewed cycle read %q, want all four rows", core.asked)
	}

	// Newest first, the list is #5 (its read failed), #2 (moved), #4 (never
	// filled), #1 (pending), #3 (settled).
	moved, fresh := listed(forgeapi.FamilyGitLab, sel, 2, 5), listed(forgeapi.FamilyGitLab, sel, 4, 2)
	core.pages[""] = page(a, moved, c, fresh, failed)
	core.reads[readKey(sel, 2)] = gitlabRead(moved, forgeapi.CheckPassing)
	core.reads[readKey(sel, 4)] = gitlabRead(fresh, forgeapi.CheckPassing)
	core.asked = nil
	p.sweep(t.Context())

	want := []string{readKey(sel, 1), readKey(sel, 5), readKey(sel, 4), readKey(sel, 2), readKey(sel, 3)}
	if !slices.Equal(core.asked, want) {
		t.Errorf("second cycle reads = %q, want pending #1, then unfilled #5 and #4 newest first, then moved #2, "+
			"then settled #3: %q", core.asked, want)
	}
}

// A re-run restarts the checks without moving updated_at, so the cycle after a
// refill reads the row with the unfilled ones, before the settled rows.
func TestFill_ARefilledRowIsReadFirst(t *testing.T) {
	const sel = "group/app"
	core := newFillCore()
	older, newer := listed(forgeapi.FamilyGitLab, sel, 1, 0), listed(forgeapi.FamilyGitLab, sel, 2, 1)
	core.pages[""] = page(newer, older)
	core.reads[readKey(sel, 1)] = gitlabRead(older, forgeapi.CheckFailing)
	core.reads[readKey(sel, 2)] = gitlabRead(newer, forgeapi.CheckPassing)
	rec := gitlabRecord()
	p, _, _ := fillPoller(t, map[string]forgeapi.Core{rec.ID: core}, nil, rec)
	p.viewers.watch(viewerTag, "", true)
	p.sweep(t.Context())
	e := entryFor(t, p, rec.ID)
	first := rowIn(t, &e, 1)

	core.reads[readKey(sel, 1)] = gitlabRead(older, forgeapi.CheckPending)
	core.asked = nil
	p.refill(rec.ID, first.RepoID, first.Number)
	p.sweep(t.Context())

	if want := []string{readKey(sel, 1), readKey(sel, 2)}; !slices.Equal(core.asked, want) {
		t.Errorf("reads after a refill = %q, want the refilled #1 before the newer settled #2: %q", core.asked, want)
	}
	e = entryFor(t, p, rec.ID)
	if row := rowIn(t, &e, 1); row.Action.Checks != "pending" {
		t.Errorf("row checks after the refill's cycle = %q, want the new read's pending", row.Action.Checks)
	}
}

// A moved base branch changes mergeability without moving updated_at, so a
// viewed cycle reads a settled row again. An unviewed cycle does not, and lays
// the last read.
func TestFill_AViewedSettledRowIsReadAgain(t *testing.T) {
	cases := []struct {
		read   func(forgeapi.PullRequest) forgeapi.PullRequest
		rec    connectionSpec
		name   string
		sel    string
		family forgeapi.Family
	}{
		{name: "GitHub", family: forgeapi.FamilyGitHub, rec: githubRecord(), sel: "bob/app", read: githubRead},
		{
			name: "GitLab", family: forgeapi.FamilyGitLab, rec: gitlabRecord(), sel: "group/app",
			read: func(pr forgeapi.PullRequest) forgeapi.PullRequest { return gitlabRead(pr, forgeapi.CheckPassing) },
		},
		{
			name: "Gitea", family: forgeapi.FamilyGitea, rec: localGiteaRecord(), sel: "bob/app",
			read: func(pr forgeapi.PullRequest) forgeapi.PullRequest { return giteaRead(pr, forgeapi.CheckPassing, "abc") },
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			core := newFillCore()
			pr := listed(c.family, c.sel, 1, 0)
			key := readKey(c.sel, 1)
			core.pages[""] = page(pr)
			core.reads[key] = c.read(pr)
			p, _, _ := fillPoller(t, map[string]forgeapi.Core{c.rec.ID: core}, nil, c.rec)
			p.viewers.watch(viewerTag, "", true)
			p.sweep(t.Context())

			conflicted := c.read(pr)
			conflicted.Action.Mergeable = forgeapi.SupportNo
			core.reads[key] = conflicted
			before := time.Now().UnixMilli()
			p.sweep(t.Context())

			if want := []string{key, key}; !slices.Equal(core.asked, want) {
				t.Errorf("reads over two viewed cycles of an unchanged row = %q, want %q", core.asked, want)
			}
			e := entryFor(t, p, c.rec.ID)
			row := rowIn(t, &e, 1)
			asOf := fillsOf(&row)["mergeable"].AsOf
			if row.Action.Mergeable != "no" || asOf < before {
				t.Errorf("after the second viewed cycle: mergeable %q, as_of %d; want the new read's no, at or after %d",
					row.Action.Mergeable, asOf, before)
			}

			p.viewers.watch(viewerTag, "", false)
			p.sweep(t.Context())

			if len(core.asked) != 2 {
				t.Errorf("reads after an unviewed cycle = %q, want the two viewed reads only", core.asked)
			}
			e = entryFor(t, p, c.rec.ID)
			row = rowIn(t, &e, 1)
			if got := fillsOf(&row)["mergeable"]; got.Reason != "filled" || got.AsOf != asOf || row.Action.Mergeable != "no" {
				t.Errorf("unviewed cycle: mergeable %q, fill %+v; want the last read's no, filled as of %d",
					row.Action.Mergeable, got, asOf)
			}
		})
	}
}

// GitHub and GitLab answer merge state unknown while they compute it, so a
// viewed cycle reads such a row before the settled rows. A field the family's
// read never supplies is unknown on every read and does not count, and an
// unviewed cycle, whose notice reads no merge state, does not rank by it.
func TestFill_AComputingMergeStateIsReadFirstWhileViewed(t *testing.T) {
	gitlabClone := RepoOrigin{Dir: "app", WebBase: "https://gitlab.com", Slug: "group/app"}
	cases := []struct {
		read        func(forgeapi.PullRequest) forgeapi.PullRequest
		computing   func(*forgeapi.ActionState)
		rec         connectionSpec
		name        string
		sel         string
		wantBlocked string
		origins     []RepoOrigin
		family      forgeapi.Family
		viewed      bool
	}{
		{
			name: "GitHubMergeable", family: forgeapi.FamilyGitHub, rec: githubRecord(), sel: "bob/app", viewed: true,
			read:        githubRead,
			computing:   func(a *forgeapi.ActionState) { a.Mergeable = forgeapi.SupportUnknown },
			wantBlocked: "none",
		},
		{
			name: "GitLabBlockReason", family: forgeapi.FamilyGitLab, rec: gitlabRecord(), sel: "group/app", viewed: true,
			read:        func(pr forgeapi.PullRequest) forgeapi.PullRequest { return gitlabRead(pr, forgeapi.CheckPassing) },
			computing:   func(a *forgeapi.ActionState) { a.MergeBlocked = forgeapi.MergeBlockUnknown },
			wantBlocked: "none",
		},
		{
			name: "GiteaMergeable", family: forgeapi.FamilyGitea, rec: localGiteaRecord(), sel: "bob/app", viewed: true,
			read:        func(pr forgeapi.PullRequest) forgeapi.PullRequest { return giteaRead(pr, forgeapi.CheckPassing, "abc") },
			computing:   func(a *forgeapi.ActionState) { a.Mergeable = forgeapi.SupportUnknown },
			wantBlocked: "unknown",
		},
		{
			name: "GitLabUnviewed", family: forgeapi.FamilyGitLab, rec: gitlabRecord(), sel: "group/app",
			origins:     []RepoOrigin{gitlabClone},
			read:        func(pr forgeapi.PullRequest) forgeapi.PullRequest { return gitlabRead(pr, forgeapi.CheckPassing) },
			computing:   func(a *forgeapi.ActionState) { a.MergeBlocked = forgeapi.MergeBlockUnknown },
			wantBlocked: "none",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			core := newFillCore()
			older, newer := listed(c.family, c.sel, 1, 0), listed(c.family, c.sel, 2, 1)
			olderKey, newerKey := readKey(c.sel, 1), readKey(c.sel, 2)
			core.pages[""] = page(newer, older)
			computing := c.read(older)
			c.computing(&computing.Action)
			core.reads[olderKey], core.reads[newerKey] = computing, c.read(newer)
			p, _, _ := fillPoller(t, map[string]forgeapi.Core{c.rec.ID: core}, c.origins, c.rec)
			if c.viewed {
				p.viewers.watch(viewerTag, "", true)
			}
			p.sweep(t.Context())
			if want := []string{newerKey, olderKey}; !slices.Equal(core.asked, want) {
				t.Fatalf("Setup: the first cycle read %q, want both rows newest first %q", core.asked, want)
			}

			core.reads[olderKey] = c.read(older)
			core.asked = nil
			p.sweep(t.Context())

			want := []string{newerKey, olderKey}
			if c.viewed {
				want = []string{olderKey, newerKey}
			}
			if !slices.Equal(core.asked, want) {
				t.Errorf("second cycle reads = %q, want %q", core.asked, want)
			}
			e := entryFor(t, p, c.rec.ID)
			if row := rowIn(t, &e, 1); row.Action.Mergeable != "yes" || row.Action.MergeBlocked != c.wantBlocked {
				t.Errorf("row #1 after its second read: mergeable %q, blocked %q; want yes and %q",
					row.Action.Mergeable, row.Action.MergeBlocked, c.wantBlocked)
			}
		})
	}
}

// A read newer than its list already saw the update the next list shows, so
// the next cycle ranks that row settled, after a row that did move.
func TestFill_AReadNewerThanItsListIsNotMoved(t *testing.T) {
	const sel = "group/app"
	core := newFillCore()
	stale, current := listed(forgeapi.FamilyGitLab, sel, 1, 0), listed(forgeapi.FamilyGitLab, sel, 1, 5)
	before, moved := listed(forgeapi.FamilyGitLab, sel, 2, 1), listed(forgeapi.FamilyGitLab, sel, 2, 3)
	core.pages[""] = page(before, stale)
	core.reads[readKey(sel, 1)] = gitlabRead(current, forgeapi.CheckPassing)
	core.reads[readKey(sel, 2)] = gitlabRead(before, forgeapi.CheckPassing)
	rec := gitlabRecord()
	p, _, _ := fillPoller(t, map[string]forgeapi.Core{rec.ID: core}, nil, rec)
	p.viewers.watch(viewerTag, "", true)
	p.sweep(t.Context())

	core.pages[""] = page(current, moved)
	core.reads[readKey(sel, 2)] = gitlabRead(moved, forgeapi.CheckPassing)
	core.asked = nil
	p.sweep(t.Context())

	if want := []string{readKey(sel, 2), readKey(sel, 1)}; !slices.Equal(core.asked, want) {
		t.Errorf("reads = %q, want moved #2 before #1, whose first read already saw its update: %q", core.asked, want)
	}
}

func TestFill_FailedReadIsUnread(t *testing.T) {
	const sel = "group/app"
	core := newFillCore()
	pr := listed(forgeapi.FamilyGitLab, sel, 1, 0)
	core.pages[""] = page(pr)
	core.errs[readKey(sel, 1)] = &forgeapi.Error{Kind: forgeapi.KindTransient}
	rec := gitlabRecord()
	p, _, _ := fillPoller(t, map[string]forgeapi.Core{rec.ID: core}, nil, rec)
	p.viewers.watch(viewerTag, "", true)
	unread := func(when string) {
		t.Helper()
		e := entryFor(t, p, rec.ID)
		row := rowIn(t, &e, 1)
		if got, want := fillsOf(&row), gitlabFills("unread", 0); !maps.Equal(got, want) {
			t.Errorf("%s: fill = %+v, want %+v", when, got, want)
		}
		if row.Action.Checks != "unknown" {
			t.Errorf("%s: checks %q, want the list's unknown", when, row.Action.Checks)
		}
	}

	p.sweep(t.Context())
	unread("a failed first read")

	delete(core.errs, readKey(sel, 1))
	core.reads[readKey(sel, 1)] = gitlabRead(pr, forgeapi.CheckPending)
	p.sweep(t.Context())
	e := entryFor(t, p, rec.ID)
	if row := rowIn(t, &e, 1); row.Action.Checks != "pending" || fillsOf(&row)["checks"].Reason != "filled" {
		t.Errorf("after a read that answered: checks %q, fill %+v; want pending and filled", row.Action.Checks, row.Fill)
	}

	core.errs[readKey(sel, 1)] = &forgeapi.Error{Kind: forgeapi.KindTransient}
	p.sweep(t.Context())
	unread("a failed read of a row filled before")
	if len(core.asked) != 3 {
		t.Errorf("reads over three cycles = %q, want three: a failed and a pending row are read again", core.asked)
	}
}

func TestFill_GitLabCountsAreNotSupplied(t *testing.T) {
	core := newFillCore()
	pr := listed(forgeapi.FamilyGitLab, "group/app", 1, 0)
	core.pages[""] = page(pr)
	core.reads[readKey("group/app", 1)] = gitlabRead(pr, forgeapi.CheckPassing)
	rec := gitlabRecord()
	p, _, _ := fillPoller(t, map[string]forgeapi.Core{rec.ID: core}, nil, rec)
	p.viewers.watch(viewerTag, "", true)

	p.sweep(t.Context())

	e := entryFor(t, p, rec.ID)
	row := rowIn(t, &e, 1)
	if got, want := fillsOf(&row), gitlabFills("filled", asOfOf(&row)); !maps.Equal(got, want) || asOfOf(&row) <= 0 {
		t.Errorf("filled GitLab row fill = %+v, want %+v with a read time", got, want)
	}
	if row.Action.Checks != "passing" || row.Action.ChecksTotal != 0 || row.Action.ChecksPassing != 0 {
		t.Errorf("filled GitLab row checks %q, total %d, passing %d; want the pipeline's passing and no counts",
			row.Action.Checks, row.Action.ChecksTotal, row.Action.ChecksPassing)
	}
}

func TestFill_GiteaBlockReasonIsNotSupplied(t *testing.T) {
	core := newFillCore()
	one, two := listed(forgeapi.FamilyGitea, "bob/app", 1, 1), listed(forgeapi.FamilyGitea, "bob/app", 2, 0)
	core.pages[""] = page(one, two)
	core.reads[readKey("bob/app", 1)] = giteaRead(one, forgeapi.CheckPassing, "abc123")
	capped := giteaRead(two, forgeapi.CheckUnknown, "def456")
	capped.Partial = &forgeapi.Partial{Reason: forgeapi.PartialPaginationCap, Fetched: 100, OmittedAtLeast: 1}
	core.reads[readKey("bob/app", 2)] = capped
	rec := localGiteaRecord()
	p, _, _ := fillPoller(t, map[string]forgeapi.Core{rec.ID: core}, nil, rec)
	p.viewers.watch(viewerTag, "", true)

	p.sweep(t.Context())

	e := entryFor(t, p, rec.ID)
	row := rowIn(t, &e, 1)
	asOf := asOfOf(&row)
	want := map[string]FieldFill{
		"checks":           {Field: "checks", Reason: "filled", AsOf: asOf},
		"check_counts":     {Field: "check_counts", Reason: "filled", AsOf: asOf},
		"mergeable":        {Field: "mergeable", Reason: "filled", AsOf: asOf},
		"head_sha":         {Field: "head_sha", Reason: "filled", AsOf: asOf},
		"source_branch":    {Field: "source_branch", Reason: "filled", AsOf: asOf},
		"target_branch":    {Field: "target_branch", Reason: "filled", AsOf: asOf},
		"merge_blocked":    {Field: "merge_blocked", Reason: "not_supplied"},
		"auto_merge_armed": {Field: "auto_merge_armed", Reason: "not_supplied"},
	}
	if got := fillsOf(&row); !maps.Equal(got, want) || asOf <= 0 {
		t.Errorf("filled Gitea row fill = %+v, want %+v with a read time", got, want)
	}
	gotFill := struct {
		head, source, target, checks, mergeable, blocked, auto string
		passing, total                                         int
	}{
		row.HeadSHA, row.SourceBranch, row.TargetBranch, row.Action.Checks, row.Action.Mergeable,
		row.Action.MergeBlocked, row.Action.AutoMergeArmed, row.Action.ChecksPassing, row.Action.ChecksTotal,
	}
	wantFill := gotFill
	wantFill.head, wantFill.source, wantFill.target, wantFill.checks, wantFill.mergeable = "abc123", "feature", "main", "passing", "yes"
	wantFill.blocked, wantFill.auto, wantFill.passing, wantFill.total = "unknown", "unknown", 2, 2
	if gotFill != wantFill {
		t.Errorf("filled Gitea row = %+v, want %+v", gotFill, wantFill)
	}
	if row.Partial != nil {
		t.Errorf("a complete fold's row partial = %+v, want none", row.Partial)
	}
	row = rowIn(t, &e, 2)
	if row.Partial == nil || row.Partial.Reason != "pagination_cap" || row.Action.Checks != "unknown" {
		t.Errorf("a fold past its page bound: partial %+v, checks %q; want pagination_cap and unknown", row.Partial, row.Action.Checks)
	}
}

// githubListed is a GitHub ListMyPRs row under WithMergeState(false): its checks
// and head, and merge state unknown.
func githubListed(selector string, number int) forgeapi.PullRequest {
	pr := listed(forgeapi.FamilyGitHub, selector, number, 0)
	pr.Action.Checks = forgeapi.CheckPending
	pr.HeadSHA = "abc123"
	return pr
}

// githubRead is GitHub's ReadPR of pr: the merge state its list leaves out.
func githubRead(pr forgeapi.PullRequest) forgeapi.PullRequest {
	pr.Action.Checks = forgeapi.CheckPassing
	pr.Action.Mergeable = forgeapi.SupportYes
	pr.Action.MergeBlocked = forgeapi.MergeBlockNone
	return pr
}

func githubFills(reason string, asOf int64) map[string]FieldFill {
	return map[string]FieldFill{
		"mergeable":     {Field: "mergeable", Reason: reason, AsOf: asOf},
		"merge_blocked": {Field: "merge_blocked", Reason: reason, AsOf: asOf},
	}
}

func TestFill_GitHubMergeStateComesFromTheRead(t *testing.T) {
	core := newFillCore()
	pr := githubListed("bob/app", 1)
	core.pages["bob"] = page(pr)
	core.reads[readKey("bob/app", 1)] = githubRead(pr)
	rec := githubRecord()
	p, _, _ := fillPoller(t, map[string]forgeapi.Core{rec.ID: core}, nil, rec)

	p.sweep(t.Context())

	e := entryFor(t, p, rec.ID)
	row := rowIn(t, &e, 1)
	if got, want := fillsOf(&row), githubFills("not_on_list", 0); !maps.Equal(got, want) {
		t.Errorf("unviewed GitHub row fill = %+v, want %+v", got, want)
	}
	if row.Action.Mergeable != "unknown" || row.Action.MergeBlocked != "unknown" {
		t.Errorf("unviewed GitHub row mergeable %q, blocked %q; want the list's unknown for both",
			row.Action.Mergeable, row.Action.MergeBlocked)
	}

	p.viewers.watch(viewerTag, "", true)
	p.sweep(t.Context())

	if want := []string{readKey("bob/app", 1)}; !slices.Equal(core.asked, want) {
		t.Errorf("a viewed cycle read GitHub rows %q, want %q", core.asked, want)
	}
	e = entryFor(t, p, rec.ID)
	row = rowIn(t, &e, 1)
	if got, want := fillsOf(&row), githubFills("filled", fillsOf(&row)["mergeable"].AsOf); !maps.Equal(got, want) ||
		want["mergeable"].AsOf <= 0 {
		t.Errorf("viewed GitHub row fill = %+v, want %+v with a read time", got, want)
	}
	if row.Action.Mergeable != "yes" || row.Action.MergeBlocked != "none" {
		t.Errorf("viewed GitHub row mergeable %q, blocked %q; want the read's yes and none",
			row.Action.Mergeable, row.Action.MergeBlocked)
	}
	if row.Action.Checks != "pending" || row.HeadSHA != "abc123" {
		t.Errorf("viewed GitHub row checks %q, head %q; want the list's pending and abc123", row.Action.Checks, row.HeadSHA)
	}
}

// GitHub's list carries the checks the notice compares, so only a viewer's
// cycle reads a GitHub row.
func TestFill_GitHubIsNotReadForTheNotice(t *testing.T) {
	core := newFillCore()
	pr := githubListed("bob/app", 1)
	core.pages[""] = page(pr)
	core.reads[readKey("bob/app", 1)] = githubRead(pr)
	rec := githubRecord()
	clone := RepoOrigin{Dir: "app", WebBase: "https://github.com", Slug: "bob/app"}
	p, g, _ := fillPoller(t, map[string]forgeapi.Core{rec.ID: core}, []RepoOrigin{clone}, rec)

	p.sweep(t.Context())
	g.present, g.push = false, true
	p.sweep(t.Context())

	if len(core.asked) != 0 {
		t.Errorf("an unviewed present cycle and a push-only cycle read %q, want no read", core.asked)
	}
	if got := slices.Sorted(maps.Keys(p.seen)); !slices.Equal(got, []string{subjectKey(rec.ID, "bob/app", 1)}) {
		t.Errorf("tracked subjects = %q, want the watched row: the setup must make the notice watch it", got)
	}
}

func TestFill_ARowInTwoScopesIsReadOnce(t *testing.T) {
	core := newFillCore()
	pr := listed(forgeapi.FamilyGitea, "bob/app", 1, 0)
	core.pages["bob"], core.pages[""] = page(pr), page(pr)
	core.reads[readKey("bob/app", 1)] = giteaRead(pr, forgeapi.CheckPassing, "abc123")
	rec := localGiteaRecord()
	p, _, _ := fillPoller(t, map[string]forgeapi.Core{rec.ID: core}, nil, rec)
	p.viewers.watch(viewerTag, "", true)

	p.sweep(t.Context())

	if len(core.asked) != 1 {
		t.Errorf("reads of a row the owner and authored scopes both list = %q, want one", core.asked)
	}
	e := entryFor(t, p, rec.ID)
	for i := range e.Scopes {
		for _, row := range e.Scopes[i].Rows {
			if row.HeadSHA != "abc123" || fillsOf(&row)["head_sha"].Reason != "filled" {
				t.Errorf("%s row head %q, fill %+v; want the one read's abc123, filled", e.Scopes[i].Scope, row.HeadSHA, row.Fill)
			}
		}
	}
}

// A fill forgotten with its row, its gate or its connection is not laid again:
// the row says its list holds the fields until a read answers.
func TestFill_AForgottenFillIsNotLaid(t *testing.T) {
	setup := func(t *testing.T) (*PRStatusPoller, *fakeGate, *Manager, *fillCore, connectionSpec) {
		t.Helper()
		core := newFillCore()
		pr := listed(forgeapi.FamilyGitLab, "group/app", 1, 0)
		core.pages[""] = page(pr)
		core.reads[readKey("group/app", 1)] = gitlabRead(pr, forgeapi.CheckPassing)
		rec := gitlabRecord()
		p, g, m := fillPoller(t, map[string]forgeapi.Core{rec.ID: core}, nil, rec)
		p.viewers.watch(viewerTag, "", true)
		p.sweep(t.Context())
		if len(core.asked) != 1 {
			t.Fatalf("Setup: the first viewed cycle read %q, want the row", core.asked)
		}
		return p, g, m, core, rec
	}
	notLaid := func(t *testing.T, p *PRStatusPoller, rec connectionSpec) {
		t.Helper()
		p.viewers.watch(viewerTag, "", false)
		p.sweep(t.Context())
		e := entryFor(t, p, rec.ID)
		row := rowIn(t, &e, 1)
		if got, want := fillsOf(&row), gitlabFills("not_on_list", 0); !maps.Equal(got, want) {
			t.Errorf("unviewed cycle after the fill was forgotten: fill = %+v, want %+v", got, want)
		}
	}
	t.Run("RowLeft", func(t *testing.T) {
		p, _, _, core, rec := setup(t)
		listedRows := core.pages[""]
		core.pages[""] = page()
		p.sweep(t.Context())
		core.pages[""] = listedRows
		notLaid(t, p, rec)
	})
	t.Run("GateClosed", func(t *testing.T) {
		p, g, _, _, rec := setup(t)
		g.present = false
		p.sweep(t.Context())
		g.present = true
		notLaid(t, p, rec)
	})
	t.Run("Disconnected", func(t *testing.T) {
		p, _, m, _, rec := setup(t)
		dropCredential(t, m, rec.ID)
		p.sweep(t.Context())
		addConnections(t, m, "bob", rec)
		notLaid(t, p, rec)
	})
}
