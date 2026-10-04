package forges

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/subject"
)

// InventoryEntry.State vocabulary.
const (
	inventoryLoading = "loading"
	inventoryReady   = "ready"
	inventoryPartial = "partial"
	inventoryFailed  = "failed"
)

// inventory is each connection's pull requests as the poller's last present
// cycle read them, held in memory. Only the poller's sweep writes it; an entry
// is replaced whole and never modified after, so a read shares its slices.
// Every write moves the entry's forge_inventory version inside mu, removals
// included, so a client holding a removed entry reads it changed.
type inventory struct {
	entries  map[string]InventoryEntry
	versions *subject.Versions
	mu       sync.Mutex
}

func newInventory() *inventory {
	return &inventory{entries: make(map[string]InventoryEntry), versions: &subject.Versions{}}
}

// put stores e and answers the version it minted for it.
func (inv *inventory) put(e *InventoryEntry) string {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	inv.entries[e.ForgeID] = *e
	return inv.versions.BumpCounter(subject.KindForgeInventory, e.ForgeID)
}

func (inv *inventory) drop(id string) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	delete(inv.entries, id)
	inv.versions.BumpCounter(subject.KindForgeInventory, id)
}

func (inv *inventory) clear() {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	for id := range inv.entries {
		inv.versions.BumpCounter(subject.KindForgeInventory, id)
	}
	clear(inv.entries)
}

// inventoryHub is where the inventory is announced, and the epoch a read's
// stamps carry. *agent.Runtime satisfies it.
type inventoryHub interface {
	broadcaster
	Epoch() string
}

// PollerOption configures a PRStatusPoller.
type PollerOption func(*PRStatusPoller)

// WithInventoryPush announces every inventory entry a cycle writes on hub as
// one forge_inventory frame, stamped with a version minted in v, the registry
// the digest reads.
func WithInventoryPush(v *subject.Versions, hub inventoryHub) PollerOption {
	return func(p *PRStatusPoller) {
		p.inv.versions = v
		p.announce = hub
	}
}

// publish puts e in the inventory and announces it under the version the put
// minted.
func (p *PRStatusPoller) publish(ctx context.Context, e *InventoryEntry) {
	version := p.inv.put(e)
	if p.announce == nil {
		return
	}
	evt := marotte.NewEvent(marotte.EventForgeInventory, "", InventoryChangedPayload{Entry: *e})
	evt.Subject = marotte.NewSubjectStamp(string(subject.KindForgeInventory), e.ForgeID, version)
	p.announce.Broadcast(ctx, evt)
}

// entryOf is r's entry from this cycle's walks. A failed scope keeps the rows its
// walk last read; the entry is failed only when every scope failed, and the
// error is the cycle's last failure.
func (p *PRStatusPoller) entryOf(r *ConnectionRead) InventoryEntry {
	e := InventoryEntry{
		Budget: r.Budget, ForgeID: r.Conn.ID, CycleID: strconv.FormatUint(p.cycle, 10), Credential: r.Credential,
		Scopes: make([]InventoryScope, 0, len(r.Pages)), Clones: append(make([]CloneRepo, 0, len(r.Clones)), r.Clones...),
		FetchedAt: time.Now().UnixMilli(),
	}
	failed, partial := 0, false
	for i := range r.Pages {
		pg := &r.Pages[i]
		s := InventoryScope{Scope: pg.Scope.Kind, Owner: pg.Scope.Owner, Rows: []PR{}}
		if w := p.walks[walkKey{conn: r.Conn.ID, scope: pg.Scope}]; w != nil {
			s.Rows, s.Next, s.Partial = w.sortedRows(), string(w.next), w.partial
		}
		if pg.Err != nil {
			failed++
			e.Error = inventoryError(pg.Err)
		}
		partial = partial || s.Partial != nil
		e.Scopes = append(e.Scopes, s)
	}
	switch {
	case failed == len(r.Pages):
		e.State = inventoryFailed
	case failed > 0 || partial:
		e.State = inventoryPartial
	default:
		e.State = inventoryReady
	}
	return e
}

// sortedRows are w's rows, most recently updated first.
func (w *prWalk) sortedRows() []PR {
	rows := slices.Collect(maps.Values(w.rows))
	slices.SortFunc(rows, func(a, b PR) int { return newerFirst(&a, &b) })
	return rows
}

// newerFirst orders rows most recently updated first, then by repository and
// number, so equal content always sorts the same.
func newerFirst(a, b *PR) int {
	return cmp.Or(cmp.Compare(b.UpdatedAt, a.UpdatedAt), cmp.Compare(a.RepoID, b.RepoID), cmp.Compare(a.Number, b.Number))
}

// inventoryError is err in the error envelope's terms. An error the library did
// not raise has no code.
func inventoryError(err error) *InventoryError {
	fe, ok := errors.AsType[*forgeapi.Error](err)
	if !ok {
		return &InventoryError{Kind: forgeapi.KindUnknown.String()}
	}
	return &InventoryError{Code: fe.Code, Kind: fe.Kind.String(), DiagID: fe.DiagID, RetryAfterS: retryAfterSeconds(fe)}
}

// heldEntry is one connection's entry, if held, and the version certifying it.
type heldEntry struct {
	version string
	entry   InventoryEntry
	ok      bool
}

// lookup answers each id's entry and version from one critical section.
func (inv *inventory) lookup(ids []string) []heldEntry {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	out := make([]heldEntry, len(ids))
	for i, id := range ids {
		out[i].entry, out[i].ok = inv.entries[id]
		out[i].version, _ = inv.versions.Current(subject.KindForgeInventory, id)
	}
	return out
}

// inventoryFor is the entry of every connected row, ordered by connection id,
// from memory, each with its stamp, and whether a client shows the pull-request
// view. A connection no cycle has filled answers
// loading and asks for a cycle; an entry of a connection no longer connected is
// left out.
func (p *PRStatusPoller) inventoryFor(rows []ConfiguredForge) InventoryList {
	ids := make([]string, 0, len(rows))
	for i := range rows {
		if rows[i].Connected {
			ids = append(ids, rows[i].ID)
		}
	}
	slices.Sort(ids)
	list := InventoryList{Entries: make([]InventoryEntry, 0, len(ids)), Subject: make([]*marotte.SubjectStamp, 0, len(ids))}
	missing := false
	held := p.inv.lookup(ids)
	for i := range held {
		if !held[i].ok {
			held[i].entry = InventoryEntry{
				ForgeID: ids[i], State: inventoryLoading, CycleID: "0", Credential: forgeapi.CredUnknown.String(),
				Scopes: []InventoryScope{}, Clones: []CloneRepo{},
			}
			missing = true
		}
		list.Entries = append(list.Entries, held[i].entry)
		list.Subject = append(list.Subject, p.restStamp(ids[i], held[i].version))
	}
	if missing {
		p.cycles.ask()
	}
	list.Viewing = p.viewers.any()
	return list
}

// restStamp is a read's stamp for id, carrying the hub's epoch as every REST
// stamp does.
func (p *PRStatusPoller) restStamp(id, version string) *marotte.SubjectStamp {
	stamp := marotte.NewSubjectStamp(string(subject.KindForgeInventory), id, version)
	if p.announce != nil {
		stamp.Epoch = p.announce.Epoch()
	}
	return stamp
}

// refresh asks for a cycle and answers the id of the one that serves it.
func (p *PRStatusPoller) refresh() string {
	return strconv.FormatUint(p.cycles.request(), 10)
}
