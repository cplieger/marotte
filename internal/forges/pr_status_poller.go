// Push when a pull request you opened flips green or red, and fill each
// connection's inventory while a client is present. A sweep is the gate first: a
// present client or its request for a cycle reads every scope of every
// connection, the notice alone the authored ListMyPRs of each connection a clone
// tracks. The rate is PRPollInterval while something is tracked or the list is
// viewed, else PRDiscoveryInterval. The verdict is GitHub's row, else a ReadPR of
// each tracked row (TestProviderCheckVerdicts_MatchTheStatedScope pins the copy);
// a first verdict seeds silently, so a boot never announces a missed flip.

package forges

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/push"
)

// PRPollInterval is the ACTIVE rate: how often the poller looks while it is
// tracking at least one open authored PR, or while a client shows the
// pull-request view, which is open to be fresh. Sixty seconds against a
// three-to-six minute CI run: fast enough that the notice is still useful, slow
// enough that a run is not sampled a dozen times. Matches schedule.TickInterval,
// and for the same reason: one shared timer rather than a timer per subject.
const PRPollInterval = time.Minute

// PRDiscoveryInterval is the IDLE rate: how often the poller asks whether an open
// authored PR has appeared while it tracks none. The seeding rule bounds it: a PR
// first seen already settled is never announced, so a three-to-six-minute CI run
// whose whole life fits inside one gap goes silent. Five minutes keeps the common
// case (discovered while pending) announced; at fifteen most runs would be seeded
// green, so do not raise it without re-reading that interaction.
const PRDiscoveryInterval = 5 * time.Minute

// The CI verdicts the poller announces, in the library's CheckState spellings.
const (
	checkPending = "pending"
	checkPassing = "passing"
	checkFailing = "failing"
)

// checkUnread is the verdict of a row whose verdict comes from a read while no
// read of it answered. It holds the last verdict, and a verdict following it
// seeds, so a row the bound left unread or a failed read is never news.
const checkUnread = ""

// WatchedPR is an authored row whose target or source repository a clone
// tracks, reduced to the fields the notice needs.
type WatchedPR struct {
	ForgeID string
	// RepoID is the target repository's canonical id, the key the
	// notification's subject uses.
	RepoID string
	// Repo is the repository's display path, the one the notice names.
	Repo  string
	Title string
	// Check is the folded CI verdict in forgeapi's CheckState spelling: unknown,
	// passing, failing, pending or neutral; checkUnread before a read answered.
	Check  string
	Number int
}

// PRConnection is one connection as a walk keys it: the same connection signed
// in as another account, or moved to another address, starts another walk.
type PRConnection struct {
	ID      string
	Account string
	WebBase string
}

// Gate is the gate's answer before a sweep.
type Gate struct {
	// Present is a client connected on the stream: the sweep reads every scope.
	Present bool
	// Push is the pull-request notice wanting a sweep: alone, it makes the
	// authored call only.
	Push bool
}

// Scope is one list a connection's cycle reads.
type Scope struct {
	// Kind is "owner", "authored" or "added".
	Kind string
	// Owner is the login or group an owner or added scope lists under.
	Owner string
}

const (
	scopeOwner    = "owner"
	scopeAuthored = "authored"
	scopeAdded    = "added"
)

// authoredScope is the credential's own open pull requests, the notifier's call.
var authoredScope = Scope{Kind: scopeAuthored}

// ScopePage is one scope's answer to a sweep: one call's rows and the cursor
// that continues its walk, empty on the page that ends it. Err is the call's
// failure, with the page empty.
type ScopePage struct {
	Err     error
	Partial *PartialResult
	Scope   Scope
	Next    forgeapi.Cursor
	Rows    []PR
}

// ConnectionRead is one connected connection's answer to a sweep. Pages is
// empty when the sweep did not read it.
type ConnectionRead struct {
	Budget *InventoryBudget
	// ReadPR reads one of the connection's pull requests, body stripped, by the
	// canonical repository id its rows carry; nil when the sweep did not read it.
	ReadPR     func(ctx context.Context, repoID string, number int) (PR, error)
	Conn       PRConnection
	Credential string
	Pages      []ScopePage
	Clones     []CloneRepo
	// Family decides which fields of the connection's rows a read fills.
	Family forgeapi.Family
}

// PRSource answers the poller's one question for a sweep: every connected
// connection, each scope read from the cursor after names for it, every scope
// when present and the authored call alone otherwise.
//
// It is also the seam that lets the loop be tested with no forge, the same shape
// schedule.Launcher has.
type PRSource interface {
	Read(ctx context.Context, present bool, after func(PRConnection, Scope) forgeapi.Cursor) []ConnectionRead
}

// PRNotifier is the one push-service method the poller uses; *push.Service
// satisfies it directly. Whether a sweep runs at all is the gate's question.
type PRNotifier interface {
	Send(ctx context.Context, title, body string, kind marotte.PushKind, subject marotte.PushSubject)
}

// trackedPR is the poller's state for one subject: the last verdict, and the
// connection whose walk prunes it.
type trackedPR struct {
	check   string
	forgeID string
}

// prWalk is one scope's walk across sweeps, one page a sweep, because a
// continuation is resumed only by another call. A row absent from one page is
// not closed: only the page that ends the walk prunes what the walk never read,
// so a verdict is read once per walk (on GitHub, ceil(n/25) sweeps for n open
// PRs).
type prWalk struct {
	// keys are the subjects the authored walk read in a tracked repository.
	keys map[string]struct{}
	// read are the rows this walk read; rows are the last whole walk's rows
	// updated by this one's.
	read    map[string]struct{}
	rows    map[string]PR
	partial *PartialResult
	next    forgeapi.Cursor
}

type walkKey struct {
	scope Scope
	conn  string
}

// cycleClock numbers the sweeps that start a cycle and carries a client's
// request for one. The inventory routes call request and ask; only the sweep
// calls begin and end.
type cycleClock struct {
	// wake rings Run's loop. It holds one ring, so a burst of requests wakes one
	// sweep; asked is what the ring was for, since Run's receive consumes it.
	wake chan struct{}
	// last is the newest cycle begun.
	last    uint64
	mu      sync.Mutex
	running bool
	asked   bool
}

func newCycleClock() *cycleClock {
	return &cycleClock{wake: make(chan struct{}, 1)}
}

// request is a refresh: it joins the cycle in flight or asks for the next one,
// and answers the id of the cycle that serves it.
func (c *cycleClock) request() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		return c.last
	}
	c.ringLocked()
	return c.last + 1
}

// ask asks for a cycle that begins after any in flight, which may have listed
// its connections before the one asking was there, or read a repository before
// a mutation changed it, and answers the id of that cycle.
func (c *cycleClock) ask() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ringLocked()
	return c.last + 1
}

func (c *cycleClock) ringLocked() {
	c.asked = true
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// begin starts the next cycle when g is open or a client asked for one, which
// makes it a present cycle: a request comes from a client by definition. A ring
// it takes the request of is drained, so it starts no second cycle.
func (c *cycleClock) begin(g Gate) (id uint64, present, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	asked := c.asked
	c.asked = false
	select {
	case <-c.wake:
	default:
	}
	present = g.Present || asked
	if !present && !g.Push {
		return 0, false, false
	}
	c.last++
	c.running = true
	return c.last, present, true
}

func (c *cycleClock) end() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.running = false
}

// PRStatusPoller notifies on a CI flip and fills the inventory. Construct with
// NewPRStatusPoller and call Run in a goroutine; it returns when ctx is
// cancelled.
type PRStatusPoller struct {
	src  PRSource
	push PRNotifier
	// gate is consulted before every sweep; closed, the sweep does no forge work.
	gate    func() Gate
	inv     *inventory
	cycles  *cycleClock
	viewers *viewers
	// announce carries each entry put in the inventory to the clients; nil, the
	// entry is only answered on a read.
	announce inventoryHub
	// seen is the last CI verdict per subject key. Bounded by the number of open
	// PRs the identity has, and pruned whenever a walk completes, so a long-lived
	// process does not accumulate one slot per PR it ever saw.
	//
	// It is also the RATE selector: empty means nothing is being tracked, which is
	// the state that earns the discovery interval rather than the active one.
	seen map[string]trackedPR
	// walks are the scopes' walks; conns is each connection as it was last read.
	walks map[walkKey]*prWalk
	conns map[string]PRConnection
	// fills are the last reads of rows a family's list leaves unknown; waits
	// are the cycles the rows due a read have waited since.
	fills map[fillKey]rowFill
	waits map[fillKey]uint64
	// refills are rows a route outdated without moving their updated_at, which
	// the next fill reads again; a route writes them, so they take refillMu.
	refills   map[fillKey]struct{}
	refillMu  sync.Mutex
	tick      time.Duration
	discovery time.Duration
	// cycle is the id of the cycle the sweep is running.
	cycle uint64
}

// NewPRStatusPoller wires a poller over a source, the push service and the gate
// every sweep consults first.
func NewPRStatusPoller(src PRSource, notifier PRNotifier, gate func() Gate, opts ...PollerOption) *PRStatusPoller {
	p := &PRStatusPoller{
		src:       src,
		push:      notifier,
		gate:      gate,
		inv:       newInventory(),
		cycles:    newCycleClock(),
		viewers:   newViewers(),
		seen:      make(map[string]trackedPR),
		walks:     make(map[walkKey]*prWalk),
		conns:     make(map[string]PRConnection),
		fills:     make(map[fillKey]rowFill),
		waits:     make(map[fillKey]uint64),
		refills:   make(map[fillKey]struct{}),
		tick:      PRPollInterval,
		discovery: PRDiscoveryInterval,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Run polls until ctx is cancelled. It does not sweep on entry, since the first
// sweep only seeds, and a client's request for a cycle sweeps at once. The next
// sweep is timed from the previous one's completion, never by a ticker, whose
// retained tick would start a sweep that outran the interval again at once: the
// interval is a floor on the gap between sweeps.
func (p *PRStatusPoller) Run(ctx context.Context) {
	last := time.Now()
	t := time.NewTimer(p.nextDelay())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-p.cycles.wake:
		case <-p.viewers.arrived:
			// The wait may have been armed at the discovery rate; the floor still
			// counts from the last sweep.
			if wait := p.nextDelay() - time.Since(last); wait > 0 {
				t.Reset(wait)
				continue
			}
		}
		p.sweep(ctx)
		last = time.Now()
		t.Reset(p.nextDelay())
	}
}

// nextDelay picks the rate for the next sweep: the active rate while something
// is tracked or a client shows the pull-request view, discovery otherwise.
//
// Called only from Run's goroutine, and sweep is the sole writer of seen, so seen
// needs no lock.
func (p *PRStatusPoller) nextDelay() time.Duration {
	if len(p.seen) > 0 || p.viewers.any() {
		return p.tick
	}
	return p.discovery
}

// sweep is one tick.
func (p *PRStatusPoller) sweep(ctx context.Context) {
	// The gate, first, before any forge work. Clearing the state on the way out is
	// deliberate: a gate opening later must not announce every flip that happened
	// while it was closed.
	id, present, ok := p.cycles.begin(p.gate())
	if !ok {
		clear(p.seen)
		clear(p.walks)
		clear(p.conns)
		clear(p.fills)
		clear(p.waits)
		p.inv.clear()
		return
	}
	defer p.cycles.end()
	p.cycle = id
	viewing := p.viewers.any()
	reads := p.src.Read(ctx, present, p.cursorFor)
	connected := make(map[string]struct{}, len(reads))
	for i := range reads {
		connected[reads[i].Conn.ID] = struct{}{}
		p.fold(ctx, &reads[i], present, viewing)
	}
	for id := range p.conns {
		if _, ok := connected[id]; !ok {
			p.forget(id)
		}
	}
}

// cursorFor is where conn's walk of scope continues, the first page when it has
// none or when conn is no longer the connection the walk started on.
func (p *PRStatusPoller) cursorFor(conn PRConnection, scope Scope) forgeapi.Cursor {
	if p.conns[conn.ID] != conn {
		return ""
	}
	if w := p.walks[walkKey{conn: conn.ID, scope: scope}]; w != nil {
		return w.next
	}
	return ""
}

// fold applies one connection's read, reads the rows the notice watches and the
// authored rows only a read can sort (every row of the entry while the view is
// shown) and notifies, and on a present cycle replaces its inventory entry. A
// connection the sweep did not read keeps its walks and fills, so the
// inventory's next refill continues them, and loses its subjects: nothing it
// tracked can be news any more.
func (p *PRStatusPoller) fold(ctx context.Context, r *ConnectionRead, present, viewing bool) {
	id := r.Conn.ID
	if prev, ok := p.conns[id]; ok && prev != r.Conn {
		p.forget(id)
	}
	p.conns[id] = r.Conn
	if len(r.Pages) == 0 {
		p.dropSubjects(id)
		return
	}
	tracked := make(map[string]struct{}, len(r.Clones))
	for _, c := range r.Clones {
		tracked[c.RepoID] = struct{}{}
	}
	var watched, unsorted []PR
	for i := range r.Pages {
		w, u := p.apply(r, &r.Pages[i], tracked)
		watched, unsorted = append(watched, w...), append(unsorted, u...)
	}
	toRead := slices.Concat(watched, unsorted)
	if !present {
		p.readRows(ctx, r, toRead, watched)
		p.notify(ctx, r, watched)
		return
	}
	// A present cycle reads every scope the connection has, so a walk it did
	// not read is a scope that went (an owner removed, a login renamed).
	for k := range p.walks {
		if k.conn == id && !slices.ContainsFunc(r.Pages, func(pg ScopePage) bool { return pg.Scope == k.scope }) {
			delete(p.walks, k)
		}
	}
	e := p.entryOf(r)
	if viewing {
		toRead = distinctRows(&e)
	}
	p.readRows(ctx, r, toRead, watched)
	p.notify(ctx, r, watched)
	p.layFills(r, &e)
	p.publish(ctx, &e)
}

// apply folds one scope's page into its walk, answers its watched and unsorted
// authored rows (watches), and prunes what the walk never read once the page
// ends it.
func (p *PRStatusPoller) apply(r *ConnectionRead, page *ScopePage, tracked map[string]struct{}) (watched, unsorted []PR) {
	conn := r.Conn
	k := walkKey{conn: conn.ID, scope: page.Scope}
	w := p.walks[k]
	if page.Err != nil {
		var fe *forgeapi.Error
		if w != nil && errors.As(page.Err, &fe) && fe.Code == forgeapi.CodeCursorInvalid {
			w.restart()
		}
		slog.Debug("pr status: listing a connection's open pull requests failed",
			"forge", conn.ID, "scope", page.Scope.Kind, "error", logsafe.Field(page.Err.Error()))
		return nil, nil
	}
	if w == nil {
		w = &prWalk{keys: make(map[string]struct{}), read: make(map[string]struct{}), rows: make(map[string]PR)}
		p.walks[k] = w
	}
	w.partial = page.Partial
	authored := page.Scope == authoredScope
	for i := range page.Rows {
		row := &page.Rows[i]
		subject := marotte.PRSubject(conn.ID, row.RepoID, row.Number)
		w.read[subject.Key] = struct{}{}
		w.rows[subject.Key] = *row
		if !authored {
			continue
		}
		switch ok, known := p.watches(r, row, tracked); {
		case ok:
			w.keys[subject.Key] = struct{}{}
			watched = append(watched, *row)
		case !known:
			unsorted = append(unsorted, *row)
		}
	}
	if page.Next != "" {
		w.next = page.Next
		return watched, unsorted
	}
	p.endWalk(conn.ID, w, authored)
	return watched, unsorted
}

// watches reports whether the notice watches an authored row, the target or
// source repository a clone tracks (a clone of a fork joins the pull requests
// opened from it), and whether that is known. A list that names no source
// leaves it unknown until a read of the row answers, which the cycle makes for
// such a row even when the answer then finds it untracked.
func (p *PRStatusPoller) watches(r *ConnectionRead, row *PR, tracked map[string]struct{}) (ok, known bool) {
	if _, onTarget := tracked[row.RepoID]; onTarget {
		return true, true
	}
	source := row.SourceRepoID
	if source == "" && familyFills[r.Family].sourceFromRead {
		f := p.fills[fillKeyOf(r.Conn.ID, row)]
		if source, known = f.source(); !known {
			return false, false
		}
	}
	_, ok = tracked[source]
	return ok, true
}

// notify observes each watched row's verdict: its own where its family lists
// one, else the last read of it.
func (p *PRStatusPoller) notify(ctx context.Context, r *ConnectionRead, watched []PR) {
	ff, ok := familyFills[r.Family]
	fromRead := ok && ff.reads(readChecks)
	for i := range watched {
		row := &watched[i]
		pr := watchedOf(r.Conn.ID, row)
		if fromRead {
			pr.Check = p.heldCheck(r.Conn.ID, row)
		}
		p.observe(ctx, marotte.PRSubject(r.Conn.ID, row.RepoID, row.Number), &pr)
	}
}

func watchedOf(forgeID string, row *PR) WatchedPR {
	return WatchedPR{
		ForgeID: forgeID, RepoID: row.RepoID, Repo: row.Repo,
		Title: row.Title, Check: row.Action.Checks, Number: row.Number,
	}
}

// observe records pr's verdict under subject, the poller's state key and the
// notification's, so the two cannot describe different things.
func (p *PRStatusPoller) observe(ctx context.Context, subject marotte.PushSubject, pr *WatchedPR) {
	prev, known := p.seen[subject.Key]
	if pr.Check == checkUnread {
		if !known {
			p.seen[subject.Key] = trackedPR{forgeID: pr.ForgeID}
		}
		return
	}
	p.seen[subject.Key] = trackedPR{check: pr.Check, forgeID: pr.ForgeID}
	if !known || prev.check == checkUnread || prev.check == pr.Check || !isSettledCheck(pr.Check) {
		// A first sighting seeds; an unchanged verdict is not news; and a flip
		// INTO pending is the run starting, which the user caused by pushing.
		return
	}
	p.push.Send(ctx, push.DefaultTitle, prStatusBody(pr), marotte.PushKindPRStatus, subject)
}

// endWalk prunes, for the authored walk, the subjects of its connection it never
// read, then the rows w never read and the reads of rows no walk of the
// connection holds, which a push-only cycle would otherwise keep until a present
// one, and starts the walk again.
func (p *PRStatusPoller) endWalk(id string, w *prWalk, authored bool) {
	if authored {
		for key, tr := range p.seen {
			if _, ok := w.keys[key]; !ok && tr.forgeID == id {
				delete(p.seen, key)
			}
		}
	}
	for key := range w.rows {
		if _, ok := w.read[key]; !ok {
			delete(w.rows, key)
		}
	}
	maps.DeleteFunc(p.fills, func(k fillKey, _ rowFill) bool { return k.conn == id && !p.holds(k) })
	w.restart()
}

// holds reports whether a walk of k's connection holds k's row.
func (p *PRStatusPoller) holds(k fillKey) bool {
	for wk, w := range p.walks {
		if _, ok := w.rows[k.subject]; ok && wk.conn == k.conn {
			return true
		}
	}
	return false
}

// restart forgets the walk's position and what it read, keeping its rows.
func (w *prWalk) restart() {
	w.next = ""
	clear(w.keys)
	clear(w.read)
}

// forget drops connection id's walks, fills, subjects and inventory entry.
func (p *PRStatusPoller) forget(id string) {
	delete(p.conns, id)
	for k := range p.walks {
		if k.conn == id {
			delete(p.walks, k)
		}
	}
	maps.DeleteFunc(p.fills, func(k fillKey, _ rowFill) bool { return k.conn == id })
	maps.DeleteFunc(p.waits, func(k fillKey, _ uint64) bool { return k.conn == id })
	p.dropSubjects(id)
	p.inv.drop(id)
}

func (p *PRStatusPoller) dropSubjects(id string) {
	for key, tr := range p.seen {
		if tr.forgeID == id {
			delete(p.seen, key)
		}
	}
}

// isSettledCheck reports whether a verdict is one worth interrupting for.
//
// Only green and red are. "pending" is the run STARTING, which the user caused by
// pushing seconds earlier, and unknown or neutral is no verdict at all, so
// notifying on any of them would make the channel noise and teach the user to
// ignore it.
func isSettledCheck(check string) bool {
	return check == checkPassing || check == checkFailing
}

// prStatusBody is what the notification says. Short by design: the tray truncates,
// and fitToCap trims against the payload cap, so the verdict and the number lead
// and the title follows.
func prStatusBody(pr *WatchedPR) string {
	verdict := "checks failed"
	if pr.Check == checkPassing {
		verdict = "checks passed"
	}
	body := pr.Repo + " #" + strconv.Itoa(pr.Number) + " " + verdict
	if pr.Title != "" {
		body += ": " + pr.Title
	}
	return body
}
