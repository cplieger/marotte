package mcp

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/tailscale/hujson"
)

// KAS reads its mcp.json with comment-json: line and block comments, and a trailing comma in an
// object or array. marotte reads the same dialect and rewrites only its own members.

func readKASText(t *testing.T, path string) string {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}

// splitAround returns what body holds before and after the first occurrence of value at marker.
func splitAround(t *testing.T, body, marker, value string) (before, after string) {
	t.Helper()
	at := strings.Index(body, marker)
	if at < 0 || !strings.HasPrefix(body[at+len(marker):], value) {
		t.Fatalf("fixture lacks %s%s", marker, value)
	}
	return body[:at+len(marker)], body[at+len(marker)+len(value):]
}

func TestHealKASConfig_JSONCKeepsEveryByteOutsideMcpServers(t *testing.T) {
	s, kas := newHealStore(t)
	body := "// top line comment\n{\n  /* block before */ \"foreign\": {\"keep\": [1, 2,],}, // trailing\n" +
		"  \"mcpServers\": {},\n  \"tail\": true, // after tail\n}\n"
	writeKASFile(t, kas, body)
	before, after := splitAround(t, body, `"mcpServers": `, `{}`)

	healed, err := s.HealKASConfig(t.Context(), "poll")
	if err != nil || !healed {
		t.Fatalf("HealKASConfig = %v, %v; want true, nil", healed, err)
	}
	got := readKASText(t, kas)
	if !strings.HasPrefix(got, before) || !strings.HasSuffix(got, after) {
		t.Fatalf("bytes outside mcpServers changed:\n%s", got)
	}
	var servers map[string]map[string]any
	if err := json.Unmarshal([]byte(got[len(before):len(got)-len(after)]), &servers); err != nil {
		t.Fatalf("mcpServers value %q: %v", got[len(before):len(got)-len(after)], err)
	}
	if !reflect.DeepEqual(servers, wantRendered(t, s)) {
		t.Errorf("mcpServers = %v, want %v", servers, wantRendered(t, s))
	}
	if healed, err := s.HealKASConfig(t.Context(), "poll"); err != nil || healed {
		t.Errorf("second heal = %v, %v; the JSONC file now matches", healed, err)
	}
}

func TestPersist_JSONCKeepsCommentsInsideMcpServers(t *testing.T) {
	s, kas := newHealStore(t)
	two, err := s.Create(t.Context(), &Server{Name: "two", Transport: TransportStdio, Command: "c2", Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	gh := `"gh": {"command": "bash", "args": ["-y"]},`
	body := "{\n  // servers below are marotte's\n  \"mcpServers\": {\n    // gh comment\n    " + gh +
		"\n    /* two comment */ \"two\": {\"command\": \"c2\"},\n  },\n  \"x\": 1, /* x */\n}\n"
	writeKASFile(t, kas, body)
	if healed, err := s.HealKASConfig(t.Context(), "poll"); err != nil || healed {
		t.Fatalf("HealKASConfig = %v, %v; the fixture is marotte's render", healed, err)
	}
	kept := []string{"// servers below are marotte's", "// gh comment\n    " + gh, "/* two comment */", "\"x\": 1, /* x */"}

	if _, err := s.setEnabled(t.Context(), two.ID, false); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	if _, err := s.Create(t.Context(), &Server{Name: "three", Transport: TransportStdio, Command: "c3", Enabled: true}); err != nil {
		t.Fatalf("install: %v", err)
	}
	got := readKASText(t, kas)
	for _, want := range kept {
		if !strings.Contains(got, want) {
			t.Errorf("lost %q:\n%s", want, got)
		}
	}
	if !strings.HasPrefix(got, "{\n  // servers below are marotte's\n  \"mcpServers\": {") {
		t.Errorf("bytes before mcpServers changed:\n%s", got)
	}
	if !strings.Contains(got, `"disabled": true`) || !strings.Contains(got, `"three"`) {
		t.Errorf("the toggle or the install did not reach the file:\n%s", got)
	}
	if healed, err := s.HealKASConfig(t.Context(), "poll"); err != nil || healed {
		t.Errorf("heal after the saves = %v, %v; want false, nil", healed, err)
	}
}

func TestHealKASConfig_JSONCFileShapes(t *testing.T) {
	for _, tc := range []struct{ name, body, keep string }{
		{name: "line comment at end of file", body: `{"mcpServers": {}} // end`, keep: "// end"},
		{name: "byte order mark", body: "\uFEFF{\"mcpServers\": {}, /* bom */}", keep: "/* bom */"},
		{name: "null document", body: "/* empty */ null\n", keep: "/* empty */ "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, kas := newHealStore(t)
			writeKASFile(t, kas, tc.body)
			if healed, err := s.HealKASConfig(t.Context(), "poll"); err != nil || !healed {
				t.Fatalf("HealKASConfig = %v, %v; want true, nil", healed, err)
			}
			got := readKASText(t, kas)
			if !strings.Contains(got, tc.keep) || !strings.Contains(got, `"gh"`) {
				t.Errorf("file = %q, want %q kept and gh rendered", got, tc.keep)
			}
		})
	}
}

// KAS takes the last of a duplicated member, so that copy carries the render and an earlier one
// keeps its bytes.
func TestHealKASConfig_TheLastDuplicateMcpServersIsRendered(t *testing.T) {
	s, kas := newHealStore(t)
	writeKASFile(t, kas, `{"mcpServers": {"first": {}}, // first
"mcpServers": {"stale": {"command": "s"}}}`)
	if _, err := s.HealKASConfig(t.Context(), "poll"); err != nil {
		t.Fatalf("HealKASConfig: %v", err)
	}
	got := readKASText(t, kas)
	if !strings.HasPrefix(got, `{"mcpServers": {"first": {}}, // first`) || strings.Contains(got, "stale") {
		t.Errorf("file = %s, want the first copy kept and the last rendered", got)
	}
	std, err := hujson.Standardize([]byte(got))
	if err != nil {
		t.Fatalf("Standardize: %v", err)
	}
	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(std, &doc); err != nil || doc.MCPServers["gh"] == nil {
		t.Errorf("the copy KAS reads lacks gh (%v): %s", err, got)
	}
}

func TestWritePowersServers_JSONCKeepsTheBlocksOtherMembers(t *testing.T) {
	seed := "{\n  // keep\n  \"powers\": {\n    \"other\": 1, // other\n  },\n}\n"
	s, kas := newPowersStore(t, seed)
	captureSlog(t)
	if _, err := s.WritePowersServers(t.Context(), map[string]json.RawMessage{
		"power-a-b": json.RawMessage(`{"command":"c"}`),
	}); err != nil {
		t.Fatalf("WritePowersServers: %v", err)
	}
	got := readKASText(t, kas)
	for _, want := range []string{"// keep\n  \"powers\": {", "\"other\": 1, // other", `"power-a-b"`} {
		if !strings.Contains(got, want) {
			t.Errorf("lost %q:\n%s", want, got)
		}
	}
	if _, err := s.WritePowersServers(t.Context(), nil); err != nil {
		t.Fatalf("WritePowersServers(empty): %v", err)
	}
	if got := readKASText(t, kas); strings.Contains(got, "power-a-b") || !strings.Contains(got, "\"other\": 1") || !strings.Contains(got, "// other") {
		t.Errorf("an empty set did not remove only its member:\n%s", got)
	}
}
