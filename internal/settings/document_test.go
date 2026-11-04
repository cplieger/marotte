package settings

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRead_TheGenerationHoldsStillWhileTheFileDoesAndMovesWhenItChanges(t *testing.T) {
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
			_, first, err := Read(t.Context(), dir)
			if err != nil {
				t.Fatalf("Read(%s) = %v, want readable", tc.name, err)
			}
			if _, _, err := FieldStrict[bool](t.Context(), dir, KeyDebugLogs); err != nil {
				t.Fatalf("FieldStrict: %v", err)
			}
			if _, again, _ := Read(t.Context(), dir); again != first {
				t.Errorf("generation over an unchanged %s = %d after %d, want it to hold still", tc.name, again, first)
			}

			writeSettings(t, dir, []byte(`{"debug_logs":false,"theme":"dark"}`))
			if _, moved, _ := Read(t.Context(), dir); moved == first {
				t.Errorf("generation after a rewrite = %d, the same as before it", moved)
			}
		})
	}
}

func TestRead_AnUnparseableDocumentAnswersItsGeneration(t *testing.T) {
	dir := t.TempDir()
	resetCache(t, dir)
	writeSettings(t, dir, []byte(`{"debug_logs":`))

	_, gen, err := Read(t.Context(), dir)
	if !errors.Is(err, ErrUnparseable) {
		t.Errorf("Read over a truncated config.json = %v, want ErrUnparseable", err)
	}
	if gen == 0 {
		t.Error("Read over a truncated config.json answered generation 0, want the generation its bytes were stored under")
	}
}

// A directory at config.json stats fine and is refused by the regular-file open on every read, root included.
func TestRead_AReadErrorAnswersGenerationZero(t *testing.T) {
	dir := t.TempDir()
	resetCache(t, dir)
	if err := os.Mkdir(filepath.Join(dir, Filename), 0o700); err != nil {
		t.Fatal(err)
	}

	_, gen, err := Read(t.Context(), dir)
	if err == nil || errors.Is(err, ErrUnparseable) {
		t.Errorf("Read over a directory at config.json = %v, want a read error", err)
	}
	if gen != 0 {
		t.Errorf("Read over a read error answered generation %d, want 0", gen)
	}
}

func TestLookup_AbsentPresentAndUndecodable(t *testing.T) {
	dir := t.TempDir()
	resetCache(t, dir)
	writeSettings(t, dir, []byte(`{"theme":"dark","auto_compact_pct":"many"}`))
	d, _, err := Read(t.Context(), dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	t.Run("absent", func(t *testing.T) {
		if v, found, err := Lookup[string](d, KeyDebugLogs); found || err != nil || v != "" {
			t.Errorf("Lookup(absent) = %q, %v, %v, want zero, false, nil", v, found, err)
		}
	})
	t.Run("present", func(t *testing.T) {
		if v, found, err := Lookup[string](d, KeyTheme); !found || err != nil || v != "dark" {
			t.Errorf("Lookup(theme) = %q, %v, %v, want dark, true, nil", v, found, err)
		}
	})
	t.Run("undecodable", func(t *testing.T) {
		if _, found, err := Lookup[int](d, KeyAutoCompactPct); found || !errors.Is(err, errKeyParse) {
			t.Errorf("Lookup(a string as int) = %v, %v, want not found and errKeyParse", found, err)
		}
	})
	t.Run("defaults", func(t *testing.T) {
		if got := LookupOr(d, KeyAutoCompactPct, 80); got != 80 {
			t.Errorf("LookupOr(undecodable) = %d, want the default 80", got)
		}
		if got := LookupOr(d, KeyDebugLogs, true); !got {
			t.Error("LookupOr(absent) = false, want the default true")
		}
		var n int
		if d.Into(KeyAutoCompactPct, &n) || d.Into(KeyDebugLogs, &n) {
			t.Error("Into answered true for an undecodable or an absent key")
		}
	})
	t.Run("the_zero_document_is_absent", func(t *testing.T) {
		if _, found, err := Lookup[string](Document{}, KeyTheme); found || err != nil {
			t.Errorf("Lookup over the zero Document = %v, %v, want absent", found, err)
		}
	})
}

func TestParsedOf_AParseOfOlderBytesIsNotServedForANewerGeneration(t *testing.T) {
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
