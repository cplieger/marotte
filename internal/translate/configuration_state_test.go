package translate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func configurationStateFixture(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "configuration_state.json"))
	if err != nil {
		t.Fatalf("read configuration_state.json: %v", err)
	}
	var fx map[string]json.RawMessage
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatalf("parse configuration_state.json: %v", err)
	}
	return fx
}

func TestDecodeConfigurationState_ReadsTheWinningLayerOfAConnectionView(t *testing.T) {
	got, ok := DecodeConfigurationState(configurationStateFixture(t)["env_connection"])
	if !ok {
		t.Fatal("DecodeConfigurationState(env connection view) ok = false, want true")
	}
	want := marotte.KiroDefault{Value: "on", Layer: "kiro-agent", LayerName: "Kiro Agent"}
	if got["validation"] != want {
		t.Errorf("validation = %+v, want %+v", got["validation"], want)
	}
	if len(got) != 1 {
		t.Errorf("decoded %d settings (%v), want only the stated validation", len(got), got)
	}
}

func TestDecodeConfigurationState_AnUnallocatedAccountStatesNothing(t *testing.T) {
	got, ok := DecodeConfigurationState(configurationStateFixture(t)["unallocated_connection"])
	if !ok || len(got) != 0 {
		t.Errorf("DecodeConfigurationState(unallocated connection view) = %v, %v; want an empty view and true", got, ok)
	}
}

func TestDecodeConfigurationState_IgnoresASessionView(t *testing.T) {
	if got, ok := DecodeConfigurationState(configurationStateFixture(t)["env_session"]); ok {
		t.Errorf("DecodeConfigurationState(session view) = %v, true; want false: only the connection view describes a new chat", got)
	}
}

func TestDecodeConfigurationState_KeepsAnUnknownLayerAndTreatsItsText(t *testing.T) {
	raw := json.RawMessage(`{"view":{"observer":"connection"},
		"layers":[{"id":"future-layer","name":"Future\nLayer"}],
		"settings":[
			{"krn":"krn:::setting/validation","value":"on","contributions":[
				{"layer":"kiro-service","stated":{"value":"off","used":true}},
				{"layer":"future-layer","stated":{"value":"on","used":true}}]},
			{"krn":"krn:::resource/x","value":"on","contributions":[]},
			{"krn":"krn:::setting/limit","value":3,"contributions":[]}
		]}`)
	got, ok := DecodeConfigurationState(raw)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if want := (marotte.KiroDefault{Value: "on", Layer: "future-layer", LayerName: "Future Layer"}); got["validation"] != want {
		t.Errorf("validation = %+v, want %+v (the last used contribution, its name single-lined)", got["validation"], want)
	}
	if want := (marotte.KiroDefault{Value: "3"}); got["limit"] != want {
		t.Errorf("limit = %+v, want %+v (a non-string value as its JSON text, no layer)", got["limit"], want)
	}
	if _, present := got["x"]; present || len(got) != 2 {
		t.Errorf("decoded %v, want the resource krn skipped", got)
	}
}

func workspaceConfigurationFixture(t *testing.T) json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "configuration_getstate_workspace.json"))
	if err != nil {
		t.Fatalf("read configuration_getstate_workspace.json: %v", err)
	}
	return data
}

func TestDecodeCustomAgents_KeepsCustomLayersWithRealDescriptions(t *testing.T) {
	got, ok := DecodeCustomAgents(workspaceConfigurationFixture(t))
	if !ok {
		t.Fatal("DecodeCustomAgents(fixture) ok = false, want true")
	}
	byID := map[string]marotte.SessionMode{}
	for _, m := range got {
		byID[m.ID] = m
	}
	if _, listed := byID["wf-auto-researcher"]; listed {
		t.Errorf("modes = %+v, list KAS's own helper agent as a mode", got)
	}
	want := marotte.SessionMode{ID: "scribe", Name: "scribe", Description: "Writes release notes from the git log", Source: "global"}
	if byID["scribe"] != want {
		t.Errorf("scribe = %+v, want %+v", byID["scribe"], want)
	}
	if len(got) != 3 {
		t.Errorf("decoded %d agents (%+v), want scribe, dup and semantic_reviewer", len(got), got)
	}
}

func TestDecodeCustomAgents_LaterLayerAndLaterRootWin(t *testing.T) {
	got, _ := DecodeCustomAgents(workspaceConfigurationFixture(t))
	byID := map[string]marotte.SessionMode{}
	for _, m := range got {
		byID[m.ID] = m
	}
	// The ~/.kiro statement of dup comes last in the list but from an earlier layer.
	if d := byID["dup"]; d.Description != "dup from r2" || d.Source != "workspace" {
		t.Errorf("dup = %+v, want the second root's workspace statement", d)
	}
	if r := byID["semantic_reviewer"]; r.Description != "override from r1" || r.Source != "workspace" {
		t.Errorf("semantic_reviewer = %+v, want the workspace override", r)
	}
}

func TestDecodeCustomAgents_AnUndecodableFrameIsNotOK(t *testing.T) {
	if got, ok := DecodeCustomAgents(json.RawMessage(`["not a configuration"]`)); ok || got != nil {
		t.Errorf("DecodeCustomAgents(array) = %+v, %v; want nil, false", got, ok)
	}
}

// modelFallbackFixture is internal/translate/testdata/configuration_model_fallback.json, captured by
// internal/bridge TestCaptureModelFallbackView.
func modelFallbackFixture(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "configuration_model_fallback.json"))
	if err != nil {
		t.Fatalf("read configuration_model_fallback.json: %v", err)
	}
	var fx map[string]json.RawMessage
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatalf("parse configuration_model_fallback.json: %v", err)
	}
	return fx
}

func fallbackOf(t *testing.T, rows []marotte.ModelFallback, model string) marotte.ModelFallback {
	t.Helper()
	for _, r := range rows {
		if r.Model == model {
			return r
		}
	}
	t.Fatalf("no row for %s in %v", model, rows)
	return marotte.ModelFallback{}
}

func TestDecodeModelFallbacks(t *testing.T) {
	fx := modelFallbackFixture(t)
	t.Run("vendor only", func(t *testing.T) {
		rows, ok := DecodeModelFallbacks(fx["connection"])
		if !ok {
			t.Fatal("ok = false for a connection view")
		}
		if got, want := fallbackOf(t, rows, "model-a"), (marotte.ModelFallback{Model: "model-a", VendorDefault: "model-k"}); got != want {
			t.Errorf("model-a = %+v, want %+v: the kiro-service layer is Kiro's default, not a choice", got, want)
		}
		if got, want := fallbackOf(t, rows, "model-c"), (marotte.ModelFallback{Model: "model-c"}); got != want {
			t.Errorf("model-c = %+v, want %+v: Kiro states an empty chain", got, want)
		}
		if len(rows) != 24 {
			t.Errorf("decoded %d rows, want the view's 24 modelFallback rows and no other setting", len(rows))
		}
	})
	t.Run("account configured", func(t *testing.T) {
		rows, _ := DecodeModelFallbacks(fx["connection_configured"])
		want := marotte.ModelFallback{Model: "model-a", Configured: "model-b", VendorDefault: "model-k"}
		if got := fallbackOf(t, rows, "model-a"); got != want {
			t.Errorf("model-a = %+v, want %+v", got, want)
		}
	})
	t.Run("locked", func(t *testing.T) {
		raw := json.RawMessage(`{"view":{"observer":"connection"},"settings":[{"krn":"krn:::setting/modelFallback/model-a",
			"value":["model-b"],"compose":"locked","contributions":[
			{"layer":"kiro-service","stated":{"value":["model-k"],"used":false}},
			{"layer":"organization","stated":{"value":["model-b"],"used":true}}]}]}`)
		rows, _ := DecodeModelFallbacks(raw)
		want := marotte.ModelFallback{Model: "model-a", Configured: "model-b", VendorDefault: "model-k", ReadOnlyReason: "locked"}
		if got := fallbackOf(t, rows, "model-a"); got != want {
			t.Errorf("model-a = %+v, want %+v", got, want)
		}
	})
	t.Run("two targets", func(t *testing.T) {
		raw := json.RawMessage(`{"view":{"observer":"connection"},"settings":[{"krn":"krn:::setting/modelFallback/model-a",
			"value":["model-b","model-c"],"compose":"replace","contributions":[
			{"layer":"kiro-service","stated":{"value":["model-k"],"used":false}},
			{"layer":"machine","stated":{"value":["model-b","model-c"],"used":true}}]}]}`)
		rows, _ := DecodeModelFallbacks(raw)
		want := marotte.ModelFallback{Model: "model-a", Configured: "model-b", VendorDefault: "model-k", ReadOnlyReason: "multiple-targets"}
		if got := fallbackOf(t, rows, "model-a"); got != want {
			t.Errorf("model-a = %+v, want %+v", got, want)
		}
	})
	t.Run("a vendor chain of two stays editable", func(t *testing.T) {
		raw := json.RawMessage(`{"view":{"observer":"connection"},"settings":[{"krn":"krn:::setting/modelFallback/model-a",
			"value":["model-k","model-j"],"compose":"replace","contributions":[
			{"layer":"kiro-service","stated":{"value":["model-k","model-j"],"used":true}}]}]}`)
		rows, _ := DecodeModelFallbacks(raw)
		if got, want := fallbackOf(t, rows, "model-a"), (marotte.ModelFallback{Model: "model-a", VendorDefault: "model-k"}); got != want {
			t.Errorf("model-a = %+v, want %+v", got, want)
		}
	})
	t.Run("a chain holding an empty id is dropped", func(t *testing.T) {
		raw := json.RawMessage(`{"view":{"observer":"connection"},"settings":[
			{"krn":"krn:::setting/modelFallback/model-a","value":[""],"compose":"replace","contributions":[]},
			{"krn":"krn:::setting/modelFallback/model-b","value":["model-k"],"compose":"replace","contributions":[
				{"layer":"kiro-service","stated":{"value":["model-k",""],"used":false}},
				{"layer":"user-synced","stated":{"value":["model-k"],"used":true}}]},
			{"krn":"krn:::setting/modelFallback/model-c","value":["model-k"],"compose":"replace","contributions":[
				{"layer":"kiro-service","stated":{"value":["model-k"],"used":true}}]}]}`)
		rows, _ := DecodeModelFallbacks(raw)
		if want := []marotte.ModelFallback{{Model: "model-c", VendorDefault: "model-k"}}; !slices.Equal(rows, want) {
			t.Errorf("rows = %+v, want %+v: kiro-cli refuses a chain with an empty id", rows, want)
		}
	})
	t.Run("treats the text", func(t *testing.T) {
		raw := json.RawMessage(`{"view":{"observer":"connection"},"settings":[{"krn":"krn:::setting/modelFallback/model\u001b-a",
			"value":["model\nb"],"compose":"replace","contributions":[{"layer":"user-synced","stated":{"value":["model\nb"],"used":true}}]}]}`)
		rows, _ := DecodeModelFallbacks(raw)
		if len(rows) != 1 || rows[0].Model != "model -a" || rows[0].Configured != "model b" {
			t.Errorf("rows = %+v, want the control characters made spaces", rows)
		}
	})
	t.Run("session view", func(t *testing.T) {
		if got, ok := DecodeModelFallbacks(json.RawMessage(`{"view":{"observer":"session","sessionId":"s"},"settings":[]}`)); ok {
			t.Errorf("DecodeModelFallbacks(session view) = %v, true; want false", got)
		}
	})
	t.Run("undecodable", func(t *testing.T) {
		if _, ok := DecodeModelFallbacks(json.RawMessage(`{"view":`)); ok {
			t.Error("ok = true for an undecodable frame")
		}
	})
}

func TestDecodeModelFallbackSetting(t *testing.T) {
	fx := modelFallbackFixture(t)
	answer := func(name string) json.RawMessage {
		var a struct {
			Result struct {
				Setting json.RawMessage `json:"setting"`
			} `json:"result"`
		}
		if err := json.Unmarshal(fx[name], &a); err != nil {
			t.Fatal(err)
		}
		return a.Result.Setting
	}
	if got, ok := DecodeModelFallbackSetting(answer("set")); !ok || got != (marotte.ModelFallback{Model: "model-a", Configured: "model-b", VendorDefault: "model-k"}) {
		t.Errorf("the set answer = %+v, %v; want model-a configured model-b over Kiro's model-k", got, ok)
	}
	if got, ok := DecodeModelFallbackSetting(answer("clear")); !ok || got != (marotte.ModelFallback{Model: "model-a", VendorDefault: "model-k"}) {
		t.Errorf("the clear answer = %+v, %v; want model-a back on Kiro's model-k", got, ok)
	}
	if _, ok := DecodeModelFallbackSetting(json.RawMessage(`{"krn":"krn:::setting/memorySync","value":"on","contributions":[]}`)); ok {
		t.Error("ok = true for a setting that is not a model fallback")
	}
}

// TestDeviceMemoryView_Choice pins the reduction kiro-cli 3.4.0's terminal UI applies to the two device rows
// (mode ?? disabled, reflection ?? off, read_write plus reflection on reads as learn) and the lock naming.
func TestDeviceMemoryView_Choice(t *testing.T) {
	row := func(setting, value, compose, assertion string) string {
		assertions := `[]`
		if assertion != "" {
			assertions = `[{"layer":"` + assertion + `"}]`
		}
		return `{"krn":"krn:::setting/` + setting + `","value":"` + value + `","compose":"` + compose +
			`","assertions":` + assertions + `,"contributions":[]}`
	}
	view := func(observer string, rows ...string) json.RawMessage {
		return json.RawMessage(`{"view":{"observer":"` + observer + `"},"layers":[{"id":"organization","name":"Your organization"},` +
			`{"id":"user-local","name":"This device"}],"settings":[` + strings.Join(rows, ",") + `]}`)
	}
	for _, tc := range []struct {
		name string
		raw  json.RawMessage
		want marotte.DeviceMemory
	}{
		{"nothing stated", view("connection"), marotte.DeviceMemory{Mode: "off"}},
		{"disabled", view("connection", row("memoryMode", "disabled", "replace", ""), row("memoryReflection", "on", "replace", "")), marotte.DeviceMemory{Mode: "off"}},
		{"read only", view("connection", row("memoryMode", "read_only", "replace", "")), marotte.DeviceMemory{Mode: "read_only"}},
		{"read and write", view("connection", row("memoryMode", "read_write", "replace", ""), row("memoryReflection", "off", "replace", "")), marotte.DeviceMemory{Mode: "read_write"}},
		{"read and write, reflection unstated", view("connection", row("memoryMode", "read_write", "replace", "")), marotte.DeviceMemory{Mode: "read_write"}},
		{"learn", view("connection", row("memoryMode", "read_write", "replace", ""), row("memoryReflection", "on", "replace", "")), marotte.DeviceMemory{Mode: "learn"}},
		{"a mode KAS added", view("connection", row("memoryMode", "write_only", "replace", "")), marotte.DeviceMemory{Mode: "write_only"}},
		{
			"a locked mode", view("connection", row("memoryMode", "disabled", "locked", "organization")),
			marotte.DeviceMemory{Mode: "off", Lock: &marotte.SettingLock{Layer: "organization", Name: "Your organization"}},
		},
		{
			"a locked reflection under an unnamed layer", view("connection", row("memoryMode", "read_write", "replace", ""), row("memoryReflection", "off", "locked", "managed")),
			marotte.DeviceMemory{Mode: "read_write", Lock: &marotte.SettingLock{Layer: "managed", Name: "managed"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, ok := DecodeDeviceMemoryView(tc.raw)
			got := v.Choice()
			if !ok {
				t.Fatal("DecodeDeviceMemoryView ok = false, want true for a connection view")
			}
			if got.Mode != tc.want.Mode || (got.Lock == nil) != (tc.want.Lock == nil) || (got.Lock != nil && *got.Lock != *tc.want.Lock) {
				t.Errorf("Choice = %+v (lock %+v), want %+v (lock %+v)", got, got.Lock, tc.want, tc.want.Lock)
			}
		})
	}
	if _, ok := DecodeDeviceMemoryView(view("session", row("memoryMode", "read_only", "replace", ""))); ok {
		t.Error("DecodeDeviceMemoryView(session view) ok = true, want false: only the connection view states the device rows")
	}
	if _, ok := DecodeDeviceMemoryView(json.RawMessage(`{`)); ok {
		t.Error("DecodeDeviceMemoryView(undecodable) ok = true, want false")
	}
}

func TestDeviceMemoryRows_MapsEachChoiceOntoKASsRows(t *testing.T) {
	for _, tc := range []struct{ mode, wantMode, wantReflection string }{
		{"off", "disabled", "off"},
		{"read_only", "read_only", "off"},
		{"read_write", "read_write", "off"},
		{"learn", "read_write", "on"},
	} {
		m, r, ok := DeviceMemoryRows(tc.mode)
		if !ok || m != tc.wantMode || r != tc.wantReflection {
			t.Errorf("DeviceMemoryRows(%q) = (%q, %q, %v), want (%q, %q, true)", tc.mode, m, r, ok, tc.wantMode, tc.wantReflection)
		}
	}
	if _, _, ok := DeviceMemoryRows("disabled"); ok {
		t.Error(`DeviceMemoryRows("disabled") ok = true, want false: the client speaks the dropdown's four`)
	}
}
