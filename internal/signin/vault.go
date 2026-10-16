package signin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cplieger/marotte/internal/filemode"
	"github.com/cplieger/marotte/internal/marotte"
)

const (
	// maxSignInWait bounds a device poll whatever expiry the backend's code carries.
	maxSignInWait = 16 * time.Minute
	// renewBudget bounds one renewal, so an engine asking for a credential always gets an answer.
	renewBudget = 30 * time.Second
	// verdictWait bounds how long SignOut waits for the worker's verdict, so a sign-out's HTTP
	// request ends even while the filesystem holds a write.
	verdictWait = 15 * time.Second
)

// User text for the sign-in failures the vault itself decides.
const (
	textSignInFailed  = "The sign-in failed; the server log has the details."
	textSignInTimeout = "The sign-in was not approved in time. Start the sign-in again."
	textSaveFailed    = "marotte could not save the sign-in; the server log has the details."
	textUnusable      = "The sign-in service returned a sign-in marotte cannot use; the server log has the details."
	textNoBackend     = "marotte has no sign-in for this backend."
)

// record is one immutable credential state. gen names the account: a renewal or a lapse makes a new
// record with the same gen, a sign-in a new gen, so "same account" is a gen comparison and "this
// exact state" a pointer comparison.
type record struct {
	cred   Credential
	gen    uint64
	lapsed bool
}

func genOf(r *record) uint64 {
	if r == nil {
		return 0
	}
	return r.gen
}

type renewal int

const (
	renewOK renewal = iota + 1
	renewRejected
	renewTransient
	// renewShort: the renewal returned a credential neither Fresh nor Servable. It is installed
	// anyway, since it may carry a rotated refresh token, and the asker is answered as after
	// renewTransient.
	renewShort
	// renewDiscarded: a sign-in, sign-out or engine spawn replaced the record being renewed.
	renewDiscarded
)

// flight is one backend's renewal in progress; every asker of that record waits on done.
type flight struct {
	from    *record
	renewed *record
	done    chan struct{}
	out     renewal
}

// slot is one backend's state. desired is what the file holds (or will, while a save is retried),
// effective what the running engine is answered from, basis the last record answered to this
// engine generation, cleared only by EngineSpawned.
type slot struct {
	b          Backend
	desired    *record
	effective  *record
	basis      *record
	flight     *flight
	cancelPoll context.CancelFunc
	// unavailable is the user text for a file refused or unreadable at Open, until a write
	// replaces that file.
	unavailable  string
	attemptState AttemptState
	attemptErr   string
	decl         Declaration
	mu           sync.Mutex
	attempt      uint64
	// spawns counts EngineSpawned calls, so an answer to a replaced engine never sets basis.
	spawns      uint64
	lastRefusal Refusal
	dirty       bool
}

// currentLocked reports whether attempt is the pending one, not ended or cancelled.
func (s *slot) currentLocked(attempt uint64) bool {
	return s.attempt == attempt && s.attemptState == AttemptPending
}

// endAttemptLocked moves a current attempt to state, reporting false when it was not current.
func (s *slot) endAttemptLocked(attempt uint64, state AttemptState, text string) bool {
	if !s.currentLocked(attempt) {
		return false
	}
	s.attemptState, s.attemptErr, s.cancelPoll = state, text, nil
	return true
}

func (s *slot) kindLocked() StatusKind {
	switch {
	case s.desired != nil && !s.desired.lapsed:
		return StatusSignedIn
	case s.desired == nil && s.unavailable != "":
		return statusUnavailable
	default:
		return StatusSignedOut
	}
}

func (s *slot) accountPendingLocked() bool { return genOf(s.desired) != genOf(s.effective) }

// Vault is the sign-in owner. Its goroutines (device polls, renewals, the persistence worker) run
// under the lifetime Open was given and end with it; Wait joins them.
type Vault struct {
	lifetime   context.Context
	disk       filemode.Disk
	now        func() time.Time
	slots      map[BackendID]*slot
	byCallback map[string]BackendID
	wake       chan struct{}
	// stopped is closed when the persistence worker has returned.
	stopped    chan struct{}
	notifyCond *sync.Cond
	path       string
	listeners  []func(Change)
	queue      []*job
	// leftovers are the staged files failed writes could not remove; only the worker touches them.
	leftovers   []*filemode.LeftoverError
	wg          sync.WaitGroup
	retryBase   time.Duration
	renewBudget time.Duration
	verdictWait time.Duration
	gens        atomic.Uint64
	tickets     atomic.Uint64
	delivered   uint64
	queueMu     sync.Mutex
	listenMu    sync.Mutex
	notifyMu    sync.Mutex
}

type config struct {
	disk        filemode.Disk
	now         func() time.Time
	retryBase   time.Duration
	renewBudget time.Duration
	verdictWait time.Duration
}

// Open reads <configDir>/signin.json and returns the vault. It never fails on the file: a refused,
// unreadable or malformed file is an unavailable Status, and the file is kept. It panics when two
// backends share an id or a non-empty HostCallback, a programming error in the compiled set.
func Open(lifetime context.Context, configDir string, backends ...Backend) *Vault {
	return open(lifetime, configDir, config{
		disk:        filemode.OS(),
		now:         time.Now,
		retryBase:   time.Second,
		renewBudget: renewBudget,
		verdictWait: verdictWait,
	}, backends...)
}

func open(lifetime context.Context, configDir string, cfg config, backends ...Backend) *Vault {
	v := &Vault{
		lifetime:    lifetime,
		disk:        cfg.disk,
		now:         cfg.now,
		path:        filepath.Join(configDir, fileName),
		slots:       make(map[BackendID]*slot, len(backends)),
		byCallback:  map[string]BackendID{},
		wake:        make(chan struct{}, 1),
		stopped:     make(chan struct{}),
		retryBase:   cfg.retryBase,
		renewBudget: cfg.renewBudget,
		verdictWait: cfg.verdictWait,
	}
	v.notifyCond = sync.NewCond(&v.notifyMu)
	for _, b := range backends {
		d := b.Declaration()
		if _, dup := v.slots[d.Backend]; dup {
			panic(fmt.Sprintf("signin: two backends declare the id %q", d.Backend))
		}
		if d.HostCallback != "" {
			if other, dup := v.byCallback[d.HostCallback]; dup {
				panic(fmt.Sprintf("signin: backends %q and %q declare the host callback %q", other, d.Backend, d.HostCallback))
			}
			v.byCallback[d.HostCallback] = d.Backend
		}
		v.slots[d.Backend] = &slot{b: b, decl: d}
	}
	v.load()
	v.wg.Go(v.persist)
	return v
}

// Wait blocks until every goroutine the vault started has returned. They end once the lifetime
// given to Open is done.
func (v *Vault) Wait() { v.wg.Wait() }

// Declaration returns the backend's declaration.
func (v *Vault) Declaration(id BackendID) (Declaration, bool) {
	s := v.slots[id]
	if s == nil {
		return Declaration{}, false
	}
	return s.decl, true
}

// ForHostCallback returns the backend that declared method as its host callback.
func (v *Vault) ForHostCallback(method string) (BackendID, bool) {
	id, ok := v.byCallback[method]
	return id, ok
}

// Status returns the backend's sign-in state.
func (v *Vault) Status(id BackendID) Status {
	s := v.slots[id]
	if s == nil {
		return Status{Kind: statusUnavailable, Reason: textNoBackend}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{
		Kind:   s.kindLocked(),
		SignIn: Attempt{ID: s.attempt, State: s.attemptState, Error: s.attemptErr},
	}
	switch st.Kind {
	case StatusSignedIn:
		st.Label = s.b.Label(s.desired.cred)
	case statusUnavailable:
		st.Reason = s.unavailable
	}
	return st
}

// AccountPending reports whether the desired record is not the one the running engine answers from.
func (v *Vault) AccountPending(id BackendID) bool {
	s := v.slots[id]
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accountPendingLocked()
}

// EngineSpawned promotes the desired record to effective and clears basis, because a new engine
// holds no cached credential. The engine owner calls it at each spawn before it computes the
// fingerprint it records; it fires no OnChange.
func (v *Vault) EngineSpawned(id BackendID) {
	s := v.slots[id]
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.effective = s.desired
	s.basis = nil
	s.spawns++
}

// OnChange registers fn for every committed edge. Calls arrive in commit order, one at a time, with
// no vault lock held, just after Status shows the change. A sign-in or sign-out edge is delivered
// on the persistence worker, so a SignOut from fn cannot be saved until fn returns and reports
// ErrInDoubt.
func (v *Vault) OnChange(fn func(Change)) {
	v.listenMu.Lock()
	defer v.listenMu.Unlock()
	v.listeners = append(v.listeners, fn)
}

// ticket reserves the next delivery slot; the caller takes it while holding the slot mutex of the
// change it reports, so delivery order is commit order.
func (v *Vault) ticket() uint64 { return v.tickets.Add(1) }

func (v *Vault) deliver(t uint64, c Change) {
	v.notifyMu.Lock()
	for v.delivered+1 != t {
		v.notifyCond.Wait()
	}
	v.notifyMu.Unlock()
	v.listenMu.Lock()
	fns := slices.Clone(v.listeners)
	v.listenMu.Unlock()
	for _, fn := range fns {
		fn(c)
	}
	v.notifyMu.Lock()
	v.delivered = t
	v.notifyCond.Broadcast()
	v.notifyMu.Unlock()
}

// StartSignIn begins a sign-in. ctx (the HTTP request's) bounds only Begin; once Pending is
// returned the poll runs under the vault's lifetime, so the request may end. Status shows the
// attempt pending from before Begin runs, its id is Pending.Attempt, and a Begin error ends it
// AttemptFailed.
func (v *Vault) StartSignIn(ctx context.Context, id BackendID, p FlowParams) (Pending, error) {
	s := v.slots[id]
	if s == nil {
		return Pending{}, errUnknownBackend
	}
	if !slices.Contains(s.decl.Flows, p.Flow) {
		return Pending{}, errFlowUnsupported
	}
	s.mu.Lock()
	if s.attemptState == AttemptPending {
		s.mu.Unlock()
		return Pending{}, ErrSignInInProgress
	}
	s.attempt++
	attempt := s.attempt
	s.attemptState, s.attemptErr = AttemptPending, ""
	s.mu.Unlock()

	d, err := s.b.Begin(ctx, p)
	if err != nil {
		s.mu.Lock()
		ended := s.endAttemptLocked(attempt, AttemptFailed, failureText(ctx, err))
		s.mu.Unlock()
		if !ended {
			return Pending{}, ErrSignInCancelled
		}
		slog.Warn("sign-in: the sign-in could not start", "backend", s.decl.Backend, "error", err)
		return Pending{}, err
	}
	wait := maxSignInWait
	if d.ExpiresIn > 0 {
		wait = min(wait, d.ExpiresIn)
	}
	pctx, cancel := context.WithTimeout(v.lifetime, wait)
	s.mu.Lock()
	if !s.currentLocked(attempt) {
		s.mu.Unlock()
		cancel()
		return Pending{}, ErrSignInCancelled
	}
	s.cancelPoll = cancel
	s.mu.Unlock()
	v.wg.Go(func() { v.poll(pctx, cancel, s, attempt, d) })
	pending := d.Pending
	pending.Attempt = attempt
	return pending, nil
}

func (v *Vault) poll(ctx context.Context, cancel context.CancelFunc, s *slot, attempt uint64, d DeviceStart) {
	defer cancel()
	cred, err := s.b.Poll(ctx, d)
	if err == nil {
		if vErr := s.b.Validate(cred); vErr != nil {
			err = &FailureError{Text: textUnusable, Err: vErr}
		}
	}
	if err == nil {
		v.submit(&job{s: s, rec: &record{cred: cred, gen: v.gens.Add(1)}, attempt: attempt})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if v.lifetime.Err() != nil {
		return
	}
	if s.endAttemptLocked(attempt, AttemptFailed, failureText(ctx, err)) {
		slog.Warn("sign-in: the sign-in did not complete", "backend", s.decl.Backend, "error", err)
	}
}

func failureText(ctx context.Context, err error) string {
	if fe, ok := errors.AsType[*FailureError](err); ok {
		return fe.Text
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return textSignInTimeout
	}
	return textSignInFailed
}

// SignOut cancels a running sign-in (its attempt reads attemptCancelled), then hands the removal to
// the worker, which removes the backend's record from the file and only then from memory, after
// every sign-in submitted before it. A failed write, or a rewrite refused while a failed save's
// staged copy is still on disk, returns an error wrapping errNotSaved, and the account stays
// answered. With no record by its turn it changes nothing, so a file refused at
// Open is kept. Past verdictWait it returns an error wrapping ErrInDoubt while the worker goes on.
func (v *Vault) SignOut(id BackendID) error {
	s := v.slots[id]
	if s == nil {
		return errUnknownBackend
	}
	s.mu.Lock()
	if s.attemptState == AttemptPending {
		s.attemptState, s.attemptErr = attemptCancelled, ""
	}
	if s.cancelPoll != nil {
		s.cancelPoll()
		s.cancelPoll = nil
	}
	s.mu.Unlock()

	j := &job{s: s, verdict: make(chan error, 1)}
	v.submit(j)
	t := time.NewTimer(v.verdictWait)
	defer t.Stop()
	select {
	case err := <-j.verdict:
		return err
	case <-v.stopped:
		select {
		case err := <-j.verdict:
			return err
		default:
			return fmt.Errorf("%w: marotte is shutting down", errNotSaved)
		}
	case <-t.C:
		slog.Error("sign-in: the sign-out is still being saved", "backend", id, "waited", v.verdictWait)
		return fmt.Errorf("%w: the sign-out is still being saved", ErrInDoubt)
	}
}

// Answer answers the backend's host callback from the effective record, renewing it first when the
// backend asks. ctx bounds only this caller's wait: the renewal runs once per backend under the
// vault's lifetime, so an asker giving up never cancels the renewal others share.
func (v *Vault) Answer(ctx context.Context, id BackendID) (json.RawMessage, *marotte.RPCError) {
	s := v.slots[id]
	if s == nil {
		return nil, &marotte.RPCError{Code: -32603, Message: textNoBackend}
	}
	// A second pass runs only when a sign-in or sign-out replaced the record mid-renewal.
	for range 2 {
		s.mu.Lock()
		r, spawns := s.effective, s.spawns
		s.mu.Unlock()
		if r == nil || r.lapsed {
			return refuse(s, RefusedSignedOut)
		}
		if s.b.Fresh(r.cred, v.now()) {
			return v.serve(s, r, spawns)
		}
		f, ok := v.awaitRenewal(ctx, s, r)
		if !ok {
			return refuse(s, RefusedUnreachable)
		}
		switch f.out {
		case renewOK:
			return v.serve(s, f.renewed, spawns)
		case renewRejected:
			return refuse(s, RefusedRejected)
		case renewTransient, renewShort:
			if s.b.Servable(r.cred, v.now()) {
				return v.serve(s, r, spawns)
			}
			return refuse(s, RefusedUnreachable)
		case renewDiscarded:
		}
	}
	return refuse(s, RefusedUnreachable)
}

// serve answers from r, which becomes basis unless an engine spawned since the ask began.
func (v *Vault) serve(s *slot, r *record, spawns uint64) (json.RawMessage, *marotte.RPCError) {
	res, err := s.b.Result(r.cred, v.now())
	if err != nil {
		slog.Error("sign-in: the backend could not build its credential answer", "backend", s.decl.Backend, "error", err)
		return refuse(s, RefusedSignedOut)
	}
	s.mu.Lock()
	if s.spawns == spawns {
		s.basis = r
	}
	s.lastRefusal = 0
	s.mu.Unlock()
	return res, nil
}

func refuse(s *slot, why Refusal) (json.RawMessage, *marotte.RPCError) {
	s.mu.Lock()
	changed := s.lastRefusal != why
	s.lastRefusal = why
	s.mu.Unlock()
	if changed {
		slog.Warn("sign-in: the engine's credential request was refused", "backend", s.decl.Backend, "refusal", why.String())
	}
	return nil, s.b.Refusal(why)
}

func (r Refusal) String() string {
	switch r {
	case RefusedSignedOut:
		return "signed_out"
	case RefusedRejected:
		return "rejected"
	case RefusedUnreachable:
		return "unreachable"
	default:
		return fmt.Sprintf("refusal(%d)", int(r))
	}
}

// awaitRenewal joins the backend's renewal, starting one from r when none runs. A flight from an
// older record, or r no longer effective, counts as discarded for r: renewing a replaced record
// would spend its refresh token, which a provider detecting reuse revokes. ok is false when ctx
// ended first.
func (v *Vault) awaitRenewal(ctx context.Context, s *slot, r *record) (*flight, bool) {
	s.mu.Lock()
	if s.effective != r {
		s.mu.Unlock()
		return &flight{out: renewDiscarded}, true
	}
	f := s.flight
	if f == nil {
		f = &flight{from: r, done: make(chan struct{})}
		s.flight = f
		v.wg.Go(func() { v.renew(s, f) })
	}
	s.mu.Unlock()
	select {
	case <-f.done:
	case <-ctx.Done():
		return nil, false
	}
	if f.from != r {
		return &flight{out: renewDiscarded}, true
	}
	return f, true
}

// renew runs one renewal and installs its outcome only while f.from is still effective: a sign-in,
// sign-out or engine spawn that replaced it discards the result.
func (v *Vault) renew(s *slot, f *flight) {
	ctx, cancel := context.WithTimeout(v.lifetime, v.renewBudget)
	defer cancel()
	start := time.Now()
	cred, err := s.b.Refresh(ctx, f.from.cred)
	if err == nil {
		if vErr := s.b.Validate(cred); vErr != nil {
			err = vErr
		}
	}
	now := v.now()
	short := err == nil && !s.b.Fresh(cred, now) && !s.b.Servable(cred, now)

	s.mu.Lock()
	paired := s.desired == s.effective
	var (
		c         Change
		t         uint64
		lapsedNow bool
		save      bool
	)
	switch {
	case s.effective != f.from:
		f.out = renewDiscarded
	case err == nil:
		f.out = renewOK
		if short {
			f.out = renewShort
		}
		f.renewed = &record{cred: cred, gen: f.from.gen}
		s.effective = f.renewed
		if paired {
			s.desired = f.renewed
			s.dirty, save = true, true
		}
	case errors.Is(err, ErrRejected):
		f.out = renewRejected
		lapsed := &record{cred: f.from.cred, gen: f.from.gen, lapsed: true}
		s.effective = lapsed
		if paired {
			from := s.kindLocked()
			s.desired = lapsed
			s.dirty, save = true, true
			c = Change{Backend: s.decl.Backend, From: from, To: s.kindLocked(), AccountPending: s.accountPendingLocked()}
			t = v.ticket()
			lapsedNow = true
		}
	default:
		f.out = renewTransient
	}
	s.flight = nil
	s.mu.Unlock()
	close(f.done)

	attrs := []any{"backend", s.decl.Backend, "outcome", f.out.String(), "duration", time.Since(start).Round(time.Millisecond)}
	if err != nil {
		attrs = append(attrs, "error", err)
	}
	slog.Info("sign-in: renewal finished", attrs...)
	if save {
		v.nudge()
	}
	if lapsedNow {
		v.deliver(t, c)
	}
}

func (r renewal) String() string {
	switch r {
	case renewOK:
		return "renewed"
	case renewRejected:
		return "rejected"
	case renewTransient:
		return "transient_failure"
	case renewShort:
		return "short_lease"
	case renewDiscarded:
		return "discarded"
	default:
		return fmt.Sprintf("renewal(%d)", int(r))
	}
}
