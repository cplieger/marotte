package server

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/cplieger/go/slogx/capture"
	"github.com/cplieger/marotte/internal/bridge"
	"github.com/cplieger/marotte/internal/modeltext"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/toolbelt/v3/pin"
)

type testPush struct{}

func (*testPush) RegisterRoutes(routeMux) {}

func TestSafeKiroSetting(t *testing.T) {
	tests := []struct {
		key  string
		want string
	}{
		// Allowed: each has a kiro-cli-SIDE role.
		{"chat.enableKnowledge", "chat.enableKnowledge"},
		{"telemetry.enabled", "telemetry.enabled"},
		{"chat.enableSubagent", "chat.enableSubagent"},
		{"chat.enablePromptHints", "chat.enablePromptHints"},
		{"hooks.showStatus", "hooks.showStatus"},
		{"chat.disableInheritingDefaultResources", "chat.disableInheritingDefaultResources"},
		{"chat.defaultModel", ""},
		{"api.timeout", ""},
		{"arbitrary.key", ""},
		// Removed: TUI-only, absent from kiro-cli 2.12.
		{"chat.enableContextUsageIndicator", ""},
		// Removed: nothing upstream reads them.
		{"chat.enableCheckpoint", ""},
		{"chat.enableTodoList", ""},
		{"compaction.excludeContextWindowPercent", ""},
		{"compaction.excludeMessages", ""},
		{"toolSearch.enabled", ""},
		// Live upstream readers, but reached through internal/kascap's doors, not this endpoint.
		{"chat.disableAutoCompaction", ""},
	}
	for _, tt := range tests {
		got := safeKiroSetting(tt.key)
		if got != tt.want {
			t.Errorf("safeKiroSetting(%q) = %q, want %q", tt.key, got, tt.want)
		}
	}
}

func TestSafeKiroSettingValue(t *testing.T) {
	tests := []struct {
		val  string
		want string
	}{
		{"true", "true"},
		{"false", "false"},
		{"15", "15"},
		{"100", "100"},
		{"0", "0"},
		{"9", "9"},       // single digit '9' sits on the digit upper boundary
		{"1234", "1234"}, // four digits is the inclusive max length
		{"abc", ""},
		{"12.5", ""},
		{"", ""},
		{"12345", ""}, // too long
		{"-1", ""},    // negative
	}
	for _, tt := range tests {
		got := safeKiroSettingValue(tt.val)
		if got != tt.want {
			t.Errorf("safeKiroSettingValue(%q) = %q, want %q", tt.val, got, tt.want)
		}
	}
}

func TestParseKiroSettingOutput(t *testing.T) {
	// kiro-cli appends " (global)" or " (local)" to every non-empty value; unstripped, every
	// enabled toggle rendered off.
	tests := []struct {
		in, want string
	}{
		{"true (global)", "true"},
		{"false (global)", "false"},
		{"true (local)", "true"},
		{"7 (global)", "7"},
		{"true (global)\n", "true"},
		{"  true (global)  ", "true"},
		{"", ""},
		{"true", "true"},
		{"plain-value", "plain-value"},
		// No leading paren: nothing to strip, bare text survives.
		{"name (with parens) (global)", "name (with parens)"},
		// A '(' at index 0 must not trim the whole value.
		{"(foo)", "(foo)"},
	}
	for _, tt := range tests {
		if got := parseKiroSettingOutput(tt.in); got != tt.want {
			t.Errorf("parseKiroSettingOutput(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func FuzzSafeKiroSettingValue(f *testing.F) {
	f.Add("true")
	f.Add("false")
	f.Add("0")
	f.Add("9999")
	f.Add("12345")
	f.Add("")
	f.Add("-1")
	f.Add("abc")
	f.Add("12.5")

	f.Fuzz(func(t *testing.T, v string) {
		got := safeKiroSettingValue(v)
		if got == "" {
			return // rejected — fine
		}
		// Accepted values are "true"/"false" or 1-4 digit non-negative integers.
		if got == "true" || got == "false" {
			return
		}
		if len(got) > 4 || len(got) == 0 {
			t.Fatalf("accepted %q with len %d (want 1-4 digit number or bool)", got, len(got))
		}
		for _, c := range got {
			if c < '0' || c > '9' {
				t.Fatalf("accepted %q containing non-digit %q", got, c)
			}
		}
	})
}

func FuzzParseKiroSettingOutput(f *testing.F) {
	seeds := []string{
		"true (global)", "false (global)", "true (local)",
		"7 (global)", "true (global)\n", "  true (global)  ",
		"", "true", "plain-value",
		"name (with parens) (global)",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		output := parseKiroSettingOutput(input)
		trimmed := strings.TrimSpace(input)
		// Output must be a substring of the trimmed input (no fabrication).
		if output != "" && !strings.Contains(trimmed, output) {
			t.Errorf("output %q is not a substring of trimmed input %q", output, trimmed)
		}
		// Output length must not exceed trimmed input length.
		if len(output) > len(trimmed) {
			t.Errorf("output %q longer than trimmed input %q", output, trimmed)
		}
	})
}

func TestModelHidden(t *testing.T) {
	tests := []struct {
		desc string
		want bool
	}{
		{"A great model for coding", false},
		{"[Deprecated] Old model", true},
		{"[Legacy] Older model", true},
		{"[deprecated] lowercase tag", true},
		{"[legacy] lowercase tag", true},
		{"This model is deprecated in prose", false},
		{"Model [DEPRECATED] uppercase", true},
		{"Model [LEGACY] uppercase", true},
		{"", false},
	}
	for _, tt := range tests {
		got := modeltext.Hidden(tt.desc)
		if got != tt.want {
			t.Errorf("Hidden(%q) = %v, want %v", tt.desc, got, tt.want)
		}
	}
}

func TestSpaHandler(t *testing.T) {
	tests := []struct {
		fs         fstest.MapFS
		name       string
		path       string
		wantBody   string
		wantHeader string
		wantCode   int
	}{
		{
			name: "serves existing file",
			fs: fstest.MapFS{
				"index.html": {Data: []byte("<html>index</html>")},
				"style.css":  {Data: []byte("body{}")},
				"js/app.js":  {Data: []byte("console.log('hi')")},
			},
			path:     "/style.css",
			wantCode: http.StatusOK,
			wantBody: "body{}",
		},
		{
			name: "falls back to index for unknown path",
			fs: fstest.MapFS{
				"index.html": {Data: []byte("<html>index</html>")},
			},
			path:     "/chat/abc123",
			wantCode: http.StatusOK,
			wantBody: "<html>index</html>",
		},
		{
			name: "root serves index",
			fs: fstest.MapFS{
				"index.html": {Data: []byte("<html>root</html>")},
			},
			path:     "/",
			wantCode: http.StatusOK,
			wantBody: "<html>root</html>",
		},
		{
			// HTML is never cached, so a release takes effect on the next load.
			name: "html gets no-store",
			fs: fstest.MapFS{
				"index.html": {Data: []byte("<html></html>")},
			},
			path:       "/",
			wantCode:   http.StatusOK,
			wantHeader: "no-store",
		},
		{
			name: "asset gets revalidation policy",
			fs: fstest.MapFS{
				"index.html": {Data: []byte("<html></html>")},
				"app.js":     {Data: []byte("console.log(1)")},
			},
			path:       "/app.js",
			wantCode:   http.StatusOK,
			wantHeader: "no-cache",
		},
		{
			name: "directory falls back to index",
			fs: fstest.MapFS{
				"index.html": {Data: []byte("<html>fallback</html>")},
				"assets":     {Mode: fs.ModeDir},
			},
			path:     "/assets",
			wantCode: http.StatusOK,
			wantBody: "<html>fallback</html>",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := spaHandler(tt.fs)
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if tt.wantBody != "" && !containsSubstring(rec.Body.String(), tt.wantBody) {
				t.Errorf("body = %q, want to contain %q", rec.Body.String(), tt.wantBody)
			}
			if tt.wantHeader != "" {
				if got := rec.Header().Get("Cache-Control"); got != tt.wantHeader {
					t.Errorf("Cache-Control = %q, want %q", got, tt.wantHeader)
				}
			}
		})
	}
}

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || findSubstring(s, sub))
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// healthReply is the health body.
type healthReply struct {
	Status  string   `json:"status"`
	Reasons []string `json:"reasons"`
}

func probeHealth(t *testing.T, s *Server) (*httptest.ResponseRecorder, healthReply) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handleHealth(rec, httptest.NewRequest(http.MethodGet, healthzPath, nil))
	var got healthReply
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body not JSON: %v; body=%s", err, rec.Body.String())
	}
	return rec, got
}

// TestHandleHealth_body pins the health body byte for byte and its three headers.
func TestHandleHealth_body(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ready bool
		code  int
		want  string
	}{
		{"unhealthy", false, http.StatusServiceUnavailable, `{"status":"unhealthy","reasons":["starting up or shutting down"]}`},
		{"ok", true, http.StatusOK, `{"status":"ok"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{}
			s.ready.Store(tc.ready)
			rec := httptest.NewRecorder()
			s.handleHealth(rec, httptest.NewRequest(http.MethodGet, healthzPath, nil))

			if rec.Code != tc.code {
				t.Errorf("status = %d, want %d", rec.Code, tc.code)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != tc.want {
				t.Errorf("raw health body = %q, want %q", got, tc.want)
			}
			for header, want := range map[string]string{
				"Content-Type":           "application/json",
				"Cache-Control":          "no-store",
				"X-Content-Type-Options": "nosniff",
			} {
				if got := rec.Header().Get(header); got != want {
					t.Errorf("%s = %q, want %q", header, got, want)
				}
			}
		})
	}
}

// TestHandleHealth_DegradedWhenKiroUnready pins the degraded-not-dead start (invariant 6):
// listener up, no usable kiro-cli, 503 with each distinct reason.
func TestHandleHealth_DegradedWhenKiroUnready(t *testing.T) {
	for why, want := range map[pin.Reason]string{
		pin.ReasonInstalling:  reasonInstalling,
		pin.ReasonRetrying:    reasonRetrying,
		pin.ReasonUnavailable: reasonUnavailable,
		pin.ReasonAssertion:   reasonSettings,
		pin.Reason(200):       reasonUnavailable,
	} {
		t.Run(want, func(t *testing.T) {
			s := &Server{kiroReady: func() (bool, pin.Reason) { return false, why }}
			s.ready.Store(true)

			rec, got := probeHealth(t, s)

			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503", rec.Code)
			}
			if got.Status != "unhealthy" || len(got.Reasons) != 1 || got.Reasons[0] != want {
				t.Errorf("body = %+v, want unhealthy with the one reason %q", got, want)
			}
		})
	}
}

// TestHandleHealth_OKWhenKiroReady pins 200 for a ready manager and for no manager, and that
// the verdict is re-read per probe.
func TestHandleHealth_OKWhenKiroReady(t *testing.T) {
	t.Run("manager reports ready", func(t *testing.T) {
		s := &Server{kiroReady: func() (bool, pin.Reason) { return true, pin.ReasonReady }}
		s.ready.Store(true)
		if rec, got := probeHealth(t, s); rec.Code != http.StatusOK || got.Status != "ok" {
			t.Errorf("status = %d, body = %+v, want 200 ok", rec.Code, got)
		}
	})

	t.Run("no manager leaves readiness pure-listener", func(t *testing.T) {
		s := &Server{}
		s.ready.Store(true)
		if rec, got := probeHealth(t, s); rec.Code != http.StatusOK || got.Status != "ok" {
			t.Errorf("status = %d, body = %+v, want 200 ok", rec.Code, got)
		}
	})

	t.Run("recovery is visible on the next probe", func(t *testing.T) {
		ready := false
		s := &Server{kiroReady: func() (bool, pin.Reason) { return ready, pin.ReasonInstalling }}
		s.ready.Store(true)
		if rec, _ := probeHealth(t, s); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503 while installing", rec.Code)
		}
		ready = true
		if rec, got := probeHealth(t, s); rec.Code != http.StatusOK {
			t.Errorf("status = %d, body = %+v, want 200 once the install completed", rec.Code, got)
		}
	})
}

// TestHandleHealth_reasonsKeepThePriorityOrder pins the order of a fault list: not serving,
// then kiro-cli, then sign-in. runtime-health.ts reads only reasons[0].
func TestHandleHealth_reasonsKeepThePriorityOrder(t *testing.T) {
	s := &Server{
		kiroReady:       func() (bool, pin.Reason) { return false, pin.ReasonRetrying },
		authUnavailable: func() bool { return true },
	}

	_, got := probeHealth(t, s)

	want := []string{"starting up or shutting down", "kiro-cli install retrying", "sign-in required"}
	if !slices.Equal(got.Reasons, want) {
		t.Errorf("reasons = %q, want %q", got.Reasons, want)
	}
}

// TestDefaultCLITimeouts pins the production timeout budget. The expectations
// are whole-second / whole-minute magnitudes, so a wrong unit or arithmetic
// slip in defaultCLITimeouts (e.g. 5*time.Second collapsing to a sub-second
// duration) is caught.
func TestDefaultCLITimeouts(t *testing.T) {
	got := defaultCLITimeouts()
	if s := got.Version.Seconds(); s != 2 {
		t.Errorf("defaultCLITimeouts().Version = %v (%.0fs), want 2s", got.Version, s)
	}
	if s := got.Diagnostics.Seconds(); s != 20 {
		t.Errorf("defaultCLITimeouts().Diagnostics = %v (%.0fs), want 20s", got.Diagnostics, s)
	}
	if s := got.Settings.Seconds(); s != 3 {
		t.Errorf("defaultCLITimeouts().Settings = %v (%.0fs), want 3s", got.Settings, s)
	}
}

// TestNotifyPermissionKeyIsUnreachable pins the setting as GONE rather than
// merely hidden: the key is not in the marotte-managed set, so a write carrying
// it is reported as unknown and no reader resolves it.
func TestNotifyPermissionKeyIsUnreachable(t *testing.T) {
	if _, known := settings.KnownKeys["notify_permission"]; known {
		t.Error("notify_permission is still a known settings key; the off switch was only hidden, not removed")
	}
	unknown := settings.WarnUnknownKeys([]string{"notify_permission"}, "TestNotifyPermissionKeyIsUnreachable")
	if len(unknown) != 1 || unknown[0] != "notify_permission" {
		t.Errorf("WarnUnknownKeys([notify_permission]) = %v, want [notify_permission]", unknown)
	}
}

// The default is process-wide, so a test using it must not run in parallel.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	capture.SwapDefault(t, slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return buf
}

func TestCaptureLogs_LeavesTheLogPackageWhereItFoundIt(t *testing.T) {
	sink := &bytes.Buffer{}
	prevWriter, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(sink)
	log.SetFlags(log.LstdFlags)
	t.Cleanup(func() { log.SetOutput(prevWriter); log.SetFlags(prevFlags) })

	t.Run("one capturing test runs and cleans up", func(t *testing.T) { captureLogs(t) })

	slog.Warn("a line after the capture")

	if got := sink.String(); !strings.Contains(got, "a line after the capture") {
		t.Errorf("after captureLogs cleaned up, slog.Warn reached %q, want it in the log package's writer;"+
			" captureLogs left log.Writer pointed at its own buffer", got)
	}
	if got := log.Flags(); got != log.LstdFlags {
		t.Errorf("after captureLogs cleaned up, log.Flags() = %d, want %d", got, log.LstdFlags)
	}
}

// An empty address would have net.Listen pick a random port, which nothing could find.
func TestListenAndServe_RefusesNoListenAddress(t *testing.T) {
	if err := New().ListenAndServe(); err == nil {
		t.Error("ListenAndServe() with no WithListenAddr = nil, want an error")
	}
}

// The drain must outlast the engine's stop, or marotte exits mid-stop and takes the engine's teardown with it, and must
// end inside Docker's default 10 s stop timeout.
func TestShutdownGrace_CoversTheEngineStop(t *testing.T) {
	if ceiling := bridge.StopCeiling(); shutdownGrace <= ceiling {
		t.Errorf("shutdownGrace = %v, want above the engine stop ceiling %v", shutdownGrace, ceiling)
	}
	if dockerStop := 10 * time.Second; shutdownGrace >= dockerStop {
		t.Errorf("shutdownGrace = %v, want under Docker's default stop timeout %v", shutdownGrace, dockerStop)
	}
}
