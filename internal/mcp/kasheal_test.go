package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

const driftWarn = `msg="mcp: kas config file had drifted from the store; re-rendered it"`

func newHealStore(t *testing.T) (*Store, string) {
	t.Helper()
	s, kas := newIsolatedStore(t)
	if _, err := s.Create(t.Context(), &Server{
		Transport: TransportStdio, Name: "gh", Command: "bash", Args: []string{"-y"}, Enabled: true,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return s, kas
}

func writeKASFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// wantRendered is the file's `mcpServers` member a save of s would write, decoded.
func wantRendered(t *testing.T, s *Store) map[string]map[string]any {
	t.Helper()
	raw, err := json.Marshal(renderKASServers(s.servers, kasRenderPolicy{}))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestHealKASConfig_RestoresWhatAnotherWriterChanged(t *testing.T) {
	cases := []struct {
		name  string
		drift func(t *testing.T, kas string)
		field string
	}{
		{"missing", func(t *testing.T, kas string) { writeKASFile(t, kas, `{"mcpServers": {}}`) }, "missing=gh"},
		{"unexpected", func(t *testing.T, kas string) {
			writeKASFile(t, kas, `{"mcpServers": {"gh": {"command": "bash", "args": ["-y"]}, "intruder": {"command": "sh"}}}`)
		}, "unexpected=intruder"},
		{"changed", func(t *testing.T, kas string) {
			writeKASFile(t, kas, `{"mcpServers": {"gh": {"command": "bash", "args": ["-y"], "cwd": "/tmp"}}}`)
		}, "changed=gh"},
		{"file deleted", func(t *testing.T, kas string) {
			if err := os.Remove(kas); err != nil {
				t.Fatal(err)
			}
		}, "missing=gh"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, kas := newHealStore(t)
			logs := captureSlog(t)
			tc.drift(t, kas)

			healed, err := s.HealKASConfig(t.Context(), "poll")
			if err != nil || !healed {
				t.Fatalf("HealKASConfig = %v, %v; want true, nil", healed, err)
			}
			if got, want := readKASServers(t, kas), wantRendered(t, s); !reflect.DeepEqual(got, want) {
				t.Errorf("mcpServers after heal = %v, want %v", got, want)
			}
			line := logs.String()
			for _, want := range []string{driftWarn, "level=WARN", "trigger=poll", tc.field} {
				if !strings.Contains(line, want) {
					t.Errorf("drift log lacks %q:\n%s", want, line)
				}
			}

			before, _ := os.ReadFile(kas)
			logs.Reset()
			healed, err = s.HealKASConfig(t.Context(), "poll")
			if err != nil || healed {
				t.Errorf("second HealKASConfig = %v, %v; want false, nil", healed, err)
			}
			if after, _ := os.ReadFile(kas); !bytes.Equal(before, after) {
				t.Errorf("a heal with no drift rewrote the file:\n%s\nto\n%s", before, after)
			}
			if strings.Contains(logs.String(), driftWarn) {
				t.Errorf("a heal with no drift warned:\n%s", logs)
			}
		})
	}
}

func TestHealKASConfig_RestoresADeletedFileOnAnEmptyStore(t *testing.T) {
	s, kas := newIsolatedStore(t)
	logs := captureSlog(t)
	if err := os.Remove(kas); err != nil {
		t.Fatal(err)
	}

	healed, err := s.HealKASConfig(t.Context(), "poll")
	if err != nil || !healed {
		t.Fatalf("HealKASConfig = %v, %v; want true, nil", healed, err)
	}
	if got := readKASServers(t, kas); len(got) != 0 {
		t.Errorf("mcpServers after heal = %v, want an empty member", got)
	}
	if !strings.Contains(logs.String(), driftWarn) {
		t.Errorf("no drift WARN for the deleted file:\n%s", logs)
	}
	if healed, err := s.HealKASConfig(t.Context(), "poll"); err != nil || healed {
		t.Errorf("second HealKASConfig = %v, %v; want false, nil", healed, err)
	}
}

// The wait setting reaches the file lazily, so a heal can be the write that carries it.
func TestHealKASConfig_APendingWaitSettingIsNotBlamedOnAnotherWriter(t *testing.T) {
	cases := []struct {
		name     string
		handEdit bool
		wantLine string
		notLine  string
	}{
		{"only the setting changed", false, `msg="mcp: kas config file carried the new wait setting"`, driftWarn},
		{"the file was also edited", true, driftWarn, "carried the new wait setting"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			kas := filepath.Join(dir, "kas", "mcp.json")
			var wait atomic.Bool
			s, err := New(t.Context(), dir, nil, withKASConfigPath(kas),
				WithWaitForReady(func(context.Context) (bool, bool) { return wait.Load(), true }))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if _, err := s.Create(t.Context(), waitServers()[0]); err != nil {
				t.Fatalf("Create: %v", err)
			}
			if tc.handEdit {
				writeKASFile(t, kas, `{"mcpServers": {"local": {"command": "npx", "cwd": "/tmp"}}}`)
			}
			logs := captureSlog(t)
			wait.Store(true)

			if healed, err := s.HealKASConfig(t.Context(), "poll"); err != nil || !healed {
				t.Fatalf("HealKASConfig = %v, %v; want true, nil", healed, err)
			}
			if got := readKASServers(t, kas)["local"]["waitForReady"]; got != true {
				t.Errorf("local waitForReady after heal = %v, want true", got)
			}
			line := logs.String()
			if !strings.Contains(line, tc.wantLine) || strings.Contains(line, tc.notLine) {
				t.Errorf("heal log = %s, want %s and not %s", line, tc.wantLine, tc.notLine)
			}
		})
	}
}

func TestHealKASConfig_ReformattedFileIsNotDrift(t *testing.T) {
	s, kas := newHealStore(t)
	compact := `{"mcpServers":{"gh":{"args":["-y"],"command":"bash"}}}`
	writeKASFile(t, kas, compact)

	healed, err := s.HealKASConfig(t.Context(), "poll")
	if err != nil || healed {
		t.Errorf("HealKASConfig = %v, %v; want false, nil", healed, err)
	}
	if got, _ := os.ReadFile(kas); string(got) != compact {
		t.Errorf("file = %s, want it left as %s", got, compact)
	}
}

func TestHealKASConfig_KeepsKeysItDoesNotOwn(t *testing.T) {
	s, kas := newHealStore(t)
	powers := `{"mcpServers":{"pw":{"command":"p"}}}`
	foreign := `{"keep":[1,2]}`
	writeKASFile(t, kas, `{"mcpServers": {}, "powers": `+powers+`, "foreign": `+foreign+`}`)

	if healed, err := s.HealKASConfig(t.Context(), "poll"); err != nil || !healed {
		t.Fatalf("HealKASConfig = %v, %v; want true, nil", healed, err)
	}
	doc := readKAS(t, kas)
	for key, want := range map[string]string{kasPowersKey: powers, "foreign": foreign} {
		var got bytes.Buffer
		if err := json.Compact(&got, doc[key]); err != nil || got.String() != want {
			t.Errorf("%s after heal = %s (%v), want %s", key, got.String(), err, want)
		}
	}
}

func TestHealKASConfig_LeavesAnUnreadableFileAlone(t *testing.T) {
	s, kas := newHealStore(t)
	captureSlog(t)
	body := "// hand-written\n{\"mcpServers\": {}}"
	writeKASFile(t, kas, body)

	_, err := s.HealKASConfig(t.Context(), "poll")
	if !errors.Is(err, ErrKASUnreadable) {
		t.Errorf("HealKASConfig error = %v, want ErrKASUnreadable", err)
	}
	if got, _ := os.ReadFile(kas); string(got) != body {
		t.Errorf("unreadable file was rewritten to %s", got)
	}
}
