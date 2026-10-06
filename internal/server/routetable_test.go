package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

// TestPlainPathRoutesRefuseTheWrongMethod pins 405 + Allow from the in-handler gate on
// plain-path routes; the loopback-gated ones go through loopbackOnly.
func TestPlainPathRoutesRefuseTheWrongMethod(t *testing.T) {
	cases := []struct {
		name      string
		method    string
		path      string
		wantAllow string
		serve     func(*Server, http.ResponseWriter, *http.Request)
	}{
		{
			name: "policy_view", method: http.MethodPost, path: "/api/permissions", wantAllow: "GET",
			serve: func(s *Server, w http.ResponseWriter, r *http.Request) { s.handlePolicyView(w, r) },
		},
		{
			name: "account_usage", method: http.MethodDelete, path: "/api/account/usage", wantAllow: "GET",
			serve: func(s *Server, w http.ResponseWriter, r *http.Request) { s.handleAccountUsage(w, r) },
		},
		{
			name: "tool_status", method: http.MethodPatch, path: "/api/tools/status", wantAllow: "GET",
			serve: func(_ *Server, w http.ResponseWriter, r *http.Request) { handleToolStatus(w, r) },
		},
		{
			name: "tool_reconcile", method: http.MethodDelete, path: "/api/tools/reconcile", wantAllow: "POST",
			serve: func(s *Server, w http.ResponseWriter, r *http.Request) { s.handleToolReconcile(w, r) },
		},
		{
			name: "kiro_rescan", method: http.MethodGet, path: kiroRescanPath, wantAllow: "POST",
			serve: func(s *Server, w http.ResponseWriter, r *http.Request) {
				loopbackOnly(kiroRescanSurface, http.HandlerFunc(s.handleKiroRescan)).ServeHTTP(w, r)
			},
		},
		{
			name: "pprof_index", method: http.MethodPost, path: pprofPath + "goroutine", wantAllow: "GET",
			serve: func(_ *Server, w http.ResponseWriter, r *http.Request) { pprofHandler().ServeHTTP(w, r) },
		},
		{
			name: "spec", method: http.MethodPost, path: "/api/specs/x", wantAllow: "GET",
			serve: func(s *Server, w http.ResponseWriter, r *http.Request) { s.handleSpec(w, r) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Recording the call is the red check that the gate refuses before the subprocess spawns.
			rescanned := false
			s := &Server{
				kiroDocs:   &docsCache{},
				kiroRescan: func(context.Context) (bool, error) { rescanned = true; return true, nil },
			}

			req := httptest.NewRequest(tc.method, "http://127.0.0.1:9847"+tc.path, http.NoBody)
			req.RemoteAddr = "127.0.0.1:54321"
			req.Host = "127.0.0.1:9847"
			rec := httptest.NewRecorder()
			tc.serve(s, rec, req)

			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s: status = %d, want 405 (a mismatch used to reach the SPA shell)",
					tc.method, tc.path, rec.Code)
			}
			if got := rec.Header().Get("Allow"); got != tc.wantAllow {
				t.Errorf("Allow = %q, want %q", got, tc.wantAllow)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"method not allowed"}` {
				t.Errorf("body = %q, want marotte's bare error envelope", got)
			}
			if rescanned {
				t.Error("the refused request still ran the rescan; the method gate is placed after the work")
			}
		})
	}
}

// TestLoopbackGateStillPrecedesTheMethodGate pins the ORDER the two gates run
// in on the loopback-only routes: a LAN caller with the wrong method gets 403
// and no Allow header, so the refusal discloses nothing about the method set.
func TestLoopbackGateStillPrecedesTheMethodGate(t *testing.T) {
	rescanServer := &Server{kiroRescan: func(context.Context) (bool, error) { return true, nil }}
	for name, h := range map[string]http.Handler{
		"kiro_rescan": loopbackOnly(kiroRescanSurface, http.HandlerFunc(rescanServer.handleKiroRescan)),
		"pprof_index": pprofHandler(),
	} {
		t.Run(name, func(t *testing.T) {
			// Either way, the LAN peer must lose at the outer gate.
			req := httptest.NewRequest(http.MethodGet, "http://localhost:9847/x", http.NoBody)
			req.RemoteAddr = "192.168.1.20:54321"
			req.Host = "localhost:9847"
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 from the loopback gate", rec.Code)
			}
			if got := rec.Header().Get("Allow"); got != "" {
				t.Errorf("Allow = %q on a 403, want unset: a refused remote caller must not learn the method set", got)
			}
		})
	}
}
