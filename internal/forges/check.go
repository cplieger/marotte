package forges

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"time"

	"github.com/cplieger/forgeapi"
)

const (
	// checkBudget bounds one check, the id-lock wait and the records write
	// included, so a Check route's answer arrives inside the client action's
	// 30 s timeout.
	checkBudget = 20 * time.Second
	// checkSaveShare holds 1/checkSaveShare of a check's budget (5 s of
	// checkBudget) back from the lock wait and the forge calls, so a forge that
	// never answers still leaves its verdict's one local records write room.
	checkSaveShare = 4
)

var (
	errCheckBusy = errors.New(
		"a check of this account is already running; its result shows here when it ends",
	)
	errReconnectRequired = errors.New(
		"this account needs a new sign-in; a check cannot revive it",
	)
	errSaveTimedOut = errors.New("the save did not finish in time")
)

// check asks the forge who connection id's credential is and installs the
// verdict, the one writer of status after Load. It answers whether the row's
// view signature changed. A caller whose ctx ended before its records write's
// last context check, and a lock wait that ran out of budget, write no verdict;
// a reconnect row is never sent anything.
func (m *Manager) check(ctx context.Context, id string, budget time.Duration) (changed bool, err error) {
	deadline := time.Now().Add(budget)
	wctx, wcancel := context.WithDeadline(ctx, deadline)
	defer wcancel()
	tctx, cancel := context.WithDeadline(wctx, deadline.Add(-budget/checkSaveShare))
	defer cancel()
	unlock, err := m.locks.lock(tctx, id)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, errCheckBusy
	}
	cur, health, _ := m.current(id)
	switch {
	case cur == nil:
		unlock()
		return false, fmt.Errorf("%w: %s", errNotLoggedIn, id)
	case health != "":
		unlock()
		return false, fmt.Errorf("%w: %s", errConnectionUnusable, health)
	case cur.status.kind == statusReconnect:
		unlock()
		return false, errReconnectRequired
	}
	_, err = cur.src.Token(tctx)
	var acct forgeapi.Account
	if err == nil {
		acct, err = cur.conn.Whoami(tctx)
	}
	end := time.Now()
	if ctx.Err() != nil {
		unlock()
		return false, ctx.Err()
	}
	next := *cur
	next.spec.RotationCursor = string(cur.conn.BudgetState().RotationCursor)
	switch {
	case err == nil:
		next.status = status{kind: statusOK, checkedAt: end}
		next.login = cmp.Or(acct.Login, cur.login)
		next.email = cmp.Or(acct.Email, cur.email)
	case tctx.Err() != nil:
		next.status = status{kind: statusRetrying, checkedAt: end, fault: &fault{
			message: "no answer from " + cur.spec.Host + " in time", kind: forgeapi.KindTransient.String(),
		}}
	default:
		next.status = statusOf(err, end)
	}
	installed, ok := m.settle(ctx, wctx, cur, &next)
	if !ok {
		unlock()
		return false, ctx.Err()
	}
	m.afterCheck(cur, installed)
	unlock()
	return m.announce(ctx, cur, installed), nil
}

// settle installs next, through commit on wctx when the records file must
// change. A failed write installs fallback instead, so the live row never claims
// more than the files hold, and answers what it installed. A write that failed
// because ctx ended installs nothing and answers false: a failed atomicfile
// write published nothing, since its rename is the last step that can fail.
func (m *Manager) settle(ctx, wctx context.Context, cur, next *connection) (*connection, bool) {
	if next.persisted() {
		m.install(next.spec.ID, next)
		return next, true
	}
	err := m.commit(wctx, next.spec.ID, next)
	switch {
	case err == nil:
		return next, true
	case ctx.Err() != nil:
		return nil, false
	case wctx.Err() != nil:
		err = errSaveTimedOut
	}
	slog.Warn("forges: a check's verdict could not be saved", "connection", next.spec.ID, "error", err)
	fb := fallback(cur, next, err)
	m.install(fb.spec.ID, fb)
	return fb, true
}

// fallback is the memory-only value a failed verdict write installs: the class
// a restart's Load would derive from the files as they stand, with the moved
// rotation cursor left for the next check to write again. A usable row the
// forge refused reads retrying, so the keeper checks it again and repeats the
// whole verdict, the credential's owed mark included.
func fallback(cur, next *connection, werr error) *connection {
	fb := *next
	fb.spec.RotationCursor = cur.spec.RotationCursor
	switch was := cur.durable.Standing; was {
	case standingOf(&next.status):
	case standingUsable:
		fb.status = status{kind: statusRetrying, checkedAt: next.status.checkedAt, fault: &fault{
			message: fmt.Sprintf("the forge refused this account (%s), but marotte could not save that (%s). "+
				"It is checked again on the next pass", next.status.fault.message, werr),
			code: next.status.fault.code, kind: next.status.fault.kind,
		}}
	default:
		held := decode(was)
		held.checkedAt = next.status.checkedAt
		held.fault.message = unsavedAnswer(&next.status, werr)
		fb.status = held
	}
	return &fb
}

// unsavedAnswer is a held refusal's message when the check's answer s could
// not be saved.
func unsavedAnswer(s *status, werr error) string {
	switch s.kind {
	case statusOK:
		return fmt.Sprintf("the forge accepted this account, but marotte could not save that (%s). Check again", werr)
	case statusRetrying:
		return fmt.Sprintf("the forge did not answer the check (%s), and marotte could not save "+
			"this account's new status (%s). Check again", s.fault.message, werr)
	}
	return fmt.Sprintf("the forge refused this account's credential (%s), but marotte could not save that (%s). "+
		"Check again", s.fault.message, werr)
}

// afterCheck drops the capabilities a status-kind change makes stale.
func (m *Manager) afterCheck(cur, installed *connection) {
	if cur.status.kind != installed.status.kind {
		m.lists.caps.evict(cur.spec.ID)
	}
}

// announce follows a change to cur's row outside its id lock: git's helper
// registration when the row moved into or out of usable, then notify when the
// view signature changed, which it answers.
func (m *Manager) announce(ctx context.Context, cur, installed *connection) bool {
	if cur.status.kind.usable() != installed.status.kind.usable() {
		m.reconcileGit(ctx)
	}
	before, after := viewOf(cur, ""), viewOf(installed, "")
	if reflect.DeepEqual(signature(&before), signature(&after)) {
		return false
	}
	m.notify(ctx, Change{ID: cur.spec.ID})
	return true
}
