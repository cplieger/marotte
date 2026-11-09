package composition

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/toolbelt/v3"
)

// fakeSettingsReactor records what a config.json owner told it.
type fakeSettingsReactor struct {
	mu       sync.Mutex
	events   []marotte.ServerEvent
	written  [][]string
	draining bool
}

func (f *fakeSettingsReactor) Broadcast(_ context.Context, evt marotte.ServerEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, evt)
}

func (f *fakeSettingsReactor) SettingsWritten(_ context.Context, keys []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.written = append(f.written, keys)
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
	manifest  string
	mu        sync.Mutex
	kinds     []string
}

// toolsSetup shapes the fixture's tools engine: the tools.json it boots over ("" for none, which
// New seeds) and an edit of its config.
type toolsSetup struct {
	config   func(*toolbelt.Config)
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

// fileID reads path through the editor's read and answers the identity it was served under.
func (fx *ownedFilesFixture) fileID(t *testing.T, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/file?path="+url.QueryEscape(path), nil))
	var read filebrowse.FileRead
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &read) != nil {
		t.Fatalf("Setup: editor read of %s = %d %s", path, rec.Code, rec.Body.String())
	}
	return read.FileID
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
