package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/policyfile"
	"github.com/cplieger/marotte/internal/settings"
)

func writeProfileSetting(t *testing.T, dir, value string) {
	t.Helper()
	body := map[string]any{}
	if value != "" {
		body[settings.KeySecurityProfile] = value
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
}

// TestSecurityPresets_ResolvesEachProfile pins each profile's preset set; Custom resolves to none, withholding the wire key.
func TestSecurityPresets_ResolvesEachProfile(t *testing.T) {
	for _, p := range policyfile.Profiles() {
		t.Run(p.ID, func(t *testing.T) {
			dir := t.TempDir()
			writeProfileSetting(t, dir, p.ID)
			got := securityPresets(t.Context(), dir)
			if !slices.Equal(got, p.Presets) {
				t.Errorf("securityPresets(%q) = %v, want %v", p.ID, got, p.Presets)
			}
		})
	}
}

// TestSecurityPresets_FallsBackLoudlyNotSilently pins that an unknown id resolves to the default, never to Custom's empty set.
func TestSecurityPresets_FallsBackLoudlyNotSilently(t *testing.T) {
	fallback, ok := policyfile.ProfileFor(policyfile.DefaultProfile)
	if !ok {
		t.Fatal("DefaultProfile does not resolve; the fallback itself is broken")
	}
	for _, value := range []string{"", "nonexistent", "Guarded", "yolo"} {
		t.Run("value="+value, func(t *testing.T) {
			dir := t.TempDir()
			writeProfileSetting(t, dir, value)
			got := securityPresets(t.Context(), dir)
			if len(got) == 0 {
				t.Fatalf("securityPresets(%q) returned nothing; that is the Custom wire and would drop the fs_read floor", value)
			}
			if !slices.Equal(got, fallback.Presets) {
				t.Errorf("securityPresets(%q) = %v, want the default profile's %v", value, got, fallback.Presets)
			}
		})
	}
}

// TestSecurityPresets_AbsentConfigResolvesToDefault covers a first boot, a different reader path.
func TestSecurityPresets_AbsentConfigResolvesToDefault(t *testing.T) {
	fallback, _ := policyfile.ProfileFor(policyfile.DefaultProfile)
	got := securityPresets(t.Context(), t.TempDir())
	if !slices.Equal(got, fallback.Presets) {
		t.Errorf("securityPresets with no config.json = %v, want %v", got, fallback.Presets)
	}
}

// TestSecurityPresets_CallerCannotMutateTheProfile pins that the returned slice must not alias the profile.
func TestSecurityPresets_CallerCannotMutateTheProfile(t *testing.T) {
	dir := t.TempDir()
	writeProfileSetting(t, dir, policyfile.ProfileTrusted)
	first := securityPresets(t.Context(), dir)
	if len(first) == 0 {
		t.Fatal("trusted resolved to nothing")
	}
	for i := range first {
		first[i] = "tampered"
	}
	second := securityPresets(t.Context(), dir)
	if slices.Contains(second, "tampered") {
		t.Error("securityPresets hands out the package's own slice; one caller's write reaches every later session")
	}
}
