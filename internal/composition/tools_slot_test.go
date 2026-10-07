package composition

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/toolbelt/v3"
)

// manifestRefusalMsg must match refuseManifest's message: it is the one line naming the
// file and the parse error an operator searches the boot log for.
const manifestRefusalMsg = "tools engine disabled: tools.json is unusable; marotte is running without the tools subsystem"

func loggedManifestRefusals(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	var found []map[string]any
	dec := json.NewDecoder(bytes.NewReader(logs.Bytes()))
	for {
		var rec map[string]any
		if err := dec.Decode(&rec); err != nil {
			break
		}
		if rec["msg"] == manifestRefusalMsg {
			found = append(found, rec)
		}
	}
	return found
}

func TestWireToolsEngineBootsOverAnInvalidManifestAndSaysWhy(t *testing.T) {
	configDir, toolsDir := fitTree(t)
	manifest := filepath.Join(configDir, "tools.json")
	doc := fmt.Sprintf(`{"version":%d,"tools":{}}`, toolbelt.ManifestVersion+1)
	if err := os.WriteFile(manifest, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	logs := captureDefaultLogger(t)

	tools, err := wireToolsEngine(t.Context(), &Config{ConfigDir: configDir, ToolsDir: toolsDir}, testRuntime(t), nil)
	if err != nil {
		t.Fatalf("a tools.json toolbelt refuses stopped the boot: %v", err)
	}
	t.Cleanup(tools.Close)

	engine, reason := tools.Engine()
	if engine != nil {
		t.Fatal("an engine came up over a manifest toolbelt refuses")
	}
	wantVersion := fmt.Sprintf("manifest version %d, want %d", toolbelt.ManifestVersion+1, toolbelt.ManifestVersion)
	if reason == nil || !strings.Contains(reason.Error(), "tools.json is invalid: "+wantVersion) {
		t.Errorf("Engine() reason = %v, want it to say tools.json is invalid: %s", reason, wantVersion)
	}
	recs := loggedManifestRefusals(t, logs)
	if len(recs) != 1 {
		t.Fatalf("ERROR records naming the unusable tools.json = %d, want 1; logs:\n%s", len(recs), logs.String())
	}
	if recs[0]["level"] != "ERROR" || recs[0]["path"] != manifest ||
		!strings.Contains(fmt.Sprint(recs[0]["error"]), wantVersion) {
		t.Errorf("refusal record = %v, want an ERROR naming %s and %q", recs[0], manifest, wantVersion)
	}
	if after, _ := os.ReadFile(manifest); string(after) != doc {
		t.Errorf("tools.json after the degraded boot = %q, want %q untouched", after, doc)
	}
}

func TestWireToolsEngineBootsOverAnUnreadableManifest(t *testing.T) {
	configDir, toolsDir := fitTree(t)
	if err := os.Mkdir(filepath.Join(configDir, "tools.json"), 0o750); err != nil {
		t.Fatal(err)
	}
	captureDefaultLogger(t)

	tools, err := wireToolsEngine(t.Context(), &Config{ConfigDir: configDir, ToolsDir: toolsDir}, testRuntime(t), nil)
	if err != nil {
		t.Fatalf("an unreadable tools.json stopped the boot: %v", err)
	}
	t.Cleanup(tools.Close)

	if engine, reason := tools.Engine(); engine != nil || reason == nil ||
		!strings.Contains(reason.Error(), "tools.json cannot be read") {
		t.Errorf("Engine() over a directory at tools.json = (%v, %v), want down saying it cannot be read", engine, reason)
	}
}

func TestToolsSlot_ManifestChangedAfterCloseStartsNothing(t *testing.T) {
	fx := newManifestSaveFixture(t, `{"version":1,"tools":{}}`)
	if err := os.WriteFile(fx.manifest, []byte(`{"version":2,"tools":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fx.tools.Close()

	fx.tools.manifestChanged()

	if engine, reason := fx.tools.Engine(); engine != nil || !errors.Is(reason, errToolsClosed) {
		t.Errorf("Engine() after Close and a save = (%v, %v), want (nil, %v)", engine, reason, errToolsClosed)
	}
}

func TestToolsSlot_StartRetriesARefusalWhoseManifestWasRepairedMeanwhile(t *testing.T) {
	configDir, toolsDir := fitTree(t)
	manifest := filepath.Join(configDir, "tools.json")
	if err := os.WriteFile(manifest, []byte(`{"version":3,"tools":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	captureDefaultLogger(t)
	builds := 0
	tools := newToolsSlot(manifest, func() (*toolbelt.Engine, error) {
		builds++
		engine, err := toolbelt.New(&toolbelt.Config{ConfigDir: configDir, ToolsDir: toolsDir})
		if builds == 1 {
			if werr := os.WriteFile(manifest, []byte(`{"version":2,"tools":{}}`), 0o600); werr != nil {
				t.Errorf("repair tools.json during the first build: %v", werr)
			}
		}
		return engine, err
	})
	t.Cleanup(tools.Close)

	if err := tools.start(); err != nil {
		t.Fatalf("start() with tools.json repaired while toolbelt refused it = %v, want no fatal boot", err)
	}

	if engine, reason := tools.Engine(); engine == nil {
		t.Errorf("Engine() after the repaired start = (nil, %v), want the engine up on the repaired file", reason)
	}
}
