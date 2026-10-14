// Manager holds every forge connection in memory, one immutable value per id
// replaced whole by its one writer path, and writes each change the records
// file must carry before it is shown.

package forges

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
	"github.com/cplieger/keyenc"
	"github.com/cplieger/marotte/internal/logsafe"
)

// Change is what Manager's notify is told: which connection changed, and
// whether its account was replaced.
type Change struct {
	// ID is "" when every row may have changed (a healed Load).
	ID string
	// AccountChanged is a sign-in or a sign-out: state keyed by ID's account
	// is dropped.
	AccountChanged bool
}

type coreFactory func(spec *connectionSpec, opts []forgeapi.Option) (forgeapi.Core, error)

// Manager owns the configured forge connections. Lock order: an id lock, then
// persist, then mu; gitMu is taken with no other lock held, then mu under it.
type Manager struct {
	notify     func(context.Context, Change)
	executable func() (string, error)
	newCore    coreFactory
	conns      map[string]*connection
	lists      *listCaches
	// store is nil exactly while health names why it could not be opened.
	store   creds.Store
	records recordsFile
	// health is "" while persistence is usable, else why not; every row then
	// reads unusable. lastHealth is the last verdict logged.
	health     string
	lastHealth string
	configDir  string
	locks      idLocks
	extraOpts  []forgeapi.Option
	deadlines  mutationDeadlines
	persist    sync.Mutex
	gitMu      sync.Mutex
	mu         sync.RWMutex
}

// connection is one live connection. It is never modified once installed:
// every change installs a new value.
type connection struct {
	conn    forgeapi.Core
	src     *creds.Source
	cred    storedToken
	status  status
	login   string
	email   string
	durable connectionRecord
	spec    connectionSpec
}

type statusKind uint8

const (
	statusOK statusKind = iota
	statusRetrying
	statusRefused
	statusReconnect
)

type status struct {
	checkedAt time.Time
	// fault is nil exactly when kind is statusOK.
	fault *fault
	kind  statusKind
}

type fault struct {
	// retryAt is the check's end plus the answer's Retry-After, zero when it
	// named no wait. Absolute, so a renewed wait changes the view signature.
	retryAt time.Time
	message string
	code    string
	kind    string
}

const (
	reconnectReason = "the stored credential can be neither used nor renewed. Sign in to this forge again"
	refusedReason   = "the forge refused this account at its last check"
	emptySlotReason = "no stored credential for this connection. Connect it again"
	storeReadReason = "the forge credential store could not be read (%s)"
)

// errConnectionUnusable marks every connection while the records file or the
// credential store cannot be used.
var errConnectionUnusable = errors.New("forges: connection unusable")

const codeConnectionUnusable = "connection_unusable"

// NewManager builds a Manager over the records and the credential store in
// configDir; Load reads them. notify is required: it is told every change a
// mutation, a check or a healing Load makes, and must not block.
func NewManager(configDir string, notify func(context.Context, Change)) *Manager {
	return &Manager{
		notify:     notify,
		executable: resolvedExecutable,
		newCore:    newFamilyClient,
		conns:      map[string]*connection{},
		lists:      newListCaches(),
		configDir:  configDir,
		records:    newRecordsFile(configDir),
		deadlines:  mutationDeadlines{total: mutationDeadline, reserve: durableReserve},
	}
}

// MakeID returns the canonical ID for a kind+host pair, the kind's default
// host standing in for an empty one.
func MakeID(kind Kind, host string) string {
	if host == "" {
		host = kind.defaultHost()
	}
	return keyenc.Join(string(kind), host)
}

func decode(st standing) status {
	switch st {
	case standingReconnect:
		return status{kind: statusReconnect, fault: &fault{
			message: reconnectReason, code: forgeapi.CodeReconnectRequired, kind: forgeapi.KindUnauthorized.String(),
		}}
	case standingRefused:
		return status{kind: statusRefused, fault: &fault{message: refusedReason, kind: forgeapi.KindForbidden.String()}}
	}
	return status{}
}

func standingOf(s *status) standing {
	switch s.kind {
	case statusRefused:
		return standingRefused
	case statusReconnect:
		return standingReconnect
	}
	return standingUsable
}

func encode(c *connection) connectionRecord {
	return connectionRecord{connectionSpec: c.spec, Standing: standingOf(&c.status)}
}

func (c *connection) persisted() bool { return reflect.DeepEqual(encode(c), c.durable) }

func (k statusKind) usable() bool { return k == statusOK || k == statusRetrying }

// usable is the one test of whether a reader may send a request through c.
// The caller holds mu.
func (m *Manager) usable(c *connection) bool {
	return m.health == "" && c.conn != nil && c.status.kind.usable()
}

func viewOf(c *connection, health string) ConfiguredForge {
	v := ConfiguredForge{
		ID: c.spec.ID, Kind: c.spec.Kind, Host: c.spec.Host, Username: c.login, Email: c.email,
		Account: c.spec.SignIn, OwnerScopes: c.spec.OwnerScopes, Status: ForgeConnected,
	}
	if !c.status.checkedAt.IsZero() {
		v.CheckedAt = c.status.checkedAt.UnixMilli()
	}
	if health != "" {
		v.Status, v.Error = ForgeUnusable, &ForgeError{Message: health, Code: codeConnectionUnusable}
		return v
	}
	switch c.status.kind {
	case statusOK:
		return v
	case statusRetrying:
		v.Status = ForgeRetrying
	case statusRefused:
		v.Status = ForgeRefused
	case statusReconnect:
		v.Status = ForgeReconnect
	}
	f := c.status.fault
	v.Error = &ForgeError{Message: logsafe.Field(f.message), Code: f.code, Kind: f.kind}
	if !f.retryAt.IsZero() {
		v.Error.RetryAt = f.retryAt.UnixMilli()
	}
	return v
}

// signature is what a change notifies on: the view less CheckedAt, so a check
// whose only news is its timestamp notifies nothing.
func signature(v *ConfiguredForge) ConfiguredForge {
	s := *v
	s.CheckedAt = 0
	return s
}

// List answers every connection's view, ordered by kind then host. It reads
// memory only.
func (m *Manager) List() []ConfiguredForge {
	m.mu.RLock()
	out := make([]ConfiguredForge, 0, len(m.conns))
	for _, c := range m.conns {
		out = append(out, viewOf(c, m.health))
	}
	m.mu.RUnlock()
	slices.SortFunc(out, func(a, b ConfiguredForge) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Host, b.Host))
	})
	return out
}

// view answers connection id's view, and false when it has no row.
func (m *Manager) view(id string) (ConfiguredForge, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.conns[id]
	if !ok {
		return ConfiguredForge{}, false
	}
	return viewOf(c, m.health), true
}

// healthy reports whether persistence is usable, which every mutation needs.
func (m *Manager) healthy() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.health == ""
}

// current answers id's live connection, nil when it has none, with the shared
// health and store it is read under.
func (m *Manager) current(id string) (cur *connection, health string, store creds.Store) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.conns[id], m.health, m.store
}

// readHandle is what one reader sends its requests through: a usable
// connection's client and the identity the reply is tagged with.
type readHandle struct {
	core    forgeapi.Core
	cred    forgeapi.CredentialSource
	id      string
	account string
	login   string
	host    string
	apiBase string
	webBase string
	kind    Kind
	owners  []string
	family  forgeapi.Family
}

func handleOf(c *connection) readHandle {
	return readHandle{
		core: c.conn, cred: c.cred, id: c.spec.ID, account: c.spec.SignIn, login: c.login, host: c.spec.Host,
		apiBase: c.spec.APIBaseURL, webBase: c.spec.webBase(), kind: c.spec.Kind, owners: c.spec.OwnerScopes,
		family: c.spec.Kind.family(),
	}
}

// reader answers connection id's handle: errConnectionUnusable while
// persistence is unusable, errNotLoggedIn when it has no row or its row serves
// no request.
func (m *Manager) reader(id string) (readHandle, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.health != "" {
		return readHandle{}, fmt.Errorf("%w: %s", errConnectionUnusable, m.health)
	}
	c, ok := m.conns[id]
	if !ok || !m.usable(c) {
		return readHandle{}, fmt.Errorf("%w: %s", errNotLoggedIn, id)
	}
	return handleOf(c), nil
}

// readers answers the handle of every usable connection, ordered by id.
func (m *Manager) readers() []readHandle {
	m.mu.RLock()
	out := make([]readHandle, 0, len(m.conns))
	for _, c := range m.conns {
		if m.usable(c) {
			out = append(out, handleOf(c))
		}
	}
	m.mu.RUnlock()
	slices.SortFunc(out, func(a, b readHandle) int { return cmp.Compare(a.id, b.id) })
	return out
}

// install swaps next in for id in memory only, for a change the records file
// already holds.
func (m *Manager) install(id string, next *connection) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.conns[id] = next
}

// commit is the one records writer: the whole document from every connection's
// durable record with next encoded for id (removed when next is nil), then the
// swap. A failed write installs nothing.
func (m *Manager) commit(ctx context.Context, id string, next *connection) error {
	m.persist.Lock()
	defer m.persist.Unlock()
	m.mu.RLock()
	doc := make([]connectionRecord, 0, len(m.conns)+1)
	for cid, c := range m.conns {
		if cid != id {
			doc = append(doc, c.durable)
		}
	}
	m.mu.RUnlock()
	var rec connectionRecord
	if next != nil {
		rec = encode(next)
		doc = append(doc, rec)
	}
	slices.SortFunc(doc, func(a, b connectionRecord) int { return cmp.Compare(a.ID, b.ID) })
	if err := m.records.write(ctx, doc); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if next == nil {
		delete(m.conns, id)
		return nil
	}
	next.durable = rec
	m.conns[id] = next
	return nil
}

// Load reads the records file and the credential store and replaces every
// connection with what they hold. It never notifies; the keeper's reload does.
func (m *Manager) Load(ctx context.Context) { m.load(ctx) }

// reload is the keeper's Load while persistence is unusable: it notifies once
// when health or any row's view changed.
func (m *Manager) reload(ctx context.Context) {
	if m.load(ctx) {
		m.notify(ctx, Change{})
	}
}

func (m *Manager) load(ctx context.Context) (changed bool) {
	m.persist.Lock()
	m.mu.RLock()
	store, before := m.store, m.signaturesLocked()
	m.mu.RUnlock()
	built := m.build(store)
	m.mu.Lock()
	old := m.conns
	m.store, m.health, m.conns = built.store, built.health, built.conns
	changed = !reflect.DeepEqual(before, m.signaturesLocked())
	logHealth := m.health != "" && m.health != m.lastHealth
	m.lastHealth = m.health
	m.mu.Unlock()
	m.persist.Unlock()
	if logHealth {
		slog.Error("forges: forge persistence unusable; every connection waits until it is repaired",
			"reason", built.health)
	}
	for _, c := range old {
		if c.conn != nil {
			c.conn.Close()
		}
	}
	m.reconcileGit(ctx)
	return changed
}

// signaturesLocked is every row's signature with the health they read under.
// The caller holds mu.
func (m *Manager) signaturesLocked() map[string]ConfiguredForge {
	out := make(map[string]ConfiguredForge, len(m.conns))
	for id, c := range m.conns {
		v := viewOf(c, m.health)
		out[id] = signature(&v)
	}
	return out
}

type built struct {
	store  creds.Store
	conns  map[string]*connection
	health string
}

// build is one Load's read, run under persist with no client of the current
// map touched. Every arm answers a whole map: a shared failure answers the rows
// with no client, so no row reads connected without one.
func (m *Manager) build(store creds.Store) built {
	reason := ""
	if store == nil {
		if fs, degraded := openCredentialStore(m.configDir); fs != nil {
			store = fs
		} else {
			reason = degraded
		}
	}
	loaded, err := m.records.load()
	if err != nil {
		reason = cmp.Or(reason, err.Error())
	}
	if reason != "" {
		return built{store: store, health: reason, conns: clientless(loaded.recs)}
	}
	conns, err := m.open(store, loaded.recs)
	var keys []string
	if err == nil {
		// Read even when nothing is swept, so a store that stopped reading is
		// found with no connection to read it through.
		keys, err = store.Keys()
	}
	if err != nil {
		for _, c := range conns {
			if c.conn != nil {
				c.conn.Close()
			}
		}
		return built{store: store, health: fmt.Sprintf(storeReadReason, err), conns: clientless(loaded.recs)}
	}
	if loaded.verified {
		sweep(store, keys, loaded.recs)
	}
	return built{store: store, conns: conns}
}

func clientless(recs []connectionRecord) map[string]*connection {
	out := make(map[string]*connection, len(recs))
	for i := range recs {
		out[recs[i].ID] = &connection{spec: recs[i].connectionSpec, durable: recs[i], status: decode(recs[i].Standing)}
	}
	return out
}

// open builds each record's connection. A store read failure is the store's,
// shared by every record, so it fails the whole map; a constructor refusal is
// that row's.
func (m *Manager) open(store creds.Store, recs []connectionRecord) (map[string]*connection, error) {
	out := make(map[string]*connection, len(recs))
	for i := range recs {
		rec := &recs[i]
		c := &connection{spec: rec.connectionSpec, durable: *rec, status: decode(rec.Standing)}
		out[rec.ID] = c
		cred, ok, err := store.Load(slotKey(rec.ID, rec.Slot))
		if err != nil {
			return out, err
		}
		c.login = cred.Account
		c.spec.OwnerScopes = loadedOwners(rec, cred.Account)
		if !ok {
			c.status = status{kind: statusReconnect, fault: &fault{
				message: emptySlotReason, code: forgeapi.CodeReconnectRequired, kind: forgeapi.KindUnauthorized.String(),
			}}
			continue
		}
		core, src, st, err := openConn(store, &c.spec, m.newCore, m.extraOpts)
		if err != nil {
			slog.Warn("forges: a connection cannot be opened with its saved settings", "connection", rec.ID, "error", err)
			c.status = status{kind: statusReconnect, fault: &fault{
				message: fmt.Sprintf("marotte cannot open this connection with its saved settings (%s). Sign in to it again",
					logsafe.Field(err.Error())),
				code: forgeapi.CodeReconnectRequired, kind: forgeapi.KindUnauthorized.String(),
			}}
			continue
		}
		c.conn, c.src, c.cred = core, src, st
		switch state := src.State(); state {
		case forgeapi.CredValid, forgeapi.CredRefreshDue, forgeapi.CredRefreshing:
		case forgeapi.CredReconnectRequired:
			c.status = decode(standingReconnect)
		case forgeapi.CredUnknown:
			return out, errors.New("its re-read for " + rec.ID + " failed")
		default:
			slog.Warn("forges: a credential state this build does not know", "connection", rec.ID, "state", state.String())
			return out, errors.New("the state of " + rec.ID + "'s credential is unknown to this build")
		}
	}
	return out, nil
}

// loadedOwners is rec's owner list as ownerScopes normalises it for the stored
// account; a list that fails its rule keeps its admissible entries, which the
// next commit for the id writes.
func loadedOwners(rec *connectionRecord, account string) []string {
	got, err := ownerScopes(rec.Kind, account, rec.OwnerScopes)
	if err == nil && slices.Equal(got, rec.OwnerScopes) {
		return rec.OwnerScopes
	}
	kept, refused := ownersKept(rec.Kind, account, rec.OwnerScopes)
	slog.Warn("forges: a connection's stored owners were normalised", "connection", rec.ID,
		"refused", refused, "kept", kept)
	return kept
}

// sweep deletes every stored key that is not the active slot of an admitted
// record: a removed row's keys, an inactive slot, a key an older build stored.
// A failed delete is retried by the next verified Load.
func sweep(store creds.Store, keys []string, recs []connectionRecord) {
	active := make(map[string]bool, len(recs))
	for i := range recs {
		active[slotKey(recs[i].ID, recs[i].Slot)] = true
	}
	for _, k := range keys {
		if active[k] {
			continue
		}
		if err := store.Delete(k); err != nil {
			slog.Warn("forges: a credential no connection owns could not be removed", "key", logsafe.Field(k), "error", err)
		}
	}
}

// RepoNames lists the display paths on the first page of connection id's
// repository list, with the account it was read through.
func (m *Manager) RepoNames(ctx context.Context, id string) (account string, names []string, err error) {
	h, err := m.reader(id)
	if err != nil {
		return "", nil, err
	}
	req, err := resolveList()
	if err != nil {
		return "", nil, err
	}
	page, err := m.repoPage(ctx, &h, &req, false)
	if err != nil {
		return "", nil, err
	}
	names = make([]string, 0, len(page.Repos))
	for i := range page.Repos {
		names = append(names, page.Repos[i].FullName)
	}
	return h.account, names, nil
}

// capabilities answers what h's instance and credential can do, through the
// account-keyed cache.
func (m *Manager) capabilities(ctx context.Context, h *readHandle, force bool) (Capabilities, error) {
	key := cacheKey{conn: h.id, account: h.account, scope: h.id, page: "capabilities"}
	return m.lists.caps.get(ctx, key, force, func(ctx context.Context) (Capabilities, error) {
		conn, err := h.core.ConnectionCaps(ctx)
		if err != nil {
			return Capabilities{}, err
		}
		grant, err := h.core.GrantCaps(ctx)
		if err != nil {
			return Capabilities{}, err
		}
		caps := capabilitiesOf(conn, grant)
		caps.Account = h.account
		return caps, nil
	})
}
