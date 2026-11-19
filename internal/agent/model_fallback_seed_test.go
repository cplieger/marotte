package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

// fallbackView is a _kiro/configuration/state notification over one of the kiro-cli 3.2.1 connection views in
// internal/translate/testdata/configuration_model_fallback.json; edit, when set, rewrites its settings rows first.
func fallbackView(t *testing.T, name string, edit func(rows []map[string]any)) *marotte.RPCResponse {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "translate", "testdata", "configuration_model_fallback.json"))
	if err != nil {
		t.Fatalf("read the configuration_model_fallback fixture: %v", err)
	}
	var fx map[string]json.RawMessage
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatalf("parse the configuration_model_fallback fixture: %v", err)
	}
	var view map[string]any
	if err := json.Unmarshal(fx[name], &view); err != nil {
		t.Fatalf("parse the fixture's %s view: %v", name, err)
	}
	if edit != nil {
		raw := view["settings"].([]any)
		rows := make([]map[string]any, len(raw))
		for i, r := range raw {
			rows[i] = r.(map[string]any)
		}
		edit(rows)
	}
	params, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal the %s view: %v", name, err)
	}
	return &marotte.RPCResponse{Method: methodKiroConfigurationState, Params: params}
}

// fallbackWrites records each modelFallback write as "<model>=<target>" and answers it as KAS does, the chain stated
// at user-synced over Kiro's own.
type fallbackWrites struct {
	got []string
	mu  sync.Mutex
}

func (w *fallbackWrites) hook(method string, p map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
	if method != methodKiroConfigurationSet {
		return nil, nil, false
	}
	krn, _ := p["krn"].(string)
	raw, _ := json.Marshal(p["value"])
	var chain []string
	_ = json.Unmarshal(raw, &chain)
	target := ""
	if len(chain) == 1 {
		target = chain[0]
	}
	w.mu.Lock()
	w.got = append(w.got, strings.TrimPrefix(krn, modelFallbackKRN)+"="+target)
	w.mu.Unlock()
	res, _ := json.Marshal(map[string]any{"setting": map[string]any{
		"krn": krn, "value": chain, "compose": "replace",
		"contributions": []any{
			map[string]any{"layer": "kiro-service", "stated": map[string]any{"value": chain, "used": false}},
			map[string]any{"layer": "user-synced", "stated": map[string]any{"value": chain, "used": true}},
		},
	}})
	return res, nil, true
}

func (w *fallbackWrites) sorted() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Sorted(slices.Values(w.got))
}

func seededModels(t *testing.T, dir string) []string {
	t.Helper()
	doc, err := settings.Stored(dir)
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	var out []string
	if raw, ok := doc[settings.KeyModelFallbackSeeded]; ok {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode %s: %v", settings.KeyModelFallbackSeeded, err)
		}
	}
	return out
}

func writeConfig(t *testing.T, dir string, doc map[string]any) {
	t.Helper()
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal config.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, settings.Filename), body, 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
}

func seedHub(t *testing.T, opts ...Option) (*Runtime, *fakeBridge, *fallbackWrites, string) {
	t.Helper()
	dir := t.TempDir()
	h, _, br := newTestHub(append([]Option{WithConfigDir(dir)}, opts...)...)
	t.Cleanup(func() { shutdownHub(t, h) })
	w := &fallbackWrites{}
	br.setOnCall(w.hook)
	return h, br, w, dir
}

func pushView(t *testing.T, h *Runtime, msg *marotte.RPCResponse) {
	t.Helper()
	if !h.dispatchWorkspace(t.Context(), "c1", msg) {
		t.Fatal("the workspace table did not claim _kiro/configuration/state")
	}
	joinInflight(h)
}

// In the capture Kiro states a default for model-a, model-b, model-i and model-p only; every other row, Auto's
// included, states an empty chain.
var capturedVendorSeeds = []string{"model-a=model-k", "model-b=model-j", "model-i=model-k", "model-p=model-k"}

func TestSeedModelFallbacks_WritesKirosDefaultOncePerModel(t *testing.T) {
	h, _, w, dir := seedHub(t)

	pushView(t, h, fallbackView(t, "connection", nil))

	if got := w.sorted(); !slices.Equal(got, capturedVendorSeeds) {
		t.Errorf("fallback writes = %v, want Kiro's default for each model that has one: %v", got, capturedVendorSeeds)
	}
	want := []string{"model-a", "model-b", "model-i", "model-p"}
	if got := seededModels(t, dir); !slices.Equal(got, want) {
		t.Errorf("%s = %v, want %v", settings.KeyModelFallbackSeeded, got, want)
	}
	for _, f := range h.config.ModelFallbacks() {
		if f.Model == "model-a" && f.Configured != "model-k" {
			t.Errorf("model-a's row after the seed = %+v, want configured model-k so GET /api/settings answers it", f)
		}
	}

	// The user clears every row back to Kiro's default: KAS reports them unset again, and none is filled again.
	pushView(t, h, fallbackView(t, "connection", nil))
	if got := w.sorted(); !slices.Equal(got, capturedVendorSeeds) {
		t.Errorf("fallback writes after a second view = %v, want only the first view's %v", got, capturedVendorSeeds)
	}
}

func TestSeedModelFallbacks_RecordsAChosenRowWithoutWritingIt(t *testing.T) {
	h, _, w, dir := seedHub(t)

	pushView(t, h, fallbackView(t, "connection_configured", nil))

	want := []string{"model-b=model-j", "model-i=model-k", "model-p=model-k"}
	if got := w.sorted(); !slices.Equal(got, want) {
		t.Errorf("fallback writes = %v, want %v: model-a already has the account's model-b", got, want)
	}
	if got := seededModels(t, dir); !slices.Contains(got, "model-a") {
		t.Errorf("%s = %v, want model-a recorded, so clearing it later is not undone", settings.KeyModelFallbackSeeded, got)
	}
}

func TestSeedModelFallbacks_LeavesALockedRowAndAuto(t *testing.T) {
	h, _, w, dir := seedHub(t)

	pushView(t, h, fallbackView(t, "connection", func(rows []map[string]any) {
		for _, r := range rows {
			switch r["krn"] {
			case modelFallbackKRN + "model-a":
				r["compose"] = "locked"
			case modelFallbackKRN + "auto":
				r["value"] = []any{"model-k"}
				r["contributions"] = []any{map[string]any{"layer": "kiro-service", "stated": map[string]any{"value": []any{"model-k"}, "used": true}}}
			}
		}
	}))

	if got := w.sorted(); slices.Contains(got, "model-a=model-k") || slices.Contains(got, "auto=model-k") {
		t.Errorf("fallback writes = %v, want none for model-a, whose row is locked, nor for Auto", got)
	}
	if got := seededModels(t, dir); slices.Contains(got, "model-a") {
		t.Errorf("%s = %v, want model-a unrecorded, so it is seeded once unlocked", settings.KeyModelFallbackSeeded, got)
	}
}

func TestSeedModelFallbacks_SkippedWithoutAKiroAccountOrTheSwitch(t *testing.T) {
	t.Run("model fallback off", func(t *testing.T) {
		h, _, w, dir := seedHub(t)
		writeConfig(t, dir, map[string]any{settings.KeyModelFallback: false})
		pushView(t, h, fallbackView(t, "connection", nil))
		if got := w.sorted(); len(got) != 0 {
			t.Errorf("fallback writes = %v, want none while Model fallback is off", got)
		}
		// Turned on, the restarted engine reports the very same rows.
		writeConfig(t, dir, map[string]any{settings.KeyModelFallback: true})
		pushView(t, h, fallbackView(t, "connection", nil))
		if got := w.sorted(); !slices.Equal(got, capturedVendorSeeds) {
			t.Errorf("fallback writes once Model fallback is on = %v, want %v", got, capturedVendorSeeds)
		}
	})
	t.Run("an API key", func(t *testing.T) {
		h, _, w, _ := seedHub(t, WithAPIKey(true))
		pushView(t, h, fallbackView(t, "connection", nil))
		if got := w.sorted(); len(got) != 0 {
			t.Errorf("fallback writes = %v, want none: an API key has no account layer", got)
		}
	})
	t.Run("no config dir", func(t *testing.T) {
		h, _, br := newTestHub()
		t.Cleanup(func() { shutdownHub(t, h) })
		w := &fallbackWrites{}
		br.setOnCall(w.hook)
		pushView(t, h, fallbackView(t, "connection", nil))
		if got := w.sorted(); len(got) != 0 {
			t.Errorf("fallback writes = %v, want none where the seed cannot be recorded", got)
		}
	})
}

func TestSeedModelFallbacks_AFailedWriteIsTriedAgain(t *testing.T) {
	h, br, w, dir := seedHub(t)
	br.setCallRPCErr(methodKiroConfigurationSet, &marotte.RPCError{Code: -32000, Message: "Fallback preference not stored: try again"})

	pushView(t, h, fallbackView(t, "connection", nil))
	if got := seededModels(t, dir); len(got) != 0 {
		t.Errorf("%s after refused writes = %v, want nothing recorded", settings.KeyModelFallbackSeeded, got)
	}

	br.clearCallRPCErr(methodKiroConfigurationSet)
	pushView(t, h, fallbackView(t, "connection", nil))
	if got := w.sorted(); !slices.Equal(got, capturedVendorSeeds) {
		t.Errorf("fallback writes once KAS stores them = %v, want %v", got, capturedVendorSeeds)
	}
}
