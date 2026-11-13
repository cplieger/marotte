package applogin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"runtime/debug"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cplieger/access"
	"github.com/cplieger/access/account"
	"github.com/cplieger/access/oidc"
	"github.com/cplieger/marotte/internal/filemode"
)

var (
	// ErrNotDurable wraps the result of a write whose new state is published and in the file but
	// may not survive a power loss; the store is fenced.
	ErrNotDurable = errors.New("applogin: sign-in data could not be confirmed on disk")
	// ErrFenced is every write's answer while the store is fenced; nothing ran.
	ErrFenced = errors.New("applogin: sign-in changes are paused until the disk is confirmed")
	// ErrNoWorkingMethod refuses an own policy write that would leave the owner no way to sign in.
	ErrNoWorkingMethod = errors.New("applogin: the change would leave no working sign-in method")

	errStoreClosed     = errors.New("applogin: the store is closed")
	errCleanupLimit    = errors.New("access: cleanup limit below 1")
	errCleanupTimeouts = errors.New("access: cleanup with a non-positive timeout")
)

// Published reports whether a write's result left its change in force: nil, or a publication
// that is in the file but not yet confirmed.
func Published(err error) bool { return err == nil || errors.Is(err, ErrNotDurable) }

type outcomeKind uint8

const (
	outcomeNoop     outcomeKind = iota + 1 // nil return, nothing written
	outcomeWrote                           // nil return, its writes kept in the private copy
	outcomeFailed                          // callback error, a refusal, or a skipped job; rolled back
	outcomePanicked                        // callback panic; rolled back
)

type outcome struct {
	err   error
	panic any
	kind  outcomeKind
}

func failed(err error) outcome { return outcome{kind: outcomeFailed, err: err} }

type jobKind uint8

const (
	jobWrite jobKind = iota
	// jobAttach is memory-only and admitted while fenced.
	jobAttach
	// jobConfirm runs as a group of its own, as the open job does by
	// running before the writer reads its queue.
	jobConfirm
	// jobHeld is appendOwnEntries: the writer moves its carried entries into the held log when it
	// takes the job, and the job appends every held entry.
	jobHeld
)

type job struct {
	ctx     context.Context
	fn      func(tx *txView) error
	guard   *ownGuard
	done    chan outcome
	marker  access.Method
	carried []account.AppEntry
	taken   []account.AppEntry
	kinds   []string
	kind    jobKind
}

// ownGuard counts the owner's methods after an own policy write, through the library's
// UsableMethods over the job's txView.
type ownGuard struct {
	count func(ctx context.Context, tx account.Tx, userID int64, st account.MethodState) (access.MethodAvailability, error)
	rules func(s *snapshot) account.Rules
	rpID  string
}

// publishHold is an armed HoldPublish: entered closes when a group reaches it, release lets the
// group go on.
type publishHold struct {
	entered chan struct{}
	release chan struct{}
}

type writer struct {
	st         *Store
	hold       atomic.Pointer[publishHold]
	wake       chan struct{}
	writerDone chan struct{}
	queue      []*job
	pending    atomic.Int64
	mu         sync.Mutex
	closed     bool
}

func newWriter(st *Store) *writer {
	return &writer{st: st, wake: make(chan struct{}, 1), writerDone: make(chan struct{})}
}

// submit queues j and waits for its outcome, even after j.ctx ends: only the outcome says whether
// the job's writes were published.
func (w *writer) submit(j *job) outcome {
	if !w.enqueue(j) {
		return failed(errStoreClosed)
	}
	return <-j.done
}

// enqueue queues j and reports false when the store is closed; j.done then receives nothing.
func (w *writer) enqueue(j *job) bool {
	j.done = make(chan outcome, 1)
	if j.marker == "" {
		j.marker = restoreAllowanceFrom(j.ctx)
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return false
	}
	w.queue = append(w.queue, j)
	w.pending.Add(1)
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
	return true
}

func (w *writer) deliver(j *job, o outcome) {
	j.done <- o
	w.pending.Add(-1)
}

// next takes the next group: the first queued job and every job queued behind it, except that a
// confirm job is always a group of its own. It returns nil once the store's context ends,
// failing what is still queued.
func (w *writer) next() []*job {
	for {
		w.mu.Lock()
		if w.st.ctx.Err() != nil {
			w.closed = true
			rest := w.queue
			w.queue = nil
			w.mu.Unlock()
			for _, j := range rest {
				w.deliver(j, failed(errStoreClosed))
			}
			return nil
		}
		if n := groupLen(w.queue); n > 0 {
			batch := w.queue[:n:n]
			w.queue = w.queue[n:]
			w.mu.Unlock()
			return batch
		}
		w.mu.Unlock()
		select {
		case <-w.wake:
		case <-w.st.ctx.Done():
		}
	}
}

// groupLen is how many queued jobs the next group takes: one for a confirm job, else every job up
// to the next confirm job.
func groupLen(queue []*job) int {
	if len(queue) == 0 || solo(queue[0]) {
		return min(len(queue), 1)
	}
	n := 1
	for n < len(queue) && !solo(queue[n]) {
		n++
	}
	return n
}

func solo(j *job) bool { return j.kind == jobConfirm }

// run serves the queue once the open job published. The open job is run before the queue is read,
// so a context that ended before OpenStore reaches the job's own ctx.Err() arm.
func (w *writer) run(open *job) {
	defer close(w.writerDone)
	if !w.runOpen(open) {
		return
	}
	for {
		batch := w.next()
		if batch == nil {
			return
		}
		w.runGroup(batch)
	}
}

// awaitHold blocks on an armed HoldPublish (tests only) until its release. It holds the next
// group after its jobs ran and before anything is published or answered, a group that writes
// nothing included, so a test can queue every call it races behind one held group.
func (w *writer) awaitHold() {
	if h := w.hold.Swap(nil); h != nil {
		close(h.entered)
		select {
		case <-h.release:
		case <-w.st.ctx.Done():
		}
	}
}

func (w *writer) runGroup(batch []*job) {
	st := w.st
	priv := st.cur.Load().clone()
	tx := &txView{s: priv, st: st, own: &ownership{}}
	outs := make([]outcome, len(batch))
	firstWrote, file := -1, false
	for _, j := range batch {
		if j.kind == jobHeld {
			st.held.add(j.carried)
		}
	}
	for i, j := range batch {
		var wroteFile bool
		outs[i], wroteFile = w.runJob(tx, j)
		if outs[i].kind == outcomeWrote {
			if firstWrote < 0 {
				firstWrote = i
			}
			file = file || wroteFile
		}
	}
	w.awaitHold()
	if firstWrote >= 0 {
		var err error
		if file {
			err = st.writeFile(priv)
		}
		st.publish(batch, outs, firstWrote, priv, err)
	}
	w.deliverAll(batch, outs)
}

// publish delivers a group whose jobs from firstWrote on wrote: err is the file write's
// result, nil for a memory-only group. A participating job's outcome becomes the publication's
// error; every other job keeps its own.
func (s *Store) publish(batch []*job, outs []outcome, firstWrote int, priv *snapshot, err error) {
	confirm := batch[0].kind == jobConfirm
	switch {
	case err == nil && confirm:
		s.clearFence()
	case err == nil:
		s.cur.Store(priv)
	case errors.Is(err, filemode.ErrNotDurable) && confirm:
		outs[0] = failed(fmt.Errorf("%w: %w", ErrNotDurable, err))
	case errors.Is(err, filemode.ErrNotDurable):
		s.cur.Store(priv)
		s.setFence()
		s.failParticipating(batch, outs, firstWrote, fmt.Errorf("%w: %w", ErrNotDurable, err), false)
	default:
		s.failParticipating(batch, outs, firstWrote, err, true)
		if lo, ok := errors.AsType[*filemode.LeftoverError](err); ok {
			s.discardLeftover(lo)
		}
	}
}

// failParticipating gives each job from firstWrote on that wrote or found nothing to write the
// publication's error; with putBack, a held job's entries return to heldEntries, since the file
// does not hold them.
func (s *Store) failParticipating(batch []*job, outs []outcome, firstWrote int, err error, putBack bool) {
	for i := firstWrote; i < len(batch); i++ {
		if outs[i].kind != outcomeWrote && outs[i].kind != outcomeNoop {
			continue
		}
		outs[i] = failed(err)
		if putBack && batch[i].kind == jobHeld {
			s.held.putBack(batch[i])
		}
	}
}

func (w *writer) deliverAll(batch []*job, outs []outcome) {
	for i, j := range batch {
		w.deliver(j, outs[i])
	}
}

// runJob runs one job on the private copy and reports its outcome and whether its writes reach
// the file.
func (w *writer) runJob(tx *txView, j *job) (outcome, bool) {
	st := w.st
	if err := j.ctx.Err(); err != nil {
		return failed(err), false
	}
	if st.fence.set && j.kind != jobAttach && j.kind != jobConfirm {
		return failed(ErrFenced), false
	}
	tx.undo, tx.creds, tx.guardDue, tx.rewrite = tx.undo[:0], nil, false, false
	o := protect(func() error { return j.fn(tx) })
	if o.kind == outcomeNoop {
		if err := settleOwnWrite(tx, j); err != nil {
			o = failed(err)
		}
	}
	if o.kind != outcomeNoop {
		tx.rollback()
		if j.kind == jobHeld {
			st.held.putBack(j)
		}
		return o, false
	}
	if len(tx.undo) > 0 || tx.rewrite {
		o.kind = outcomeWrote
	}
	return o, tx.writesFile()
}

// settleOwnWrite applies the job's restore allowance, then its guard: the guard counts the
// methods the allowance turned on.
func settleOwnWrite(tx *txView, j *job) error {
	if j.marker != "" {
		if err := applyRestore(tx, j.marker); err != nil {
			return err
		}
	}
	if tx.guardDue && j.guard != nil {
		return checkGuard(tx, j)
	}
	return nil
}

// protect runs fn under recover; a nil return is outcomeNoop, which runJob refines.
func protect(fn func() error) (o outcome) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("access: a store write panicked", "panic", r, "stack", string(debug.Stack()))
			o = outcome{kind: outcomePanicked, panic: r}
		}
	}()
	if err := fn(); err != nil {
		return failed(err)
	}
	return outcome{kind: outcomeNoop}
}

func (s *Store) writeFile(snap *snapshot) error {
	data, err := encode(snap)
	if err != nil {
		return err
	}
	s.diskOwned.Store(true)
	defer s.diskOwned.Store(false)
	return filemode.WritePrivate(context.WithoutCancel(s.ctx), s.disk, s.path, data)
}

// discardLeftover removes a staged file a failed publication left, retrying 1 s doubling to 60 s
// until it is gone or the store closes. It touches only the staged file, never access.json.
func (s *Store) discardLeftover(lo *filemode.LeftoverError) {
	slog.Error("access: a staged sign-in file could not be removed", "path", lo.Name(), "err", lo)
	s.loops.Go(func() {
		for delay := time.Second; ; delay = min(2*delay, time.Minute) {
			if lo.Discard() == nil {
				return
			}
			t := time.NewTimer(delay)
			select {
			case <-t.C:
			case <-s.ctx.Done():
				t.Stop()
				return
			}
		}
	})
}

// --- account.Store ---

// write runs fn as one job and re-raises a callback panic in the caller.
func (s *Store) write(ctx context.Context, fn func(tx *txView) error) error {
	o := s.w.submit(&job{ctx: ctx, fn: fn})
	if o.kind == outcomePanicked {
		panic(o.panic)
	}
	return o.err
}

// updateHook, tests only, runs at the top of every Update, before its job is queued.
var updateHook func()

// Update runs fn in the next group and waits for its outcome even if ctx ends after fn started.
func (s *Store) Update(ctx context.Context, fn func(account.Tx) error) error {
	if updateHook != nil {
		updateHook()
	}
	return s.write(ctx, func(tx *txView) error { return fn(tx) })
}

// CreateUser implements [account.UserStore].
func (s *Store) CreateUser(ctx context.Context, user *access.User) error {
	return s.write(ctx, func(tx *txView) error { return tx.CreateUser(ctx, user) })
}

// CreateInitialUser implements [account.UserStore].
func (s *Store) CreateInitialUser(ctx context.Context, user *access.User) error {
	return s.write(ctx, func(tx *txView) error { return tx.CreateInitialUser(ctx, user) })
}

// UserByID implements [account.UserStore].
func (s *Store) UserByID(ctx context.Context, id int64) (*access.User, bool, error) {
	return s.view().UserByID(ctx, id)
}

// UserByUsername implements [account.UserStore].
func (s *Store) UserByUsername(ctx context.Context, canonical string) (*access.User, bool, error) {
	return s.view().UserByUsername(ctx, canonical)
}

// UserByOIDC implements [account.UserStore].
func (s *Store) UserByOIDC(ctx context.Context, id access.OIDCIdentity) (*access.User, bool, error) {
	return s.view().UserByOIDC(ctx, id)
}

// UserByWebAuthnHandle implements [account.UserStore].
func (s *Store) UserByWebAuthnHandle(ctx context.Context, handle []byte) (*access.User, bool, error) {
	return s.view().UserByWebAuthnHandle(ctx, handle)
}

// ListUsers implements [account.UserStore].
func (s *Store) ListUsers(ctx context.Context) ([]access.User, error) { return s.view().ListUsers(ctx) }

// UpdateUser implements [account.UserStore].
func (s *Store) UpdateUser(ctx context.Context, user *access.User) error {
	return s.write(ctx, func(tx *txView) error { return tx.UpdateUser(ctx, user) })
}

// DeleteUser implements [account.UserStore].
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	return s.write(ctx, func(tx *txView) error { return tx.DeleteUser(ctx, id) })
}

// UserCount implements [account.UserStore].
func (s *Store) UserCount(ctx context.Context) (int, error) { return s.view().UserCount(ctx) }

// CreateSession implements [account.SessionStore].
func (s *Store) CreateSession(ctx context.Context, sess *access.Session) error {
	return s.write(ctx, func(tx *txView) error { return tx.CreateSession(ctx, sess) })
}

// SessionByHash implements [account.SessionStore].
func (s *Store) SessionByHash(ctx context.Context, tokenHash access.Hash) (*access.Session, bool, error) {
	return s.view().SessionByHash(ctx, tokenHash)
}

// SessionsByUserID implements [account.SessionStore].
func (s *Store) SessionsByUserID(ctx context.Context, userID int64) ([]access.Session, error) {
	return s.view().SessionsByUserID(ctx, userID)
}

// UpdateSessionActivity implements [account.SessionStore].
func (s *Store) UpdateSessionActivity(ctx context.Context, tokenHash access.Hash, act access.SessionActivity) error {
	return s.write(ctx, func(tx *txView) error { return tx.UpdateSessionActivity(ctx, tokenHash, act) })
}

// RotateSession implements [account.SessionStore].
func (s *Store) RotateSession(ctx context.Context, rot account.Rotation) error { //nolint:gocritic // hugeParam: account.SessionStore's signature
	return s.write(ctx, func(tx *txView) error { return tx.RotateSession(ctx, rot) })
}

// UpdateSessionLabel implements [account.SessionStore].
func (s *Store) UpdateSessionLabel(ctx context.Context, ref access.SessionRef, label string) error {
	return s.write(ctx, func(tx *txView) error { return tx.UpdateSessionLabel(ctx, ref, label) })
}

// DeleteSession implements [account.SessionStore].
func (s *Store) DeleteSession(ctx context.Context, ref access.SessionRef) error {
	return s.write(ctx, func(tx *txView) error { return tx.DeleteSession(ctx, ref) })
}

// DeleteSessionByHash implements [account.SessionStore].
func (s *Store) DeleteSessionByHash(ctx context.Context, tokenHash access.Hash) error {
	return s.write(ctx, func(tx *txView) error { return tx.DeleteSessionByHash(ctx, tokenHash) })
}

// DeleteUserSessions implements [account.SessionStore].
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64, exceptID string) ([]string, error) {
	var ids []string
	err := s.write(ctx, func(tx *txView) error {
		ids = tx.deleteUserSessions(userID, exceptID)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// CreatePasskey implements [account.PasskeyStore].
func (s *Store) CreatePasskey(ctx context.Context, cred *access.PasskeyCredential) error {
	return s.write(ctx, func(tx *txView) error { return tx.CreatePasskey(ctx, cred) })
}

// PasskeysByUserID implements [account.PasskeyStore].
func (s *Store) PasskeysByUserID(ctx context.Context, userID int64) ([]access.PasskeyCredential, error) {
	return s.view().PasskeysByUserID(ctx, userID)
}

// UpdatePasskeyAfterLogin implements [account.PasskeyStore].
func (s *Store) UpdatePasskeyAfterLogin(ctx context.Context, userID int64, credID []byte, upd access.PasskeyLoginUpdate) error {
	return s.write(ctx, func(tx *txView) error { return tx.UpdatePasskeyAfterLogin(ctx, userID, credID, upd) })
}

// RenamePasskey implements [account.PasskeyStore].
func (s *Store) RenamePasskey(ctx context.Context, ref access.PasskeyRef, name string) error {
	return s.write(ctx, func(tx *txView) error { return tx.RenamePasskey(ctx, ref, name) })
}

// DeletePasskey implements [account.PasskeyStore].
func (s *Store) DeletePasskey(ctx context.Context, ref access.PasskeyRef) error {
	return s.write(ctx, func(tx *txView) error { return tx.DeletePasskey(ctx, ref) })
}

// DeleteUserPasskeys implements [account.PasskeyStore].
func (s *Store) DeleteUserPasskeys(ctx context.Context, userID int64) ([]int64, error) {
	var ids []int64
	err := s.write(ctx, func(tx *txView) error {
		ids = tx.deleteUserPasskeys(userID)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// CreateAPIKey implements [account.KeyStore].
func (s *Store) CreateAPIKey(ctx context.Context, key *access.Key) error {
	return s.write(ctx, func(tx *txView) error { return tx.CreateAPIKey(ctx, key) })
}

// APIKeyByHash implements [account.KeyStore].
func (s *Store) APIKeyByHash(ctx context.Context, hash access.Hash) (*access.Key, bool, error) {
	return s.view().APIKeyByHash(ctx, hash)
}

// ListAPIKeysByUserID implements [account.KeyStore].
func (s *Store) ListAPIKeysByUserID(ctx context.Context, userID int64) ([]access.Key, error) {
	return s.view().ListAPIKeysByUserID(ctx, userID)
}

// ListAPIKeys implements [account.KeyStore].
func (s *Store) ListAPIKeys(ctx context.Context) ([]access.Key, error) {
	return s.view().ListAPIKeys(ctx)
}

// DeleteAPIKey implements [account.KeyStore].
func (s *Store) DeleteAPIKey(ctx context.Context, ref access.KeyRef) error {
	return s.write(ctx, func(tx *txView) error { return tx.DeleteAPIKey(ctx, ref) })
}

// DeleteUserAPIKeys implements [account.KeyStore].
func (s *Store) DeleteUserAPIKeys(ctx context.Context, userID int64) ([]int64, error) {
	var ids []int64
	err := s.write(ctx, func(tx *txView) error {
		ids = tx.deleteUserKeys(userID)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// TouchAPIKey implements [account.KeyStore].
func (s *Store) TouchAPIKey(ctx context.Context, keyID int64, at time.Time) error {
	return s.write(ctx, func(tx *txView) error { return tx.TouchAPIKey(ctx, keyID, at) })
}

// CreateOneTimeToken implements [account.TokenStore].
func (s *Store) CreateOneTimeToken(ctx context.Context, tok access.OneTimeToken) error { //nolint:gocritic // hugeParam: account.TokenStore's signature
	return s.write(ctx, func(tx *txView) error { return tx.CreateOneTimeToken(ctx, tok) })
}

// OneTimeTokenByHash implements [account.TokenStore].
func (s *Store) OneTimeTokenByHash(ctx context.Context, hash access.Hash) (access.OneTimeToken, bool, error) {
	return s.view().OneTimeTokenByHash(ctx, hash)
}

// ConsumeOneTimeToken implements [account.TokenStore].
func (s *Store) ConsumeOneTimeToken(ctx context.Context, hash access.Hash, now time.Time) (access.OneTimeToken, bool, error) {
	var tok access.OneTimeToken
	var found bool
	err := s.write(ctx, func(tx *txView) error {
		var err error
		tok, found, err = tx.ConsumeOneTimeToken(ctx, hash, now)
		return err
	})
	if err != nil {
		return access.OneTimeToken{}, false, err
	}
	return tok, found, nil
}

// DeleteOneTimeTokens implements [account.TokenStore].
func (s *Store) DeleteOneTimeTokens(ctx context.Context, purpose access.TokenPurpose, subject int64) (int, error) {
	var n int
	err := s.write(ctx, func(tx *txView) error {
		n, _ = tx.DeleteOneTimeTokens(ctx, purpose, subject)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// LiveOneTimeTokens implements [account.TokenStore].
func (s *Store) LiveOneTimeTokens(ctx context.Context, purposes []access.TokenPurpose, now time.Time) ([]access.OneTimeToken, error) {
	return s.view().LiveOneTimeTokens(ctx, purposes, now)
}

// DeleteOneTimeTokensIssuedBy implements [account.TokenStore].
func (s *Store) DeleteOneTimeTokensIssuedBy(ctx context.Context, keyIDs []int64) (int, error) {
	var n int
	err := s.write(ctx, func(tx *txView) error {
		n, _ = tx.DeleteOneTimeTokensIssuedBy(ctx, keyIDs)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// CreateKeyAttribution implements [account.AttributionStore].
func (s *Store) CreateKeyAttribution(ctx context.Context, a *access.KeyAttribution) error {
	return s.write(ctx, func(tx *txView) error { return tx.CreateKeyAttribution(ctx, a) })
}

// ListKeyAttributions implements [account.AttributionStore].
func (s *Store) ListKeyAttributions(ctx context.Context) ([]access.KeyAttribution, error) {
	return s.view().ListKeyAttributions(ctx)
}

// AuthFailure implements [account.FailureStore].
func (s *Store) AuthFailure(ctx context.Context, ref account.FailureRef) (account.FailureCount, bool, error) {
	return s.view().AuthFailure(ctx, ref)
}

// PutAuthFailure implements [account.FailureStore].
func (s *Store) PutAuthFailure(ctx context.Context, f account.FailureCount) error { //nolint:gocritic // hugeParam: account.FailureStore's signature
	return s.write(ctx, func(tx *txView) error { return tx.PutAuthFailure(ctx, f) })
}

// UnreportedLocks implements [account.FailureStore].
func (s *Store) UnreportedLocks(ctx context.Context) ([]account.FailureCount, error) {
	return s.view().UnreportedLocks(ctx)
}

// DeleteAuthFailures implements [account.FailureStore].
func (s *Store) DeleteAuthFailures(ctx context.Context, name string, methods []access.Method) error {
	return s.write(ctx, func(tx *txView) error { return tx.DeleteAuthFailures(ctx, name, methods) })
}

// PruneAuthFailures implements [account.FailureStore].
func (s *Store) PruneAuthFailures(ctx context.Context, keep []string) (int64, error) {
	var n int64
	err := s.write(ctx, func(tx *txView) error {
		n, _ = tx.PruneAuthFailures(ctx, keep)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// AppendLog implements [account.LogStore].
func (s *Store) AppendLog(ctx context.Context, e *account.LogEntry) error {
	return s.write(ctx, func(tx *txView) error { return tx.AppendLog(ctx, e) })
}

// ListLog implements [account.LogStore].
func (s *Store) ListLog(ctx context.Context, r account.LogRange) ([]account.LogEntry, error) {
	return s.view().ListLog(ctx, r)
}

// PruneLog implements [account.LogStore].
func (s *Store) PruneLog(ctx context.Context, p account.LogPrune) (int, error) {
	var n int
	err := s.write(ctx, func(tx *txView) error {
		var err error
		n, err = tx.PruneLog(ctx, p)
		return err
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// LogStats implements [account.LogStore].
func (s *Store) LogStats(ctx context.Context) ([]account.LogClassStats, error) {
	return s.view().LogStats(ctx)
}

// CleanupExpiredSessions is one writer job: it judges the sessions on the private copy at its turn
// in the group and returns the refs it deleted only once their publication is durable (rule 14).
func (s *Store) CleanupExpiredSessions(ctx context.Context, now time.Time, timeouts access.SessionTimeouts, limit int) ([]access.SessionRef, error) {
	switch {
	case limit < 1:
		return nil, errCleanupLimit
	case timeouts.Idle <= 0 || timeouts.Absolute <= 0:
		return nil, errCleanupTimeouts
	}
	var refs []access.SessionRef
	err := s.write(ctx, func(tx *txView) error {
		refs = tx.cleanup(now, timeouts, limit)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return refs, nil
}

// CleanupExpiredOneTimeTokens implements [account.Sweeper].
func (s *Store) CleanupExpiredOneTimeTokens(ctx context.Context, now time.Time) (int64, error) {
	var n int
	err := s.write(ctx, func(tx *txView) error {
		n = tx.deleteTokens(func(r *storeToken) bool { return !r.ExpiresAt.After(now) })
		return nil
	})
	if err != nil {
		return 0, err
	}
	return int64(n), nil
}

// --- marotte's own writes ---

// DeleteSessionDirect is the gate's delete of a refused Plain session; the gate answers 401
// whatever it returns, a fence's refusal included.
func (s *Store) DeleteSessionDirect(ctx context.Context, h access.Hash) error {
	return s.write(ctx, func(tx *txView) error { return tx.DeleteSessionByHash(ctx, h) })
}

// setSeenSeq raises activity_seen_seq to seq, capped at the highest Seq assigned, never lowering it.
func (s *Store) setSeenSeq(ctx context.Context, seq uint64) error {
	return s.write(ctx, func(tx *txView) error {
		tx.setSeenSeq(seq)
		return nil
	})
}

// attachProvider publishes p as the provider of the record generation gen names, in memory only;
// a no-op unless providerGen still equals gen and no provider is attached.
func (s *Store) attachProvider(ctx context.Context, gen uint64, p *oidc.Provider) error {
	o := s.w.submit(&job{ctx: ctx, kind: jobAttach, fn: func(tx *txView) error {
		snap := tx.s
		if snap.providerGen != gen || snap.oidc != nil || snap.policy.Provider.isZero() {
			return nil
		}
		old := snap.oidc
		snap.oidc = p
		tx.recordMem(func() { snap.oidc = old })
		return nil
	}})
	if o.kind == outcomePanicked {
		panic(o.panic)
	}
	return o.err
}

// ownService is the account.Service an own write appends its log entries and counts methods
// through.
type ownService interface {
	AppendLog(ctx context.Context, tx account.Tx, e account.AppEntry) (account.LogEntry, error)
	UsableMethods(ctx context.Context, tx account.Tx, userID int64, st account.MethodState) (access.MethodAvailability, error)
}

var _ ownService = (*account.Service)(nil)

const (
	kindPlainOn   = "marotte.plain_http_on"
	kindPlainOff  = "marotte.plain_http_off"
	kindMethodOn  = "marotte.method_on"
	kindMethodOff = "marotte.method_off"
)

// applyDelta deletes the Plain sessions a switch-off ends and appends one log entry per delta
// entry, on the job's txView, so each commits with its change or not at all.
func applyDelta(ctx context.Context, tx *txView, svc ownService, actor *account.Actor, d *policyDelta) error {
	if d.plainOff {
		for _, ref := range tx.deletePlainSessions() {
			d.deletedPlain = append(d.deletedPlain, ref.ID)
		}
	}
	var entries []account.AppEntry
	switch {
	case d.plainOn:
		entries = append(entries, account.AppEntry{Kind: kindPlainOn, Actor: *actor})
	case d.plainOff:
		entries = append(entries, account.AppEntry{Kind: kindPlainOff, Actor: *actor})
	}
	for _, m := range policyMethodOrder {
		on, ok := d.methods[m]
		if !ok {
			continue
		}
		kind := kindMethodOff
		if on {
			kind = kindMethodOn
		}
		entries = append(entries, account.AppEntry{Kind: kind, Method: m, Actor: *actor})
	}
	for i := range entries {
		if _, err := svc.AppendLog(ctx, tx, entries[i]); err != nil {
			return err
		}
	}
	return nil
}

func bumpGeneration(p *storePolicy) error {
	if p.Generation >= maxID {
		return errIDsExhausted
	}
	p.Generation++
	return nil
}

// putPolicy is the Settings save: applied only when the private copy
// still holds baseGen, else ErrPolicyConflict with nothing written. candidate is the request's
// record; edit is planProviderEdit's answer, nil for an untouched provider, whose display name
// alone is taken from candidate. rpID is the canonical origin's RP ID, empty while degraded.
func (s *Store) putPolicy(ctx context.Context, svc ownService, actor *account.Actor, rpID string,
	candidate storePolicy, baseGen int64, edit *providerEdit, //nolint:gocritic // hugeParam: the form's record, read once
) (policyDelta, PolicyView, error) {
	var delta policyDelta
	var view PolicyView
	j := &job{ctx: ctx, guard: &ownGuard{count: svc.UsableMethods, rules: s.rulesFor, rpID: rpID}}
	j.fn = func(tx *txView) error {
		var err error
		delta, view, err = applyPolicy(ctx, tx, svc, actor, &candidate, baseGen, edit)
		return err
	}
	o := s.w.submit(j)
	if o.kind == outcomePanicked {
		panic(o.panic)
	}
	if !Published(o.err) {
		return policyDelta{}, PolicyView{}, o.err
	}
	return delta, view, o.err
}

// applyPolicy is putPolicy's job on the private copy: the record, the edit's candidate provider,
// the guard trigger and the delta's log entries, or ErrPolicyConflict before any of them.
func applyPolicy(ctx context.Context, tx *txView, svc ownService, actor *account.Actor,
	candidate *storePolicy, baseGen int64, edit *providerEdit,
) (policyDelta, PolicyView, error) {
	cur := tx.s.policy
	if cur.Generation != baseGen {
		return policyDelta{}, PolicyView{}, ErrPolicyConflict
	}
	next := nextPolicy(&cur, candidate, edit)
	if edit != nil || !sameRecord(&cur, &next) {
		if err := bumpGeneration(&next); err != nil {
			return policyDelta{}, PolicyView{}, err
		}
		tx.setPolicy(&next)
	}
	if edit != nil {
		snap := tx.s
		oldP, oldGen := snap.oidc, snap.providerGen
		snap.oidc, snap.providerGen = edit.built, snap.providerGen+1
		tx.record(func() { snap.oidc, snap.providerGen = oldP, oldGen })
	}
	delta := deltaOf(&cur, &next)
	if edit != nil || slices.Contains(slices.Collect(maps.Values(delta.methods)), false) {
		tx.guardDue = true
	}
	if err := applyDelta(ctx, tx, svc, actor, &delta); err != nil {
		return policyDelta{}, PolicyView{}, err
	}
	return delta, viewOf(&tx.s.policy), nil
}

// nextPolicy is the record a save writes over cur: the candidate's fields at cur's generation,
// with the edit's provider, or cur's own provider and the candidate's display name.
func nextPolicy(cur, candidate *storePolicy, edit *providerEdit) storePolicy {
	next := candidate.clone()
	next.Generation = cur.Generation
	if edit != nil {
		next.Provider = edit.record
		next.Provider.Pinned = clonePins(edit.record.Pinned)
		return next
	}
	name := next.Provider.DisplayName
	next.Provider = cur.clone().Provider
	if !next.Provider.isZero() {
		next.Provider.DisplayName = name
	}
	return next
}

// setPlainHTTP is the admin socket's switch, shape (b): it compares and sets plain_http on the
// private copy and deletes the Plain sessions when it turns the switch off.
func (s *Store) setPlainHTTP(ctx context.Context, svc ownService, on bool) (policyDelta, error) {
	var delta policyDelta
	err := s.write(ctx, func(tx *txView) error {
		delta = policyDelta{}
		cur := tx.s.policy
		if cur.PlainHTTP == on {
			return nil
		}
		next := cur.clone()
		next.PlainHTTP = on
		if err := bumpGeneration(&next); err != nil {
			return err
		}
		tx.setPolicy(&next)
		delta = deltaOf(&cur, &next)
		actor := account.OutOfBand("admin-socket")
		return applyDelta(ctx, tx, svc, &actor, &delta)
	})
	if !Published(err) {
		return policyDelta{}, err
	}
	return delta, err
}
