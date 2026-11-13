package applogin

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/access"
	"github.com/cplieger/access/accesstest"
	"github.com/cplieger/access/account"
	"github.com/cplieger/go/slogx/capture"
)

// waitUntil polls cond every 5 ms for up to 5 s and fails naming what never happened.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen within 5 s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// fenceStore fences s through a non-durable session write, with the fence loop on tick.
func fenceStore(t *testing.T, s *Store, d *testDisk, tick <-chan time.Time) {
	t.Helper()
	s.setFenceTick(tick)
	d.faults().AfterPublish()
	if err := s.CreateSession(plainCtx(t.Context()), newTestSession(1, epoch)); !errors.Is(err, ErrNotDurable) {
		t.Fatalf("the non-durable write = %v, want ErrNotDurable", err)
	}
	if !s.Fenced() {
		t.Fatal("the store is not fenced after a non-durable publication")
	}
}

func TestCleanup_RefsOnlyForADurablePublish(t *testing.T) {
	ctx := t.Context()
	now := epoch.Add(48 * time.Hour)
	timeouts := access.SessionTimeouts{Idle: time.Hour, Absolute: 24 * time.Hour}
	seed := func(t *testing.T) (*Store, *testDisk, *access.Session, *access.Session) {
		s, d := openTestStore(t)
		expired, live := newTestSession(1, epoch), newTestSession(1, now.Add(-time.Minute))
		live.CreatedAt = now.Add(-time.Hour)
		for _, sess := range []*access.Session{expired, live} {
			if err := s.CreateSession(plainCtx(ctx), sess); err != nil {
				t.Fatal(err)
			}
		}
		return s, d, expired, live
	}
	t.Run("before_publish_fails", func(t *testing.T) {
		s, d, expired, live := seed(t)
		d.faults().BeforePublish()
		refs, err := s.CleanupExpiredSessions(ctx, now, timeouts, 10)
		if err == nil || refs != nil {
			t.Errorf("CleanupExpiredSessions = (%v, %v), want an error and nil refs", refs, err)
		}
		s = reopenStore(t, s, d)
		for _, sess := range []*access.Session{expired, live} {
			if _, ok := s.sessionRecord(sess.TokenHash); !ok {
				t.Errorf("session %s gone after a failed publication", sess.ID)
			}
		}
	})
	t.Run("not_durable", func(t *testing.T) {
		s, d, expired, live := seed(t)
		var unconfirmed atomic.Int32
		s.SetFenceHooks(func() { unconfirmed.Add(1) }, func() {})
		s.setFenceTick(make(chan time.Time))
		d.faults().AfterPublish()
		refs, err := s.CleanupExpiredSessions(ctx, now, timeouts, 10)
		if !errors.Is(err, ErrNotDurable) || refs != nil {
			t.Errorf("CleanupExpiredSessions = (%v, %v), want ErrNotDurable and nil refs", refs, err)
		}
		if _, ok := s.sessionRecord(expired.TokenHash); ok {
			t.Error("cur still holds the expired session after a non-durable publication")
		}
		if _, ok := s.sessionRecord(live.TokenHash); !ok {
			t.Error("the live session is gone")
		}
		if !s.Fenced() {
			t.Error("not fenced after a non-durable publication")
		}
		waitUntil(t, "onUnconfirmed", func() bool { return unconfirmed.Load() == 1 })
		snap, err := decodeFile(d.path)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := snap.sessByID[expired.ID]; ok {
			t.Error("the file still holds the expired session after a non-durable publication")
		}
	})
	t.Run("ctx_cancelled_during_hold", func(t *testing.T) {
		s, _, expired, _ := seed(t)
		entered, _, release := s.holdGroup()
		cctx, cancel := context.WithCancel(ctx)
		type result struct {
			refs []access.SessionRef
			err  error
		}
		done := make(chan result, 1)
		go func() {
			refs, err := s.CleanupExpiredSessions(cctx, now, timeouts, 10)
			done <- result{refs, err}
		}()
		<-entered
		cancel()
		release()
		r := <-done
		if r.err != nil || len(r.refs) != 1 || r.refs[0].ID != expired.ID {
			t.Errorf("CleanupExpiredSessions with ctx cancelled during the hold = (%v, %v), want the expired session's ref", r.refs, r.err)
		}
	})
}

func decodeFile(path string) (*snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return decode(data)
}

// TestFence_RevokeNotDurable: a revoke whose publish is not durable, at the store and account
// layers.
func TestFence_RevokeNotDurable(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	h.setupOwner(ctx)
	keep := h.signIn(ctx)
	revoked := h.signIn(ctx)
	p := h.principal(keep.Token)
	var unconfirmed atomic.Int32
	h.store.SetFenceHooks(func() { unconfirmed.Add(1) }, nil)
	tick := make(chan time.Time)
	h.store.setFenceTick(tick)
	before := len(h.events.evs)
	h.disk.faults().AfterPublish()
	err := h.svc.RevokeSession(ctx, p, revoked.Session.ID)
	if !errors.Is(err, access.ErrAuthBackend) {
		t.Fatalf("RevokeSession on a non-durable publish = %v, want ErrAuthBackend", err)
	}
	if got := len(h.events.of(account.EventSessionRevoked)); got != 0 || len(h.events.evs) != before {
		t.Errorf("events fired on a non-durable revoke: %d revoked, %d total new", got, len(h.events.evs)-before)
	}
	if _, ok := h.store.sessionRecord(revoked.Session.TokenHash); ok {
		t.Error("reads still show the revoked session")
	}
	waitUntil(t, "onUnconfirmed", func() bool { return unconfirmed.Load() == 1 })
	file, _ := os.ReadFile(h.disk.path)
	if _, err := h.svc.PasswordLogin(plainCtx(ctx), testClient, account.Credentials{Username: testUsername, Password: access.NewSecret(testPassword)}); !errors.Is(err, access.ErrAuthBackend) {
		t.Errorf("a sign-in while fenced = %v, want ErrAuthBackend (503)", err)
	}
	if after, _ := os.ReadFile(h.disk.path); !bytes.Equal(file, after) {
		t.Error("a refused sign-in changed the file while fenced")
	}
	h.principal(keep.Token)
	tick <- time.Now()
	waitUntil(t, "the fence's clear", func() bool { return !h.store.Fenced() })
}

func TestFence_ConfirmIsAWriterJob(t *testing.T) {
	t.Run("behind_a_held_group", func(t *testing.T) {
		logs := capture.Default(t)
		s, d := openTestStore(t)
		var confirmed atomic.Int32
		s.SetFenceHooks(nil, func() { confirmed.Add(1) })
		tick := make(chan time.Time)
		fenceStore(t, s, d, tick)
		entered, pending, release := s.holdGroup()
		attached := make(chan error, 1)
		go func() { attached <- s.attachProvider(context.Background(), 1, nil) }()
		<-entered
		calls := len(d.recorded())
		tick <- time.Now()
		waitUntil(t, "the queued confirm job", func() bool { return pending() == 2 })
		if n := len(d.recorded()); n != calls {
			t.Errorf("the disk saw %d calls while the attach group was held", n-calls)
		}
		if !s.Fenced() || confirmed.Load() != 0 {
			t.Errorf("while held: fenced %t, onConfirmed %d, want fenced and none", s.Fenced(), confirmed.Load())
		}
		release()
		if err := <-attached; err != nil {
			t.Errorf("attachProvider while fenced = %v, want nil", err)
		}
		waitUntil(t, "onConfirmed", func() bool { return confirmed.Load() == 1 })
		if s.Fenced() {
			t.Error("still fenced after a durable confirm")
		}
		want, err := encode(s.cur.Load())
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(d.path); !bytes.Equal(got, want) {
			t.Error("the file after the confirm differs from cur's serialization")
		}
		if n := logs.CountExact("access: sign-in data is confirmed on disk again"); n != 1 {
			t.Errorf("the clear's INFO logged %d times, want 1", n)
		}
	})
	t.Run("first_confirm_not_durable", func(t *testing.T) {
		s, d := openTestStore(t)
		var confirmed atomic.Int32
		s.SetFenceHooks(nil, func() { confirmed.Add(1) })
		tick := make(chan time.Time)
		fenceStore(t, s, d, tick)
		d.faults().AfterPublish()
		base := d.publishes.Load()
		tick <- time.Now()
		waitUntil(t, "the first confirm's commit", func() bool { d.mu.Lock(); defer d.mu.Unlock(); return d.after == 0 })
		waitUntil(t, "the writer idle", func() bool { return s.pending() == 0 })
		if !s.Fenced() || confirmed.Load() != 0 || d.publishes.Load() != base {
			t.Errorf("after a non-durable confirm: fenced %t, onConfirmed %d, publishes +%d; want fenced, none, +0",
				s.Fenced(), confirmed.Load(), d.publishes.Load()-base)
		}
		tick <- time.Now()
		waitUntil(t, "onConfirmed", func() bool { return confirmed.Load() == 1 })
		if s.Fenced() {
			t.Error("still fenced after the second, durable confirm")
		}
	})
	t.Run("writes_refused_while_fenced", func(t *testing.T) {
		s, d := openTestStore(t)
		var unconfirmed atomic.Int32
		s.SetFenceHooks(func() { unconfirmed.Add(1) }, nil)
		fenceStore(t, s, d, make(chan time.Time))
		if err := s.CreateSession(plainCtx(t.Context()), newTestSession(1, epoch)); !errors.Is(err, ErrFenced) {
			t.Errorf("a write while fenced = %v, want ErrFenced", err)
		}
		d.faults().AfterPublish()
		if err := s.CreateSession(plainCtx(t.Context()), newTestSession(1, epoch)); !errors.Is(err, ErrFenced) {
			t.Errorf("a second write while fenced = %v, want ErrFenced", err)
		}
		waitUntil(t, "onUnconfirmed", func() bool { return unconfirmed.Load() >= 1 })
		time.Sleep(20 * time.Millisecond)
		if n := unconfirmed.Load(); n != 1 {
			t.Errorf("onUnconfirmed ran %d times for one fence, want 1", n)
		}
	})
}

// TestHeldEntries_AppendedOnceInOrder: held own log entries across the arms.
func TestHeldEntries_AppendedOnceInOrder(t *testing.T) {
	kinds := func(t *testing.T, h *harness) []string {
		t.Helper()
		es, err := h.store.ListLog(t.Context(), account.LogRange{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range es {
			if e.Event.Kind == kindStoreReset || e.Event.Kind == kindStoreFenced {
				out = append(out, string(e.Event.Kind)+"@"+e.Detail)
			}
		}
		return out
	}
	t.Run("r1_reset_then_fence_in_order", func(t *testing.T) {
		h := newHarness(t)
		tick := make(chan time.Time)
		fenceStore(t, h.store, h.disk, tick)
		start := h.store.fence.since.Format(time.RFC3339)
		reset := Reset{Aside: "x", At: epoch}
		h.store.appendHeld(resetEntry(reset))
		tick <- time.Now()
		want := []string{kindStoreFenced + "@" + start, kindStoreReset + "@" + epoch.Format(time.RFC3339)}
		waitUntil(t, "both entries", func() bool { return len(kinds(t, h)) == 2 })
		if got := kinds(t, h); !slices.Equal(got, want) {
			t.Errorf("entries %v, want %v", got, want)
		}
	})
	t.Run("r2_holds_the_new_fence", func(t *testing.T) {
		h := newHarness(t)
		tick := make(chan time.Time)
		fenceStore(t, h.store, h.disk, tick)
		first := h.store.fence.since.Format(time.RFC3339)
		var calls atomic.Int32
		h.store.SetFenceHooks(nil, func() {
			if calls.Add(1) == 1 {
				h.disk.faults().AfterPublish()
			}
			h.store.appendHeld()
		})
		tick <- time.Now()
		waitUntil(t, "the second fence", func() bool { return calls.Load() == 1 && h.store.Fenced() && h.store.pending() == 0 })
		second := h.store.fence.since.Format(time.RFC3339)
		tick <- time.Now()
		waitUntil(t, "two fence entries", func() bool { return len(kinds(t, h)) == 2 })
		want := []string{kindStoreFenced + "@" + first, kindStoreFenced + "@" + second}
		if got := kinds(t, h); !slices.Equal(got, want) {
			t.Errorf("entries %v, want %v", got, want)
		}
	})
	t.Run("r3_keeps_them", func(t *testing.T) {
		h := newHarness(t)
		tick := make(chan time.Time)
		fenceStore(t, h.store, h.disk, tick)
		h.store.appendHeld(resetEntry(Reset{Aside: "x", At: epoch}))
		waitUntil(t, "the refused job", func() bool { return h.store.pending() == 0 })
		if got := kinds(t, h); len(got) != 0 {
			t.Errorf("entries appended while fenced: %v", got)
		}
		tick <- time.Now()
		waitUntil(t, "both entries after the clear", func() bool { return len(kinds(t, h)) == 2 })
	})
	t.Run("r4_put_back_and_retried", func(t *testing.T) {
		logs := capture.Default(t)
		old := fenceInterval
		fenceInterval = 20 * time.Millisecond
		t.Cleanup(func() { fenceInterval = old })
		h := newHarness(t)
		failing := atomic.Bool{}
		failing.Store(true)
		h.disk.setInject(func(op, path string) error {
			if op == "stage" && path == h.disk.path && failing.Load() {
				return errInjected
			}
			return nil
		})
		h.store.appendHeld(resetEntry(Reset{Aside: "x", At: epoch}))
		waitUntil(t, "two failing retries", func() bool {
			n := 0
			for _, c := range h.disk.recorded() {
				if c.op == "stage" && c.path == h.disk.path {
					n++
				}
			}
			return n >= 3
		})
		failing.Store(false)
		waitUntil(t, "the retried append", func() bool { return len(kinds(t, h)) == 1 })
		time.Sleep(60 * time.Millisecond)
		if got := kinds(t, h); len(got) != 1 {
			t.Errorf("entries %v after the fault lifted, want the reset entry once", got)
		}
		if n := logs.CountExact("access: security log entry not saved"); n != 1 {
			t.Errorf("the failure ERROR logged %d times over the failing run, want 1", n)
		}
	})
}

// prePublishOp is one operation of TestFault_EachPrePublishStepLeavesTheOldState: prepare runs
// what comes before it and returns the operation, whose update-th Update the fault hits.
type prePublishOp struct {
	prepare func(t *testing.T, h *harness) func(h *harness) error
	name    string
	update  int32
}

func prePublishOps() []prePublishOp {
	setupToken := func(t *testing.T, h *harness) access.Secret {
		t.Helper()
		tok, err := h.svc.IssueSetupToken(t.Context(), account.OutOfBand("boot"), time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	recovery := func(t *testing.T, h *harness) access.Secret {
		t.Helper()
		h.setupOwner(t.Context())
		tok, err := h.svc.IssueRecovery(t.Context(), account.OutOfBand("admin-socket"), h.owner().ID, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		rec, err := h.svc.StartRecovery(httpsCtx(t.Context()), testClient, tok.Plaintext)
		if err != nil {
			t.Fatal(err)
		}
		return rec.Issued.Token
	}
	return []prePublishOp{
		{name: "setup_password", update: 1, prepare: func(t *testing.T, h *harness) func(*harness) error {
			tok := setupToken(t, h)
			return func(h *harness) error {
				_, err := h.svc.SetupWithPassword(plainCtx(t.Context()), testClient, account.SetupPassword{
					Token: tok, Password: access.NewSecret(testPassword), Username: testUsername,
				})
				return err
			}
		}},
		{name: "setup_passkey", update: 1, prepare: func(t *testing.T, h *harness) func(*harness) error {
			tok := setupToken(t, h)
			dev := accesstest.NewPasskeyDevice(t)
			return func(h *harness) error {
				cer, err := h.svc.SetupPasskeyBegin(httpsCtx(t.Context()), testClient, account.SetupPasskey{
					Token: tok, Username: testUsername, Origin: webOrigin(t),
				})
				if err != nil {
					return err
				}
				_, err = h.svc.SetupPasskeyFinish(httpsCtx(t.Context()), testClient, cer.ID,
					bytes.NewReader(dev.Register(mustJSON(t, cer.Creation))))
				return err
			}
		}},
		{name: "recovery_start", update: 1, prepare: func(t *testing.T, h *harness) func(*harness) error {
			h.setupOwner(t.Context())
			tok, err := h.svc.IssueRecovery(t.Context(), account.OutOfBand("admin-socket"), h.owner().ID, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			return func(h *harness) error {
				_, err := h.svc.StartRecovery(httpsCtx(t.Context()), testClient, tok.Plaintext)
				return err
			}
		}},
		{name: "recovery_password", update: 1, prepare: func(t *testing.T, h *harness) func(*harness) error {
			tok := recovery(t, h)
			return func(h *harness) error {
				return h.svc.RecoverWithPassword(httpsCtx(t.Context()), h.recoveryPrincipal(tok),
					access.NewSecret(recoveredPassword), account.RecoverOptions{})
			}
		}},
		{name: "recovery_passkey", update: 1, prepare: func(t *testing.T, h *harness) func(*harness) error {
			tok := recovery(t, h)
			dev := accesstest.NewPasskeyDevice(t)
			return func(h *harness) error {
				p := h.recoveryPrincipal(tok)
				cer, err := h.svc.RecoverPasskeyBegin(t.Context(), p, webOrigin(t))
				if err != nil {
					return err
				}
				return h.svc.RecoverPasskeyFinish(httpsCtx(t.Context()), p, cer.ID,
					bytes.NewReader(dev.Register(mustJSON(t, cer.Creation))), account.RecoverOptions{})
			}
		}},
		// SetPassword counts the current password in one Update and changes it in a second.
		{name: "set_password", update: 2, prepare: func(t *testing.T, h *harness) func(*harness) error {
			h.setupOwner(t.Context())
			tok := h.signIn(t.Context()).Token
			return func(h *harness) error {
				_, err := h.svc.SetPassword(plainCtx(t.Context()), testClient, h.principal(tok),
					access.NewSecret(testPassword), access.NewSecret(recoveredPassword))
				return err
			}
		}},
	}
}

func (l *eventLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.evs)
}

// TestFault_EachPrePublishStepLeavesTheOldState: each pre-publication step
// of the disk seam failing in turn (the temp create, the write, and the commit, which holds the
// file's fsync and the rename) leaves the old state on disk, in memory and on reopen, answers
// ErrAuthBackend and fires no event; after the reopen the same operation, its token or session
// unspent, lands.
func TestFault_EachPrePublishStepLeavesTheOldState(t *testing.T) {
	for _, op := range prePublishOps() {
		for _, step := range []string{"stage", "write", "commit"} {
			t.Run(op.name+"_"+step, func(t *testing.T) {
				h := newHarness(t)
				run := op.prepare(t, h)
				// The old state is the one the faulted Update starts from: SetPassword's count
				// before it is its own durable step.
				var file []byte
				var cur *snapshot
				var events int
				var armed, fired atomic.Bool
				var updates atomic.Int32
				updateHook = func() {
					if updates.Add(1) == op.update {
						file, _ = os.ReadFile(h.disk.path)
						cur, events = h.store.cur.Load(), h.events.count()
						armed.Store(true)
					}
				}
				t.Cleanup(func() { updateHook = nil })
				h.disk.setInject(func(o, path string) error {
					if o == step && path == h.disk.path && armed.Load() && fired.CompareAndSwap(false, true) {
						return errInjected
					}
					return nil
				})
				if err := run(h); !errors.Is(err, access.ErrAuthBackend) {
					t.Fatalf("with the %s failing: %v, want ErrAuthBackend", step, err)
				}
				if !fired.Load() {
					t.Fatalf("the %s fault never fired", step)
				}
				if got, _ := os.ReadFile(h.disk.path); !bytes.Equal(got, file) {
					t.Error("the file changed")
				}
				if h.store.cur.Load() != cur {
					t.Error("the published state changed")
				}
				if n := h.events.count() - events; n != 0 {
					t.Errorf("%d events fired, want none", n)
				}
				updateHook = nil
				h.disk.setInject(nil)
				h = h.reopen()
				if got, _ := encode(h.store.cur.Load()); !bytes.Equal(got, file) {
					t.Error("the reopened store differs from the old file")
				}
				if err := run(h); err != nil {
					t.Errorf("the operation after the reopen = %v, want it to land", err)
				}
			})
		}
	}
}
