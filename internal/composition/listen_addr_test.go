package composition

import (
	"strings"
	"testing"
)

func TestListenAddrFromEnv(t *testing.T) {
	tests := []struct {
		name, env, wantAddr, wantLocal string
	}{
		{"unset is port 9847 on every address", "", ":9847", "127.0.0.1:9847"},
		{"another port on every address", ":9000", ":9000", "127.0.0.1:9000"},
		{"the unspecified IPv4 address", "0.0.0.0:9847", "0.0.0.0:9847", "127.0.0.1:9847"},
		{"one bound address", "192.0.2.10:9847", "192.0.2.10:9847", "192.0.2.10:9847"},
		{"the IPv6 loopback", "[::1]:9847", "[::1]:9847", "[::1]:9847"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(listenAddrVar, tc.env)
			addr, local, err := listenAddrFromEnv()
			if err != nil {
				t.Fatalf("listenAddrFromEnv() with LISTEN_ADDR=%q error = %v", tc.env, err)
			}
			if addr != tc.wantAddr || local != tc.wantLocal {
				t.Errorf("listenAddrFromEnv() with LISTEN_ADDR=%q = (%q, %q), want (%q, %q)",
					tc.env, addr, local, tc.wantAddr, tc.wantLocal)
			}
		})
	}
}

func TestListenAddrFromEnv_RefusesAnAddressTheProbeCannotReach(t *testing.T) {
	tests := []struct{ name, env string }{
		{"no port", "9847"},
		{"an empty port", "localhost:"},
		{"the ephemeral port", ":0"},
		{"a service name", ":http"},
		{"out of range", ":65536"},
		{"a host that makes no URL", "a b:9847"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(listenAddrVar, tc.env)
			_, _, err := listenAddrFromEnv()
			if err == nil {
				t.Fatalf("listenAddrFromEnv() with LISTEN_ADDR=%q error = nil, want a refusal", tc.env)
			}
			if !strings.Contains(err.Error(), "LISTEN_ADDR") {
				t.Errorf("error %q does not name LISTEN_ADDR", err)
			}
		})
	}
}

func TestValidateConfig_ListenAddr(t *testing.T) {
	t.Setenv("KIRO_CONFIG_DIR", t.TempDir())
	t.Setenv("KIRO_WORK_DIR", t.TempDir())

	t.Run("a usable address starts", func(t *testing.T) {
		t.Setenv(listenAddrVar, "127.0.0.1:9000")
		cfg := ConfigFromEnv()
		if cfg.ListenAddr != "127.0.0.1:9000" {
			t.Errorf("ListenAddr = %q, want 127.0.0.1:9000", cfg.ListenAddr)
		}
		if err := validateConfig(t.Context(), &cfg); err != nil {
			t.Errorf("validateConfig with LISTEN_ADDR=127.0.0.1:9000 = %v, want nil", err)
		}
	})

	t.Run("an unusable address stops startup naming the variable", func(t *testing.T) {
		t.Setenv(listenAddrVar, ":http")
		cfg := ConfigFromEnv()
		err := validateConfig(t.Context(), &cfg)
		if err == nil {
			t.Fatal("validateConfig with LISTEN_ADDR=:http = nil, want a fatal error")
		}
		if !strings.Contains(err.Error(), "LISTEN_ADDR") {
			t.Errorf("err = %v, want it to name LISTEN_ADDR", err)
		}
	})
}
