package applogin

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/access"
	"github.com/cplieger/access/accesstest"
	"github.com/cplieger/access/account"
	"github.com/cplieger/access/oidc"
	"github.com/cplieger/access/ratelimit"
	"github.com/cplieger/access/webauthn"
	"github.com/cplieger/go/slogx/capture"
	"github.com/cplieger/marotte/internal/filemode"
)

var epoch = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// reopenStore closes s and opens a new store over the same file and test disk.
func reopenStore(t *testing.T, s *Store, d *testDisk) *Store {
	t.Helper()
	s.Close()
	return d.open(t)
}

func mustCreateUser(t *testing.T, s *Store, name string) *access.User {
	t.Helper()
	u := &access.User{Username: name, Role: access.RoleAdmin, Enabled: true, CreatedAt: epoch}
	if err := s.CreateUser(t.Context(), u); err != nil {
		t.Fatalf("CreateUser(%s): %v", name, err)
	}
	return u
}

func mustCreateKey(t *testing.T, s *Store, userID int64) *access.Key {
	t.Helper()
	g, err := access.GenerateAPIKey("test_")
	if err != nil {
		t.Fatalf("GenerateAPIKey: %v", err)
	}
	k := &access.Key{KeyHash: g.Hash, UserID: userID, Label: "k", Scopes: []string{"tools"}, CreatedAt: epoch}
	if err := s.CreateAPIKey(t.Context(), k); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	return k
}

func mustCreatePasskey(t *testing.T, s *Store, userID int64, cred string) *access.PasskeyCredential {
	t.Helper()
	p := &access.PasskeyCredential{UserID: userID, CredentialID: []byte(cred), PublicKey: []byte("pk"), RPID: "marotte.example", Name: cred}
	if err := s.CreatePasskey(t.Context(), p); err != nil {
		t.Fatalf("CreatePasskey: %v", err)
	}
	return p
}

func mustAttribute(t *testing.T, s *Store, userID int64, action access.AttributionAction) access.KeyAttribution {
	t.Helper()
	a := &access.KeyAttribution{UserID: userID, Action: action, KeyLineage: []int64{7, 9}, At: epoch}
	if err := s.Update(t.Context(), func(tx account.Tx) error { return tx.CreateKeyAttribution(t.Context(), a) }); err != nil {
		t.Fatalf("CreateKeyAttribution: %v", err)
	}
	return *a
}

func TestRecords_PlainTagSurvivesAReopen(t *testing.T) {
	s, d := openTestStore(t)
	plain, https := newTestSession(1, epoch), newTestSession(1, epoch)
	if err := s.CreateSession(plainCtx(t.Context()), plain); err != nil {
		t.Fatalf("CreateSession(plain): %v", err)
	}
	if err := s.CreateSession(httpsCtx(t.Context()), https); err != nil {
		t.Fatalf("CreateSession(https): %v", err)
	}
	s = reopenStore(t, s, d)
	for _, c := range []struct {
		sess *access.Session
		name string
		want bool
	}{{plain, "plain", true}, {https, "https", false}} {
		rec, ok := s.sessionRecord(c.sess.TokenHash)
		if !ok {
			t.Errorf("%s session is gone after the reopen", c.name)
			continue
		}
		if rec.Plain != c.want {
			t.Errorf("%s session's Plain = %t after the reopen, want %t", c.name, rec.Plain, c.want)
		}
	}
}

func TestKeys_LineageEndsWithOwnID(t *testing.T) {
	h := newHarness(t)
	si := h.setupOwner(t.Context())
	p := h.principal(si.Token)
	fromSession, err := h.svc.CreateAPIKey(plainCtx(t.Context()), account.As(p), p.User.ID, account.NewKey{Label: "s", Scopes: []string{"tools"}})
	if err != nil {
		t.Fatalf("CreateAPIKey(session): %v", err)
	}
	outOfBand, err := h.svc.CreateAPIKey(t.Context(), account.OutOfBand("admin-socket"), p.User.ID, account.NewKey{Label: "o", Scopes: []string{"tools"}})
	if err != nil {
		t.Fatalf("CreateAPIKey(out of band): %v", err)
	}
	for _, k := range []access.Key{fromSession.Key, outOfBand.Key} {
		stored, ok := h.store.keyRecord(k.ID)
		if !ok {
			t.Fatalf("key %d not stored", k.ID)
		}
		if !slices.Equal(stored.LineageIDs, []int64{k.ID}) {
			t.Errorf("key %d stored lineage %v, want [%d]", k.ID, stored.LineageIDs, k.ID)
		}
		if got := stored.toAuth().ParentKeyID(); got != 0 {
			t.Errorf("key %d ParentKeyID = %d, want 0", k.ID, got)
		}
	}

	t.Run("empty_lineage_decodes_as_stored", func(t *testing.T) {
		s, d := openTestStore(t)
		u := mustCreateUser(t, s, "lineage")
		k := mustCreateKey(t, s, u.ID)
		s.Close()
		editFile(t, d.path, func(f map[string]any) {
			f["keys"].([]any)[0].(map[string]any)["lineage_ids"] = []any{}
		})
		s = d.open(t)
		got, found, err := s.APIKeyByHash(t.Context(), k.KeyHash)
		if err != nil || !found {
			t.Fatalf("APIKeyByHash after the reopen = (%t, %v)", found, err)
		}
		if len(got.LineageIDs) != 0 {
			t.Errorf("an empty stored lineage reopened as %v, want it empty", got.LineageIDs)
		}
	})
}

// editFile rewrites access.json through edit, keeping its private mode.
func editFile(t *testing.T, path string, edit func(map[string]any)) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var f map[string]any
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	edit(f)
	out, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestTokens_ConsumeRefusedAtTheExpiryInstant(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := t.Context()
	newTok := func() access.OneTimeToken {
		_, h := access.GenerateSessionToken()
		tok := access.OneTimeToken{Hash: h, Purpose: access.PurposeRecovery, Subject: 1, CreatedAt: epoch, ExpiresAt: epoch.Add(time.Hour)}
		if err := s.CreateOneTimeToken(ctx, tok); err != nil {
			t.Fatalf("CreateOneTimeToken: %v", err)
		}
		return tok
	}
	before := newTok()
	if _, found, err := s.ConsumeOneTimeToken(ctx, before.Hash, before.ExpiresAt.Add(-time.Nanosecond)); err != nil || !found {
		t.Errorf("Consume one nanosecond before ExpiresAt = (%t, %v), want consumed", found, err)
	}
	at := newTok()
	if _, found, err := s.ConsumeOneTimeToken(ctx, at.Hash, at.ExpiresAt); err != nil || found {
		t.Errorf("Consume at ExpiresAt = (%t, %v), want not found", found, err)
	}
	if _, found, _ := s.OneTimeTokenByHash(ctx, at.Hash); found {
		t.Error("a token refused at its expiry instant is still stored")
	}
}

func TestTokens_DeleteIssuedByMatchesOnlyListedKeys(t *testing.T) {
	ctx := t.Context()
	const keyK, keyJ = 11, 12
	seed := func(t *testing.T) (*Store, map[int64]access.Hash) {
		s, _ := openTestStore(t)
		hashes := map[int64]access.Hash{}
		for _, by := range []int64{0, keyK, keyJ} {
			_, h := access.GenerateSessionToken()
			if err := s.CreateOneTimeToken(ctx, access.OneTimeToken{Hash: h, Purpose: access.PurposeInvite, IssuedByKey: by, ExpiresAt: epoch.Add(time.Hour)}); err != nil {
				t.Fatalf("CreateOneTimeToken: %v", err)
			}
			hashes[by] = h
		}
		return s, hashes
	}
	for _, c := range []struct {
		name string
		keys []int64
		gone []int64
	}{
		{"listed_key", []int64{keyK}, []int64{keyK}},
		{"empty_list", []int64{}, nil},
		{"zero_key", []int64{0}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, hashes := seed(t)
			n, err := s.DeleteOneTimeTokensIssuedBy(ctx, c.keys)
			if err != nil || n != len(c.gone) {
				t.Errorf("DeleteOneTimeTokensIssuedBy(%v) = (%d, %v), want %d", c.keys, n, err, len(c.gone))
			}
			for by, h := range hashes {
				_, found, _ := s.OneTimeTokenByHash(ctx, h)
				if want := !slices.Contains(c.gone, by); found != want {
					t.Errorf("token issued by %d found = %t, want %t", by, found, want)
				}
			}
		})
	}
}

func TestAttributions_AppendOnlyAndDeletedOnlyWithUser(t *testing.T) {
	s, d := openTestStore(t)
	ctx := t.Context()
	a := mustCreateUser(t, s, "attributed-a")
	b := mustCreateUser(t, s, "attributed-b")
	key := mustCreateKey(t, s, a.ID)
	var written []access.KeyAttribution
	for _, u := range []*access.User{a, b, a, b} {
		written = append(written, mustAttribute(t, s, u.ID, access.AttributionCreated))
	}
	for i := 1; i < len(written); i++ {
		if written[i].ID <= written[i-1].ID {
			t.Fatalf("attribution IDs %d then %d, want increasing in append order", written[i-1].ID, written[i].ID)
		}
	}
	if err := s.DeleteAPIKey(ctx, access.KeyRef{ID: key.ID, UserID: a.ID}); err != nil {
		t.Fatalf("DeleteAPIKey: %v", err)
	}
	disabled := *b
	disabled.Enabled = false
	if err := s.UpdateUser(ctx, &disabled); err != nil {
		t.Fatalf("UpdateUser(disable): %v", err)
	}
	if got, _ := s.ListKeyAttributions(ctx); len(got) != len(written) {
		t.Fatalf("after a key revoke and a disable, %d attributions, want %d", len(got), len(written))
	}
	if err := s.DeleteUser(ctx, a.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	var kept []access.KeyAttribution
	for _, w := range written {
		if w.UserID == b.ID {
			kept = append(kept, w)
		}
	}
	before := attributionsFile(t, d.path)
	s = reopenStore(t, s, d)
	got, err := s.ListKeyAttributions(ctx)
	if err != nil {
		t.Fatalf("ListKeyAttributions: %v", err)
	}
	if len(got) != len(kept) {
		t.Fatalf("after DeleteUser and a reopen, %d attributions, want %d", len(got), len(kept))
	}
	for i := range kept {
		w, g := kept[i], got[i]
		if g.ID != w.ID || g.UserID != w.UserID || g.Action != w.Action || !g.At.Equal(w.At) || !slices.Equal(g.KeyLineage, w.KeyLineage) {
			t.Errorf("attribution %d after the reopen = %+v, want %+v", i, g, w)
		}
	}
	if after := attributionsFile(t, d.path); !bytes.Equal(before, after) {
		t.Error("a reopen rewrote the stored attributions")
	}
}

func attributionsFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var f struct {
		Attributions json.RawMessage `json:"attributions"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return f.Attributions
}

// TestIDs_NeverReusedAfterDeleteAndReopen: after a cascade delete and a
// reopen, the next user, passkey, key and key-attribution IDs are fresh, and every surviving
// attribution survives the reopen.
func TestIDs_NeverReusedAfterDeleteAndReopen(t *testing.T) {
	s, d := openTestStore(t)
	ctx := t.Context()
	// The deleted user and attribution hold the highest IDs, so a counter rebuilt from the
	// surviving records at decode would hand them out again.
	other := mustCreateUser(t, s, "other")
	otherAtt := []access.KeyAttribution{
		mustAttribute(t, s, other.ID, access.AttributionCreated),
		mustAttribute(t, s, other.ID, access.AttributionRecoveryStarted),
	}
	gone := mustCreateUser(t, s, "gone")
	passkey := mustCreatePasskey(t, s, gone.ID, "cred-gone")
	key := mustCreateKey(t, s, gone.ID)
	att := mustAttribute(t, s, gone.ID, access.AttributionCreated)
	if err := s.DeleteUser(ctx, gone.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	s = reopenStore(t, s, d)
	got, err := s.ListKeyAttributions(ctx)
	if err != nil {
		t.Fatalf("ListKeyAttributions: %v", err)
	}
	if len(got) != len(otherAtt) {
		t.Fatalf("after the reopen %d attributions, want the other user's %d", len(got), len(otherAtt))
	}
	for i, w := range otherAtt {
		if g := got[i]; g.ID != w.ID || g.UserID != w.UserID || g.Action != w.Action || !slices.Equal(g.KeyLineage, w.KeyLineage) {
			t.Errorf("attribution %d after the reopen = %+v, want %+v", i, g, w)
		}
	}
	nu := mustCreateUser(t, s, "fresh")
	np := mustCreatePasskey(t, s, nu.ID, "cred-fresh")
	nk := mustCreateKey(t, s, nu.ID)
	na := mustAttribute(t, s, nu.ID, access.AttributionCreated)
	for _, c := range []struct {
		name      string
		next, old int64
	}{
		{"user", nu.ID, max(gone.ID, other.ID)},
		{"passkey", np.ID, passkey.ID},
		{"key", nk.ID, key.ID},
		{"attribution", na.ID, max(att.ID, otherAtt[1].ID)},
	} {
		if c.next <= c.old {
			t.Errorf("next %s ID %d, want above every ID assigned before the reopen (%d)", c.name, c.next, c.old)
		}
	}
}

func TestCreateSession_NoTransportStoresPlainAndLogsError(t *testing.T) {
	logs := capture.Default(t)
	s, _ := openTestStore(t)
	sess := newTestSession(1, epoch)
	if err := s.CreateSession(context.Background(), sess); err != nil {
		t.Fatalf("CreateSession without a transport = %v, want nil", err)
	}
	if rec, ok := s.sessionRecord(sess.TokenHash); !ok || !rec.Plain {
		t.Errorf("session without a transport stored Plain = %t (found %t), want true", rec.Plain, ok)
	}
	if n := logs.CountExact("access: a session was created without a transport; stored as plain HTTP"); n != 1 {
		t.Errorf("missing-transport ERROR logged %d times, want 1", n)
	}
}

func TestRotateSession_KeepsTheTag(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := t.Context()
	sess := newTestSession(1, epoch)
	sess.IdleCap, sess.AbsoluteCap = 30*time.Minute, 12*time.Hour
	if err := s.CreateSession(plainCtx(ctx), sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	next := *sess
	_, next.TokenHash = access.GenerateSessionToken()
	if err := s.RotateSession(httpsCtx(ctx), account.Rotation{ID: sess.ID, UserID: sess.UserID, Next: next, ExpectHash: sess.TokenHash}); err != nil {
		t.Fatalf("RotateSession: %v", err)
	}
	rec, ok := s.sessionRecord(next.TokenHash)
	if !ok {
		t.Fatal("the rotated session is not found under its new hash")
	}
	if !rec.Plain || rec.IdleCap != 30*time.Minute || rec.AbsoluteCap != 12*time.Hour {
		t.Errorf("rotated record Plain %t caps (%s, %s), want true and (30m, 12h)", rec.Plain, rec.IdleCap, rec.AbsoluteCap)
	}
	if _, ok := s.sessionRecord(sess.TokenHash); ok {
		t.Error("the old hash still finds the session after the rotation")
	}
}

func TestReads_DoNotBlockOnASlowFsync(t *testing.T) {
	s, d := openTestStore(t)
	u := mustCreateUser(t, s, "reader")
	entered, release := make(chan struct{}), make(chan struct{})
	d.setInject(func(op, path string) error {
		if op == "commit" {
			close(entered)
			<-release
			d.setInject(nil)
		}
		return nil
	})
	done := make(chan error, 1)
	go func() { done <- s.CreateSession(plainCtx(context.Background()), newTestSession(u.ID, epoch)) }()
	<-entered
	readDone := make(chan bool, 1)
	go func() {
		_, found, err := s.UserByUsername(context.Background(), "reader")
		readDone <- found && err == nil
	}()
	select {
	case ok := <-readDone:
		if !ok {
			t.Error("UserByUsername during a slow fsync did not find the user")
		}
	case <-time.After(5 * time.Second):
		t.Error("UserByUsername waited on a held fsync")
	}
	close(release)
	if err := <-done; err != nil {
		t.Errorf("CreateSession after the fsync = %v", err)
	}
}

func TestCleanup_RefusesZeroTimeoutsAndLimitBelowOne(t *testing.T) {
	s, d := openTestStore(t)
	ctx := t.Context()
	old := newTestSession(1, epoch)
	if err := s.CreateSession(plainCtx(ctx), old); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	base := d.publishes.Load()
	now := epoch.Add(400 * 24 * time.Hour)
	for _, c := range []struct {
		name     string
		timeouts access.SessionTimeouts
		limit    int
	}{
		{"limit_zero", access.SessionTimeouts{Idle: time.Hour, Absolute: time.Hour}, 0},
		{"zero_timeouts", access.SessionTimeouts{}, 10},
		{"absolute_only", access.SessionTimeouts{Absolute: time.Hour}, 10},
	} {
		refs, err := s.CleanupExpiredSessions(ctx, now, c.timeouts, c.limit)
		if err == nil || refs != nil {
			t.Errorf("%s: CleanupExpiredSessions = (%v, %v), want an error and no refs", c.name, refs, err)
		}
	}
	if got := d.publishes.Load(); got != base {
		t.Errorf("refused cleanups published %d times", got-base)
	}
	if _, ok := s.sessionRecord(old.TokenHash); !ok {
		t.Error("a refused cleanup deleted the session")
	}
}

func TestPlainCaps_PlainOrMissingTransportCapsHTTPSDoesNot(t *testing.T) {
	for _, c := range []struct {
		ctx            context.Context
		name           string
		idle, absolute time.Duration
	}{
		{plainCtx(t.Context()), "plain", 30 * time.Minute, 12 * time.Hour},
		{t.Context(), "missing", 30 * time.Minute, 12 * time.Hour},
		{httpsCtx(t.Context()), "https", 0, 0},
	} {
		idle, absolute := PlainCaps(c.ctx, account.Client{})
		if idle != c.idle || absolute != c.absolute {
			t.Errorf("%s: PlainCaps = (%s, %s), want (%s, %s)", c.name, idle, absolute, c.idle, c.absolute)
		}
	}
}

func TestDirectDelete_ReturnsFenceErrorAndCallerIgnoresIt(t *testing.T) {
	s, d := openTestStore(t)
	ctx := t.Context()
	sess := newTestSession(1, epoch)
	if err := s.CreateSession(plainCtx(ctx), sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	d.faults().AfterPublish()
	if err := s.CreateSession(plainCtx(ctx), newTestSession(1, epoch)); !errors.Is(err, ErrNotDurable) {
		t.Fatalf("the non-durable write = %v, want ErrNotDurable", err)
	}
	if err := s.DeleteSessionDirect(ctx, sess.TokenHash); !errors.Is(err, ErrFenced) {
		t.Errorf("DeleteSessionDirect while fenced = %v, want ErrFenced", err)
	}
	if _, ok := s.sessionRecord(sess.TokenHash); !ok {
		t.Error("a fenced direct delete removed the record")
	}
}

func TestLog_ResetStoreStartsAtSeqOne(t *testing.T) {
	d := newTestDisk(t)
	if err := os.WriteFile(d.path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, reset, err := OpenStore(context.Background(), d.path, d)
	if err != nil {
		t.Fatalf("OpenStore over a corrupt file: %v", err)
	}
	t.Cleanup(s.Close)
	if !reset.Taken() {
		t.Fatal("the corrupt file was not reset")
	}
	e := &account.LogEntry{Class: account.LogClassChange, At: epoch, Event: account.Event{Kind: "marotte.test"}}
	if err := s.AppendLog(t.Context(), e); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	if e.Event.Seq != 1 {
		t.Errorf("the reset store's first Seq = %d, want 1", e.Event.Seq)
	}
	stats, _ := s.LogStats(t.Context())
	for _, st := range stats {
		if st.Evicted != 0 {
			t.Errorf("class %s Evicted = %d after a reset, want 0", st.Class, st.Evicted)
		}
	}
}

const (
	markerHash   = "marker-hash-0123456789abcdef"
	markerSecret = "marker-secret-fedcba9876543210"
)

// TestRecords_PrintNoSecret: no printer shows a marker, and every field able
// to carry secret material is masked or on the reviewed allow-list.
func TestRecords_PrintNoSecret(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := t.Context()
	u := &access.User{Username: "printed", PasswordHash: access.NewHash(markerHash + "-u"), Enabled: true}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	sess := newTestSession(u.ID, epoch)
	sess.TokenHash = access.NewHash(markerHash + "-s")
	if err := s.CreateSession(plainCtx(ctx), sess); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAPIKey(ctx, &access.Key{KeyHash: access.NewHash(markerHash + "-k"), UserID: u.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOneTimeToken(ctx, access.OneTimeToken{Hash: access.NewHash(markerHash + "-t"), Purpose: access.PurposeInvite, ExpiresAt: epoch}); err != nil {
		t.Fatal(err)
	}
	provider := storeProvider{Issuer: "https://idp.example.com", ClientID: "c", ClientSecret: access.NewSecret(markerSecret)}
	policy := defaultPolicy()
	policy.Provider = provider
	snap := s.cur.Load()
	values := map[string]any{
		"storeUser":     userFromAuth(u),
		"storeSession":  sessionFromAuth(sess, true),
		"storeKey":      *snap.keys[snap.keyByID[1]],
		"storeToken":    snap.tokens[access.NewHash(markerHash+"-t").Key()],
		"storeProvider": provider,
		"storePolicy":   policy,
		"PolicyView":    viewOf(&policy),
		"snapshot":      snap,
	}
	markers := []string{markerHash, markerSecret}
	for name, v := range values {
		for _, out := range printed(t, v) {
			for _, m := range markers {
				if bytes.Contains(out.text, []byte(m)) || bytes.Contains(out.text, []byte(hexOf(m))) {
					t.Errorf("%s printed with %s shows a marker: %s", name, out.how, out.text)
				}
			}
		}
	}
	for _, typ := range []any{storeUser{}, storeSession{}, storeKey{}, storeToken{}, storeProvider{}, storePolicy{}, PolicyView{}, snapshot{}} {
		for _, bad := range unmaskedSecretFields(reflect.TypeOf(typ)) {
			t.Errorf("field %s can carry secret material but is neither masked nor allow-listed", bad)
		}
	}
}

type printOut struct {
	how  string
	text []byte
}

func printed(t *testing.T, v any) []printOut {
	t.Helper()
	var outs []printOut
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d", "%p"} {
		outs = append(outs, printOut{verb, []byte(fmt.Sprintf(verb, v))})
	}
	var text, js bytes.Buffer
	slog.New(slog.NewTextHandler(&text, nil)).Info("m", "v", v)
	slog.New(slog.NewJSONHandler(&js, nil)).Info("m", "v", v)
	outs = append(outs, printOut{"slog text", text.Bytes()}, printOut{"slog json", js.Bytes()})
	if data, err := json.Marshal(v); err == nil {
		outs = append(outs, printOut{"json.Marshal", data})
	}
	return outs
}

func hexOf(s string) string { return hex.EncodeToString([]byte(s)) }

var (
	hashType   = reflect.TypeFor[access.Hash]()
	secretType = reflect.TypeFor[access.Secret]()
)

// secretAllowList names the fields whose names look secret but hold none: index keys and display
// parts the library makes public.
var secretAllowList = map[string]bool{
	"snapshot.keys":          true, // map key HashKey: the library's comparable tag, never the text
	"snapshot.keyByID":       true,
	"snapshot.tokens":        true,
	"storeKey.KeyPrefix":     true,
	"storeKey.KeySuffix":     true,
	"storePasskey.PublicKey": true, // a public key
	"ProviderOrigins.Token":  true, // the token endpoint's origin
}

// unmaskedSecretFields walks t (fields, pointers, slices, maps) and lists each field whose name
// says it holds a hash, secret, password, token or key that is not typed access.Hash or
// access.Secret (or a slice of one) and not allow-listed.
func unmaskedSecretFields(t reflect.Type) []string {
	var bad []string
	seen := map[reflect.Type]bool{}
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Map {
			if t.Kind() == reflect.Map {
				walk(t.Key())
			}
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct || seen[t] || t == hashType || t == secretType {
			return
		}
		seen[t] = true
		if t.PkgPath() != reflect.TypeFor[storeUser]().PkgPath() {
			return
		}
		for f := range t.Fields() {
			path := t.Name() + "." + f.Name
			ft := f.Type
			for ft.Kind() == reflect.Slice && ft.Elem().Kind() != reflect.Uint8 {
				ft = ft.Elem()
			}
			leaf := ft.Kind() == reflect.String || ft.Kind() == reflect.Slice || ft == hashType || ft == secretType
			name := strings.ToLower(f.Name)
			looks := strings.Contains(name, "hash") || strings.Contains(name, "secret") ||
				strings.Contains(name, "password") || strings.Contains(name, "token") || strings.HasSuffix(name, "key") || strings.HasSuffix(name, "keys")
			if looks && leaf && ft != hashType && ft != secretType && !secretAllowList[path] && !slices.Contains(bad, path) {
				bad = append(bad, path)
			}
			walk(f.Type)
		}
	}
	walk(t)
	return bad
}

func TestKeys_RingAndFailureKeySurviveAReopen(t *testing.T) {
	s, d := openTestStore(t)
	before := KeyBytes(append(s.DeviceKeys(), s.FailureKey())...)
	ring, err := access.NewSealer(KeyBytes(s.DeviceKeys()...)...)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := ring.Seal("device", []byte("payload"), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	s = reopenStore(t, s, d)
	after := KeyBytes(append(s.DeviceKeys(), s.FailureKey())...)
	if len(after) != len(before) {
		t.Fatalf("key count %d after the reopen, want %d", len(after), len(before))
	}
	for i := range before {
		if !bytes.Equal(before[i], after[i]) {
			t.Errorf("key %d changed across the reopen", i)
		}
	}
	ring2, err := access.NewSealer(KeyBytes(s.DeviceKeys()...)...)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ring2.Open("device", sealed, time.Now()); err != nil || string(got) != "payload" {
		t.Errorf("a device cookie sealed before the reopen opens as (%q, %v)", got, err)
	}

	t.Run("failure_record_found_after", func(t *testing.T) {
		h := newHarness(t)
		h.setupOwner(t.Context())
		if _, err := h.svc.PasswordLogin(plainCtx(t.Context()), testClient, account.Credentials{Username: testUsername, Password: access.NewSecret("wrong password, not it")}); err == nil {
			t.Fatal("a wrong password signed in")
		}
		counted := failureCounts(t, h.store)
		h = h.reopen()
		if _, err := h.svc.PasswordLogin(plainCtx(t.Context()), testClient, account.Credentials{Username: testUsername, Password: access.NewSecret("wrong password, not it")}); err == nil {
			t.Fatal("a wrong password signed in")
		}
		after := failureCounts(t, h.store)
		if len(after) != len(counted) {
			t.Fatalf("failure records %v after the reopen's attempt, want the same refs as %v", after, counted)
		}
		for ref, n := range counted {
			if after[ref] != n+1 {
				t.Errorf("record %+v counts %d after the reopen's attempt, want %d", ref, after[ref], n+1)
			}
		}
	})
}

func failureCounts(t *testing.T, s *Store) map[account.FailureRef]int {
	t.Helper()
	out := map[account.FailureRef]int{}
	for ref, f := range s.cur.Load().failures {
		out[ref] = f.Count
	}
	return out
}

func TestKeys_BinaryKeysSurviveInvalidUTF8(t *testing.T) {
	s, d := openTestStore(t)
	device := bytes.Repeat([]byte{0xff, 0xfe, 0x80, 0xe2, 0x82, 0x41, 0x42, 0x43}, 4)
	failure := bytes.Repeat([]byte{0xe2, 0x82, 0x80, 0xfe, 0xff, 0x00, 0x01, 0x02}, 4)
	if err := s.setKeysForTest(device, failure); err != nil {
		t.Fatalf("set keys: %v", err)
	}
	raw, err := os.ReadFile(d.path)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		DeviceKeys []string `json:"device_keys"`
		FailureKey string   `json:"failure_key"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.DeviceKeys) != 1 || f.DeviceKeys[0] != base64.StdEncoding.EncodeToString(device) {
		t.Errorf("file device_keys %v, want the fixture's base64", f.DeviceKeys)
	}
	if f.FailureKey != base64.StdEncoding.EncodeToString(failure) {
		t.Errorf("file failure_key %q, want the fixture's base64", f.FailureKey)
	}
	s = reopenStore(t, s, d)
	if got := []byte(s.DeviceKeys()[0].Reveal()); !bytes.Equal(got, device) {
		t.Errorf("device key after the reopen = %x, want %x", got, device)
	}
	if got := []byte(s.FailureKey().Reveal()); !bytes.Equal(got, failure) {
		t.Errorf("failure key after the reopen = %x, want %x", got, failure)
	}
}

func TestKeys_WrongKeyShapeTakesTheCorruptArm(t *testing.T) {
	b64 := func(n int) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, n)) }
	for _, c := range []struct {
		edit func(map[string]any)
		name string
	}{
		{func(f map[string]any) { f["failure_key"] = b64(31) }, "failure_key_31"},
		{func(f map[string]any) { f["failure_key"] = b64(33) }, "failure_key_33"},
		{func(f map[string]any) { f["device_keys"] = []any{b64(31)} }, "device_key_31"},
		{func(f map[string]any) { f["device_keys"] = []any{b64(33)} }, "device_key_33"},
		{func(f map[string]any) { delete(f, "failure_key") }, "failure_key_missing"},
		{func(f map[string]any) { f["device_keys"] = []any{} }, "device_keys_empty"},
		{func(f map[string]any) {
			f["device_keys"] = []any{b64(32), b64(32), b64(32), b64(32), b64(32)}
		}, "device_keys_five"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, d := openTestStore(t)
			mustCreateUser(t, s, "owner")
			s.Close()
			editFile(t, d.path, c.edit)
			s2, reset, err := OpenStore(context.Background(), d.path, d)
			if err != nil {
				t.Fatalf("OpenStore = %v, want the corrupt arm", err)
			}
			t.Cleanup(s2.Close)
			if !reset.Taken() {
				t.Error("no Reset: the wrong key shape did not take the corrupt arm")
			}
			if n, _ := s2.UserCount(t.Context()); n != 0 {
				t.Errorf("after the reset %d users, want setup mode (0)", n)
			}
		})
	}
}

func TestDecode_TrailingDataAndCounterRange(t *testing.T) {
	appendBytes := func(tail string) func([]byte) []byte {
		return func(b []byte) []byte { return append(b, tail...) }
	}
	setNext := func(field string, v float64) func([]byte) []byte {
		return func(b []byte) []byte {
			var f map[string]any
			if err := json.Unmarshal(b, &f); err != nil {
				t.Fatal(err)
			}
			if field == "" {
				delete(f, "next_ids")
			} else {
				f["next_ids"].(map[string]any)[field] = v
			}
			out, err := json.Marshal(f)
			if err != nil {
				t.Fatal(err)
			}
			return out
		}
	}
	for _, c := range []struct {
		edit      func([]byte) []byte
		name      string
		wantReset bool
	}{
		{appendBytes("}"), "trailing_brace", true},
		{appendBytes("]"), "trailing_bracket", true},
		{appendBytes("{}"), "trailing_object", true},
		{appendBytes(" \n"), "trailing_whitespace", false},
		{setNext("", 0), "next_ids_missing", true},
		{setNext("user", 0), "next_ids_user_zero", true},
		{setNext("attribution", -1), "next_ids_attribution_negative", true},
		{setNext("key", 1<<54), "next_ids_key_past_the_cap", true},
		{setNext("log", 0), "next_ids_log_zero", true},
		{setNext("passkey", maxID+1), "next_ids_passkey_at_the_cap", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, d := openTestStore(t)
			mustCreateUser(t, s, "owner")
			s.Close()
			data, err := os.ReadFile(d.path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(d.path, c.edit(data), 0o600); err != nil {
				t.Fatal(err)
			}
			s2, reset, err := OpenStore(context.Background(), d.path, d)
			if err != nil {
				t.Fatalf("OpenStore = %v", err)
			}
			t.Cleanup(s2.Close)
			if reset.Taken() != c.wantReset {
				t.Errorf("Reset taken = %t, want %t", reset.Taken(), c.wantReset)
			}
		})
	}
}

func TestCorrupt_CopiedAsideAndSetupMode(t *testing.T) {
	s, d := openTestStore(t)
	mustCreateUser(t, s, "owner")
	oldKey := KeyBytes(s.FailureKey())[0]
	s.Close()
	garbage := []byte(`{"users": [ broken`)
	if err := os.WriteFile(d.path, garbage, 0o600); err != nil {
		t.Fatal(err)
	}
	s2, reset, err := OpenStore(context.Background(), d.path, d)
	if err != nil {
		t.Fatalf("OpenStore = %v", err)
	}
	t.Cleanup(s2.Close)
	if !reset.Taken() {
		t.Fatal("no Reset for a corrupt file")
	}
	kept, err := os.ReadFile(reset.Aside)
	if err != nil || !bytes.Equal(kept, garbage) {
		t.Errorf("the copy aside holds (%q, %v), want the corrupt bytes", kept, err)
	}
	if n, _ := s2.UserCount(t.Context()); n != 0 {
		t.Errorf("after the reset %d users, want 0", n)
	}
	if bytes.Equal(KeyBytes(s2.FailureKey())[0], oldKey) {
		t.Error("the reset store kept the old failure key, want fresh keys")
	}
}

func TestCustody_WidenedFileRepairedOrRefusedNeverDeleted(t *testing.T) {
	t.Run("widened_repaired", func(t *testing.T) {
		s, d := openTestStore(t)
		mustCreateUser(t, s, "owner")
		s.Close()
		if err := os.Chmod(d.path, 0o644); err != nil {
			t.Fatal(err)
		}
		s = d.open(t)
		if n, _ := s.UserCount(t.Context()); n != 1 {
			t.Errorf("the repaired file serves %d users, want 1", n)
		}
		fi, err := os.Stat(d.path)
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("file mode after the open = %v (%v), want 0600", fi.Mode().Perm(), err)
		}
	})
	t.Run("repair_refused_kept", func(t *testing.T) {
		s, d := openTestStore(t)
		mustCreateUser(t, s, "owner")
		s.Close()
		if err := os.Chmod(d.path, 0o644); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(d.path)
		d.setInject(func(op, path string) error {
			if op == "handle.chmod" {
				return errInjected
			}
			return nil
		})
		_, _, err := OpenStore(context.Background(), d.path, d)
		if _, ok := errors.AsType[*filemode.NotPrivateError](err); !ok {
			t.Fatalf("OpenStore = %v, want a *filemode.NotPrivateError", err)
		}
		after, rerr := os.ReadFile(d.path)
		if rerr != nil || !bytes.Equal(before, after) {
			t.Errorf("the refused file changed or vanished (%v)", rerr)
		}
	})
	t.Run("symlink_refused_kept", func(t *testing.T) {
		d := newTestDisk(t)
		target := d.path + ".target"
		if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, d.path); err != nil {
			t.Fatal(err)
		}
		_, _, err := OpenStore(context.Background(), d.path, d)
		if _, ok := errors.AsType[*filemode.NotPrivateError](err); !ok {
			t.Fatalf("OpenStore over a symlink = %v, want a *filemode.NotPrivateError", err)
		}
		if fi, err := os.Lstat(d.path); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("the symlink was removed or replaced (%v)", err)
		}
	})
}

// TestSweep_CappedPlainSessionExpiresWithEvent runs the library's sweep over the store: a Plain
// session past its idle cap goes, with one EventSessionsExpired naming it alone.
func TestSweep_CappedPlainSessionExpiresWithEvent(t *testing.T) {
	cleanupInterval = 20 * time.Millisecond
	t.Cleanup(func() { cleanupInterval = 0 })
	h := newHarness(t)
	ctx := t.Context()
	owner := &access.User{Username: testUsername, Role: access.RoleAdmin, Enabled: true}
	if err := h.store.CreateUser(ctx, owner); err != nil {
		t.Fatal(err)
	}
	age := time.Now().Add(-time.Hour)
	plain, https := newTestSession(owner.ID, age), newTestSession(owner.ID, age)
	plain.IdleCap, plain.AbsoluteCap = 30*time.Minute, 12*time.Hour
	if err := h.store.CreateSession(plainCtx(ctx), plain); err != nil {
		t.Fatal(err)
	}
	if err := h.store.CreateSession(httpsCtx(ctx), https); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && len(h.events.of(account.EventSessionsExpired)) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	evs := h.events.of(account.EventSessionsExpired)
	if len(evs) != 1 {
		t.Fatalf("%d EventSessionsExpired within 1 s, want 1", len(evs))
	}
	e := evs[0]
	if e.UserID != owner.ID || !slices.Equal(e.SessionIDs, []string{plain.ID}) || e.Actor != (account.EventActor{}) {
		t.Errorf("event = user %d sessions %v actor %+v, want the owner, [%s] and no actor", e.UserID, e.SessionIDs, e.Actor, plain.ID)
	}
	if _, ok := h.store.sessionRecord(plain.TokenHash); ok {
		t.Error("the capped Plain session is still stored")
	}
	if _, ok := h.store.sessionRecord(https.TokenHash); !ok {
		t.Error("the uncapped HTTPS session was swept")
	}
}

// openFault is the second half of an open cell's name.
type openFault uint8

const (
	faultNone openFault = iota + 1
	faultCopyFails
	faultCopyNotDurable
	faultCopyLeftover
	faultWriteFails
	faultWriteNotDurable
	faultWriteLeftover
	faultCtxEnded
	faultPanics
)

var openFaults = []openFault{
	faultNone, faultCopyFails, faultCopyNotDurable, faultCopyLeftover,
	faultWriteFails, faultWriteNotDurable, faultWriteLeftover, faultCtxEnded, faultPanics,
}

var faultNames = map[openFault]string{
	faultNone: "None", faultCopyFails: "CopyFails", faultCopyNotDurable: "CopyNotDurable",
	faultCopyLeftover: "CopyLeftover", faultWriteFails: "WriteFails", faultWriteNotDurable: "WriteNotDurable",
	faultWriteLeftover: "WriteLeftover", faultCtxEnded: "CtxEnded", faultPanics: "Panics",
}

var armNames = map[openArm]string{
	armNoFile: "NoFile", armCustody: "Custody", armUnreadable: "Unreadable", armValid: "Valid", armCorrupt: "Corrupt",
}

// openWant is a possible cell's columns. held is set exactly for a cell whose writer runs, and is
// asserted while HoldPublish holds the open job's group. next is the arm the next fault-free open
// takes with the arm's condition kept; fixed, when set, the arm of one more open with it removed.
type openWant struct {
	returns func(t *testing.T, r *openRun)
	disk    func(t *testing.T, r *openRun)
	held    func(t *testing.T, r *openRun)
	next    openArm
	fixed   openArm
}

type openCell struct {
	want       *openWant
	impossible string
	variants   []string
	arm        openArm
	fault      openFault
}

const openPanicValue = "test: injected disk panic"

var corruptFixture = []byte(`{"users": [ not a sign-in store`)

// openRun is one cell's run: its disk, the fixture access.json held before the open, and what
// the open returned.
type openRun struct {
	d       *testDisk
	logs    *capture.Recorder
	s       *Store
	err     error
	variant string
	fixture []byte
	reset   Reset
	arm     openArm
}

func (r *openRun) dir() string { return filepath.Dir(r.d.path) }

// entries lists the directory's copies (access.json.corrupt-*) and staged files (*.tmp).
func (r *openRun) entries(t *testing.T) (copies, staged []string) {
	t.Helper()
	es, err := os.ReadDir(r.dir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		switch name := e.Name(); {
		case strings.HasPrefix(name, "access.json.corrupt-"):
			copies = append(copies, filepath.Join(r.dir(), name))
		case strings.HasSuffix(name, ".tmp"):
			staged = append(staged, filepath.Join(r.dir(), name))
		}
	}
	return copies, staged
}

// condition is the arm's own fault for Custody and Unreadable: a refused mode repair, or the
// variant's read failure ("read" for a cell without an Unreadable variant).
func (r *openRun) condition() func(op, path string) error {
	switch r.arm {
	case armCustody:
		return func(op, _ string) error {
			if op == "handle.chmod" {
				return errInjected
			}
			return nil
		}
	case armUnreadable:
		switch r.variant {
		case "toolarge":
			return nil
		case "lstat":
			return func(op, path string) error {
				if op == "lstat" && path == r.d.path {
					return syscall.EACCES
				}
				return nil
			}
		}
		return func(op, path string) error {
			if op == "read" && path == r.d.path {
				return syscall.EIO
			}
			return nil
		}
	}
	return nil
}

// fault is the cell's injected fault on the disk wrapper; the WriteFails and WriteNotDurable
// faults are the contract's hooks, armed by arm.
func (r *openRun) fault(f openFault) func(op, path string) error {
	isCopy := func(path string) bool { return strings.HasPrefix(filepath.Base(path), "access.json.corrupt-") }
	var discards atomic.Int32
	leftover := func(target func(string) bool) func(op, path string) error {
		return func(op, path string) error {
			switch {
			case op == "write" && target(path):
				return errInjected
			case op == "discard" && target(path):
				if discards.Add(1) == 1 || r.variant == "always" {
					return errInjected
				}
			}
			return nil
		}
	}
	isJ := func(path string) bool { return path == r.d.path }
	switch f {
	case faultCopyFails:
		var lstats atomic.Int32
		return func(op, path string) error {
			switch r.variant {
			case "lstat":
				if op == "lstat" && isJ(path) && lstats.Add(1) == 2 {
					return errInjected
				}
			case "probe":
				if op == "lstat" && isCopy(path) {
					return errInjected
				}
			case "stage":
				if op == "stage" && isCopy(path) {
					return errInjected
				}
			}
			return nil
		}
	case faultCopyNotDurable:
		return func(op, path string) error {
			if op == "commit" && isCopy(path) {
				return errNotDurableCommit
			}
			return nil
		}
	case faultCopyLeftover:
		return leftover(isCopy)
	case faultWriteLeftover:
		return leftover(isJ)
	case faultPanics:
		return func(op, path string) error {
			if (r.variant == "read" && op == "lstat" || r.variant == "stage" && op == "stage") && isJ(path) {
				panic(openPanicValue)
			}
			return nil
		}
	}
	return nil
}

func joinInjects(fns ...func(op, path string) error) func(op, path string) error {
	return func(op, path string) error {
		for _, fn := range fns {
			if fn == nil {
				continue
			}
			if err := fn(op, path); err != nil {
				return err
			}
		}
		return nil
	}
}

func readOrNil(path string) []byte {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return b
}

// --- the columns' checks ---

func retStore(wantReset bool) func(t *testing.T, r *openRun) {
	return func(t *testing.T, r *openRun) {
		t.Helper()
		if r.err != nil || r.s == nil {
			t.Fatalf("OpenStore = (%v, %v), want the store", r.s, r.err)
		}
		if r.reset.Taken() != wantReset {
			t.Errorf("Reset taken %t, want %t", r.reset.Taken(), wantReset)
		}
		if wantReset {
			copies, _ := r.entries(t)
			if !slices.Equal(copies, []string{r.reset.Aside}) {
				t.Errorf("Reset names %q, the directory's copies are %q", r.reset.Aside, copies)
			}
		}
		snap, err := decodeFile(r.d.path)
		if err != nil {
			t.Fatalf("access.json after the open: %v", err)
		}
		if !reflect.DeepEqual(KeyBytes(r.s.DeviceKeys()...), KeyBytes(snap.deviceKeys...)) ||
			!bytes.Equal(KeyBytes(r.s.FailureKey())[0], KeyBytes(snap.failureKey)[0]) {
			t.Error("the store's keys are not byte-equal to the keys access.json holds")
		}
	}
}

func retFailed(t *testing.T, r *openRun) {
	t.Helper()
	if r.s != nil || r.reset.Taken() {
		t.Errorf("a failed open returned a store (%t) or a Reset (%t)", r.s != nil, r.reset.Taken())
	}
}

func retIs(target error) func(t *testing.T, r *openRun) {
	return func(t *testing.T, r *openRun) {
		t.Helper()
		retFailed(t, r)
		if !errors.Is(r.err, target) {
			t.Errorf("OpenStore error = %v, want one matching %v", r.err, target)
		}
	}
}

// retCopyNotDurable: the copy's commit is the unconfirmed write, so access.json was never written.
func retCopyNotDurable(t *testing.T, r *openRun) {
	t.Helper()
	retIs(filemode.ErrNotDurable)(t, r)
	if errors.Is(r.err, ErrNotDurable) {
		t.Errorf("OpenStore error = %v names access.json published, but access.json was never written", r.err)
	}
}

func retLeftover(t *testing.T, r *openRun) {
	t.Helper()
	retFailed(t, r)
	var lo *filemode.LeftoverError
	if !errors.As(r.err, &lo) {
		t.Fatalf("OpenStore error = %v, want a *filemode.LeftoverError", r.err)
	}
	const msg = "access: a staged sign-in file could not be removed"
	switch r.variant {
	case "once":
		if n := r.logs.CountExact(msg); n != 0 {
			t.Errorf("%d staged-file ERRORs after a discard that succeeded, want 0", n)
		}
	case "always":
		if n := r.logs.CountExact(msg); n != 1 || !r.logs.HasAttr(msg, "path", lo.Name()) {
			t.Errorf("%d staged-file ERRORs (naming %s: %t), want 1", n, lo.Name(), r.logs.HasAttr(msg, "path", lo.Name()))
		}
		if strings.Count(r.err.Error(), errInjected.Error()) < 3 {
			t.Errorf("OpenStore error = %v, want the write's and both discards' errors joined", r.err)
		}
	}
}

func retUnreadable(t *testing.T, r *openRun) {
	t.Helper()
	switch r.variant {
	case "toolarge":
		retIs(filemode.ErrTooLarge)(t, r)
	case "lstat":
		retIs(syscall.EACCES)(t, r)
	default:
		retIs(syscall.EIO)(t, r)
	}
}

func retNotPrivate(t *testing.T, r *openRun) {
	t.Helper()
	retFailed(t, r)
	if _, ok := errors.AsType[*filemode.NotPrivateError](r.err); !ok {
		t.Errorf("OpenStore error = %v, want a *filemode.NotPrivateError", r.err)
	}
}

func retPanicked(t *testing.T, r *openRun) {
	t.Helper()
	retFailed(t, r)
	if r.err == nil || r.err.Error() != "access: opening the store panicked: "+openPanicValue {
		t.Errorf("OpenStore error = %v, want one naming the recovered value", r.err)
	}
	const msg = "access: a store write panicked"
	if n := r.logs.CountExact(msg); n != 1 || !r.logs.AttrContains(msg, "panic", openPanicValue) ||
		!r.logs.AttrContains(msg, "stack", "goroutine") {
		t.Errorf("%d panic ERRORs with the value and a stack, want 1", n)
	}
}

func diskAll(checks ...func(t *testing.T, r *openRun)) func(t *testing.T, r *openRun) {
	return func(t *testing.T, r *openRun) {
		t.Helper()
		for _, c := range checks {
			c(t, r)
		}
	}
}

func jSame(t *testing.T, r *openRun) {
	t.Helper()
	if got := readOrNil(r.d.path); !bytes.Equal(got, r.fixture) {
		t.Errorf("access.json changed: %d bytes, want the fixture's %d", len(got), len(r.fixture))
	}
}

func noJ(t *testing.T, r *openRun) {
	t.Helper()
	if _, err := os.Lstat(r.d.path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("access.json exists (%v), want none", err)
	}
}

func jFresh(t *testing.T, r *openRun) {
	t.Helper()
	snap, err := decodeFile(r.d.path)
	if err != nil {
		t.Fatalf("access.json is not a store: %v", err)
	}
	if len(snap.users) != 0 || snap.policy.Generation != 1 || len(snap.deviceKeys) != 1 ||
		len(KeyBytes(snap.deviceKeys[0])[0]) != keyBytes || len(KeyBytes(snap.failureKey)[0]) != keyBytes {
		t.Errorf("access.json = %d users, generation %d, %d device keys; want a fresh state at generation 1 with its two keys",
			len(snap.users), snap.policy.Generation, len(snap.deviceKeys))
	}
}

func noC(t *testing.T, r *openRun) {
	t.Helper()
	if copies, _ := r.entries(t); len(copies) != 0 {
		t.Errorf("copies %q, want none", copies)
	}
}

func cCorrupt(t *testing.T, r *openRun) {
	t.Helper()
	copies, _ := r.entries(t)
	if len(copies) != 1 || !bytes.Equal(readOrNil(copies[0]), corruptFixture) {
		t.Errorf("copies %q, want one holding the corrupt bytes", copies)
	}
}

func noStaged(t *testing.T, r *openRun) {
	t.Helper()
	if _, staged := r.entries(t); len(staged) != 0 {
		t.Errorf("staged files %q left, want none", staged)
	}
}

// discardsOwned: the wrapper recorded n Discard calls, each with diskOwned set.
func discardsOwned(n int) func(t *testing.T, r *openRun) {
	return func(t *testing.T, r *openRun) {
		t.Helper()
		ds := r.d.discards()
		owned := !slices.ContainsFunc(ds, func(c diskCall) bool { return !c.owned })
		if len(ds) != n || !owned {
			t.Errorf("%d Discard calls (all owned: %t), want %d owned", len(ds), owned, n)
		}
	}
}

// leftoverDisk: two Discards, both owned; the staged file gone in once and present in always.
func leftoverDisk(t *testing.T, r *openRun) {
	t.Helper()
	discardsOwned(2)(t, r)
	var lo *filemode.LeftoverError
	if !errors.As(r.err, &lo) {
		return
	}
	_, err := os.Lstat(lo.Name())
	switch r.variant {
	case "once":
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the staged file %s is still present after the job's Discard (%v)", lo.Name(), err)
		}
	case "always":
		if err != nil {
			t.Errorf("the staged file %s is gone (%v), want it left", lo.Name(), err)
		}
	}
}

func noWrite(t *testing.T, r *openRun) {
	t.Helper()
	if slices.ContainsFunc(r.d.recorded(), func(c diskCall) bool { return c.op == "stage" }) {
		t.Error("the open staged a write")
	}
}

func publishesUnmoved(t *testing.T, r *openRun) {
	t.Helper()
	if n := r.d.publishes.Load(); n != 0 {
		t.Errorf("%d publishes, want none", n)
	}
}

// resetLogged: one ERROR naming access.json and the corrupt copy.
func resetLogged(t *testing.T, r *openRun) {
	t.Helper()
	const msg = "access: sign-in data could not be read; it was copied aside and replaced by a fresh store"
	copies, _ := r.entries(t)
	if n := r.logs.CountExact(msg); n != 1 || len(copies) != 1 ||
		!r.logs.HasAttr(msg, "path", r.d.path) || !r.logs.HasAttr(msg, "copy", copies[0]) {
		t.Errorf("%d reset ERRORs naming access.json and the corrupt copy (%q), want 1", n, copies)
	}
}

func heldNoJ(t *testing.T, r *openRun) { t.Helper(); noJ(t, r) }

func heldNothing(*testing.T, *openRun) {}

func heldBothCorrupt(t *testing.T, r *openRun) {
	t.Helper()
	copies, _ := r.entries(t)
	if len(copies) != 1 || !bytes.Equal(readOrNil(copies[0]), corruptFixture) || !bytes.Equal(readOrNil(r.d.path), corruptFixture) {
		t.Errorf("while held: copies %q; want the corrupt copy and access.json both holding the corrupt bytes", copies)
	}
}

var (
	writesNothing = "cannot occur: the arm writes nothing (adoption is memory-only and skips the file write), so no write exists for the fault to hit"
	noFileNoCopy  = "cannot occur: no file, so no copy step"
	refusedArm    = "cannot occur: the arm writes nothing"
)

var openCells = []openCell{
	{arm: armNoFile, fault: faultNone, want: &openWant{returns: retStore(false), disk: diskAll(jFresh), held: heldNoJ, next: armValid}},
	{arm: armNoFile, fault: faultCopyFails, impossible: noFileNoCopy},
	{arm: armNoFile, fault: faultCopyNotDurable, impossible: noFileNoCopy},
	{arm: armNoFile, fault: faultCopyLeftover, impossible: noFileNoCopy},
	{arm: armNoFile, fault: faultWriteFails, want: &openWant{returns: retIs(errInjected), disk: diskAll(noJ, noStaged), next: armNoFile}},
	{arm: armNoFile, fault: faultWriteNotDurable, want: &openWant{returns: retIs(ErrNotDurable), disk: diskAll(jFresh), next: armValid}},
	{
		arm: armNoFile, fault: faultWriteLeftover, variants: []string{"once", "always"},
		want: &openWant{returns: retLeftover, disk: diskAll(noJ, leftoverDisk), next: armNoFile},
	},
	{arm: armNoFile, fault: faultCtxEnded, want: &openWant{returns: retIs(context.Canceled), disk: diskAll(noJ), next: armNoFile}},
	{
		arm: armNoFile, fault: faultPanics, variants: []string{"read", "stage"},
		want: &openWant{returns: retPanicked, disk: diskAll(noJ, noStaged), next: armNoFile},
	},

	{arm: armValid, fault: faultNone, want: &openWant{returns: retStore(false), disk: diskAll(jSame, publishesUnmoved), held: heldNothing, next: armValid}},
	{arm: armValid, fault: faultCopyFails, impossible: writesNothing},
	{arm: armValid, fault: faultCopyNotDurable, impossible: writesNothing},
	{arm: armValid, fault: faultCopyLeftover, impossible: writesNothing},
	{arm: armValid, fault: faultWriteFails, impossible: writesNothing},
	{arm: armValid, fault: faultWriteNotDurable, impossible: writesNothing},
	{arm: armValid, fault: faultWriteLeftover, impossible: writesNothing},
	{arm: armValid, fault: faultCtxEnded, want: &openWant{returns: retIs(context.Canceled), disk: diskAll(jSame), next: armValid}},
	{arm: armValid, fault: faultPanics, variants: []string{"read"}, want: &openWant{returns: retPanicked, disk: diskAll(jSame), next: armValid}},

	{arm: armCorrupt, fault: faultNone, want: &openWant{returns: retStore(true), disk: diskAll(cCorrupt, jFresh, resetLogged), held: heldBothCorrupt, next: armValid}},
	{
		arm: armCorrupt, fault: faultCopyFails, variants: []string{"lstat", "probe", "stage"},
		want: &openWant{returns: retIs(errInjected), disk: diskAll(jSame, noC, noStaged), next: armCorrupt},
	},
	{arm: armCorrupt, fault: faultCopyNotDurable, want: &openWant{returns: retCopyNotDurable, disk: diskAll(jSame, cCorrupt), next: armCorrupt}},
	{
		arm: armCorrupt, fault: faultCopyLeftover, variants: []string{"once", "always"},
		want: &openWant{returns: retLeftover, disk: diskAll(jSame, noC, leftoverDisk), next: armCorrupt},
	},
	{arm: armCorrupt, fault: faultWriteFails, want: &openWant{returns: retIs(errInjected), disk: diskAll(jSame, cCorrupt, noStaged), next: armCorrupt}},
	{arm: armCorrupt, fault: faultWriteNotDurable, want: &openWant{returns: retIs(ErrNotDurable), disk: diskAll(cCorrupt, jFresh, resetLogged), next: armValid}},
	{
		arm: armCorrupt, fault: faultWriteLeftover, variants: []string{"once", "always"},
		want: &openWant{returns: retLeftover, disk: diskAll(jSame, cCorrupt, leftoverDisk), next: armCorrupt},
	},
	{arm: armCorrupt, fault: faultCtxEnded, want: &openWant{returns: retIs(context.Canceled), disk: diskAll(jSame, noC), next: armCorrupt}},
	{
		arm: armCorrupt, fault: faultPanics, variants: []string{"read", "stage"},
		want: &openWant{returns: retPanicked, disk: diskAll(jSame, panicCopy), next: armCorrupt},
	},

	{arm: armCustody, fault: faultNone, want: &openWant{returns: retNotPrivate, disk: diskAll(jSame, noC, noWrite), next: armCustody, fixed: armValid}},
	{arm: armCustody, fault: faultCopyFails, impossible: refusedArm},
	{arm: armCustody, fault: faultCopyNotDurable, impossible: refusedArm},
	{arm: armCustody, fault: faultCopyLeftover, impossible: refusedArm},
	{arm: armCustody, fault: faultWriteFails, impossible: refusedArm},
	{arm: armCustody, fault: faultWriteNotDurable, impossible: refusedArm},
	{arm: armCustody, fault: faultWriteLeftover, impossible: refusedArm},
	{arm: armCustody, fault: faultCtxEnded, want: &openWant{returns: retIs(context.Canceled), disk: diskAll(jSame), next: armCustody}},
	{arm: armCustody, fault: faultPanics, variants: []string{"read"}, want: &openWant{returns: retPanicked, disk: diskAll(jSame, noC), next: armCustody}},

	{
		arm: armUnreadable, fault: faultNone, variants: []string{"toolarge", "lstat", "read"},
		want: &openWant{returns: retUnreadable, disk: diskAll(jSame, noC, noWrite), next: armUnreadable, fixed: armValid},
	},
	{arm: armUnreadable, fault: faultCopyFails, impossible: refusedArm},
	{arm: armUnreadable, fault: faultCopyNotDurable, impossible: refusedArm},
	{arm: armUnreadable, fault: faultCopyLeftover, impossible: refusedArm},
	{arm: armUnreadable, fault: faultWriteFails, impossible: refusedArm},
	{arm: armUnreadable, fault: faultWriteNotDurable, impossible: refusedArm},
	{arm: armUnreadable, fault: faultWriteLeftover, impossible: refusedArm},
	{arm: armUnreadable, fault: faultCtxEnded, want: &openWant{returns: retIs(context.Canceled), disk: diskAll(jSame), next: armUnreadable}},
	{arm: armUnreadable, fault: faultPanics, variants: []string{"read"}, want: &openWant{returns: retPanicked, disk: diskAll(jSame), next: armUnreadable}},
}

// panicCopy: read panics before the copy, stage after it is durable.
func panicCopy(t *testing.T, r *openRun) {
	t.Helper()
	if r.variant == "stage" {
		cCorrupt(t, r)
	} else {
		noC(t, r)
	}
}

// goroutineIDs is the set of live goroutines of the caller's synctest bubble, from a profile of
// all of them; a goroutine outside the bubble (the runtime's finalizer, another test's) is not the
// open's.
func goroutineIDs() map[string]bool {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	ids := map[string]bool{}
	for block := range strings.SplitSeq(string(buf), "\n\n") {
		header, _, _ := strings.Cut(block, "\n")
		if !strings.Contains(header, "synctest bubble") {
			continue
		}
		if id, _, ok := strings.Cut(strings.TrimPrefix(header, "goroutine "), " "); ok {
			ids[id] = true
		}
	}
	return ids
}

// TestOpenStore_EveryArmAndFault runs every possible cell of the open lifecycle: each arm under
// each fault.
func TestOpenStore_EveryArmAndFault(t *testing.T) {
	seen := map[[2]uint8]int{}
	for _, c := range openCells {
		seen[[2]uint8{uint8(c.arm), uint8(c.fault)}]++
		if (c.impossible == "") == (c.want == nil) {
			t.Fatalf("cell %s/%s: an impossible cell carries its reason and no want, a possible one its want", armNames[c.arm], faultNames[c.fault])
		}
	}
	for _, a := range openArms() {
		for _, f := range openFaults {
			if n := seen[[2]uint8{uint8(a), uint8(f)}]; n != 1 {
				t.Fatalf("cell %s/%s (arm %d, fault %d) has %d entries in openCells, want 1", armNames[a], faultNames[f], a, f, n)
			}
		}
	}
	if len(seen) != len(openArms())*len(openFaults) {
		t.Fatalf("openCells holds %d cells, want %d", len(seen), len(openArms())*len(openFaults))
	}
	valid := validFixture(t)
	for _, c := range openCells {
		if c.want == nil {
			continue
		}
		name := armNames[c.arm] + "_" + faultNames[c.fault]
		variants := c.variants
		if variants == nil {
			variants = []string{""}
		}
		for _, v := range variants {
			full := name
			if v != "" {
				full += "_" + v
			}
			t.Run(full, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) { runOpenCell(t, c, v, valid) })
			})
		}
	}
}

// validFixture is the bytes of a store a fault-free first boot wrote.
func validFixture(t *testing.T) []byte {
	t.Helper()
	s, d := openTestStore(t)
	s.Close()
	b, err := os.ReadFile(d.path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func runOpenCell(t *testing.T, c openCell, variant string, valid []byte) {
	r := &openRun{d: newTestDisk(t), logs: capture.Default(t), variant: variant, arm: c.arm}
	switch c.arm {
	case armValid, armUnreadable, armCustody:
		r.fixture = valid
	case armCorrupt:
		r.fixture = corruptFixture
	}
	if r.fixture != nil {
		mode := os.FileMode(0o600)
		if c.arm == armCustody {
			mode = 0o640
		}
		if err := os.WriteFile(r.d.path, r.fixture, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(r.d.path, mode); err != nil {
			t.Fatal(err)
		}
	}
	if c.arm == armUnreadable && variant == "toolarge" {
		bound := openReadBound
		openReadBound = int64(len(r.fixture) - 1)
		t.Cleanup(func() { openReadBound = bound })
	}
	r.d.setInject(joinInjects(r.condition(), r.fault(c.fault)))
	switch c.fault {
	case faultWriteFails:
		r.d.faults().BeforePublish()
	case faultWriteNotDurable:
		r.d.faults().AfterPublish()
	}
	ctx := context.Background()
	if c.fault == faultCtxEnded {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		ctx = cctx
	}

	type result struct {
		s     *Store
		err   error
		reset Reset
	}
	res := make(chan result, 1)
	open := func() {
		defer func() {
			if p := recover(); p != nil {
				res <- result{err: fmt.Errorf("OpenStore panicked: %v", p)}
			}
		}()
		s, reset, err := OpenStore(ctx, r.d.path, r.d)
		res <- result{s, err, reset}
	}
	returned := func() bool {
		select {
		case got := <-res:
			res <- got
			return true
		default:
			return false
		}
	}
	before := goroutineIDs()
	if c.want.held != nil {
		type held struct {
			entered <-chan struct{}
			release func()
		}
		holds := make(chan held, 1)
		r.d.onStore = func(s *Store) {
			entered, _, release := s.holdGroup()
			holds <- held{entered, release}
		}
		go open()
		h := <-holds
		<-h.entered
		synctest.Wait()
		if returned() {
			t.Fatal("OpenStore returned while its group was held")
		}
		c.want.held(t, r)
		h.release()
		got := <-res
		r.s, r.err, r.reset = got.s, got.err, got.reset
		c.want.returns(t, r)
		select {
		case <-r.s.w.writerDone:
			t.Error("the writer stopped after a published open")
		default:
		}
		c.want.disk(t, r)
		r.s.Close()
	} else {
		exitEntered := make(chan (<-chan struct{}), 1)
		exitRelease := make(chan struct{})
		openExitHook = func(done <-chan struct{}) {
			exitEntered <- done
			<-exitRelease
		}
		t.Cleanup(func() { openExitHook = nil })
		go open()
		var done <-chan struct{}
		select {
		case done = <-exitEntered:
		case got := <-res:
			// A published open never runs the exit hook; failing here keeps the bubble from
			// deadlocking, so the other cells still run.
			if got.s != nil {
				got.s.Close()
			}
			t.Fatalf("OpenStore = (%v, %v) with its writer still running, want the cell's failure", got.s, got.err)
		}
		synctest.Wait()
		if returned() {
			t.Error("OpenStore returned before the writer stopped")
		}
		close(exitRelease)
		got := <-res
		r.s, r.err, r.reset = got.s, got.err, got.reset
		select {
		case <-done:
		default:
			t.Error("writerDone is open after OpenStore returned")
		}
		synctest.Wait()
		after := goroutineIDs()
		for id := range after {
			if !before[id] {
				t.Errorf("goroutine %s outlived the failed open", id)
			}
		}
		if r.logs.Contains("access: sign-in data could not be confirmed on disk") {
			t.Error("a failed open logged the fence's ERROR")
		}
		if c.fault == faultCtxEnded && len(r.d.recorded()) != 0 {
			t.Errorf("a cancelled open made disk calls: %v", r.d.recorded())
		}
		c.want.returns(t, r)
		c.want.disk(t, r)
	}
	openExitHook = nil
	r.d.onStore = nil
	nextOpen(t, r, c.want.next, true)
	if c.want.fixed != 0 {
		nextOpen(t, r, c.want.fixed, false)
	}
}

// nextOpen opens again with no fault, the arm's condition kept or removed, and asserts the cell
// of arm's None fault.
func nextOpen(t *testing.T, r *openRun, arm openArm, keep bool) {
	t.Helper()
	cond := r.condition()
	if !keep {
		cond = nil
		openReadBound = 16 << 20
	}
	r.d.setInject(cond)
	r.d.mu.Lock()
	r.d.before, r.d.after = 0, 0
	r.d.mu.Unlock()
	pre, base := readOrNil(r.d.path), r.d.publishes.Load()
	copiesBefore, _ := r.entries(t)
	s, reset, err := OpenStore(context.Background(), r.d.path, r.d)
	n := &openRun{d: r.d, logs: r.logs, s: s, err: err, reset: reset, variant: r.variant, fixture: pre, arm: r.arm}
	if s != nil {
		defer s.Close()
	}
	switch arm {
	case armNoFile:
		retStore(false)(t, n)
		jFresh(t, n)
	case armValid:
		retStore(false)(t, n)
		jSame(t, n)
		if r.d.publishes.Load() != base {
			t.Error("the next open published")
		}
		if copies, _ := r.entries(t); !slices.Equal(copies, copiesBefore) {
			t.Errorf("the next open changed the copies: %q, then %q", copiesBefore, copies)
		}
	case armCorrupt:
		retStore(true)(t, n)
		if !bytes.Equal(readOrNil(reset.Aside), corruptFixture) {
			t.Error("the next open's copy does not hold the corrupt bytes")
		}
		jFresh(t, n)
		if copies, _ := r.entries(t); len(copies) != 1 {
			t.Errorf("the directory holds copies %q, want one", copies)
		}
	case armCustody:
		retNotPrivate(t, n)
		jSame(t, n)
	case armUnreadable:
		retUnreadable(t, n)
		jSame(t, n)
	}
}

const ownerRPID = "marotte.example"

// failureName is a canonical username's failure-record name: the hex HMAC-SHA256 of the name
// under the store's failure key (account.FailureRef's contract).
func (h *harness) failureName(canonical string) string {
	mac := hmac.New(sha256.New, KeyBytes(h.store.FailureKey())[0])
	mac.Write([]byte(canonical))
	return hex.EncodeToString(mac.Sum(nil))
}

// queueInOrder runs calls in one group: it holds a filler group, queues each call behind it in
// order, releases the hold and waits for every call.
func queueInOrder(t *testing.T, s *Store, calls ...func()) {
	t.Helper()
	entered, pending, release := s.holdGroup()
	first := make(chan error, 1)
	go func() { first <- s.CreateSession(plainCtx(context.Background()), newTestSession(999, time.Now())) }()
	<-entered
	var wg sync.WaitGroup
	for i, call := range calls {
		wg.Go(call)
		deadline := time.Now().Add(5 * time.Second)
		for pending() < i+2 {
			if time.Now().After(deadline) {
				release()
				t.Fatalf("call %d never queued", i)
			}
			runtime.Gosched()
		}
	}
	release()
	if err := <-first; err != nil {
		t.Fatalf("the held filler write: %v", err)
	}
	wg.Wait()
}

// enrolPasskey registers dev for p's user through the real ceremony.
func (h *harness) enrolPasskey(p access.Principal, dev *accesstest.PasskeyDevice) {
	h.t.Helper()
	ctx := context.Background()
	o := webOrigin(h.t)
	cer, err := h.svc.PasskeyRegisterBegin(ctx, p, o)
	if err != nil {
		h.t.Fatalf("PasskeyRegisterBegin: %v", err)
	}
	if _, err := h.svc.PasskeyRegisterFinish(ctx, p, cer.ID, bytes.NewReader(dev.Register(mustJSON(h.t, cer.Creation)))); err != nil {
		h.t.Fatalf("PasskeyRegisterFinish: %v", err)
	}
}

// passkeySignIn signs in with dev through the real ceremony.
func (h *harness) passkeySignIn(dev *accesstest.PasskeyDevice) error {
	h.t.Helper()
	ctx := httpsCtx(context.Background())
	cer, err := h.svc.PasskeyLoginBegin(ctx, testClient, webOrigin(h.t), account.LoginModal)
	if err != nil {
		return err
	}
	_, err = h.svc.PasskeyLoginFinish(ctx, testClient, cer.ID, bytes.NewReader(dev.Assert(mustJSON(h.t, cer.Assertion))))
	return err
}

func webOrigin(t testing.TB) webauthn.Origin {
	t.Helper()
	o, err := webauthn.ParseOrigin(testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func mustJSON(t testing.TB, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// savedProvider puts rec on the policy through putPolicy with its built candidate, linking the
// owner to its issuer when link is set.
func (h *harness) savedProvider(rec storeProvider, built *oidc.Provider, link bool) {
	h.t.Helper()
	ctx := context.Background()
	if link {
		owner := h.owner()
		h.fixture(func(tx *txView) error {
			u, _, _ := tx.UserByID(ctx, owner.ID)
			u.OIDC = access.OIDCIdentity{Issuer: rec.Issuer, Subject: "owner-sub"}
			return tx.UpdateUser(ctx, u)
		})
	}
	candidate := h.store.policy()
	candidate.Provider = rec
	if _, _, err := h.store.putPolicy(ctx, h.svc, &accountTestActor, h.origin.RPID(), candidate, candidate.Generation,
		&providerEdit{record: rec, built: built}); err != nil {
		h.t.Fatalf("saving the provider: %v", err)
	}
}

func allowanceOff(p storePolicy, m access.Method) storePolicy {
	switch m {
	case access.MethodPassword:
		p.Methods.Password = false
	case access.MethodPasskey:
		p.Methods.Passkey = false
	case access.MethodOIDC:
		p.Methods.OIDC = false
	}
	return p
}

// TestGuard_RacesInOneGroup: two writes that each leave a working method,
// held in one group in both orders; the second is refused whichever it is.
func TestGuard_RacesInOneGroup(t *testing.T) {
	t.Run("allowance_off_against_passkey_removal", func(t *testing.T) {
		for _, removalFirst := range []bool{false, true} {
			t.Run(map[bool]string{false: "allowance_first", true: "removal_first"}[removalFirst], func(t *testing.T) {
				h := newHarness(t)
				ctx := t.Context()
				h.setupOwner(ctx)
				pk := mustCreatePasskey(t, h.store, h.owner().ID, "race-pk")
				p := h.principal(h.signIn(ctx).Token)
				base := h.store.policy()
				var policyErr, removeErr error
				policy := func() {
					_, _, policyErr = h.store.putPolicy(ctx, h.svc, &accountTestActor, ownerRPID,
						allowanceOff(base, access.MethodPassword), base.Generation, nil)
				}
				remove := func() { removeErr = h.svc.RemovePasskey(ctx, p, pk.ID) }
				if removalFirst {
					queueInOrder(t, h.store, remove, policy)
					if removeErr != nil || !errors.Is(policyErr, ErrNoWorkingMethod) {
						t.Errorf("removal then allowance off = (%v, %v), want (nil, ErrNoWorkingMethod)", removeErr, policyErr)
					}
				} else {
					queueInOrder(t, h.store, policy, remove)
					if policyErr != nil || !errors.Is(removeErr, account.ErrLastMethod) {
						t.Errorf("allowance off then removal = (%v, %v), want (nil, ErrLastMethod)", policyErr, removeErr)
					}
				}
				passkeys, _ := h.store.PasskeysByUserID(ctx, h.owner().ID)
				if pwOn := h.store.policy().Methods.Password; !pwOn && len(passkeys) == 0 {
					t.Error("the published state keeps no working method")
				}
			})
		}
	})
	t.Run("provider_removal_against_password_removal", func(t *testing.T) {
		for _, removalFirst := range []bool{false, true} {
			t.Run(map[bool]string{false: "provider_first", true: "removal_first"}[removalFirst], func(t *testing.T) {
				idp := newSeamIdP(t, idpURL, accesstest.IdPOptions{})
				h := newHarness(t)
				ctx := t.Context()
				h.setupOwner(ctx)
				rec := providerOf(idp)
				h.savedProvider(rec, builtProvider(t, rec, h.origin), true)
				p := h.principal(h.signIn(ctx).Token)
				base := h.store.policy()
				var policyErr, removeErr error
				policy := func() {
					c := base.clone()
					c.Provider = storeProvider{}
					_, _, policyErr = h.store.putPolicy(ctx, h.svc, &accountTestActor, ownerRPID, c, base.Generation, &providerEdit{})
				}
				// RemovePassword counts the password in one Update and removes it in a second: the
				// hook holds that second Update until the group is queued.
				arrived, proceed := make(chan struct{}), make(chan struct{})
				var updates atomic.Int32
				updateHook = func() {
					if updates.Add(1) == 2 {
						close(arrived)
						<-proceed
					}
				}
				t.Cleanup(func() { updateHook = nil })
				done := make(chan struct{})
				go func() {
					defer close(done)
					_, removeErr = h.svc.RemovePassword(plainCtx(ctx), testClient, p, access.NewSecret(testPassword))
				}()
				<-arrived
				removal := func() { close(proceed); <-done }
				if removalFirst {
					queueInOrder(t, h.store, removal, policy)
				} else {
					queueInOrder(t, h.store, policy, removal)
				}
				if removalFirst {
					if removeErr != nil || !errors.Is(policyErr, ErrNoWorkingMethod) {
						t.Errorf("password removal then provider removal = (%v, %v), want (nil, ErrNoWorkingMethod)", removeErr, policyErr)
					}
				} else if policyErr != nil || !errors.Is(removeErr, account.ErrLastMethod) {
					t.Errorf("provider removal then password removal = (%v, %v), want (nil, ErrLastMethod)", policyErr, removeErr)
				}
				hasPassword := !h.owner().PasswordHash.IsZero()
				if !hasPassword && h.store.OIDCProvider() == nil {
					t.Error("the published state keeps no working method")
				}
			})
		}
	})
}

// TestGuard_RemovalAfterAllowanceOffInOneGroupReadsTheGroup: the removal's
// Rules.MethodUsable reads the tx it is handed, the group's private copy, so it sees the
// allowance turned off earlier in the same group.
func TestGuard_RemovalAfterAllowanceOffInOneGroupReadsTheGroup(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	h.setupOwner(ctx)
	pk := mustCreatePasskey(t, h.store, h.owner().ID, "group-pk")
	p := h.principal(h.signIn(ctx).Token)
	base := h.store.policy()
	var policyErr, removeErr error
	queueInOrder(t, h.store,
		func() {
			_, _, policyErr = h.store.putPolicy(ctx, h.svc, &accountTestActor, ownerRPID,
				allowanceOff(base, access.MethodPassword), base.Generation, nil)
		},
		func() { removeErr = h.svc.RemovePasskey(ctx, p, pk.ID) },
	)
	if policyErr != nil {
		t.Fatalf("the allowance-off save = %v, want nil", policyErr)
	}
	if !errors.Is(removeErr, account.ErrLastMethod) {
		t.Errorf("the removal grouped after the allowance off = %v, want ErrLastMethod", removeErr)
	}
	if passkeys, _ := h.store.PasskeysByUserID(ctx, h.owner().ID); len(passkeys) != 1 {
		t.Errorf("%d passkeys after the refused removal, want 1", len(passkeys))
	}
}

// guardState is one fixture of TestGuard_OwnWriteCountsThroughUsableMethods.
type guardState struct {
	pkRPID   string // "" when the owner holds no passkey
	allow    policyMethods
	lock     int // the password's locked failure records: 0, 1 (untrusted only) or 2 (both)
	provider int // 0 none, 1 saved and not built, 2 saved and attached
	pw, link bool
	plain    bool
}

func (st guardState) String() string {
	return fmt.Sprintf("pw=%t lock=%d pk=%q link=%t allow=%+v provider=%d plain=%t",
		st.pw, st.lock, st.pkRPID, st.link, st.allow, st.provider, st.plain)
}

// guardWrite is an own write the guard judges: one allowance off, or the provider removed.
type guardWrite struct {
	off            access.Method
	removeProvider bool
}

// guardWorks reports whether the state w leaves has a working method: a password held, allowed
// and not locked in both contexts; a passkey under the canonical origin's RP ID, allowed; an OIDC
// link, allowed, with its issuer's provider attached.
func guardWorks(st guardState, w guardWrite) bool {
	allow := allowanceOff(storePolicy{Methods: st.allow}, w.off).Methods
	pw := st.pw && allow.Password && st.lock < 2
	pk := st.pkRPID == ownerRPID && allow.Passkey
	oidcWorks := st.link && allow.OIDC && st.provider == 2 && !w.removeProvider
	return pw || pk || oidcWorks
}

func guardStates() []guardState {
	var out []guardState
	for _, pw := range []bool{false, true} {
		locks := []int{0}
		if pw {
			locks = []int{0, 1, 2}
		}
		for _, lock := range locks {
			for _, pk := range []string{"", ownerRPID, "other.example"} {
				for _, link := range []bool{false, true} {
					for a := range 8 {
						for provider := range 3 {
							for _, plain := range []bool{false, true} {
								out = append(out, guardState{
									pw: pw, lock: lock, pkRPID: pk, link: link, provider: provider, plain: plain,
									allow: policyMethods{Password: a&1 != 0, Passkey: a&2 != 0, OIDC: a&4 != 0},
								})
							}
						}
					}
				}
			}
		}
	}
	return out
}

// TestGuard_OwnWriteCountsThroughUsableMethods, for marotte's own writes:
// under every combination of held methods, allowances, locks, RP IDs, provider states and switch
// states, an allowance turned off or the provider removed is refused exactly when no working
// method would be left. Each case's fixture and write run in one group, so the guard can only
// judge the fixture by reading the job's private copy.
func TestGuard_OwnWriteCountsThroughUsableMethods(t *testing.T) {
	idp := newSeamIdP(t, idpURL, accesstest.IdPOptions{})
	h := newHarness(t)
	ctx := t.Context()
	h.setupOwner(ctx)
	owner := h.owner()
	hash := owner.PasswordHash
	rec := providerOf(idp)
	built := builtProvider(t, rec, h.origin)
	name := h.failureName(testUsername)
	var gen int64 = 1000
	var credSeq int
	fixture := func(st guardState) func(tx *txView) error {
		gen += 10
		g := gen
		credSeq++
		cred := fmt.Sprintf("guard-pk-%d", credSeq)
		return func(tx *txView) error {
			u, _, _ := tx.UserByID(ctx, owner.ID)
			u.PasswordHash, u.OIDC = access.Hash{}, access.OIDCIdentity{}
			if st.pw {
				u.PasswordHash = hash
			}
			if st.link {
				u.OIDC = access.OIDCIdentity{Issuer: rec.Issuer, Subject: "owner-sub"}
			}
			if err := tx.UpdateUser(ctx, u); err != nil {
				return err
			}
			tx.deleteUserPasskeys(owner.ID)
			if st.pkRPID != "" {
				if err := tx.CreatePasskey(ctx, &access.PasskeyCredential{
					UserID: owner.ID, CredentialID: []byte(cred), PublicKey: []byte("pk"), RPID: st.pkRPID, Name: cred,
				}); err != nil {
					return err
				}
			}
			if err := tx.DeleteAuthFailures(ctx, name, []access.Method{access.MethodPassword}); err != nil {
				return err
			}
			for i, trusted := range []bool{false, true} {
				if i < st.lock {
					ref := account.FailureRef{Name: name, Method: access.MethodPassword, Trusted: trusted}
					if err := tx.PutAuthFailure(ctx, account.FailureCount{FailureRef: ref, Count: account.FailureCap, Locked: true, LockedAt: epoch}); err != nil {
						return err
					}
				}
			}
			p := tx.s.policy.clone()
			p.Methods, p.PlainHTTP, p.Provider, p.Generation = st.allow, st.plain, storeProvider{}, g
			if st.provider > 0 {
				p.Provider = rec
			}
			tx.setPolicy(&p)
			snap := tx.s
			old := snap.oidc
			snap.oidc = nil
			if st.provider == 2 {
				snap.oidc = built
			}
			tx.record(func() { snap.oidc = old })
			return nil
		}
	}
	type run struct {
		st      guardState
		w       guardWrite
		err     error
		view    PolicyView
		fixture error
	}
	var runs []*run
	var calls []func()
	for _, st := range guardStates() {
		var writes []guardWrite
		for _, m := range policyMethodOrder {
			if st.allow.get(m) {
				writes = append(writes, guardWrite{off: m})
			}
		}
		if st.provider > 0 {
			writes = append(writes, guardWrite{removeProvider: true})
		}
		for _, w := range writes {
			r := &run{st: st, w: w}
			runs = append(runs, r)
			fx := fixture(st)
			candidate := storePolicy{Methods: st.allow, PlainHTTP: st.plain, Idle: defaultIdle, Absolute: defaultAbsolute}
			if st.provider > 0 {
				candidate.Provider = rec
			}
			candidate = allowanceOff(candidate, w.off)
			var edit *providerEdit
			if w.removeProvider {
				candidate.Provider, edit = storeProvider{}, &providerEdit{}
			}
			base := gen
			calls = append(calls,
				func() { r.fixture = h.store.write(ctx, fx) },
				func() {
					_, r.view, r.err = h.store.putPolicy(ctx, h.svc, &accountTestActor, h.origin.RPID(), candidate, base, edit)
				})
		}
	}
	for i := 0; i < len(calls); i += 200 {
		queueInOrder(t, h.store, calls[i:min(i+200, len(calls))]...)
	}
	for _, r := range runs {
		if r.fixture != nil {
			t.Fatalf("%v: fixture write: %v", r.st, r.fixture)
		}
		want := guardWorks(r.st, r.w)
		switch {
		case want && r.err != nil:
			t.Errorf("%v, write %+v: refused (%v), want applied: a working method is left", r.st, r.w, r.err)
		case !want && !errors.Is(r.err, ErrNoWorkingMethod):
			t.Errorf("%v, write %+v: %v, want ErrNoWorkingMethod: no working method is left", r.st, r.w, r.err)
		case want && r.w.removeProvider && r.view.Provider != nil:
			t.Errorf("%v: the applied removal still shows a provider", r.st)
		case want && r.w.off != "" && policyMethods(r.view.Methods).get(r.w.off):
			t.Errorf("%v: the applied save left %s on", r.st, r.w.off)
		}
	}
}

// TestGuard_SoleOIDC: an owner whose only method is a linked OIDC identity.
func TestGuard_SoleOIDC(t *testing.T) {
	idp := newSeamIdP(t, idpURL, accesstest.IdPOptions{})
	idp2 := newSeamIdP(t, idp2URL, accesstest.IdPOptions{})
	h := newHarness(t)
	ctx := t.Context()
	h.setupOwner(ctx)
	rec := providerOf(idp)
	h.savedProvider(rec, builtProvider(t, rec, h.origin), true)
	owner := h.owner()
	h.fixture(func(tx *txView) error {
		u, _, _ := tx.UserByID(ctx, owner.ID)
		u.PasswordHash = access.Hash{}
		return tx.UpdateUser(ctx, u)
	})
	save := func(candidate storePolicy, edit *providerEdit) error {
		base := h.store.policy()
		_, _, err := h.store.putPolicy(ctx, h.svc, &accountTestActor, ownerRPID, candidate, base.Generation, edit)
		return err
	}
	attached := h.store.OIDCProvider()
	t.Run("removing_the_provider", func(t *testing.T) {
		c := h.store.policy()
		c.Provider = storeProvider{}
		if err := save(c, &providerEdit{}); !errors.Is(err, ErrNoWorkingMethod) {
			t.Errorf("= %v, want ErrNoWorkingMethod", err)
		}
	})
	t.Run("another_issuer", func(t *testing.T) {
		rec2 := providerOf(idp2)
		c := h.store.policy()
		c.Provider = rec2
		if err := save(c, &providerEdit{record: rec2, built: builtProvider(t, rec2, h.origin)}); !errors.Is(err, ErrNoWorkingMethod) {
			t.Errorf("= %v, want ErrNoWorkingMethod", err)
		}
	})
	t.Run("sso_allowance_off", func(t *testing.T) {
		if err := save(allowanceOff(h.store.policy(), access.MethodOIDC), nil); !errors.Is(err, ErrNoWorkingMethod) {
			t.Errorf("= %v, want ErrNoWorkingMethod", err)
		}
	})
	if h.store.OIDCProvider() != attached || h.store.policy().Provider.Issuer != rec.Issuer || !h.store.policy().Methods.OIDC {
		t.Fatal("a refused save changed the provider or the allowance")
	}
	t.Run("same_issuer_new_client", func(t *testing.T) {
		next := rec
		next.ClientID, next.ClientSecret = "rotated-client", access.NewSecret("rotated-secret")
		c := h.store.policy()
		c.Provider = next
		built := builtProvider(t, next, h.origin)
		if err := save(c, &providerEdit{record: next, built: built}); err != nil {
			t.Fatalf("= %v, want nil", err)
		}
		if h.store.OIDCProvider() != built || h.store.policy().Provider.ClientID != "rotated-client" {
			t.Error("the accepted save is not the published provider")
		}
	})
}

// TestGuard_ProviderEditToTheLinkedIssuerIsApplied: an owner whose only method is an OIDC link
// saves a provider of that link's issuer, from no provider and from another issuer. The guard
// must count the provider the save attaches, which only the job's private copy holds.
func TestGuard_ProviderEditToTheLinkedIssuerIsApplied(t *testing.T) {
	idp := newSeamIdP(t, idpURL, accesstest.IdPOptions{})
	idp2 := newSeamIdP(t, idp2URL, accesstest.IdPOptions{})
	rec, rec2 := providerOf(idp), providerOf(idp2)
	for _, from := range []string{"no_provider", "another_issuer"} {
		t.Run(from, func(t *testing.T) {
			h := newHarness(t)
			ctx := t.Context()
			h.setupOwner(ctx)
			if from == "another_issuer" {
				h.savedProvider(rec2, builtProvider(t, rec2, h.origin), false)
			}
			owner := h.owner()
			h.fixture(func(tx *txView) error {
				u, _, _ := tx.UserByID(ctx, owner.ID)
				u.PasswordHash = access.Hash{}
				u.OIDC = access.OIDCIdentity{Issuer: rec.Issuer, Subject: "owner-sub"}
				return tx.UpdateUser(ctx, u)
			})
			h.setPolicy(func(p *storePolicy) { p.Methods = policyMethods{OIDC: true} })
			candidate := h.store.policy()
			candidate.Provider = rec
			built := builtProvider(t, rec, h.origin)
			if _, _, err := h.store.putPolicy(ctx, h.svc, &accountTestActor, h.origin.RPID(), candidate,
				candidate.Generation, &providerEdit{record: rec, built: built}); err != nil {
				t.Fatalf("saving the linked issuer's provider = %v, want nil: the link works through it", err)
			}
			if h.store.OIDCProvider() != built || h.store.policy().Provider.Issuer != rec.Issuer {
				t.Error("the applied save is not the published provider")
			}
		})
	}
}

// TestGuard_PasskeyUnderAnotherRPIDCountsForNothing: a passkey enrolled
// under marotte.example, then a boot with a degraded origin and one with another RP ID.
func TestGuard_PasskeyUnderAnotherRPIDCountsForNothing(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	h.setupOwner(ctx)
	h.enrolPasskey(h.principal(h.signIn(ctx).Token), accesstest.NewPasskeyDevice(t))
	for _, origin := range []string{"https://192.0.2.5", "https://other.example"} {
		t.Run(strings.TrimPrefix(origin, "https://"), func(t *testing.T) {
			b := h.reopen(withOrigin(origin))
			h = b
			p := b.principal(b.signIn(ctx).Token)
			if _, err := b.svc.RemovePassword(plainCtx(ctx), testClient, p, access.NewSecret(testPassword)); !errors.Is(err, account.ErrLastMethod) {
				t.Errorf("removing the password = %v, want ErrLastMethod", err)
			}
			base := b.store.policy()
			if _, _, err := b.store.putPolicy(ctx, b.svc, &accountTestActor, b.origin.RPID(),
				allowanceOff(base, access.MethodPassword), base.Generation, nil); !errors.Is(err, ErrNoWorkingMethod) {
				t.Errorf("turning the password allowance off = %v, want ErrNoWorkingMethod", err)
			}
			if _, err := b.store.setPlainHTTP(ctx, b.svc, false); err != nil {
				t.Errorf("turning the plain-HTTP switch off = %v, want nil", err)
			}
			if _, err := b.svc.PasswordLogin(httpsCtx(ctx), testClient, account.Credentials{
				Username: testUsername, Password: access.NewSecret(testPassword),
			}); err != nil {
				t.Errorf("password sign-in over HTTPS after the switch-off = %v", err)
			}
			b.setPolicy(func(p *storePolicy) { p.PlainHTTP = true })
		})
	}
}

const recoveredPassword = "a brand new recovered passphrase 7"

// TestRestoreAllowance_PasswordRepairTurnsPasswordOn: the shell's
// recovery with Password off turns it on in the same publication, across an abrupt restart.
func TestRestoreAllowance_PasswordRepairTurnsPasswordOn(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	h.setupOwner(ctx)
	h.setPolicy(func(p *storePolicy) { p.Methods.Password = false })
	gen := h.store.policy().Generation
	publishes := h.disk.publishes.Load()
	err := h.svc.RecoverOutOfBand(WithRestoreAllowance(plainCtx(ctx), access.MethodPassword), account.OutOfBand("admin-socket"),
		h.owner().ID, access.NewSecret(recoveredPassword), account.RecoverOptions{})
	if err != nil {
		t.Fatalf("RecoverOutOfBand: %v", err)
	}
	if p := h.store.policy(); !p.Methods.Password || p.Generation != gen+1 {
		t.Errorf("after the repair Password %t at generation %d, want on at %d", p.Methods.Password, p.Generation, gen+1)
	}
	if n := h.disk.publishes.Load() - publishes; n != 1 {
		t.Errorf("the repair published %d times, want once: the allowance rides the credential's publication", n)
	}
	for _, b := range []*harness{h, h.crashCopy()} {
		if !b.store.policy().Methods.Password {
			t.Error("Password is off")
		}
		if _, err := b.svc.PasswordLogin(httpsCtx(ctx), testClient, account.Credentials{
			Username: testUsername, Password: access.NewSecret(recoveredPassword),
		}); err != nil {
			t.Errorf("the recovered password does not sign in: %v", err)
		}
	}
}

// TestRestoreAllowance_RefusedPasswordChangesNothing: the arms that add nothing, which are a
// password the rules refuse, a marked Update whose callback fails after writing the hash, and a
// marked Update that writes no credential.
func TestRestoreAllowance_RefusedPasswordChangesNothing(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	h.setupOwner(ctx)
	h.setPolicy(func(p *storePolicy) { p.Methods.Password = false })
	owner := h.owner()
	gen := h.store.policy().Generation
	marked := WithRestoreAllowance(plainCtx(ctx), access.MethodPassword)
	t.Run("refused_password", func(t *testing.T) {
		err := h.svc.RecoverOutOfBand(marked, account.OutOfBand("admin-socket"), owner.ID, access.NewSecret("short"), account.RecoverOptions{})
		if err == nil {
			t.Fatal("a too-short password was accepted")
		}
	})
	t.Run("callback_fails_after_writing", func(t *testing.T) {
		err := h.store.Update(marked, func(tx account.Tx) error {
			u, _, _ := tx.UserByID(ctx, owner.ID)
			u.PasswordHash = access.Hash{}
			if err := tx.UpdateUser(ctx, u); err != nil {
				return err
			}
			u.PasswordHash = owner.PasswordHash
			if err := tx.UpdateUser(ctx, u); err != nil {
				return err
			}
			return errA
		})
		if !errors.Is(err, errA) {
			t.Fatalf("= %v, want the callback's error", err)
		}
	})
	t.Run("no_credential_written", func(t *testing.T) {
		if err := h.store.CreateSession(marked, newTestSession(owner.ID, epoch)); err != nil {
			t.Fatal(err)
		}
	})
	if p := h.store.policy(); p.Methods.Password || p.Generation != gen {
		t.Errorf("Password %t at generation %d, want off at %d", p.Methods.Password, p.Generation, gen)
	}
	if !h.owner().PasswordHash.Equal(owner.PasswordHash) {
		t.Error("the password hash changed")
	}
}

// TestRestoreAllowance_ExhaustedGenerationRefusesTheRepair: at the last policy generation the
// repair fails whole, its password included, rather than publish a generation decode refuses.
func TestRestoreAllowance_ExhaustedGenerationRefusesTheRepair(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	h.setupOwner(ctx)
	h.fixture(func(tx *txView) error {
		p := tx.s.policy.clone()
		p.Methods.Password, p.Generation = false, maxID
		tx.setPolicy(&p)
		return nil
	})
	owner := h.owner()
	err := h.svc.RecoverOutOfBand(WithRestoreAllowance(plainCtx(ctx), access.MethodPassword), account.OutOfBand("admin-socket"),
		owner.ID, access.NewSecret(recoveredPassword), account.RecoverOptions{})
	if !errors.Is(err, errIDsExhausted) {
		t.Fatalf("RecoverOutOfBand = %v, want errIDsExhausted", err)
	}
	for _, b := range []*harness{h, h.crashCopy()} {
		if p := b.store.policy(); p.Methods.Password || p.Generation != maxID {
			t.Errorf("Password %t at generation %d, want off at %d", p.Methods.Password, p.Generation, maxID)
		}
		if !b.owner().PasswordHash.Equal(owner.PasswordHash) {
			t.Error("the password hash changed")
		}
	}
}

// TestRestoreAllowance_RecoveryPasskeyTurnsPasskeyOn: with
// Passkey off, a browser recovery that enrols a passkey turns it on, and the passkey signs in,
// across an abrupt restart.
func TestRestoreAllowance_RecoveryPasskeyTurnsPasskeyOn(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	h.setupOwner(ctx)
	h.setPolicy(func(p *storePolicy) { p.Methods.Passkey = false })
	gen := h.store.policy().Generation
	tok, err := h.svc.IssueRecovery(ctx, account.OutOfBand("admin-socket"), h.owner().ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := h.svc.StartRecovery(httpsCtx(ctx), testClient, tok.Plaintext)
	if err != nil {
		t.Fatal(err)
	}
	p := h.recoveryPrincipal(rec.Issued.Token)
	dev := accesstest.NewPasskeyDevice(t)
	cer, err := h.svc.RecoverPasskeyBegin(ctx, p, webOrigin(t))
	if err != nil {
		t.Fatal(err)
	}
	err = h.svc.RecoverPasskeyFinish(WithRestoreAllowance(httpsCtx(ctx), access.MethodPasskey), p, cer.ID,
		bytes.NewReader(dev.Register(mustJSON(t, cer.Creation))), account.RecoverOptions{})
	if err != nil {
		t.Fatalf("RecoverPasskeyFinish: %v", err)
	}
	if pol := h.store.policy(); !pol.Methods.Passkey || pol.Generation != gen+1 {
		t.Errorf("after the recovery Passkey %t at generation %d, want on at %d", pol.Methods.Passkey, pol.Generation, gen+1)
	}
	for _, b := range []*harness{h, h.crashCopy()} {
		if !b.store.policy().Methods.Passkey {
			t.Error("Passkey is off")
		}
		if err := b.passkeySignIn(dev); err != nil {
			t.Errorf("the recovered passkey does not sign in: %v", err)
		}
	}
}

// clientAt is testClient from the IPv4 address 10.a.b.c for n, so each attempt's IP has its own
// limiter window.
func clientAt(n int) account.Client {
	c := testClient
	ip := netip.AddrFrom4([4]byte{10, byte(n >> 16), byte(n >> 8), byte(n)})
	c.ClientAddr = access.ClientAddr{IP: ip, Peer: ip}
	return c
}

func (h *harness) failedLogin(ctx context.Context, c account.Client, username, password string) error {
	_, err := h.svc.PasswordLogin(plainCtx(ctx), c, account.Credentials{Username: username, Password: access.NewSecret(password)})
	return err
}

// failureCount reads one failure record of canonical's name.
func (h *harness) failureCount(canonical string, trusted bool) (account.FailureCount, bool) {
	ref := account.FailureRef{Name: h.failureName(canonical), Method: access.MethodPassword, Trusted: trusted}
	f, found, err := h.store.AuthFailure(context.Background(), ref)
	if err != nil {
		h.t.Fatal(err)
	}
	return f, found
}

// TestFailures_EqualWork: every failure arm runs one Update
// holding one UserByUsername, one AuthFailure and one PutAuthFailure.
func TestFailures_EqualWork(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	h.setupOwner(ctx)
	owner := h.owner()
	var mu sync.Mutex
	var ops []string
	rec := func(op string) { mu.Lock(); ops = append(ops, op); mu.Unlock() }
	want := []string{"Update", "UserByUsername", "AuthFailure", "PutAuthFailure"}
	arms := []struct {
		setup              func()
		name, user, secret string
	}{
		{name: "unknown_user", user: "nobody", secret: testPassword},
		{name: "wrong_password", user: testUsername, secret: "not the password at all"},
		{name: "disabled_user", user: testUsername, secret: testPassword, setup: func() {
			h.fixture(func(tx *txView) error {
				u, _, _ := tx.UserByID(ctx, owner.ID)
				u.Enabled = false
				return tx.UpdateUser(ctx, u)
			})
		}},
		{name: "locked_both_contexts", user: "locked-name", secret: testPassword, setup: func() {
			name := h.failureName("locked-name")
			h.fixture(func(tx *txView) error {
				for _, trusted := range []bool{false, true} {
					ref := account.FailureRef{Name: name, Method: access.MethodPassword, Trusted: trusted}
					if err := tx.PutAuthFailure(ctx, account.FailureCount{FailureRef: ref, Count: account.FailureCap, Locked: true, LockedAt: epoch}); err != nil {
						return err
					}
				}
				return nil
			})
		}},
	}
	for i, arm := range arms {
		t.Run(arm.name, func(t *testing.T) {
			if arm.setup != nil {
				arm.setup()
			}
			ops = nil
			updateHook, txTrace = func() { rec("Update") }, rec
			err := h.failedLogin(ctx, clientAt(i), arm.user, arm.secret)
			updateHook, txTrace = nil, nil
			if !errors.Is(err, account.ErrInvalidCredentials) {
				t.Errorf("PasswordLogin = %v, want ErrInvalidCredentials", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if !slices.Equal(ops, want) {
				t.Errorf("the attempt made %v, want %v", ops, want)
			}
		})
	}
}

// TestFailures_KillAfterCountStaysCounted: a process killed after the count's Update returned
// and before the hash finished reopens with the attempt counted.
func TestFailures_KillAfterCountStaysCounted(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	h.setupOwner(ctx)
	slow := access.DefaultArgon2Params()
	slow.Iterations = 40
	budget, err := access.NewHashBudget(access.HashCost(slow))
	if err != nil {
		t.Fatal(err)
	}
	hasher, err := access.NewHasher(access.HasherConfig{Params: slow, Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := hasher.Hash(ctx, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	h.fixture(func(tx *txView) error {
		u, _, _ := tx.UserByUsername(ctx, testUsername)
		u.PasswordHash = hash
		return tx.UpdateUser(ctx, u)
	})
	done := make(chan error, 1)
	go func() { done <- h.failedLogin(ctx, clientAt(1), testUsername, "a wrong password guess") }()
	name := h.failureName(testUsername)
	var crash *harness
	deadline := time.Now().Add(5 * time.Second)
	for crash == nil {
		snap, err := decodeFile(h.disk.path)
		if err == nil && snap.failures[account.FailureRef{Name: name, Method: access.MethodPassword}].Count == 1 {
			select {
			case <-done:
				t.Fatal("the attempt finished before the copy; the hash is too fast to place the kill")
			default:
			}
			crash = h.crashCopy()
		}
		if time.Now().After(deadline) {
			t.Fatal("the count never reached the file")
		}
		time.Sleep(time.Millisecond)
	}
	if f, found := crash.failureCount(testUsername, false); !found || f.Count != 1 {
		t.Errorf("after the kill the record is (%+v, found %t), want Count 1", f, found)
	}
	<-done
}

// TestFailures_UnknownNamesPruned: the sweep removes the failure records of names no user has
// and keeps the owner's.
func TestFailures_UnknownNamesPruned(t *testing.T) {
	cleanupInterval = 20 * time.Millisecond
	t.Cleanup(func() { cleanupInterval = 0 })
	h := newHarness(t)
	ctx := t.Context()
	h.setupOwner(ctx)
	_ = h.failedLogin(ctx, clientAt(1), testUsername, "wrong guess one")
	_ = h.failedLogin(ctx, clientAt(2), "stranger", "wrong guess two")
	waitUntil(t, "the unknown name's prune", func() bool {
		_, found := h.failureCount("stranger", false)
		return !found
	})
	if f, found := h.failureCount(testUsername, false); !found || f.Count != 1 {
		t.Errorf("the owner's record = (%+v, found %t), want kept at Count 1", f, found)
	}
}

// TestFailures_UntrustedAllowanceIs10000: spending account.MaxUntrustedCounts untrusted
// attempts wakes a prune, and an attempt past it is refused with a one-second Retry-After until
// the prune commits. The allowance counts admitted attempts, and an attempt whose count the disk
// refuses returns before its hash, so the first slots are spent with the count's write failing:
// 10,000 hashes and count commits would take minutes.
func TestFailures_UntrustedAllowanceIs10000(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	h.setupOwner(ctx)
	// Each attempt has its own name and IP, under the limiter's per-account and per-IP windows.
	attempt := func(n int) error {
		return h.failedLogin(ctx, clientAt(n), fmt.Sprintf("guess-%d", n), "wrong")
	}
	var failing atomic.Bool
	failing.Store(true)
	h.disk.setInject(func(op, path string) error {
		if failing.Load() && op == "stage" && path == h.disk.path {
			return errInjected
		}
		return nil
	})
	var wg sync.WaitGroup
	var unexpected atomic.Value
	var next atomic.Int64
	for range 8 {
		wg.Go(func() {
			for n := int(next.Add(1)); n < account.MaxUntrustedCounts; n = int(next.Add(1)) {
				if err := attempt(n); !errors.Is(err, access.ErrAuthBackend) {
					unexpected.CompareAndSwap(nil, fmt.Sprint(err))
				}
			}
		})
	}
	wg.Wait()
	failing.Store(false)
	if err := unexpected.Load(); err != nil {
		t.Fatalf("an attempt within the allowance = %v, want its count refused by the disk", err)
	}
	// The last slot's attempt queues its count behind a held group, so the prune it wakes cannot
	// commit while the next attempt is judged.
	entered, pending, release := h.store.holdGroup()
	held := make(chan error, 1)
	go func() { held <- h.store.CreateSession(plainCtx(context.Background()), newTestSession(999, time.Now())) }()
	<-entered
	last := make(chan error, 1)
	go func() { last <- attempt(account.MaxUntrustedCounts) }()
	waitUntil(t, "the last slot's count queued", func() bool { return pending() >= 2 })
	var refused *ratelimit.RefusedError
	n := account.MaxUntrustedCounts + 1
	if err := attempt(n); !errors.As(err, &refused) {
		release()
		t.Fatalf("the attempt past the allowance = %v, want a *ratelimit.RefusedError", err)
	}
	if refused.Dimension != "untrusted" || refused.RetryAfter != time.Second {
		t.Errorf("refusal = %+v, want the untrusted dimension with a 1 s Retry-After", refused)
	}
	release()
	if err := <-held; err != nil {
		t.Fatal(err)
	}
	if err := <-last; !errors.Is(err, account.ErrInvalidCredentials) {
		t.Errorf("the last slot's attempt = %v, want ErrInvalidCredentials", err)
	}
	waitUntil(t, "an attempt admitted after the prune", func() bool {
		n++
		return errors.Is(attempt(n), account.ErrInvalidCredentials)
	})
}

// TestFailures_LockReported: a stored lock the sweep has not reported fires one
// EventMethodLocked at the lock's own time, and the record is marked reported.
func TestFailures_LockReported(t *testing.T) {
	cleanupInterval = 20 * time.Millisecond
	t.Cleanup(func() { cleanupInterval = 0 })
	h := newHarness(t)
	ctx := t.Context()
	h.setupOwner(ctx)
	lockedAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	ref := account.FailureRef{Name: h.failureName(testUsername), Method: access.MethodPassword}
	h.fixture(func(tx *txView) error {
		return tx.PutAuthFailure(ctx, account.FailureCount{FailureRef: ref, Count: account.FailureCap, Locked: true, LockedAt: lockedAt, Unreported: true})
	})
	waitUntil(t, "EventMethodLocked", func() bool { return len(h.events.of(account.EventMethodLocked)) > 0 })
	evs := h.events.of(account.EventMethodLocked)
	if len(evs) != 1 || evs[0].UserID != h.owner().ID || !evs[0].At.Equal(lockedAt) || evs[0].Method != access.MethodPassword {
		t.Errorf("events = %+v, want one for the owner's password at %s", evs, lockedAt)
	}
	waitUntil(t, "the record marked reported", func() bool {
		f, _ := h.failureCount(testUsername, false)
		return f.Locked && !f.Unreported
	})
}

func TestSeenSeq_CappedAtLastSeqSurvivesAReopen(t *testing.T) {
	reopen := func(t *testing.T, d *testDisk) *Store {
		t.Helper()
		s, reset, err := OpenStore(context.Background(), d.path, d)
		if err != nil {
			t.Fatalf("OpenStore = %v", err)
		}
		t.Cleanup(s.Close)
		if reset.Taken() {
			t.Fatal("the reopen took the Corrupt arm, want Valid")
		}
		return s
	}
	t.Run("two_entries", func(t *testing.T) {
		s, d := openTestStore(t)
		for range 2 {
			e := &account.LogEntry{Class: account.LogClassChange, At: epoch, Event: account.Event{Kind: "marotte.test"}}
			if err := s.AppendLog(t.Context(), e); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.setSeenSeq(t.Context(), 1000); err != nil {
			t.Fatal(err)
		}
		if got := s.seenSeq(); got != 2 {
			t.Errorf("seen = %d after raising it to 1000, want the last Seq 2", got)
		}
		s.Close()
		if got := reopen(t, d).seenSeq(); got != 2 {
			t.Errorf("seen after the reopen = %d, want 2", got)
		}
	})
	t.Run("empty_log", func(t *testing.T) {
		s, d := openTestStore(t)
		if err := s.setSeenSeq(t.Context(), 1000); err != nil {
			t.Fatal(err)
		}
		if got := s.seenSeq(); got != 0 {
			t.Errorf("seen = %d on an empty log, want 0", got)
		}
		s.Close()
		if got := reopen(t, d).seenSeq(); got != 0 {
			t.Errorf("seen after the reopen = %d, want 0", got)
		}
	})
}

func TestDecode_DuplicateUserClaims(t *testing.T) {
	for _, field := range []string{"username", "oidc", "webauthn_handle"} {
		t.Run(field, func(t *testing.T) {
			s, d := openTestStore(t)
			for i, name := range []string{"first", "second"} {
				u := &access.User{
					Username: name, Role: access.RoleAdmin, Enabled: true, CreatedAt: epoch,
					OIDC:           access.OIDCIdentity{Issuer: "https://idp.example.com", Subject: name},
					WebAuthnHandle: []byte{byte(i + 1), 2, 3, 4},
				}
				if err := s.CreateUser(t.Context(), u); err != nil {
					t.Fatal(err)
				}
			}
			s.Close()
			data, err := os.ReadFile(d.path)
			if err != nil {
				t.Fatal(err)
			}
			var f map[string]any
			if err := json.Unmarshal(data, &f); err != nil {
				t.Fatal(err)
			}
			users := f["users"].([]any)
			users[1].(map[string]any)[field] = users[0].(map[string]any)[field]
			out, err := json.Marshal(f)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(d.path, out, 0o600); err != nil {
				t.Fatal(err)
			}
			s2, reset, err := OpenStore(context.Background(), d.path, d)
			if err != nil {
				t.Fatalf("OpenStore = %v", err)
			}
			t.Cleanup(s2.Close)
			if !reset.Taken() {
				t.Errorf("two users sharing %s opened as Valid, want the Corrupt arm", field)
			}
		})
	}
}
