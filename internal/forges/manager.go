// Manager lists the configured forges: the connection records Marotte
// keeps beside the library's credential store. A thin read-through with
// caching.

package forges

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
	"github.com/cplieger/marotte/internal/logsafe"
	"golang.org/x/sync/singleflight"
)

// ConfiguredForge is one connected forge backend: a connection record's row.
type ConfiguredForge struct {
	ID        string `json:"id"`
	Kind      Kind   `json:"kind"`
	Host      string `json:"host"`
	Username  string `json:"username,omitempty"`
	Email     string `json:"email,omitempty"`
	LastError string `json:"last_error,omitempty"`
	// ErrorCode and ErrorKind are LastError's code and kind in the error
	// envelope's terms. The code connection_unusable marks a row no request can
	// serve until the record file or the credential store is repaired.
	ErrorCode string `json:"error_code,omitempty"`
	ErrorKind string `json:"error_kind,omitempty"`
	// webBase is the record's web base URL, which the clone join compares a
	// remote against; it is not on the wire.
	webBase string
	// OwnerScopes are the owners a present cycle reads beside the connection's
	// own pull requests.
	OwnerScopes []string `json:"owner_scopes,omitempty"`
	LastProbed  int64    `json:"last_probed,omitempty"`
	// RetryAfterS is the wait the refusal behind LastError asked for, counted
	// from LastProbed.
	RetryAfterS int64 `json:"retry_after_s,omitempty"`
	Connected   bool  `json:"connected"`
	// ReconnectRequired says the stored credential can be neither used nor
	// renewed: only a new sign-in revives the connection.
	ReconnectRequired bool `json:"reconnect_required"`
	// probeFailed says the row's error is a probe's refusal, which no record
	// or store read restates, so a refresh carries it.
	probeFailed bool
}

// Manager owns the list of configured forges.
type Manager struct {
	forges map[string]*ConfiguredForge
	// lists caches the forge LISTING calls, which is a separate concern from
	// the forge list this type is named for: that one is read off the record
	// file, while a listing is an upstream API call. See list_cache.go.
	lists *listCaches
	conns *connectionStore
	// store is nil when it could not be opened; storeReason then says why.
	store *creds.FileStore
	// records are the connection records the last Refresh listed, and
	// recordsReason is why none of them may be used, empty when they may.
	records map[string]connectionRecord
	clients *clientFactory
	// executable is the binary git is told to run as the credential helper.
	executable    func() (string, error)
	cacheAt       time.Time
	refreshSF     singleflight.Group
	probeSF       singleflight.Group
	storeReason   string
	recordsReason string
	// lastRecordsErr is the last record-file verdict logged, so a 30 s refresh
	// does not repeat it.
	lastRecordsErr string
	configDir      string
	ttl            time.Duration
	mu             sync.RWMutex
	helpersOnce    sync.Once
}

// NewManager constructs a Manager over the connection records and the
// credential store in configDir.
func NewManager(configDir string) *Manager {
	store, reason := openCredentialStore(configDir)
	return &Manager{
		forges:      make(map[string]*ConfiguredForge),
		lists:       newListCaches(),
		conns:       &connectionStore{path: filepath.Join(configDir, connectionsFileName)},
		clients:     newClientFactory(),
		store:       store,
		storeReason: reason,
		configDir:   configDir,
		executable:  resolvedExecutable,
		ttl:         30 * time.Second,
	}
}

// MakeID returns the canonical ID for a kind+host pair.
func MakeID(kind Kind, host string) string {
	if host == "" {
		host = kind.DefaultHost()
	}
	return fmt.Sprintf("%s:%s", kind, host)
}

// List returns all configured forges, refreshing them if the cache is
// stale.
func (m *Manager) List(ctx context.Context) []ConfiguredForge {
	m.mu.RLock()
	stale := time.Since(m.cacheAt) > m.ttl
	m.mu.RUnlock()
	if stale {
		ch := m.refreshSF.DoChan("refresh", func() (any, error) {
			return nil, m.Refresh(context.WithoutCancel(ctx))
		})
		select {
		case <-ch:
		case <-ctx.Done():
			return nil
		}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ConfiguredForge, 0, len(m.forges))
	for _, f := range m.forges {
		out = append(out, *f)
	}
	slices.SortStableFunc(out, func(a, b ConfiguredForge) int {
		return cmp.Or(
			cmp.Compare(a.Kind, b.Kind),
			cmp.Compare(a.Host, b.Host),
		)
	})
	return out
}

// Get returns the configured forge with the given ID, or nil.
func (m *Manager) Get(id string) *ConfiguredForge {
	m.mu.RLock()
	defer m.mu.RUnlock()
	f, ok := m.forges[id]
	if !ok {
		return nil
	}
	cp := *f
	return &cp
}

// Refresh rebuilds the forge list from the connection records, sending no
// request: Probe is the sole network path. The first call also registers git's
// credential helper for every connection record.
func (m *Manager) Refresh(ctx context.Context) error {
	out := make(map[string]*ConfiguredForge)
	recs, reason := m.addRecordForges(out)

	m.mergeForges(out, recs, reason)
	m.clients.retain(recs)
	m.helpersOnce.Do(func() { m.reconcileHelpers(ctx) })
	return nil
}

// addRecordForges adds one row per connection record and answers the records
// with why none may be used. Reading the records and the store makes no
// request.
func (m *Manager) addRecordForges(out map[string]*ConfiguredForge) (byID map[string]connectionRecord, reason string) {
	recs, err := m.conns.load()
	reason = m.storeReason
	if err != nil {
		reason = err.Error()
	}
	m.logRecordsVerdict(err)
	byID = make(map[string]connectionRecord, len(recs))
	for i := range recs {
		rec := &recs[i]
		byID[rec.ID] = *rec
		row := &ConfiguredForge{
			ID: rec.ID, Kind: rec.Kind, Host: rec.Host, webBase: rec.webBase(), OwnerScopes: rec.OwnerScopes,
		}
		if reason != "" {
			row.LastError, row.ErrorCode = reason, codeConnectionUnusable
		} else {
			m.fillFromCredential(row, rec)
		}
		out[rec.ID] = row
	}
	return byID, reason
}

func (m *Manager) fillFromCredential(row *ConfiguredForge, rec *connectionRecord) {
	cred, ok, err := m.store.Load(row.ID)
	switch {
	case err != nil:
		row.LastError = "the stored credential could not be read: " + err.Error()
	case !ok:
		row.LastError = "no stored credential for this connection. Connect it again"
		row.ReconnectRequired, row.ErrorCode = true, forgeapi.CodeReconnectRequired
		row.ErrorKind = forgeapi.KindUnauthorized.String()
	default:
		row.Username = cred.Account
		row.Connected = true
		m.fillStanding(row, rec)
	}
}

// reconnectReason is the error a row whose stored credential the library
// holds as reconnect-required carries.
const reconnectReason = "the stored credential can be neither used nor renewed. Sign in to this forge again"

// fillStanding marks row reconnect-required when the library's source over
// rec's credential says so. Reading the standing sends nothing.
func (m *Manager) fillStanding(row *ConfiguredForge, rec *connectionRecord) {
	src, err := m.clients.sourceFor(m.store, rec)
	if err != nil || src.State() != forgeapi.CredReconnectRequired {
		return
	}
	row.ReconnectRequired, row.Connected, row.LastError = true, false, reconnectReason
	row.ErrorCode, row.ErrorKind = forgeapi.CodeReconnectRequired, forgeapi.KindUnauthorized.String()
}

// recordFailure records err as the row's error in the error envelope's terms.
// The message carries upstream text, so it is sanitized and bounded first.
func (f *ConfiguredForge) recordFailure(err error) {
	f.Connected = false
	if fe, ok := errors.AsType[*forgeapi.Error](err); ok {
		env := envelopeFor(fe)
		f.LastError, f.ErrorCode, f.ErrorKind, f.RetryAfterS = env.Error, env.Code, env.Kind, env.RetryAfterS
		return
	}
	f.LastError, f.ErrorCode, f.ErrorKind, f.RetryAfterS = logsafe.Field(err.Error()), "", "", 0
}

func (m *Manager) logRecordsVerdict(err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	m.mu.Lock()
	changed := msg != m.lastRecordsErr
	m.lastRecordsErr = msg
	m.mu.Unlock()
	if changed && err != nil {
		slog.Error("forges: connection records unusable; their rows stay disconnected", "error", err)
	}
}

// mergeForges swaps in the freshly-read forge set and records, carrying across
// what only Probe populates: Email, LastProbed and a probe's refusal. A row the
// fresh read itself marks unusable keeps the fresh verdict.
func (m *Manager) mergeForges(out map[string]*ConfiguredForge, recs map[string]connectionRecord, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records, m.recordsReason = recs, reason
	for id, f := range out {
		prev, ok := m.forges[id]
		if !ok {
			continue
		}
		if prev.Email != "" && f.Email == "" {
			f.Email = prev.Email
		}
		if prev.LastProbed > f.LastProbed {
			f.LastProbed = prev.LastProbed
		}
		if prev.probeFailed && f.Connected {
			f.Connected, f.ReconnectRequired, f.probeFailed = false, prev.ReconnectRequired, true
			f.LastError, f.ErrorCode, f.ErrorKind, f.RetryAfterS = prev.LastError, prev.ErrorCode, prev.ErrorKind, prev.RetryAfterS
		}
	}
	m.forges = out
	m.cacheAt = time.Now()
}

// Invalidate clears the cache so the next List/Get reloads, and drops the
// cached repo and PR listings with it: a connection change decides which
// repositories are visible at all, so keeping them would serve one account's
// listings after another has signed in.
func (m *Manager) Invalidate() {
	m.mu.Lock()
	m.cacheAt = time.Time{}
	m.mu.Unlock()
	m.lists.clear()
}

// Probe runs a Whoami against the forge to verify auth still works
// and updates the Connected/LastProbed/LastError fields.
func (m *Manager) Probe(ctx context.Context, id string) error {
	m.mu.RLock()
	f, ok := m.forges[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("forges: unknown id %q", id)
	}
	fc, err := m.clientOf(f)
	if err != nil {
		return err
	}
	_, err = m.probe(ctx, fc)
	return err
}

// probeBudget bounds a probe, which runs detached from the callers sharing it
// so one leaving does not end it for the others.
const probeBudget = 30 * time.Second

// probe reads fc's account and records the verdict on its connection's row.
func (m *Manager) probe(ctx context.Context, fc forgeClient) (forgeapi.Account, error) {
	ch := m.probeSF.DoChan(fc.id, func() (any, error) {
		pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), probeBudget)
		defer cancel()
		acct, err := fc.core.Whoami(pctx)
		err = m.recordProbe(fc.id, &acct, err)
		return acct, err
	})
	select {
	case res := <-ch:
		acct, _ := res.Val.(forgeapi.Account)
		return acct, res.Err
	case <-ctx.Done():
		return forgeapi.Account{}, ctx.Err()
	}
}

// recordProbe records a probe's verdict on id's row and answers err.
func (m *Manager) recordProbe(id string, acct *forgeapi.Account, err error) error {
	now := time.Now().UnixMilli()
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.forges[id]
	if !ok {
		return err
	}
	f.LastProbed = now
	if err != nil {
		f.recordFailure(err)
		f.ReconnectRequired = f.ReconnectRequired || isReconnectRequired(err)
		f.probeFailed = true
		return err
	}
	f.Connected, f.probeFailed = true, false
	f.ReconnectRequired = false
	f.LastError, f.ErrorCode, f.ErrorKind, f.RetryAfterS = "", "", "", 0
	if acct.Login != "" {
		f.Username = acct.Login
	}
	if acct.Email != "" {
		f.Email = acct.Email
	}
	return nil
}

// errConnectionUnusable marks a record-backed connection the server cannot
// serve until its record file or credential store is repaired.
var errConnectionUnusable = errors.New("forges: connection unusable")

// codeConnectionUnusable is the row code of errConnectionUnusable.
const codeConnectionUnusable = "connection_unusable"

// forgeClient is one connection's forgeapi client, the credential source behind
// it, and the family its repository ids decode in.
type forgeClient struct {
	core   forgeapi.Core
	cred   forgeapi.CredentialSource
	id     string
	family forgeapi.Family
}

// client answers connection id's forgeapi client.
func (m *Manager) client(id string) (forgeClient, error) {
	f := m.Get(id)
	if f == nil {
		return forgeClient{}, fmt.Errorf("forges: unknown id %q", id)
	}
	return m.clientOf(f)
}

// clientOf answers the client of f's connection record. A row whose record a
// refresh removed meanwhile answers ErrNotLoggedIn.
func (m *Manager) clientOf(f *ConfiguredForge) (forgeClient, error) {
	m.mu.RLock()
	rec, recorded := m.records[f.ID]
	reason := m.recordsReason
	m.mu.RUnlock()
	if !recorded {
		return forgeClient{}, fmt.Errorf("%w: %s connects through a connection record", ErrNotLoggedIn, f.Kind)
	}
	if reason != "" {
		return forgeClient{}, fmt.Errorf("%w: %s", errConnectionUnusable, reason)
	}
	c, err := m.clients.clientFor(m.store, &rec)
	if err != nil {
		return forgeClient{}, err
	}
	return forgeClient{core: c.core, cred: c.cred, id: f.ID, family: rec.Kind.family()}, nil
}

// RepoNames lists the display paths of the repositories on the first page of
// connection id's repository list.
func (m *Manager) RepoNames(ctx context.Context, id string) ([]string, error) {
	fc, err := m.client(id)
	if err != nil {
		return nil, err
	}
	req, err := resolveList()
	if err != nil {
		return nil, err
	}
	page, err := m.repoPage(ctx, fc, &req, false)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(page.Repos))
	for i := range page.Repos {
		names = append(names, page.Repos[i].FullName)
	}
	return names, nil
}

// connect stores token as a static credential for the instance rec addresses,
// through connectCredential.
func (m *Manager) connect(ctx context.Context, rec *connectionRecord, token string) error {
	return m.connectCredential(ctx, rec, &creds.Record{Kind: forgeapi.CredKindStaticPAT, Token: token, Issued: time.Now()})
}

// connectCredential verifies cred's token against the instance rec addresses,
// completes cred with the account that read names, then stores it and rec,
// registers git's credential helper for the origin and scrubs the host's
// cleartext git credentials. A refused verification stores nothing.
func (m *Manager) connectCredential(ctx context.Context, rec *connectionRecord, cred *creds.Record) error {
	if cred.Token == "" {
		return errors.New("forges: empty token")
	}
	prev, err := m.writableRecord(rec.ID)
	if err != nil {
		return err
	}
	core, err := m.clients.verifier(rec, cred.Token)
	if err != nil {
		return err
	}
	defer core.Close()
	acct, err := core.Whoami(ctx)
	if err != nil {
		return err
	}
	cred.Family, cred.WebBaseURL, cred.Account = rec.Kind.family(), rec.webBase(), acct.Login
	if len(cred.Scopes) == 0 {
		cred.Scopes = acct.Scopes
	}
	old, hadOld, _ := m.store.Load(rec.ID)
	if err := m.store.Save(rec.ID, *cred); err != nil {
		return fmt.Errorf("forges: save the credential for %s: %w", rec.ID, err)
	}
	stored := *rec
	if prev != nil {
		stored.HelperValue, stored.RotationCursor = prev.HelperValue, prev.RotationCursor
		stored.OwnerScopes = prev.OwnerScopes
	}
	if err := m.putRecord(ctx, &stored); err != nil {
		m.restoreCredential(rec.ID, &old, hadOld)
		return err
	}
	// The new credential owes nothing to the old one's refusal.
	m.mu.Lock()
	delete(m.forges, rec.ID)
	m.mu.Unlock()
	if prev != nil && prev.webBase() != stored.webBase() {
		m.unregisterHelper(ctx, prev)
	}
	m.registerHelper(ctx, &stored)
	scrubCleartext(ctx, rec.Host)
	return nil
}

// restoreCredential puts back what id's store entry held before a connect
// whose record write failed: the previous credential, or nothing.
func (m *Manager) restoreCredential(id string, old *creds.Record, hadOld bool) {
	err := m.store.Delete(id)
	if hadOld {
		err = m.store.Save(id, *old)
	}
	if err != nil {
		slog.Warn("forges: credential not restored after a failed connect", "connection", id, "error", err)
	}
}

// disconnect removes f's connection record, then the helper pair and the
// credential it no longer references. A connection with no record is already
// gone.
func (m *Manager) disconnect(ctx context.Context, f *ConfiguredForge) error {
	rec, err := m.writableRecord(f.ID)
	if err != nil || rec == nil {
		return err
	}
	if err := m.conns.update(ctx, func(cur []connectionRecord) []connectionRecord {
		return slices.DeleteFunc(cur, func(r connectionRecord) bool { return r.ID == rec.ID })
	}); err != nil {
		return err
	}
	m.unregisterHelper(ctx, rec)
	scrubCleartext(ctx, rec.Host)
	if err := m.store.Delete(rec.ID); err != nil {
		return fmt.Errorf("forges: %s is disconnected, but its stored credential could not be deleted: %w", rec.ID, err)
	}
	return nil
}

// errNoRecord answers a change to a connection whose record a disconnect
// removed meanwhile.
var errNoRecord = errors.New("forges: the connection has no record. Connect it again")

// setOwnerScopes replaces f's owner scopes with owners as ownerScopes stores
// them, in one write that keeps every other field of the record as the file
// holds it, and answers what it stored. The row carries them once this returns,
// so the next present cycle reads them.
func (m *Manager) setOwnerScopes(ctx context.Context, f *ConfiguredForge, owners []string) ([]string, error) {
	scopes, err := ownerScopes(f.Kind, f.Username, owners)
	if err != nil {
		return nil, err
	}
	if rec, err := m.writableRecord(f.ID); err != nil || rec == nil {
		return nil, cmp.Or(err, errNoRecord)
	}
	found := false
	if err := m.conns.update(ctx, func(cur []connectionRecord) []connectionRecord {
		if i := slices.IndexFunc(cur, func(r connectionRecord) bool { return r.ID == f.ID }); i >= 0 {
			cur[i].OwnerScopes, found = scopes, true
		}
		return cur
	}); err != nil {
		return nil, err
	}
	if !found {
		return nil, errNoRecord
	}
	return scopes, m.Refresh(ctx)
}

// writableRecord answers the record id names, nil when there is none, once the
// store and the record file can both be written.
func (m *Manager) writableRecord(id string) (*connectionRecord, error) {
	if m.store == nil {
		return nil, fmt.Errorf("%w: %s", errConnectionUnusable, m.storeReason)
	}
	recs, err := m.conns.load()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errConnectionUnusable, err)
	}
	for i := range recs {
		if recs[i].ID == id {
			return &recs[i], nil
		}
	}
	return nil, nil
}

// putRecord replaces the record with rec's id, or appends rec.
func (m *Manager) putRecord(ctx context.Context, rec *connectionRecord) error {
	return m.conns.update(ctx, func(cur []connectionRecord) []connectionRecord {
		if i := slices.IndexFunc(cur, func(r connectionRecord) bool { return r.ID == rec.ID }); i >= 0 {
			cur[i] = *rec
			return cur
		}
		return append(cur, *rec)
	})
}
