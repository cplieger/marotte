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
	// answerWindow bounds Answer's renewal retries; it equals one renewal's budget, so a renewal that hangs is
	// one attempt and one that fails fast is retried every renewGap within it.
	answerWindow = renewBudget
	renewGap     = 2 * time.Second
	// probeEvery is how often an unreachable record is renewed with no asker, so it turns answerable again
	// while nothing asks.
	probeEvery = 30 * time.Second
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
	// anyway, since it may carry a rotated refresh token, and marks the record unreachable until a
	// renewal returns a usable one.
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
	// answer is the Answerability last delivered to OnAnswerability, or set silently by EngineSpawned.
	answer Answerability
	dirty  bool
	// unreachable marks effective as a record the provider gave no usable renewal for: none answered within an
	// asker's window, or the last one was short. Replacing effective or a usable renewal clears it.
	unreachable bool
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

func (s *slot) answerabilityLocked() Answerability {
	switch {
	case s.effective == nil || s.effective.lapsed:
		return NeedsSignIn
	case s.unreachable:
		return ProviderUnreachable
	default:
		return Answerable
	}
}

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
	answerFns  []func(BackendID, Answerability)
	queue      []*job
	// leftovers are the staged files failed writes could not remove; only the worker touches them.
	leftovers    []*filemode.LeftoverError
	wg           sync.WaitGroup
	retryBase    time.Duration
	renewBudget  time.Duration
	verdictWait  time.Duration
	answerWindow time.Duration
	renewGap     time.Duration
	probeEvery   time.Duration
	gens         atomic.Uint64
	tickets      atomic.Uint64
	delivered    uint64
	queueMu      sync.Mutex
	listenMu     sync.Mutex
	notifyMu     sync.Mutex
}

type config struct {
	disk         filemode.Disk
	now          func() time.Time
	retryBase    time.Duration
	renewBudget  time.Duration
	verdictWait  time.Duration
	answerWindow time.Duration
	renewGap     time.Duration
	probeEvery   time.Duration
}

// Open reads <configDir>/signin.json and returns the vault. It never fails on the file: a refused,
// unreadable or malformed file is an unavailable Status, and the file is kept. It panics when two
// backends share an id or a non-empty HostCallback, a programming error in the compiled set.
func Open(lifetime context.Context, configDir string, backends ...Backend) *Vault {
	return open(lifetime, configDir, config{
		disk:         filemode.OS(),
		now:          time.Now,
		retryBase:    time.Second,
		renewBudget:  renewBudget,
		verdictWait:  verdictWait,
		answerWindow: answerWindow,
		renewGap:     renewGap,
		probeEvery:   probeEvery,
	}, backends...)
}

func open(lifetime context.Context, configDir string, cfg config, backends ...Backend) *Vault {
	v := &Vault{
		lifetime:     lifetime,
		disk:         cfg.disk,
		now:          cfg.now,
		path:         filepath.Join(configDir, fileName),
		slots:        make(map[BackendID]*slot, len(backends)),
		byCallback:   map[string]BackendID{},
		wake:         make(chan struct{}, 1),
		stopped:      make(chan struct{}),
		retryBase:    cfg.retryBase,
		renewBudget:  cfg.renewBudget,
		verdictWait:  cfg.verdictWait,
		answerWindow: cfg.answerWindow,
		renewGap:     cfg.renewGap,
		probeEvery:   cfg.probeEvery,
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
	for _, s := range v.slots {
		s.answer = s.answerabilityLocked()
	}
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
// fingerprint it records; it fires neither OnChange nor OnAnswerability.
func (v *Vault) EngineSpawned(id BackendID) {
	s := v.slots[id]
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.effective != s.desired {
		s.unreachable = false
	}
	s.effective = s.desired
	s.basis = nil
	s.spawns++
	s.answer = s.answerabilityLocked()
}

// Answerability reports the backend's last observed Answerability, with no renewal; an unknown backend needs a
// sign-in it cannot get.
func (v *Vault) Answerability(id BackendID) Answerability {
	s := v.slots[id]
	if s == nil {
		return NeedsSignIn
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.answerabilityLocked()
}

// OnAnswerability registers fn for every move of a backend's Answerability. Calls arrive one at a time with no
// vault lock held, in one commit order with OnChange's, except the move EngineSpawned makes: that one is read with
// Answerability once the engine is up.
func (v *Vault) OnAnswerability(fn func(BackendID, Answerability)) {
	v.listenMu.Lock()
	defer v.listenMu.Unlock()
	v.answerFns = append(v.answerFns, fn)
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
	v.inTurn(t, func() {
		v.listenMu.Lock()
		fns := slices.Clone(v.listeners)
		v.listenMu.Unlock()
		for _, fn := range fns {
			fn(c)
		}
	})
}

// noteAnswerLocked reserves a delivery for the slot's Answerability when it moved since the last one; t is 0
// when it did not.
func (v *Vault) noteAnswerLocked(s *slot) (t uint64, a Answerability) {
	a = s.answerabilityLocked()
	if a == s.answer {
		return 0, a
	}
	s.answer = a
	return v.ticket(), a
}

// deliverAnswer delivers ticket t of noteAnswerLocked; a zero t delivers nothing.
func (v *Vault) deliverAnswer(t uint64, id BackendID, a Answerability) {
	if t == 0 {
		return
	}
	v.inTurn(t, func() {
		v.listenMu.Lock()
		fns := slices.Clone(v.answerFns)
		v.listenMu.Unlock()
		for _, fn := range fns {
			fn(id, a)
		}
	})
}

// inTurn runs call once every earlier ticket was delivered. Every ticket must reach it, or later ones wait forever.
func (v *Vault) inTurn(t uint64, call func()) {
	v.notifyMu.Lock()
	for v.delivered+1 != t {
		v.notifyCond.Wait()
	}
	v.notifyMu.Unlock()
	call()
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
// backend asks. A renewal that fails transiently while the record cannot be served is retried every
// renewGap until answerWindow passes or ctx ends; only that window passing, or a short lease, marks
// the record ProviderUnreachable. ctx bounds only this caller's wait: the renewal runs once per
// backend under the vault's lifetime, so an asker giving up never cancels the renewal others share.
func (v *Vault) Answer(ctx context.Context, id BackendID) (json.RawMessage, *marotte.RPCError) {
	s := v.slots[id]
	if s == nil {
		return nil, &marotte.RPCError{Code: -32603, Message: textNoBackend}
	}
	ctx, cancel := context.WithTimeoutCause(ctx, v.answerWindow, errAnswerWindow)
	defer cancel()
	// A second discard only follows a second sign-in or sign-out during this one ask.
	for discards := 0; discards < 2; {
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
			return v.windowEnded(ctx, s, r.gen)
		}
		if f.out == renewDiscarded {
			discards++
			continue
		}
		if res, rerr, again := v.afterRenewal(ctx, s, r, f, spawns); !again {
			return res, rerr
		}
	}
	return refuse(s, RefusedUnreachable)
}

// afterRenewal answers from f, a finished renewal of r, or reports again once renewGap passed after a transient
// failure on a record that cannot be served.
func (v *Vault) afterRenewal(
	ctx context.Context, s *slot, r *record, f *flight, spawns uint64,
) (res json.RawMessage, rerr *marotte.RPCError, again bool) {
	if f.out == renewOK {
		res, rerr = v.serve(s, f.renewed, spawns)
		return res, rerr, false
	}
	if f.out == renewRejected {
		res, rerr = refuse(s, RefusedRejected)
		return res, rerr, false
	}
	if s.b.Servable(r.cred, v.now()) {
		res, rerr = v.serve(s, r, spawns)
		return res, rerr, false
	}
	// A short lease is the provider's answer; asking again only rotates its refresh token.
	if f.out == renewShort {
		res, rerr = refuse(s, RefusedUnreachable)
		return res, rerr, false
	}
	if !sleepCtx(ctx, v.renewGap) {
		res, rerr = v.windowEnded(ctx, s, r.gen)
		return res, rerr, false
	}
	return nil, nil, true
}

// errAnswerWindow is the cause of Answer's own window ending, which an asker's earlier deadline or cancel is not.
var errAnswerWindow = errors.New("the answer window elapsed without a usable renewal")

// windowEnded refuses an asker whose wait ended with no usable renewal. Only Answer's own window elapsing marks the
// account's record unreachable: an asker that gave up first says nothing about the provider.
func (v *Vault) windowEnded(ctx context.Context, s *slot, gen uint64) (json.RawMessage, *marotte.RPCError) {
	if errors.Is(context.Cause(ctx), errAnswerWindow) && v.lifetime.Err() == nil {
		v.markUnreachable(s, gen)
	}
	return refuse(s, RefusedUnreachable)
}

// markUnreachable marks the effective record of account gen unreachable, unless it lapsed, was replaced or can
// be answered again, and starts the reprobe that clears the mark.
func (v *Vault) markUnreachable(s *slot, gen uint64) {
	s.mu.Lock()
	r := s.effective
	s.mu.Unlock()
	if r == nil || r.lapsed || r.gen != gen {
		return
	}
	if now := v.now(); s.b.Fresh(r.cred, now) || s.b.Servable(r.cred, now) {
		return
	}
	s.mu.Lock()
	if s.unreachable || s.effective != r {
		s.mu.Unlock()
		return
	}
	s.unreachable = true
	t, a := v.noteAnswerLocked(s)
	s.mu.Unlock()
	slog.Warn("sign-in: no renewal succeeded within the answer window; renewing until one does",
		"backend", s.decl.Backend)
	v.wg.Go(func() { v.reprobe(s) })
	v.deliverAnswer(t, s.decl.Backend, a)
}

// reprobe renews the effective record every probeEvery while it is marked unreachable, so it turns answerable
// again when nothing asks. A usable renewal clears the mark; a lapse or a replaced record ends the loop too.
func (v *Vault) reprobe(s *slot) {
	tick := time.NewTicker(v.probeEvery)
	defer tick.Stop()
	for {
		select {
		case <-v.lifetime.Done():
			return
		case <-tick.C:
		}
		s.mu.Lock()
		r, marked := s.effective, s.unreachable
		s.mu.Unlock()
		if !marked || r == nil || r.lapsed {
			return
		}
		v.awaitRenewal(v.lifetime, s, r)
	}
}

// sleepCtx waits d, reporting false when ctx ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
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
		probe     bool
	)
	switch {
	case s.effective != f.from:
		f.out = renewDiscarded
	case err == nil:
		f.out = renewOK
		if short {
			f.out = renewShort
			probe = !s.unreachable
		}
		s.unreachable = short
		f.renewed = &record{cred: cred, gen: f.from.gen}
		s.effective = f.renewed
		if paired {
			s.desired = f.renewed
			s.dirty, save = true, true
		}
	case errors.Is(err, ErrRejected):
		f.out = renewRejected
		lapsed := &record{cred: f.from.cred, gen: f.from.gen, lapsed: true}
		s.effective, s.unreachable = lapsed, false
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
	// After the Change's ticket, so a lapse reaches OnChange before OnAnswerability.
	at, a := v.noteAnswerLocked(s)
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
	if probe {
		slog.Warn("sign-in: the identity provider returned a lease too short to use; renewing until it does not",
			"backend", s.decl.Backend)
		v.wg.Go(func() { v.reprobe(s) })
	}
	if lapsedNow {
		v.deliver(t, c)
	}
	v.deliverAnswer(at, s.decl.Backend, a)
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
