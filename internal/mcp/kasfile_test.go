package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readKAS(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read kas config: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse kas config %s: %v", data, err)
	}
	return doc
}

func readKASServers(t *testing.T, path string) map[string]map[string]any {
	t.Helper()
	doc := readKAS(t, path)
	raw, ok := doc[kasServerKey]
	if !ok {
		t.Fatalf("kas config has no %q key: %v", kasServerKey, doc)
	}
	var out map[string]map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("parse mcpServers: %v", err)
	}
	return out
}

func newIsolatedStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	kas := filepath.Join(dir, "kas", "mcp.json")
	s, err := New(t.Context(), dir, nil, withKASConfigPath(kas))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, kas
}

// TestNew_DefaultKASPathIsUnderKiroHome pins that PRODUCTION gets the real path,
// which is the other half of the isolation option: every test overrides the path,
// so without this nothing would notice if the default silently changed, or went
// empty and wrote to the process's cwd.
//
// A default resolved eagerly in `New` would make the package's own tests write the
// developer's own ~/.kiro/settings/mcp.json.
func TestNew_DefaultKASPathIsUnderKiroHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := t.TempDir()
	s, err := New(t.Context(), dir, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	want := filepath.Join(home, ".kiro", "settings", "mcp.json")
	if s.kasPath != want {
		t.Errorf("kasPath = %q, want %q", s.kasPath, want)
	}
	// And it really wrote there, so the path is used and not merely stored.
	if _, err := os.Stat(want); err != nil {
		t.Errorf("no config at the default path: %v", err)
	}
}

// TestWriteKASConfig_StdioShape pins the stdio entry: command/args plus env as a
// RECORD (the store holds ordered KeyPairs; KAS's schema is a map).
func TestWriteKASConfig_StdioShape(t *testing.T) {
	s, kas := newIsolatedStore(t)
	srv := &Server{
		Transport: TransportStdio, Name: "gh", Enabled: true,
		Command: "npx", Args: []string{"-y", "gh-mcp"},
		Env: []keyPair{{Name: "TOKEN", Value: "t1"}, {Name: "MODE", Value: "ro"}},
	}
	if _, err := s.Create(t.Context(), srv); err != nil {
		t.Fatalf("Create: %v", err)
	}

	entry := readKASServers(t, kas)["gh"]
	if entry["command"] != "npx" {
		t.Errorf("command = %v, want npx", entry["command"])
	}
	args, ok := entry["args"].([]any)
	if !ok || len(args) != 2 || args[0] != "-y" || args[1] != "gh-mcp" {
		t.Errorf("args = %v, want [-y gh-mcp]", entry["args"])
	}
	env, ok := entry["env"].(map[string]any)
	if !ok {
		t.Fatalf("env = %T, want an object (KAS's schema is a record, not an array)", entry["env"])
	}
	if env["TOKEN"] != "t1" || env["MODE"] != "ro" {
		t.Errorf("env = %v, want TOKEN=t1 MODE=ro", env)
	}
	// url/headers belong to the other transport and must not appear.
	if _, ok := entry["url"]; ok {
		t.Errorf("stdio entry carries a url: %v", entry)
	}
}

// TestWriteKASConfig_RemoteCarriesOAuth is the one that matters most, because
// this field never reached the agent before. On the inline path KAS's
// acpServerToWire read only command/args/env or url/headers plus two _meta
// fields — it DROPPED oauth, so a pre-registered client id for a server that
// cannot do dynamic client registration (Slack, GitHub, Figma) was collected in
// the UI, validated, persisted, and then silently discarded.
func TestWriteKASConfig_RemoteCarriesOAuth(t *testing.T) {
	s, kas := newIsolatedStore(t)
	srv := &Server{
		Transport: TransportHTTP, Name: "slack", Enabled: true,
		URL: "https://slack.example/mcp", OAuthClientID: "cid-123",
	}
	if _, err := s.Create(t.Context(), srv); err != nil {
		t.Fatalf("Create: %v", err)
	}

	entry := readKASServers(t, kas)["slack"]
	oauth, ok := entry["oauth"].(map[string]any)
	if !ok {
		t.Fatalf("oauth = %T, want an object — the field the inline path dropped", entry["oauth"])
	}
	if len(oauth) != 1 || oauth["clientId"] != "cid-123" {
		t.Errorf("oauth = %v, want exactly clientId=cid-123", oauth)
	}
	if entry["url"] != "https://slack.example/mcp" {
		t.Errorf("url = %v", entry["url"])
	}
}

// A server with only the metadata URL and the redirect set has no clientId,
// so the render must not gate the oauth block on the client id.
func TestWriteKASConfig_RemoteCarriesClientMetadataAndRedirect(t *testing.T) {
	s, kas := newIsolatedStore(t)
	srv := &Server{
		Transport: TransportHTTP, Name: "cimd", Enabled: true,
		URL:                    "https://mcp.example/mcp",
		OAuthClientMetadataURL: "https://example.com/c.json",
		OAuthRedirectURI:       "localhost:7778",
	}
	if _, err := s.Create(t.Context(), srv); err != nil {
		t.Fatalf("Create: %v", err)
	}
	oauth, ok := readKASServers(t, kas)["cimd"]["oauth"].(map[string]any)
	if !ok {
		t.Fatalf("no oauth block rendered for a CIMD-only server")
	}
	want := map[string]any{"clientMetadataUrl": "https://example.com/c.json", "redirectUri": "localhost:7778"}
	if len(oauth) != len(want) || oauth["clientMetadataUrl"] != want["clientMetadataUrl"] || oauth["redirectUri"] != want["redirectUri"] {
		t.Errorf("oauth = %v, want %v", oauth, want)
	}
}

// The boot render rebuilds KAS's file from marotte's record, so a field the
// record does not carry is destroyed on every boot.
func TestNew_BootRenderKeepsClientMetadataURL(t *testing.T) {
	dir := t.TempDir()
	rec := `{"version":1,"servers":[{"id":"abcdefghij","name":"cimd","transport":"http","url":"https://mcp.example/mcp",` +
		`"oauth_client_metadata_url":"https://example.com/c.json","oauth_redirect_uri":":7778",` +
		`"enabled":true,"created_at":1,"updated_at":1}]}`
	if err := os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(rec), 0o600); err != nil {
		t.Fatalf("seed mcp.json: %v", err)
	}
	kas := filepath.Join(dir, "kas", "mcp.json")
	if _, err := New(t.Context(), dir, nil, withKASConfigPath(kas)); err != nil {
		t.Fatalf("New: %v", err)
	}
	oauth, _ := readKASServers(t, kas)["cimd"]["oauth"].(map[string]any)
	if oauth["clientMetadataUrl"] != "https://example.com/c.json" || oauth["redirectUri"] != ":7778" {
		t.Errorf("boot render oauth = %v, want both fields kept", oauth)
	}
}

// TestWriteKASConfig_NoTypeHint pins that no `type` is emitted. KAS accepts one
// and then ignores it: transport is inferred from which fields are present and
// HTTP-vs-SSE is negotiated at connect time. Writing it would imply the store's
// http/sse split still means something on the wire, and a reader would look for
// behaviour that is not there.
func TestWriteKASConfig_NoTypeHint(t *testing.T) {
	s, kas := newIsolatedStore(t)
	for _, tr := range []Transport{TransportHTTP, TransportSSE} {
		srv := &Server{
			Transport: tr, Name: "srv-" + string(tr), Enabled: true,
			URL: "https://x.example/mcp",
		}
		if _, err := s.Create(t.Context(), srv); err != nil {
			t.Fatalf("Create(%s): %v", tr, err)
		}
	}
	servers := readKASServers(t, kas)
	http, sse := servers["srv-http"], servers["srv-sse"]
	if _, ok := http["type"]; ok {
		t.Errorf("http entry carries a type hint: %v", http)
	}
	if _, ok := sse["type"]; ok {
		t.Errorf("sse entry carries a type hint: %v", sse)
	}
	// Same url, so the two render identically — the distinction is the store's.
	if http["url"] != sse["url"] {
		t.Errorf("http url %v != sse url %v", http["url"], sse["url"])
	}
}

// TestWriteKASConfig_DisabledServerStaysWithFlag pins that a disabled server is
// RETAINED with `disabled: true` rather than omitted. Omitting it would make
// "off" indistinguishable from "deleted" to KAS, and its status would go missing
// from the UI instead of reading "disabled".
func TestWriteKASConfig_DisabledServerStaysWithFlag(t *testing.T) {
	s, kas := newIsolatedStore(t)
	srv := &Server{Transport: TransportStdio, Name: "off-server", Enabled: true, Command: "npx"}
	created, err := s.Create(t.Context(), srv)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.setEnabled(t.Context(), created.ID, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}

	entry, ok := readKASServers(t, kas)["off-server"]
	if !ok {
		t.Fatal("a disabled server was dropped from the file; KAS cannot report it as disabled")
	}
	if entry["disabled"] != true {
		t.Errorf("disabled = %v, want true", entry["disabled"])
	}
}

// TestWriteKASConfig_PreservesForeignKeys pins the shared-file contract: KAS
// reads `powers.mcpServers` out of this same file, so a write must replace ONLY
// the key marotte owns.
func TestWriteKASConfig_PreservesForeignKeys(t *testing.T) {
	dir := t.TempDir()
	kas := filepath.Join(dir, "mcp.json")
	seed := `{"mcpServers":{"stale":{"command":"gone"}},"powers":{"mcpServers":{"kept":{"command":"keepme"}}}}`
	if err := os.WriteFile(kas, []byte(seed), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	s, err := New(t.Context(), dir, nil, withKASConfigPath(kas))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_ = s

	doc := readKAS(t, kas)
	powers, ok := doc["powers"]
	if !ok {
		t.Fatal("the powers block was clobbered; KAS reads it from this file too")
	}
	if !strings.Contains(string(powers), "keepme") {
		t.Errorf("powers = %s, want the seeded entry preserved", powers)
	}
	// And ours was replaced, not merged: the store is empty, so the stale entry
	// must be gone.
	if strings.Contains(string(doc[kasServerKey]), "stale") {
		t.Errorf("mcpServers = %s, want the stale entry replaced by the store's (empty) set", doc[kasServerKey])
	}
}

// TestWriteKASConfig_Mode0600 pins the file mode. It holds header values and
// OAuth client secrets.
func TestWriteKASConfig_Mode0600(t *testing.T) {
	_, kas := newIsolatedStore(t)
	info, err := os.Stat(kas)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
}

// TestRenderKASServers_SkipsUnknownTransport pins that a record with a transport
// this code does not understand is skipped rather than written. Such an entry
// would have neither a command nor a url, and KAS rejects that with a warning —
// so writing it produces log noise and no server.
func TestRenderKASServers_SkipsUnknownTransport(t *testing.T) {
	got := renderKASServers([]*Server{
		{Name: "weird", Transport: Transport("carrier-pigeon"), Enabled: true},
		{Name: "fine", Transport: TransportStdio, Command: "npx", Enabled: true},
		nil,
		{Name: "", Transport: TransportStdio, Command: "npx", Enabled: true},
	}, kasRenderPolicy{})
	if _, ok := got["weird"]; ok {
		t.Errorf("unknown transport was rendered: %v", got["weird"])
	}
	if _, ok := got["fine"]; !ok {
		t.Error("the valid server was dropped")
	}
	if len(got) != 1 {
		t.Errorf("rendered %d entries, want 1 (nil and empty-name entries skipped): %v", len(got), got)
	}
}

// TestPairsRecord_LastDuplicateWins states the flattening loss explicitly: the
// store keeps ordered KeyPairs so the editor can round-trip duplicates, and
// KAS's schema cannot represent them.
func TestPairsRecord_LastDuplicateWins(t *testing.T) {
	got := pairsRecord([]keyPair{{Name: "K", Value: "first"}, {Name: "K", Value: "second"}})
	if len(got) != 1 || got["K"] != "second" {
		t.Errorf("pairsRecord = %v, want {K: second}", got)
	}
	if pairsRecord(nil) != nil {
		t.Error("pairsRecord(nil) should stay nil so the field is omitted")
	}
}

// The negative half of TestWriteKASConfig_RemoteCarriesOAuth: a remote server the
// user configured without credentials must carry no `oauth` block at all. An
// empty one is worse than none — KAS would read a client id and secret that are
// both the empty string and attempt the flow with them, which fails at the
// provider with nothing in marotte's own UI to explain why.
func TestWriteKASConfig_RemoteWithoutOAuthCarriesNoBlock(t *testing.T) {
	s, kas := newIsolatedStore(t)
	srv := &Server{
		Transport: TransportHTTP, Name: "plain", Enabled: true,
		URL: "https://plain.example/mcp",
	}
	if _, err := s.Create(t.Context(), srv); err != nil {
		t.Fatalf("Create: %v", err)
	}

	entry, ok := readKASServers(t, kas)["plain"]
	if !ok {
		t.Fatal("the server was not rendered")
	}
	if got, present := entry["oauth"]; present {
		t.Errorf("oauth = %v, want the key absent for a server with no credentials", got)
	}
}

// The size cap bounds the RE-READ of a file marotte shares with KAS, so its two
// sides have different consequences: a file at the cap is still merged with (its
// foreign keys survive a write), and one past it is left untouched, logged as
// oversized rather than as a stat failure, which would send the reader looking
// for a broken path.
//
// Not parallel: it swaps the process-wide slog default.
func TestReadKASConfig_SizeCapIsInclusive(t *testing.T) {
	// Trailing whitespace is legal JSON, so the padding keeps the document
	// parseable at any size.
	seed := func(t *testing.T, size int) string {
		t.Helper()
		dir := t.TempDir()
		kas := filepath.Join(dir, "mcp.json")
		doc := `{"powers":{"mcpServers":{"kept":{"command":"keepme"}}}}`
		if len(doc) > size {
			t.Fatalf("seed document is %d bytes, over the %d target", len(doc), size)
		}
		data := append([]byte(doc), bytes.Repeat([]byte(" "), size-len(doc))...)
		if err := os.WriteFile(kas, data, 0o600); err != nil {
			t.Fatalf("seed: %v", err)
		}
		return kas
	}

	t.Run("at_the_cap", func(t *testing.T) {
		kas := seed(t, kasFileMaxBytes)
		if _, err := New(t.Context(), filepath.Dir(kas), nil, withKASConfigPath(kas)); err != nil {
			t.Fatalf("New: %v", err)
		}
		doc := readKAS(t, kas)
		powers, ok := doc[kasPowersKey]
		if !ok {
			t.Fatalf("a file of exactly %d bytes lost its %q block; it was skipped rather than read",
				kasFileMaxBytes, kasPowersKey)
		}
		if !strings.Contains(string(powers), "keepme") {
			t.Errorf("%s = %s, want the seeded entry preserved", kasPowersKey, powers)
		}
	})

	t.Run("one_past_the_cap", func(t *testing.T) {
		logs := captureSlog(t)
		kas := seed(t, kasFileMaxBytes+1)
		if _, err := New(t.Context(), filepath.Dir(kas), nil, withKASConfigPath(kas)); err != nil {
			t.Fatalf("New: %v", err)
		}
		if info, err := os.Stat(kas); err != nil || info.Size() != kasFileMaxBytes+1 {
			t.Errorf("a file past the %d-byte cap was rewritten: stat = %v, %v", kasFileMaxBytes, info, err)
		}
		if !strings.Contains(logs.String(), "merge cap") {
			t.Errorf("an oversized file's refusal does not name the cap:\n%s", logs.String())
		}
	})
}

func TestReadKASDoc_BOMKeepsForeignKeys(t *testing.T) {
	s, kas := newIsolatedStore(t)
	seed := `{"powers":{"mcpServers":{"power-a-b":{"command":"c"}}},"other":1}`
	if err := os.WriteFile(kas, append([]byte{0xEF, 0xBB, 0xBF}, seed...), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := s.Create(t.Context(), &Server{Name: "mine", Transport: TransportStdio, Command: "x", Enabled: true}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	doc := readKAS(t, kas)
	if !strings.Contains(string(doc[kasPowersKey]), "power-a-b") {
		t.Errorf("powers = %s, want the BOM-prefixed file's block kept", doc[kasPowersKey])
	}
	if string(doc["other"]) != "1" {
		t.Errorf("other = %s, want the BOM-prefixed file's foreign key kept", doc["other"])
	}
}

// Not parallel: it swaps the process-wide slog default.
func TestReadKASDoc_EmptyFileIsReplacedQuietly(t *testing.T) {
	for _, tc := range []struct{ name, content string }{
		{name: "zero bytes", content: ""},
		{name: "whitespace", content: " \n\t"},
		{name: "bom only", content: "\xEF\xBB\xBF"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, kas := newIsolatedStore(t)
			if _, err := s.Create(t.Context(), &Server{Name: "mine", Transport: TransportStdio, Command: "x", Enabled: true}); err != nil {
				t.Fatalf("Create: %v", err)
			}
			logs := captureSlog(t)

			if err := os.WriteFile(kas, []byte(tc.content), 0o600); err != nil {
				t.Fatalf("seed: %v", err)
			}
			if changed, err := s.WritePowersServers(t.Context(), nil); err != nil || !changed {
				t.Fatalf("WritePowersServers over an empty file = %v, %v; want true, nil", changed, err)
			}
			if _, ok := readKASServers(t, kas)["mine"]; !ok {
				t.Errorf("WritePowersServers over an empty file left out marotte's own servers")
			}

			if err := os.WriteFile(kas, []byte(tc.content), 0o600); err != nil {
				t.Fatalf("seed: %v", err)
			}
			if _, err := s.Create(t.Context(), &Server{Name: "second", Transport: TransportStdio, Command: "y", Enabled: true}); err != nil {
				t.Fatalf("Create over an empty file: %v", err)
			}
			if _, ok := readKASServers(t, kas)["second"]; !ok {
				t.Errorf("a persist over an empty file did not render the new server")
			}
			if strings.Contains(logs.String(), "level=WARN") || strings.Contains(logs.String(), "level=ERROR") {
				t.Errorf("an empty KAS file was reported:\n%s", logs.String())
			}
		})
	}
}

// A file marotte cannot parse may still be valid to KAS (JSONC), so every writer leaves it
// byte for byte and refuses, rather than replacing it with marotte's key alone. Not parallel:
// it swaps the process-wide slog default.
func TestReadKASDoc_UnreadableFileIsKeptAndTheWriteRefused(t *testing.T) {
	for _, tc := range []struct{ name, content string }{
		{name: "garbage", content: "{garbage"},
		{name: "jsonc comment", content: "{\n  // mine\n  \"powers\": {\"mcpServers\": {\"power-a-b\": {\"command\": \"c\"}}}\n}"},
		{name: "array document", content: `[1]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			kas := filepath.Join(dir, "kas", "mcp.json")
			s, err := New(t.Context(), dir, nil, withKASConfigPath(kas))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			record, err := os.ReadFile(filepath.Join(dir, "mcp.json"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatalf("read record: %v", err)
			}
			if err := os.WriteFile(kas, []byte(tc.content), 0o600); err != nil {
				t.Fatalf("seed: %v", err)
			}
			logs := captureSlog(t)
			assertKept := func(t *testing.T, writer string) {
				t.Helper()
				got, err := os.ReadFile(kas)
				if err != nil || string(got) != tc.content {
					t.Errorf("%s rewrote the unreadable file: %q, %v", writer, got, err)
				}
			}

			if _, err := s.Create(t.Context(), &Server{Name: "mine", Transport: TransportStdio, Command: "x", Enabled: true}); !errors.Is(err, ErrKASUnreadable) {
				t.Errorf("Create = %v, want ErrKASUnreadable", err)
			}
			assertKept(t, "persist")
			if now, err := os.ReadFile(filepath.Join(dir, "mcp.json")); string(now) != string(record) {
				t.Errorf("marotte's record moved under a refused KAS write: %q, %v", now, err)
			}
			if n := len(s.list(t.Context())); n != 0 {
				t.Errorf("store holds %d servers after a refused Create, want 0", n)
			}
			if _, err := s.WritePowersServers(t.Context(), map[string]json.RawMessage{"power-x-y": json.RawMessage(`{"command":"c"}`)}); !errors.Is(err, ErrKASUnreadable) {
				t.Errorf("WritePowersServers = %v, want ErrKASUnreadable", err)
			}
			assertKept(t, "WritePowersServers")
			if err := s.RenderKASConfig(t.Context()); !errors.Is(err, ErrKASUnreadable) {
				t.Errorf("RenderKASConfig = %v, want ErrKASUnreadable", err)
			}
			assertKept(t, "RenderKASConfig")
			if !strings.Contains(logs.String(), "level=ERROR") {
				t.Errorf("a refused write logged no error:\n%s", logs.String())
			}
		})
	}
}

// The boot reconcile over an unreadable KAS file keeps the file and still starts the store, so the
// user can reach the UI to fix it.
func TestNew_BootOverAnUnreadableKASFileKeepsItAndStarts(t *testing.T) {
	dir := t.TempDir()
	kas := filepath.Join(dir, "kas-mcp.json")
	const content = "{\n  // mine\n  \"keep\": true\n}"
	if err := os.WriteFile(kas, []byte(content), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	record := `{"version":1,"servers":[{"id":"abcdefghij","name":"local","transport":"stdio","command":"x","enabled":true}]}`
	if err := os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(record), 0o600); err != nil {
		t.Fatalf("seed record: %v", err)
	}
	logs := captureSlog(t)

	s, err := New(t.Context(), dir, nil, withKASConfigPath(kas))
	if err != nil {
		t.Fatalf("New over an unreadable KAS file = %v, want the store to start", err)
	}
	if got, err := os.ReadFile(kas); err != nil || string(got) != content {
		t.Errorf("boot reconcile rewrote the unreadable file: %q, %v", got, err)
	}
	if n := len(s.list(t.Context())); n != 1 {
		t.Errorf("store holds %d servers after boot, want the record's 1", n)
	}
	if !strings.Contains(logs.String(), "level=ERROR msg=\"mcp: kas config file is not a readable JSON object") {
		t.Errorf("boot refusal logged no error:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), "initial kas config write failed") {
		t.Errorf("boot logged the refusal a second time:\n%s", logs.String())
	}
}

// A refusal of one file state is reported at Error once; a changed file is reported again.
func TestReadKASDoc_RefusalOfOneFileStateIsLoggedOnce(t *testing.T) {
	dir := t.TempDir()
	kas := filepath.Join(dir, "kas-mcp.json")
	if err := os.WriteFile(kas, []byte("{garbage"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	logs := captureSlog(t)
	s, err := New(t.Context(), dir, nil, withKASConfigPath(kas))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	refusals := func() int {
		return strings.Count(logs.String(), "level=ERROR msg=\"mcp: kas config file is not a readable JSON object")
	}

	for range 2 {
		if err := s.RenderKASConfig(t.Context()); !errors.Is(err, ErrKASUnreadable) {
			t.Fatalf("RenderKASConfig = %v, want ErrKASUnreadable", err)
		}
	}
	if n := refusals(); n != 1 {
		t.Errorf("three refusals of one unchanged file logged %d errors, want 1", n)
	}
	if err := os.WriteFile(kas, []byte("{garbage, longer"), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := s.RenderKASConfig(t.Context()); !errors.Is(err, ErrKASUnreadable) {
		t.Fatalf("RenderKASConfig = %v, want ErrKASUnreadable", err)
	}
	if n := refusals(); n != 2 {
		t.Errorf("a changed unreadable file logged %d errors in all, want 2", n)
	}

	// A file fixed and then broken back to the same state, mtime included, is a new refusal.
	info, err := os.Stat(kas)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if err := os.WriteFile(kas, []byte("{}"), 0o600); err != nil {
		t.Fatalf("fix: %v", err)
	}
	if err := s.RenderKASConfig(t.Context()); err != nil {
		t.Fatalf("RenderKASConfig over a fixed file = %v, want nil", err)
	}
	if err := os.WriteFile(kas, []byte("{garbage, longer"), 0o600); err != nil {
		t.Fatalf("break again: %v", err)
	}
	if err := os.Chtimes(kas, info.ModTime(), info.ModTime()); err != nil {
		t.Fatalf("restore mtime: %v", err)
	}
	if err := s.RenderKASConfig(t.Context()); !errors.Is(err, ErrKASUnreadable) {
		t.Fatalf("RenderKASConfig = %v, want ErrKASUnreadable", err)
	}
	if n := refusals(); n != 3 {
		t.Errorf("a file broken again after a fix logged %d errors in all, want 3", n)
	}
}

func TestReadKASDoc_RefusedSaveAnswers409WithTheHint(t *testing.T) {
	dir := t.TempDir()
	kas := filepath.Join(dir, "kas-mcp.json")
	s, err := New(t.Context(), dir, nil, withKASConfigPath(kas))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	if err := os.WriteFile(kas, []byte("{garbage"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rec := doJSON(t, mux, http.MethodPost, "/api/mcp", &Server{Transport: TransportStdio, Name: "gh", Command: "bash", Enabled: true})
	if rec.Code != http.StatusConflict {
		t.Errorf("POST over an unreadable KAS file = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "left the file unchanged") {
		t.Errorf("body = %s, want the hint", rec.Body.String())
	}
}
