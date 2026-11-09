package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

const settingsGetWarn = "settings: serving defaults, stored config unreadable"

// The settings GET reads config.json through the one owned-file rule: a held read serves the applied document, a
// settled absence the seed's defaults, and a refused file, or an absence held over one, the defaults with a Warn.
func TestSettingsGet_ReadsThroughTheOwnedFileRule(t *testing.T) {
	poll := func(t *testing.T, dir string) {
		t.Helper()
		if _, _, err := settings.Poll(context.WithoutCancel(t.Context()), dir, func(context.Context, marotte.ServerEvent) {}); err != nil {
			t.Fatalf("Setup: settings.Poll: %v", err)
		}
	}
	remove := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.Remove(filepath.Join(dir, settings.Filename)); err != nil {
			t.Fatal(err)
		}
	}
	truncate := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, settings.Filename), []byte(`{"theme":`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	defaults := settings.EffectiveDefaults().Theme
	for _, tc := range []struct {
		plant func(t *testing.T, dir string)
		name  string
		theme string
		warn  bool
	}{
		{name: "held absence", theme: "dark", plant: func(t *testing.T, dir string) {
			t.Helper()
			remove(t, dir)
			poll(t, dir)
		}},
		{name: "held unparseable", theme: "dark", plant: func(t *testing.T, dir string) {
			t.Helper()
			truncate(t, dir)
			poll(t, dir)
		}},
		{name: "adopted absence", theme: defaults, plant: func(t *testing.T, dir string) {
			t.Helper()
			remove(t, dir)
			poll(t, dir)
			poll(t, dir)
		}},
		{name: "adopted unparseable", theme: defaults, warn: true, plant: func(t *testing.T, dir string) {
			t.Helper()
			truncate(t, dir)
			poll(t, dir)
			poll(t, dir)
		}},
		{name: "held absence after an adopted unparseable read", theme: defaults, warn: true, plant: func(t *testing.T, dir string) {
			t.Helper()
			truncate(t, dir)
			poll(t, dir)
			poll(t, dir)
			remove(t, dir)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			logs := captureLogs(t)
			if _, err := settings.Update(t.Context(), dir, func(doc map[string]json.RawMessage) error {
				doc[settings.KeyTheme] = json.RawMessage(`"dark"`)
				return nil
			}, nil); err != nil {
				t.Fatalf("Setup: seed config.json: %v", err)
			}
			tc.plant(t, dir)

			got := getEffective(t, dir)

			if warned := strings.Contains(logs.String(), settingsGetWarn); got.Theme != tc.theme || warned != tc.warn {
				t.Errorf("GET /api/settings = theme %q, warned %v; want theme %q, warned %v", got.Theme, warned, tc.theme, tc.warn)
			}
		})
	}
}
