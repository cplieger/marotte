package server

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// pprofRequest drives the gated handler with the full /debug/pprof/ path intact, which
// pprof.Index derives the profile name from.
func pprofRequest(t *testing.T, path, remote, host string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = remote
	req.Host = host
	rec := httptest.NewRecorder()
	pprofHandler().ServeHTTP(rec, req)
	return rec
}

// TestPprof_GoroutineDumpAnswersOnLoopback asserts the goroutine dump end to end.
func TestPprof_GoroutineDumpAnswersOnLoopback(t *testing.T) {
	rec := pprofRequest(t, pprofPath+"goroutine?debug=2", "127.0.0.1:54321", "localhost:9847")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "goroutine") {
		t.Errorf("body does not look like a goroutine dump: %.200s", body)
	}
	// ?debug=2 is plain text, fetchable inside the container with no pprof tooling.
	if !strings.Contains(body, "runtime.") && !strings.Contains(body, ".go:") {
		t.Errorf("body carries no stack frames: %.400s", body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func TestPprof_IndexAnswersOnLoopback(t *testing.T) {
	rec := pprofRequest(t, pprofPath, "127.0.0.1:54321", "127.0.0.1:9847")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "goroutine") {
		t.Errorf("index does not list the goroutine profile: %.400s", rec.Body.String())
	}
	// The 1.27 profile is reached by name off this index; a toolchain dropping it shows here.
	if !strings.Contains(rec.Body.String(), "goroutineleak") {
		t.Errorf("index does not list the goroutineleak profile: %.400s", rec.Body.String())
	}
}

// TestPprof_GoroutineLeakProfileAnswersOnLoopback pins that goroutineleak answers through the
// mount in all three forms (nothing in the package names it, so only a request proves it).
// The count is not asserted: detection is reachability-based.
func TestPprof_GoroutineLeakProfileAnswersOnLoopback(t *testing.T) {
	t.Run("debug=1 is the text summary", func(t *testing.T) {
		rec := pprofRequest(t, pprofPath+"goroutineleak?debug=1", "127.0.0.1:54321", "localhost:9847")
		if rec.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200: %.200s", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
			t.Errorf("Content-Type = %q, want text/plain", got)
		}
		const prefix = "goroutineleak profile: total "
		body := rec.Body.String()
		if !strings.HasPrefix(body, prefix) {
			t.Fatalf("body = %q, want it to open %q", body, prefix)
		}
		total := strings.TrimSpace(strings.TrimPrefix(strings.SplitN(body, "\n", 2)[0], prefix))
		if _, err := strconv.Atoi(total); err != nil {
			t.Errorf("total = %q, want an integer: %v", total, err)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store", got)
		}
	})

	t.Run("debug=2 carries stack frames", func(t *testing.T) {
		rec := pprofRequest(t, pprofPath+"goroutineleak?debug=2", "127.0.0.1:54321", "localhost:9847")
		if rec.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200: %.200s", rec.Code, rec.Body.String())
		}
		if body := rec.Body.String(); !strings.Contains(body, ".go:") {
			t.Errorf("body carries no stack frames: %.400s", body)
		}
	})

	t.Run("bare path is the protobuf go tool pprof fetches", func(t *testing.T) {
		rec := pprofRequest(t, pprofPath+"goroutineleak", "127.0.0.1:54321", "localhost:9847")
		if rec.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200: %.200s", rec.Code, rec.Body.String())
		}
		// Decompressing proves a real profile rather than an index page answering 200.
		zr, err := gzip.NewReader(rec.Body)
		if err != nil {
			t.Fatalf("body is not the gzipped protobuf go tool pprof expects: %v", err)
		}
		defer zr.Close()
		if _, err := io.Copy(io.Discard, zr); err != nil {
			t.Errorf("protobuf body did not decompress cleanly: %v", err)
		}
	})

	// Behind the SAME gate: a leak profile names every function on every parked stack.
	t.Run("refused off loopback", func(t *testing.T) {
		rec := pprofRequest(t, pprofPath+"goroutineleak?debug=2", "10.0.0.5:1", "localhost:9847")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("code = %d, want 403", rec.Code)
		}
		if strings.Contains(rec.Body.String(), "goroutineleak profile") {
			t.Errorf("the refusal leaked the profile: %.400s", rec.Body.String())
		}
	})
}

// TestPprof_RefusesEverythingButAnInContainerCaller pins the gate case by case.
func TestPprof_RefusesEverythingButAnInContainerCaller(t *testing.T) {
	cases := map[string]struct {
		remote, host string
		header       [2]string
		wantStatus   int
	}{
		"loopback peer and host":      {remote: "127.0.0.1:1", host: "localhost:9847", wantStatus: http.StatusOK},
		"IPv6 loopback":               {remote: "[::1]:1", host: "[::1]:9847", wantStatus: http.StatusOK},
		"LAN peer":                    {remote: "192.168.1.20:1", host: "localhost:9847", wantStatus: http.StatusForbidden},
		"rebound host, loopback peer": {remote: "127.0.0.1:1", host: "evil.example.com", wantStatus: http.StatusForbidden},
		"loopback peer behind a proxy": {
			remote: "127.0.0.1:1", host: "localhost:9847",
			header: [2]string{"X-Forwarded-For", "203.0.113.7"}, wantStatus: http.StatusForbidden,
		},
		"from a browser tab": {
			remote: "127.0.0.1:1", host: "localhost:9847",
			header: [2]string{"Sec-Fetch-Site", "same-origin"}, wantStatus: http.StatusForbidden,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, pprofPath+"goroutine", nil)
			req.RemoteAddr = c.remote
			req.Host = c.host
			if c.header[0] != "" {
				req.Header.Set(c.header[0], c.header[1])
			}
			rec := httptest.NewRecorder()
			pprofHandler().ServeHTTP(rec, req)
			if rec.Code != c.wantStatus {
				t.Errorf("code = %d, want %d: %.200s", rec.Code, c.wantStatus, rec.Body.String())
			}
		})
	}
}

// TestPprof_HoldTheServerProfilesAreNotMounted pins CPU and Trace absent (a 404 from Index).
func TestPprof_HoldTheServerProfilesAreNotMounted(t *testing.T) {
	for _, name := range []string{"profile", "trace"} {
		t.Run(name, func(t *testing.T) {
			rec := pprofRequest(t, pprofPath+name, "127.0.0.1:1", "localhost:9847")
			if rec.Code == http.StatusOK {
				t.Errorf("%s answered 200; it holds the server for its sample window", name)
			}
		})
	}
}

// TestPprof_RefusalNamesProfilesAndCarriesNoProfileData pins that a refusal leaks no profile
// and names this endpoint.
func TestPprof_RefusalNamesProfilesAndCarriesNoProfileData(t *testing.T) {
	rec := pprofRequest(t, pprofPath+"goroutine?debug=2", "10.0.0.5:1", "localhost:9847")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "goroutine profile") || strings.Contains(body, "runtime.gopark") {
		t.Errorf("the refusal leaked profile data: %.400s", body)
	}
	if !strings.Contains(body, "loopback-only") {
		t.Errorf("refusal body = %q, want the loopback-only error envelope", body)
	}
	if !strings.Contains(body, pprofSurface) {
		t.Errorf("refusal body = %q, want it to name %q", body, pprofSurface)
	}
	if strings.Contains(body, kiroRescanSurface) {
		t.Errorf("refusal body names the repair hook instead of this endpoint: %q", body)
	}
}
