package composition

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/toolbelt/v3"
)

type manifestSaveFixture struct {
	handler  http.Handler
	logs     *bytes.Buffer
	tools    *toolsSlot
	manifest string
	mu       sync.Mutex
	kinds    []string
}

func newManifestSaveFixture(t *testing.T, initial string) *manifestSaveFixture {
	t.Helper()
	dir := t.TempDir()
	fx := &manifestSaveFixture{manifest: filepath.Join(dir, toolsManifestName)}
	if initial != "" {
		if err := os.WriteFile(fx.manifest, []byte(initial), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	toolsDir := filepath.Join(t.TempDir(), "tools")
	fx.tools = newToolsSlot(fx.manifest, func() (*toolbelt.Engine, error) {
		return toolbelt.New(&toolbelt.Config{
			ConfigDir: dir,
			ToolsDir:  toolsDir,
			OnJobChanged: func(j *toolbelt.Job) {
				if j != nil && j.State == toolbelt.JobQueued {
					fx.mu.Lock()
					fx.kinds = append(fx.kinds, j.Kind)
					fx.mu.Unlock()
				}
			},
		})
	})
	fx.logs = captureDefaultLogger(t)
	if err := fx.tools.start(); err != nil {
		t.Fatalf("Setup: start the tools slot: %v", err)
	}
	t.Cleanup(fx.tools.Close)
	files, err := filebrowse.New(filebrowse.NewSensitive(dir), []string{dir}, toolsManifestOwner(fx.tools))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	files.RegisterRoutes(mux)
	fx.handler = mux
	return fx
}

func (fx *manifestSaveFixture) save(t *testing.T, content string) (int, string) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"content": content})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/file?path="+url.QueryEscape(fx.manifest), strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, req)
	var reply struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &reply)
	return rec.Code, reply.Error
}

func (fx *manifestSaveFixture) enqueued() []string {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	return append([]string(nil), fx.kinds...)
}

func TestToolsManifestSave_RefusesWhatTheEngineWouldRefuse(t *testing.T) {
	fx := newManifestSaveFixture(t, "")
	before, err := os.ReadFile(fx.manifest)
	if err != nil {
		t.Fatalf("the engine seeds tools.json at start: %v", err)
	}

	code, msg := fx.save(t, `{"version":1,"tools":{}}`)

	if code != http.StatusBadRequest || !strings.Contains(msg, "manifest version 1") {
		t.Errorf("save of a wrong-version manifest = %d %q, want 400 naming the version", code, msg)
	}
	if after, _ := os.ReadFile(fx.manifest); string(after) != string(before) {
		t.Errorf("tools.json after a refused save = %q, want %q unchanged", after, before)
	}
	if got := fx.enqueued(); len(got) != 0 {
		t.Errorf("jobs enqueued by a refused save = %v, want none", got)
	}
}

func TestToolsManifestSave_ValidSaveQueuesAnInstallPassOnly(t *testing.T) {
	fx := newManifestSaveFixture(t, "")
	doc := `{"version":2,"tools":{"hello":{"source":"manual","install":"true"}}}`

	if code, msg := fx.save(t, doc); code != http.StatusOK {
		t.Fatalf("save of a valid manifest = %d %q, want 200", code, msg)
	}

	// The reconcile may already have rewritten the file in the engine's own layout.
	written, _ := os.ReadFile(fx.manifest)
	if m, err := toolbelt.ParseManifest(written); err != nil || m.Tools["hello"].Source != "manual" {
		t.Errorf("tools.json = %q (parse error %v), want the saved hello entry", written, err)
	}
	got := fx.enqueued()
	if len(got) != 1 || got[0] != toolbelt.JobKindReconcile {
		t.Errorf("jobs enqueued by the save = %v, want exactly one %q (no update pass)", got, toolbelt.JobKindReconcile)
	}
}

func TestToolsManifestSave_ValidSaveStartsAnEngineABadManifestKeptDown(t *testing.T) {
	fx := newManifestSaveFixture(t, `{"version":1,"tools":{}}`)
	if engine, reason := fx.tools.Engine(); engine != nil || reason == nil {
		t.Fatalf("Setup: Engine() over a version-1 manifest = (%v, %v), want down with a reason", engine, reason)
	}

	if code, msg := fx.save(t, `{"version":2,"tools":{}}`); code != http.StatusOK {
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
