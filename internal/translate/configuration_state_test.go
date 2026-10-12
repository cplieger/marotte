package translate

import (
	"encoding/json"
	"os"
	"path/filepath"
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
