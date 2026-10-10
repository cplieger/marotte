package push

import (
	"encoding/base64"
	"maps"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/slogx/capture"
)

func TestNew_GeneratesKeys(t *testing.T) {
	dir := t.TempDir()
	s := New(t.Context(), dir, "mailto:test@example.com")

	if s.publicKey() == "" {
		t.Fatal("public key is empty")
	}
	raw, err := base64.RawURLEncoding.DecodeString(s.publicKey())
	if err != nil {
		t.Fatalf("decode public key: %v", err)
	}
	if len(raw) != 65 {
		t.Errorf("public key length = %d, want 65 (uncompressed P-256)", len(raw))
	}
	if raw[0] != 0x04 {
		t.Errorf("public key prefix = 0x%02x, want 0x04", raw[0])
	}
}

// TestNew_ClientTimeout pins the 10-second HTTP client timeout New
// configures (an integer-division slip would zero it out).
func TestNew_ClientTimeout(t *testing.T) {
	s := New(t.Context(), t.TempDir(), testSubject)
	defer s.Close()

	if s.client.Timeout != 10*time.Second {
		t.Errorf("client.Timeout = %v, want %v", s.client.Timeout, 10*time.Second)
	}
}

func TestSubscribeUnsubscribe(t *testing.T) {
	dir := t.TempDir()
	s := New(t.Context(), dir, "mailto:test@example.com")
	defer s.Close()

	sub := marotte.PushSubscription{Endpoint: "https://push.example.com/1"}
	sub.Keys.P256dh = "dGVzdA"
	sub.Keys.Auth = "YXV0aA"

	s.Subscribe(sub)
	if !s.HasSubscribers() {
		t.Error("expected subscribers after subscribe")
	}

	s.unsubscribe("https://push.example.com/1")
	if s.HasSubscribers() {
		t.Error("expected no subscribers after unsubscribe")
	}
}

func TestSubscribe_OverwritesDuplicate(t *testing.T) {
	dir := t.TempDir()
	s := New(t.Context(), dir, "mailto:test@example.com")
	defer s.Close()

	sub1 := marotte.PushSubscription{Endpoint: "https://push.example.com/1"}
	sub1.Keys.Auth = "old"
	s.Subscribe(sub1)

	sub2 := marotte.PushSubscription{Endpoint: "https://push.example.com/1"}
	sub2.Keys.Auth = "new"
	s.Subscribe(sub2)

	s.mu.Lock()
	count := len(s.subs)
	auth := s.subs["https://push.example.com/1"].Keys.Auth
	s.mu.Unlock()

	if count != 1 {
		t.Errorf("count = %d, want 1 (should overwrite)", count)
	}
	if auth != "new" {
		t.Errorf("auth = %q, want 'new'", auth)
	}
}

// TestSubscribe_HostLogging verifies the logged host: the endpoint's host when it has
// one, else "unknown". The log is this branch's only observable.
func TestSubscribe_HostLogging(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		wantHost string
	}{
		{
			name:     "non_empty_host_logged_verbatim",
			endpoint: "https://fcm.googleapis.com/fcm/send/sub",
			wantHost: "fcm.googleapis.com",
		},
		{
			name:     "empty_host_keeps_unknown",
			endpoint: "mailto:someone@example.com",
			wantHost: "unknown",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(t.Context(), t.TempDir(), testSubject)
			defer s.Close() // drain writeLoop before TempDir cleanup

			// Install capture AFTER New so its "push: ready" line is excluded.
			capLog := capture.Default(t)
			s.Subscribe(marotte.PushSubscription{Endpoint: tt.endpoint})

			got, ok := capLog.AttrValue("push: subscribed", "host")
			if !ok {
				t.Fatalf("Subscribe(%q) did not emit a %q log line",
					tt.endpoint, "push: subscribed")
			}
			if got != tt.wantHost {
				t.Errorf("Subscribe(%q) logged host = %v, want %q",
					tt.endpoint, got, tt.wantHost)
			}
		})
	}
}

// TestService_WantsNeedsKindEnabledAndASubscription pins that a subscription alone is not
// enough while the kind is off (the PR-checks default), and neither is the kind alone.
func TestService_WantsNeedsKindEnabledAndASubscription(t *testing.T) {
	sub := marotte.PushSubscription{Endpoint: "https://fcm.googleapis.com/fcm/send/1"}
	cases := []struct {
		name    string
		kind    marotte.PushKind
		enable  bool
		sub     bool
		healthy bool
		want    bool
	}{
		{name: "EnabledWithASubscription", kind: marotte.PushKindPRStatus, enable: true, sub: true, healthy: true, want: true},
		{name: "SubscriptionButKindOff", kind: marotte.PushKindPRStatus, enable: false, sub: true, healthy: true},
		{name: "KindOnButNoSubscription", kind: marotte.PushKindPRStatus, enable: true, sub: false, healthy: true},
		{name: "UnhealthyService", kind: marotte.PushKindPRStatus, enable: true, sub: true, healthy: false},
		{name: "UnknownKind", kind: marotte.PushKind("not_a_kind"), enable: true, sub: true, healthy: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New(t.Context(), t.TempDir(), testSubject)
			defer s.Close()
			s.SetPreferences(map[marotte.PushKind]bool{tc.kind: tc.enable})
			if tc.sub {
				s.Subscribe(sub)
			}
			s.mu.Lock()
			s.healthy = tc.healthy
			s.mu.Unlock()
			if got := s.Wants(tc.kind); got != tc.want {
				t.Errorf("Wants(%q) with enabled=%v subscribed=%v healthy=%v = %v, want %v",
					tc.kind, tc.enable, tc.sub, tc.healthy, got, tc.want)
			}
		})
	}
	t.Run("DefaultPreferenceIsOffForPRStatus", func(t *testing.T) {
		s := New(t.Context(), t.TempDir(), testSubject)
		defer s.Close()
		s.Subscribe(sub)
		if s.Wants(marotte.PushKindPRStatus) {
			t.Error("Wants(pr_status) on a fresh install with a subscription = true, want false (the kind defaults off)")
		}
	})
	t.Run("UnsubscribingClosesIt", func(t *testing.T) {
		s := New(t.Context(), t.TempDir(), testSubject)
		defer s.Close()
		s.SetPreferences(map[marotte.PushKind]bool{marotte.PushKindPRStatus: true})
		s.Subscribe(sub)
		s.unsubscribe(sub.Endpoint)
		if s.Wants(marotte.PushKindPRStatus) {
			t.Error("Wants(pr_status) after the last subscription left = true, want false")
		}
	})
}

func TestSetPreferences(t *testing.T) {
	dir := t.TempDir()
	s := New(t.Context(), dir, "mailto:test@example.com")

	s.mu.Lock()
	if !s.prefs[marotte.PushKindAgentFinished] || !s.prefs[marotte.PushKindPermission] {
		t.Error("default preferences should be true")
	}
	if s.prefs[marotte.PushKindPRStatus] {
		t.Error("pr_status defaults on; it is the one keyed kind whose default is OFF, so a fresh install sends no pull-request pushes")
	}
	s.mu.Unlock()

	s.SetPreferences(map[marotte.PushKind]bool{
		marotte.PushKindAgentFinished: false,
		marotte.PushKindPermission:    true,
	})
	s.mu.Lock()
	if s.prefs[marotte.PushKindAgentFinished] {
		t.Error("agentFinished should be false")
	}
	if !s.prefs[marotte.PushKindPermission] {
		t.Error("permissionNeeded should be true")
	}
	s.mu.Unlock()
}

func TestLoadPreferences(t *testing.T) {
	tests := []struct {
		name     string
		settings string // empty means no file written
		wantAF   bool
		wantPN   bool
	}{
		{"MissingFileKeepsDefaults", "", true, true},
		{"MalformedJSONFallsBackToDefaults", `{not json`, true, true},
		{"PartialJSONOnlyAgentFinished", `{"notify_agent_finished":false}`, false, true},
		{"EmptyObjectKeepsDefaults", `{}`, true, true},
		// The permission kind has no settings key, so a notify_permission value is read by nothing.
		{"StaleNotifyPermissionIsIgnored", `{"notify_permission":false}`, true, true},
		{"StaleKeyDoesNotBleedIntoAgentFinished", `{"notify_agent_finished":false,"notify_permission":false}`, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.settings != "" {
				if err := os.WriteFile(filepath.Join(dir, "config.json"),
					[]byte(tt.settings), 0o644); err != nil {
					t.Fatalf("write settings: %v", err)
				}
			}

			s := New(t.Context(), dir, "mailto:test@example.com")

			s.mu.Lock()
			af, pn := s.prefs[marotte.PushKindAgentFinished], s.prefs[marotte.PushKindPermission]
			s.mu.Unlock()
			if af != tt.wantAF || pn != tt.wantPN {
				t.Errorf("agentFinished=%v permissionNeeded=%v, want %v %v",
					af, pn, tt.wantAF, tt.wantPN)
			}
		})
	}
}

// TestClose_CancelsInternalContext verifies Close cancels the service's
// internal context.
func TestClose_CancelsInternalContext(t *testing.T) {
	dir := t.TempDir()
	s := New(t.Context(), dir, "mailto:test@example.com")

	select {
	case <-s.lifetime.Done():
		t.Fatal("context already Done before Close")
	default:
	}

	s.Close()

	select {
	case <-s.lifetime.Done():
	case <-time.After(100 * time.Millisecond):
		t.Error("context not Done after Close")
	}
}

// TestClose_IsIdempotent verifies a second Close does not panic.
func TestClose_IsIdempotent(t *testing.T) {
	dir := t.TempDir()
	s := New(t.Context(), dir, "mailto:test@example.com")
	s.Close()
	s.Close()
}

func TestResolvePreferences_ResolvesEveryKeyedKindAgainstItsOwnDefault(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want map[marotte.PushKind]bool
	}{
		{
			name: "no_file_takes_each_registry_default",
			want: map[marotte.PushKind]bool{
				marotte.PushKindAgentFinished: true, marotte.PushKindPermission: true,
				marotte.PushKindPRStatus: false, marotte.PushKindRunOutcome: true,
			},
		},
		{
			name: "each_stored_toggle_moves_only_its_kind",
			body: `{"notify_run_outcome":false,"notify_pr_status":true}`,
			want: map[marotte.PushKind]bool{
				marotte.PushKindAgentFinished: true, marotte.PushKindPermission: true,
				marotte.PushKindPRStatus: true, marotte.PushKindRunOutcome: false,
			},
		},
		{
			name: "a_malformed_toggle_takes_its_kind_default",
			body: `{"notify_agent_finished":"nonsense","notify_pr_status":"nonsense"}`,
			want: map[marotte.PushKind]bool{
				marotte.PushKindAgentFinished: true, marotte.PushKindPermission: true,
				marotte.PushKindPRStatus: false, marotte.PushKindRunOutcome: true,
			},
		},
		{
			name: "the_master_switch_off_silences_every_registered_kind",
			body: `{"notifications_enabled":false,"notify_agent_finished":true,"notify_pr_status":true}`,
			want: map[marotte.PushKind]bool{
				marotte.PushKindAgentFinished: false, marotte.PushKindPermission: false,
				marotte.PushKindPRStatus: false, marotte.PushKindRunOutcome: false,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolvePreferences(t.Context(), seedConfig(t, tc.body))
			if !maps.Equal(got, tc.want) {
				t.Errorf("ResolvePreferences over %q = %v, want %v", tc.body, got, tc.want)
			}
			if len(got) != len(Kinds()) {
				t.Errorf("ResolvePreferences carries %d kinds, want the registry's %d", len(got), len(Kinds()))
			}
		})
	}
}

func TestPreferences_ReportsTheTogglesInForceAsACopy(t *testing.T) {
	s := New(t.Context(), seedConfig(t, `{"notify_pr_status":true}`), "mailto:test@example.com")
	t.Cleanup(s.Close)
	s.SetPreferences(map[marotte.PushKind]bool{marotte.PushKindRunOutcome: false})

	got := s.Preferences()
	want := map[marotte.PushKind]bool{
		marotte.PushKindAgentFinished: true, marotte.PushKindPermission: true,
		marotte.PushKindPRStatus: true, marotte.PushKindRunOutcome: false,
	}
	if !maps.Equal(got, want) {
		t.Errorf("Preferences() over notify_pr_status true then run_outcome set off = %v, want %v", got, want)
	}
	got[marotte.PushKindAgentFinished] = false
	if !s.Preferences()[marotte.PushKindAgentFinished] {
		t.Error("a write to the map Preferences returned changed the service's toggles")
	}
}
