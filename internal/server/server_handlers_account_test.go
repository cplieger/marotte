package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

type fakeAcctUsage struct {
	ret   *marotte.AccountUsage
	err   error
	calls int
}

func (f *fakeAcctUsage) AccountUsage(_ context.Context) (*marotte.AccountUsage, error) {
	f.calls++
	return f.ret, f.err
}

func getUsage(t *testing.T, s *Server) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/account/usage", http.NoBody)
	rec := httptest.NewRecorder()
	s.handleAccountUsage(rec, req)
	return rec
}

func sampleUsage() *marotte.AccountUsage {
	return &marotte.AccountUsage{
		PlanName:   "KIRO POWER",
		Breakdowns: []marotte.AccountUsageBreakdown{{ResourceType: "CREDIT", Used: 10, Limit: 100, Percentage: 10, HasLimit: true}},
	}
}

func TestHandleAccountUsageNilProvider(t *testing.T) {
	s := &Server{}
	rec := getUsage(t, s)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestHandleAccountUsageSuccess(t *testing.T) {
	f := &fakeAcctUsage{ret: sampleUsage()}
	s := &Server{accountUsage: f}
	rec := getUsage(t, s)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got marotte.AccountUsage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.PlanName != "KIRO POWER" || got.Stale {
		t.Errorf("body = %+v", got)
	}
	if f.calls != 1 {
		t.Errorf("provider calls = %d, want 1", f.calls)
	}
}

func TestHandleAccountUsageCacheHit(t *testing.T) {
	// Pre-seed a fresh cache entry; the provider must NOT be called.
	f := &fakeAcctUsage{err: context.DeadlineExceeded}
	s := &Server{accountUsage: f}
	s.acctUsage.data = sampleUsage()
	s.acctUsage.atNanos = time.Now().UnixNano()

	rec := getUsage(t, s)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if f.calls != 0 {
		t.Errorf("provider called %d times on a fresh cache hit", f.calls)
	}
}

func TestHandleAccountUsageStaleFallback(t *testing.T) {
	// Older than the TTL and the fetch fails: serve last-known, marked stale.
	f := &fakeAcctUsage{err: context.DeadlineExceeded}
	s := &Server{accountUsage: f}
	s.acctUsage.data = sampleUsage()
	s.acctUsage.atNanos = time.Now().Add(-2 * accountUsageTTL).UnixNano()

	rec := getUsage(t, s)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got marotte.AccountUsage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Stale {
		t.Error("stale fallback should set stale=true")
	}
	if f.calls != 1 {
		t.Errorf("provider calls = %d, want 1", f.calls)
	}
}

func TestHandleAccountUsageErrorNoCache(t *testing.T) {
	f := &fakeAcctUsage{err: context.DeadlineExceeded}
	s := &Server{accountUsage: f}
	rec := getUsage(t, s)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// TestHandleAccountUsage_theTTLEdgeIsRefetched pins that a snapshot at exactly the TTL is
// refetched; a synthetic clock lands on the single instant.
func TestHandleAccountUsage_theTTLEdgeIsRefetched(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fakeAcctUsage{ret: sampleUsage()}
		s := &Server{accountUsage: f}
		s.acctUsage.data = sampleUsage()
		s.acctUsage.atNanos = time.Now().UnixNano()

		// A nanosecond short of the TTL is still fresh.
		synctest.Sleep(accountUsageTTL - time.Nanosecond)
		if rec := getUsage(t, s); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if f.calls != 0 {
			t.Errorf("provider called %d times a nanosecond inside the TTL, want 0", f.calls)
		}

		// On the edge it is stale.
		synctest.Sleep(time.Nanosecond)
		if rec := getUsage(t, s); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if f.calls != 1 {
			t.Errorf("provider called %d times exactly on the TTL, want 1", f.calls)
		}
	})
}
