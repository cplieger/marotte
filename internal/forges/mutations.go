package forges

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
)

const (
	// mutationDeadline ends a sign-in's, a sign-out's or an owners edit's
	// forge-facing and context-aware work before the browser stops waiting:
	// every forge action and the device poll go through @cplieger/fetch, whose
	// API_TIMEOUT_MS is 30 s from the request's start. The 5 s margin covers the
	// request's own transit.
	mutationDeadline = 25 * time.Second
	// durableReserve is held back from the forge-facing half for the durable
	// half: one credential save, one records write, a slot delete, the cleartext
	// scrub and a few git config calls, all local.
	durableReserve = 5 * time.Second
)

// mutationDeadlines is the deadline pair a mutation takes at its route's entry;
// a test shortens it.
type mutationDeadlines struct{ total, reserve time.Duration }

func (d mutationDeadlines) from(start time.Time) time.Time { return start.Add(d.total) }

var (
	errConnectionBusy = errors.New("another change to this account is still running. Try again")
	errSignInTimedOut = errors.New("the sign-in ran out of time before it was saved; nothing changed. Try again")
	errCleanupPending = errors.New("signed out, but a stored credential could not be deleted")
)

const (
	codeConnectionBusy = "connection_busy"
	codeSignInTimedOut = "sign_in_timed_out"
	codeCleanupPending = "sign_out_cleanup_pending"
)

// windows is a mutation's two contexts: fctx for its forge-facing half, ending
// durableReserve before deadline, and dctx for its durable half, which the
// request's end cannot cancel.
func (m *Manager) windows(base context.Context, deadline time.Time) (fctx, dctx context.Context, cancel func()) {
	f, fcancel := context.WithDeadline(base, deadline.Add(-m.deadlines.reserve))
	d, dcancel := context.WithDeadline(context.WithoutCancel(base), deadline)
	return f, d, func() { fcancel(); dcancel() }
}

// begin takes id's lock on fctx and answers the row and store it holds: a
// deadline is errConnectionBusy, a base that ended its own error, and unusable
// persistence errConnectionUnusable with the lock released.
func (m *Manager) begin(base, fctx context.Context, id string) (cur *connection, store creds.Store, unlock func(), err error) {
	unlock, err = m.locks.Lock(fctx, id)
	if err != nil {
		if base.Err() != nil {
			return nil, nil, nil, base.Err()
		}
		return nil, nil, nil, errConnectionBusy
	}
	cur, health, store := m.current(id)
	if health != "" {
		unlock()
		return nil, nil, nil, fmt.Errorf("%w: %s", errConnectionUnusable, health)
	}
	return cur, store, unlock, nil
}

// retire ends old's account as connection id's once the records write has
// replaced it: its client, its cached answers, the notify, and host's
// cleartext git-credentials line.
func (m *Manager) retire(ctx context.Context, id, host string, old *connection) {
	if old != nil && old.conn != nil {
		old.conn.Close()
	}
	m.lists.drop(id)
	m.notify(ctx, Change{ID: id, AccountChanged: true})
	scrubCleartext(ctx, host)
}

// signIn verifies cred against the forge and makes it spec's connection: saved
// to the slot the current row does not use, then the records write that
// switches git and every reader to it. base is the context only a browser that
// loses nothing may cancel, and deadline the route's. A failure before the
// records write leaves the old account whole.
func (m *Manager) signIn(base context.Context, deadline time.Time, spec *connectionSpec, signed *creds.Record) (string, error) {
	fctx, dctx, cancel := m.windows(base, deadline)
	defer cancel()
	id := spec.ID
	// A token is never sent for a sign-in that cannot be saved.
	if _, health, _ := m.current(id); health != "" {
		return "", fmt.Errorf("%w: %s", errConnectionUnusable, health)
	}
	acct, err := verify(fctx, m, spec, signed.Token)
	if err != nil {
		return "", m.forgeFacing(base, fctx, err)
	}
	cred := *signed
	cred.Family, cred.WebBaseURL, cred.Account = spec.Kind.family(), spec.webBase(), acct.Login
	if len(cred.Scopes) == 0 {
		cred.Scopes = acct.Scopes
	}
	cur, store, unlock, err := m.begin(base, fctx, id)
	if err != nil {
		return "", err
	}
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()
	next := &connection{spec: *spec, login: acct.Login, email: acct.Email}
	if cur != nil {
		next.spec.Slot = 1 - cur.spec.Slot
		next.spec.RotationCursor = cur.spec.RotationCursor
		next.spec.OwnerScopes, _ = ownersKept(spec.Kind, acct.Login, cur.spec.OwnerScopes)
	}
	next.spec.SignIn = rand.Text()
	key := slotKey(id, next.spec.Slot)
	if dctx.Err() != nil {
		return "", errSignInTimedOut
	}
	if serr := store.Save(key, cred); serr != nil {
		return "", fmt.Errorf("forges: save the credential for %s: %w", id, serr)
	}
	core, src, st, err := openConn(store, &next.spec, m.newCore, m.extraOpts)
	if err != nil {
		deleteKey(store, key)
		return "", err
	}
	next.conn, next.src, next.cred = core, src, st
	if cerr := m.commitBy(dctx, id, next); cerr != nil {
		deleteKey(store, key)
		core.Close()
		return "", cerr
	}
	m.retire(context.WithoutCancel(base), id, spec.Host, cur)
	deleteKey(store, slotKey(id, 1-next.spec.Slot))
	locked = false
	unlock()
	m.reconcileGit(dctx)
	return id, nil
}

// forgeFacing is a forge-facing step's error as a mutation reports it: a base
// that ended its own error, a deadline errSignInTimedOut.
func (*Manager) forgeFacing(base, fctx context.Context, err error) error {
	switch {
	case base.Err() != nil:
		return base.Err()
	case fctx.Err() != nil:
		return errSignInTimedOut
	}
	return err
}

// commitBy is commit for a mutation's durable half: a deadline spent before or
// during the write stores nothing and is errSignInTimedOut.
func (m *Manager) commitBy(dctx context.Context, id string, next *connection) error {
	if dctx.Err() != nil {
		return errSignInTimedOut
	}
	err := m.commit(dctx, id, next)
	if err != nil && dctx.Err() != nil {
		return errSignInTimedOut
	}
	return err
}

func deleteKey(store creds.Store, key string) {
	if err := store.Delete(key); err != nil {
		slog.Warn("forges: a credential slot could not be removed; the next verified load removes it", "key", key, "error", err)
	}
}

// signOut removes connection id: the records write first, so git's helper stops
// answering it, then both credential slots, whether or not a row existed. A
// failed delete answers errCleanupPending, which a repeat finishes.
func (m *Manager) signOut(base context.Context, deadline time.Time, id string) error {
	fctx, dctx, cancel := m.windows(base, deadline)
	defer cancel()
	cur, store, unlock, err := m.begin(base, fctx, id)
	if err != nil {
		return err
	}
	// Only a row is retired: a crafted id with no row must not remove a
	// person's own git-credentials line.
	if cur != nil {
		if err := m.commit(dctx, id, nil); err != nil {
			unlock()
			return err
		}
		m.retire(context.WithoutCancel(base), id, cur.spec.Host, cur)
	}
	cleanup := errors.Join(store.Delete(slotKey(id, 0)), store.Delete(slotKey(id, 1)))
	unlock()
	m.reconcileGit(dctx)
	if cleanup != nil {
		return fmt.Errorf("%w: %w", errCleanupPending, cleanup)
	}
	return nil
}

// setOwners replaces connection id's owner scopes with owners as ownerScopes
// stores them, and answers what it stored.
func (m *Manager) setOwners(base context.Context, deadline time.Time, id string, owners []string) ([]string, error) {
	fctx, dctx, cancel := m.windows(base, deadline)
	defer cancel()
	cur, _, unlock, err := m.begin(base, fctx, id)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		unlock()
		return nil, fmt.Errorf("%w: %s", errNotLoggedIn, id)
	}
	scopes, err := ownerScopes(cur.spec.Kind, cur.login, owners)
	if err != nil {
		unlock()
		return nil, err
	}
	next := *cur
	next.spec.OwnerScopes = scopes
	if next.persisted() {
		m.install(id, &next)
	} else if err := m.commit(dctx, id, &next); err != nil {
		unlock()
		return nil, err
	}
	unlock()
	m.announce(context.WithoutCancel(base), cur, &next)
	return scopes, nil
}

// verify reads the account token signs in as through a throwaway client that
// holds it in memory, so nothing is stored for a token the forge refuses.
func verify(ctx context.Context, m *Manager, spec *connectionSpec, token string) (forgeapi.Account, error) {
	core, err := verifier(m, spec, token)
	if err != nil {
		return forgeapi.Account{}, err
	}
	defer core.Close()
	return core.Whoami(ctx)
}
