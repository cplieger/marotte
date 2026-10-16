package translate

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func TestDocKeyFromAbs(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"/workspace/.kiro/agents/a.json", "workspace/.kiro/agents/a.json", true},
		{"/workspace/repo/../.kiro/agents/a.json", "workspace/.kiro/agents/a.json", true},
		{"/workspace/.kiro/agents/", "workspace/.kiro/agents", true},
		{"workspace/.kiro/agents/a.json", "", false},
		{"", "", false},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := DocKeyFromAbs(tc.in)
			if got != tc.want || ok != tc.ok {
				t.Errorf("DocKeyFromAbs(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestDecodeConfigurationStanding_DisplacedAndDisabled(t *testing.T) {
	inactive := readStandingFixture(t, "testdata/configuration_standing_inactive.json")
	got, ok := DecodeConfigurationStanding(inactive)
	if !ok {
		t.Fatal("the captured inactiveItems answer did not decode")
	}
	want := marotte.ConfigStanding{
		Type: "mcp-server", Key: "off-server", Kind: "disabled",
		DecidedBy: "workspace", DecidedByName: "Workspace",
		Setting: "krn:::setting/mcp-server/krn%3Aworkspace%3A89bcb80485872fb7%3Amcp-server/.kiro/settings/mcp.json#mcpServers/off-server/enabled",
	}
	if len(got) != 1 || got[0] != want {
		t.Errorf("standing = %+v, want exactly %+v", got, want)
	}
	active, ok := DecodeConfigurationStanding(readStandingFixture(t, "testdata/configuration_standing_active.json"))
	if !ok || len(active) != 0 {
		t.Errorf("the capture without inactiveItems = %+v (ok %v), want no standing", active, ok)
	}
}

func TestDecodeConfigurationStanding_UnknownKindKept(t *testing.T) {
	// The agent path is spliced in: a backtick string is scanned as prose, where a private document path is refused.
	raw := json.RawMessage(strings.ReplaceAll(`{"layers":[{"id":"user-local","name":"Local Settings"}],"resources":[
		{"type":"agent","items":[
			{"readFrom":{"uri":"file:///workspace/@AGENT@"},"standing":{"kind":"quarantined","decidedBy":"user-local"}},
			{"standing":{"kind":"displaced","decidedBy":"user-local","compose":"replace"},"value":{"name":"no-file"}},
			{"readFrom":{"uri":"file:///workspace/repo/@AGENT@"},"standing":{"kind":"displaced","decidedBy":"user-local","compose":"replace"}}
		]}]}`, "@AGENT@", ".kiro/agents/a.json"))
	got, ok := DecodeConfigurationStanding(raw)
	if !ok {
		t.Fatal("did not decode")
	}
	want := []marotte.ConfigStanding{
		{Type: "agent", Key: "workspace/.kiro/agents/a.json", Kind: "quarantined", DecidedBy: "user-local", DecidedByName: "Local Settings"},
		{Type: "agent", Key: "workspace/repo/.kiro/agents/a.json", Kind: "displaced", DecidedBy: "user-local", DecidedByName: "Local Settings", Compose: "replace"},
	}
	if len(got) != len(want) {
		t.Fatalf("standing = %+v, want %+v (the item with no file joins no row)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("standing[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func readStandingFixture(t *testing.T, path string) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return raw
}
