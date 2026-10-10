package push

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	mrand "math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/runesafe/v2"
	"golang.org/x/sync/errgroup"
)

// Send delivers n to all subscribers, debounced per kind AND subject. The payload is n
// itself, so the service worker shows what the page would. preflightSend's nil means do
// not send; a non-nil empty slice has stamped the debounce.
func (s *Service) Send(ctx context.Context, sent *marotte.NotificationPayload) {
	n := *sent
	slog.Debug("push: send", "kind", string(n.Kind))
	// Trim against the MARSHALED size: the JSON envelope and escaping count toward pushBodyCap,
	// and push() drops an over-cap payload rather than truncating it.
	if fit, truncated := fitToCap(&n); truncated {
		slog.Warn("push: payload too large, truncating",
			"bytes", len(n.Title)+len(n.Body), "cap", pushBodyCap)
		n = fit
	}
	subs := s.preflightSend(n.Kind, n.PushSubject)
	if subs == nil {
		return
	}
	payload, err := json.Marshal(n)
	if err != nil {
		slog.Error("push: marshal payload", "error", err)
		return
	}
	s.fanOut(ctx, s.absent(subs, n.Kind, n.PushSubject, payload), payload, n.Kind)
}

// absent is the send filter: subscriptions whose profile is not receiving the event on a
// stream. It lives here, not in the coordinator, so the PR status poller is filtered too.
// A subscription with no presence row is sent: a tag mismatch costs one notification too
// many, never one too few.
func (s *Service) absent(
	subs []marotte.PushSubscription, kind marotte.PushKind, subject marotte.PushSubject, payload []byte,
) []marotte.PushSubscription {
	if s.presence == nil {
		return subs
	}
	out := make([]marotte.PushSubscription, 0, len(subs))
	for _, sub := range subs {
		tag := tagOf(sub.Endpoint)
		if s.presence.Gone(tag) {
			out = append(out, sub)
			continue
		}
		if counter, ok := s.suppressed[kind]; ok {
			counter.Add(1)
		}
		slog.Debug("push: suppressed, profile present", "kind", string(kind), "tag", tag)
		s.holdForLater(sub, kind, subject, payload)
	}
	return out
}

func (s *Service) fanOut(
	ctx context.Context, subs []marotte.PushSubscription, payload []byte, kind marotte.PushKind,
) {
	var (
		mu      sync.Mutex
		outcome fanOut
	)
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(pushFanOutLimit)
	for _, sub := range subs {
		g.Go(func() error {
			d := s.deliver(ctx, sub, payload, kind)
			mu.Lock()
			outcome.record(d, sub.Endpoint)
			mu.Unlock()
			return nil // best-effort: never fail the group
		})
	}
	if err := g.Wait(); err != nil {
		slog.Error("push: fan-out wait", "error", err)
	}
	s.pruneStale(outcome.prunable())
}

// Suppressed reports how many subscriptions the send filter skipped for kind since start.
func (s *Service) Suppressed(kind marotte.PushKind) uint64 {
	if counter, ok := s.suppressed[kind]; ok {
		return counter.Load()
	}
	return 0
}

// PresenceRows is the presence table as the test-only probe reports it; empty
// when no table is wired.
func (s *Service) PresenceRows() []PresenceRow {
	if s.presence == nil {
		return []PresenceRow{}
	}
	return s.presence.snapshotRows()
}

// PresenceTransitions is the table's alive and expired counts; zero when no
// table is wired.
func (s *Service) PresenceTransitions() (alive, expired uint64) {
	if s.presence == nil {
		return 0, 0
	}
	return s.presence.transitions()
}

// fanOut is what one notification's deliveries came to (see prunable).
// record is called under the fan-out's mutex; prunable only after it joins.
type fanOut struct {
	gone         []string // 404/410: over, whatever this server's keys are
	authRejected []string // 401/403: either their key is stale or ours is wrong
	delivered    int
}

func (f *fanOut) record(d disposition, endpoint string) {
	switch d {
	case dispDelivered:
		f.delivered++
	case dispPrune:
		f.gone = append(f.gone, endpoint)
	case dispAuthReject:
		f.authRejected = append(f.authRejected, endpoint)
	case dispPermanent, dispRetry:
		// Keep. A malformed request, an oversize record and a service outage are
		// all reasons to fix something here, never to forget a subscriber.
	}
}

// It logs the verdict because only a re-subscribe undoes a prune. A 404/410 goes on its own answer.
// A 401/403 goes only when another subscriber accepted this notification: that proves this server's
// credentials work, while a server-side mistake refuses everyone and so deletes nothing.
func (f *fanOut) prunable() []string {
	switch {
	case len(f.authRejected) == 0:
		return f.gone
	case f.delivered == 0:
		slog.Error("push: every subscriber refused the VAPID authorization",
			"refused", len(f.authRejected), "hint", pushResubscribeHint)
		return f.gone
	default:
		slog.Warn("push: pruning subscriptions this server has no key for",
			"pruned", len(f.authRejected), "delivered", f.delivered,
			"hint", pushResubscribeHint)
		return append(f.gone, f.authRejected...)
	}
}

// RFC 8292 section 4.2 requires the USER AGENT to create the replacement, so nothing server-side
// repairs one.
const pushResubscribeHint = "re-subscribe from a browser tab; a subscription bound to a replaced VAPID key cannot be repaired server-side"

type disposition int

const (
	dispDelivered  disposition = iota // 2xx: the service accepted it
	dispPrune                         // the subscription is gone; forget it
	dispAuthReject                    // VAPID refused; whose key is wrong is not stated
	dispPermanent                     // retrying cannot help; this one is ours to fix
	dispRetry                         // transient; try again
)

// classify maps a push service's HTTP status onto a disposition.
//
//	2xx          accepted
//	400 413      malformed request, or a payload over the vendor's record limit
//	401 403      VAPID authorization refused (RFC 8292 section 4.2)
//	404 410      subscription expired or unsubscribed (RFC 8030 section 7.3)
//	429 5xx      rate limited (honour Retry-After) or a service outage
func classify(code int) disposition {
	switch {
	case code >= 200 && code < 300:
		return dispDelivered
	case code == http.StatusNotFound, code == http.StatusGone:
		return dispPrune
	case code == http.StatusUnauthorized, code == http.StatusForbidden:
		return dispAuthReject
	case code == http.StatusTooManyRequests:
		return dispRetry
	case code >= 500:
		return dispRetry
	default:
		return dispPermanent
	}
}

// deliver sends one payload to one subscriber, retrying the retryable, and reports the
// disposition it ended on (a give-up reports dispRetry: keep). Log lines carry the
// subscription's tag, never its endpoint, which is a capability URL.
func (s *Service) deliver(
	ctx context.Context,
	sub marotte.PushSubscription,
	payload []byte,
	kind marotte.PushKind,
) disposition {
	tag := tagOf(sub.Endpoint)
	deadline := time.Now().Add(pushRetryBudget)
	backoff := pushRetryBase

	for attempt := 1; ; attempt++ {
		code, retryAfter, err := s.push(ctx, sub, payload, kind)
		if err != nil {
			// A transport failure has no status: treat it as transient and let the budget decide.
			slog.Warn("push: send failed", "tag", tag, "code", code,
				"attempt", attempt, "error", withoutEndpoint(err))
			if !s.waitRetry(ctx, attempt, deadline, 0, &backoff, tag) {
				return dispRetry
			}
			continue
		}

		switch classify(code) {
		case dispDelivered:
			slog.Info("push: delivered", "kind", string(kind), "tag", tag,
				"code", code, "attempts", attempt)
			return dispDelivered
		case dispPrune:
			slog.Info("push: subscription invalidated", "tag", tag, "code", code)
			return dispPrune
		case dispAuthReject:
			slog.Warn("push: subscription refused as unauthorized", "tag", tag,
				"code", code)
			return dispAuthReject
		case dispPermanent:
			slog.Error("push: permanent delivery failure", "tag", tag,
				"code", code, "hint", permanentHint(code))
			return dispPermanent
		case dispRetry:
			slog.Warn("push: retryable status", "tag", tag, "code", code,
				"attempt", attempt, "retry_after", retryAfter)
			if !s.waitRetry(ctx, attempt, deadline, retryAfter, &backoff, tag) {
				return dispRetry
			}
		}
	}
}

// withoutEndpoint strips the request URL a *url.Error carries, so a transport
// failure's log line names the failure and not the capability URL it was
// addressed to. Any other error is returned as is.
func withoutEndpoint(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}

// waitRetry sleeps before the next attempt and reports whether to make one: no once the
// attempt cap or budget is spent, or a Retry-After lands past the notification's usefulness.
// Full jitter keeps a recovering vendor from receiving every subscriber's retry at once.
func (*Service) waitRetry(
	ctx context.Context,
	attempt int,
	deadline time.Time,
	retryAfter time.Duration,
	backoff *time.Duration,
	tag string,
) bool {
	if attempt >= pushMaxAttempts {
		slog.Warn("push: giving up, attempts exhausted",
			"tag", tag, "attempts", attempt)
		return false
	}
	//nolint:gosec // G404: retry jitter, not a secret — an attacker who could
	// predict it would learn when a push retry fires and nothing else.
	wait := time.Duration(mrand.Int64N(int64(*backoff)))
	if retryAfter > 0 {
		wait = retryAfter
	}
	*backoff *= 2
	if time.Now().Add(wait).After(deadline) {
		slog.Warn("push: giving up, retry would land past the notification's usefulness",
			"tag", tag, "wait", wait, "budget", pushRetryBudget)
		return false
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// Each is marotte's bug. Authorization refusals have their own disposition, because whose key is
// wrong decides pruning.
func permanentHint(code int) string {
	switch code {
	case http.StatusBadRequest:
		return "malformed push request or headers"
	case http.StatusRequestEntityTooLarge:
		return "payload over the vendor record limit; pushBodyCap is too high"
	default:
		return "unexpected status, not retryable"
	}
}

// parseRetryAfter reads a Retry-After header in either legal form: a
// delay in seconds, or an HTTP-date. An absent, malformed or past value
// yields 0, which leaves the caller on its own backoff schedule.
func parseRetryAfter(h string) time.Duration {
	if h == "" {
		return 0
	}
	if secs, err := strconv.Atoi(h); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if when, err := http.ParseTime(h); err == nil {
		if d := time.Until(when); d > 0 {
			return d
		}
	}
	return 0
}

// preflightSend evaluates every pre-send gate and stamps the debounce under one mu hold
// (closing the decide/record TOCTOU), and returns the subscriber snapshot, or nil to drop.
func (s *Service) preflightSend(notifyType marotte.PushKind, subject marotte.PushSubject) []marotte.PushSubscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.healthy {
		return nil
	}
	if !notifyType.Valid() {
		slog.Warn("push: unknown notification kind", "kind", string(notifyType))
		return nil
	}
	enabled, known := s.prefs[notifyType]
	if !known {
		return nil
	}
	if !enabled {
		return nil
	}
	key := debounceKey(notifyType, subject)
	if last, ok := s.lastPush[key]; ok && time.Since(last) < pushDebounce {
		slog.Debug("push: debounced", "kind", string(notifyType), "subject", key.subject)
		return nil
	}
	s.pruneDebounceLocked()
	s.lastPush[key] = time.Now()
	subs := make([]marotte.PushSubscription, 0, len(s.subs))
	for _, sub := range s.subs {
		subs = append(subs, sub)
	}
	return subs
}

// pruneDebounceLocked drops entries whose window has expired. Caller holds mu.
// An expired entry cannot suppress anything, so this only bounds map growth.
func (s *Service) pruneDebounceLocked() {
	if len(s.lastPush) < debounceHighWater {
		return
	}
	for k, at := range s.lastPush {
		if time.Since(at) >= pushDebounce {
			delete(s.lastPush, k)
		}
	}
}

// No-op on empty.
func (s *Service) pruneStale(stale []string) {
	if len(stale) == 0 {
		return
	}
	s.mu.Lock()
	for _, ep := range stale {
		delete(s.subs, ep)
	}
	s.mu.Unlock()
	s.saveSubs(s.lifetime)
}

// RFC 8030 section 5.3 urgencies. Only these two are used: nothing marotte
// sends is an advertisement or a topic update, which are what the other two
// rungs describe.
const (
	urgencyNormal = "normal"
	urgencyHigh   = "high"
)

// urgencyFor maps a kind onto its RFC 8030 section 5.3 urgency. A permission ask blocks
// the turn ("time-sensitive alert"); everything else is "chat or calendar message".
func urgencyFor(kind marotte.PushKind) string {
	if kind == marotte.PushKindPermission {
		return urgencyHigh
	}
	return urgencyNormal
}

// RFC 8030 section 5.2 TTLs: how long a push service may hold a notification for an
// OFFLINE device. Not pushRetryBudget (this server's retrying); do not derive one from the
// other. Each value is how long the notification is still worth showing.
const (
	// The dock replays an unanswered permission ask on reconnect, so this buys only promptness.
	ttlPermission = 10 * time.Minute
	// A finished turn is moot an hour later: by then the reader has either come back
	// to the chat or stopped waiting.
	ttlAgentFinished = time.Hour
	// A pull request's verdict does not expire — it is still true tomorrow — so
	// this is the one kind worth delivering to a device that was away all day.
	ttlPRStatus = 24 * time.Hour
)

// An unknown kind (the wire grew one this build lacks) takes the longest window: delivering late
// beats dropping it.
func ttlFor(kind marotte.PushKind) string {
	return ttlSeconds(ttlDuration(kind))
}

// ttlDuration is the window ttlFor renders, as a duration: how long a
// notification of this kind is still worth showing.
func ttlDuration(kind marotte.PushKind) time.Duration {
	switch kind {
	case marotte.PushKindPermission:
		return ttlPermission
	case marotte.PushKindAgentFinished:
		return ttlAgentFinished
	default:
		return ttlPRStatus
	}
}

func ttlSeconds(d time.Duration) string {
	return strconv.FormatInt(int64(d/time.Second), 10)
}

// push performs RFC 8291 encryption and delivers the payload to a
// single subscriber endpoint via HTTP POST with VAPID authentication.
// It returns the status code and, for a rate-limited or unavailable
// service, the Retry-After delay it asked for (0 when absent).
func (s *Service) push(
	ctx context.Context,
	sub marotte.PushSubscription,
	payload []byte,
	kind marotte.PushKind,
) (int, time.Duration, error) {
	// Bound the payload before any allocation; this is what bounds encryptPayload's
	// len(payload)+1. pushBodyCap is the pre-pad ceiling under the spec's 4096-byte record.
	if len(payload) > pushBodyCap {
		return 0, 0, fmt.Errorf("payload too large: %d bytes (max %d)", len(payload), pushBodyCap)
	}
	body, err := encryptPayload(sub, payload)
	if err != nil {
		return 0, 0, err
	}
	vapidAuth, err := s.vapidHeader(sub.Endpoint)
	if err != nil {
		return 0, 0, err
	}

	// Merge with s.lifetime UNCONDITIONALLY: merging only when it is already cancelled leaves
	// a send that started healthy blind to a later Close.
	reqCtx, mergeCleanup := mergeCtx(ctx, s.lifetime)
	defer mergeCleanup()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Authorization", vapidAuth)
	req.Header.Set("TTL", ttlFor(kind))
	req.Header.Set("Urgency", urgencyFor(kind))
	req.ContentLength = int64(len(body))

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	// Drain (capped: the body is untrusted) and close so keep-alive can reuse the connection.
	if _, copyErr := io.Copy(io.Discard, io.LimitReader(resp.Body, pushResponseCap)); copyErr != nil {
		slog.Debug("push: drain response body", "error", copyErr)
	}
	_ = resp.Body.Close()
	return resp.StatusCode, parseRetryAfter(resp.Header.Get("Retry-After")), nil
}

// encryptPayload performs RFC 8291 (aes128gcm) content encryption and returns the wire
// body: salt(16) || rs(4) || idlen(1) || ephemeralPublicKey || ciphertext.
// The caller bounds len(payload) to pushBodyCap.
func encryptPayload(sub marotte.PushSubscription, payload []byte) ([]byte, error) {
	clientPubBytes, err := base64.RawURLEncoding.DecodeString(sub.Keys.P256dh)
	if err != nil {
		return nil, fmt.Errorf("decode p256dh: %w", err)
	}
	authSecret, err := base64.RawURLEncoding.DecodeString(sub.Keys.Auth)
	if err != nil {
		return nil, fmt.Errorf("decode auth: %w", err)
	}
	clientPub, err := ecdh.P256().NewPublicKey(clientPubBytes)
	if err != nil {
		return nil, fmt.Errorf("import client key: %w", err)
	}
	ephPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	shared, err := ephPriv.ECDH(clientPub)
	if err != nil {
		return nil, err
	}

	salt := make([]byte, 16)
	if _, saltErr := rand.Read(salt); saltErr != nil {
		return nil, saltErr
	}
	cek, nonce, err := deriveKeyNonce(keyMaterial{
		Shared:     shared,
		AuthSecret: authSecret,
		ClientPub:  clientPubBytes,
		ServerPub:  ephPriv.PublicKey().Bytes(),
		Salt:       salt,
	})
	if err != nil {
		return nil, err
	}

	// RFC 8188 §2.1 single-record plaintext: the payload followed by the 0x02 padding
	// delimiter, NOT the obsolete "aesgcm" draft's 2-byte zero prefix. A conformant
	// browser silently DISCARDS an aes128gcm record whose delimiter octet is not 0x02.
	padded := make([]byte, len(payload)+1)
	copy(padded, payload)
	padded[len(payload)] = 0x02

	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ciphertext := gcm.Seal(nil, nonce, padded, nil)

	ephPubBytes := ephPriv.PublicKey().Bytes()
	body := make([]byte, 0, 16+4+1+len(ephPubBytes)+len(ciphertext))
	body = append(body, salt...)
	body = binary.BigEndian.AppendUint32(body, 4096)
	body = append(body, byte(len(ephPubBytes))) //nolint:gosec // G115: value bounded by protocol
	body = append(body, ephPubBytes...)
	body = append(body, ciphertext...)
	return body, nil
}

// pushTruncMarker marks a trimmed title or body. runesafe's Capped pair charges
// it INSIDE the byte cap, so a trimmed field's total — marker included — never
// exceeds the budget fitToCap hands it.
const pushTruncMarker = "..."

// fitToCap trims the body, then (only if an empty body still overflows) the title, until
// the marshaled payload fits pushBodyCap, and reports whether it trimmed. runesafe's
// Capped pair never splits a rune and charges the marker inside the cap.
func fitToCap(in *marotte.NotificationPayload) (fit marotte.NotificationPayload, truncated bool) {
	n := *in
	if marshaledLen(&n) <= pushBodyCap {
		return n, false
	}
	for marshaledLen(&n) > pushBodyCap {
		over := marshaledLen(&n) - pushBodyCap
		switch {
		case len(n.Body) > over:
			n.Body, _ = runesafe.SanitizeCapped(n.Body, len(n.Body)-over, pushTruncMarker)
		case n.Body != "":
			n.Body = "" // too small to absorb the overflow; drop it
		case len(n.Title) > over:
			n.Title, _ = runesafe.SanitizeSingleLineCapped(n.Title, len(n.Title)-over, pushTruncMarker)
		default:
			n.Title = "" // pathological: cap below the JSON envelope size
		}
	}
	return n, true
}

// Marshaling cannot fail here.
func marshaledLen(n *marotte.NotificationPayload) int {
	p, _ := json.Marshal(n)
	return len(p)
}

func mergeCtx(primary, secondary context.Context) (ctx context.Context, cleanup func()) {
	ctx, cancel := context.WithCancel(primary)
	stop := context.AfterFunc(secondary, func() { cancel() })
	cleanup = func() {
		stop()
		cancel()
	}
	return ctx, cleanup
}
