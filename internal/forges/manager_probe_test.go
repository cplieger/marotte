package forges

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/cplieger/forgeapi"
)

const probedID = "github:github.com"

func TestManagerProbe_ATemporaryFailureKeepsTheRowConnected(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"dead network", &forgeapi.Error{
			Op: "Whoami", Kind: forgeapi.KindTransient, Retryable: true,
			Message: `SSRF dial: all 1 IPs for "api.github.com" failed: connect: no route to host`,
		}},
		{"throttle", &forgeapi.Error{Op: "Whoami", Kind: forgeapi.KindRateLimited, Status: http.StatusTooManyRequests}},
		{"instance 503", &forgeapi.Error{Op: "Whoami", Kind: forgeapi.KindUpstream, Status: http.StatusServiceUnavailable}},
		{"instance 408", &forgeapi.Error{Op: "Whoami", Kind: forgeapi.KindUpstream, Status: http.StatusRequestTimeout}},
		{"no answer in time", fmt.Errorf("whoami: %w", context.DeadlineExceeded)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core := &whoamiCore{err: tc.err}
			m := recordManager(t, core)

			if err := m.probeConnection(t.Context(), probedID); err == nil {
				t.Fatalf("Probe() over %v = nil, want the failure", tc.err)
			}
			if f := m.get(probedID); !f.Connected || f.ReconnectRequired || f.LastError == "" || f.LastProbed == 0 {
				t.Errorf("row after the failure = %+v, want connected with the error and the probe time recorded", f)
			}
			if err := m.Refresh(t.Context()); err != nil {
				t.Fatalf("Setup: Refresh() = %v", err)
			}
			if f := m.get(probedID); !f.Connected || f.LastError == "" {
				t.Errorf("row after the failure and a Refresh = %+v, want still connected with the error carried", f)
			}

			core.err = nil
			if err := m.probeConnection(t.Context(), probedID); err != nil {
				t.Fatalf("Probe() once the forge answers = %v", err)
			}
			if f := m.get(probedID); !f.Connected || f.LastError != "" || f.ErrorKind != "" || f.RetryAfterS != 0 {
				t.Errorf("row after a successful probe = %+v, want connected with the error cleared", f)
			}
		})
	}
}

func TestManagerProbe_ANonTemporaryFailureDisconnects(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"dead credential", &forgeapi.Error{Op: "Whoami", Kind: forgeapi.KindUnauthorized, Status: http.StatusUnauthorized}},
		{"scope refusal", &forgeapi.Error{
			Op: "Whoami", Code: forgeapi.CodeScopeInsufficient, Kind: forgeapi.KindForbidden, Status: http.StatusForbidden,
		}},
		{"instance 4xx", &forgeapi.Error{Op: "Whoami", Kind: forgeapi.KindUpstream, Status: http.StatusUnprocessableEntity}},
		{"refusal before any request", &forgeapi.Error{
			Op: "Whoami", Code: forgeapi.CodePrivateAddressRefused, Kind: forgeapi.KindUpstream,
		}},
		{"local refusal", &forgeapi.Error{Code: forgeapi.CodeConnectionInvalid, Kind: forgeapi.KindUnknown}},
		{"not a forge failure", errNoClient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := recordManager(t, &whoamiCore{err: tc.err})

			if err := m.probeConnection(t.Context(), probedID); err == nil {
				t.Fatalf("Probe() over %v = nil, want the failure", tc.err)
			}
			if f := m.get(probedID); f.Connected || f.LastError == "" {
				t.Errorf("row after the failure = %+v, want disconnected with the error", f)
			}
			if err := m.Refresh(t.Context()); err != nil {
				t.Fatalf("Setup: Refresh() = %v", err)
			}
			if f := m.get(probedID); f.Connected {
				t.Errorf("row after the failure and a Refresh = %+v, want still disconnected", f)
			}
		})
	}
}
