package mcp

import (
	"encoding/json"
	"testing"
)

// A registry entry names a catalog server, so KAS needs `{"type":"registry"}`
// and nothing it would take for a command or url.
func TestRenderKASServers_RegistryEntry(t *testing.T) {
	out := renderKASServers([]*Server{{Name: "github", Transport: TransportRegistry, Enabled: true}}, kasRenderPolicy{})
	raw, err := json.Marshal(out["github"])
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"type":"registry"}` {
		t.Errorf("rendered registry entry = %s, want {\"type\":\"registry\"}", raw)
	}
	off := renderKASServers([]*Server{{Name: "github", Transport: TransportRegistry}}, kasRenderPolicy{})
	if raw, _ := json.Marshal(off["github"]); string(raw) != `{"type":"registry","disabled":true}` {
		t.Errorf("rendered disabled registry entry = %s", raw)
	}
}

func TestValidate_RegistryRefusesCatalogFields(t *testing.T) {
	if err := Validate(&Server{Name: "github", Transport: TransportRegistry}); err != nil {
		t.Errorf("Validate(bare registry entry) = %v, want nil", err)
	}
	err := Validate(&Server{Name: "github", Transport: TransportRegistry, URL: "https://x.example/mcp", Command: "npx"})
	fields := map[string]bool{}
	for _, fe := range FieldErrors(err) {
		fields[fe.Field] = true
	}
	if !fields[fieldURL] || !fields[fieldCommand] {
		t.Errorf("Validate(registry with url and command) fields = %v, want url and command refused", fields)
	}
}
