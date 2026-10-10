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
