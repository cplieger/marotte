package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/httpreply"
)

// TestServeMuxMethodPatternCannotAnswer405UnderACatchAll pins the net/http fact the route
// table rests on: ServeMux's 405 + Allow fires only when NO pattern matched, so a "/" mount
// absorbs every method mismatch. An A/B, because the claim is a difference.
func TestServeMuxMethodPatternCannotAnswer405UnderACatchAll(t *testing.T) {
	const catchAllStatus = 299 // a status no marotte handler produces

	build := func(withCatchAll bool) *http.ServeMux {
		mux := http.NewServeMux()
		if withCatchAll {
			mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(catchAllStatus)
			}))
		}
		mux.HandleFunc("GET /api/permissions", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		return mux
	}

	serve := func(mux *http.ServeMux, method string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, "http://example.com/api/permissions", http.NoBody))
		return rec
	}

	t.Run("without_catch_all_405_names_allow", func(t *testing.T) {
		rec := serve(build(false), http.MethodPost)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405 (ServeMux's own method refusal)", rec.Code)
		}
		// GET patterns match HEAD, so ServeMux advertises both.
		if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
			t.Errorf("Allow = %q, want %q", got, "GET, HEAD")
		}
	})

	t.Run("with_catch_all_the_mismatch_is_absorbed", func(t *testing.T) {
		rec := serve(build(true), http.MethodPost)
		if rec.Code != catchAllStatus {
			t.Fatalf("status = %d, want %d (the catch-all answered, not a 405)",
				rec.Code, catchAllStatus)
		}
		if got := rec.Header().Get("Allow"); got != "" {
			t.Errorf("Allow = %q, want unset — ServeMux never computed a method list", got)
		}
	})

	// HEAD is the one other method a GET pattern serves.
	t.Run("HEAD_is_served_by_a_GET_pattern", func(t *testing.T) {
		if rec := serve(build(true), http.MethodHead); rec.Code != http.StatusOK {
			t.Errorf("HEAD status = %d, want 200", rec.Code)
		}
	})
}

// TestPprofRefusesTheWrongMethod pins pprof's own 405: its catch-all mount has no registrar
// filter, so the check inside loopbackOnly is the only method gate on the subtree.
func TestPprofRefusesTheWrongMethod(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9847"+pprofPath+"goroutine", http.NoBody)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Host = "127.0.0.1:9847"
	rec := httptest.NewRecorder()
	pprofHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST %s: status = %d, want 405", pprofPath, rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodGet {
		t.Errorf("Allow = %q, want GET", got)
	}
}

// lanRequest is a request from a LAN peer through the loopback host name.
func lanRequest(method, path string) *http.Request {
	req := httptest.NewRequest(method, "http://localhost:9847"+path, http.NoBody)
	req.RemoteAddr = "192.168.1.20:54321"
	req.Host = "localhost:9847"
	return req
}

// TestLoopbackGateStillPrecedesTheMethodGate pins the ORDER the two gates run in on the
// loopback-only routes: a LAN caller gets no Allow header whatever its method, so a refusal
// discloses nothing about the method set.
func TestLoopbackGateStillPrecedesTheMethodGate(t *testing.T) {
	t.Run("kiro_rescan", func(t *testing.T) {
		rescanned := false
		s := docServer(t.TempDir(), WithKiroRescan(func(context.Context) (bool, error) { rescanned = true; return true, nil }))
		mux := s.mux()

		cases := []struct {
			method string
			want   int
		}{
			{http.MethodPost, http.StatusForbidden},
			{http.MethodGet, http.StatusNotFound},
		}
		for _, tc := range cases {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, lanRequest(tc.method, kiroRescanPath))
			if rec.Code != tc.want {
				t.Errorf("LAN %s %s = %d, want %d", tc.method, kiroRescanPath, rec.Code, tc.want)
			}
			if got := rec.Header().Get("Allow"); got != "" {
				t.Errorf("LAN %s Allow = %q, want unset: a refused remote caller must not learn the method set", tc.method, got)
			}
		}
		if rescanned {
			t.Error("a LAN caller ran the rescan")
		}
	})

	t.Run("kiro_rescan_unmanaged", func(t *testing.T) {
		mux := docServer(t.TempDir()).mux()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, lanRequest(http.MethodPost, kiroRescanPath))
		if rec.Code != http.StatusForbidden || rec.Header().Get("Allow") != "" {
			t.Errorf("LAN POST %s with no managed install = %d Allow %q, want the same 403 with no Allow",
				kiroRescanPath, rec.Code, rec.Header().Get("Allow"))
		}
		loop := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9847"+kiroRescanPath, http.NoBody)
		loop.RemoteAddr = "127.0.0.1:54321"
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, loop)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), httpreply.MsgUnknownEndpoint) {
			t.Errorf("loopback POST %s with no managed install = %d %s, want the fallback's 404", kiroRescanPath, rec.Code, rec.Body.String())
		}
	})

	t.Run("pprof_index", func(t *testing.T) {
		rec := httptest.NewRecorder()
		pprofHandler().ServeHTTP(rec, lanRequest(http.MethodPost, pprofPath))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 from the loopback gate", rec.Code)
		}
		if got := rec.Header().Get("Allow"); got != "" {
			t.Errorf("Allow = %q on a 403, want unset", got)
		}
	})
}
