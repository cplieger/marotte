package forges

import (
	"cmp"
	"context"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
)

// fillBound is how many rows of one connection a cycle reads one by one, for the
// view and the notice together. It is a launch value: a cycle that leaves rows
// unfilled logs how many, and that count is what moves it.
const fillBound = 10

// FieldFill.Reason vocabulary.
const (
	fillFilled      = "filled"
	fillNotOnList   = "not_on_list"
	fillUnread      = "unread"
	fillNotSupplied = "not_supplied"
)

// rowField is one row field a family's ListMyPRs leaves unknown and its ReadPR
// supplies, and how a read's value is laid over the list row.
type rowField struct {
	lay  func(dst, src *PR)
	name string
}

var (
	readChecks       = rowField{name: "checks", lay: func(d, s *PR) { d.Action.Checks = s.Action.Checks }}
	readMergeable    = rowField{name: "mergeable", lay: func(d, s *PR) { d.Action.Mergeable = s.Action.Mergeable }}
	readMergeBlocked = rowField{name: "merge_blocked", lay: func(d, s *PR) { d.Action.MergeBlocked = s.Action.MergeBlocked }}
	readHeadSHA      = rowField{name: "head_sha", lay: func(d, s *PR) { d.HeadSHA = s.HeadSHA }}
	readSourceBranch = rowField{name: "source_branch", lay: func(d, s *PR) { d.SourceBranch = s.SourceBranch }}
	readTargetBranch = rowField{name: "target_branch", lay: func(d, s *PR) { d.TargetBranch = s.TargetBranch }}
	// The fold's partial travels with its counts: past its page bound a read
	// marks the row rather than the list.
	readCheckCounts = rowField{name: "check_counts", lay: func(d, s *PR) {
		a, b := &d.Action, &s.Action
		a.ChecksPassing, a.ChecksFailing, a.ChecksPending = b.ChecksPassing, b.ChecksFailing, b.ChecksPending
		a.ChecksNeutral, a.ChecksUnknown, a.ChecksTotal = b.ChecksNeutral, b.ChecksUnknown, b.ChecksTotal
		d.Partial = s.Partial
	}}
)

// familyFill is what a family's ListMyPRs rows lack: the fields a read fills, and
// the ones not even a read supplies (forgeapi's ListMyPRs and ReadPR
// departures). GitHub's rows lack nothing a read would add.
type familyFill struct {
	read   []rowField
	absent []string
	// sourceFromRead is a list whose rows name no source repository, which a
	// read does.
	sourceFromRead bool
}

// reads reports whether a read fills field.
func (ff *familyFill) reads(field rowField) bool {
	return slices.ContainsFunc(ff.read, func(f rowField) bool { return f.name == field.name })
}

var familyFills = map[forgeapi.Family]familyFill{
	forgeapi.FamilyGitLab: {
		read:   []rowField{readChecks, readMergeable, readMergeBlocked},
		absent: []string{"check_counts"},
	},
	forgeapi.FamilyGitea: {
		read:           []rowField{readChecks, readCheckCounts, readMergeable, readHeadSHA, readSourceBranch, readTargetBranch},
		absent:         []string{"merge_blocked", "auto_merge_armed"},
		sourceFromRead: true,
	},
}

type fillKey struct {
	conn    string
	subject string
}

// rowFill is the last read of one row: what it answered, at which updated_at,
// and when. A failed read clears ok and keeps the last answered read, whose
// source the notice's tracking still reads; asOf is zero until one answered.
type rowFill struct {
	read      PR
	updatedAt int64
	asOf      int64
	ok        bool
}

// due reports whether a row is read again, and its rank: a fill whose checks
// were pending, then a row with no fill or a failed one, then a row updated
// since its fill, then a row the notice watches, whose verdict a re-run moves
// without moving updated_at. A settled fill of any other unchanged row does not
// change.
func (f *rowFill) due(held, watched bool, row *PR) (rank int, ok bool) {
	switch {
	case held && f.ok && f.read.Action.Checks == checkPending:
		return 0, true
	case !held || !f.ok:
		return 1, true
	case row.UpdatedAt != f.updatedAt:
		return 2, true
	case watched:
		return rankReread, true
	}
	return 0, false
}

// source is the source repository the last answered read named, and whether a
// read answered.
func (f *rowFill) source() (string, bool) {
	return f.read.SourceRepoID, f.asOf != 0
}

// readRows reads the due rows among rows on a family whose list leaves fields
// unknown, at most fillBound of them; watched are the rows among them the notice
// compares. It is the one per-row reader: the view's fill and the notice both
// read through it, under one bound a cycle.
func (p *PRStatusPoller) readRows(ctx context.Context, r *ConnectionRead, rows, watched []PR) {
	if _, ok := familyFills[r.Family]; ok && r.ReadPR != nil && len(rows) > 0 {
		p.readDue(ctx, r, rows, watched)
	}
}

// heldCheck is the verdict the last read of conn's row answered, checkUnread
// while no read of it answered.
func (p *PRStatusPoller) heldCheck(conn string, row *PR) string {
	if f, ok := p.fills[fillKeyOf(conn, row)]; ok && f.ok {
		return f.read.Action.Checks
	}
	return checkUnread
}

// layFills lays every held read over e's rows and drops the reads of rows e no
// longer holds.
func (p *PRStatusPoller) layFills(r *ConnectionRead, e *InventoryEntry) {
	ff, ok := familyFills[r.Family]
	if !ok {
		return
	}
	kept := make(map[fillKey]struct{})
	for i := range e.Scopes {
		rows := e.Scopes[i].Rows
		for j := range rows {
			k := fillKeyOf(r.Conn.ID, &rows[j])
			kept[k] = struct{}{}
			f, held := p.fills[k]
			rows[j].Fill = layFill(&ff, &f, held, &rows[j])
		}
	}
	for k := range p.fills {
		if _, ok := kept[k]; !ok && k.conn == r.Conn.ID {
			delete(p.fills, k)
		}
	}
}

func fillKeyOf(conn string, row *PR) fillKey {
	return fillKey{conn: conn, subject: marotte.PRSubject(conn, row.RepoID, row.Number).Key}
}

// refill has the next fill read conn's pull request number in repoID again,
// whatever its updated_at says: a re-run restarts its checks without moving it.
func (p *PRStatusPoller) refill(conn, repoID string, number int) {
	p.refillMu.Lock()
	defer p.refillMu.Unlock()
	p.refills[fillKey{conn: conn, subject: marotte.PRSubject(conn, repoID, number).Key}] = struct{}{}
}

// takeRefills forgets the fills of conn's rows a refill named.
func (p *PRStatusPoller) takeRefills(conn string) {
	p.refillMu.Lock()
	defer p.refillMu.Unlock()
	for k := range p.refills {
		if k.conn == conn {
			delete(p.fills, k)
			delete(p.refills, k)
		}
	}
}

// distinctRows is each row of e once.
func distinctRows(e *InventoryEntry) []PR {
	type rowKey struct {
		repo   string
		number int
	}
	seen := make(map[rowKey]struct{})
	var out []PR
	for i := range e.Scopes {
		for j := range e.Scopes[i].Rows {
			row := &e.Scopes[i].Rows[j]
			key := rowKey{repo: row.RepoID, number: row.Number}
			if _, dup := seen[key]; !dup {
				seen[key] = struct{}{}
				out = append(out, *row)
			}
		}
	}
	return out
}

// dueRow is a row a cycle may read: its rank, and the cycle it has waited since.
type dueRow struct {
	row   *PR
	key   fillKey
	since uint64
	rank  int
}

// byWait orders rows longest waiting first, then most recently updated first.
func byWait(a, b dueRow) int {
	return cmp.Or(cmp.Compare(a.since, b.since), newerFirst(a.row, b.row))
}

// rankReread is due's rank for a watched row whose settled fill is read again.
const rankReread = 3

// capDue keeps fillBound of the sorted due rows, the last slot for the longest
// waiting of the rest whatever its rank, so rows that stay pending starve none,
// and answers how many it left unfilled (a deferred re-read is not).
func capDue(due []dueRow) (kept []dueRow, unfilled int) {
	if len(due) <= fillBound {
		return due, 0
	}
	last := fillBound - 1
	oldest := last
	for i := last + 1; i < len(due); i++ {
		if byWait(due[i], due[oldest]) < 0 {
			oldest = i
		}
	}
	due[last], due[oldest] = due[oldest], due[last]
	for _, d := range due[fillBound:] {
		if d.rank < rankReread {
			unfilled++
		}
	}
	return due[:fillBound], unfilled
}

// readDue reads the due rows by rank, each rank by wait, under capDue, and logs
// the cycle's failed reads once.
func (p *PRStatusPoller) readDue(ctx context.Context, r *ConnectionRead, rows, watched []PR) {
	due := p.dueRows(r.Conn.ID, rows, watched)
	slices.SortFunc(due, func(a, b dueRow) int { return cmp.Or(cmp.Compare(a.rank, b.rank), byWait(a, b)) })
	due, left := capDue(due)
	if left > 0 {
		slog.Info("pr fill: the bound left rows unfilled", "forge", r.Conn.ID, "unfilled", left, "bound", fillBound)
	}
	var failed []*PR
	var firstErr error
	for _, d := range due {
		p.waits[d.key] = p.cycle
		got, err := r.ReadPR(ctx, d.row.RepoID, d.row.Number)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			failed = append(failed, d.row)
			f := p.fills[d.key]
			f.ok = false
			p.fills[d.key] = f
			continue
		}
		p.fills[d.key] = rowFill{read: got, updatedAt: got.UpdatedAt, asOf: time.Now().UnixMilli(), ok: true}
	}
	if len(failed) > 0 {
		slog.Debug("pr fill: reading pull requests failed", "forge", r.Conn.ID, "failed", len(failed),
			"repo", logsafe.Field(failed[0].Repo), "number", failed[0].Number, "error", logsafe.Field(firstErr.Error()))
	}
}

// dueRows are the rows of conn due a read, each with the cycle it has waited
// since: its last read, or the cycle it fell due unread. A row no longer due
// stops waiting.
func (p *PRStatusPoller) dueRows(conn string, rows, watched []PR) []dueRow {
	notice := make(map[fillKey]struct{}, len(watched))
	for i := range watched {
		notice[fillKeyOf(conn, &watched[i])] = struct{}{}
	}
	p.takeRefills(conn)
	var due []dueRow
	waiting := make(map[fillKey]struct{}, len(rows))
	for i := range rows {
		k := fillKeyOf(conn, &rows[i])
		f, held := p.fills[k]
		_, w := notice[k]
		rank, ok := f.due(held, w, &rows[i])
		if !ok {
			continue
		}
		since, queued := p.waits[k]
		if !queued {
			since = p.cycle
			p.waits[k] = since
		}
		waiting[k] = struct{}{}
		due = append(due, dueRow{row: &rows[i], key: k, since: since, rank: rank})
	}
	maps.DeleteFunc(p.waits, func(k fillKey, _ uint64) bool {
		_, ok := waiting[k]
		return k.conn == conn && !ok
	})
	return due
}

// layFill lays a held read over row and answers the row's fill: every field the
// family's list lacks, with why it holds its value.
func layFill(ff *familyFill, f *rowFill, held bool, row *PR) []FieldFill {
	reason, asOf := fillNotOnList, int64(0)
	switch {
	case held && f.ok:
		reason, asOf = fillFilled, f.asOf
	case held:
		reason = fillUnread
	}
	out := make([]FieldFill, 0, len(ff.read)+len(ff.absent))
	for _, field := range ff.read {
		if reason == fillFilled {
			field.lay(row, &f.read)
		}
		out = append(out, FieldFill{Field: field.name, Reason: reason, AsOf: asOf})
	}
	for _, name := range ff.absent {
		out = append(out, FieldFill{Field: name, Reason: fillNotSupplied})
	}
	return out
}
