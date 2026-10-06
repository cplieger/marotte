package settings

import (
	"bytes"
	"encoding/json"
	"log"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func TestEffectiveKeys_AreAllKnown(t *testing.T) {
	// Every effective key must be in KnownKeys, or a GET round-tripped as a PATCH would warn.
	for _, k := range effectiveKeys() {
		if _, ok := KnownKeys[k]; !ok {
			t.Errorf("effectiveKeys includes %q but it is not in KnownKeys", k)
		}
	}
}

// TestEffectiveSettings_EveryFieldIsSettable pins a setter for every field (reflected off the
// json tags, not a second list): an unsettable field would serve its default forever.
func TestEffectiveSettings_EveryFieldIsSettable(t *testing.T) {
	settable := make(map[string]struct{}, len(effectiveKeys()))
	for _, k := range effectiveKeys() {
		settable[k] = struct{}{}
	}
	rt := reflect.TypeFor[marotte.EffectiveSettings]()
	for f := range rt.Fields() {
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if tag == "" || tag == "-" {
			t.Errorf("EffectiveSettings.%s has no json tag; every field is part of the wire", f.Name)
			continue
		}
		if _, ok := settable[tag]; !ok {
			t.Errorf("EffectiveSettings.%s (json %q) has no setter, so a stored value for it is ignored", f.Name, tag)
		}
		// No omitempty: wiregen would emit an OPTIONAL field, letting a client invent a fallback.
		if strings.Contains(f.Tag.Get("json"), "omitempty") {
			t.Errorf("EffectiveSettings.%s carries omitempty; that generates an optional TS field and reopens the client-fallback class", f.Name)
		}
	}
	if got, want := rt.NumField(), len(effectiveKeys()); got != want {
		t.Errorf("EffectiveSettings has %d fields but %d setters; one side gained a key alone", got, want)
	}
}

func TestGuardPayloadLinks_DefaultsOnAndAStoredOffIsHonoured(t *testing.T) {
	if got := EffectiveDefaults().GuardPayloadLinks; !got {
		t.Errorf("EffectiveDefaults().GuardPayloadLinks = %v, want true", got)
	}
	tests := []struct {
		name   string
		stored map[string]json.RawMessage
		want   bool
	}{
		{name: "absent", stored: nil, want: true},
		{name: "stored_false", stored: map[string]json.RawMessage{KeyGuardPayloadLinks: json.RawMessage(`false`)}, want: false},
		{name: "stored_null", stored: map[string]json.RawMessage{KeyGuardPayloadLinks: json.RawMessage(`null`)}, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := EffectiveFrom(tc.stored)
			if got.GuardPayloadLinks != tc.want {
				t.Errorf("EffectiveFrom(%s).GuardPayloadLinks = %v, want %v", tc.name, got.GuardPayloadLinks, tc.want)
			}
		})
	}
}

func TestEffective_MCPWaitForReadyDefaultsOffAndDecodes(t *testing.T) {
	if EffectiveDefaults().MCPWaitForReady {
		t.Error("EffectiveDefaults().MCPWaitForReady = true, want false")
	}
	got, rejected := EffectiveFrom(map[string]json.RawMessage{KeyMCPWaitForReady: json.RawMessage(`true`)})
	if !got.MCPWaitForReady || len(rejected) != 0 {
		t.Errorf("EffectiveFrom(stored true) = %v, rejected %v; want true and nothing rejected", got.MCPWaitForReady, rejected)
	}
	if unknown := WarnUnknownKeys([]string{KeyMCPWaitForReady}, "test"); len(unknown) != 0 {
		t.Errorf("WarnUnknownKeys(%q) = %v, want it known", KeyMCPWaitForReady, unknown)
	}
}

func TestEffective_AutoCompactionDefaultsAndInvalidPctReadsAsDefault(t *testing.T) {
	d := EffectiveDefaults()
	if !d.AutoCompactionEnabled || d.AutoCompactPct != 80 {
		t.Errorf("EffectiveDefaults() compaction = (%v, %d), want (true, 80)", d.AutoCompactionEnabled, d.AutoCompactPct)
	}
	tests := []struct {
		stored string
		want   int
		reject bool
	}{
		{stored: `50`, want: 50},
		{stored: `65`, want: 65},
		{stored: `90`, want: 90},
		{stored: `47`, want: 80, reject: true},
		{stored: `92`, want: 80, reject: true},
		{stored: `83`, want: 80, reject: true},
		{stored: `82.5`, want: 80, reject: true},
		{stored: `"85"`, want: 80, reject: true},
	}
	for _, tc := range tests {
		t.Run(tc.stored, func(t *testing.T) {
			got, rejected := EffectiveFrom(map[string]json.RawMessage{
				KeyAutoCompactPct:        json.RawMessage(tc.stored),
				KeyAutoCompactionEnabled: json.RawMessage(`false`),
			})
			if got.AutoCompactPct != tc.want {
				t.Errorf("EffectiveFrom(auto_compact_pct %s).AutoCompactPct = %d, want %d", tc.stored, got.AutoCompactPct, tc.want)
			}
			if got.AutoCompactionEnabled {
				t.Errorf("EffectiveFrom(auto_compaction_enabled false).AutoCompactionEnabled = true, want false")
			}
			if gotReject := slices.Contains(rejected, KeyAutoCompactPct); gotReject != tc.reject {
				t.Errorf("EffectiveFrom(auto_compact_pct %s) rejected = %v, want rejected %v", tc.stored, rejected, tc.reject)
			}
		})
	}
}

func TestValidateCompactionPatch(t *testing.T) {
	tests := []struct {
		name    string
		patch   map[string]json.RawMessage
		wantErr bool
	}{
		{name: "neither key", patch: map[string]json.RawMessage{KeyTheme: json.RawMessage(`"dark"`)}},
		{name: "valid pct", patch: map[string]json.RawMessage{KeyAutoCompactPct: json.RawMessage(`85`)}},
		{name: "valid switch", patch: map[string]json.RawMessage{KeyAutoCompactionEnabled: json.RawMessage(`false`)}},
		{name: "pct 95", patch: map[string]json.RawMessage{KeyAutoCompactPct: json.RawMessage(`95`)}, wantErr: true},
		{name: "pct 83", patch: map[string]json.RawMessage{KeyAutoCompactPct: json.RawMessage(`83`)}, wantErr: true},
		{name: "pct null", patch: map[string]json.RawMessage{KeyAutoCompactPct: json.RawMessage(`null`)}, wantErr: true},
		{name: "switch string", patch: map[string]json.RawMessage{KeyAutoCompactionEnabled: json.RawMessage(`"no"`)}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateCompactionPatch(tc.patch); (err != nil) != tc.wantErr {
				t.Errorf("ValidateCompactionPatch(%s) = %v, want error %v", tc.name, err, tc.wantErr)
			}
		})
	}
}

func TestWarnUnknownKeys(t *testing.T) {
	tests := []struct {
		name string
		keys []string
		want []string
	}{
		{
			name: "all known returns nil",
			keys: []string{"debug_logs", "chat_retention_days", "supervised_default"},
			want: nil,
		},
		{
			name: "single unknown surfaces",
			keys: []string{"debug_logs", "typo_key"},
			want: []string{"typo_key"},
		},
		{
			name: "multiple unknowns preserve input order",
			keys: []string{"debug_logs", "zzz_typo", "aaa_typo"},
			want: []string{"zzz_typo", "aaa_typo"},
		},
		{
			name: "all unknown",
			keys: []string{"foo", "bar"},
			want: []string{"foo", "bar"},
		},
		{
			name: "empty input returns nil",
			keys: nil,
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := WarnUnknownKeys(tc.keys, "test")
			if !slices.Equal(got, tc.want) {
				t.Errorf("WarnUnknownKeys(%v) = %v, want %v", tc.keys, got, tc.want)
			}
		})
	}
}

// TestKnownKeys_CoversTheClientSurface pins that KnownKeys covers every key of the generated
// client type.
func TestKnownKeys_CoversTheClientSurface(t *testing.T) {
	for _, k := range effectiveKeys() {
		if _, ok := KnownKeys[k]; !ok {
			t.Errorf("KnownKeys missing %q, which the generated client type reads", k)
		}
	}
	// security_profile is read through /api/permissions, not this payload.
	if _, ok := KnownKeys[KeySecurityProfile]; !ok {
		t.Errorf("KnownKeys missing %q", KeySecurityProfile)
	}
	if slices.Contains(effectiveKeys(), KeySecurityProfile) {
		t.Errorf("%q is in the effective view; it is owned by the permissions endpoints, not this payload", KeySecurityProfile)
	}
	// model_effort stays absent: effort is per chat.
	if _, ok := KnownKeys["model_effort"]; ok {
		t.Error("KnownKeys still declares model_effort; effort moved to the chat record")
	}
}

// TestDefaultAgentIgnoreFiles_EmptyAndTheFloorCarriesEnforcement pins the empty default and
// the floor that still enforces, as one test.
func TestDefaultAgentIgnoreFiles_EmptyAndTheFloorCarriesEnforcement(t *testing.T) {
	got := DefaultAgentIgnoreFiles()
	if len(got) != 0 {
		t.Errorf("DefaultAgentIgnoreFiles() = %v, want it empty; a seeded entry filters reads nobody asked to filter", got)
	}
	// Non-nil: nil marshals as null.
	if got == nil {
		t.Error("DefaultAgentIgnoreFiles() returned nil; it marshals as null and the client cannot decode it")
	}

	if list := EffectiveDefaults().AgentIgnoreFiles; !slices.Equal(list, DefaultAgentIgnoreFiles()) {
		t.Errorf("EffectiveDefaults().AgentIgnoreFiles = %v, want %v", list, DefaultAgentIgnoreFiles())
	}

	if list := AgentIgnoreList(DefaultAgentIgnoreFiles()); !slices.Equal(list, []string{AgentIgnoreFloor}) {
		t.Errorf("AgentIgnoreList(default) = %v, want just the floor %q", list, AgentIgnoreFloor)
	}
}

// captureSlog installs a Debug-level slog handler writing to a buffer and restores it, along
// with the log package's writer and flags, which slog.SetDefault also redirects. The default
// is process-wide, so a test using it must not run in parallel.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevLogger, prevWriter, prevFlags := slog.Default(), log.Writer(), log.Flags()
	t.Cleanup(func() {
		slog.SetDefault(prevLogger)
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return &buf
}

// TestWarnUnknownKeys_LogsOnlyWhenUnknownPresent pins one warn iff a key is unknown.
func TestWarnUnknownKeys_LogsOnlyWhenUnknownPresent(t *testing.T) {
	const msg = "settings: unknown keys in write"

	t.Run("all_known_no_warn", func(t *testing.T) {
		buf := captureSlog(t)
		got := WarnUnknownKeys([]string{KeySupervisedDefault, KeyDebugLogs}, "test-src")
		if len(got) != 0 {
			t.Errorf("WarnUnknownKeys(all known) = %v, want empty", got)
		}
		if strings.Contains(buf.String(), msg) {
			t.Errorf("warned with zero unknown keys; log=%q", buf.String())
		}
	})

	t.Run("one_unknown_warns", func(t *testing.T) {
		buf := captureSlog(t)
		got := WarnUnknownKeys([]string{"bogus_key"}, "test-src")
		if len(got) != 1 || got[0] != "bogus_key" {
			t.Errorf("WarnUnknownKeys(one unknown) = %v, want [bogus_key]", got)
		}
		if !strings.Contains(buf.String(), msg) {
			t.Errorf("did not warn for an unknown key; log=%q", buf.String())
		}
	})
}
