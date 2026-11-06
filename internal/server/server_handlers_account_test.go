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
	ret *marotte.AccountUsage
	err error
	// keyAfter, when set, moves the account during the fetch.
	keyAfter func() uint64
	calls    int
	key      uint64
	ok       bool
}

func (f *fakeAcctUsage) AccountUsage(_ context.Context) (*marotte.AccountUsage, error) {
	f.calls++
	if f.keyAfter != nil {
		f.key = f.keyAfter()
	}
	return f.ret, f.err
}

func (f *fakeAcctUsage) UsageAccount() (uint64, bool) { return f.key, f.ok }

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
	f := &fakeAcctUsage{ret: sampleUsage(), ok: true}
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
	f := &fakeAcctUsage{err: context.DeadlineExceeded, ok: true}
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
	f := &fakeAcctUsage{err: context.DeadlineExceeded, ok: true}
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
	f := &fakeAcctUsage{err: context.DeadlineExceeded, ok: true}
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
		f := &fakeAcctUsage{ret: sampleUsage(), ok: true}
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

func decodeUsage(t *testing.T, rec *httptest.ResponseRecorder) marotte.AccountUsage {
	t.Helper()
	var got marotte.AccountUsage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

func TestHandleAccountUsageNotTheSignedInAccount(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cache bool
	}{{"no_cache", false}, {"cached", true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeAcctUsage{ret: sampleUsage(), key: 1}
			s := &Server{accountUsage: f}
			if tc.cache {
				s.acctUsage.data, s.acctUsage.key = sampleUsage(), 1
				s.acctUsage.atNanos = time.Now().UnixNano()
			}
			if rec := getUsage(t, s); rec.Code != http.StatusServiceUnavailable {
				t.Errorf("status = %d, want 503", rec.Code)
			}
			if f.calls != 0 {
				t.Errorf("provider calls = %d, want 0: a fetch reads the held account's numbers", f.calls)
			}
		})
	}
}

func TestHandleAccountUsageDropsAnotherAccountsEntry(t *testing.T) {
	t.Run("fresh_entry_refetched", func(t *testing.T) {
		f := &fakeAcctUsage{ret: sampleUsage(), key: 1, ok: true}
		s := &Server{accountUsage: f}
		if rec := getUsage(t, s); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		f.key = 2
		f.ret = &marotte.AccountUsage{PlanName: "KIRO PRO"}
		rec := getUsage(t, s)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if got := decodeUsage(t, rec); got.PlanName != "KIRO PRO" {
			t.Errorf("plan = %q, want the new account's KIRO PRO", got.PlanName)
		}
		if f.calls != 2 {
			t.Errorf("provider calls = %d, want 2", f.calls)
		}
	})
	t.Run("failed_fetch_serves_no_stale_entry", func(t *testing.T) {
		f := &fakeAcctUsage{ret: sampleUsage(), key: 1, ok: true}
		s := &Server{accountUsage: f}
		if rec := getUsage(t, s); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		s.acctUsage.atNanos = time.Now().Add(-2 * accountUsageTTL).UnixNano()
		f.key, f.ret, f.err = 2, nil, context.DeadlineExceeded
		if rec := getUsage(t, s); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503, not account 1's reading marked stale", rec.Code)
		}
	})
}

func TestHandleAccountUsageAccountMovedDuringRead(t *testing.T) {
	f := &fakeAcctUsage{ret: sampleUsage(), key: 1, ok: true, keyAfter: func() uint64 { return 2 }}
	s := &Server{accountUsage: f}
	if rec := getUsage(t, s); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 for a reading of unknown account", rec.Code)
	}
	f.keyAfter = nil
	if rec := getUsage(t, s); rec.Code != http.StatusOK {
		t.Fatalf("second status = %d, want 200", rec.Code)
	}
	if f.calls != 2 {
		t.Errorf("provider calls = %d, want 2: the first reading must not be cached", f.calls)
	}
}
