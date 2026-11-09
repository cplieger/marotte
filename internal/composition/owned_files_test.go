package composition

import (
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
	configDir string
}

func newOwnedFilesFixture(t *testing.T) *ownedFilesFixture {
	t.Helper()
	dir := t.TempDir()
	toolsDir := filepath.Join(t.TempDir(), "tools")
	tools := newToolsSlot(filepath.Join(dir, toolsManifestName), func() (*toolbelt.Engine, error) {
		return toolbelt.New(&toolbelt.Config{ConfigDir: dir, ToolsDir: toolsDir})
	})
	if err := tools.start(); err != nil {
		t.Fatalf("Setup: start the tools slot: %v", err)
	}
	t.Cleanup(tools.Close)
	fx := &ownedFilesFixture{reactor: &fakeSettingsReactor{}, tools: tools, configDir: dir}
	files, err := filebrowse.New(filebrowse.NewSensitive(dir), []string{dir}, ownedFileOptions(ownedFileDeps{
		Settings: fx.reactor,
		Tools:    tools,
		Dirs:     settings.AgentDirs{Workspace: t.TempDir(), Config: dir},
	})...)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	files.RegisterRoutes(mux)
	fx.handler = mux
	return fx
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

	wantMsg := settings.ValidateCompactionPatch(map[string]json.RawMessage{settings.KeyAutoCompactPct: json.RawMessage(`7`)})
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
