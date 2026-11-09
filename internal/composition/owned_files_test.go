package composition

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/toolbelt/v3"
)

// fakeSettingsReactor records what a config.json owner told it. When entered is set, each
// SettingsWritten call sends on it after recording; when hold is set, the call then blocks until
// hold is closed, whatever its context says.
type fakeSettingsReactor struct {
	entered  chan struct{}
	hold     chan struct{}
	events   []marotte.ServerEvent
	written  [][]string
	mu       sync.Mutex
	draining bool
}

func (f *fakeSettingsReactor) Broadcast(_ context.Context, evt marotte.ServerEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, evt)
}

func (f *fakeSettingsReactor) SettingsWritten(_ context.Context, keys []string) {
	f.mu.Lock()
	f.written = append(f.written, keys)
	entered, hold := f.entered, f.hold
	f.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if hold != nil {
		<-hold
	}
}

// writtenKeys answers the keys of every SettingsWritten call so far.
func (f *fakeSettingsReactor) writtenKeys() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.written)
}

// settingsStamps answers the stamp of every settings_updated event broadcast so far.
func (f *fakeSettingsReactor) settingsStamps() []marotte.SettingsUpdatedPayload {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []marotte.SettingsUpdatedPayload
	for _, evt := range f.events {
		if p, ok := evt.Payload.(marotte.SettingsUpdatedPayload); ok && evt.Type == marotte.EventSettingsUpdated {
			out = append(out, p)
		}
	}
	return out
}

func (f *fakeSettingsReactor) HoldUnlessDraining() (release func(), ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.draining {
		return nil, false
	}
	return func() {}, true
}

// ownedFilesFixture serves the file browser over one config dir with every owned file registered.
type ownedFilesFixture struct {
	handler   http.Handler
	reactor   *fakeSettingsReactor
	tools     *toolsSlot
	watch     *configWatch
	logs      *bytes.Buffer
	configDir string
	toolsDir  string
	manifest  string
	mu        sync.Mutex
	kinds     []string
}

// toolsSetup shapes the fixture's tools engine: the tools.json it boots over ("" for none, which
// New seeds) and an edit of its config.
type toolsSetup struct {
	config   func(*toolbelt.Config)
	prepare  func(t *testing.T, configDir, toolsDir string)
	manifest string
}

func newOwnedFilesFixture(t *testing.T) *ownedFilesFixture {
	t.Helper()
	return newOwnedFilesFixtureWith(t, toolsSetup{})
}

func newOwnedFilesFixtureWith(t *testing.T, ts toolsSetup) *ownedFilesFixture {
	t.Helper()
	dir := t.TempDir()
	fx := &ownedFilesFixture{reactor: &fakeSettingsReactor{}, configDir: dir, manifest: filepath.Join(dir, toolsManifestName)}
	if ts.manifest != "" {
		if err := os.WriteFile(fx.manifest, []byte(ts.manifest), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	toolsDir := filepath.Join(t.TempDir(), "tools")
	fx.toolsDir = toolsDir
	if ts.prepare != nil {
		ts.prepare(t, dir, toolsDir)
	}
	fx.tools = newToolsSlot(fx.manifest, func() (*toolbelt.Engine, error) {
		cfg := &toolbelt.Config{
			ConfigDir: dir,
			ToolsDir:  toolsDir,
			OnJobChanged: func(j *toolbelt.Job) {
				if j != nil && j.State == toolbelt.JobQueued {
					fx.mu.Lock()
					fx.kinds = append(fx.kinds, j.Kind)
					fx.mu.Unlock()
				}
			},
		}
		if ts.config != nil {
			ts.config(cfg)
		}
		return toolbelt.New(cfg)
	})
	fx.logs = captureDefaultLogger(t)
	if err := fx.tools.start(); err != nil {
		t.Fatalf("Setup: start the tools slot: %v", err)
	}
	t.Cleanup(fx.tools.Close)
	fx.watch = newConfigWatch(dir, fx.reactor)
	files, err := filebrowse.New(filebrowse.NewSensitive(dir), []string{dir}, ownedFileOptions(ownedFileDeps{
		Config: fx.watch,
		Tools:  fx.tools,
		Dirs:   settings.AgentDirs{Workspace: t.TempDir(), Config: dir},
	})...)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	files.RegisterRoutes(mux)
	fx.handler = mux
	return fx
}

// enqueued answers the kinds of the jobs the tools engine queued so far.
func (fx *ownedFilesFixture) enqueued() []string {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	return append([]string(nil), fx.kinds...)
}

func (fx *ownedFilesFixture) configPath() string {
	return filepath.Join(fx.configDir, settings.Filename)
}

// editorRead reads path through the editor's read.
func (fx *ownedFilesFixture) editorRead(t *testing.T, path string) filebrowse.FileRead {
	t.Helper()
	rec := httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/file?path="+url.QueryEscape(path), nil))
	var read filebrowse.FileRead
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &read) != nil {
		t.Fatalf("Setup: editor read of %s = %d %s", path, rec.Code, rec.Body.String())
	}
	return read
}

// fileID reads path through the editor's read and answers the identity it was served under.
func (fx *ownedFilesFixture) fileID(t *testing.T, path string) string {
	t.Helper()
	return fx.editorRead(t, path).FileID
}

// save PUTs content to path through the editor's write, with fileID unless it is empty.
func (fx *ownedFilesFixture) save(t *testing.T, path, content, fileID string) (code int, msg string) {
	t.Helper()
	body := map[string]any{"content": content}
	if fileID != "" {
		body["file_id"] = fileID
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/file?path="+url.QueryEscape(path), strings.NewReader(string(raw))))
	var reply struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &reply)
	return rec.Code, reply.Error
}

func TestEditorSaveOfConfigJSON_WaitsForASettingsWriteInFlight(t *testing.T) {
	fx := newOwnedFilesFixture(t)
	writeConfig(t, fx.configPath(), `{"theme":"dark"}`+"\n")
	before := fx.fileID(t, fx.configPath())

	entered, release := make(chan struct{}), make(chan struct{})
	updated := make(chan error, 1)
	go func() {
		_, err := settings.Update(context.WithoutCancel(t.Context()), fx.configDir, func(doc map[string]json.RawMessage) error {
			close(entered)
			<-release
			doc[settings.KeyChatRetentionDays] = json.RawMessage(`-1`)
			return nil
		}, nil)
		updated <- err
	}()
	<-entered
	type answer struct {
		code int
		msg  string
	}
	saved := make(chan answer, 1)
	go func() {
		code, msg := fx.save(t, fx.configPath(), `{"theme":"light"}`+"\n", before)
		saved <- answer{code, msg}
	}()
	select {
	case a := <-saved:
		close(release)
		t.Errorf("the editor save answered %d %q while a settings write held config.json, want it to wait for that write", a.code, a.msg)
		<-updated
		return
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	if err := <-updated; err != nil {
		t.Fatalf("Update: %v", err)
	}
	a := <-saved

	if a.code != http.StatusConflict {
		t.Errorf("the editor save made from the file before the settings write = %d %q, want 409 stale", a.code, a.msg)
	}
	days, ok, err := settings.FieldStrict[int](t.Context(), fx.configDir, settings.KeyChatRetentionDays)
	if err != nil || !ok || days != -1 {
		t.Errorf("chat_retention_days after both writes = %d (found %v, err %v), want the settings write's -1 kept", days, ok, err)
	}
}

func TestEditorSaveOfConfigJSON_RefusesAValueThePatchRefuses(t *testing.T) {
	fx := newOwnedFilesFixture(t)
	const stored = `{"theme":"dark"}` + "\n"
	writeConfig(t, fx.configPath(), stored)

	code, msg := fx.save(t, fx.configPath(), `{"auto_compact_pct": 7}`, "")

	wantMsg := settings.CheckPatch(settings.AgentDirs{Config: fx.configDir},
		map[string]json.RawMessage{settings.KeyTheme: json.RawMessage(`"dark"`)},
		map[string]json.RawMessage{settings.KeyAutoCompactPct: json.RawMessage(`7`)})
	if wantMsg == nil {
		t.Fatal("Setup: the PATCH's compaction check accepts auto_compact_pct 7")
	}
	if code != http.StatusBadRequest || msg != wantMsg.Error() {
		t.Errorf("editor save of auto_compact_pct 7 = %d %q, want 400 %q", code, msg, wantMsg.Error())
	}
	if got, _ := os.ReadFile(fx.configPath()); string(got) != stored {
		t.Errorf("config.json after a refused save = %q, want %q unchanged", got, stored)
	}
}

func TestEditorSaveOfToolsJSON_GoesThroughTheEngine(t *testing.T) {
	fx := newOwnedFilesFixture(t)
	doc := `{"version":2,"tools":{"hello":{"source":"manual","install":"true"}}}`

	if code, msg := fx.save(t, fx.manifest, doc, ""); code != http.StatusOK {
		t.Fatalf("save of a valid manifest = %d %q, want 200", code, msg)
	}

	// The reconcile may already have rewritten the file in the engine's own layout.
	written, _ := os.ReadFile(fx.manifest)
	if m, err := toolbelt.ParseManifest(written); err != nil || m.Tools["hello"].Source != "manual" {
		t.Errorf("tools.json = %q (parse error %v), want the saved hello entry", written, err)
	}
	if got := fx.enqueued(); len(got) != 1 || got[0] != toolbelt.JobKindReconcile {
		t.Errorf("jobs enqueued by the save = %v, want exactly one %q (no update pass)", got, toolbelt.JobKindReconcile)
	}
}

func TestEditorSaveOfToolsJSON_InvalidIsRefusedWithTodaysText(t *testing.T) {
	t.Run("a wrong version", func(t *testing.T) {
		fx := newOwnedFilesFixture(t)
		before, err := os.ReadFile(fx.manifest)
		if err != nil {
			t.Fatalf("Setup: the engine seeds tools.json at start: %v", err)
		}

		code, msg := fx.save(t, fx.manifest, `{"version":1,"tools":{}}`, "")

		if code != http.StatusBadRequest || !strings.HasPrefix(msg, "tools.json was not saved:") || !strings.Contains(msg, "manifest version 1") {
			t.Errorf("save of a wrong-version manifest = %d %q, want 400 tools.json was not saved: naming the version", code, msg)
		}
		if after, _ := os.ReadFile(fx.manifest); string(after) != string(before) {
			t.Errorf("tools.json after a refused save = %q, want %q unchanged", after, before)
		}
		if got := fx.enqueued(); len(got) != 0 {
			t.Errorf("jobs enqueued by a refused save = %v, want none", got)
		}
	})
	t.Run("an essential row dropped", func(t *testing.T) {
		catalog := filepath.Join(t.TempDir(), "catalog.json")
		writeConfig(t, catalog, `{"entries":{"node":{"name":"node","source":"manual","essential":true}}}`)
		doc := manifestOf(disabledRow("node"), disabledRow("x"))
		fx := newOwnedFilesFixtureWith(t, toolsSetup{manifest: doc, config: func(c *toolbelt.Config) { c.CatalogPath = catalog }})
		if engine, reason := fx.tools.Engine(); engine == nil {
			t.Fatalf("Setup: Engine() = (nil, %v), want the engine up", reason)
		}

		code, msg := fx.save(t, fx.manifest, manifestOf(disabledRow("x")), "")

		const want = "tools.json was not saved: node is essential to this application; disable it instead"
		if code != http.StatusBadRequest || msg != want {
			t.Errorf("save of a manifest without the essential node = %d %q, want 400 %q", code, msg, want)
		}
		if after, _ := os.ReadFile(fx.manifest); string(after) != doc {
			t.Errorf("tools.json after a refused save = %q, want %q unchanged", after, doc)
		}
	})
}

// manifestOf is a tools.json document of rows at the engine's manifest version.
func manifestOf(rows ...string) string {
	return fmt.Sprintf(`{"version":%d,"tools":{%s}}`, toolbelt.ManifestVersion, strings.Join(rows, ","))
}

// disabledRow is a manual row the engine records and never installs.
func disabledRow(name string) string {
	return fmt.Sprintf(`%q:{"source":"manual","install":"true","disabled":true}`, name)
}

func TestToolsJSONSaveWhileDownStartsTheEngine(t *testing.T) {
	fx := newOwnedFilesFixtureWith(t, toolsSetup{manifest: `{"version":1,"tools":{}}`})
	if engine, reason := fx.tools.Engine(); engine != nil || reason == nil {
		t.Fatalf("Setup: Engine() over a version-1 manifest = (%v, %v), want down with a reason", engine, reason)
	}

	if code, msg := fx.save(t, fx.manifest, `{"version":2,"tools":{}}`, ""); code != http.StatusOK {
		t.Fatalf("save of a valid manifest over a down engine = %d %q, want 200", code, msg)
	}

	engine, reason := fx.tools.Engine()
	if engine == nil {
		t.Fatalf("Engine() after a valid save = (nil, %v), want the engine up without a restart", reason)
	}
	if inv, err := engine.Inventory(); err != nil {
		t.Errorf("Inventory() on the engine the save started: %v", err)
	} else if len(inv.Tools) != 0 {
		t.Errorf("Inventory().Tools on the engine the save started = %v, want the saved empty manifest", inv.Tools)
	}
}

// seedConfig writes doc's keys through settings.Update, so config.json is stamped and is the
// document its owner applied, and answers the bytes written.
func seedConfig(t *testing.T, dir string, doc map[string]string) []byte {
	t.Helper()
	if _, err := settings.Update(t.Context(), dir, func(m map[string]json.RawMessage) error {
		for k, v := range doc {
			m[k] = json.RawMessage(v)
		}
		return nil
	}, nil); err != nil {
		t.Fatalf("Setup: seed config.json: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, settings.Filename))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// handEdit writes config.json as an outside writer would: from's document with edit applied.
func handEdit(t *testing.T, dir string, from []byte, edit func(map[string]json.RawMessage)) []byte {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(from, &doc); err != nil {
		t.Fatalf("Setup: parse %q: %v", from, err)
	}
	edit(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(t, filepath.Join(dir, settings.Filename), string(out))
	return out
}

func stampOfConfig(t *testing.T, data []byte) marotte.SettingsUpdatedPayload {
	t.Helper()
	var s marotte.SettingsUpdatedPayload
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("stamp of %q: %v", data, err)
	}
	return s
}

// stampAbove compares epoch first, as every page does.
func stampAbove(a, b marotte.SettingsUpdatedPayload) bool {
	return a.SettingsEpoch > b.SettingsEpoch || a.SettingsEpoch == b.SettingsEpoch && a.SettingsRevision > b.SettingsRevision
}

// settingsGetStamp is the stamp the settings GET's resolution answers over dir.
func settingsGetStamp(t *testing.T, dir string) marotte.SettingsUpdatedPayload {
	t.Helper()
	stored, err := settings.Stored(dir)
	if err != nil {
		t.Fatalf("Stored: %v", err)
	}
	eff, _ := settings.EffectiveFrom(stored)
	return marotte.SettingsUpdatedPayload{SettingsEpoch: eff.SettingsEpoch, SettingsRevision: eff.SettingsRevision}
}

// configWarns counts settings.Poll's Warn for an adopted unreadable config.json at path.
func configWarns(t *testing.T, logs *bytes.Buffer, path string) int {
	t.Helper()
	n := 0
	dec := json.NewDecoder(bytes.NewReader(logs.Bytes()))
	for {
		var rec struct {
			Msg   string `json:"msg"`
			Level string `json:"level"`
			Path  string `json:"path"`
		}
		if err := dec.Decode(&rec); err != nil {
			return n
		}
		if rec.Level == "WARN" && rec.Path == path && strings.HasPrefix(rec.Msg, "settings: config.json cannot be read") {
			n++
		}
	}
}

func TestEditorSaveOfConfigJSON_StampsAnnouncesAndApplies(t *testing.T) {
	fx := newOwnedFilesFixture(t)
	seeded := stampOfConfig(t, seedConfig(t, fx.configDir, map[string]string{settings.KeyTheme: `"dark"`}))
	hold := make(chan struct{})
	fx.reactor.entered, fx.reactor.hold = make(chan struct{}, 4), hold
	srv := httptest.NewServer(fx.handler)
	defer srv.Close()
	stop := startConfigWatch(context.WithoutCancel(t.Context()), fx.watch)
	defer stop()
	var once sync.Once
	release := func() { once.Do(func() { close(hold) }) }
	defer release()

	type reply struct {
		err  error
		body []byte
		code int
	}
	replied := make(chan reply, 1)
	go func() {
		raw, _ := json.Marshal(map[string]string{"content": `{"theme":"light"}` + "\n"})
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPut,
			srv.URL+"/api/file?path="+url.QueryEscape(fx.configPath()), bytes.NewReader(raw))
		if err != nil {
			replied <- reply{err: err}
			return
		}
		res, err := srv.Client().Do(req)
		if err != nil {
			replied <- reply{err: err}
			return
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		replied <- reply{err: err, body: body, code: res.StatusCode}
	}()
	var r reply
	select {
	case r = <-replied:
	case <-time.After(5 * time.Second):
		t.Fatal("the save's answer was not read in full within 5s while its after-write effect was held")
	}

	if r.err != nil || r.code != http.StatusOK {
		t.Fatalf("PUT config.json = %d %q (%v), want 200", r.code, r.body, r.err)
	}
	disk, err := os.ReadFile(fx.configPath())
	if err != nil {
		t.Fatal(err)
	}
	stamp := stampOfConfig(t, disk)
	if !stampAbove(stamp, seeded) {
		t.Errorf("config.json's stamp after the save = %+v, want above the seeded %+v", stamp, seeded)
	}
	if got := fx.reactor.settingsStamps(); !slices.Equal(got, []marotte.SettingsUpdatedPayload{stamp}) {
		t.Errorf("settings_updated events = %+v, want one at the file's stamp %+v", got, stamp)
	}
	var result filebrowse.FileWriteResult
	if err := json.Unmarshal(r.body, &result); err != nil || result.Content == nil || *result.Content != string(disk) {
		t.Errorf("the save's answer = %s (%v), want content equal to the file's bytes %q", r.body, err, disk)
	}
	select {
	case <-fx.reactor.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("SettingsWritten was not called within 5s of the save")
	}
	release()
	stop()
	if got := fx.reactor.writtenKeys(); len(got) != 1 || !slices.Equal(got[0], []string{settings.KeyTheme}) {
		t.Errorf("SettingsWritten calls = %q, want one with [theme]", got)
	}
}

func TestConfigJSONOutsideWriteIsAppliedAtTheNextPass(t *testing.T) {
	fx := newOwnedFilesFixture(t)
	seeded := seedConfig(t, fx.configDir, map[string]string{settings.KeyTheme: `"dark"`})
	edited := handEdit(t, fx.configDir, seeded, func(d map[string]json.RawMessage) { d[settings.KeyTheme] = json.RawMessage(`"light"`) })

	fx.watch.pass(t.Context())

	want := []marotte.SettingsUpdatedPayload{stampOfConfig(t, edited)}
	if got := fx.reactor.settingsStamps(); !slices.Equal(got, want) {
		t.Errorf("settings_updated events after the pass = %+v, want %+v", got, want)
	}
	if got := fx.reactor.writtenKeys(); len(got) != 1 || !slices.Equal(got[0], []string{settings.KeyTheme}) {
		t.Errorf("SettingsWritten calls after the pass = %q, want one with [theme]", got)
	}
	if disk, _ := os.ReadFile(fx.configPath()); !bytes.Equal(disk, edited) {
		t.Errorf("config.json after the pass = %q, want the outside write %q byte for byte", disk, edited)
	}

	fx.watch.pass(t.Context())

	if got := fx.reactor.settingsStamps(); len(got) != 1 {
		t.Errorf("settings_updated events after a second pass = %+v, want still one", got)
	}
	if got := fx.reactor.writtenKeys(); len(got) != 1 {
		t.Errorf("SettingsWritten calls after a second pass = %q, want still one", got)
	}
}

func TestConfigJSONOutsideWriteBelowThePagesVersionIsRestamped(t *testing.T) {
	for _, tc := range []struct {
		// write makes the outside change and answers the highest stamp marotte held before it.
		write    func(t *testing.T, fx *ownedFilesFixture, seeded []byte) marotte.SettingsUpdatedPayload
		name     string
		wantKeys []string
		passes   int
	}{
		{name: "removed", passes: 2, wantKeys: []string{settings.KeyTheme}, write: func(t *testing.T, fx *ownedFilesFixture, seeded []byte) marotte.SettingsUpdatedPayload {
			t.Helper()
			if err := os.Remove(fx.configPath()); err != nil {
				t.Fatal(err)
			}
			return stampOfConfig(t, seeded)
		}},
		{name: "rewritten without the stamp keys", passes: 1, wantKeys: []string{settings.KeyTheme}, write: func(t *testing.T, fx *ownedFilesFixture, seeded []byte) marotte.SettingsUpdatedPayload {
			t.Helper()
			handEdit(t, fx.configDir, seeded, func(d map[string]json.RawMessage) {
				delete(d, settings.KeySettingsEpoch)
				delete(d, settings.KeySettingsRevision)
				d[settings.KeyTheme] = json.RawMessage(`"light"`)
			})
			return stampOfConfig(t, seeded)
		}},
		{name: "an older stamped copy copied back", passes: 1, wantKeys: []string{settings.KeyTheme}, write: func(t *testing.T, fx *ownedFilesFixture, seeded []byte) marotte.SettingsUpdatedPayload {
			t.Helper()
			later := seedConfig(t, fx.configDir, map[string]string{settings.KeyTheme: `"light"`})
			writeConfig(t, fx.configPath(), string(seeded))
			return stampOfConfig(t, later)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newOwnedFilesFixture(t)
			seeded := seedConfig(t, fx.configDir, map[string]string{settings.KeyTheme: `"dark"`})
			held := tc.write(t, fx, seeded)

			for range tc.passes - 1 {
				fx.watch.pass(t.Context())
				if got := fx.reactor.settingsStamps(); len(got) != 0 {
					t.Errorf("settings_updated events before the change settled = %+v, want none", got)
				}
				if _, err := os.Stat(fx.configPath()); !os.IsNotExist(err) {
					t.Errorf("config.json before the removal settled: stat error %v, want it still absent", err)
				}
			}
			fx.watch.pass(t.Context())

			got := fx.reactor.settingsStamps()
			if len(got) != 1 || !stampAbove(got[0], held) {
				t.Fatalf("settings_updated events = %+v, want one above %+v", got, held)
			}
			if get := settingsGetStamp(t, fx.configDir); get != got[0] {
				t.Errorf("the settings GET's stamp = %+v, want the event's %+v", get, got[0])
			}
			if keys := fx.reactor.writtenKeys(); len(keys) != 1 || !slices.Equal(keys[0], tc.wantKeys) {
				t.Errorf("SettingsWritten calls = %q, want one with %q", keys, tc.wantKeys)
			}
		})
	}
}

func TestEditorSaveOfConfigJSON_UnchangedContentRunsNoEffect(t *testing.T) {
	fx := newOwnedFilesFixture(t)
	seedConfig(t, fx.configDir, map[string]string{settings.KeyTheme: `"dark"`})
	read := fx.editorRead(t, fx.configPath())

	code, msg := fx.save(t, fx.configPath(), read.Content, read.FileID)
	fx.watch.drain(t.Context())

	if code != http.StatusOK {
		t.Errorf("save of the unchanged content = %d %q, want 200", code, msg)
	}
	if got := fx.reactor.writtenKeys(); len(got) != 0 {
		t.Errorf("SettingsWritten calls after a save that changed no value = %q, want none", got)
	}
}

func TestConfigWatchPass_AnUnreadableFileWarnsOncePerRead(t *testing.T) {
	fx := newOwnedFilesFixture(t)
	seedConfig(t, fx.configDir, map[string]string{settings.KeyTheme: `"dark"`})
	plantDir := func() {
		t.Helper()
		if err := os.RemoveAll(fx.configPath()); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(fx.configPath(), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	plantDir()

	fx.watch.pass(t.Context())
	if n, ev, keys := configWarns(t, fx.logs, fx.configPath()), fx.reactor.settingsStamps(), fx.reactor.writtenKeys(); n != 0 || len(ev) != 0 || len(keys) != 0 {
		t.Errorf("after one pass over a directory: %d Warns, events %+v, SettingsWritten %q; want nothing (the read is held)", n, ev, keys)
	}
	fx.watch.pass(t.Context())
	if n := configWarns(t, fx.logs, fx.configPath()); n != 1 {
		t.Errorf("Warns naming %s after the second pass = %d, want 1", fx.configPath(), n)
	}
	if got := fx.reactor.settingsStamps(); !slices.Equal(got, []marotte.SettingsUpdatedPayload{{}}) {
		t.Errorf("settings_updated events after the second pass = %+v, want one at the zero stamp", got)
	}
	if got := fx.reactor.writtenKeys(); len(got) != 1 || got[0] != nil {
		t.Errorf("SettingsWritten calls after the second pass = %q, want one with nil keys", got)
	}
	fx.watch.pass(t.Context())
	if n, ev, keys := configWarns(t, fx.logs, fx.configPath()), fx.reactor.settingsStamps(), fx.reactor.writtenKeys(); n != 1 || len(ev) != 1 || len(keys) != 1 {
		t.Errorf("after a third pass: %d Warns, %d events, %d SettingsWritten; want 1, 1, 1 (nothing more)", n, len(ev), len(keys))
	}

	if err := os.Remove(fx.configPath()); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, fx.configPath(), `{"theme":"light"}`)
	fx.watch.pass(t.Context())
	if got := fx.reactor.writtenKeys(); len(got) != 2 {
		t.Fatalf("SettingsWritten calls after a valid document's pass = %q, want a second", got)
	}
	plantDir()
	fx.watch.pass(t.Context())
	fx.watch.pass(t.Context())
	if n := configWarns(t, fx.logs, fx.configPath()); n != 2 {
		t.Errorf("Warns after the directory came back for two passes = %d, want 2", n)
	}
}

func TestConfigWatchStop_WaitsForAnEffectInFlight(t *testing.T) {
	t.Run("an effect in flight", func(t *testing.T) {
		// After the release the loop's select has both its cancelled context and the queued
		// key's signal ready and picks one at random, so one run catches a drain after the stop
		// only half the time.
		for range 16 {
			synctest.Test(t, func(t *testing.T) {
				reactor := &fakeSettingsReactor{entered: make(chan struct{}, 4), hold: make(chan struct{})}
				w := newConfigWatch(t.TempDir(), reactor)
				parent, cancel := context.WithCancel(t.Context())
				defer cancel()
				stop := startConfigWatch(parent, w)
				w.effects.queue([]string{settings.KeyTheme})
				<-reactor.entered
				w.effects.queue([]string{settings.KeyDebugLogs})

				stopped := make(chan struct{})
				go func() {
					stop()
					close(stopped)
				}()
				synctest.Wait()

				select {
				case <-stopped:
					t.Error("the stop returned while an effect was in flight")
				default:
				}
				if parent.Err() != nil {
					t.Errorf("the parent context after the stop began = %v, want it untouched", parent.Err())
				}
				close(reactor.hold)
				<-stopped
				if got := reactor.writtenKeys(); len(got) != 1 {
					t.Errorf("SettingsWritten calls = %q, want only the one in flight (nothing drained after the stop)", got)
				}
			})
		}
	})
	t.Run("no effect in flight", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			parent, cancel := context.WithCancel(t.Context())
			defer cancel()
			stop := startConfigWatch(parent, newConfigWatch(t.TempDir(), &fakeSettingsReactor{}))
			stop()
			if parent.Err() != nil {
				t.Errorf("the parent context after the stop = %v, want it untouched", parent.Err())
			}
		})
	})
	t.Run("draining", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			dir := t.TempDir()
			seeded := seedConfig(t, dir, map[string]string{settings.KeyTheme: `"dark"`})
			handEdit(t, dir, seeded, func(d map[string]json.RawMessage) { d[settings.KeyTheme] = json.RawMessage(`"light"`) })
			reactor := &fakeSettingsReactor{draining: true}
			w := newConfigWatch(dir, reactor)

			w.pass(t.Context())

			if ev, keys := reactor.settingsStamps(), reactor.writtenKeys(); len(ev) != 0 || len(keys) != 0 {
				t.Errorf("a pass while the runtime drains: events %+v, SettingsWritten %q; want neither", ev, keys)
			}
			startConfigWatch(t.Context(), w)()
		})
	})
}

func TestConfigWatch_AnOutsideEditRunsSettingsWrittenOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		seeded := seedConfig(t, dir, map[string]string{settings.KeyTheme: `"dark"`})
		reactor := &fakeSettingsReactor{}
		w := newConfigWatch(dir, reactor)
		stop := startConfigWatch(t.Context(), w)
		defer stop()
		handEdit(t, dir, seeded, func(d map[string]json.RawMessage) { d[settings.KeyTheme] = json.RawMessage(`"light"`) })

		time.Sleep(ownedFilePoll + time.Millisecond)
		synctest.Wait()

		if got := reactor.writtenKeys(); len(got) != 1 || !slices.Equal(got[0], []string{settings.KeyTheme}) {
			t.Errorf("SettingsWritten calls after one tick = %q, want exactly one with [theme]", got)
		}
		w.effects.wake()
		synctest.Wait()
		if got := reactor.writtenKeys(); len(got) != 1 {
			t.Errorf("SettingsWritten calls after a signal-only wake = %q, want still one", got)
		}
	})
}

func TestToolsJSONShellEditConvergesAtTheNextTick(t *testing.T) {
	t.Run("an edit, then an invalid edit", func(t *testing.T) {
		fx := newOwnedFilesFixture(t)
		writeConfig(t, fx.manifest, manifestOf(disabledRow("x")))

		fx.tools.tick()
		if got := fx.enqueued(); !slices.Equal(got, []string{toolbelt.JobKindReconcile}) {
			t.Errorf("jobs after a tick over a hand edit = %v, want one %q", got, toolbelt.JobKindReconcile)
		}

		writeConfig(t, fx.manifest, `{"version":3,"tools":{}}`)
		fx.tools.tick()
		if engine, reason := fx.tools.Engine(); engine == nil {
			t.Errorf("Engine() after one tick over an invalid edit = (nil, %v), want still up (the read is held)", reason)
		}
		if got := fx.enqueued(); len(got) != 1 {
			t.Errorf("jobs after one tick over an invalid edit = %v, want no new one", got)
		}
		fx.tools.tick()
		if engine, reason := fx.tools.Engine(); engine != nil || reason == nil || !strings.Contains(reason.Error(), "tools.json is invalid") {
			t.Errorf("Engine() after two ticks over an invalid edit = (%v, %v), want down saying tools.json is invalid", engine, reason)
		}
	})
	t.Run("a directory at tools.json", func(t *testing.T) {
		fx := newOwnedFilesFixture(t)
		if err := os.Remove(fx.manifest); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(fx.manifest, 0o750); err != nil {
			t.Fatal(err)
		}

		fx.tools.tick()
		if engine, reason := fx.tools.Engine(); engine == nil || len(loggedManifestRefusals(t, fx.logs)) != 0 {
			t.Errorf("after one tick: Engine() = (%v, %v), refusals logged %d; want up and none", engine, reason, len(loggedManifestRefusals(t, fx.logs)))
		}
		fx.tools.tick()
		if engine, reason := fx.tools.Engine(); engine != nil || reason == nil || !strings.Contains(reason.Error(), "not a regular file") {
			t.Errorf("Engine() after two ticks = (%v, %v), want down naming a file that is not regular", engine, reason)
		}
		if n := len(loggedManifestRefusals(t, fx.logs)); n != 1 {
			t.Errorf("refusals logged after two ticks = %d, want 1", n)
		}
		fx.tools.tick()
		if n := len(loggedManifestRefusals(t, fx.logs)); n != 1 {
			t.Errorf("refusals logged after a third tick = %d, want still 1", n)
		}

		if err := os.Remove(fx.manifest); err != nil {
			t.Fatal(err)
		}
		writeConfig(t, fx.manifest, manifestOf())
		fx.tools.tick()
		if engine, reason := fx.tools.Engine(); engine == nil {
			t.Errorf("Engine() after a valid manifest and a tick = (nil, %v), want up", reason)
		}
	})
}
