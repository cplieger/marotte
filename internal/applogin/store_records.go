package applogin

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"slices"
	"time"

	"github.com/cplieger/access"
	"github.com/cplieger/access/account"
)

// The store-private record types keep every field of the library's records with explicit tags:
// the library tags the fields a store must keep `json:"-"`. Each hash field is `json:"-"` here too;
// its durable text travels only in the wire struct that embeds the record.

type storeUser struct {
	CreatedAt      time.Time           `json:"created_at"`
	UpdatedAt      time.Time           `json:"updated_at"`
	Username       string              `json:"username"`
	Email          string              `json:"email,omitempty"`
	DisplayName    string              `json:"display_name,omitempty"`
	PasswordHash   access.Hash         `json:"-"`
	Role           access.Role         `json:"role"`
	OIDC           access.OIDCIdentity `json:"oidc"`
	WebAuthnHandle []byte              `json:"webauthn_handle,omitempty"`
	ID             int64               `json:"id"`
	Enabled        bool                `json:"enabled"`
}

func userFromAuth(u *access.User) storeUser {
	return storeUser{
		CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt, Username: u.Username, Email: u.Email,
		DisplayName: u.DisplayName, PasswordHash: u.PasswordHash, Role: u.Role, OIDC: u.OIDC,
		WebAuthnHandle: slices.Clone(u.WebAuthnHandle), ID: u.ID, Enabled: u.Enabled,
	}
}

func (r *storeUser) toAuth() *access.User {
	return &access.User{
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, Username: r.Username, Email: r.Email,
		DisplayName: r.DisplayName, PasswordHash: r.PasswordHash, Role: r.Role, OIDC: r.OIDC,
		WebAuthnHandle: slices.Clone(r.WebAuthnHandle), ID: r.ID, Enabled: r.Enabled,
	}
}

type storeSession struct {
	CreatedAt    time.Time          `json:"created_at"`
	LastActivity time.Time          `json:"last_activity"`
	AuthTime     time.Time          `json:"auth_time"`
	ID           string             `json:"id"`
	TokenHash    access.Hash        `json:"-"`
	AuthMethod   access.Method      `json:"auth_method"`
	Kind         access.SessionKind `json:"kind"`
	UserAgent    string             `json:"user_agent,omitempty"`
	Label        string             `json:"label,omitempty"`
	LastIP       string             `json:"last_ip,omitempty"`
	UserID       int64              `json:"user_id"`
	IssuedByKey  int64              `json:"issued_by_key,omitempty"`
	IdleCap      time.Duration      `json:"idle_cap,omitempty"`
	AbsoluteCap  time.Duration      `json:"absolute_cap,omitempty"`
	Plain        bool               `json:"plain"`
}

func sessionFromAuth(s *access.Session, plain bool) storeSession {
	return storeSession{
		CreatedAt: s.CreatedAt, LastActivity: s.LastActivity, AuthTime: s.AuthTime, ID: s.ID,
		TokenHash: s.TokenHash, AuthMethod: s.AuthMethod, Kind: s.Kind, UserAgent: s.UserAgent,
		Label: s.Label, LastIP: s.LastIP, UserID: s.UserID, IssuedByKey: s.IssuedByKey,
		IdleCap: s.IdleCap, AbsoluteCap: s.AbsoluteCap, Plain: plain,
	}
}

func (r *storeSession) toAuth() *access.Session {
	return &access.Session{
		CreatedAt: r.CreatedAt, LastActivity: r.LastActivity, AuthTime: r.AuthTime, ID: r.ID,
		TokenHash: r.TokenHash, AuthMethod: r.AuthMethod, Kind: r.Kind, UserAgent: r.UserAgent,
		Label: r.Label, LastIP: r.LastIP, UserID: r.UserID, IssuedByKey: r.IssuedByKey,
		IdleCap: r.IdleCap, AbsoluteCap: r.AbsoluteCap,
	}
}

func (r *storeSession) ref() access.SessionRef {
	return access.SessionRef{ID: r.ID, UserID: r.UserID}
}

type storePasskey struct {
	CreatedAt         time.Time                      `json:"created_at"`
	LastUsedAt        time.Time                      `json:"last_used_at,omitzero"`
	Discoverable      *bool                          `json:"discoverable,omitempty"`
	AttestationType   string                         `json:"attestation_type,omitempty"`
	AttestationFormat string                         `json:"attestation_format,omitempty"`
	RPID              string                         `json:"rp_id"`
	Transport         string                         `json:"transport,omitempty"`
	Name              string                         `json:"name"`
	Attachment        access.AuthenticatorAttachment `json:"attachment,omitempty"`
	CredentialID      []byte                         `json:"credential_id"`
	PublicKey         []byte                         `json:"public_key"`
	AAGUID            []byte                         `json:"aaguid,omitempty"`
	RawAttestation    []byte                         `json:"raw_attestation,omitempty"`
	ID                int64                          `json:"id"`
	UserID            int64                          `json:"user_id"`
	SignCount         uint32                         `json:"sign_count"`
	RawFlags          uint8                          `json:"raw_flags"`
	BackupEligible    bool                           `json:"backup_eligible"`
	BackupState       bool                           `json:"backup_state"`
	UserPresent       bool                           `json:"user_present"`
	UserVerified      bool                           `json:"user_verified"`
	CloneWarning      bool                           `json:"clone_warning"`
}

func passkeyFromAuth(c *access.PasskeyCredential) storePasskey {
	cp := c.Clone()
	return storePasskey{
		CreatedAt: cp.CreatedAt, LastUsedAt: cp.LastUsedAt, Discoverable: cp.Discoverable,
		AttestationType: cp.AttestationType, AttestationFormat: cp.AttestationFormat, RPID: cp.RPID,
		Transport: cp.Transport, Name: cp.Name, Attachment: cp.Attachment, CredentialID: cp.CredentialID,
		PublicKey: cp.PublicKey, AAGUID: cp.AAGUID, RawAttestation: cp.RawAttestation, ID: cp.ID,
		UserID: cp.UserID, SignCount: cp.SignCount, RawFlags: cp.RawFlags, BackupEligible: cp.BackupEligible,
		BackupState: cp.BackupState, UserPresent: cp.UserPresent, UserVerified: cp.UserVerified,
		CloneWarning: cp.CloneWarning,
	}
}

func (r *storePasskey) toAuth() *access.PasskeyCredential {
	c := &access.PasskeyCredential{
		CreatedAt: r.CreatedAt, LastUsedAt: r.LastUsedAt, Discoverable: r.Discoverable,
		AttestationType: r.AttestationType, AttestationFormat: r.AttestationFormat, RPID: r.RPID,
		Transport: r.Transport, Name: r.Name, Attachment: r.Attachment, CredentialID: r.CredentialID,
		PublicKey: r.PublicKey, AAGUID: r.AAGUID, RawAttestation: r.RawAttestation, ID: r.ID,
		UserID: r.UserID, SignCount: r.SignCount, RawFlags: r.RawFlags, BackupEligible: r.BackupEligible,
		BackupState: r.BackupState, UserPresent: r.UserPresent, UserVerified: r.UserVerified,
		CloneWarning: r.CloneWarning,
	}
	return c.Clone()
}

type storeKey struct {
	CreatedAt  time.Time   `json:"created_at"`
	ExpiresAt  time.Time   `json:"expires_at,omitzero"`
	LastUsedAt time.Time   `json:"last_used_at,omitzero"`
	KeyHash    access.Hash `json:"-"`
	KeyPrefix  string      `json:"key_prefix"`
	KeySuffix  string      `json:"key_suffix"`
	Label      string      `json:"label"`
	Scopes     []string    `json:"scopes"`
	LineageIDs []int64     `json:"lineage_ids"`
	ID         int64       `json:"id"`
	UserID     int64       `json:"user_id"`
}

func keyFromAuth(k *access.Key) storeKey {
	return storeKey{
		CreatedAt: k.CreatedAt, ExpiresAt: k.ExpiresAt, LastUsedAt: k.LastUsedAt, KeyHash: k.KeyHash,
		KeyPrefix: k.KeyPrefix, KeySuffix: k.KeySuffix, Label: k.Label, Scopes: slices.Clone(k.Scopes),
		LineageIDs: slices.Clone(k.LineageIDs), ID: k.ID, UserID: k.UserID,
	}
}

func (r *storeKey) toAuth() *access.Key {
	return &access.Key{
		CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, LastUsedAt: r.LastUsedAt, KeyHash: r.KeyHash,
		KeyPrefix: r.KeyPrefix, KeySuffix: r.KeySuffix, Label: r.Label, Scopes: slices.Clone(r.Scopes),
		LineageIDs: slices.Clone(r.LineageIDs), ID: r.ID, UserID: r.UserID,
	}
}

type storeToken struct {
	CreatedAt   time.Time           `json:"created_at"`
	ExpiresAt   time.Time           `json:"expires_at"`
	Hash        access.Hash         `json:"-"`
	Purpose     access.TokenPurpose `json:"purpose"`
	Role        access.Role         `json:"role,omitempty"`
	Subject     int64               `json:"subject,omitempty"`
	IssuedByKey int64               `json:"issued_by_key,omitempty"`
}

func tokenFromAuth(t *access.OneTimeToken) storeToken {
	return storeToken{
		CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt, Hash: t.Hash, Purpose: t.Purpose,
		Role: t.Role, Subject: t.Subject, IssuedByKey: t.IssuedByKey,
	}
}

func (r *storeToken) toAuth() access.OneTimeToken {
	return access.OneTimeToken{
		CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, Hash: r.Hash, Purpose: r.Purpose,
		Role: r.Role, Subject: r.Subject, IssuedByKey: r.IssuedByKey,
	}
}

type storeFailure struct {
	LockedAt   time.Time     `json:"locked_at,omitzero"`
	Name       string        `json:"name"`
	Method     access.Method `json:"method"`
	Count      int           `json:"count"`
	Trusted    bool          `json:"trusted,omitempty"`
	Locked     bool          `json:"locked,omitempty"`
	Unreported bool          `json:"unreported,omitempty"`
}

func failureFromAuth(f *account.FailureCount) storeFailure {
	return storeFailure{
		LockedAt: f.LockedAt, Name: f.Name, Method: f.Method, Trusted: f.Trusted,
		Count: f.Count, Locked: f.Locked, Unreported: f.Unreported,
	}
}

func (r *storeFailure) ref() account.FailureRef {
	return account.FailureRef{Name: r.Name, Method: r.Method, Trusted: r.Trusted}
}

func (r *storeFailure) toAuth() account.FailureCount {
	return account.FailureCount{
		LockedAt: r.LockedAt, FailureRef: r.ref(), Count: r.Count, Locked: r.Locked, Unreported: r.Unreported,
	}
}

type storeAttribution struct {
	At         time.Time                `json:"at"`
	Action     access.AttributionAction `json:"action"`
	KeyLineage []int64                  `json:"key_lineage"`
	ID         int64                    `json:"id"`
	UserID     int64                    `json:"user_id"`
}

func attributionFromAuth(a *access.KeyAttribution) storeAttribution {
	return storeAttribution{At: a.At, Action: a.Action, KeyLineage: slices.Clone(a.KeyLineage), ID: a.ID, UserID: a.UserID}
}

func (r *storeAttribution) toAuth() access.KeyAttribution {
	return access.KeyAttribution{At: r.At, Action: r.Action, KeyLineage: slices.Clone(r.KeyLineage), ID: r.ID, UserID: r.UserID}
}

type storeLogEntry struct {
	At             time.Time        `json:"at"`
	IP             netip.Addr       `json:"ip,omitzero"`
	Class          account.LogClass `json:"class"`
	ActorLabel     string           `json:"actor_label,omitempty"`
	ActorUserAgent string           `json:"actor_user_agent,omitempty"`
	ActorKeySuffix string           `json:"actor_key_suffix,omitempty"`
	Detail         string           `json:"detail,omitempty"`
	Event          storeEvent       `json:"event"`
}

type storeEvent struct {
	At         time.Time         `json:"at"`
	Kind       account.EventKind `json:"kind"`
	Method     access.Method     `json:"method,omitempty"`
	Role       access.Role       `json:"role,omitempty"`
	SessionIDs []string          `json:"session_ids,omitempty"`
	Actor      storeEventActor   `json:"actor"`
	UserID     int64             `json:"user_id,omitempty"`
	KeyID      int64             `json:"key_id,omitempty"`
	PasskeyID  int64             `json:"passkey_id,omitempty"`
	Seq        uint64            `json:"seq"`
	Trusted    bool              `json:"trusted,omitempty"`
}

type storeEventActor struct {
	SessionID string `json:"session_id,omitempty"`
	OutOfBand string `json:"out_of_band,omitempty"`
	UserID    int64  `json:"user_id,omitempty"`
	KeyID     int64  `json:"key_id,omitempty"`
}

func logEntryFromAuth(e *account.LogEntry) storeLogEntry {
	ev := &e.Event
	return storeLogEntry{
		At: e.At, IP: e.IP, Class: e.Class, ActorLabel: e.ActorLabel, ActorUserAgent: e.ActorUserAgent,
		ActorKeySuffix: e.ActorKeySuffix, Detail: e.Detail,
		Event: storeEvent{
			At: ev.At, Kind: ev.Kind, Method: ev.Method, Role: ev.Role, SessionIDs: slices.Clone(ev.SessionIDs),
			Actor:  storeEventActor{SessionID: ev.Actor.SessionID, OutOfBand: ev.Actor.OutOfBand, UserID: ev.Actor.UserID, KeyID: ev.Actor.KeyID},
			UserID: ev.UserID, KeyID: ev.KeyID, PasskeyID: ev.PasskeyID, Seq: ev.Seq, Trusted: ev.Trusted,
		},
	}
}

func (r *storeLogEntry) toAuth() account.LogEntry {
	ev := &r.Event
	return account.LogEntry{
		At: r.At, IP: r.IP, Class: r.Class, ActorLabel: r.ActorLabel, ActorUserAgent: r.ActorUserAgent,
		ActorKeySuffix: r.ActorKeySuffix, Detail: r.Detail,
		Event: account.Event{
			At: ev.At, Kind: ev.Kind, Method: ev.Method, Role: ev.Role, SessionIDs: slices.Clone(ev.SessionIDs),
			Actor:  account.EventActor{SessionID: ev.Actor.SessionID, OutOfBand: ev.Actor.OutOfBand, UserID: ev.Actor.UserID, KeyID: ev.Actor.KeyID},
			UserID: ev.UserID, KeyID: ev.KeyID, PasskeyID: ev.PasskeyID, Seq: ev.Seq, Trusted: ev.Trusted,
		},
	}
}

type storeLogClass struct {
	Class   account.LogClass `json:"class"`
	Evicted uint64           `json:"evicted"`
}

// nextIDs are the high-water counters of rule 17 and rule 22: each holds the next value to assign,
// and nothing ever lowers one.
type nextIDs struct {
	User        int64  `json:"user"`
	Passkey     int64  `json:"passkey"`
	Key         int64  `json:"key"`
	Attribution int64  `json:"attribution"`
	Log         uint64 `json:"log"`
}

func freshNextIDs() nextIDs { return nextIDs{User: 1, Passkey: 1, Key: 1, Attribution: 1, Log: 1} }

// maxID is the largest ID a JSON number, and so the browser, reads exactly (rule 17).
const maxID = 1<<53 - 1

var errIDsExhausted = errors.New("applogin: record IDs exhausted")

func takeID(next *int64) (int64, error) {
	if *next > maxID {
		return 0, errIDsExhausted
	}
	id := *next
	*next++
	return id, nil
}

// The wire structs exist only inside encode and decode: they carry each masked value's durable form
// and are never logged, returned or kept.

type wireUser struct {
	PasswordHash string `json:"password_hash,omitempty"`
	storeUser
}

type wireSession struct {
	TokenHash string `json:"token_hash"`
	storeSession
}

type wireKey struct {
	KeyHash string `json:"key_hash"`
	storeKey
}

type wireToken struct {
	Hash string `json:"hash"`
	storeToken
}

type wireProvider struct {
	storeProvider
	ClientSecret string `json:"client_secret,omitempty"`
}

type wirePolicy struct {
	Provider wireProvider `json:"provider"`
	storePolicy
}

// fileState is access.json's shape.
type fileState struct {
	Attributions    []storeAttribution `json:"attributions"`
	Passkeys        []storePasskey     `json:"passkeys"`
	Keys            []wireKey          `json:"keys"`
	Tokens          []wireToken        `json:"tokens"`
	Failures        []storeFailure     `json:"failures"`
	Users           []wireUser         `json:"users"`
	Sessions        []wireSession      `json:"sessions"`
	Log             []storeLogEntry    `json:"log"`
	LogClasses      []storeLogClass    `json:"log_classes"`
	DeviceKeys      [][]byte           `json:"device_keys"`
	FailureKey      []byte             `json:"failure_key"`
	Policy          wirePolicy         `json:"policy"`
	NextIDs         nextIDs            `json:"next_ids"`
	ActivitySeenSeq uint64             `json:"activity_seen_seq"`
}

// encode serializes s in a deterministic order, so equal states write equal bytes.
func encode(s *snapshot) ([]byte, error) {
	f := fileState{
		Users:        make([]wireUser, 0, len(s.users)),
		Sessions:     make([]wireSession, 0, len(s.sessions)),
		Passkeys:     []storePasskey{},
		Keys:         make([]wireKey, 0, len(s.keys)),
		Tokens:       make([]wireToken, 0, len(s.tokens)),
		Failures:     make([]storeFailure, 0, len(s.failures)),
		Attributions: s.attributions,
		NextIDs:      s.nextIDs,
		Policy: wirePolicy{storePolicy: s.policy, Provider: wireProvider{
			storeProvider: s.policy.Provider, ClientSecret: s.policy.Provider.ClientSecret.Reveal(),
		}},
		Log:             []storeLogEntry{},
		LogClasses:      []storeLogClass{},
		ActivitySeenSeq: s.seenSeq,
		DeviceKeys:      make([][]byte, 0, len(s.deviceKeys)),
		FailureKey:      []byte(s.failureKey.Reveal()),
	}
	if f.Attributions == nil {
		f.Attributions = []storeAttribution{}
	}
	for i := range s.users {
		f.Users = append(f.Users, wireUser{storeUser: s.users[i], PasswordHash: s.users[i].PasswordHash.Reveal()})
	}
	for _, r := range s.sessions {
		f.Sessions = append(f.Sessions, wireSession{storeSession: *r, TokenHash: r.TokenHash.Reveal()})
	}
	slices.SortFunc(f.Sessions, func(a, b wireSession) int { return cmpSessionOrder(&a.storeSession, &b.storeSession) })
	for _, uid := range sortedKeys(s.passkeys) {
		f.Passkeys = append(f.Passkeys, s.passkeys[uid]...)
	}
	for _, id := range sortedKeys(s.keyByID) {
		r := s.keys[s.keyByID[id]]
		f.Keys = append(f.Keys, wireKey{storeKey: *r, KeyHash: r.KeyHash.Reveal()})
	}
	for _, r := range s.tokens {
		f.Tokens = append(f.Tokens, wireToken{storeToken: r, Hash: r.Hash.Reveal()})
	}
	slices.SortFunc(f.Tokens, func(a, b wireToken) int { return cmp.Compare(a.Hash, b.Hash) })
	for _, r := range s.failures {
		f.Failures = append(f.Failures, r)
	}
	slices.SortFunc(f.Failures, compareFailures)
	for _, class := range account.LogClasses() {
		if ev, ok := s.logEvicted[class]; ok {
			f.LogClasses = append(f.LogClasses, storeLogClass{Class: class, Evicted: ev})
		}
	}
	f.Log = mergeLog(s.log, 0, 0, 0, false)
	for _, k := range s.deviceKeys {
		f.DeviceKeys = append(f.DeviceKeys, []byte(k.Reveal()))
	}
	return json.Marshal(&f)
}

// errCorrupt marks bytes that do not decode into a state the store can serve: the
// open's corrupt arm.
var errCorrupt = errors.New("applogin: access.json is not a valid sign-in store")

func corrupt(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errCorrupt, fmt.Sprintf(format, args...))
}

const keyBytes = 32

// decode parses access.json and refuses, wrapping errCorrupt, trailing data, out-of-range counters,
// bad key counts and lengths, duplicate or out-of-range IDs, and records missing their hash or ID.
func decode(data []byte) (*snapshot, error) {
	var f fileState
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%w: %w", errCorrupt, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, corrupt("trailing data after the JSON value")
	}
	if err := checkNextIDs(&f.NextIDs); err != nil {
		return nil, err
	}
	if n := len(f.DeviceKeys); n < 1 || n > 4 {
		return nil, corrupt("%d device keys, want 1 to 4", n)
	}
	for i, k := range f.DeviceKeys {
		if len(k) != keyBytes {
			return nil, corrupt("device key %d is %d bytes, want %d", i, len(k), keyBytes)
		}
	}
	if len(f.FailureKey) != keyBytes {
		return nil, corrupt("failure key is %d bytes, want %d", len(f.FailureKey), keyBytes)
	}
	if f.Policy.Generation < 1 || f.Policy.Generation > maxID {
		return nil, corrupt("policy generation %d out of range", f.Policy.Generation)
	}
	s := emptySnapshot()
	s.nextIDs = f.NextIDs
	s.policy = f.Policy.storePolicy
	s.policy.Provider = f.Policy.Provider.storeProvider
	s.policy.Provider.ClientSecret = access.NewSecret(f.Policy.Provider.ClientSecret)
	s.policy.Provider.Pinned = clonePins(s.policy.Provider.Pinned)
	s.seenSeq = f.ActivitySeenSeq
	for _, k := range f.DeviceKeys {
		s.deviceKeys = append(s.deviceKeys, access.NewSecret(string(k)))
	}
	s.failureKey = access.NewSecret(string(f.FailureKey))
	steps := []func(*snapshot, *fileState) error{decodeUsers, decodeSessions, decodePasskeys, decodeKeys, decodeTokens, decodeRest, decodeLog}
	for _, step := range steps {
		if err := step(s, &f); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func decodeUsers(s *snapshot, f *fileState) error {
	for i := range f.Users {
		u := f.Users[i].storeUser
		u.PasswordHash = access.NewHash(f.Users[i].PasswordHash)
		switch {
		case u.ID < 1 || u.ID >= s.nextIDs.User:
			return corrupt("user ID %d outside 1 to next_ids.user", u.ID)
		case i > 0 && u.ID <= s.users[i-1].ID:
			return corrupt("users not in ascending ID order at %d", u.ID)
		}
		if err := checkUserClaims(s, &u); err != nil {
			return corrupt("user %d: %v", u.ID, err)
		}
		s.users = append(s.users, u)
		s.indexUser(len(s.users) - 1)
	}
	return nil
}

func decodeSessions(s *snapshot, f *fileState) error {
	for i := range f.Sessions {
		r := f.Sessions[i].storeSession
		r.TokenHash = access.NewHash(f.Sessions[i].TokenHash)
		if r.TokenHash.IsZero() || r.ID == "" {
			return corrupt("a session without a hash or an ID")
		}
		k := r.TokenHash.Key()
		if _, dup := s.sessions[k]; dup {
			return corrupt("two sessions share a hash")
		}
		if _, dup := s.sessByID[r.ID]; dup {
			return corrupt("two sessions share an ID")
		}
		s.sessions[k] = &r
		s.sessByID[r.ID] = k
	}
	return nil
}

func decodePasskeys(s *snapshot, f *fileState) error {
	ids := map[int64]bool{}
	for i := range f.Passkeys {
		p := f.Passkeys[i]
		switch {
		case p.ID < 1 || p.ID >= s.nextIDs.Passkey:
			return corrupt("passkey ID %d outside 1 to next_ids.passkey", p.ID)
		case ids[p.ID]:
			return corrupt("two passkeys share ID %d", p.ID)
		case len(p.CredentialID) == 0:
			return corrupt("passkey %d has no credential ID", p.ID)
		}
		if _, dup := s.credIDs[string(p.CredentialID)]; dup {
			return corrupt("two passkeys share a credential ID")
		}
		ids[p.ID] = true
		s.passkeys[p.UserID] = append(s.passkeys[p.UserID], p)
		s.credIDs[string(p.CredentialID)] = p.ID
	}
	for uid := range s.passkeys {
		slices.SortFunc(s.passkeys[uid], func(a, b storePasskey) int { return cmp.Compare(a.ID, b.ID) })
	}
	return nil
}

func decodeKeys(s *snapshot, f *fileState) error {
	for i := range f.Keys {
		r := f.Keys[i].storeKey
		r.KeyHash = access.NewHash(f.Keys[i].KeyHash)
		switch {
		case r.ID < 1 || r.ID >= s.nextIDs.Key:
			return corrupt("key ID %d outside 1 to next_ids.key", r.ID)
		case r.KeyHash.IsZero():
			return corrupt("key %d has no hash", r.ID)
		}
		k := r.KeyHash.Key()
		if _, dup := s.keys[k]; dup {
			return corrupt("two keys share a hash")
		}
		if _, dup := s.keyByID[r.ID]; dup {
			return corrupt("two keys share ID %d", r.ID)
		}
		s.keys[k] = &r
		s.keyByID[r.ID] = k
	}
	return nil
}

func decodeTokens(s *snapshot, f *fileState) error {
	for i := range f.Tokens {
		r := f.Tokens[i].storeToken
		r.Hash = access.NewHash(f.Tokens[i].Hash)
		if r.Hash.IsZero() {
			return corrupt("a token without a hash")
		}
		k := r.Hash.Key()
		if _, dup := s.tokens[k]; dup {
			return corrupt("two tokens share a hash")
		}
		s.tokens[k] = r
	}
	return nil
}

func decodeRest(s *snapshot, f *fileState) error {
	for i := range f.Failures {
		r := f.Failures[i]
		if _, dup := s.failures[r.ref()]; dup {
			return corrupt("two failure records share a ref")
		}
		s.failures[r.ref()] = r
	}
	for i := range f.Attributions {
		a := f.Attributions[i]
		if a.ID < 1 || a.ID >= s.nextIDs.Attribution || (i > 0 && a.ID <= f.Attributions[i-1].ID) {
			return corrupt("attribution ID %d out of order or outside next_ids.attribution", a.ID)
		}
		s.attributions = append(s.attributions, a)
	}
	return nil
}

func decodeLog(s *snapshot, f *fileState) error {
	for _, c := range f.LogClasses {
		if !slices.Contains(account.LogClasses(), c.Class) {
			return corrupt("log class %q", c.Class)
		}
		if _, dup := s.logEvicted[c.Class]; dup {
			return corrupt("log class %q listed twice", c.Class)
		}
		s.logEvicted[c.Class] = c.Evicted
	}
	var last uint64
	for i := range f.Log {
		e := f.Log[i]
		switch _, held := s.logEvicted[e.Class]; {
		case !held:
			return corrupt("log entry of unlisted class %q", e.Class)
		case e.Event.Seq <= last || e.Event.Seq >= s.nextIDs.Log:
			return corrupt("log Seq %d out of order or outside next_ids.log", e.Event.Seq)
		}
		last = e.Event.Seq
		s.log[e.Class] = append(s.log[e.Class], e)
	}
	if s.seenSeq >= s.nextIDs.Log {
		return corrupt("activity_seen_seq %d at or past next_ids.log", s.seenSeq)
	}
	return nil
}

// checkNextIDs refuses a counter outside 1 to maxID+1, the values a create can leave. takeID
// trusts its counter, so a file missing next_ids would otherwise open and hand out ID 0, the
// value every reference reads as none.
func checkNextIDs(n *nextIDs) error {
	for _, c := range []struct {
		name string
		next int64
	}{{"user", n.User}, {"passkey", n.Passkey}, {"key", n.Key}, {"attribution", n.Attribution}} {
		if c.next < 1 || c.next > maxID+1 {
			return corrupt("next_ids.%s %d outside 1 to %d", c.name, c.next, int64(maxID+1))
		}
	}
	if n.Log < 1 || n.Log > maxID+1 {
		return corrupt("next_ids.log %d outside 1 to %d", n.Log, uint64(maxID+1))
	}
	return nil
}

func compareFailures(a, b storeFailure) int {
	if c := cmp.Compare(a.Name, b.Name); c != 0 {
		return c
	}
	if c := cmp.Compare(string(a.Method), string(b.Method)); c != 0 {
		return c
	}
	switch {
	case a.Trusted == b.Trusted:
		return 0
	case b.Trusted:
		return -1
	}
	return 1
}

// cmpSessionOrder is the (UserID, ID) order the cleanup walks.
func cmpSessionOrder(a, b *storeSession) int {
	if c := cmp.Compare(a.UserID, b.UserID); c != 0 {
		return c
	}
	return cmp.Compare(a.ID, b.ID)
}
