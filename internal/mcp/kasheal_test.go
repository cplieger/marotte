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

// An empty store renders `{}`, so every other member value is drift, an empty-looking one too.
func TestHealKASConfig_AnEmptyStoreHealsEveryMemberItWouldNotWrite(t *testing.T) {
	cases := []struct {
		name   string
		body   string // "" deletes the file
		member string
	}{
		{"file deleted", "", "absent"},
		{"member absent", `{"other": 1}`, "absent"},
		{"member null", `{"mcpServers": null}`, "null"},
		{"member array", `{"mcpServers": []}`, "array"},
		{"member string", `{"mcpServers": "bad"}`, "string"},
		{"member number", `{"mcpServers": 7}`, "number"},
		{"member boolean", `{"mcpServers": false}`, "boolean"},
		{"server added", `{"mcpServers": {"x": {"command": "sh"}}}`, "object"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, kas := newIsolatedStore(t)
			if tc.body == "" {
				if err := os.Remove(kas); err != nil {
					t.Fatal(err)
				}
			} else {
				writeKASFile(t, kas, tc.body)
			}
			logs := captureSlog(t)

			healed, err := s.HealKASConfig(t.Context(), "poll")
			if err != nil || !healed {
				t.Fatalf("HealKASConfig = %v, %v; want true, nil", healed, err)
			}
			var member any
			if err := json.Unmarshal(readKAS(t, kas)[kasServerKey], &member); err != nil {
				t.Fatalf("parse mcpServers: %v", err)
			}
			if m, ok := member.(map[string]any); !ok || len(m) != 0 {
				t.Errorf("mcpServers after heal = %#v, want an empty object", member)
			}
			line := logs.String()
			for _, want := range []string{driftWarn, "level=WARN", "member=" + tc.member} {
				if !strings.Contains(line, want) {
					t.Errorf("heal log lacks %q:\n%s", want, line)
				}
			}

			logs.Reset()
			if healed, err := s.HealKASConfig(t.Context(), "poll"); err != nil || healed {
				t.Errorf("second HealKASConfig = %v, %v; want false, nil", healed, err)
			}
			if strings.Contains(logs.String(), driftWarn) {
				t.Errorf("the second heal warned:\n%s", logs)
			}
		})
	}
}

func TestHealKASConfig_AnEmptyStoreLeavesItsOwnRenderAlone(t *testing.T) {
	s, kas := newIsolatedStore(t)
	body := `{"mcpServers": {}, "other": 1}`
	writeKASFile(t, kas, body)
	logs := captureSlog(t)

	if healed, err := s.HealKASConfig(t.Context(), "poll"); err != nil || healed {
		t.Errorf("HealKASConfig = %v, %v; want false, nil", healed, err)
	}
	if got, _ := os.ReadFile(kas); string(got) != body {
		t.Errorf("file = %s, want it left as %s", got, body)
	}
	if strings.Contains(logs.String(), driftWarn) {
		t.Errorf("a file already as rendered warned:\n%s", logs)
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

// An empty store renders `{}` under either wait setting, so only a raw-value comparison tells a
// foreign null from marotte's own pending render.
func TestHealKASConfig_AnEmptyStoreBlamesANullWrittenDuringAWaitChange(t *testing.T) {
	dir := t.TempDir()
	kas := filepath.Join(dir, "kas", "mcp.json")
	var wait atomic.Bool
	s, err := New(t.Context(), dir, nil, withKASConfigPath(kas),
		WithWaitForReady(func(context.Context) (bool, bool) { return wait.Load(), true }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	writeKASFile(t, kas, `{"mcpServers": null}`)
	logs := captureSlog(t)
	wait.Store(true)

	if healed, err := s.HealKASConfig(t.Context(), "poll"); err != nil || !healed {
		t.Fatalf("HealKASConfig = %v, %v; want true, nil", healed, err)
	}
	line := logs.String()
	for _, want := range []string{driftWarn, "level=WARN", "member=null"} {
		if !strings.Contains(line, want) {
			t.Errorf("heal log lacks %q:\n%s", want, line)
		}
	}
	if strings.Contains(line, "carried the new wait setting") {
		t.Errorf("a foreign null was reported as the pending wait render:\n%s", line)
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
	body := "# hand-written\n{\"mcpServers\": {}}"
	writeKASFile(t, kas, body)

	_, err := s.HealKASConfig(t.Context(), "poll")
	if !errors.Is(err, ErrKASUnreadable) {
		t.Errorf("HealKASConfig error = %v, want ErrKASUnreadable", err)
	}
	if got, _ := os.ReadFile(kas); string(got) != body {
		t.Errorf("unreadable file was rewritten to %s", got)
	}
}

// marotte owns identityProvider as it owns mcpServers, so another writer's version is put back and named.
func TestHealKASConfig_IdentityProviderDrift(t *testing.T) {
	const saved = `{"issuer":"https://idp.example.com","clientId":"marotte"}`
	cases := []struct {
		name, body, field string
		stored            bool
	}{
		{"changed", `{"issuer":"https://evil.example.com","clientId":"marotte"}`, "identity_provider=changed", true},
		{"missing", ``, "identity_provider=missing", true},
		{"unexpected", `{"issuer":"https://idp.example.com","clientId":"x"}`, "identity_provider=unexpected", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, kas := newHealStore(t)
			if tc.stored {
				if err := s.setIdentityProvider(t.Context(), &identityProvider{Issuer: "https://idp.example.com", ClientID: "marotte"}); err != nil {
					t.Fatal(err)
				}
			}
			servers, _ := json.Marshal(wantRendered(t, s))
			doc := `{"mcpServers": ` + string(servers)
			if tc.body != "" {
				doc += `, "identityProvider": ` + tc.body
			}
			writeKASFile(t, kas, doc+`}`)
			logs := captureSlog(t)

			healed, err := s.HealKASConfig(t.Context(), "poll")
			if err != nil || !healed {
				t.Fatalf("HealKASConfig = %v, %v; want true, nil", healed, err)
			}
			got, present := readKAS(t, kas)[kasIdentityProviderKey]
			switch {
			case tc.stored && !sameJSONValue(got, json.RawMessage(saved)):
				t.Errorf("identityProvider after heal = %s, want %s", got, saved)
			case !tc.stored && present:
				t.Errorf("identityProvider after heal = %s, want the member removed", got)
			}
			for _, want := range []string{driftWarn, tc.field} {
				if !strings.Contains(logs.String(), want) {
					t.Errorf("drift log lacks %q:\n%s", want, logs)
				}
			}
		})
	}
}

// A stored oauth_mode is part of the render, so a heal keeps it and puts it back when another writer drops it.
func TestHealKASConfig_KeepsOAuthMode(t *testing.T) {
	s, kas := newIsolatedStore(t)
	if _, err := s.Create(t.Context(), &Server{
		Transport: transportHTTP, Name: "corp", Enabled: true, URL: "https://mcp.corp.example/mcp", OAuthMode: oauthModeAuto,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if healed, err := s.HealKASConfig(t.Context(), "poll"); err != nil || healed {
		t.Fatalf("HealKASConfig over its own render = %v, %v; want false, nil", healed, err)
	}

	writeKASFile(t, kas, `{"mcpServers": {"corp": {"url": "https://mcp.corp.example/mcp"}}}`)
	if healed, err := s.HealKASConfig(t.Context(), "poll"); err != nil || !healed {
		t.Fatalf("HealKASConfig over a dropped mode = %v, %v; want true, nil", healed, err)
	}
	if oauth, _ := readKASServers(t, kas)["corp"]["oauth"].(map[string]any); oauth["mode"] != "auto" {
		t.Errorf("oauth after heal = %v, want mode=auto", oauth)
	}
}
