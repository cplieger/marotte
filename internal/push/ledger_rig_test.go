package push

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/liveness"
	"github.com/cplieger/marotte/internal/marotte"
)

const (
	epA = "https://fcm.googleapis.com/fcm/send/profile-a"
	epB = "https://fcm.googleapis.com/fcm/send/profile-b"
)

// delivered is one push as the subscriber decrypted it.
type delivered struct {
	path    string
	payload marotte.NotificationPayload
}

// decryptingHandler is a push service that decrypts each delivery with the subscriber's keys (RFC 8291), so a test
// reads the notice a device would show.
type decryptingHandler struct {
	keys map[string]subscriberKeys
	got  []delivered
	mu   sync.Mutex
}

type subscriberKeys struct {
	priv *ecdh.PrivateKey
	auth []byte
}

func (h *decryptingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	h.mu.Lock()
	defer h.mu.Unlock()
	k, ok := h.keys[r.URL.Path]
	if !ok || len(body) < 21 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	salt, idLen := body[:16], int(body[20])
	serverPub, ct := body[21:21+idLen], body[21+idLen:]
	eph, err := ecdh.P256().NewPublicKey(serverPub)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	shared, _ := k.priv.ECDH(eph)
	cek, nonce, err := deriveKeyNonce(keyMaterial{
		Shared: shared, AuthSecret: k.auth, ClientPub: k.priv.PublicKey().Bytes(), ServerPub: serverPub, Salt: salt,
	})
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil || len(plain) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var p marotte.NotificationPayload
	_ = json.Unmarshal(plain[:len(plain)-1], &p)
	h.got = append(h.got, delivered{path: r.URL.Path, payload: p})
	w.WriteHeader(http.StatusCreated)
}

// ledgerRig is a Service on the in-memory push server with a presence table on a fake clock, a beat shrunk to a
// millisecond, a subscription per endpoint named, and the notification frames it published.
type ledgerRig struct {
	s      *Service
	clock  *presenceClock
	h      *decryptingHandler
	frames []marotte.NotificationPayload
	mu     sync.Mutex
}

func newLedgerRig(t *testing.T, endpoints ...string) *ledgerRig {
	t.Helper()
	prevPoll := deferPoll
	deferPoll = time.Millisecond
	t.Cleanup(func() { deferPoll = prevPoll })
	r := &ledgerRig{
		clock: &presenceClock{at: time.Unix(1_700_000_000, 0)},
		h:     &decryptingHandler{keys: map[string]subscriberKeys{}},
	}
	srv := httptest.NewTestServer(t, r.h)
	r.s = New(context.Background(), t.TempDir(), testSubject, WithPresence(newPresenceAt(r.clock)))
	t.Cleanup(r.s.Close)
	r.s.client = srv.Client()
	r.s.SetFrames(func(_ marotte.ChatID, n marotte.NotificationPayload) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.frames = append(r.frames, n)
	})
	for _, ep := range endpoints {
		priv, err := ecdh.P256().GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("gen client key: %v", err)
		}
		auth := make([]byte, 16)
		_, _ = rand.Read(auth)
		sub := marotte.PushSubscription{Endpoint: ep}
		sub.Keys.P256dh = base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes())
		sub.Keys.Auth = base64.RawURLEncoding.EncodeToString(auth)
		r.h.keys[pathOf(ep)] = subscriberKeys{priv: priv, auth: auth}
		r.s.Subscribe(sub)
	}
	return r
}

func pathOf(endpoint string) string { return endpoint[len("https://fcm.googleapis.com"):] }

// present connects ep's profile.
func (r *ledgerRig) present(ep string) { r.s.presence.Observe(connected(tagOf(ep))) }

// view reports one page of ep's profile.
func (r *ledgerRig) view(ep string, v marotte.PageView) { r.s.View(tagOf(ep), v) }

// leave silences every profile past the alive window, so each reads gone at the next beat.
func (r *ledgerRig) leave() { r.clock.Advance(liveness.AliveWindow + time.Millisecond) }

// raise raises n about its chat at position and runs the push fan-out Raise returned.
func (r *ledgerRig) raise(t *testing.T, n *marotte.NotificationPayload, position uint64) {
	t.Helper()
	r.s.Raise(t.Context(), n.ChatID, n, position)(t.Context())
}

func (r *ledgerRig) framesNow() []marotte.NotificationPayload {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.frames)
}

func (r *ledgerRig) pushes() []delivered {
	r.h.mu.Lock()
	defer r.h.mu.Unlock()
	return slices.Clone(r.h.got)
}

// pushesTo is the pushes ep received, after enough beats for any due one to land.
func (r *ledgerRig) pushesTo(ep string) []marotte.NotificationPayload {
	time.Sleep(30 * deferPoll)
	var out []marotte.NotificationPayload
	for _, d := range r.pushes() {
		if d.path == pathOf(ep) {
			out = append(out, d.payload)
		}
	}
	return out
}

// awaitPushTo waits for ep's first push and answers every push ep received.
func (r *ledgerRig) awaitPushTo(t *testing.T, ep string) []marotte.NotificationPayload {
	t.Helper()
	waitFor(t, "a push to "+ep, func() bool {
		return slices.ContainsFunc(r.pushes(), func(d delivered) bool { return d.path == pathOf(ep) })
	})
	return r.pushesTo(ep)
}

// finished is an agent_finished notice about chatID.
func finished(chatID marotte.ChatID, title string) *marotte.NotificationPayload {
	return &marotte.NotificationPayload{
		PushSubject: marotte.ChatSubject(chatID), Kind: marotte.PushKindAgentFinished, Title: title, Body: "body",
	}
}

// askNotice is a permission ask's notice about chatID, announcing ask.
func askNotice(chatID marotte.ChatID, ask string) *marotte.NotificationPayload {
	return &marotte.NotificationPayload{
		PushSubject: marotte.ChatSubject(chatID), Kind: marotte.PushKindPermission, Title: "ask " + ask, Body: "body", Ask: ask,
	}
}

func chatTab(chatID string) *marotte.TabRef {
	return &marotte.TabRef{Kind: marotte.TabKindChat, Ref: chatID}
}
