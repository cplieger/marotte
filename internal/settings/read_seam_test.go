package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// readSeamShape is one thing config.json can hold at a read. object is the valid document a shape that carries one
// (the over-cap prefix, the BOM, the valid file) wraps.
type readSeamShape struct {
	name     string
	plant    func(t *testing.T, path string, object []byte)
	readable bool // a reader gets the document; otherwise every reader refuses it
	absent   bool
}

func plantBytes(b []byte) func(*testing.T, string, []byte) {
	return func(t *testing.T, path string, _ []byte) {
		t.Helper()
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// readSeamShapes are the inputs at config.json's read seam: one rule decides which of them is one JSON object.
func readSeamShapes() []readSeamShape {
	bom := []byte("\xef\xbb\xbf")
	return []readSeamShape{
		{name: "absent", plant: func(*testing.T, string, []byte) {}, readable: true, absent: true},
		{name: "empty file", plant: plantBytes(nil)},
		{name: "directory", plant: func(t *testing.T, path string, _ []byte) {
			t.Helper()
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "null", plant: plantBytes([]byte("null\n"))},
		{name: "null after a BOM", plant: plantBytes(append(bom, "null"...))},
		{name: "array", plant: plantBytes([]byte(`[{"theme":"dark"}]`))},
		{name: "string", plant: plantBytes([]byte(`"dark"`))},
		{name: "number", plant: plantBytes([]byte(`7`))},
		{name: "truncated", plant: plantBytes([]byte(`{"theme":`))},
		{name: "over the cap with a valid prefix", plant: func(t *testing.T, path string, object []byte) {
			t.Helper()
			padded := append(bytes.Clone(object), bytes.Repeat([]byte(" "), maxBytes)...)
			if err := os.WriteFile(path, padded, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "valid after a BOM", plant: func(t *testing.T, path string, object []byte) {
			t.Helper()
			if err := os.WriteFile(path, append(bom, object...), 0o600); err != nil {
				t.Fatal(err)
			}
		}, readable: true},
		{name: "valid", plant: func(t *testing.T, path string, object []byte) {
			t.Helper()
			if err := os.WriteFile(path, object, 0o600); err != nil {
				t.Fatal(err)
			}
		}, readable: true},
	}
}

// Every reader asks one question of config.json's bytes, so a document Update refuses to merge over is one Read,
// FieldStrict and RetentionEnabled refuse too.
func TestReadSeam_EveryReaderAnswersOneRuleForWhatIsOneObject(t *testing.T) {
	object := []byte(`{"chat_retention_days":0,"theme":"dark"}`)
	for _, shape := range readSeamShapes() {
		t.Run(shape.name, func(t *testing.T) {
			dir := t.TempDir()
			resetCache(t, dir)
			t.Cleanup(func() { resetCache(t, dir) })
			path := filepath.Join(dir, Filename)
			shape.plant(t, path, object)

			doc, _, readErr := Read(t.Context(), dir)
			theme, found, strictErr := FieldStrict[string](t.Context(), dir, KeyTheme)
			stored, storedErr := Stored(dir)
			keeps := RetentionEnabled(t.Context(), dir)

			switch {
			case shape.absent:
				if readErr != nil || strictErr != nil || storedErr != nil || found || len(stored) != 0 {
					t.Errorf("absent: Read err %v, FieldStrict (%q, %v, %v), Stored (%v, %v); want the empty document",
						readErr, theme, found, strictErr, stored, storedErr)
				}
			case shape.readable:
				if readErr != nil || LookupOr(doc, KeyTheme, "") != "dark" {
					t.Errorf("Read = theme %q, err %v; want dark", LookupOr(doc, KeyTheme, ""), readErr)
				}
				if strictErr != nil || !found || theme != "dark" {
					t.Errorf("FieldStrict(theme) = (%q, %v, %v), want (dark, true, nil)", theme, found, strictErr)
				}
				if storedErr != nil || string(stored[KeyTheme]) != `"dark"` {
					t.Errorf("Stored = (%v, %v), want the document", stored, storedErr)
				}
				if keeps {
					t.Error("RetentionEnabled over a stored 0 = true, want false")
				}
			default:
				if readErr == nil {
					t.Errorf("Read answered a readable document %v, want an error", doc)
				}
				if strictErr == nil {
					t.Errorf("FieldStrict(theme) = (%q, %v, nil), want an error", theme, found)
				}
				if storedErr == nil {
					t.Errorf("Stored = %v, want an error", stored)
				}
				if !keeps {
					t.Error("RetentionEnabled over a document no reader may read = false, want true (it fails toward keeping)")
				}
			}
		})
	}
}

// A document that is not one object is refused by Update and left on disk byte for byte.
func TestReadSeam_UpdateRefusesWhatIsNotOneObject(t *testing.T) {
	object := []byte(`{"theme":"dark"}`)
	for _, shape := range readSeamShapes() {
		t.Run(shape.name, func(t *testing.T) {
			dir := t.TempDir()
			resetCache(t, dir)
			t.Cleanup(func() { resetCache(t, dir) })
			path := filepath.Join(dir, Filename)
			shape.plant(t, path, object)
			before, _ := os.ReadFile(path)

			_, err := Update(t.Context(), dir, func(doc map[string]json.RawMessage) error {
				doc[KeyChatRetentionDays] = json.RawMessage(`-1`)
				return nil
			}, nil)

			if shape.readable {
				if err != nil {
					t.Errorf("Update = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, ErrUnreadable) {
				t.Errorf("Update = %v, want ErrUnreadable", err)
			}
			if after, _ := os.ReadFile(path); !bytes.Equal(after, before) {
				t.Errorf("config.json after a refused Update changed (%d bytes, was %d)", len(after), len(before))
			}
		})
	}
}
