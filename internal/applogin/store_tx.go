package applogin

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/cplieger/access"
	"github.com/cplieger/access/account"
)

var (
	errHashTaken        = errors.New("applogin: hash already stored")
	errSessionIDTaken   = errors.New("applogin: session ID already stored")
	errHandleTaken      = errors.New("applogin: WebAuthn handle already stored")
	errNoHash           = errors.New("applogin: record has no hash")
	errNoSessionID      = errors.New("applogin: session has no ID")
	errNoCredentialID   = errors.New("applogin: passkey has no credential ID")
	errRotationIdentity = errors.New("applogin: rotation changes the session's ID or user")
	errLogClass         = errors.New("applogin: log entry of no LogClasses class")
	errLogLimit         = errors.New("applogin: log limit below 1")
	errLogCriterion     = errors.New("applogin: log prune needs exactly one of a positive Keep and a non-zero Before")
)

// txView is account.Tx over one snapshot: the published one for the store's reads, the writer's
// private copy inside a job. A write on the private copy never changes a map, slice or record a
// published snapshot shares: the first write of a group to a shared container clones it (own),
// and every write records its inverse in undo, which rolls back one job.
type txView struct {
	s    *snapshot
	st   *Store
	own  *ownership
	undo []undoEntry
	// creds lists the credentials this job wrote, for the restore marker.
	creds []credWrite
	// guardDue is set by an own write that can shrink the owner's working methods.
	guardDue bool
	// rewrite marks the fence's confirm job, which writes the file with no field changed.
	rewrite bool
}

type undoEntry struct {
	fn func()
	// mem marks a write the file does not hold (attachProvider, a parsed file adopted at open).
	mem bool
}

type credWrite struct {
	method access.Method
	userID int64
}

// ownership records which shared containers the group's private copy has cloned.
type ownership struct {
	logClass                                             map[account.LogClass]bool
	sessions, sessByID, passkeys, credIDs, keys, keyByID bool
	tokens, failures, attributions, logMap, logEvicted   bool
}

var _ account.Tx = (*txView)(nil)

// txTrace, tests only, sees the failure count's three calls.
var txTrace func(op string)

func trace(op string) {
	if txTrace != nil {
		txTrace(op)
	}
}

func (t *txView) record(fn func())    { t.undo = append(t.undo, undoEntry{fn: fn}) }
func (t *txView) recordMem(fn func()) { t.undo = append(t.undo, undoEntry{fn: fn, mem: true}) }

// rollback undoes the current job's writes, newest first.
func (t *txView) rollback() {
	for _, u := range slices.Backward(t.undo) {
		u.fn()
	}
	t.undo, t.creds, t.guardDue, t.rewrite = t.undo[:0], nil, false, false
}

func (t *txView) writesFile() bool {
	if t.rewrite {
		return true
	}
	for _, u := range t.undo {
		if !u.mem {
			return true
		}
	}
	return false
}

func cowMap[K comparable, V any](t *txView, m *map[K]V, owned *bool) { //nolint:gocritic // ptrToRefParam: the clone replaces the caller's map
	if *owned {
		return
	}
	old := *m
	*m = maps.Clone(old)
	if *m == nil {
		*m = map[K]V{}
	}
	*owned = true
	t.record(func() { *m, *owned = old, false })
}

func mapSet[K comparable, V any](t *txView, m *map[K]V, owned *bool, k K, v V) { //nolint:gocritic // ptrToRefParam: cowMap may replace the map
	cowMap(t, m, owned)
	old, had := (*m)[k]
	(*m)[k] = v
	t.record(func() {
		if had {
			(*m)[k] = old
		} else {
			delete(*m, k)
		}
	})
}

func mapDel[K comparable, V any](t *txView, m *map[K]V, owned *bool, k K) { //nolint:gocritic // ptrToRefParam: cowMap may replace the map
	old, had := (*m)[k]
	if !had {
		return
	}
	cowMap(t, m, owned)
	delete(*m, k)
	t.record(func() { (*m)[k] = old })
}

// setSlice replaces *p; v must not share a backing array a published snapshot can read past.
func setSlice[T any](t *txView, p *[]T, owned *bool, v []T) {
	old, oldOwned := *p, *owned
	*p, *owned = v, true
	t.record(func() { *p, *owned = old, oldOwned })
}

// appendSlice appends in place to an owned slice; a shared one is cloned first, so the append
// never writes into an array a published snapshot holds.
func appendSlice[T any](t *txView, p *[]T, owned *bool, v T) {
	base := *p
	if !*owned {
		base = slices.Grow(slices.Clone(base), 1)
	}
	setSlice(t, p, owned, append(base, v))
}

func (t *txView) setUsers(users []storeUser) {
	s := t.s
	old, name, handle, oidcIx := s.users, s.byName, s.byHandle, s.byOIDC
	s.users = users
	s.reindexUsers()
	t.record(func() { s.users, s.byName, s.byHandle, s.byOIDC = old, name, handle, oidcIx })
}

func (t *txView) setNextIDs(n nextIDs) {
	old := t.s.nextIDs
	t.s.nextIDs = n
	t.record(func() { t.s.nextIDs = old })
}

func (t *txView) nextID(field func(*nextIDs) *int64) (int64, error) {
	n := t.s.nextIDs
	id, err := takeID(field(&n))
	if err != nil {
		return 0, err
	}
	t.setNextIDs(n)
	return id, nil
}

func (t *txView) setPolicy(p *storePolicy) {
	old := t.s.policy
	t.s.policy = *p
	t.record(func() { t.s.policy = old })
}

func (t *txView) wroteCred(m access.Method, userID int64) {
	t.creds = append(t.creds, credWrite{method: m, userID: userID})
}

// --- Users ---

func checkUserClaims(s *snapshot, u *storeUser) error {
	if i, ok := s.byName[u.Username]; ok && s.users[i].ID != u.ID {
		return account.ErrUsernameTaken
	}
	if !u.OIDC.IsZero() {
		if i, ok := s.byOIDC[u.OIDC]; ok && s.users[i].ID != u.ID {
			return account.ErrOIDCIdentityTaken
		}
	}
	if len(u.WebAuthnHandle) > 0 {
		if i, ok := s.byHandle[string(u.WebAuthnHandle)]; ok && s.users[i].ID != u.ID {
			return errHandleTaken
		}
	}
	return nil
}

func (t *txView) CreateUser(_ context.Context, user *access.User) error {
	r := userFromAuth(user)
	r.ID = 0
	if err := checkUserClaims(t.s, &r); err != nil {
		return err
	}
	id, err := t.nextID(func(n *nextIDs) *int64 { return &n.User })
	if err != nil {
		return err
	}
	r.ID = id
	t.setUsers(append(slices.Clone(t.s.users), r))
	if !r.PasswordHash.IsZero() {
		t.wroteCred(access.MethodPassword, id)
	}
	user.ID = id
	return nil
}

func (t *txView) CreateInitialUser(ctx context.Context, user *access.User) error {
	if len(t.s.users) > 0 {
		return account.ErrSetupComplete
	}
	return t.CreateUser(ctx, user)
}

// userAt copies the user at index i, or a zero record when ok is false, so a found and an
// unknown name cost the same work.
func (t *txView) userAt(i int, ok bool) (*access.User, bool, error) {
	var r storeUser
	if ok {
		r = t.s.users[i]
	}
	u := r.toAuth()
	if !ok {
		return nil, false, nil
	}
	return u, true, nil
}

func (t *txView) UserByID(_ context.Context, id int64) (*access.User, bool, error) {
	i, ok := t.s.userIndex(id)
	return t.userAt(i, ok)
}

func (t *txView) UserByUsername(_ context.Context, canonical string) (*access.User, bool, error) {
	trace("UserByUsername")
	i, ok := t.s.byName[canonical]
	return t.userAt(i, ok)
}

func (t *txView) UserByOIDC(_ context.Context, ident access.OIDCIdentity) (*access.User, bool, error) {
	i, ok := t.s.byOIDC[ident]
	return t.userAt(i, ok && !ident.IsZero())
}

func (t *txView) UserByWebAuthnHandle(_ context.Context, handle []byte) (*access.User, bool, error) {
	i, ok := t.s.byHandle[string(handle)]
	return t.userAt(i, ok && len(handle) > 0)
}

func (t *txView) ListUsers(context.Context) ([]access.User, error) {
	out := make([]access.User, 0, len(t.s.users))
	for i := range t.s.users {
		out = append(out, *t.s.users[i].toAuth())
	}
	return out, nil
}

func (t *txView) UpdateUser(_ context.Context, user *access.User) error {
	i, ok := t.s.userIndex(user.ID)
	if !ok {
		return account.ErrNotFound
	}
	r := userFromAuth(user)
	if err := checkUserClaims(t.s, &r); err != nil {
		return err
	}
	users := slices.Clone(t.s.users)
	users[i] = r
	t.setUsers(users)
	if !r.PasswordHash.IsZero() {
		t.wroteCred(access.MethodPassword, r.ID)
	}
	return nil
}

func (t *txView) DeleteUser(_ context.Context, id int64) error {
	i, ok := t.s.userIndex(id)
	if !ok {
		return account.ErrNotFound
	}
	t.setUsers(slices.Delete(slices.Clone(t.s.users), i, i+1))
	t.deleteUserSessions(id, "")
	t.deleteUserPasskeys(id)
	t.deleteUserKeys(id)
	t.deleteTokens(func(r *storeToken) bool { return r.Subject == id })
	if slices.ContainsFunc(t.s.attributions, func(a storeAttribution) bool { return a.UserID == id }) {
		kept := slices.DeleteFunc(slices.Clone(t.s.attributions), func(a storeAttribution) bool { return a.UserID == id })
		setSlice(t, &t.s.attributions, &t.own.attributions, kept)
	}
	return nil
}

func (t *txView) UserCount(context.Context) (int, error) { return len(t.s.users), nil }

// --- Sessions ---

func (t *txView) putSession(r *storeSession) {
	k := r.TokenHash.Key()
	mapSet(t, &t.s.sessions, &t.own.sessions, k, r)
	mapSet(t, &t.s.sessByID, &t.own.sessByID, r.ID, k)
}

func (t *txView) deleteSession(k access.HashKey) {
	r, ok := t.s.sessions[k]
	if !ok {
		return
	}
	mapDel(t, &t.s.sessByID, &t.own.sessByID, r.ID)
	mapDel(t, &t.s.sessions, &t.own.sessions, k)
}

// CreateSession stores the transport the gate decided; a context without one is stored as plain
// HTTP, the stricter tag.
func (t *txView) CreateSession(ctx context.Context, sess *access.Session) error {
	switch {
	case sess.TokenHash.IsZero():
		return errNoHash
	case sess.ID == "":
		return errNoSessionID
	}
	k := sess.TokenHash.Key()
	if _, ok := t.s.sessions[k]; ok {
		return errHashTaken
	}
	if _, ok := t.s.sessByID[sess.ID]; ok {
		return errSessionIDTaken
	}
	plain, ok := transportOf(ctx)
	if !ok {
		slog.Error("access: a session was created without a transport; stored as plain HTTP")
		plain = true
	}
	r := sessionFromAuth(sess, plain)
	t.putSession(&r)
	return nil
}

func (t *txView) SessionByHash(_ context.Context, tokenHash access.Hash) (*access.Session, bool, error) {
	r, ok := t.s.sessions[tokenHash.Key()]
	if !ok {
		return nil, false, nil
	}
	return r.toAuth(), true, nil
}

func (t *txView) SessionsByUserID(_ context.Context, userID int64) ([]access.Session, error) {
	out := []access.Session{}
	for _, r := range t.s.sessions {
		if r.UserID == userID {
			out = append(out, *r.toAuth())
		}
	}
	slices.SortFunc(out, func(a, b access.Session) int { return cmp.Compare(a.ID, b.ID) })
	return out, nil
}

// UpdateSessionActivity compares and writes on the copy at the job's turn, so no other write
// falls between the two (rule 21).
func (t *txView) UpdateSessionActivity(_ context.Context, tokenHash access.Hash, act access.SessionActivity) error {
	r, ok := t.s.sessions[tokenHash.Key()]
	if !ok || !act.At.After(r.LastActivity) {
		return nil
	}
	next := *r
	next.LastActivity, next.LastIP = act.At, act.IP
	t.putSession(&next)
	return nil
}

func (t *txView) owned(ref access.SessionRef) (access.HashKey, bool) {
	k, ok := t.s.sessByID[ref.ID]
	if !ok || ref.UserID == 0 || t.s.sessions[k].UserID != ref.UserID {
		return access.HashKey{}, false
	}
	return k, true
}

// RotateSession keeps the stored transport tag and, under rule 21, a newer stored activity.
func (t *txView) RotateSession(_ context.Context, rot account.Rotation) error { //nolint:gocritic // hugeParam: account.SessionStore's signature
	k, ok := t.owned(access.SessionRef{ID: rot.ID, UserID: rot.UserID})
	if !ok {
		return account.ErrNotFound
	}
	cur := t.s.sessions[k]
	if !rot.ExpectHash.IsZero() && !cur.TokenHash.Equal(rot.ExpectHash) {
		return account.ErrNotFound
	}
	switch {
	case rot.Next.ID != rot.ID || rot.Next.UserID != rot.UserID:
		return errRotationIdentity
	case rot.Next.TokenHash.IsZero():
		return errNoHash
	}
	nk := rot.Next.TokenHash.Key()
	if _, taken := t.s.sessions[nk]; taken && nk != k {
		return errHashTaken
	}
	next := sessionFromAuth(&rot.Next, cur.Plain)
	if cur.LastActivity.After(next.LastActivity) {
		next.LastActivity, next.LastIP = cur.LastActivity, cur.LastIP
	}
	t.deleteSession(k)
	t.putSession(&next)
	return nil
}

func (t *txView) UpdateSessionLabel(_ context.Context, ref access.SessionRef, label string) error {
	k, ok := t.owned(ref)
	if !ok {
		return account.ErrNotFound
	}
	next := *t.s.sessions[k]
	next.Label = label
	t.putSession(&next)
	return nil
}

func (t *txView) DeleteSession(_ context.Context, ref access.SessionRef) error {
	k, ok := t.owned(ref)
	if !ok {
		return account.ErrNotFound
	}
	t.deleteSession(k)
	return nil
}

func (t *txView) DeleteSessionByHash(_ context.Context, tokenHash access.Hash) error {
	t.deleteSession(tokenHash.Key())
	return nil
}

func (t *txView) deleteUserSessions(userID int64, exceptID string) []string {
	ids := []string{}
	for k, r := range t.s.sessions {
		if r.UserID == userID && r.ID != exceptID {
			ids = append(ids, r.ID)
			t.deleteSession(k)
		}
	}
	slices.Sort(ids)
	return ids
}

func (t *txView) DeleteUserSessions(_ context.Context, userID int64, exceptID string) ([]string, error) {
	return t.deleteUserSessions(userID, exceptID), nil
}

// cleanup deletes, in (UserID, ID) order, at most limit of the sessions ValidateSession refuses
// at now, and returns their refs in a slice made by this call (rule 14).
func (t *txView) cleanup(now time.Time, timeouts access.SessionTimeouts, limit int) []access.SessionRef {
	var expired []*storeSession
	for _, r := range t.s.sessions {
		if access.ValidateSession(r.toAuth(), timeouts, now) != nil {
			expired = append(expired, r)
		}
	}
	slices.SortFunc(expired, cmpSessionOrder)
	refs := make([]access.SessionRef, 0, min(limit, len(expired)))
	for _, r := range expired[:min(limit, len(expired))] {
		refs = append(refs, r.ref())
		t.deleteSession(r.TokenHash.Key())
	}
	return refs
}

// deletePlainSessions removes every Plain session and returns the refs it removed.
func (t *txView) deletePlainSessions() []access.SessionRef {
	var gone []*storeSession
	for _, r := range t.s.sessions {
		if r.Plain {
			gone = append(gone, r)
		}
	}
	slices.SortFunc(gone, cmpSessionOrder)
	refs := make([]access.SessionRef, 0, len(gone))
	for _, r := range gone {
		refs = append(refs, r.ref())
		t.deleteSession(r.TokenHash.Key())
	}
	return refs
}

// --- Passkeys ---

func (t *txView) setUserPasskeys(userID int64, ps []storePasskey) {
	if len(ps) == 0 {
		mapDel(t, &t.s.passkeys, &t.own.passkeys, userID)
		return
	}
	mapSet(t, &t.s.passkeys, &t.own.passkeys, userID, ps)
}

func (t *txView) CreatePasskey(_ context.Context, cred *access.PasskeyCredential) error {
	if len(cred.CredentialID) == 0 {
		return errNoCredentialID
	}
	if _, ok := t.s.credIDs[string(cred.CredentialID)]; ok {
		return account.ErrDuplicateCredential
	}
	id, err := t.nextID(func(n *nextIDs) *int64 { return &n.Passkey })
	if err != nil {
		return err
	}
	r := passkeyFromAuth(cred)
	r.ID = id
	t.setUserPasskeys(r.UserID, append(slices.Clone(t.s.passkeys[r.UserID]), r))
	mapSet(t, &t.s.credIDs, &t.own.credIDs, string(r.CredentialID), id)
	t.wroteCred(access.MethodPasskey, r.UserID)
	cred.ID = id
	return nil
}

func (t *txView) PasskeysByUserID(_ context.Context, userID int64) ([]access.PasskeyCredential, error) {
	out := make([]access.PasskeyCredential, 0, len(t.s.passkeys[userID]))
	for i := range t.s.passkeys[userID] {
		out = append(out, *t.s.passkeys[userID][i].toAuth())
	}
	return out, nil
}

// passkeyAt finds userID's passkey id; i is its index in the user's slice.
func (t *txView) passkeyAt(userID, id int64) (int, bool) {
	if userID == 0 {
		return 0, false
	}
	i := slices.IndexFunc(t.s.passkeys[userID], func(p storePasskey) bool { return p.ID == id })
	return i, i >= 0
}

func (t *txView) replacePasskey(userID int64, i int, p *storePasskey) {
	ps := slices.Clone(t.s.passkeys[userID])
	ps[i] = *p
	t.setUserPasskeys(userID, ps)
}

func (t *txView) UpdatePasskeyAfterLogin(_ context.Context, userID int64, credID []byte, upd access.PasskeyLoginUpdate) error {
	id, ok := t.s.credIDs[string(credID)]
	if !ok {
		return account.ErrNotFound
	}
	i, ok := t.passkeyAt(userID, id)
	if !ok {
		return account.ErrNotFound
	}
	p := t.s.passkeys[userID][i]
	p.LastUsedAt, p.SignCount, p.RawFlags, p.CloneWarning = upd.At, upd.SignCount, upd.RawFlags, upd.CloneWarning
	p.UserPresent, p.UserVerified, p.BackupEligible, p.BackupState = access.DecodePasskeyFlags(upd.RawFlags)
	t.replacePasskey(userID, i, &p)
	return nil
}

func (t *txView) RenamePasskey(_ context.Context, ref access.PasskeyRef, name string) error {
	i, ok := t.passkeyAt(ref.UserID, ref.ID)
	if !ok {
		return account.ErrNotFound
	}
	p := t.s.passkeys[ref.UserID][i]
	p.Name = name
	t.replacePasskey(ref.UserID, i, &p)
	return nil
}

func (t *txView) DeletePasskey(_ context.Context, ref access.PasskeyRef) error {
	i, ok := t.passkeyAt(ref.UserID, ref.ID)
	if !ok {
		return account.ErrNotFound
	}
	p := t.s.passkeys[ref.UserID][i]
	mapDel(t, &t.s.credIDs, &t.own.credIDs, string(p.CredentialID))
	t.setUserPasskeys(ref.UserID, slices.Delete(slices.Clone(t.s.passkeys[ref.UserID]), i, i+1))
	return nil
}

func (t *txView) deleteUserPasskeys(userID int64) []int64 {
	ids := make([]int64, 0, len(t.s.passkeys[userID]))
	for i := range t.s.passkeys[userID] {
		p := &t.s.passkeys[userID][i]
		ids = append(ids, p.ID)
		mapDel(t, &t.s.credIDs, &t.own.credIDs, string(p.CredentialID))
	}
	t.setUserPasskeys(userID, nil)
	return ids
}

func (t *txView) DeleteUserPasskeys(_ context.Context, userID int64) ([]int64, error) {
	return t.deleteUserPasskeys(userID), nil
}

// --- API keys ---

func (t *txView) putKey(r *storeKey) {
	k := r.KeyHash.Key()
	mapSet(t, &t.s.keys, &t.own.keys, k, r)
	mapSet(t, &t.s.keyByID, &t.own.keyByID, r.ID, k)
}

func (t *txView) deleteKey(id int64) {
	k, ok := t.s.keyByID[id]
	if !ok {
		return
	}
	mapDel(t, &t.s.keys, &t.own.keys, k)
	mapDel(t, &t.s.keyByID, &t.own.keyByID, id)
}

// CreateAPIKey appends the ID it assigns to the lineage it was given, as stored, with no default
// for an empty one (rule 17).
func (t *txView) CreateAPIKey(_ context.Context, key *access.Key) error {
	if key.KeyHash.IsZero() {
		return errNoHash
	}
	if _, ok := t.s.keys[key.KeyHash.Key()]; ok {
		return errHashTaken
	}
	id, err := t.nextID(func(n *nextIDs) *int64 { return &n.Key })
	if err != nil {
		return err
	}
	r := keyFromAuth(key)
	r.ID = id
	r.LineageIDs = append(r.LineageIDs, id)
	t.putKey(&r)
	key.ID, key.LineageIDs = id, slices.Clone(r.LineageIDs)
	return nil
}

func (t *txView) APIKeyByHash(_ context.Context, hash access.Hash) (*access.Key, bool, error) {
	r, ok := t.s.keys[hash.Key()]
	if !ok {
		return nil, false, nil
	}
	return r.toAuth(), true, nil
}

func (t *txView) listKeys(match func(*storeKey) bool) []access.Key {
	out := []access.Key{}
	for _, id := range sortedKeys(t.s.keyByID) {
		if r := t.s.keys[t.s.keyByID[id]]; match(r) {
			out = append(out, *r.toAuth())
		}
	}
	return out
}

func (t *txView) ListAPIKeysByUserID(_ context.Context, userID int64) ([]access.Key, error) {
	return t.listKeys(func(r *storeKey) bool { return r.UserID == userID }), nil
}

func (t *txView) ListAPIKeys(context.Context) ([]access.Key, error) {
	return t.listKeys(func(*storeKey) bool { return true }), nil
}

func (t *txView) DeleteAPIKey(_ context.Context, ref access.KeyRef) error {
	k, ok := t.s.keyByID[ref.ID]
	if !ok || ref.UserID == 0 || t.s.keys[k].UserID != ref.UserID {
		return account.ErrNotFound
	}
	t.deleteKey(ref.ID)
	return nil
}

func (t *txView) deleteUserKeys(userID int64) []int64 {
	ids := []int64{}
	for _, id := range sortedKeys(t.s.keyByID) {
		if t.s.keys[t.s.keyByID[id]].UserID == userID {
			ids = append(ids, id)
			t.deleteKey(id)
		}
	}
	return ids
}

func (t *txView) DeleteUserAPIKeys(_ context.Context, userID int64) ([]int64, error) {
	return t.deleteUserKeys(userID), nil
}

// TouchAPIKey compares and writes in one writer step (rule 21).
func (t *txView) TouchAPIKey(_ context.Context, keyID int64, at time.Time) error {
	k, ok := t.s.keyByID[keyID]
	if !ok || !at.After(t.s.keys[k].LastUsedAt) {
		return nil
	}
	next := *t.s.keys[k]
	next.LastUsedAt = at
	t.putKey(&next)
	return nil
}

// --- One-time tokens ---

func (t *txView) CreateOneTimeToken(_ context.Context, tok access.OneTimeToken) error { //nolint:gocritic // hugeParam: account.TokenStore's signature
	if tok.Hash.IsZero() {
		return errNoHash
	}
	k := tok.Hash.Key()
	if _, ok := t.s.tokens[k]; ok {
		return errHashTaken
	}
	mapSet(t, &t.s.tokens, &t.own.tokens, k, tokenFromAuth(&tok))
	return nil
}

func (t *txView) OneTimeTokenByHash(_ context.Context, hash access.Hash) (access.OneTimeToken, bool, error) {
	r, ok := t.s.tokens[hash.Key()]
	if !ok {
		return access.OneTimeToken{}, false, nil
	}
	return r.toAuth(), true, nil
}

// ConsumeOneTimeToken refuses a token from its ExpiresAt instant on, deleting it (rule 7).
func (t *txView) ConsumeOneTimeToken(_ context.Context, hash access.Hash, now time.Time) (access.OneTimeToken, bool, error) {
	k := hash.Key()
	r, ok := t.s.tokens[k]
	if !ok {
		return access.OneTimeToken{}, false, nil
	}
	mapDel(t, &t.s.tokens, &t.own.tokens, k)
	if !r.ExpiresAt.After(now) {
		return access.OneTimeToken{}, false, nil
	}
	return r.toAuth(), true, nil
}

func (t *txView) deleteTokens(match func(*storeToken) bool) int {
	var gone []access.HashKey
	for k, r := range t.s.tokens {
		if match(&r) {
			gone = append(gone, k)
		}
	}
	for _, k := range gone {
		mapDel(t, &t.s.tokens, &t.own.tokens, k)
	}
	return len(gone)
}

func (t *txView) DeleteOneTimeTokens(_ context.Context, purpose access.TokenPurpose, subject int64) (int, error) {
	return t.deleteTokens(func(r *storeToken) bool { return r.Purpose == purpose && r.Subject == subject }), nil
}

func (t *txView) LiveOneTimeTokens(_ context.Context, purposes []access.TokenPurpose, now time.Time) ([]access.OneTimeToken, error) {
	out := []access.OneTimeToken{}
	for _, r := range t.s.tokens {
		if slices.Contains(purposes, r.Purpose) && r.ExpiresAt.After(now) {
			out = append(out, r.toAuth())
		}
	}
	return out, nil
}

func (t *txView) DeleteOneTimeTokensIssuedBy(_ context.Context, keyIDs []int64) (int, error) {
	return t.deleteTokens(func(r *storeToken) bool {
		return r.IssuedByKey != 0 && slices.Contains(keyIDs, r.IssuedByKey)
	}), nil
}

// --- Key attributions ---

func (t *txView) CreateKeyAttribution(_ context.Context, a *access.KeyAttribution) error {
	id, err := t.nextID(func(n *nextIDs) *int64 { return &n.Attribution })
	if err != nil {
		return err
	}
	r := attributionFromAuth(a)
	r.ID = id
	appendSlice(t, &t.s.attributions, &t.own.attributions, r)
	a.ID = id
	return nil
}

func (t *txView) ListKeyAttributions(context.Context) ([]access.KeyAttribution, error) {
	out := make([]access.KeyAttribution, 0, len(t.s.attributions))
	for i := range t.s.attributions {
		out = append(out, t.s.attributions[i].toAuth())
	}
	return out, nil
}

// --- Failure records ---

func (t *txView) AuthFailure(_ context.Context, ref account.FailureRef) (account.FailureCount, bool, error) {
	trace("AuthFailure")
	r, ok := t.s.failures[ref]
	if !ok {
		return account.FailureCount{}, false, nil
	}
	return r.toAuth(), true, nil
}

func (t *txView) PutAuthFailure(_ context.Context, f account.FailureCount) error { //nolint:gocritic // hugeParam: account.FailureStore's signature
	trace("PutAuthFailure")
	mapSet(t, &t.s.failures, &t.own.failures, f.FailureRef, failureFromAuth(&f))
	return nil
}

func (t *txView) UnreportedLocks(context.Context) ([]account.FailureCount, error) {
	out := []account.FailureCount{}
	for _, r := range t.s.failures {
		if r.Unreported {
			out = append(out, r.toAuth())
		}
	}
	return out, nil
}

func (t *txView) DeleteAuthFailures(_ context.Context, name string, methods []access.Method) error {
	for _, m := range methods {
		for _, trusted := range []bool{false, true} {
			mapDel(t, &t.s.failures, &t.own.failures, account.FailureRef{Name: name, Method: m, Trusted: trusted})
		}
	}
	return nil
}

func (t *txView) PruneAuthFailures(_ context.Context, keep []string) (int64, error) {
	var gone []account.FailureRef
	for ref := range t.s.failures {
		if !slices.Contains(keep, ref.Name) {
			gone = append(gone, ref)
		}
	}
	for _, ref := range gone {
		mapDel(t, &t.s.failures, &t.own.failures, ref)
	}
	return int64(len(gone)), nil
}

// --- Security log (rule 22) ---

// setLogClass stores es as class's slice; owned says es shares no array a published snapshot
// holds, so a later append may write into it.
func (t *txView) setLogClass(class account.LogClass, es []storeLogEntry, owned bool) {
	if t.own.logClass == nil {
		t.own.logClass = map[account.LogClass]bool{}
	}
	was := t.own.logClass[class]
	mapSet(t, &t.s.log, &t.own.logMap, class, es)
	t.own.logClass[class] = owned
	t.record(func() { t.own.logClass[class] = was })
}

func (t *txView) AppendLog(_ context.Context, e *account.LogEntry) error {
	if !slices.Contains(account.LogClasses(), e.Class) {
		return errLogClass
	}
	n := t.s.nextIDs
	if n.Log > maxID {
		return errIDsExhausted
	}
	seq := n.Log
	n.Log++
	t.setNextIDs(n)
	r := logEntryFromAuth(e)
	r.Event.Seq = seq
	base := t.s.log[e.Class]
	if t.own.logClass == nil || !t.own.logClass[e.Class] {
		base = slices.Grow(slices.Clone(base), 1)
	}
	t.setLogClass(e.Class, append(base, r), true)
	if _, held := t.s.logEvicted[e.Class]; !held {
		mapSet(t, &t.s.logEvicted, &t.own.logEvicted, e.Class, 0)
	}
	e.Event.Seq = seq
	return nil
}

func (t *txView) ListLog(_ context.Context, r account.LogRange) ([]account.LogEntry, error) {
	if r.Limit < 1 {
		return nil, errLogLimit
	}
	es := mergeLog(t.s.log, r.AfterSeq, r.BeforeSeq, r.Limit, r.Descending)
	out := make([]account.LogEntry, 0, len(es))
	for i := range es {
		out = append(out, es[i].toAuth())
	}
	return out, nil
}

// PruneLog removes from the front of its class's slice, so a Keep trim costs only what it
// removes and a no-op costs one length check.
func (t *txView) PruneLog(_ context.Context, p account.LogPrune) (int, error) {
	switch {
	case p.Limit < 1:
		return 0, errLogLimit
	case p.Keep < 0, (p.Keep > 0) == !p.Before.IsZero():
		return 0, errLogCriterion
	}
	es, ok := t.s.log[p.Class]
	if !ok {
		return 0, nil
	}
	if p.Keep > 0 {
		n := min(p.Limit, max(len(es)-p.Keep, 0))
		if n == 0 {
			return 0, nil
		}
		t.setLogClass(p.Class, es[n:], t.own.logClass[p.Class])
		mapSet(t, &t.s.logEvicted, &t.own.logEvicted, p.Class, t.s.logEvicted[p.Class]+uint64(n))
		return n, nil
	}
	n := 0
	kept := make([]storeLogEntry, 0, len(es))
	for i := range es {
		if n < p.Limit && es[i].At.Before(p.Before) {
			n++
			continue
		}
		kept = append(kept, es[i])
	}
	if n > 0 {
		t.setLogClass(p.Class, kept, true)
	}
	return n, nil
}

func (t *txView) LogStats(context.Context) ([]account.LogClassStats, error) {
	out := []account.LogClassStats{}
	for _, class := range account.LogClasses() {
		if ev, ok := t.s.logEvicted[class]; ok {
			out = append(out, account.LogClassStats{Class: class, Count: len(t.s.log[class]), Evicted: ev})
		}
	}
	return out, nil
}

// setSeenSeq caps seq at the highest Seq assigned, so activity_seen_seq stays below
// next_ids.log: decode resets a store whose seen value is not.
func (t *txView) setSeenSeq(seq uint64) {
	seq = min(seq, t.s.nextIDs.Log-1)
	if seq <= t.s.seenSeq {
		return
	}
	old := t.s.seenSeq
	t.s.seenSeq = seq
	t.record(func() { t.s.seenSeq = old })
}
