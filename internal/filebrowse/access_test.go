package filebrowse

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// with answers s plus extra entries ahead of its own, for tests that block a
// path inside a temp-dir mount.
func (s Sensitive) with(extra ...sensitivePath) Sensitive {
	return Sensitive{list: append(slices.Clone(extra), s.entries()...)}
}

func TestSensitive_DefaultRootKeepsEveryExistingEntry(t *testing.T) {
	want := []sensitivePath{
		{Path: "/config/kiro/", IsDir: true},
		{Path: "/config/home/", IsDir: true},
		{Path: "/config/chats/", IsDir: true},
		{Path: "/config/push-subs.json"},
		{Path: "/config/vapid-keys.json"},
		{Path: "/config/mcp.json", Family: true},
		{Path: "/config/mcp-secrets.json", Family: true},
		{Path: "/config/forge-store/", IsDir: true},
		{Path: "/config/forge-connections.json", Family: true},
	}
	for name, s := range map[string]Sensitive{"zero value": {}, "NewSensitive(\"\")": NewSensitive("")} {
		t.Run(name, func(t *testing.T) {
			if got := s.entries(); !slices.Equal(got, want) {
				t.Errorf("entries() = %v, want %v", got, want)
			}
		})
	}
	for _, p := range []string{"/config/forge-store/credentials.json", "/config/forge-connections.json"} {
		if !(Sensitive{}).Blocks(p) {
			t.Errorf("Blocks(%q) = false, want true", p)
		}
	}
	if !(Sensitive{}).protectedDir("/config/forge-store") {
		t.Error(`protectedDir("/config/forge-store") = false, want true`)
	}
}

// A symlinked config root is blocked under both spellings: the docs scanner
// compares resolved paths, the file browser compares both forms.
func TestSensitive_SymlinkedRootBlocksBothSpellings(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "cfg")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	sens := NewSensitive(link)
	for _, p := range []string{filepath.Join(link, "forge-connections.json"), filepath.Join(real, "forge-connections.json")} {
		if !sens.Blocks(p) {
			t.Errorf("Blocks(%q) = false, want true", p)
		}
	}
}

// A store that cannot parse its file renames it aside beside itself, and the
// copy holds the same secrets as the original.
func TestSensitive_BlocksTheCopiesAStoreMovesAside(t *testing.T) {
	root := t.TempDir()
	sens := NewSensitive(root)
	for _, name := range []string{
		"forge-connections.json.corrupt.20260101-000000.12",
		"mcp.json.corrupt.20260101-000000.12",
		"mcp-secrets.json.corrupt",
	} {
		if p := filepath.Join(root, name); !sens.Blocks(p) {
			t.Errorf("Blocks(%q) = false, want true", p)
		}
	}
	if p := filepath.Join(root, "mcp.jsonl"); sens.Blocks(p) {
		t.Errorf("Blocks(%q) = true, want false: only a dotted extension of the name is its copy", p)
	}
}

// A config root other than /config (KIRO_CONFIG_DIR) moves every entry with it,
// and the two forge files are refused through every route that reaches them.
func TestSensitive_NonDefaultConfigRootBlocksTheStoreAndTheRecord(t *testing.T) {
	root := t.TempDir()
	store := filepath.Join(root, "forge-store")
	if err := os.Mkdir(store, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(store, "credentials.json"), filepath.Join(root, "forge-connections.json")} {
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sens := NewSensitive(root)
	h, err := New(sens, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	prefix := strings.TrimPrefix(root, "/")

	for _, rel := range []string{"forge-store/credentials.json", "forge-connections.json"} {
		if rec := getReq(t, h, "/api/file?path="+prefix+"/"+rel); rec.Code != http.StatusForbidden {
			t.Errorf("GET %s: status = %d, want 403; body=%s", rel, rec.Code, rec.Body.String())
		}
	}
	if rec := postReq(t, h, "/api/files/action",
		`{"action":"delete","path":"`+prefix+`/forge-store"}`); rec.Code != http.StatusForbidden {
		t.Errorf("delete forge-store: status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	if rec := postReq(t, h, "/api/files/action",
		`{"action":"rename","path":"`+prefix+`/forge-store","name":"elsewhere"}`); rec.Code != http.StatusForbidden {
		t.Errorf("rename forge-store: status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(store, "credentials.json")); err != nil {
		t.Errorf("credential file gone after refused operations: %v", err)
	}
	if !sens.Blocks(filepath.Join(root, "home", ".gitconfig")) {
		t.Error("home/ is not rooted at the configured directory")
	}
	if sens.Blocks("/config/forge-connections.json") {
		t.Error("a non-default root still blocks the /config spelling; the list did not move")
	}
}
