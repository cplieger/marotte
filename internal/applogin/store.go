// Package applogin is marotte's sign-in: the access library's file store, the constructor chain
// over it, and the request facts, origin and policy the library's configuration reads.
package applogin

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cplieger/access"
	"github.com/cplieger/access/account"
	"github.com/cplieger/access/oidc"
	"github.com/cplieger/marotte/internal/filemode"
)

// snapshot is one published state. Nothing in a published snapshot is ever changed: a write
// replaces a record, map or slice in the writer's private copy (store_tx.go).
type snapshot struct {
	tokens     map[access.HashKey]storeToken
	failures   map[account.FailureRef]storeFailure
	byHandle   map[string]int
	byOIDC     map[access.OIDCIdentity]int
	sessions   map[access.HashKey]*storeSession
	sessByID   map[string]access.HashKey
	passkeys   map[int64][]storePasskey // by UserID, ascending ID
	credIDs    map[string]int64         // credential ID → passkey ID, across users (rule 4)
	keys       map[access.HashKey]*storeKey
	failureKey access.Secret
	byName     map[string]int
	// logEvicted holds each class that ever held an entry, with its Keep evictions.
	logEvicted map[account.LogClass]uint64
	keyByID    map[int64]access.HashKey
	log        map[account.LogClass][]storeLogEntry // ascending Seq per class
	// oidc is built from policy.Provider and never serialized.
	oidc         *oidc.Provider
	users        []storeUser // ascending ID
	deviceKeys   []access.Secret
	attributions []storeAttribution // ascending ID
	policy       storePolicy
	nextIDs      nextIDs
	// providerGen, memory only, names the provider record oidc may be attached for.
	providerGen uint64
	seenSeq     uint64
}

func emptySnapshot() *snapshot {
	return &snapshot{
		byName:     map[string]int{},
		byHandle:   map[string]int{},
		byOIDC:     map[access.OIDCIdentity]int{},
		sessions:   map[access.HashKey]*storeSession{},
		sessByID:   map[string]access.HashKey{},
		passkeys:   map[int64][]storePasskey{},
		credIDs:    map[string]int64{},
		keys:       map[access.HashKey]*storeKey{},
		keyByID:    map[int64]access.HashKey{},
		tokens:     map[access.HashKey]storeToken{},
		failures:   map[account.FailureRef]storeFailure{},
		log:        map[account.LogClass][]storeLogEntry{},
		logEvicted: map[account.LogClass]uint64{},
		nextIDs:    freshNextIDs(),
		policy:     defaultPolicy(),
	}
}

// clone is the writer's private copy: every container is shared until the group's first write to
// it clones it (store_tx.go).
func (s *snapshot) clone() *snapshot {
	c := *s
	return &c
}

func (s *snapshot) reindexUsers() {
	s.byName = make(map[string]int, len(s.users))
	s.byHandle = make(map[string]int, len(s.users))
	s.byOIDC = make(map[access.OIDCIdentity]int, len(s.users))
	for i := range s.users {
		s.indexUser(i)
	}
}

// indexUser adds s.users[i] to the three claim indexes.
func (s *snapshot) indexUser(i int) {
	u := &s.users[i]
	s.byName[u.Username] = i
	if len(u.WebAuthnHandle) > 0 {
		s.byHandle[string(u.WebAuthnHandle)] = i
	}
	if !u.OIDC.IsZero() {
		s.byOIDC[u.OIDC] = i
	}
}

func (s *snapshot) userIndex(id int64) (int, bool) {
	return slices.BinarySearchFunc(s.users, id, func(u storeUser, id int64) int { return cmp.Compare(u.ID, id) })
}

// owner is the single owner SingleOwnerPolicy allows; nil before setup.
func (s *snapshot) owner() *storeUser {
	if len(s.users) == 0 {
		return nil
	}
	return &s.users[0]
}

func sortedKeys[K cmp.Ordered, V any](m map[K]V) []K { return slices.Sorted(maps.Keys(m)) }

// mergeLog merges the per-class slices into one Seq-ordered slice of the entries with
// after < Seq < before (before 0 is unbounded), at most limit of them (0 is unbounded).
func mergeLog(log map[account.LogClass][]storeLogEntry, after, before uint64, limit int, desc bool) []storeLogEntry {
	parts := logParts(log, after, before)
	total := 0
	for _, p := range parts {
		total += len(p)
	}
	if limit <= 0 || limit > total {
		limit = total
	}
	out := make([]storeLogEntry, 0, limit)
	pos := make([]int, len(parts))
	if desc {
		for i, p := range parts {
			pos[i] = len(p) - 1
		}
	}
	for len(out) < limit {
		best := nextPart(parts, pos, desc)
		out = append(out, parts[best][pos[best]])
		if desc {
			pos[best]--
		} else {
			pos[best]++
		}
	}
	return out
}

// logParts is each class's run of entries with after < Seq < before.
func logParts(log map[account.LogClass][]storeLogEntry, after, before uint64) [][]storeLogEntry {
	var parts [][]storeLogEntry
	for _, class := range account.LogClasses() {
		es := log[class]
		lo, _ := slices.BinarySearchFunc(es, after+1, seqCmp)
		hi := len(es)
		if before != 0 {
			hi, _ = slices.BinarySearchFunc(es, before, seqCmp)
		}
		if lo < hi {
			parts = append(parts, es[lo:hi])
		}
	}
	return parts
}

// nextPart is the part whose entry at pos comes next in the merge's order.
func nextPart(parts [][]storeLogEntry, pos []int, desc bool) int {
	best := -1
	for i, p := range parts {
		if pos[i] < 0 || pos[i] >= len(p) {
			continue
		}
		if best < 0 || (p[pos[i]].Event.Seq < parts[best][pos[best]].Event.Seq) != desc {
			best = i
		}
	}
	return best
}

func seqCmp(e storeLogEntry, seq uint64) int { //nolint:gocritic // hugeParam: slices.BinarySearchFunc's signature
	return cmp.Compare(e.Event.Seq, seq)
}

// Store is marotte's account.Store over /config/access.json.
//
//deadset:ignore DS1501 -- composition opens and holds it once the login is wired
type Store struct {
	disk filemode.Disk
	ctx  context.Context
	stop context.CancelFunc
	cur  atomic.Pointer[snapshot]
	w    *writer
	// buildFailure is the provider builder's last failure (provider.go).
	buildFailure atomic.Pointer[buildFailure]
	fence        fence
	path         string
	held         heldLog
	loops        sync.WaitGroup
	// diskOwned is set around each call the writer makes on the disk seam.
	diskOwned atomic.Bool
	rpValid   atomic.Bool
}

var (
	_ account.Store             = (*Store)(nil)
	_ access.AuthenticatorStore = (*Store)(nil)
)

// Reset reports a corrupt store copied aside and replaced by a fresh one.
type Reset struct {
	At    time.Time
	Aside string
}

// Taken reports whether a reset happened.
func (r Reset) Taken() bool { return r.Aside != "" }

// Close ends the store's writer and background loops and waits for them; reads keep answering
// from the last published state. It is what cancelling OpenStore's context does, plus the wait.
func (s *Store) Close() {
	s.stop()
	<-s.w.writerDone
	s.loops.Wait()
}

func (s *Store) view() *txView { return &txView{s: s.cur.Load(), st: s} }

// sessionRecord is the gate's and the stream registry's lookup, with the transport tag.
func (s *Store) sessionRecord(h access.Hash) (storeSession, bool) {
	r, ok := s.cur.Load().sessions[h.Key()]
	if !ok {
		return storeSession{}, false
	}
	return *r, true
}

func (s *Store) keyRecord(id int64) (storeKey, bool) {
	snap := s.cur.Load()
	k, ok := snap.keyByID[id]
	if !ok {
		return storeKey{}, false
	}
	r := *snap.keys[k]
	r.Scopes, r.LineageIDs = slices.Clone(r.Scopes), slices.Clone(r.LineageIDs)
	return r, true
}

func (s *Store) policy() storePolicy { return s.cur.Load().policy.clone() }

func (s *Store) seenSeq() uint64 { return s.cur.Load().seenSeq }

// OIDCProvider is account.Config.OIDC: the provider of the published snapshot, nil while none is
// built.
func (s *Store) OIDCProvider() *oidc.Provider { return s.cur.Load().oidc }

// DeviceKeys returns the device-cookie keys, newest first, masked.
func (s *Store) DeviceKeys() []access.Secret { return slices.Clone(s.cur.Load().deviceKeys) }

// FailureKey returns the failure-record HMAC key, masked.
func (s *Store) FailureKey() access.Secret { return s.cur.Load().failureKey }

// KeyBytes reveals each key into a fresh slice, for the library calls that take key material as
// []byte.
func KeyBytes(keys ...access.Secret) [][]byte {
	out := make([][]byte, len(keys))
	for i, k := range keys {
		out[i] = []byte(k.Reveal())
	}
	return out
}

// openArm is what the open job found at the path.
type openArm uint8

const (
	armNoFile openArm = iota + 1
	armCustody
	armUnreadable
	armValid
	armCorrupt
)

func openArms() []openArm {
	return []openArm{armNoFile, armCustody, armUnreadable, armValid, armCorrupt}
}

// openReadBound caps the open's read, far above the file's size at rule 15's bound.
var openReadBound int64 = 16 << 20

// openExitHook, tests only, runs when the writer stops after a failed open, before writerDone
// closes.
var openExitHook func(writerDone <-chan struct{})

// newStoreHook, tests only, sees the store before its writer starts, so a test can arm a hold on
// the open job.
var newStoreHook func(*Store)

// OpenStore opens the store at path through disk. The open is the writer's first job, a group of
// its own; the store is returned only once that job published, and ctx is the store's lifetime.
// A Reset is returned only beside a store, for a corrupt file whose copy aside and fresh state
// are both durable.
func OpenStore(ctx context.Context, path string, disk filemode.Disk) (*Store, Reset, error) {
	sctx, stop := context.WithCancel(ctx)
	s := &Store{path: path, disk: disk, ctx: sctx, stop: stop}
	s.w = newWriter(s)
	if newStoreHook != nil {
		newStoreHook(s)
	}
	var reset Reset
	var corruptErr error
	j := &job{ctx: ctx, done: make(chan outcome, 1), fn: func(tx *txView) error {
		r, cErr, err := s.openState(tx)
		reset, corruptErr = r, cErr
		return err
	}}
	s.w.pending.Add(1)
	go s.w.run(j)
	o := <-j.done
	published := o.kind == outcomeNoop || o.kind == outcomeWrote
	if corruptErr != nil && (published || errors.Is(o.err, ErrNotDurable)) {
		slog.Error("access: sign-in data could not be read; it was copied aside and replaced by a fresh store",
			"path", path, "copy", reset.Aside, "err", corruptErr)
	}
	if published {
		return s, reset, nil
	}
	<-s.w.writerDone
	stop()
	if o.kind == outcomePanicked {
		return nil, Reset{}, fmt.Errorf("access: opening the store panicked: %v", o.panic)
	}
	return nil, Reset{}, o.err
}

// runOpen runs the open job and its publication under one recover. It reports whether the writer
// goes on: every failed outcome stops it, stores nothing in cur and starts no loop.
func (w *writer) runOpen(j *job) bool {
	st := w.st
	tx := &txView{s: emptySnapshot(), st: st, own: &ownership{}}
	o := failed(j.ctx.Err())
	if o.err == nil {
		o = protect(func() error { return w.openAndWrite(j, tx) })
	}
	if o.kind == outcomeNoop {
		st.cur.Store(tx.s)
		w.deliver(j, o)
		return true
	}
	if o.kind == outcomeFailed {
		o.err = st.discardOpenLeftover(o.err)
	}
	w.deliver(j, o)
	if openExitHook != nil {
		openExitHook(w.writerDone)
	}
	return false
}

// openAndWrite is the open job's callback and its file write. Only that write's unconfirmed result
// is ErrNotDurable: the corrupt copy's is the copy's failure, with access.json never written.
func (w *writer) openAndWrite(j *job, tx *txView) error {
	if err := j.fn(tx); err != nil {
		return err
	}
	w.awaitHold()
	if !tx.writesFile() {
		return nil
	}
	err := w.st.writeFile(tx.s)
	if errors.Is(err, filemode.ErrNotDurable) {
		return fmt.Errorf("%w: %w", ErrNotDurable, err)
	}
	return err
}

// discardOpenLeftover makes the open's one synchronous Discard of a staged file its write left,
// joining a failed Discard into err.
func (s *Store) discardOpenLeftover(err error) error {
	lo, ok := errors.AsType[*filemode.LeftoverError](err)
	if !ok {
		return err
	}
	s.diskOwned.Store(true)
	dErr := lo.Discard()
	s.diskOwned.Store(false)
	if dErr == nil {
		return err
	}
	slog.Error("access: a staged sign-in file could not be removed", "path", lo.Name(), "err", dErr)
	return errors.Join(err, dErr)
}

// openArmOf picks the open's arm from ReadPrivate's result and, for bytes, the decode.
func openArmOf(data []byte, readErr error) (*snapshot, openArm, error) {
	var np *filemode.NotPrivateError
	switch {
	case errors.Is(readErr, fs.ErrNotExist):
		return nil, armNoFile, nil
	case errors.As(readErr, &np):
		return nil, armCustody, readErr
	case readErr != nil:
		return nil, armUnreadable, readErr
	}
	snap, err := decode(data)
	if err != nil {
		return nil, armCorrupt, err
	}
	return snap, armValid, nil
}

// openState reads the file and puts the arm's state on the private copy. For the
// corrupt arm it also returns the decode error, which OpenStore logs once access.json is replaced.
func (s *Store) openState(tx *txView) (Reset, error, error) {
	s.diskOwned.Store(true)
	data, err := filemode.ReadPrivate(s.disk, s.path, openReadBound)
	s.diskOwned.Store(false)
	snap, arm, err := openArmOf(data, err)
	switch arm {
	case armNoFile:
		adoptFresh(tx)
		return Reset{}, nil, nil
	case armValid:
		snap.providerGen = 1
		*tx.s = *snap
		tx.recordMem(func() {})
		return Reset{}, nil, nil
	case armCorrupt:
		aside, cErr := s.copyAside(data)
		if cErr != nil {
			return Reset{}, nil, fmt.Errorf("access: copying the unreadable sign-in data aside: %w", cErr)
		}
		adoptFresh(tx)
		return Reset{Aside: aside, At: time.Now().UTC()}, err, nil
	}
	return Reset{}, nil, err
}

// adoptFresh puts the first-boot state on the private copy: no records, the default policy at
// generation 1, one device key and a failure key.
func adoptFresh(tx *txView) {
	fk := make([]byte, keyBytes)
	_, _ = rand.Read(fk) // never fails since Go 1.24
	fresh := emptySnapshot()
	fresh.providerGen = 1
	fresh.deviceKeys = []access.Secret{access.NewSecret(string(access.GenerateSealerKey()))}
	fresh.failureKey = access.NewSecret(string(fk))
	*tx.s = *fresh
	tx.record(func() {})
}

// copyAside writes the corrupt bytes beside the file, named for its modification time, at the
// first name that is absent or already holds the same bytes, so a retry rewrites its own copy and
// no copy is replaced by other bytes.
func (s *Store) copyAside(data []byte) (string, error) {
	s.diskOwned.Store(true)
	fi, err := s.disk.Lstat(s.path)
	s.diskOwned.Store(false)
	if err != nil {
		return "", err
	}
	base := s.path + ".corrupt-" + fi.ModTime().UTC().Format("20060102T150405.000000000Z")
	for n := 0; ; n++ {
		name := base
		if n > 0 {
			name = fmt.Sprintf("%s-%d", base, n)
		}
		s.diskOwned.Store(true)
		held, err := filemode.ReadPrivate(s.disk, name, openReadBound)
		s.diskOwned.Store(false)
		switch {
		case errors.Is(err, fs.ErrNotExist), err == nil && bytes.Equal(held, data):
		case err != nil:
			return "", err
		default:
			continue
		}
		s.diskOwned.Store(true)
		err = filemode.WritePrivate(context.WithoutCancel(s.ctx), s.disk, name, data)
		s.diskOwned.Store(false)
		if err != nil {
			return "", err
		}
		return name, nil
	}
}
