package settings

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func storedRevision(t *testing.T, dir string) string {
	t.Helper()
	return string(readConfig(t, dir)[KeySettingsRevision])
}

func TestUpdate_BumpsTheRevisionOncePerWrite(t *testing.T) {
	dir := t.TempDir()
	for _, want := range []string{"1", "2", "3"} {
		if _, err := Update(t.Context(), dir, setKey(KeyTheme, "dark")); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if got := storedRevision(t, dir); got != want {
			t.Errorf("settings_revision = %s, want %s", got, want)
		}
	}
	if got := Revision(t.Context(), dir); got != 3 {
		t.Errorf("Revision = %d, want 3", got)
	}
}

func TestUpdate_ConcurrentWritesEachBumpTheRevision(t *testing.T) {
	dir := t.TempDir()
	const writers = 8
	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() {
			if _, err := Update(t.Context(), dir, setKey(KeyTheme, "dark")); err != nil {
				t.Errorf("Update: %v", err)
			}
		})
	}
	wg.Wait()
	if got := Revision(t.Context(), dir); got != writers {
		t.Errorf("Revision = %d after %d writes, want %d", got, writers, writers)
	}
}

func TestUpdate_ARefusedWriteKeepsTheRevision(t *testing.T) {
	t.Run("the merge refuses", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, `{"settings_revision":4}`)
		refusal := errors.New("refused")
		if _, err := Update(t.Context(), dir, func(map[string]json.RawMessage) error { return refusal }); !errors.Is(err, refusal) {
			t.Fatalf("Update = %v, want the merge's refusal", err)
		}
		if got := storedRevision(t, dir); got != "4" {
			t.Errorf("settings_revision = %s after a refused write, want 4", got)
		}
	})
	t.Run("the stored document is unreadable", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, `{"settings_revision":4,`)
		if _, err := Update(t.Context(), dir, setKey(KeyTheme, "dark")); !errors.Is(err, ErrUnreadable) {
			t.Fatalf("Update = %v, want ErrUnreadable", err)
		}
		data, err := os.ReadFile(filepath.Join(dir, Filename))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != `{"settings_revision":4,` {
			t.Errorf("config.json = %q after a refused write, want it untouched", data)
		}
	})
}

// The count lives in config.json, so a process that starts over (an empty reader cache) continues it.
func TestRevision_PersistsInConfigJSONAcrossAReload(t *testing.T) {
	dir := t.TempDir()
	for range 2 {
		if _, err := Update(t.Context(), dir, setKey(KeyTheme, "dark")); err != nil {
			t.Fatalf("Update: %v", err)
		}
	}
	globalCacheMu.Lock()
	delete(globalCaches, dir)
	globalCacheMu.Unlock()

	effective, rejected := EffectiveFrom(readConfig(t, dir))
	if len(rejected) > 0 || effective.SettingsRevision != 2 {
		t.Errorf("EffectiveFrom(config.json).SettingsRevision = %d (rejected %v), want 2", effective.SettingsRevision, rejected)
	}
	if got := Revision(t.Context(), dir); got != 2 {
		t.Errorf("Revision after a reload = %d, want 2", got)
	}
	if _, err := Update(t.Context(), dir, setKey(KeyTheme, "light")); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := storedRevision(t, dir); got != "3" {
		t.Errorf("settings_revision after a reload and a write = %s, want 3", got)
	}
}

func TestUpdate_TheRevisionIsNeverTheMergesToSet(t *testing.T) {
	tests := []struct {
		name, seed, want string
		merge            func(map[string]json.RawMessage) error
	}{
		{
			name: "a merge writing its own value",
			seed: `{"settings_revision":7}`,
			merge: func(doc map[string]json.RawMessage) error {
				doc[KeySettingsRevision] = json.RawMessage(`100`)
				return nil
			},
			want: "8",
		},
		{
			name: "a merge deleting it",
			seed: `{"settings_revision":7}`,
			merge: func(doc map[string]json.RawMessage) error {
				delete(doc, KeySettingsRevision)
				return nil
			},
			want: "8",
		},
		{name: "a malformed stored value restarts the count", seed: `{"settings_revision":"seven"}`, merge: setKey(KeyTheme, "dark"), want: "1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, dir, tc.seed)
			if _, err := Update(t.Context(), dir, tc.merge); err != nil {
				t.Fatalf("Update: %v", err)
			}
			if got := storedRevision(t, dir); got != tc.want {
				t.Errorf("settings_revision = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestUpdatedEvent_CarriesTheStoredRevision(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"settings_revision":12}`)
	evt := UpdatedEvent(t.Context(), dir)
	if evt.Type != marotte.EventSettingsUpdated {
		t.Errorf("event type = %q, want %q", evt.Type, marotte.EventSettingsUpdated)
	}
	if p, ok := evt.Payload.(marotte.SettingsUpdatedPayload); !ok || p.SettingsRevision != 12 {
		t.Errorf("payload = %#v, want SettingsUpdatedPayload{SettingsRevision: 12}", evt.Payload)
	}
}
