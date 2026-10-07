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

	"github.com/cplieger/marotte/internal/agent"
	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/toolbelt/v3"
)

type manifestSaveFixture struct {
	handler     http.Handler
	logs        *bytes.Buffer
	tools       *toolsSlot
	agentWrites *capturedWriteHooks
	manifest    string
	mu          sync.Mutex
	kinds       []string
}

type capturedWriteHooks struct {
	hooks map[string]agent.WriteHook
}

func (c *capturedWriteHooks) SetWriteHook(path string, hook agent.WriteHook) {
	c.hooks[path] = hook
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
	fx.agentWrites = &capturedWriteHooks{hooks: map[string]agent.WriteHook{}}
	files, err := filebrowse.New(filebrowse.NewSensitive(dir), []string{dir},
		wireToolsManifestHook(fx.tools, fx.agentWrites))
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

func TestToolsManifestAgentWrite_RefusesABadManifestAndAValidOneStartsTheEngine(t *testing.T) {
	fx := newManifestSaveFixture(t, `{"version":1,"tools":{}}`)
	hook, ok := fx.agentWrites.hooks[fx.manifest]
	if !ok || hook.Check == nil || hook.Saved == nil {
		t.Fatalf("agent write hooks = %v, want a Check and a Saved registered at %s", fx.agentWrites.hooks, fx.manifest)
	}

	err := hook.Check([]byte(`{"version":3,"tools":{}}`))
	if err == nil || !strings.Contains(err.Error(), "tools.json was not saved: manifest version 3") {
		t.Errorf("agent Check(version 3 manifest) = %v, want the parse error naming the version", err)
	}
	valid := []byte(`{"version":2,"tools":{}}`)
	if err := hook.Check(valid); err != nil {
		t.Fatalf("agent Check(valid manifest) = %v, want nil", err)
	}
	if err := os.WriteFile(fx.manifest, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	hook.Saved()

	if engine, reason := fx.tools.Engine(); engine == nil {
		t.Errorf("Engine() after an agent write of a valid manifest = (nil, %v), want the engine up without a restart", reason)
	}
}

func TestToolsManifestAgentWrite_ARestoredBadManifestTakesTheEngineDownWithItsReason(t *testing.T) {
	fx := newManifestSaveFixture(t, `{"version":2,"tools":{}}`)
	hook := fx.agentWrites.hooks[fx.manifest]
	if engine, reason := fx.tools.Engine(); engine == nil {
		t.Fatalf("Setup: Engine() over a valid manifest = (nil, %v), want the engine up", reason)
	}
	if err := os.WriteFile(fx.manifest, []byte(`{"version":3,"tools":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	hook.Saved()

	engine, reason := fx.tools.Engine()
	if engine != nil {
		t.Fatal("Engine() after a restore put back a version-3 manifest = a live engine, want down")
	}
	if reason == nil || !strings.Contains(reason.Error(), "tools.json is invalid: manifest version 3") {
		t.Errorf("Engine() reason after the restore = %v, want it to say tools.json is invalid: manifest version 3", reason)
	}
	if recs := loggedManifestRefusals(t, fx.logs); len(recs) != 1 || recs[0]["path"] != fx.manifest {
		t.Errorf("ERROR records naming the unusable tools.json = %v, want one naming %s", recs, fx.manifest)
	}
}
