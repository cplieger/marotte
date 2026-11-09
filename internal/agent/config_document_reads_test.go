package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/settings"
)

// The agent's config document is one more reader of config.json's one rule: a read the rule holds answers the applied
// document, and bytes the rule refuses are unreadable however they parse.
func TestLastReadable_ReadsThroughTheOwnedFileRule(t *testing.T) {
	read := func(l *lastReadable, dir string) (configDocument, docSource) {
		return l.read(t.Context(), dir, readConfigDocument)
	}
	t.Run("a_held_absence_answers_the_applied_document", func(t *testing.T) {
		var l lastReadable
		dir := writeSettings(t, `{"engine_heap_mib":2048}`)
		read(&l, dir)
		if err := os.Remove(filepath.Join(dir, settings.Filename)); err != nil {
			t.Fatal(err)
		}
		if doc, src := read(&l, dir); src != fromDocument || doc.engine.heapMiB != 2048 || l.held() {
			t.Errorf("read = heap %d from %d, held %v, want the applied 2048 from the document, not held", doc.engine.heapMiB, src, l.held())
		}
	})
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{name: "null", body: []byte("null\n")},
		{name: "over_the_cap_with_a_valid_prefix", body: append([]byte(`{"engine_heap_mib":4096}`), bytes.Repeat([]byte(" "), 1<<20)...)},
	} {
		t.Run("a_first_read_of_"+tc.name+"_is_unreadable", func(t *testing.T) {
			var l lastReadable
			if _, src := read(&l, writeSettings(t, string(tc.body))); src != fromDefaults || !l.held() {
				t.Errorf("read = from %d, held %v, want the defaults, held", src, l.held())
			}
		})
	}
}
