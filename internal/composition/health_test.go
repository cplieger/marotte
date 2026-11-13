package composition

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeHealthz serves GET /healthz answering status.
func fakeHealthz(t *testing.T, status int) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"unhealthy","reasons":["kiro-cli is not installed"]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String()
}

// closedAddr is a loopback address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Setup: listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("Setup: close: %v", err)
	}
	return addr
}

func TestRunHealth_ExitCodes(t *testing.T) {
	tests := []struct {
		name       string
		listenAddr func(t *testing.T) string
		wantCode   int
		wantReason string
	}{
		{"healthy", func(t *testing.T) string { return fakeHealthz(t, http.StatusOK) }, 0, ""},
		{"unhealthy", func(t *testing.T) string { return fakeHealthz(t, http.StatusServiceUnavailable) }, 1, "503"},
		{"no listener", closedAddr, 1, "connection refused"},
		{"a refused LISTEN_ADDR", func(*testing.T) string { return ":http" }, 1, "LISTEN_ADDR"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(listenAddrVar, tc.listenAddr(t))
			var stderr bytes.Buffer
			if code := RunHealth(&stderr); code != tc.wantCode {
				t.Errorf("RunHealth() = %d, want %d; stderr %q", code, tc.wantCode, stderr.String())
			}
			got := stderr.String()
			if tc.wantReason == "" {
				if got != "" {
					t.Errorf("RunHealth() stderr = %q, want nothing when healthy", got)
				}
				return
			}
			if strings.Count(got, "\n") != 1 || !strings.HasSuffix(got, "\n") {
				t.Errorf("RunHealth() stderr = %q, want one line", got)
			}
			if !strings.Contains(got, tc.wantReason) {
				t.Errorf("RunHealth() stderr = %q, want it to name %q", got, tc.wantReason)
			}
		})
	}
}
