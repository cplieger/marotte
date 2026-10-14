package forges

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
)

// testSignIn is the sign-in id every seeded connection carries.
const testSignIn = "test-sign-in"

// specFor is a valid connection on host, signed in at slot 0.
func specFor(kind Kind, host string) connectionSpec {
	return connectionSpec{ID: MakeID(kind, host), Kind: kind, Host: host, SignIn: testSignIn}
}

func githubRecord() connectionSpec {
	s := specFor(KindGitHub, "github.com")
	s.WebBaseURL = "https://github.com"
	return s
}

// writeRecords writes recs as configDir's records file at mode 0600, the bytes
// a commit writes.
func writeRecords(t *testing.T, configDir string, recs ...connectionRecord) {
	t.Helper()
	if err := newRecordsFile(configDir).write(t.Context(), recs); err != nil {
		t.Fatalf("Setup: write the records: %v", err)
	}
}

// readRecords is the records file as the git helper reads it.
func readRecords(t *testing.T, configDir string) []connectionRecord {
	t.Helper()
	recs, err := newRecordsFile(configDir).read()
	if err != nil {
		t.Fatalf("read the records: %v", err)
	}
	return recs
}

func openTestStore(t *testing.T, configDir string) *creds.FileStore {
	t.Helper()
	store, reason := openCredentialStore(configDir)
	if store == nil {
		t.Fatalf("Setup: open the store: %s", reason)
	}
	return store
}

// staticCredential is a static token for spec signed in as account.
func staticCredential(spec *connectionSpec, account, token string) creds.Record {
	return creds.Record{
		Family: spec.Kind.family(), WebBaseURL: spec.webBase(), Kind: forgeapi.CredKindStaticPAT,
		Token: token, Issued: time.Now().Truncate(time.Second), Account: account,
	}
}

// seedConnections writes specs as configDir's records with no refusal, each
// with a static token for account under its active slot.
func seedConnections(t *testing.T, configDir, account string, specs ...connectionSpec) {
	t.Helper()
	store := openTestStore(t, configDir)
	recs := make([]connectionRecord, 0, len(specs))
	for i := range specs {
		s := &specs[i]
		recs = append(recs, connectionRecord{connectionSpec: *s})
		if err := store.Save(slotKey(s.ID, s.Slot), staticCredential(s, account, "test-token")); err != nil {
			t.Fatalf("Setup: save %s's credential: %v", s.ID, err)
		}
	}
	writeRecords(t, configDir, recs...)
}

// changeLog records every change a manager notifies.
type changeLog struct {
	got []Change
	mu  sync.Mutex
}

func (l *changeLog) notify(_ context.Context, c Change) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.got = append(l.got, c)
}

func (l *changeLog) all() []Change {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.got)
}

// newManager is a Manager over configDir with a fixed helper binary whose
// clients are core when it is not nil, not yet loaded.
func newManager(t *testing.T, configDir string, core forgeapi.Core) (*Manager, *changeLog) {
	t.Helper()
	log := &changeLog{}
	m := NewManager(configDir, log.notify)
	m.executable = func() (string, error) { return testHelperBin, nil }
	if core != nil {
		m.newCore = func(*connectionSpec, []forgeapi.Option) (forgeapi.Core, error) { return core, nil }
	}
	return m, log
}

// loadedManager is newManager after its boot Load.
func loadedManager(t *testing.T, configDir string, core forgeapi.Core) (*Manager, *changeLog) {
	t.Helper()
	m, log := newManager(t, configDir, core)
	m.Load(t.Context())
	return m, log
}

func recordManager(t *testing.T, core forgeapi.Core) *Manager {
	t.Helper()
	rec := githubRecord()
	return recordManagerFor(t, &rec, core)
}

// recordManagerFor is a loaded manager holding spec signed in as bob, its
// client core.
func recordManagerFor(t *testing.T, spec *connectionSpec, core forgeapi.Core) *Manager {
	t.Helper()
	stubPath(t)
	cfg := t.TempDir()
	seedConnections(t, cfg, "bob", *spec)
	m, _ := loadedManager(t, cfg, core)
	return m
}

// widenStore makes m's credential store unreadable, as a directory widened to
// 0750 is, and reloads, so every row reads unusable; the returned func narrows
// it again.
func widenStore(t *testing.T, m *Manager) (restore func()) {
	t.Helper()
	dir := filepath.Join(m.configDir, credentialStoreDir)
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatalf("Setup: widen the store: %v", err)
	}
	restore = func() {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Errorf("narrow the store: %v", err)
		}
	}
	t.Cleanup(restore)
	m.Load(t.Context())
	return restore
}

// viewFor is id's view, failing the test when it has no row.
func viewFor(t *testing.T, m *Manager, id string) ConfiguredForge {
	t.Helper()
	v, ok := m.view(id)
	if !ok {
		t.Fatalf("no row for %s", id)
	}
	return v
}

// dropCredential deletes connection id's active credential and reloads m, so
// its row reads reconnect.
func dropCredential(t *testing.T, m *Manager, id string) {
	t.Helper()
	if err := openTestStore(t, m.configDir).Delete(slotKey(id, 0)); err != nil {
		t.Fatalf("Setup: delete %s's credential: %v", id, err)
	}
	m.Load(t.Context())
}

// addConnections adds specs, each with a static token for account, to m's
// records and store, and reloads m.
func addConnections(t *testing.T, m *Manager, account string, specs ...connectionSpec) {
	t.Helper()
	store := openTestStore(t, m.configDir)
	recs := readRecords(t, m.configDir)
	for i := range specs {
		s := &specs[i]
		recs = slices.DeleteFunc(recs, func(r connectionRecord) bool { return r.ID == s.ID })
		recs = append(recs, connectionRecord{connectionSpec: *s})
		if err := store.Save(slotKey(s.ID, s.Slot), staticCredential(s, account, "test-token")); err != nil {
			t.Fatalf("Setup: save %s's credential: %v", s.ID, err)
		}
	}
	writeRecords(t, m.configDir, recs...)
	m.Load(t.Context())
}

// whoCore answers Whoami from a script of errors, a nil one answering alice,
// the last repeating; block, when set, holds each call until it closes or the
// call's context ends. Its capabilities reads are counted.
type whoCore struct {
	fakeCore
	block    chan struct{}
	script   []error
	cursor   forgeapi.RotationCursor
	whoamis  int
	capsRead int
	mu       sync.Mutex
}

func (c *whoCore) answer(errs ...error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.script = errs
}

func (c *whoCore) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.whoamis
}

func (c *whoCore) Whoami(ctx context.Context) (forgeapi.Account, error) {
	c.mu.Lock()
	c.whoamis++
	var err error
	if len(c.script) > 0 {
		err = c.script[0]
		if len(c.script) > 1 {
			c.script = c.script[1:]
		}
	}
	block := c.block
	c.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return forgeapi.Account{}, ctx.Err()
		}
	}
	if err != nil {
		return forgeapi.Account{}, err
	}
	return forgeapi.Account{Login: "alice", Email: "alice@example.test"}, nil
}

func (c *whoCore) BudgetState() forgeapi.BudgetState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return forgeapi.BudgetState{Remaining: forgeapi.BudgetRemainingUnknown, RotationCursor: c.cursor}
}

func (c *whoCore) ConnectionCaps(context.Context) (forgeapi.ConnectionCaps, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.capsRead++
	return forgeapi.ConnectionCaps{}, nil
}

func (*whoCore) GrantCaps(context.Context) (forgeapi.GrantCaps, error) {
	return forgeapi.GrantCaps{}, nil
}

// checkManager is a loaded manager holding github.com signed in as bob over
// core, with its change log, git isolated.
func checkManager(t *testing.T, core forgeapi.Core) (*Manager, *changeLog) {
	t.Helper()
	isolateGit(t)
	cfg := t.TempDir()
	seedConnections(t, cfg, "bob", githubRecord())
	return loadedManager(t, cfg, core)
}

var (
	errTransient    = &forgeapi.Error{Op: "Whoami", Kind: forgeapi.KindTransient, Message: "no route to host"}
	errForbidden    = &forgeapi.Error{Op: "Whoami", Kind: forgeapi.KindForbidden, Status: 403, Message: "an IP allowlist refused"}
	errUnauthorized = &forgeapi.Error{Op: "Whoami", Kind: forgeapi.KindUnauthorized, Status: 401}
)

// failingWrites makes m's records writes fail while the file stays readable
// and unchanged; the returned func lets them through again.
func failingWrites(t *testing.T, m *Manager) (restore func()) {
	t.Helper()
	m.persist.Lock()
	real := m.records.writeFile
	var fail atomicBool
	fail.set(true)
	m.records.writeFile = func(ctx context.Context, path string, data []byte) error {
		if fail.get() {
			return errors.New("the disk refused the write")
		}
		return real(ctx, path, data)
	}
	m.persist.Unlock()
	return func() { fail.set(false) }
}

type atomicBool struct {
	v  bool
	mu sync.Mutex
}

func (b *atomicBool) set(v bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.v = v
}

func (b *atomicBool) get() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.v
}

// helperAnswer is what the git credential helper answers git for origin's
// host over configDir, "" for nothing.
func helperAnswer(t *testing.T, configDir, host string) string {
	t.Helper()
	code, out, _ := runHelper(t, configDir, "get", "protocol=https\nhost="+host+"\n\n")
	if code != 0 {
		t.Fatalf("the credential helper exited %d", code)
	}
	return out
}

// restarted is a second manager over m's directory after its boot Load, as a
// restart reads it, building clients as m does.
func restarted(t *testing.T, m *Manager) *Manager {
	t.Helper()
	again, _ := newManager(t, m.configDir, nil)
	again.newCore, again.extraOpts = m.newCore, m.extraOpts
	again.Load(t.Context())
	return again
}

// fileBytes is path's content, "" when it is absent.
func fileBytes(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// storeLog is a credential store that records each Save and Delete by key, in
// order, beside an event a test notes, and can fail or slow a Delete.
type storeLog struct {
	*creds.FileStore
	onDelete func(key string) error
	events   []string
	mu       sync.Mutex
}

func (s *storeLog) note(event string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

func (s *storeLog) log() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.events)
}

func (s *storeLog) Save(key string, rec creds.Record) error {
	s.note("save " + key)
	return s.FileStore.Save(key, rec)
}

func (s *storeLog) Delete(key string) error {
	s.note("delete " + key)
	if s.onDelete != nil {
		if err := s.onDelete(key); err != nil {
			return err
		}
	}
	return s.FileStore.Delete(key)
}

// logStore puts a storeLog over m's store, which every later mutation uses.
func logStore(t *testing.T, m *Manager) *storeLog {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	fs, ok := m.store.(*creds.FileStore)
	if !ok {
		t.Fatalf("Setup: m's store is %T, want the file store", m.store)
	}
	s := &storeLog{FileStore: fs}
	m.store = s
	return s
}

// stubForge answers a family client's requests by path, as the wire under it,
// and records each request's method and path.
type stubForge struct {
	routes map[string]http.HandlerFunc
	seen   []string
	// validators is each request's If-None-Match, in order.
	validators []string
	mu         sync.Mutex
}

func newStubForge() *stubForge { return &stubForge{routes: map[string]http.HandlerFunc{}} }

func (s *stubForge) handle(path string, h http.HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[path] = h
}

// answer serves path with status and body, a JSON content type and headers.
func (s *stubForge) answer(path string, status int, body string, headers ...string) {
	s.handle(path, func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i+1 < len(headers); i += 2 {
			w.Header().Set(headers[i], headers[i+1])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
}

func (s *stubForge) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.seen)
}

func (s *stubForge) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	s.seen = append(s.seen, r.Method+" "+r.URL.Path)
	s.validators = append(s.validators, r.Header.Get("If-None-Match"))
	h, ok := s.routes[r.URL.Path]
	s.mu.Unlock()
	rec := httptest.NewRecorder()
	if ok {
		h(rec, r)
	} else {
		rec.Header().Set("Content-Type", "application/json")
		rec.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(rec, `{"message":"Not Found"}`)
	}
	resp := rec.Result()
	resp.Request = r
	return resp, nil
}

func (s *stubForge) validatorsOf() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.validators)
}

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("answer = %d %s, want 200", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
}
