package settings

import (
	"testing"
)

func TestGeneration_HoldsStillWhileTheFileDoesAndMovesWhenItChanges(t *testing.T) {
	cases := []struct {
		name  string
		write string
	}{
		{name: "absent file"},
		{name: "present file", write: `{"debug_logs":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			resetCache(t, dir)
			if tc.write != "" {
				writeSettings(t, dir, []byte(tc.write))
			}
			first, ok := Generation(t.Context(), dir)
			if !ok {
				t.Fatalf("Generation(%s) readable = false, want true", tc.name)
			}
			if _, _, err := FieldStrict[bool](t.Context(), dir, KeyDebugLogs); err != nil {
				t.Fatalf("FieldStrict: %v", err)
			}
			if again, _ := Generation(t.Context(), dir); again != first {
				t.Errorf("Generation over an unchanged %s = %d after %d, want it to hold still", tc.name, again, first)
			}

			writeSettings(t, dir, []byte(`{"debug_logs":false,"theme":"dark"}`))
			if moved, _ := Generation(t.Context(), dir); moved == first {
				t.Errorf("Generation after a rewrite = %d, the same as before it", moved)
			}
		})
	}
}

func TestGeneration_AnUnparseableDocumentIsNotReadable(t *testing.T) {
	dir := t.TempDir()
	resetCache(t, dir)
	writeSettings(t, dir, []byte(`{"debug_logs":`))

	if _, ok := Generation(t.Context(), dir); ok {
		t.Error("Generation over a truncated config.json reports readable")
	}
}

func TestParsedMap_AParseOfOlderBytesIsNotServedForANewerGeneration(t *testing.T) {
	dir := t.TempDir()
	resetCache(t, dir)
	writeSettings(t, dir, []byte(`{"theme":"old"}`))
	older := readGeneration(t.Context(), dir)
	writeSettings(t, dir, []byte(`{"theme":"newer one"}`))
	if newer := readGeneration(t.Context(), dir); newer.gen == older.gen {
		t.Fatalf("setup: the rewrite stored no new generation (%d)", newer.gen)
	}

	if _, err := parsedOf(dir, older); err != nil {
		t.Fatalf("parsedOf(older): %v", err)
	}

	got, _, err := FieldStrict[string](t.Context(), dir, KeyTheme)
	if err != nil || got != "newer one" {
		t.Errorf("FieldStrict(theme) = %q, %v, want %q from the generation on disk", got, err, "newer one")
	}
}
