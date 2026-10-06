package mcp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newPowersStore(t *testing.T, seed string) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	kas := filepath.Join(dir, "kas-mcp.json")
	if seed != "" {
		if err := os.WriteFile(kas, []byte(seed), 0o600); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	s, err := New(t.Context(), dir, nil, WithKASConfigPath(kas))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, kas
}

func TestWritePowersServers_RendersTheBlockAndKeepsTheServers(t *testing.T) {
	s, kas := newPowersStore(t, `{"powers":{"other":1}}`)
	if _, err := s.Create(t.Context(), &Server{Name: "mine", Transport: TransportStdio, Command: "x", Enabled: true}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	changed, err := s.WritePowersServers(t.Context(), map[string]json.RawMessage{
		"power-postman-postman": json.RawMessage(`{"url":"https://mcp.postman.com/minimal"}`),
	})
	if err != nil || !changed {
		t.Fatalf("WritePowersServers = %v, %v; want true, nil", changed, err)
	}
	doc := readKAS(t, kas)
	if !strings.Contains(string(doc[kasServerKey]), `"mine"`) {
		t.Errorf("mcpServers = %s, want marotte's own server kept", doc[kasServerKey])
	}
	var block map[string]json.RawMessage
	if err := json.Unmarshal(doc[kasPowersKey], &block); err != nil {
		t.Fatalf("powers = %s: %v", doc[kasPowersKey], err)
	}
	if string(block["other"]) != "1" {
		t.Errorf("powers.other = %s, want the foreign member kept", block["other"])
	}
	if !strings.Contains(string(block[kasServerKey]), "power-postman-postman") {
		t.Errorf("powers.mcpServers = %s, want the rendered server", block[kasServerKey])
	}

	if _, err := s.Create(t.Context(), &Server{Name: "second", Transport: TransportStdio, Command: "y", Enabled: true}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.Contains(string(readKAS(t, kas)[kasPowersKey]), "power-postman-postman") {
		t.Errorf("a persist dropped the powers block")
	}
}

func TestWritePowersServers_AnUnchangedSetIsNotRewritten(t *testing.T) {
	s, kas := newPowersStore(t, `{"powers":{"mcpServers":{"power-a-b":{"command":"c"}}}}`)
	before, err := os.Stat(kas)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	changed, err := s.WritePowersServers(t.Context(), map[string]json.RawMessage{
		"power-a-b": json.RawMessage(`{ "command" : "c" }`),
	})
	if err != nil || changed {
		t.Fatalf("WritePowersServers = %v, %v; want false, nil for the same set", changed, err)
	}
	after, err := os.Stat(kas)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("the file was rewritten for an unchanged set")
	}
}

func TestWritePowersServers_AnEmptySetRemovesTheBlock(t *testing.T) {
	s, kas := newPowersStore(t, `{"powers":{"mcpServers":{"power-a-b":{"command":"c"}}}}`)
	changed, err := s.WritePowersServers(t.Context(), nil)
	if err != nil || !changed {
		t.Fatalf("WritePowersServers = %v, %v; want true, nil", changed, err)
	}
	if raw, ok := readKAS(t, kas)[kasPowersKey]; ok {
		t.Errorf("powers = %s, want the emptied block removed", raw)
	}
}

func TestWritePowersServers_AnEmptySetNormalizesEveryLeftoverShape(t *testing.T) {
	for _, tc := range []struct {
		name, seed, wantPowers string
		wantOwnServers         bool
	}{
		{name: "empty block", seed: `{"powers":{}}`},
		{name: "null block", seed: `{"powers":null}`},
		{name: "non-object block", seed: `{"powers":[1]}`},
		{name: "empty member", seed: `{"powers":{"mcpServers":{}}}`},
		{name: "null member", seed: `{"powers":{"mcpServers":null}}`},
		{name: "non-object member", seed: `{"powers":{"mcpServers":[],"other":1}}`, wantPowers: `{"other":1}`},
		{name: "invalid document", seed: `{not json`, wantOwnServers: true},
		{name: "null document", seed: `null`, wantOwnServers: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, kas := newPowersStore(t, "")
			if _, err := s.Create(t.Context(), &Server{Name: "mine", Transport: TransportStdio, Command: "x", Enabled: true}); err != nil {
				t.Fatalf("Create: %v", err)
			}
			if err := os.WriteFile(kas, []byte(tc.seed), 0o600); err != nil {
				t.Fatalf("seed: %v", err)
			}
			changed, err := s.WritePowersServers(t.Context(), nil)
			if err != nil || !changed {
				t.Fatalf("WritePowersServers(nil) over %s = %v, %v; want true, nil", tc.seed, changed, err)
			}
			doc := readKAS(t, kas)
			var got bytes.Buffer
			if raw, ok := doc[kasPowersKey]; ok {
				if err := json.Compact(&got, raw); err != nil {
					t.Fatalf("powers = %s: %v", raw, err)
				}
			}
			if got.String() != tc.wantPowers {
				t.Errorf("powers over %s = %q, want %q", tc.seed, got.String(), tc.wantPowers)
			}
			if own := strings.Contains(string(doc[kasServerKey]), `"mine"`); own != tc.wantOwnServers {
				t.Errorf("mcpServers over %s = %s, want marotte's own server %v", tc.seed, doc[kasServerKey], tc.wantOwnServers)
			}
			if changed, err := s.WritePowersServers(t.Context(), nil); err != nil || changed {
				t.Errorf("second WritePowersServers(nil) over %s = %v, %v; want false, nil", tc.seed, changed, err)
			}
		})
	}
}

func TestWritePowersServers_ASetReplacesEveryInvalidShape(t *testing.T) {
	for _, seed := range []string{`null`, `{"powers":null}`, `{"powers":[1]}`, `{"powers":{"mcpServers":null}}`} {
		t.Run(seed, func(t *testing.T) {
			s, kas := newPowersStore(t, "")
			if err := os.WriteFile(kas, []byte(seed), 0o600); err != nil {
				t.Fatalf("seed: %v", err)
			}
			changed, err := s.WritePowersServers(t.Context(), map[string]json.RawMessage{
				"power-a-b": json.RawMessage(`{"command":"c"}`),
			})
			if err != nil || !changed {
				t.Fatalf("WritePowersServers over %s = %v, %v; want true, nil", seed, changed, err)
			}
			if raw := readKAS(t, kas)[kasPowersKey]; !strings.Contains(string(raw), "power-a-b") {
				t.Errorf("powers over %s = %s, want the rendered server", seed, raw)
			}
		})
	}
}
