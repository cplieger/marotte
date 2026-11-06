package composition

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/notify"
)

const notifyTestToken = "cfg-notify-token-0123456789-abcdefghij" // gitleaks:allow

func TestNotifyGateFromEnv(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "token")
	if err := os.WriteFile(good, []byte(notifyTestToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	blank := filepath.Join(dir, "blank")
	if err := os.WriteFile(blank, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, env, file string
		on, fails       bool
	}{
		{name: "unset is off", on: false},
		{name: "env channel", env: notifyTestToken, on: true},
		{name: "file channel", file: good, on: true},
		{name: "file wins over env", env: "ignored-but-long-enough-0123456789", file: good, on: true},
		{name: "blank file pointer", file: " ", fails: true},
		{name: "missing file", file: filepath.Join(dir, "absent"), fails: true},
		{name: "blank file", file: blank, fails: true},
		{name: "unclean path", file: dir + "/../token", fails: true},
		{name: "malformed value", env: "short", fails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setOrUnset(t, notify.TokenEnv, tc.env)
			setOrUnset(t, notify.TokenEnv+"_FILE", tc.file)
			gate, err := notifyGateFromEnv()
			if (err != nil) != tc.fails {
				t.Fatalf("err = %v, want fails=%v", err, tc.fails)
			}
			if err != nil {
				if !strings.Contains(err.Error(), notify.TokenEnv) {
					t.Errorf("error %q does not name the key", err)
				}
				for _, leak := range []string{dir, tc.env} {
					if leak != "" && strings.Contains(err.Error(), leak) {
						t.Errorf("error %q echoes %q", err, leak)
					}
				}
				return
			}
			if gate.On() != tc.on {
				t.Errorf("On() = %v, want %v", gate.On(), tc.on)
			}
		})
	}
}

// setOrUnset sets key to value, or unsets it when value is empty; t.Setenv restores either.
func setOrUnset(t *testing.T, key, value string) {
	t.Helper()
	t.Setenv(key, value)
	if value == "" {
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNotifyGateFromEnv_EmptyFilePointerFails(t *testing.T) {
	setOrUnset(t, notify.TokenEnv, notifyTestToken)
	t.Setenv(notify.TokenEnv+"_FILE", "")
	if _, err := notifyGateFromEnv(); err == nil || !strings.Contains(err.Error(), notify.TokenEnv+"_FILE") {
		t.Fatalf("err = %v, want the blank pointer named", err)
	}
}

func TestValidateConfig_FailsOnABadNotifyToken(t *testing.T) {
	cfg := &Config{ConfigDir: t.TempDir(), WorkDir: t.TempDir(), notifyErr: errors.New(notify.TokenEnv + ": bad")}
	err := validateConfig(t.Context(), cfg)
	if err == nil || !strings.Contains(err.Error(), notify.TokenEnv) {
		t.Fatalf("validateConfig = %v, want the notify token error", err)
	}
}

func TestLogNotifyPosture(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	t.Run("off", func(t *testing.T) {
		buf.Reset()
		logNotifyPosture(&Config{})
		if got := strings.Count(buf.String(), "\n"); got != 1 || !strings.Contains(buf.String(), "level=INFO") ||
			!strings.Contains(buf.String(), "off") || !strings.Contains(buf.String(), notify.TokenEnv) {
			t.Fatalf("want one INFO line saying the route is off and naming the key, got %q", buf.String())
		}
	})
	t.Run("on", func(t *testing.T) {
		buf.Reset()
		gate, err := notify.ParseToken(notifyTestToken)
		if err != nil {
			t.Fatal(err)
		}
		logNotifyPosture(&Config{NotifyGate: gate})
		if !strings.Contains(buf.String(), "on") || strings.Contains(buf.String(), notifyTestToken) {
			t.Fatalf("want an on line without the token, got %q", buf.String())
		}
	})
}
