package kiro

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/signin"
)

func TestOIDC_ARedirectIsNotFollowed(t *testing.T) {
	var (
		mu       sync.Mutex
		received []string
	)
	elsewhere := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		received = append(received, string(body))
		mu.Unlock()
	}))
	defer elsewhere.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", elsewhere.URL+"/token")
		w.WriteHeader(http.StatusTemporaryRedirect)
		_, _ = io.WriteString(w, `{"accessToken":"from-the-redirect"}`)
	}))
	defer redirector.Close()
	b := New()
	pointAt(b, redirector.URL)
	r := testRecord(providerBuilderID, "")
	_, err := b.Refresh(t.Context(), credOf(t, r))
	if err == nil || errors.Is(err, signin.ErrRejected) {
		t.Errorf("Refresh through a redirect = %v, want a transient failure", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(received) != 0 {
		t.Errorf("the redirect target received %d requests (%q), want none", len(received), received)
	}
}

func TestOIDC_ABodyOverTheCapIsRefused(t *testing.T) {
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"accessToken":"`+strings.Repeat("a", maxBodyBytes)+`"}`)
	}))
	defer big.Close()
	b := New()
	pointAt(b, big.URL)
	if _, err := b.Refresh(t.Context(), credOf(t, testRecord(providerBuilderID, ""))); err == nil {
		t.Error("a token answer over 1 MiB was accepted")
	}
}

// pollWith runs one device flow whose token polls get answers, then the default success.
func pollWith(t *testing.T, answers ...scripted) (*fakeAWS, record, error) {
	t.Helper()
	aws := newFakeAWS(t, "alice")
	aws.set(func(f *fakeAWS) { f.deviceAnswers = answers })
	r, err := signInDirect(t, backendFor(aws), builderID())
	return aws, r, err
}

func TestDeviceFlow_SlowDownAndExpiry(t *testing.T) {
	pending := scripted{status: 400, body: `{"error":"authorization_pending"}`}
	t.Run("pending_then_approved", func(t *testing.T) {
		aws, r, err := pollWith(t, pending, pending)
		if err != nil || r.AccessToken != "alice-access" || r.RefreshToken != "alice-refresh" {
			t.Fatalf("record = %+v, %v", r, err)
		}
		if n := len(aws.servedPath("/token")); n != 3 {
			t.Errorf("token polls = %d, want 3", n)
		}
		if body := aws.servedPath("/token")[0].body; !strings.Contains(body, deviceGrant) || !strings.Contains(body, `"deviceCode":"device-1"`) {
			t.Errorf("device grant body = %s", body)
		}
	})
	t.Run("slow_down_widens_the_interval", func(t *testing.T) {
		aws := newFakeAWS(t, "alice")
		aws.set(func(f *fakeAWS) { f.deviceAnswers = []scripted{{status: 400, body: `{"error":"slow_down"}`}} })
		b := backendFor(aws)
		b.pollUnit = 20 * time.Millisecond
		d, err := b.Begin(t.Context(), builderID())
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		if _, err := b.Poll(t.Context(), d); err != nil {
			t.Fatal(err)
		}
		// One interval (20 ms) before the slow_down, then 1 + 5 intervals before the approval.
		if elapsed := time.Since(start); elapsed < 7*b.pollUnit {
			t.Errorf("the flow took %v, want at least %v: slow_down did not add five intervals", elapsed, 7*b.pollUnit)
		}
	})
	for _, tc := range []struct {
		answer scripted
		name   string
		want   string
	}{
		{name: "expired_token", answer: scripted{status: 400, body: `{"error":"expired_token"}`}, want: textExpired},
		{name: "access_denied", answer: scripted{status: 400, body: `{"error":"access_denied"}`}, want: textDenied},
		{name: "unknown_error", answer: scripted{status: 400, body: `{"error":"invalid_client"}`}, want: textOIDCRefused},
		{name: "a_200_without_a_token", answer: scripted{status: 200, body: `{}`}, want: textOIDCRefused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := pollWith(t, pending, tc.answer)
			if fe, ok := errors.AsType[*signin.FailureError](err); !ok || fe.Text != tc.want {
				t.Errorf("error = %v, want the %q failure", err, tc.want)
			}
		})
	}
	t.Run("aws_error_type_header", func(t *testing.T) {
		if got := oauthError(response{status: 400, header: http.Header{"X-Amzn-Errortype": {"AccessDeniedException:x"}}}); got != "access_denied" {
			t.Errorf("oauthError = %q, want access_denied", got)
		}
	})
	t.Run("transport_failures_are_ridden_out_then_fail", func(t *testing.T) {
		down := scripted{status: 502}
		if _, r, err := pollWith(t, down, down); err != nil || r.AccessToken == "" {
			t.Errorf("two 502s then approval = %v, want the sign-in", err)
		}
		_, _, err := pollWith(t, down, down, down)
		if fe, ok := errors.AsType[*signin.FailureError](err); !ok || fe.Text != textOIDCUnreachable {
			t.Errorf("three 502s = %v, want the unreachable failure", err)
		}
	})
	t.Run("the_code_expires_locally", func(t *testing.T) {
		aws := newFakeAWS(t, "alice")
		aws.set(func(f *fakeAWS) { f.deviceAnswers = []scripted{pending, pending, pending} })
		b := backendFor(aws)
		clk := &testClock{t: time.Now()}
		b.now = clk.now
		d, err := b.Begin(t.Context(), builderID())
		if err != nil {
			t.Fatal(err)
		}
		clk.advance(601 * time.Second)
		_, err = b.Poll(t.Context(), d)
		if fe, ok := errors.AsType[*signin.FailureError](err); !ok || fe.Text != textExpired {
			t.Errorf("error = %v, want the expired failure", err)
		}
		if n := len(aws.servedPath("/token")); n != 0 {
			t.Errorf("token polls = %d after the code expired, want none", n)
		}
	})
	t.Run("a_cancelled_poll_returns_the_context_error", func(t *testing.T) {
		aws := newFakeAWS(t, "alice")
		aws.set(func(f *fakeAWS) { f.deviceAnswers = []scripted{pending, pending, pending, pending} })
		b := backendFor(aws)
		d, err := b.Begin(t.Context(), builderID())
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := b.Poll(ctx, d); !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
	})
}

func TestDeviceFlow_ANoRefreshTokenAnswerIsRefused(t *testing.T) {
	_, _, err := pollWith(t, scripted{status: 200, body: `{"accessToken":"a","expiresIn":3600}`})
	if fe, ok := errors.AsType[*signin.FailureError](err); !ok || fe.Text != textOIDCRefused {
		t.Errorf("error = %v, want the refused failure", err)
	}
}

func TestTokenResponse_TTLIsBounded(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want time.Duration
	}{
		{0, time.Hour},
		{-5, time.Hour},
		{60, time.Minute},
		{1 << 62, maxTokenTTL},
	} {
		if got := (tokenResponse{ExpiresIn: tc.in}).ttl(); got != tc.want {
			t.Errorf("ttl(%d) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
