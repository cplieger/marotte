package composition

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/policyfile"
	"github.com/cplieger/marotte/internal/securityprofile"
	"github.com/cplieger/marotte/internal/settings"
)

// ownedReadsFixture is config.json as the reader rows start from: a stored -1 (keep every chat), a theme and a
// non-default profile.
const ownedReadsFixture = `{"chat_retention_days":-1,"security_profile":"trusted","theme":"dark"}`

// ownedReadCase plants a read for the reader rows: held is a valid document read once and then removed before any
// pass of the config watch; the others are a first read of bytes no reader may take as a document.
type ownedReadCase struct {
	name  string
	plant func(t *testing.T, dir string)
	held  bool
}

func ownedReadCases() []ownedReadCase {
	return []ownedReadCase{
		{name: "held absence", held: true, plant: func(t *testing.T, dir string) {
			t.Helper()
			path := filepath.Join(dir, settings.Filename)
			writeConfig(t, path, ownedReadsFixture)
			if _, _, err := settings.Read(t.Context(), dir); err != nil {
				t.Fatalf("Setup: first read: %v", err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "null", plant: func(t *testing.T, dir string) {
			t.Helper()
			writeConfig(t, filepath.Join(dir, settings.Filename), "null\n")
		}},
		{name: "over the cap with a valid prefix", plant: func(t *testing.T, dir string) {
			t.Helper()
			padded := append([]byte(`{"chat_retention_days":7}`), bytes.Repeat([]byte(" "), 1<<20)...)
			writeConfig(t, filepath.Join(dir, settings.Filename), string(padded))
		}},
	}
}

func TestOwnedFileReads(t *testing.T) {
	t.Run("reader", func(t *testing.T) {
		for _, tc := range ownedReadCases() {
			t.Run("chatRetention/"+tc.name, func(t *testing.T) {
				dir := t.TempDir()
				tc.plant(t, dir)
				if got := chatRetention(t.Context(), dir); got != 0 {
					t.Errorf("chatRetention = %v, want 0: purge nothing over a stored -1 or a document no reader may read", got)
				}
			})
			t.Run("runRetention/"+tc.name, func(t *testing.T) {
				dir := t.TempDir()
				tc.plant(t, dir)
				if got := runRetention(t.Context(), dir); got.Enabled {
					t.Errorf("runRetention = %+v, want disabled", got)
				}
			})
		}
		t.Run("securityprofile.Active/held absence", func(t *testing.T) {
			dir := t.TempDir()
			ownedReadCases()[0].plant(t, dir)
			store := &securityprofile.Store{ConfigDir: dir}
			if got := store.Active(t.Context()); got != policyfile.ProfileTrusted {
				t.Errorf("Active over a held absence = %q, want the applied %q", got, policyfile.ProfileTrusted)
			}
		})
		t.Run("settings.Update/held absence", func(t *testing.T) {
			dir := t.TempDir()
			ownedReadCases()[0].plant(t, dir)
			if _, err := settings.Update(t.Context(), dir, func(doc map[string]json.RawMessage) error {
				doc[settings.KeyDebugLogs] = json.RawMessage(`true`)
				return nil
			}, nil); err != nil {
				t.Fatalf("Update over a held absence: %v", err)
			}
			stored, err := settings.Stored(dir)
			if err != nil {
				t.Fatalf("Stored after the Update: %v", err)
			}
			for key, want := range map[string]string{
				settings.KeyTheme:             `"dark"`,
				settings.KeyChatRetentionDays: `-1`,
				settings.KeyDebugLogs:         `true`,
			} {
				if got := string(stored[key]); got != want {
					t.Errorf("config.json after an Update over a held absence: %s = %s, want %s (the applied document plus the change)", key, got, want)
				}
			}
		})
	})
}
