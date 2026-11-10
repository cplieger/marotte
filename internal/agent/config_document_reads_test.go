package agent

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
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
	object := []byte(`{"engine_heap_mib":4096}`)
	bom := []byte("\xef\xbb\xbf")
	defaults := defaultConfigDocument().engine.heapMiB
	for _, tc := range []struct {
		body   []byte
		name   string
		heap   int
		src    docSource
		absent bool
		dir    bool
		held   bool
	}{
		{name: "absent", absent: true, heap: defaults, src: fromDocument},
		{name: "a_directory", dir: true, heap: defaults, src: fromDefaults, held: true},
		{name: "an_empty_file", body: []byte{}, heap: defaults, src: fromDefaults, held: true},
		{name: "null", body: []byte("null\n"), heap: defaults, src: fromDefaults, held: true},
		{name: "null_after_a_BOM", body: append(slices.Clone(bom), "null"...), heap: defaults, src: fromDefaults, held: true},
		{name: "an_array", body: []byte(`[{"engine_heap_mib":4096}]`), heap: defaults, src: fromDefaults, held: true},
		{name: "a_string", body: []byte(`"4096"`), heap: defaults, src: fromDefaults, held: true},
		{name: "a_number", body: []byte(`4096`), heap: defaults, src: fromDefaults, held: true},
		{name: "truncated", body: []byte(`{"engine_heap_mib":`), heap: defaults, src: fromDefaults, held: true},
		{name: "over_the_cap_with_a_valid_prefix", body: append(slices.Clone(object), bytes.Repeat([]byte(" "), 1<<20)...), heap: defaults, src: fromDefaults, held: true},
		{name: "valid_after_a_BOM", body: append(slices.Clone(bom), object...), heap: 4096, src: fromDocument},
		{name: "valid", body: object, heap: 4096, src: fromDocument},
	} {
		t.Run("a_first_read_of_"+tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, settings.Filename)
			switch {
			case tc.dir:
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case !tc.absent:
				if err := os.WriteFile(path, tc.body, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var l lastReadable
			if doc, src := read(&l, dir); doc.engine.heapMiB != tc.heap || src != tc.src || l.held() != tc.held {
				t.Errorf("read = heap %d from %d, held %v; want heap %d from %d, held %v", doc.engine.heapMiB, src, l.held(), tc.heap, tc.src, tc.held)
			}
		})
	}
}

// pollConfigOnce is one pass of the config.json watch's outside-write check.
func pollConfigOnce(t *testing.T, dir string) {
	t.Helper()
	if _, _, err := settings.Poll(context.WithoutCancel(t.Context()), dir, func(context.Context, marotte.ServerEvent) {}); err != nil {
		t.Fatalf("Setup: settings.Poll: %v", err)
	}
}

// A runtime with a restart queued reads config.json through the one rule: a held read answers the applied document,
// and a refused one, or an absence held over it, keeps the last readable values and holds the restart.
func TestRuntimeConfigDocument_ReadsThroughTheOwnedFileRule(t *testing.T) {
	const truncated = `{"engine_heap_mib":`
	remove := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.Remove(filepath.Join(dir, settings.Filename)); err != nil {
			t.Fatal(err)
		}
	}
	truncate := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, settings.Filename), []byte(truncated), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	defaults := defaultConfigDocument().engine.heapMiB
	for _, tc := range []struct {
		plant func(t *testing.T, dir string)
		name  string
		heap  int
		src   docSource
		held  bool
	}{
		{name: "held absence", heap: 1024, src: fromDocument, plant: func(t *testing.T, dir string) {
			t.Helper()
			remove(t, dir)
			pollConfigOnce(t, dir)
		}},
		{name: "held unparseable", heap: 1024, src: fromDocument, plant: func(t *testing.T, dir string) {
			t.Helper()
			truncate(t, dir)
			pollConfigOnce(t, dir)
		}},
		{name: "adopted absence", heap: defaults, src: fromDocument, plant: func(t *testing.T, dir string) {
			t.Helper()
			remove(t, dir)
			settleConfig(t, dir)
		}},
		{name: "adopted unparseable", heap: 1024, src: fromLastReadable, held: true, plant: func(t *testing.T, dir string) {
			t.Helper()
			rewriteConfigByHand(t, dir, truncated)
		}},
		{name: "held absence after an adopted unparseable read", heap: 1024, src: fromLastReadable, held: true, plant: func(t *testing.T, dir string) {
			t.Helper()
			rewriteConfigByHand(t, dir, truncated)
			remove(t, dir)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, dir, src := restartFixture(t)
			executingLease(t, h, "wf_1")
			pendRestart(t, h, dir, 1024)
			if got := h.engine.status().Restart.Reasons; !slices.Equal(got, []marotte.EngineReason{reasonHeap}) {
				t.Fatalf("Setup: reasons = %v, want [%s]", got, reasonHeap)
			}
			tc.plant(t, dir)

			doc, from := h.configDocument(t.Context())
			if doc.engine.heapMiB != tc.heap || from != tc.src || h.configDoc.held() != tc.held {
				t.Errorf("configDocument = heap %d from %d, held %v; want heap %d from %d, held %v",
					doc.engine.heapMiB, from, h.configDoc.held(), tc.heap, tc.src, tc.held)
			}
			if !tc.held {
				return
			}
			h.runs.disarmDeadline(t.Context(), "wf_1")
			assertNoRestart(t, src, "config.json is refused")
			rec := httptest.NewRecorder()
			h.handleEngineRestart(rec, httptest.NewRequest(http.MethodPost, "/api/engine/restart", nil))
			if rec.Code != http.StatusConflict {
				t.Errorf("POST /api/engine/restart = %d, want 409 while config.json is refused and a reason waits", rec.Code)
			}
		})
	}
}
