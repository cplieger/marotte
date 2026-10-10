package push

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/slogx/capture"
)

// TestSendFailureLogCarriesTheTagNotTheEndpoint pins that the send-failed warn names the
// subscription by its tag and no byte of the endpoint (a capability URL) reaches the log.
// It runs in a synctest bubble so the PRODUCTION retry ladder costs no real time.
func TestSendFailureLogCarriesTheTagNotTheEndpoint(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := capture.Default(t)
		dir := t.TempDir()
		s := New(t.Context(), dir, "mailto:test@example.com")
		defer s.Close() // wait for writeLoop to drain before TempDir cleanup

		// Control bytes make the URL invalid, so the send fails with no network I/O, which keeps
		// the bubble's clock able to advance.
		hostile := "https://evil.example/\x1b]0;pwned\x07/" + strings.Repeat("x", 100)
		s.Subscribe(pushSubscriptionWithValidKeys(t, hostile))

		s.Send(t.Context(), &marotte.NotificationPayload{Kind: marotte.PushKindAgentFinished, Title: "t", Body: "b"})

		got, ok := rec.AttrValue("push: send failed", "tag")
		if !ok {
			t.Fatalf("no tag attr on the send-failed warn; logs = %q", rec.Messages())
		}
		if got != tagOf(hostile) {
			t.Errorf("tag attr = %q, want TagOf(endpoint) %q", got, tagOf(hostile))
		}
		if _, leaked := rec.AttrValue("push: send failed", "endpoint"); leaked {
			t.Error("the send-failed warn carries an endpoint attr; the tag is the only key")
		}
		for _, r := range rec.Records() {
			r.Attrs(func(a slog.Attr) bool {
				v := a.Value.String()
				if strings.Contains(v, "evil.example") || strings.ContainsAny(v, "\x1b\x07") {
					t.Errorf("log attr %s=%q carries endpoint bytes", a.Key, v)
				}
				return true
			})
		}
		if n := rec.CountExact("push: send failed"); n != pushMaxAttempts {
			t.Errorf("send-failed warns = %d, want pushMaxAttempts (%d)", n, pushMaxAttempts)
		}
	})
}

func TestSend_PreferenceFiltering(t *testing.T) {
	// The permission leg reaches the fan-out, so the client is in-memory and the
	// subscription carries real keys (see newServiceOnTestServer).
	rec := &recordingHandler{}
	s := newServiceOnTestServer(t, rec)
	// Subscribe so Send reaches the preflight stage rather than the empty-subs early exit.
	s.Subscribe(pushSubscriptionWithValidKeys(t, "https://fcm.googleapis.com/fcm/send/pref-test"))

	// With agentFinished disabled, preflight short-circuits before stamping last-push.
	s.SetPreferences(map[marotte.PushKind]bool{
		marotte.PushKindAgentFinished: false,
		marotte.PushKindPermission:    true,
	})
	s.Send(t.Context(), &marotte.NotificationPayload{Kind: marotte.PushKindAgentFinished, Title: "title", Body: "body"})
	s.mu.Lock()
	_, afRecorded := s.lastPush[debounceKey(marotte.PushKindAgentFinished, marotte.PushSubject{})]
	s.mu.Unlock()
	if afRecorded {
		t.Error("agentFinished=false should prevent Send from recording last-push timestamp")
	}

	// Permission has no settings key (see floor_test.go), so the reachable assertion is that
	// the ask gets through with the other kind switched off.
	s.SetPreferences(map[marotte.PushKind]bool{
		marotte.PushKindAgentFinished: false,
		marotte.PushKindPermission:    true,
	})
	s.Send(t.Context(), &marotte.NotificationPayload{Kind: marotte.PushKindPermission, Title: "title", Body: "body"})
	s.mu.Lock()
	_, pnRecorded := s.lastPush[debounceKey(marotte.PushKindPermission, marotte.PushSubject{})]
	s.mu.Unlock()
	if !pnRecorded {
		t.Error("permission push must reach the send path even with agent_finished off")
	}

	// Exactly one delivery, to the SUBSCRIPTION's own host and path, carrying the RFC 8291
	// content coding and the RFC 8292 VAPID header; the agent_finished leg contributes nothing.
	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("deliveries = %d, want 1 (only the permission send passes the gate)", len(got))
	}
	if got[0].host != "fcm.googleapis.com" || got[0].path != "/fcm/send/pref-test" {
		t.Errorf("delivered to %s%s, want fcm.googleapis.com/fcm/send/pref-test",
			got[0].host, got[0].path)
	}
	if got[0].contentEncoding != "aes128gcm" {
		t.Errorf("Content-Encoding = %q, want aes128gcm", got[0].contentEncoding)
	}
	if !strings.HasPrefix(got[0].authorization, "vapid t=") {
		t.Errorf("Authorization = %q, want a vapid t=<jwt>, k=<key> header", got[0].authorization)
	}
}

func TestSend_Debounce(t *testing.T) {
	// In-memory client: otherwise a debounce regression would deliver over the real network.
	s := newServiceOnTestServer(t, &recordingHandler{})

	s.mu.Lock()
	s.lastPush[debounceKey(marotte.PushKindAgentFinished, marotte.PushSubject{})] = time.Now()
	s.mu.Unlock()

	s.Subscribe(marotte.PushSubscription{Endpoint: "https://push.example.com/debounce-test"})

	s.mu.Lock()
	before := s.lastPush[debounceKey(marotte.PushKindAgentFinished, marotte.PushSubject{})]
	s.mu.Unlock()

	s.Send(t.Context(), &marotte.NotificationPayload{Kind: marotte.PushKindAgentFinished, Title: "title", Body: "body"})

	s.mu.Lock()
	after := s.lastPush[debounceKey(marotte.PushKindAgentFinished, marotte.PushSubject{})]
	s.mu.Unlock()

	if !after.Equal(before) {
		t.Error("lastPush should not change when debounced")
	}
}

// TestSend_DebouncePerType pins that a recent agent_finished push does not suppress a
// permission push: the windows are keyed per kind.
func TestSend_DebouncePerType(t *testing.T) {
	rec := &recordingHandler{}
	s := newServiceOnTestServer(t, rec)

	s.mu.Lock()
	s.lastPush[debounceKey(marotte.PushKindAgentFinished, marotte.PushSubject{})] = time.Now()
	s.mu.Unlock()

	s.Subscribe(pushSubscriptionWithValidKeys(t, "https://push.example.com/x"))
	s.Send(t.Context(), &marotte.NotificationPayload{Kind: marotte.PushKindPermission, Title: "title", Body: "body"})

	s.mu.Lock()
	permTimestamp := s.lastPush[debounceKey(marotte.PushKindPermission, marotte.PushSubject{})]
	s.mu.Unlock()
	if permTimestamp.IsZero() {
		t.Error("permission push was suppressed by agent_finished debounce window")
	}
	if got := rec.snapshot(); len(got) != 1 {
		t.Errorf("deliveries = %d, want 1 (the permission send only)", len(got))
	}
}

// TestSend_UnknownKindRejected pins that an unknown kind is refused before any delivery
// and with no debounce side effect.
func TestSend_UnknownKindRejected(t *testing.T) {
	rec := &recordingHandler{}
	s := newServiceOnTestServer(t, rec)
	s.Subscribe(marotte.PushSubscription{Endpoint: "https://push.example.com/x"})
	s.Send(t.Context(), &marotte.NotificationPayload{Kind: "what-is-this", Title: "title", Body: "body"})
	if got := rec.snapshot(); len(got) != 0 {
		t.Errorf("an unknown kind attempted %d deliveries, want 0", len(got))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.lastPush[debounceKey("what-is-this", marotte.PushSubject{})]; ok {
		t.Error("unknown kind should not record a debounce entry")
	}
}

func TestSend_UnhealthySkips(t *testing.T) {
	dir := t.TempDir()
	s := New(t.Context(), dir, "mailto:test@example.com")
	s.mu.Lock()
	s.healthy = false
	s.mu.Unlock()

	s.Send(t.Context(), &marotte.NotificationPayload{Kind: marotte.PushKindAgentFinished, Title: "title", Body: "body"})
}

func TestSend_StatusCodePruning(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		wantPruned bool
	}{
		{"PrunesOn410Gone", http.StatusGone, true},
		{"PrunesOn404NotFound", http.StatusNotFound, true},
		{"KeepsOn201Created", http.StatusCreated, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}))

			dir := t.TempDir()
			s := New(t.Context(), dir, "mailto:test@example.com")
			defer s.Close() // wait for writeLoop to drain before TempDir cleanup
			s.client = srv.Client()
			s.Subscribe(pushSubscriptionWithValidKeys(t, srv.URL))

			s.Send(t.Context(), &marotte.NotificationPayload{Kind: marotte.PushKindAgentFinished, Title: "title", Body: "body"})

			if tt.wantPruned && s.HasSubscribers() {
				t.Errorf("Send did not prune subscription after %d", tt.status)
			}
			if !tt.wantPruned && !s.HasSubscribers() {
				t.Errorf("Send pruned subscription on %d", tt.status)
			}
		})
	}
}

// TestSend_AuthRejectionNeedsAWitnessBeforePruning pins the prune asymmetry: a 401/403 is
// pruned only beside another subscriber's delivery of the same notification. Cases:
// refused alone (keep), refused beside a delivery (prune), refused store-wide (keep all).
func TestSend_AuthRejectionNeedsAWitnessBeforePruning(t *testing.T) {
	const (
		witnessEP = "https://fcm.googleapis.com/fcm/send/witness"
		refusedEP = "https://fcm.googleapis.com/fcm/send/refused"
		refused2  = "https://fcm.googleapis.com/fcm/send/refused-two"

		wholeStoreMsg = "push: every subscriber refused the VAPID authorization"
		prunedMsg     = "push: pruning subscriptions this server has no key for"
	)

	// One handler answering by path, so a single fan-out gets two different answers.
	answerByPath := func(refusal int) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "refused") {
				w.WriteHeader(refusal)
				return
			}
			w.WriteHeader(http.StatusCreated)
		})
	}
	remaining := func(s *Service) []string {
		s.mu.Lock()
		defer s.mu.Unlock()
		return slices.Sorted(maps.Keys(s.subs))
	}

	t.Run("refused_with_no_witness_keeps_the_subscription", func(t *testing.T) {
		s := newServiceOnTestServer(t, answerByPath(http.StatusForbidden))
		s.Subscribe(pushSubscriptionWithValidKeys(t, refusedEP))

		capLog := capture.Default(t)
		s.Send(t.Context(), &marotte.NotificationPayload{Kind: marotte.PushKindAgentFinished, Title: "title", Body: "body"})

		if got := remaining(s); !slices.Equal(got, []string{refusedEP}) {
			t.Errorf("subs after a 403 with nothing delivered = %v, want the subscription kept", got)
		}
		if n := capLog.CountExact(wholeStoreMsg); n != 1 {
			t.Errorf("whole-store refusal logged %d times, want 1; logs = %q", n, capLog.Messages())
		}
		// The remedy must ride the line: only a person re-subscribing fixes this state.
		if !capLog.HasAttr(wholeStoreMsg, "hint", pushResubscribeHint) {
			t.Errorf("the whole-store refusal carried no re-subscribe remedy; logs = %q", capLog.Messages())
		}
		if n := capLog.CountExact(prunedMsg); n != 0 {
			t.Errorf("logged %d prune lines with nothing delivered, want 0", n)
		}
	})

	t.Run("a_delivery_licenses_pruning_the_refused_one", func(t *testing.T) {
		s := newServiceOnTestServer(t, answerByPath(http.StatusForbidden))
		s.Subscribe(pushSubscriptionWithValidKeys(t, witnessEP))
		s.Subscribe(pushSubscriptionWithValidKeys(t, refusedEP))

		capLog := capture.Default(t)
		s.Send(t.Context(), &marotte.NotificationPayload{Kind: marotte.PushKindAgentFinished, Title: "title", Body: "body"})

		if got := remaining(s); !slices.Equal(got, []string{witnessEP}) {
			t.Errorf("subs after one 201 and one 403 = %v, want only the delivering endpoint", got)
		}
		if n := capLog.CountExact(prunedMsg); n != 1 {
			t.Errorf("prune line logged %d times, want 1; logs = %q", n, capLog.Messages())
		}
		if n := capLog.CountExact(wholeStoreMsg); n != 0 {
			t.Errorf("claimed every subscriber refused while one delivered (%d lines)", n)
		}
	})

	t.Run("a_401_across_the_whole_store_deletes_nothing", func(t *testing.T) {
		s := newServiceOnTestServer(t, answerByPath(http.StatusUnauthorized))
		s.Subscribe(pushSubscriptionWithValidKeys(t, refusedEP))
		s.Subscribe(pushSubscriptionWithValidKeys(t, refused2))

		capLog := capture.Default(t)
		s.Send(t.Context(), &marotte.NotificationPayload{Kind: marotte.PushKindAgentFinished, Title: "title", Body: "body"})

		if got := remaining(s); !slices.Equal(got, []string{refusedEP, refused2}) {
			t.Errorf("subs after a store-wide 401 = %v, want both kept", got)
		}
		if n := capLog.CountExact(wholeStoreMsg); n != 1 {
			t.Errorf("whole-store refusal logged %d times, want 1; logs = %q", n, capLog.Messages())
		}
	})
}

type perKindHeaderRecorder struct {
	mu  sync.Mutex
	got []deliveryHeaders
}

// deliveryHeaders holds the two RFC 8030 headers a notification's KIND decides.
type deliveryHeaders struct {
	urgency string
	ttl     string
}

func (u *perKindHeaderRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	u.got = append(u.got, deliveryHeaders{
		urgency: r.Header.Get("Urgency"),
		ttl:     r.Header.Get("TTL"),
	})
	u.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
}

func (u *perKindHeaderRecorder) snapshot() []deliveryHeaders {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.got)
}

// The kind is switched on explicitly because pr_status defaults off.
func sendOneAndRecordHeaders(t *testing.T, kind marotte.PushKind) deliveryHeaders {
	t.Helper()
	rec := &perKindHeaderRecorder{}
	s := newServiceOnTestServer(t, rec)
	s.Subscribe(pushSubscriptionWithValidKeys(t, "https://fcm.googleapis.com/fcm/send/headers"))
	s.SetPreferences(map[marotte.PushKind]bool{kind: true})

	s.Send(t.Context(), &marotte.NotificationPayload{Kind: kind, Title: "title", Body: "body"})

	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("deliveries for %s = %d, want 1: this case has to reach the push service",
			kind, len(got))
	}
	return got[0]
}

// TestSend_SetsUrgencyPerKind pins the RFC 8030 section 5.3 urgency per kind, asserted
// through Send so the kind reaches the header as production sends it.
func TestSend_SetsUrgencyPerKind(t *testing.T) {
	cases := []struct {
		kind marotte.PushKind
		want string
	}{
		{marotte.PushKindAgentFinished, "normal"},
		{marotte.PushKindPRStatus, "normal"},
		{marotte.PushKindPermission, "high"},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			if got := sendOneAndRecordHeaders(t, tc.kind).urgency; got != tc.want {
				t.Errorf("Urgency for %s = %q, want %q", tc.kind, got, tc.want)
			}
		})
	}
}

// TestSend_SetsTTLPerKind pins the RFC 8030 section 5.2 TTL per kind. Wanted values are
// literal seconds: reusing ttlFor would assert the table against itself.
func TestSend_SetsTTLPerKind(t *testing.T) {
	cases := []struct {
		kind marotte.PushKind
		want string
	}{
		{marotte.PushKindPermission, "600"},
		{marotte.PushKindAgentFinished, "3600"},
		{marotte.PushKindPRStatus, "86400"},
		// A run outcome takes ttlFor's DEFAULT arm, so the default's coverage is asserted here.
		{marotte.PushKindRunOutcome, "86400"},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			if got := sendOneAndRecordHeaders(t, tc.kind).ttl; got != tc.want {
				t.Errorf("TTL for %s = %q seconds, want %q", tc.kind, got, tc.want)
			}
		})
	}
}

func TestSend_TruncatesOversizePayload(t *testing.T) {
	var receivedPayload []byte
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The payload is encrypted, so only the Send path's success is verifiable here.
		w.WriteHeader(http.StatusCreated)
	}))

	dir := t.TempDir()
	s := New(t.Context(), dir, "mailto:test@example.com")
	defer s.Close() // wait for writeLoop to drain before TempDir cleanup
	s.client = srv.Client()
	s.Subscribe(pushSubscriptionWithValidKeys(t, srv.URL))

	title := "Marotte"
	body := strings.Repeat("x", 4000)

	s.Send(t.Context(), &marotte.NotificationPayload{Kind: marotte.PushKindAgentFinished, Title: title, Body: body})

	if !s.HasSubscribers() {
		t.Error("subscriber was pruned after successful oversize send")
	}

	// After truncation the MARSHALED payload (envelope and escaping included) fits pushBodyCap;
	// sizing on raw title+body leaves the envelope over the cap and push() drops it.
	fit, truncated := fitToCap(&marotte.NotificationPayload{Title: title, Body: body})
	gotTitle, gotBody := fit.Title, fit.Body
	if !truncated {
		t.Fatalf("fitToCap reported no truncation for a %d-byte body", len(body))
	}
	if n := marshaledLen(&marotte.NotificationPayload{Title: gotTitle, Body: gotBody}); n > pushBodyCap {
		t.Errorf("marshaled payload = %d bytes, exceeds cap %d", n, pushBodyCap)
	}
	if !strings.HasSuffix(gotBody, "...") {
		t.Errorf("truncated body should end with '...', got suffix %q",
			gotBody[max(len(gotBody)-10, 0):])
	}

	_ = receivedPayload
}

// TestSend_OversizeTruncationWarn verifies the truncation warn fires exactly when the
// payload exceeds pushBodyCap.
func TestSend_OversizeTruncationWarn(t *testing.T) {
	const warnMsg = "push: payload too large, truncating"

	t.Run("small_does_not_warn", func(t *testing.T) {
		s := New(t.Context(), t.TempDir(), testSubject)
		defer s.Close()
		capLog := capture.Default(t)
		s.Send(t.Context(), &marotte.NotificationPayload{Kind: marotte.PushKindAgentFinished, Title: "aa", Body: "bb"})
		if capLog.CountExact(warnMsg) > 0 {
			t.Errorf("Send warned %q for a 4-byte payload; want no warn", warnMsg)
		}
	})

	t.Run("oversize_warns_with_total_bytes", func(t *testing.T) {
		s := New(t.Context(), t.TempDir(), testSubject)
		defer s.Close()
		capLog := capture.Default(t)
		s.Send(t.Context(), &marotte.NotificationPayload{Kind: marotte.PushKindAgentFinished, Title: strings.Repeat("a", 10), Body: strings.Repeat("b", 4000)})
		got, ok := capLog.AttrValue(warnMsg, "bytes")
		if !ok {
			t.Fatalf("Send did not warn %q for a 4010-byte payload", warnMsg)
		}
		if got != "4010" {
			t.Errorf("truncation warn bytes = %v, want 4010", got)
		}
	})

	t.Run("marshaled_at_cap_does_not_warn", func(t *testing.T) {
		// The title is sized so the envelope marshals to exactly pushBodyCap: not over, so no warn.
		s := New(t.Context(), t.TempDir(), testSubject)
		defer s.Close()
		capLog := capture.Default(t)
		n := marotte.NotificationPayload{Kind: marotte.PushKindAgentFinished, Body: strings.Repeat("b", 2000)}
		n.Title = strings.Repeat("a", pushBodyCap-marshaledLen(&n))
		if got := marshaledLen(&n); got != pushBodyCap {
			t.Fatalf("Setup: envelope = %d bytes, want exactly %d", got, pushBodyCap)
		}
		s.Send(t.Context(), &n)
		if capLog.CountExact(warnMsg) > 0 {
			t.Errorf("Send warned %q at exactly the marshaled cap; want no warn", warnMsg)
		}
	})
}

// TestPush_PayloadSizeBoundary pins push's payload-size guard as
// strictly-greater-than: a payload of exactly pushBodyCap bytes passes
// the guard (and fails later decoding p256dh), while pushBodyCap+1 is
// rejected as too large before any work.
func TestPush_PayloadSizeBoundary(t *testing.T) {
	s := &Service{lifetime: t.Context()}
	sub := marotte.PushSubscription{Endpoint: "https://fcm.googleapis.com/fcm/send/size"}
	sub.Keys.P256dh = "###not-base64###" // invalid → "decode p256dh" once past the guard
	sub.Keys.Auth = "AAAA"

	t.Run("exactly_cap_passes_size_guard", func(t *testing.T) {
		_, _, err := s.push(t.Context(), sub, make([]byte, pushBodyCap), marotte.PushKindAgentFinished)
		if err == nil {
			t.Fatalf("push(payload=%d) err = nil, want a downstream error", pushBodyCap)
		}
		if strings.Contains(err.Error(), "payload too large") {
			t.Errorf("push(payload=%d) rejected as too large; %d is not > %d",
				pushBodyCap, pushBodyCap, pushBodyCap)
		}
		if !strings.Contains(err.Error(), "decode p256dh") {
			t.Errorf("push(payload=%d) err = %v, want a decode p256dh error", pushBodyCap, err)
		}
	})

	t.Run("over_cap_rejected", func(t *testing.T) {
		_, _, err := s.push(t.Context(), sub, make([]byte, pushBodyCap+1), marotte.PushKindAgentFinished)
		if err == nil || !strings.Contains(err.Error(), "payload too large") {
			t.Errorf("push(payload=%d) err = %v, want payload-too-large", pushBodyCap+1, err)
		}
	})
}

// TestPush_BodyCapacityStaysPositive verifies the RFC 8291 body capacity never goes
// negative (which would panic) across payload sizes.
func TestPush_BodyCapacityStaysPositive(t *testing.T) {
	s := New(t.Context(), t.TempDir(), testSubject)
	defer s.Close()
	s.client = &http.Client{Transport: errRoundTripper{}}
	sub := pushSubscriptionWithValidKeys(t, "https://fcm.googleapis.com/fcm/send/cap")

	pushExpectNoPanic(t, s, sub, make([]byte, 10), "small")  // payload < ephemeral-key length
	pushExpectNoPanic(t, s, sub, make([]byte, 100), "large") // payload > ephemeral-key length
}

func pushExpectNoPanic(t *testing.T, s *Service, sub marotte.PushSubscription, payload []byte, label string) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("push(%s, len=%d) panicked (body capacity went negative): %v",
				label, len(payload), r)
		}
	}()
	_, _, err := s.push(t.Context(), sub, payload, marotte.PushKindAgentFinished)
	if err == nil {
		t.Errorf("push(%s) err = nil, want forced transport error", label)
		return
	}
	if !strings.Contains(err.Error(), "forced transport error") {
		t.Errorf("push(%s) err = %v, want forced transport error", label, err)
	}
}

// TestSend_ResultStatusLogging pins the disposition (retry, drop, prune) each push-service
// status maps onto, one log message per disposition. The retry ladder is collapsed so the
// 5xx case reaches the give-up path quickly.
func TestSend_ResultStatusLogging(t *testing.T) {
	restoreBase, restoreBudget := pushRetryBase, pushRetryBudget
	pushRetryBase, pushRetryBudget = time.Microsecond, time.Second
	t.Cleanup(func() { pushRetryBase, pushRetryBudget = restoreBase, restoreBudget })

	cases := []struct {
		name   string
		status int
		want   string // the log message this status must produce
		absent string // and one it must not
	}{
		// 400 and 413 are ours to fix; no retry helps.
		{"400_permanent", http.StatusBadRequest, "push: permanent delivery failure", "push: retryable status"},
		{"413_permanent", http.StatusRequestEntityTooLarge, "push: permanent delivery failure", "push: retryable status"},
		// An authorization refusal is its own disposition: whose key is wrong decides pruning.
		{"401_auth_refused", http.StatusUnauthorized, "push: subscription refused as unauthorized", "push: permanent delivery failure"},
		{"403_auth_refused", http.StatusForbidden, "push: subscription refused as unauthorized", "push: permanent delivery failure"},
		// A 429 is retryable, not permanent.
		{"429_retryable", http.StatusTooManyRequests, "push: retryable status", "push: permanent delivery failure"},
		{"500_retryable", http.StatusInternalServerError, "push: retryable status", "push: permanent delivery failure"},
		{"503_retryable", http.StatusServiceUnavailable, "push: retryable status", "push: permanent delivery failure"},
		{"410_prunes", http.StatusGone, "push: subscription invalidated", "push: permanent delivery failure"},
		{"404_prunes", http.StatusNotFound, "push: subscription invalidated", "push: permanent delivery failure"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))

			s := New(t.Context(), t.TempDir(), testSubject)
			defer s.Close()
			s.client = srv.Client()
			s.Subscribe(pushSubscriptionWithValidKeys(t, srv.URL))

			capLog := capture.Default(t)
			s.Send(t.Context(), &marotte.NotificationPayload{Kind: marotte.PushKindAgentFinished, Title: "title", Body: "body"})

			if capLog.CountExact(tc.want) == 0 {
				t.Errorf("status %d: did not log %q", tc.status, tc.want)
			}
			if capLog.CountExact(tc.absent) > 0 {
				t.Errorf("status %d: logged %q, which is the wrong disposition",
					tc.status, tc.absent)
			}
			// g.Wait always returns nil, so the fan-out error is never logged.
			if capLog.CountExact("push: fan-out wait") > 0 {
				t.Errorf("status %d: logged %q though g.Wait() returns nil",
					tc.status, "push: fan-out wait")
			}
			if capLog.CountExact("push: drain response body") > 0 {
				t.Errorf("status %d: logged %q though the drain succeeded",
					tc.status, "push: drain response body")
			}
		})
	}
}

// TestSend_RetriesThenSucceeds pins that a notification survives one 429, and the attempt
// CAP: an uncapped loop against a 429-forever service would leak a goroutine.
func TestSend_RetriesThenSucceeds(t *testing.T) {
	restoreBase, restoreBudget := pushRetryBase, pushRetryBudget
	pushRetryBase, pushRetryBudget = time.Microsecond, time.Second
	t.Cleanup(func() { pushRetryBase, pushRetryBudget = restoreBase, restoreBudget })

	t.Run("429_then_201_delivers", func(t *testing.T) {
		var attempts atomic.Int32
		srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if attempts.Add(1) == 1 {
				w.Header().Set("Retry-After", "0") // 0 is ignored; own backoff applies
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			w.WriteHeader(http.StatusCreated)
		}))

		s := New(t.Context(), t.TempDir(), testSubject)
		defer s.Close()
		s.client = srv.Client()
		s.Subscribe(pushSubscriptionWithValidKeys(t, srv.URL))

		capLog := capture.Default(t)
		s.Send(t.Context(), &marotte.NotificationPayload{Kind: marotte.PushKindAgentFinished, Title: "title", Body: "body"})

		if got := attempts.Load(); got != 2 {
			t.Errorf("attempts = %d, want 2 (one 429 then one success)", got)
		}
		if got, _ := capLog.AttrValue("push: delivered", "attempts"); got != "2" {
			t.Errorf("the delivered line reports attempts=%q, want \"2\" (one 429 then one success)", got)
		}
	})

	t.Run("persistent_429_stops_at_the_cap", func(t *testing.T) {
		var attempts atomic.Int32
		srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts.Add(1)
			w.WriteHeader(http.StatusTooManyRequests)
		}))

		s := New(t.Context(), t.TempDir(), testSubject)
		defer s.Close()
		s.client = srv.Client()
		s.Subscribe(pushSubscriptionWithValidKeys(t, srv.URL))

		capLog := capture.Default(t)
		s.Send(t.Context(), &marotte.NotificationPayload{Kind: marotte.PushKindAgentFinished, Title: "title", Body: "body"})

		if got := attempts.Load(); got != int32(pushMaxAttempts) {
			t.Errorf("attempts = %d, want pushMaxAttempts (%d)", got, pushMaxAttempts)
		}
		if capLog.CountExact("push: giving up, attempts exhausted") == 0 {
			t.Error("gave up without saying so")
		}
	})

	t.Run("a_retry_past_the_budget_is_not_made", func(t *testing.T) {
		// A Retry-After past the notification's usefulness is a REFUSAL, not a sleep.
		var attempts atomic.Int32
		srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts.Add(1)
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(http.StatusServiceUnavailable)
		}))

		s := New(t.Context(), t.TempDir(), testSubject)
		defer s.Close()
		s.client = srv.Client()
		s.Subscribe(pushSubscriptionWithValidKeys(t, srv.URL))

		capLog := capture.Default(t)
		s.Send(t.Context(), &marotte.NotificationPayload{Kind: marotte.PushKindAgentFinished, Title: "title", Body: "body"})

		if got := attempts.Load(); got != 1 {
			t.Errorf("attempts = %d, want 1 (the retry lands past the budget)", got)
		}
		if capLog.CountExact("push: giving up, retry would land past the notification's usefulness") == 0 {
			t.Error("dropped a notification without naming the budget as the reason")
		}
	})
	t.Run("a_first_attempt_delivery_is_not_reported_as_a_retry", func(t *testing.T) {
		// The retried line must not fire for an ordinary success, or a needed retry is invisible.
		var attempts atomic.Int32
		srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts.Add(1)
			w.WriteHeader(http.StatusCreated)
		}))

		s := New(t.Context(), t.TempDir(), testSubject)
		defer s.Close()
		s.client = srv.Client()
		s.Subscribe(pushSubscriptionWithValidKeys(t, srv.URL))

		capLog := capture.Default(t)
		s.Send(t.Context(), &marotte.NotificationPayload{Kind: marotte.PushKindAgentFinished, Title: "title", Body: "body"})

		if got := attempts.Load(); got != 1 {
			t.Fatalf("attempts = %d, want 1: this case has to deliver first try", got)
		}
		if n := capLog.CountExact("push: delivered after retry"); n != 0 {
			t.Errorf("a first-attempt delivery reported %d retry line(s), want 0", n)
		}
	})
}

// TestParseRetryAfter covers both header forms plus the values that must yield 0 (absent,
// unparseable, zero, a past date) so the caller keeps its own backoff.
func TestParseRetryAfter(t *testing.T) {
	past := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
	future := time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)

	if got := parseRetryAfter("12"); got != 12*time.Second {
		t.Errorf("seconds form: got %v, want 12s", got)
	}
	if got := parseRetryAfter(future); got <= 0 || got > 31*time.Second {
		t.Errorf("date form: got %v, want a positive delay under ~30s", got)
	}
	for _, in := range []string{"", "soon", "0", "-5", past} {
		if got := parseRetryAfter(in); got != 0 {
			t.Errorf("parseRetryAfter(%q) = %v, want 0", in, got)
		}
	}
}

// TestPush_MergesCancelledServiceCtx verifies an already-cancelled service ctx cancels the
// request at once. The handler must not wait on r.Context() alone: with an unread body,
// net/http never detects the disconnect, so Close and the handler deadlock. unblock's
// cleanup registers after the server's, so it runs first.
func TestPush_MergesCancelledServiceCtx(t *testing.T) {
	unblock := make(chan struct{})
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-unblock:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(unblock) })

	s := New(t.Context(), t.TempDir(), testSubject)
	defer s.Close()
	s.client = srv.Client()
	sub := pushSubscriptionWithValidKeys(t, srv.URL)

	s.cancel()

	callerCtx, callerCancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer callerCancel()

	_, _, err := s.push(callerCtx, sub, []byte(`{"title":"t","body":"b"}`), marotte.PushKindAgentFinished)
	if err == nil {
		t.Fatalf("push with cancelled service ctx returned nil error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("push err = %v; want context.Canceled (merged cancelled service ctx)", err)
	}
}

// TestEncryptPayload_buildsRFC8291WireBody pins the aes128gcm body: salt(16) || rs(4) ||
// idlen(1) || ephemeral key(65) || ciphertext (payload + 0x02 delimiter + 16-byte tag).
func TestEncryptPayload_buildsRFC8291WireBody(t *testing.T) {
	sub := pushSubscriptionWithValidKeys(t, "https://fcm.googleapis.com/fcm/send/wire")
	payload := []byte(`{"title":"t","body":"hello"}`)

	body, err := encryptPayload(sub, payload)
	if err != nil {
		t.Fatalf("encryptPayload err = %v, want nil for a valid subscription", err)
	}

	const ephLen = 65 // P-256 uncompressed point: 0x04 || X(32) || Y(32)
	const gcmTag = 16
	wantLen := 16 + 4 + 1 + ephLen + (len(payload) + 1 + gcmTag)
	if len(body) != wantLen {
		t.Fatalf("len(body) = %d, want %d (salt+rs+idlen+ephKey+ciphertext)", len(body), wantLen)
	}
	if body[16] != 0x00 || body[17] != 0x00 || body[18] != 0x10 || body[19] != 0x00 {
		t.Errorf("record-size header = % x, want 00 00 10 00 (4096)", body[16:20])
	}
	if body[20] != ephLen {
		t.Errorf("ephemeral key length byte = %d, want %d", body[20], ephLen)
	}
}

// TestFitToCap_ChargesTheMarkerInsideTheCap pins that one pass lands the marshaled payload
// on pushBodyCap EXACTLY: a marker charged outside the cap falls short every trim, which an
// at-most-the-cap assertion cannot see.
func TestFitToCap_ChargesTheMarkerInsideTheCap(t *testing.T) {
	title := "Marotte"
	body := strings.Repeat("x", 4000)

	fit, truncated := fitToCap(&marotte.NotificationPayload{Title: title, Body: body})
	gotTitle, gotBody := fit.Title, fit.Body
	if !truncated {
		t.Fatalf("fitToCap reported no truncation for a %d-byte body", len(body))
	}
	if n := marshaledLen(&marotte.NotificationPayload{Title: gotTitle, Body: gotBody}); n != pushBodyCap {
		t.Errorf("marshaled payload = %d bytes, want exactly %d: the trim must spend the whole budget, marker included", n, pushBodyCap)
	}
	if !strings.HasSuffix(gotBody, pushTruncMarker) {
		t.Errorf("trimmed body should end with %q, got suffix %q",
			pushTruncMarker, gotBody[max(len(gotBody)-10, 0):])
	}
	if gotTitle != title {
		t.Errorf("title = %q, want it untouched: the body absorbed the overflow", gotTitle)
	}
}

// TestFitToCap_KeepsTheBodysCRLFAxis pins that the body keeps its newlines while the title
// (a single-line sink) does not.
func TestFitToCap_KeepsTheBodysCRLFAxis(t *testing.T) {
	t.Run("body keeps newlines, loses other control runes", func(t *testing.T) {
		body := "a\x1bb\nc" + strings.Repeat("x", 4000)
		fit, truncated := fitToCap(&marotte.NotificationPayload{Title: "Marotte", Body: body})
		gotTitle, gotBody := fit.Title, fit.Body
		if !truncated {
			t.Fatalf("fitToCap reported no truncation for a %d-byte body", len(body))
		}
		if !strings.Contains(gotBody, "\n") {
			t.Errorf("body lost its newline: %q", gotBody[:min(len(gotBody), 10)])
		}
		if strings.Contains(gotBody, "\x1b") {
			t.Error("body kept a raw ESC; the sanitize half of the trim did not run")
		}
		if n := marshaledLen(&marotte.NotificationPayload{Title: gotTitle, Body: gotBody}); n > pushBodyCap {
			t.Errorf("marshaled payload = %d bytes, exceeds cap %d", n, pushBodyCap)
		}
	})

	t.Run("title loses newlines", func(t *testing.T) {
		title := "a\x1bb\nc" + strings.Repeat("y", 4000)
		fit, truncated := fitToCap(&marotte.NotificationPayload{Title: title, Body: ""})
		gotTitle, gotBody := fit.Title, fit.Body
		if !truncated {
			t.Fatalf("fitToCap reported no truncation for a %d-byte title", len(title))
		}
		if strings.ContainsAny(gotTitle, "\n\r\x1b") {
			t.Errorf("title kept a record-forging rune: %q", gotTitle[:min(len(gotTitle), 10)])
		}
		if n := marshaledLen(&marotte.NotificationPayload{Title: gotTitle, Body: gotBody}); n > pushBodyCap {
			t.Errorf("marshaled payload = %d bytes, exceeds cap %d", n, pushBodyCap)
		}
	})
}
