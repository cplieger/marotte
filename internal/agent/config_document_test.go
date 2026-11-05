package agent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/cplieger/go/slogx/capture"
	"github.com/cplieger/marotte/internal/settings"
)

// docAt is the readable document config.json in dir holds now.
func docAt(t *testing.T, dir string) settings.Document {
	t.Helper()
	d, _, err := settings.Read(t.Context(), dir)
	if err != nil {
		t.Fatalf("Setup: read %s: %v", dir, err)
	}
	return d
}

// heapDoc is a configDocument marked by its heap.
func heapDoc(heap int) *configDocument {
	d := defaultConfigDocument()
	d.engine.heapMiB = heap
	return &d
}

// replaceWithDirectory puts a directory at dir's config.json: it stats fine and the regular-file open refuses it on
// every read, root included.
func replaceWithDirectory(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, settings.Filename)
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

// restoreFile replaces whatever is at dir's config.json with body.
func restoreFile(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(dir, settings.Filename)); err != nil {
		t.Fatal(err)
	}
	rewriteConfigByHand(t, dir, body)
}

func TestLastReadable(t *testing.T) {
	read := func(t *testing.T, l *lastReadable, ctx context.Context, dir string) (configDocument, docSource) {
		t.Helper()
		return l.read(ctx, dir, readConfigDocument)
	}
	t.Run("a_readable_read_is_kept", func(t *testing.T) {
		var l lastReadable
		dir := writeSettings(t, `{"engine_heap_mib":2048}`)
		if doc, src := read(t, &l, t.Context(), dir); src != fromDocument || doc.engine.heapMiB != 2048 || l.held() {
			t.Errorf("read = heap %d from %d, held %v, want 2048 from the document, not held", doc.engine.heapMiB, src, l.held())
		}
	})
	t.Run("an_unparseable_document_answers_the_last_readable_values", func(t *testing.T) {
		var l lastReadable
		dir := writeSettings(t, `{"engine_heap_mib":2048}`)
		read(t, &l, t.Context(), dir)
		rewriteConfigByHand(t, dir, `{"engine_heap_mib":`)
		if doc, src := read(t, &l, t.Context(), dir); src != fromLastReadable || doc.engine.heapMiB != 2048 || !l.held() {
			t.Errorf("read = heap %d from %d, held %v, want the last readable 2048, held", doc.engine.heapMiB, src, l.held())
		}
	})
	t.Run("with_none_readable_yet_the_defaults", func(t *testing.T) {
		var l lastReadable
		doc, src := read(t, &l, t.Context(), writeSettings(t, `{not json`))
		if src != fromDefaults || !l.held() {
			t.Errorf("read = from %d, held %v, want the defaults, held", src, l.held())
		}
		if absent := readConfigDocument(docAt(t, t.TempDir())); !reflect.DeepEqual(doc, absent) {
			t.Errorf("the defaults = %+v, want what an absent config.json gives, %+v", doc, absent)
		}
	})
	t.Run("an_absent_document_is_readable_defaults", func(t *testing.T) {
		var l lastReadable
		if doc, src := read(t, &l, t.Context(), t.TempDir()); src != fromDocument || doc.engine.heapMiB != 0 || l.held() {
			t.Errorf("read of no config.json = heap %d from %d, held %v, want the defaults as the document's answer", doc.engine.heapMiB, src, l.held())
		}
	})
	t.Run("a_cancelled_caller_still_reads_the_document", func(t *testing.T) {
		var l lastReadable
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if doc, src := read(t, &l, ctx, writeSettings(t, `{"engine_heap_mib":2048}`)); src != fromDocument || doc.engine.heapMiB != 2048 {
			t.Errorf("read under a cancelled ctx = heap %d from %d, want the document's 2048", doc.engine.heapMiB, src)
		}
	})
	t.Run("an_unreadable_document_runs_no_field_reader", func(t *testing.T) {
		var l lastReadable
		calls := 0
		l.read(t.Context(), writeSettings(t, `{not json`), func(d settings.Document) configDocument {
			calls++
			return readConfigDocument(d)
		})
		if calls != 0 {
			t.Errorf("the field readers ran %d times over an unparseable config.json, want 0", calls)
		}
	})
	t.Run("a_read_error_holds_and_decodes_nothing", func(t *testing.T) {
		var l lastReadable
		dir := writeSettings(t, `{"engine_heap_mib":2048}`)
		read(t, &l, t.Context(), dir)
		replaceWithDirectory(t, dir)
		calls := 0
		doc, src := l.read(t.Context(), dir, func(d settings.Document) configDocument {
			calls++
			return readConfigDocument(d)
		})
		if calls != 0 || src != fromLastReadable || doc.engine.heapMiB != 2048 || !l.held() {
			t.Errorf("read over a read error = heap %d from %d, held %v, %d decodes, want the last readable 2048, held, no decode",
				doc.engine.heapMiB, src, l.held(), calls)
		}
		restoreFile(t, dir, `{"engine_heap_mib":2048}`)
		if _, src := read(t, &l, t.Context(), dir); src != fromDocument || l.held() {
			t.Errorf("read after the repair = from %d, held %v, want the document, released", src, l.held())
		}
	})
}

// No reader on the read path can reach the file: a reader that read it itself would meet a read error mid-decode.
func TestConfigReads_TheReadersDecodeOnlyTheSnapshot(t *testing.T) {
	const body = `{"engine_heap_mib":1024,"debug_logs":true,"agent_ignore_files":[".gitignore"],` +
		`"terminal_command_timeout_ms":300000,"content_collection_enabled":true,"security_profile":"unrestricted",` +
		`"additional_directories":["/data"],"memory_mode":"learn","auto_compaction_enabled":false,"auto_compact_pct":90,` +
		`"knowledge_enabled":false,"steering_reminders_enabled":true,"cloudformation_safety_check":"enforce","auto_routing":"on",` +
		`"auto_delegation":"on","model_fallback_enabled":true,"high_throughput_optimizations":false,` +
		`"spec_planning":"quick","spec_planning_ask_first":true,"workflows_enabled":false,"work_validation":"off",` +
		`"mcp_wait_for_ready":true,"notify_pr_status":true}`
	dir := writeSettings(t, body)
	t.Run("configDocument", func(t *testing.T) {
		want := readConfigDocument(docAt(t, dir))
		if want.engine.heapMiB != 1024 || want.session.profile != "unrestricted" {
			t.Fatalf("Setup: the document decodes to %+v, want its non-default values", want)
		}
		var l lastReadable
		got, src := l.read(t.Context(), dir, func(d settings.Document) configDocument {
			replaceWithDirectory(t, dir)
			return readConfigDocument(d)
		})
		restoreFile(t, dir, body)
		if src != fromDocument || !reflect.DeepEqual(got, want) {
			t.Errorf("read = from %d, %+v; want the document's, %+v", src, got, want)
		}
	})
	t.Run("liveSettings", func(t *testing.T) {
		want := readLiveFields(docAt(t, dir))
		want.readable = true
		got := readLiveSettings(t.Context(), dir, func(d settings.Document) liveSettings {
			replaceWithDirectory(t, dir)
			return readLiveFields(d)
		})
		restoreFile(t, dir, body)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("readLiveSettings = %+v; want %+v", got, want)
		}
	})
}

// The cache's cell table: each state built by a settle sequence, then one arrival. Heaps mark the generations.
func TestLastReadable_SettleTable(t *testing.T) {
	type arrival struct {
		gen      uint64
		heap     int
		readable bool
	}
	states := map[string][]arrival{
		"K0": nil,
		"K1": {{5, 2048, true}},
		"K2": {{0, 0, false}, {3, 0, false}},
		"K3": {{5, 2048, true}, {6, 0, false}},
		"K4": {{5, 2048, true}, {0, 0, false}},
	}
	for _, tc := range []struct {
		name, state string
		arrive      arrival
		held        bool
		src         docSource
		heap        int
	}{
		{"K0_a1", "K0", arrival{7, 4096, true}, false, fromDocument, 4096},
		{"K0_a3", "K0", arrival{8, 0, false}, true, fromDefaults, 0},
		{"K0_a5", "K0", arrival{0, 0, false}, true, fromDefaults, 0},
		{"K1_a1_newer", "K1", arrival{7, 4096, true}, false, fromDocument, 4096},
		{"K1_a1_same", "K1", arrival{5, 2048, true}, false, fromDocument, 2048},
		{"K1_a2", "K1", arrival{4, 1024, true}, false, fromDocument, 2048},
		{"K1_a3", "K1", arrival{8, 0, false}, true, fromLastReadable, 2048},
		{"K1_a4", "K1", arrival{4, 0, false}, false, fromDocument, 2048},
		{"K1_a5", "K1", arrival{0, 0, false}, true, fromLastReadable, 2048},
		{"K2_a1_newer", "K2", arrival{7, 4096, true}, false, fromDocument, 4096},
		{"K2_a1_same", "K2", arrival{3, 1024, true}, false, fromDocument, 1024},
		{"K2_a2", "K2", arrival{2, 1024, true}, true, fromDefaults, 0},
		{"K2_a3", "K2", arrival{8, 0, false}, true, fromDefaults, 0},
		{"K2_a4", "K2", arrival{2, 0, false}, true, fromDefaults, 0},
		{"K2_a5", "K2", arrival{0, 0, false}, true, fromDefaults, 0},
		{"K3_a1", "K3", arrival{7, 4096, true}, false, fromDocument, 4096},
		{"K3_a2", "K3", arrival{4, 1024, true}, true, fromLastReadable, 2048},
		{"K3_a2_the_cached_generation", "K3", arrival{5, 2048, true}, true, fromLastReadable, 2048},
		{"K3_a3", "K3", arrival{8, 0, false}, true, fromLastReadable, 2048},
		{"K3_a4", "K3", arrival{4, 0, false}, true, fromLastReadable, 2048},
		{"K3_a4_the_broken_generation_again", "K3", arrival{6, 0, false}, true, fromLastReadable, 2048},
		{"K3_a5", "K3", arrival{0, 0, false}, true, fromLastReadable, 2048},
		{"K4_a1_newer", "K4", arrival{7, 4096, true}, false, fromDocument, 4096},
		{"K4_a1", "K4", arrival{5, 2048, true}, false, fromDocument, 2048},
		{"K4_a2", "K4", arrival{4, 1024, true}, true, fromLastReadable, 2048},
		{"K4_a3", "K4", arrival{8, 0, false}, true, fromLastReadable, 2048},
		{"K4_a4", "K4", arrival{4, 0, false}, true, fromLastReadable, 2048},
		{"K4_a5", "K4", arrival{0, 0, false}, true, fromLastReadable, 2048},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var l lastReadable
			for _, a := range states[tc.state] {
				l.settle(heapDoc(a.heap), a.gen, a.readable)
			}
			doc, src := l.settle(heapDoc(tc.arrive.heap), tc.arrive.gen, tc.arrive.readable)
			if l.held() != tc.held || src != tc.src || doc.engine.heapMiB != tc.heap {
				t.Errorf("%s then %+v = held %v, source %d, heap %d; want held %v, source %d, heap %d",
					tc.state, tc.arrive, l.held(), src, doc.engine.heapMiB, tc.held, tc.src, tc.heap)
			}
		})
	}
	t.Run("K1_a4_after_a_released_hold", func(t *testing.T) {
		var l lastReadable
		l.settle(heapDoc(2048), 5, true)
		l.settle(&configDocument{}, 6, false)
		l.settle(heapDoc(4096), 7, true)
		doc, src := l.settle(&configDocument{}, 6, false)
		if l.held() || src != fromDocument || doc.engine.heapMiB != 4096 {
			t.Errorf("an older broken read after the release = held %v, source %d, heap %d; want the release kept, 4096",
				l.held(), src, doc.engine.heapMiB)
		}
	})
	t.Run("the_answer_does_not_alias_the_cache", func(t *testing.T) {
		var l lastReadable
		v := heapDoc(1)
		v.engine.ignoreFiles = []string{".kiroignore", ".gitignore"}
		v.session.dirEntries = []string{"/data"}
		got, _ := l.settle(v, 5, true)
		got.engine.ignoreFiles[1] = "tampered"
		got.session.dirEntries[0] = "tampered"
		again, _ := l.settle(&configDocument{}, 6, false)
		if again.engine.ignoreFiles[1] != ".gitignore" || again.session.dirEntries[0] != "/data" {
			t.Errorf("after a caller wrote its answer the cache answers %v and %v, want the originals",
				again.engine.ignoreFiles, again.session.dirEntries)
		}
	})
}

// The hold logs once per spell, whatever the number of reads. Serial: slog's default is process-global.
func TestLastReadable_TheHoldLogsOncePerSpell(t *testing.T) {
	rec := capture.Default(t)
	var l lastReadable
	l.settle(heapDoc(1), 1, true)
	for gen := range uint64(3) {
		l.settle(&configDocument{}, gen+2, false)
	}
	l.settle(heapDoc(1), 5, true)
	l.settle(heapDoc(1), 5, true)
	hold := "config.json cannot be read; marotte keeps its last readable settings, and engine restarts and session reloads wait until it reads again"
	if n := rec.CountExact(hold); n != 1 {
		t.Errorf("hold logged %d times, want 1: %v", n, rec.Messages())
	}
	if n := rec.CountExact("config.json reads again; waiting engine restarts and session reloads continue"); n != 1 {
		t.Errorf("release logged %d times, want 1: %v", n, rec.Messages())
	}
}

// A key that does not decode logs once per generation, however many reads answer it. Serial: slog's default is
// process-global.
func TestLastReadable_AKeyThatDoesNotDecodeLogsOncePerGeneration(t *testing.T) {
	rec := capture.Default(t)
	var l lastReadable
	dir := writeSettings(t, `{"engine_heap_mib":"big","debug_logs":true}`)
	for range 2 {
		l.read(t.Context(), dir, readConfigDocument)
	}
	if doc, src := l.read(t.Context(), dir, readConfigDocument); src != fromDocument || !doc.engine.debugLogs {
		t.Errorf("a read of the cached generation = debug logs %v from %d, want the document's true", doc.engine.debugLogs, src)
	}
	if n := rec.CountExact("settings: parse " + settings.KeyEngineHeapMiB); n != 1 {
		t.Errorf("three reads of one generation logged the parse %d times, want 1: %v", n, rec.Messages())
	}
	rewriteConfigByHand(t, dir, `{"engine_heap_mib":"bigger"}`)
	for range 2 {
		l.read(t.Context(), dir, readConfigDocument)
	}
	if n := rec.CountExact("settings: parse " + settings.KeyEngineHeapMiB); n != 2 {
		t.Errorf("a new generation logged the parse %d times in all, want 2: %v", n, rec.Messages())
	}
}
