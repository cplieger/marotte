package settings

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func FuzzSettingsField(f *testing.F) {
	f.Add([]byte(`{"foo":"bar"}`), "foo")
	f.Add([]byte(`{"enabled":true}`), "enabled")
	f.Add([]byte(`{"count":42}`), "count")
	f.Add([]byte(`{"nested":{"a":1}}`), "nested")
	f.Add([]byte(`{}`), "missing")
	f.Add([]byte(`null`), "key")
	f.Add([]byte(`"just a string"`), "key")
	f.Add([]byte(``), "key")
	f.Add([]byte(`{"":"empty key"}`), "")

	f.Fuzz(func(t *testing.T, data []byte, key string) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}

		// Reset cache for this dir so each fuzz input is fresh.
		globalCacheMu.Lock()
		delete(globalCaches, dir)
		globalCacheMu.Unlock()

		ctx := t.Context()

		Field[bool](ctx, dir, key)
		Field[int](ctx, dir, key)
		Field[[]string](ctx, dir, key)

		// Field[string] and FieldInto(&string) share one parse path, so they must agree.
		val, okField := Field[string](ctx, dir, key)
		var into string
		okInto := FieldInto(ctx, dir, key, &into)
		if okField != okInto {
			t.Errorf("Field/FieldInto presence disagree for key %q: %v vs %v", key, okField, okInto)
		}
		if okField && val != into {
			t.Errorf("Field/FieldInto value disagree for key %q: %q vs %q", key, val, into)
		}
	})
}

func FuzzSettingsReadBytes(f *testing.F) {
	f.Add([]byte(``))                                 // empty file
	f.Add([]byte(`{}`))                               // valid empty JSON
	f.Add([]byte(`{"key": "value"}`))                 // valid JSON
	f.Add([]byte(`{"truncated`))                      // truncated JSON
	f.Add([]byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0xFD}) // binary content
	f.Add(make([]byte, 4096))                         // zeroed block

	f.Fuzz(func(t *testing.T, data []byte) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}

		globalCacheMu.Lock()
		delete(globalCaches, dir)
		globalCacheMu.Unlock()

		ctx := t.Context()

		got, err := readBytes(ctx, dir)
		if err != nil {
			return
		}

		// A successful read round-trips exactly, capped at MaxBytes.
		expected := data
		if len(expected) > MaxBytes {
			expected = expected[:MaxBytes]
		}
		if !bytes.Equal(got, expected) {
			t.Errorf("readBytes content mismatch: got %d bytes, want %d", len(got), len(expected))
		}
	})
}
