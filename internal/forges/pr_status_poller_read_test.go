package forges

// The CI notice beyond GitHub: GitLab and Gitea family rows list no verdict, so
// every cycle reads the rows the notice tracks with ReadPR, through the fill's
// bounded reader, and the notice compares what those reads answered.

import (
	"slices"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/slogx/capture"
)

// noticePoller is a push-only poller over the production source for recs, each
// served by the core cores names, with the clones origins describe.
func noticePoller(t *testing.T, cores map[string]forgeapi.Core, origins []RepoOrigin, recs ...connectionRecord,
) (*PRStatusPoller, *fakeGate, *fakeNotifier) {
	t.Helper()
	p, g, n, _ := inventoryPoller(t, cores, origins, recs...)
	g.present, g.push = false, true
	return p, g, n
}

func gitlabClone(slug string) RepoOrigin {
	return RepoOrigin{Dir: "app", WebBase: "https://gitlab.com", Slug: slug}
}

func giteaClone(slug string) RepoOrigin {
	return RepoOrigin{Dir: "app", WebBase: "http://127.0.0.1:3000", Slug: slug}
}

func TestNotice_GitLabRowFlipToFailedNotifies(t *testing.T) {
	const sel = "group/app"
	core := newFillCore()
	pr := listed(forgeapi.FamilyGitLab, sel, 4, 0)
	core.pages[""] = page(pr)
	core.reads[readKey(sel, 4)] = gitlabRead(pr, forgeapi.CheckPending)
	rec := gitlabRecord()
	p, _, n := noticePoller(t, map[string]forgeapi.Core{rec.ID: core}, []RepoOrigin{gitlabClone(sel)}, rec)

	p.sweep(t.Context())
	core.reads[readKey(sel, 4)] = gitlabRead(pr, forgeapi.CheckFailing)
	p.sweep(t.Context())

	want := []sentPush{{
		title: "group/app #4", body: "Checks failed · PR group/app", kind: marotte.PushKindPRStatus,
		subject: marotte.PRSubject(rec.ID, repoIDOf(sel), 4),
	}}
	if !slices.Equal(n.sent, want) {
		t.Errorf("a GitLab row read pending then failed sent %+v, want %+v (reads %q)", n.sent, want, core.asked)
	}
}

func TestNotice_GiteaRowFlipToPassedNotifies(t *testing.T) {
	const sel = "bob/app"
	core := newFillCore()
	pr := listed(forgeapi.FamilyGitea, sel, 7, 0)
	core.pages[""] = page(pr)
	core.reads[readKey(sel, 7)] = giteaRead(pr, forgeapi.CheckPending, "abc")
	rec := localGiteaRecord()
	p, _, n := noticePoller(t, map[string]forgeapi.Core{rec.ID: core}, []RepoOrigin{giteaClone(sel)}, rec)

	p.sweep(t.Context())
	core.reads[readKey(sel, 7)] = giteaRead(pr, forgeapi.CheckPassing, "abc")
	p.sweep(t.Context())

	want := []sentPush{{
		title: "bob/app #7", body: "Checks passed · PR bob/app", kind: marotte.PushKindPRStatus,
		subject: marotte.PRSubject(rec.ID, repoIDOf(sel), 7),
	}}
	if !slices.Equal(n.sent, want) {
		t.Errorf("a Gitea row read pending then passing sent %+v, want %+v (reads %q)", n.sent, want, core.asked)
	}
}

// Without a viewer the notice reads its tracked rows: authored, in a repository
// a clone tracks as its target or as its fork source. A row another author
// opened in a tracked repository is never read. An authored row in a repository
// no clone tracks is never read where the list names its source (GitLab), and
// read once to learn it where the list names none (Gitea).
func TestNotice_ReadsOnlyTrackedRows(t *testing.T) {
	gt := newFillCore()
	mine := listed(forgeapi.FamilyGitea, "bob/app", 1, 0)
	elsewhere := listed(forgeapi.FamilyGitea, "bob/other", 2, 0)
	theirs := listed(forgeapi.FamilyGitea, "bob/app", 3, 0)
	gt.pages[""], gt.pages["bob"] = page(mine, elsewhere), page(theirs)
	for _, pr := range []forgeapi.PullRequest{mine, elsewhere, theirs} {
		gt.reads[readKey(pr.Repo.Selector, pr.Ref.Number)] = giteaRead(pr, forgeapi.CheckPending, "abc")
	}

	gl := newFillCore()
	fromFork := listed(forgeapi.FamilyGitLab, "upstream/lib", 4, 0)
	fromFork.SourceRepo = forgeapi.RepoRef{Family: forgeapi.FamilyGitLab, Selector: "group/lib", DisplayPath: "group/lib"}
	untracked := listed(forgeapi.FamilyGitLab, "other/thing", 5, 0)
	gl.pages[""] = page(fromFork, untracked)
	for _, pr := range []forgeapi.PullRequest{fromFork, untracked} {
		gl.reads[readKey(pr.Repo.Selector, pr.Ref.Number)] = gitlabRead(pr, forgeapi.CheckPending)
	}

	gtRec, glRec := localGiteaRecord(), gitlabRecord()
	p, g, _ := noticePoller(t, map[string]forgeapi.Core{gtRec.ID: gt, glRec.ID: gl},
		[]RepoOrigin{giteaClone("bob/app"), gitlabClone("group/lib")}, gtRec, glRec)

	g.present, g.push = true, true
	p.sweep(t.Context())
	if want := []string{readKey("bob/app", 1), readKey("bob/other", 2)}; !slices.Equal(gt.asked, want) {
		t.Errorf("a present cycle with no viewer read Gitea rows %q, want the tracked authored row and the one whose source "+
			"only a read names %q", gt.asked, want)
	}
	if want := []string{readKey("upstream/lib", 4)}; !slices.Equal(gl.asked, want) {
		t.Errorf("a present cycle with no viewer read GitLab rows %q, want only the row from the cloned fork %q", gl.asked, want)
	}

	g.present = false
	p.sweep(t.Context())
	if want := []string{readKey("bob/app", 1), readKey("bob/other", 2), readKey("bob/app", 1)}; !slices.Equal(gt.asked, want) {
		t.Errorf("after a push-only cycle Gitea reads %q, want the tracked pending row again and the untracked one not %q",
			gt.asked, want)
	}
	if want := []string{readKey("upstream/lib", 4), readKey("upstream/lib", 4)}; !slices.Equal(gl.asked, want) {
		t.Errorf("after a push-only cycle GitLab reads %q, want the tracked pending row again %q", gl.asked, want)
	}
}

// The bound holds for the notice's reads: a cycle reads at most fillBound
// tracked rows of a connection, pending first, then never read, then settled,
// each longest waiting then newest first, and the rest wait for the next cycle.
// A row first read in that next cycle seeds silently, since the cycle that
// listed it read no verdict.
func TestNotice_ReadsAreBoundedPendingFirst(t *testing.T) {
	const sel = "group/app"
	core := newFillCore()
	var rows []forgeapi.PullRequest
	for n := 1; n <= 12; n++ {
		pr := listed(forgeapi.FamilyGitLab, sel, n, n)
		rows = append(rows, pr)
		check := forgeapi.CheckPassing
		if n == 12 || n == 5 {
			check = forgeapi.CheckPending
		}
		core.reads[readKey(sel, n)] = gitlabRead(pr, check)
	}
	core.pages[""] = page(rows...)
	rec := gitlabRecord()
	p, _, n := noticePoller(t, map[string]forgeapi.Core{rec.ID: core}, []RepoOrigin{gitlabClone(sel)}, rec)

	p.sweep(t.Context())
	var first []string
	for k := 12; k >= 3; k-- {
		first = append(first, readKey(sel, k))
	}
	if !slices.Equal(core.asked, first) {
		t.Fatalf("the first push-only cycle read %q, want the ten newest %q", core.asked, first)
	}

	core.reads[readKey(sel, 12)] = gitlabRead(rows[11], forgeapi.CheckPassing)
	p.sweep(t.Context())
	second := []string{readKey(sel, 12), readKey(sel, 5), readKey(sel, 2), readKey(sel, 1)}
	for k := 11; k >= 6; k-- {
		second = append(second, readKey(sel, k))
	}
	if got := core.asked[len(first):]; !slices.Equal(got, second) {
		t.Errorf("the second push-only cycle read %q, want the pending rows, the two the bound left, then the newest "+
			"settled ones %q", got, second)
	}
	if got, want := sortedBodies(n.sent), []string{"group/app #12: Checks passed · PR group/app"}; !slices.Equal(got, want) {
		t.Errorf("notices after two cycles = %q, want %q: #12 flipped, #5 is still pending, #1 and #2 seed", got, want)
	}
}

// A failed read is no verdict: it sends nothing, keeps the verdict last read, is
// logged once for the cycle, and is read again on the next one.
func TestNotice_FailedReadSendsNothing(t *testing.T) {
	logs := capture.Default(t)
	const sel = "group/app"
	core := newFillCore()
	a, b := listed(forgeapi.FamilyGitLab, sel, 1, 1), listed(forgeapi.FamilyGitLab, sel, 2, 2)
	core.pages[""] = page(a, b)
	core.reads[readKey(sel, 1)] = gitlabRead(a, forgeapi.CheckPending)
	core.reads[readKey(sel, 2)] = gitlabRead(b, forgeapi.CheckPending)
	rec := gitlabRecord()
	p, _, n := noticePoller(t, map[string]forgeapi.Core{rec.ID: core}, []RepoOrigin{gitlabClone(sel)}, rec)
	const msg = "pr fill: reading pull requests failed"

	p.sweep(t.Context())
	core.errs[readKey(sel, 1)] = &forgeapi.Error{Kind: forgeapi.KindTransient}
	core.errs[readKey(sel, 2)] = &forgeapi.Error{Kind: forgeapi.KindTransient}
	p.sweep(t.Context())
	if len(n.sent) != 0 {
		t.Errorf("a cycle whose reads failed sent %+v, want nothing", n.sent)
	}
	if got := logs.CountExact(msg); got != 1 {
		t.Errorf("%d %q lines for one cycle whose two reads failed, want 1", got, msg)
	}

	clear(core.errs)
	core.reads[readKey(sel, 1)] = gitlabRead(a, forgeapi.CheckPassing)
	core.reads[readKey(sel, 2)] = gitlabRead(b, forgeapi.CheckFailing)
	p.sweep(t.Context())
	want := []string{"group/app #1: Checks passed · PR group/app", "group/app #2: Checks failed · PR group/app"}
	if got := sortedBodies(n.sent); !slices.Equal(got, want) {
		t.Errorf("after the reads answered again, notices = %q, want %q: the pending verdict held through the failure", got, want)
	}
	if len(core.asked) != 6 {
		t.Errorf("reads over three cycles = %q, want both rows each cycle", core.asked)
	}
}

// A read that still answers no verdict sends nothing, and a first read seeds
// silently whatever it answers.
func TestNotice_UnknownReadAndFirstSightingAreSilent(t *testing.T) {
	const sel = "group/app"
	core := newFillCore()
	waiting, settled := listed(forgeapi.FamilyGitLab, sel, 1, 1), listed(forgeapi.FamilyGitLab, sel, 2, 2)
	core.pages[""] = page(waiting, settled)
	core.reads[readKey(sel, 1)] = gitlabRead(waiting, forgeapi.CheckPending)
	core.reads[readKey(sel, 2)] = gitlabRead(settled, forgeapi.CheckPassing)
	rec := gitlabRecord()
	p, _, n := noticePoller(t, map[string]forgeapi.Core{rec.ID: core}, []RepoOrigin{gitlabClone(sel)}, rec)

	p.sweep(t.Context())
	if len(n.sent) != 0 || len(core.asked) != 2 {
		t.Fatalf("a first cycle sent %+v after reads %q, want nothing after reading both rows", n.sent, core.asked)
	}

	core.reads[readKey(sel, 1)] = gitlabRead(waiting, forgeapi.CheckUnknown)
	p.sweep(t.Context())
	if len(n.sent) != 0 || !slices.Contains(core.asked[2:], readKey(sel, 1)) {
		t.Errorf("a pending row read again as unknown sent %+v after reads %q, want a read and nothing sent", n.sent, core.asked)
	}

	moved := listed(forgeapi.FamilyGitLab, sel, 2, 9)
	core.pages[""] = page(waiting, moved)
	core.reads[readKey(sel, 2)] = gitlabRead(moved, forgeapi.CheckFailing)
	p.sweep(t.Context())
	if got, want := sortedBodies(n.sent), []string{"group/app #2: Checks failed · PR group/app"}; !slices.Equal(got, want) {
		t.Errorf("a row seeded passing, moved and read failing sent %q, want %q", got, want)
	}
}

// A tracked row that leaves the authored list takes its read with it, so a
// server nobody opens does not hold one read per pull request it ever tracked.
func TestNotice_ClosedRowDropsItsRead(t *testing.T) {
	const sel = "group/app"
	core := newFillCore()
	pr := listed(forgeapi.FamilyGitLab, sel, 4, 0)
	core.pages[""] = page(pr)
	core.reads[readKey(sel, 4)] = gitlabRead(pr, forgeapi.CheckPassing)
	rec := gitlabRecord()
	p, _, _ := noticePoller(t, map[string]forgeapi.Core{rec.ID: core}, []RepoOrigin{gitlabClone(sel)}, rec)

	p.sweep(t.Context())
	key := fillKey{conn: rec.ID, subject: marotte.PRSubject(rec.ID, repoIDOf(sel), 4).Key}
	if _, held := p.fills[key]; !held {
		t.Fatalf("after a push-only cycle the poller holds no read of the tracked row, want one")
	}
	core.pages[""] = page()
	p.sweep(t.Context())
	if _, held := p.fills[key]; held || len(p.seen) != 0 {
		t.Errorf("after the row left the list the poller holds its read %v and %d subjects, want neither", held, len(p.seen))
	}
}

// A re-run outside marotte moves the verdict without moving the row's
// updated_at, so a watched row is read every cycle even after it settled.
func TestNotice_ASettledRowIsReadEveryCycle(t *testing.T) {
	cases := []struct {
		read  func(forgeapi.PullRequest, forgeapi.CheckState) forgeapi.PullRequest
		rec   connectionRecord
		clone func(string) RepoOrigin
		name  string
		fam   forgeapi.Family
		sel   string
	}{
		{name: "GitLab", fam: forgeapi.FamilyGitLab, sel: "group/app", rec: gitlabRecord(), clone: gitlabClone, read: gitlabRead},
		{
			name: "Gitea", fam: forgeapi.FamilyGitea, sel: "bob/app", rec: localGiteaRecord(), clone: giteaClone,
			read: func(pr forgeapi.PullRequest, c forgeapi.CheckState) forgeapi.PullRequest {
				return giteaRead(pr, c, "abc")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			core := newFillCore()
			pr := listed(tc.fam, tc.sel, 1, 0)
			core.pages[""] = page(pr)
			core.reads[readKey(tc.sel, 1)] = tc.read(pr, forgeapi.CheckPassing)
			p, _, n := noticePoller(t, map[string]forgeapi.Core{tc.rec.ID: core}, []RepoOrigin{tc.clone(tc.sel)}, tc.rec)

			p.sweep(t.Context())
			core.reads[readKey(tc.sel, 1)] = tc.read(pr, forgeapi.CheckFailing)
			p.sweep(t.Context())

			want := []string{tc.sel + " #1: Checks failed · PR " + tc.sel}
			if got := sortedBodies(n.sent); len(core.asked) != 2 || !slices.Equal(got, want) {
				t.Errorf("two cycles over one listed row read passing then failing read %q and sent %q, want two reads and %q",
					core.asked, got, want)
			}
		})
	}
}

// A connection whose fillBound newest tracked rows stay pending still reads the
// rest: every row is read within len(rows)-fillBound+1 cycles after the first,
// while each of those cycles still reads the pending rows first.
func TestNotice_ARowBehindAFullBoundIsStillRead(t *testing.T) {
	const sel = "group/app"
	core := newFillCore()
	var rows []forgeapi.PullRequest
	for n := 1; n <= fillBound+1; n++ {
		pr := listed(forgeapi.FamilyGitLab, sel, n, n)
		rows = append(rows, pr)
		core.reads[readKey(sel, n)] = gitlabRead(pr, forgeapi.CheckPending)
	}
	core.pages[""] = page(rows...)
	rec := gitlabRecord()
	p, _, _ := noticePoller(t, map[string]forgeapi.Core{rec.ID: core}, []RepoOrigin{gitlabClone(sel)}, rec)
	p.sweep(t.Context())
	if slices.Contains(core.asked, readKey(sel, 1)) {
		t.Fatalf("Setup: the first cycle read %q, want the %d newest only", core.asked, fillBound)
	}

	oldest := readKey(sel, 1)
	for cycle := 2; cycle <= len(rows)-fillBound+2; cycle++ {
		core.asked = nil
		p.sweep(t.Context())
		pending := slices.DeleteFunc(slices.Clone(core.asked), func(k string) bool { return k == oldest })
		if len(pending) < fillBound-1 {
			t.Errorf("cycle %d read %q, want at least %d of the pending rows", cycle, core.asked, fillBound-1)
		}
		if len(pending) < len(core.asked) {
			return
		}
	}
	t.Errorf("%s was never read in the %d cycles after the first, want it read while the bound stays full of pending rows",
		oldest, len(rows)-fillBound+1)
}

// The Gitea family lists no source repository, so an authored row whose target
// no clone tracks is read once to learn its source: a row from a cloned fork is
// then watched and notifies, and a row from any other source is not read again.
func TestNotice_AGiteaPullRequestFromAClonedForkNotifies(t *testing.T) {
	core := newFillCore()
	fromFork := listed(forgeapi.FamilyGitea, "upstream/lib", 4, 1)
	elsewhere := listed(forgeapi.FamilyGitea, "carol/other", 5, 0)
	core.pages[""] = page(fromFork, elsewhere)
	readFrom := func(pr forgeapi.PullRequest, source string, check forgeapi.CheckState) forgeapi.PullRequest {
		got := giteaRead(pr, check, "abc")
		got.SourceRepo = forgeapi.RepoRef{Family: forgeapi.FamilyGitea, Selector: source, DisplayPath: source}
		return got
	}
	core.reads[readKey("upstream/lib", 4)] = readFrom(fromFork, "bob/lib", forgeapi.CheckPending)
	core.reads[readKey("carol/other", 5)] = readFrom(elsewhere, "carol/other", forgeapi.CheckPending)
	rec := localGiteaRecord()
	p, _, n := noticePoller(t, map[string]forgeapi.Core{rec.ID: core}, []RepoOrigin{giteaClone("bob/lib")}, rec)

	p.sweep(t.Context())
	p.sweep(t.Context())
	core.reads[readKey("upstream/lib", 4)] = readFrom(fromFork, "bob/lib", forgeapi.CheckPassing)
	p.sweep(t.Context())

	want := []sentPush{{
		title: "upstream/lib #4", body: "Checks passed · PR upstream/lib", kind: marotte.PushKindPRStatus,
		subject: marotte.PRSubject(rec.ID, repoIDOf("upstream/lib"), 4),
	}}
	if !slices.Equal(n.sent, want) {
		t.Errorf("a pull request from the cloned fork read pending then passing sent %+v, want %+v (reads %q)",
			n.sent, want, core.asked)
	}
	reads := map[string]int{}
	for _, k := range core.asked {
		reads[k]++
	}
	if got := reads[readKey("carol/other", 5)]; got != 1 {
		t.Errorf("an authored row whose read names an untracked source was read %d times over three cycles, want once", got)
	}

	// A failed read keeps the source the last answered one named, so the row
	// stays watched and keeps its verdict.
	core.errs[readKey("upstream/lib", 4)] = &forgeapi.Error{Kind: forgeapi.KindTransient}
	p.sweep(t.Context())
	clear(core.errs)
	core.reads[readKey("upstream/lib", 4)] = readFrom(fromFork, "bob/lib", forgeapi.CheckFailing)
	p.sweep(t.Context())
	if got, want := sortedBodies(n.sent), []string{
		"upstream/lib #4: Checks failed · PR upstream/lib", "upstream/lib #4: Checks passed · PR upstream/lib",
	}; !slices.Equal(got, want) {
		t.Errorf("after a failed read then a failing one, notices = %q, want %q", got, want)
	}
}

// The read that sorted an untracked authored row goes when the row leaves the
// list, so a server nobody opens holds no read per pull request it ever sorted.
func TestNotice_AnUntrackedRowLeavingTheListDropsItsRead(t *testing.T) {
	core := newFillCore()
	elsewhere := listed(forgeapi.FamilyGitea, "carol/other", 5, 0)
	core.pages[""] = page(elsewhere)
	core.reads[readKey("carol/other", 5)] = giteaRead(elsewhere, forgeapi.CheckPassing, "abc")
	rec := localGiteaRecord()
	p, _, _ := noticePoller(t, map[string]forgeapi.Core{rec.ID: core}, []RepoOrigin{giteaClone("bob/lib")}, rec)

	p.sweep(t.Context())
	key := fillKey{conn: rec.ID, subject: marotte.PRSubject(rec.ID, repoIDOf("carol/other"), 5).Key}
	if _, held := p.fills[key]; !held {
		t.Fatalf("Setup: after a push-only cycle the poller holds no read of the authored row, want the one that sorted it")
	}
	core.pages[""] = page()
	p.sweep(t.Context())
	if _, held := p.fills[key]; held {
		t.Errorf("after the row left the list the poller still holds its read, want none")
	}
}

// A settled row the bound defers is read again later and is not unfilled, so a
// connection with more tracked rows than the bound does not log every cycle.
func TestNotice_ADeferredRereadIsNotLoggedUnfilled(t *testing.T) {
	logs := capture.Default(t)
	const sel = "group/app"
	const msg = "pr fill: the bound left rows unfilled"
	core := newFillCore()
	var rows []forgeapi.PullRequest
	for n := 1; n <= fillBound+1; n++ {
		pr := listed(forgeapi.FamilyGitLab, sel, n, n)
		rows = append(rows, pr)
		core.reads[readKey(sel, n)] = gitlabRead(pr, forgeapi.CheckPassing)
	}
	core.pages[""] = page(rows...)
	rec := gitlabRecord()
	p, _, _ := noticePoller(t, map[string]forgeapi.Core{rec.ID: core}, []RepoOrigin{gitlabClone(sel)}, rec)

	p.sweep(t.Context())
	if got := logs.CountExact(msg); got != 1 {
		t.Fatalf("Setup: %d %q lines after a first cycle that left the oldest row unread, want 1", got, msg)
	}
	p.sweep(t.Context())
	p.sweep(t.Context())
	if got := logs.CountExact(msg); got != 1 {
		t.Errorf("%d %q lines after two cycles that only deferred re-reads of settled rows, want the first cycle's 1", got, msg)
	}
	if !slices.Contains(core.asked, readKey(sel, 1)) {
		t.Errorf("reads %q never reached the oldest row, want it read", core.asked)
	}
}

// One cycle where one row's read fails and another's answers: the answered row
// notifies, the failed one keeps its verdict and is logged once, and both are
// read again on the next cycle.
func TestNotice_OneFailedReadBesideAnAnsweredOne(t *testing.T) {
	logs := capture.Default(t)
	const sel = "group/app"
	const msg = "pr fill: reading pull requests failed"
	core := newFillCore()
	a, b := listed(forgeapi.FamilyGitLab, sel, 1, 1), listed(forgeapi.FamilyGitLab, sel, 2, 2)
	core.pages[""] = page(a, b)
	core.reads[readKey(sel, 1)] = gitlabRead(a, forgeapi.CheckPending)
	core.reads[readKey(sel, 2)] = gitlabRead(b, forgeapi.CheckPending)
	rec := gitlabRecord()
	p, _, n := noticePoller(t, map[string]forgeapi.Core{rec.ID: core}, []RepoOrigin{gitlabClone(sel)}, rec)
	p.sweep(t.Context())

	core.errs[readKey(sel, 1)] = &forgeapi.Error{Kind: forgeapi.KindTransient}
	core.reads[readKey(sel, 2)] = gitlabRead(b, forgeapi.CheckFailing)
	p.sweep(t.Context())
	if got, want := sortedBodies(n.sent), []string{"group/app #2: Checks failed · PR group/app"}; !slices.Equal(got, want) {
		t.Errorf("a cycle with one failed and one failing read sent %q, want %q", got, want)
	}
	if got := logs.CountExact(msg); got != 1 {
		t.Errorf("%d %q lines for a cycle with one failed read, want 1", got, msg)
	}

	clear(core.errs)
	core.reads[readKey(sel, 1)] = gitlabRead(a, forgeapi.CheckPassing)
	core.asked = nil
	p.sweep(t.Context())
	want := []string{"group/app #1: Checks passed · PR group/app", "group/app #2: Checks failed · PR group/app"}
	if got := sortedBodies(n.sent); !slices.Equal(got, want) {
		t.Errorf("after the failed read answered passing, notices = %q, want %q: its pending verdict held", got, want)
	}
	if !slices.Contains(core.asked, readKey(sel, 1)) || !slices.Contains(core.asked, readKey(sel, 2)) {
		t.Errorf("the cycle after a partial failure read %q, want both rows", core.asked)
	}
}

// A push-only cycle with nobody present costs each connection one ListMyPRs and
// no account read, plus at most fillBound reads of its tracked rows.
func TestNotice_PushOnlyCycleCost(t *testing.T) {
	gl, gt := newFillCore(), newFillCore()
	var glRows, gtRows []forgeapi.PullRequest
	for n := 1; n <= 12; n++ {
		pr := listed(forgeapi.FamilyGitLab, "group/app", n, n)
		glRows = append(glRows, pr)
		gl.reads[readKey("group/app", n)] = gitlabRead(pr, forgeapi.CheckPending)
	}
	for n := 1; n <= 3; n++ {
		pr := listed(forgeapi.FamilyGitea, "bob/app", n, n)
		gtRows = append(gtRows, pr)
		gt.reads[readKey("bob/app", n)] = giteaRead(pr, forgeapi.CheckPending, "abc")
	}
	gl.pages[""], gt.pages[""] = page(glRows...), page(gtRows...)
	glRec, gtRec := gitlabRecord(), localGiteaRecord()
	p, _, _ := noticePoller(t, map[string]forgeapi.Core{glRec.ID: gl, gtRec.ID: gt},
		[]RepoOrigin{gitlabClone("group/app"), giteaClone("bob/app")}, glRec, gtRec)

	p.sweep(t.Context())

	for name, c := range map[string]*fillCore{"GitLab": gl, "Gitea": gt} {
		if !slices.Equal(c.scopeCore.asked, []string{""}) || c.whoamis != 0 {
			t.Errorf("%s: a push-only cycle listed %q with %d account reads, want one authored ListMyPRs and none",
				name, c.scopeCore.asked, c.whoamis)
		}
	}
	if len(gl.asked) != fillBound || len(gt.asked) != 3 {
		t.Errorf("a push-only cycle read %d GitLab and %d Gitea rows, want %d (the bound) and 3",
			len(gl.asked), len(gt.asked), fillBound)
	}
}
