package applogin

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/access"
	"github.com/cplieger/access/account"
)

// heldGroup queues calls behind one held group, so they run as the next group together; it returns
// once each call has returned.
func heldGroup(t *testing.T, s *Store, calls ...func()) {
	t.Helper()
	entered, pending, release := s.holdGroup()
	first := make(chan error, 1)
	go func() { first <- s.CreateSession(plainCtx(context.Background()), newTestSession(999, time.Now())) }()
	<-entered
	done := make(chan struct{}, len(calls))
	for i, call := range calls {
		go func() {
			defer func() { done <- struct{}{} }()
			call()
		}()
		waitUntil(t, fmt.Sprintf("call %d queued", i), func() bool { return pending() == i+2 })
	}
	release()
	if err := <-first; err != nil {
		t.Fatalf("the held first write: %v", err)
	}
	for range calls {
		<-done
	}
}

var (
	errA = errors.New("test: job A")
	errB = errors.New("test: job B")
)

func TestWriter_AllErrorGroupPublishesNothing(t *testing.T) {
	s, d := openTestStore(t)
	var gotA, gotB error
	before := d.publishes.Load() + 1 // the held first group's own publish
	heldGroup(t, s,
		func() { gotA = s.Update(t.Context(), func(account.Tx) error { return errA }) },
		func() { gotB = s.Update(t.Context(), func(account.Tx) error { return errB }) },
	)
	if !errors.Is(gotA, errA) || !errors.Is(gotB, errB) {
		t.Errorf("errors = (%v, %v), want each job its own", gotA, gotB)
	}
	if n := d.publishes.Load() - before; n != 0 {
		t.Errorf("an all-error group published %d times", n)
	}
}

func TestWriter_GuardOnlyRefusal(t *testing.T) {
	h := newHarness(t)
	h.setupOwner(t.Context())
	base := h.disk.publishes.Load()
	candidate := h.store.policy()
	candidate.Methods.Password = false
	logBefore, _ := h.store.LogStats(t.Context())
	_, _, err := h.store.putPolicy(t.Context(), h.svc, &accountTestActor, "", candidate, candidate.Generation, nil)
	if !errors.Is(err, ErrNoWorkingMethod) {
		t.Fatalf("an allowance-off save leaving no method = %v, want ErrNoWorkingMethod", err)
	}
	if n := h.disk.publishes.Load() - base; n != 0 {
		t.Errorf("the refused save published %d times", n)
	}
	if !h.store.policy().Methods.Password {
		t.Error("the refused save turned the password allowance off")
	}
	if after, _ := h.store.LogStats(t.Context()); fmt.Sprint(after) != fmt.Sprint(logBefore) {
		t.Errorf("the refused save appended a log entry: %v then %v", logBefore, after)
	}
}

func TestWriter_FailedCallbackBesideAWrite(t *testing.T) {
	s, d := openTestStore(t)
	var gotA, gotB error
	heldGroup(t, s,
		func() {
			gotA = s.Update(t.Context(), func(tx account.Tx) error {
				if err := tx.CreateUser(t.Context(), &access.User{Username: "rolled-back"}); err != nil {
					return err
				}
				return errA
			})
		},
		func() { gotB = s.CreateUser(t.Context(), &access.User{Username: "kept"}) },
	)
	if !errors.Is(gotA, errA) || gotB != nil {
		t.Errorf("results = (%v, %v), want (errA, nil)", gotA, gotB)
	}
	s = reopenStore(t, s, d)
	if _, found, _ := s.UserByUsername(t.Context(), "rolled-back"); found {
		t.Error("the failed callback's write is in the reopened file")
	}
	if _, found, _ := s.UserByUsername(t.Context(), "kept"); !found {
		t.Error("the successful write is not in the reopened file")
	}
}

func TestWriter_PanicReRaisedInItsCaller(t *testing.T) {
	s, _ := openTestStore(t)
	var recovered any
	var gotB error
	heldGroup(t, s,
		func() {
			defer func() { recovered = recover() }()
			_ = s.Update(t.Context(), func(tx account.Tx) error {
				_ = tx.CreateUser(t.Context(), &access.User{Username: "panicked"})
				panic("test: boom")
			})
		},
		func() { gotB = s.CreateUser(t.Context(), &access.User{Username: "beside"}) },
	)
	if recovered != "test: boom" {
		t.Errorf("the Update caller recovered %v, want the callback's value", recovered)
	}
	if gotB != nil {
		t.Errorf("the job beside the panic = %v, want nil", gotB)
	}
	if _, found, _ := s.UserByUsername(t.Context(), "panicked"); found {
		t.Error("the panicked job's write was kept")
	}
	if err := s.CreateUser(t.Context(), &access.User{Username: "next-group"}); err != nil {
		t.Errorf("the writer's next group = %v, want served", err)
	}
}

func TestWriter_PublicationFailureReachesOnlyParticipants(t *testing.T) {
	s, d := openTestStore(t)
	var gotNoop, gotC, gotW error
	entered, pending, release := s.holdGroup()
	first := make(chan error, 1)
	go func() { first <- s.CreateSession(plainCtx(context.Background()), newTestSession(999, time.Now())) }()
	<-entered
	queue := func(n int, call func()) chan struct{} {
		done := make(chan struct{})
		go func() { defer close(done); call() }()
		waitUntil(t, "queued", func() bool { return pending() == n })
		return done
	}
	d1 := queue(2, func() { gotNoop = s.Update(t.Context(), func(account.Tx) error { return nil }) })
	d2 := queue(3, func() { gotC = s.Update(t.Context(), func(account.Tx) error { return errA }) })
	d3 := queue(4, func() { gotW = s.CreateUser(t.Context(), &access.User{Username: "w"}) })
	var stages atomic.Int32
	d.setInject(func(op, path string) error {
		if op == "stage" && path == d.path && stages.Add(1) == 2 {
			return errInjected
		}
		return nil
	})
	release()
	<-first
	<-d1
	<-d2
	<-d3
	if gotNoop != nil {
		t.Errorf("a no-op before the first write = %v, want its own nil", gotNoop)
	}
	if !errors.Is(gotC, errA) {
		t.Errorf("the failing callback = %v, want its own error", gotC)
	}
	if !errors.Is(gotW, errInjected) {
		t.Errorf("the participating write = %v, want the publication error", gotW)
	}
}

func TestWriter_JobCancelledWhileQueuedDoesNotRun(t *testing.T) {
	s, _ := openTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	ran := false
	var got error
	entered, pending, release := s.holdGroup()
	first := make(chan error, 1)
	go func() { first <- s.CreateSession(plainCtx(context.Background()), newTestSession(999, time.Now())) }()
	<-entered
	done := make(chan struct{})
	go func() {
		defer close(done)
		got = s.Update(ctx, func(account.Tx) error { ran = true; return nil })
	}()
	waitUntil(t, "the queued job", func() bool { return pending() == 2 })
	cancel()
	release()
	<-first
	<-done
	if ran || !errors.Is(got, context.Canceled) {
		t.Errorf("a job cancelled while queued ran %t and returned %v, want not run and ctx.Err()", ran, got)
	}
}

func TestCleanup_GroupedWritesJudgedInOrder(t *testing.T) {
	now := epoch.Add(48 * time.Hour)
	timeouts := access.SessionTimeouts{Idle: time.Hour, Absolute: 7 * 24 * time.Hour}
	seed := func(t *testing.T) (*Store, *access.Session) {
		s, _ := openTestStore(t)
		sess := newTestSession(1, now.Add(-2*time.Hour))
		if err := s.CreateSession(plainCtx(t.Context()), sess); err != nil {
			t.Fatal(err)
		}
		return s, sess
	}
	t.Run("activity_first", func(t *testing.T) {
		s, sess := seed(t)
		var refs []access.SessionRef
		heldGroup(t, s,
			func() {
				_ = s.UpdateSessionActivity(t.Context(), sess.TokenHash, access.SessionActivity{At: now, IP: "192.0.2.5"})
			},
			func() { refs, _ = s.CleanupExpiredSessions(t.Context(), now, timeouts, 10) },
		)
		for _, r := range refs {
			if r.ID == sess.ID {
				t.Error("the cleanup named a session an earlier job of its group touched")
			}
		}
		if _, ok := s.sessionRecord(sess.TokenHash); !ok {
			t.Error("the touched session was deleted")
		}
	})
	t.Run("cleanup_first", func(t *testing.T) {
		s, sess := seed(t)
		var refs []access.SessionRef
		var rotErr error
		next := *sess
		_, next.TokenHash = access.GenerateSessionToken()
		heldGroup(t, s,
			func() { refs, _ = s.CleanupExpiredSessions(t.Context(), now, timeouts, 10) },
			func() {
				_ = s.UpdateSessionActivity(t.Context(), sess.TokenHash, access.SessionActivity{At: now, IP: "192.0.2.5"})
			},
			func() {
				rotErr = s.RotateSession(t.Context(), account.Rotation{ID: sess.ID, UserID: sess.UserID, Next: next})
			},
		)
		if len(refs) != 1 || refs[0].ID != sess.ID {
			t.Errorf("cleanup refs %v, want the idle session", refs)
		}
		if _, ok := s.sessionRecord(sess.TokenHash); ok {
			t.Error("the activity write after the cleanup brought the session back")
		}
		if !errors.Is(rotErr, account.ErrNotFound) {
			t.Errorf("a rotation of the deleted session = %v, want ErrNotFound", rotErr)
		}
	})
}

func TestActivity_OlderWriteIsANoop(t *testing.T) {
	t0 := epoch.Add(time.Hour)
	t.Run("session_in_one_group", func(t *testing.T) {
		s, d := openTestStore(t)
		sess := newTestSession(1, epoch)
		if err := s.CreateSession(plainCtx(t.Context()), sess); err != nil {
			t.Fatal(err)
		}
		base := d.publishes.Load() + 1
		heldGroup(t, s,
			func() {
				_ = s.UpdateSessionActivity(t.Context(), sess.TokenHash, access.SessionActivity{At: t0.Add(30 * time.Minute), IP: "192.0.2.2"})
			},
			func() {
				_ = s.UpdateSessionActivity(t.Context(), sess.TokenHash, access.SessionActivity{At: t0, IP: "192.0.2.1"})
			},
		)
		rec, _ := s.sessionRecord(sess.TokenHash)
		if !rec.LastActivity.Equal(t0.Add(30*time.Minute)) || rec.LastIP != "192.0.2.2" {
			t.Errorf("record (%s, %s), want the newer write's (t+30m, B)", rec.LastActivity, rec.LastIP)
		}
		if n := d.publishes.Load() - base; n != 1 {
			t.Errorf("the group published %d times, want 1", n)
		}
	})
	t.Run("older_alone_publishes_nothing", func(t *testing.T) {
		s, d := openTestStore(t)
		sess := newTestSession(1, t0)
		if err := s.CreateSession(plainCtx(t.Context()), sess); err != nil {
			t.Fatal(err)
		}
		base := d.publishes.Load()
		if err := s.UpdateSessionActivity(t.Context(), sess.TokenHash, access.SessionActivity{At: epoch, IP: "192.0.2.1"}); err != nil {
			t.Errorf("an older activity write = %v, want nil", err)
		}
		if n := d.publishes.Load() - base; n != 0 {
			t.Errorf("an older activity write published %d times, want 0", n)
		}
		k := mustCreateKey(t, s, 1)
		if err := s.TouchAPIKey(t.Context(), k.ID, t0); err != nil {
			t.Fatal(err)
		}
		base = d.publishes.Load()
		if err := s.TouchAPIKey(t.Context(), k.ID, epoch); err != nil {
			t.Errorf("an older touch = %v, want nil", err)
		}
		if n := d.publishes.Load() - base; n != 0 {
			t.Errorf("older writes published %d times, want 0", n)
		}
		rec, _ := s.keyRecord(k.ID)
		if !rec.LastUsedAt.Equal(t0) {
			t.Errorf("key LastUsedAt %s, want the newer %s", rec.LastUsedAt, t0)
		}
	})
	t.Run("rotation_keeps_newer_stored_activity", func(t *testing.T) {
		s, _ := openTestStore(t)
		sess := newTestSession(1, epoch)
		if err := s.CreateSession(plainCtx(t.Context()), sess); err != nil {
			t.Fatal(err)
		}
		if err := s.UpdateSessionActivity(t.Context(), sess.TokenHash, access.SessionActivity{At: t0, IP: "192.0.2.2"}); err != nil {
			t.Fatal(err)
		}
		next := *sess
		_, next.TokenHash = access.GenerateSessionToken()
		next.LastActivity, next.LastIP = epoch, "192.0.2.1"
		if err := s.RotateSession(t.Context(), account.Rotation{ID: sess.ID, UserID: sess.UserID, Next: next}); err != nil {
			t.Fatal(err)
		}
		rec, _ := s.sessionRecord(next.TokenHash)
		if !rec.LastActivity.Equal(t0) || rec.LastIP != "192.0.2.2" {
			t.Errorf("rotated record (%s, %s), want the stored newer (t0, B)", rec.LastActivity, rec.LastIP)
		}
	})
}

func TestPublished_NamesTheTwoPublishedResults(t *testing.T) {
	for _, c := range []struct {
		err  error
		name string
		want bool
	}{
		{nil, "nil", true},
		{fmt.Errorf("%w: disk", ErrNotDurable), "not_durable", true},
		{ErrFenced, "fenced", false},
		{ErrNoWorkingMethod, "no_working_method", false},
		{errInjected, "before_publish", false},
	} {
		if got := Published(c.err); got != c.want {
			t.Errorf("Published(%s) = %t, want %t", c.name, got, c.want)
		}
	}
}

func TestHold_PendingCountsEveryJob(t *testing.T) {
	s, d := openTestStore(t)
	pending, release := d.faults().HoldPublish()
	defer release()
	done := make(chan error, 2)
	go func() { done <- s.CreateSession(plainCtx(context.Background()), newTestSession(1, epoch)) }()
	waitUntil(t, "pending 1 for a lone held CreateSession", func() bool { return pending() == 1 })
	go func() { done <- s.Update(context.Background(), func(account.Tx) error { return nil }) }()
	waitUntil(t, "pending 2 once an Update queues behind it", func() bool { return pending() == 2 })
	release()
	<-done
	<-done
}
