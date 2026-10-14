package forges

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCredentialStore_WidenedDirectoryDegradesWithAReason(t *testing.T) {
	cfg := t.TempDir()
	dir := filepath.Join(cfg, credentialStoreDir)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}

	store, reason := openCredentialStore(cfg)
	if store != nil {
		t.Fatal("openCredentialStore over a group-readable directory returned a store")
	}
	for _, want := range []string{dir, "0700"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason %q does not name %q", reason, want)
		}
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("store directory gone after a refused open: %v", err)
	}
}

func TestOpenCredentialStore_CreatesThePrivateDirectory(t *testing.T) {
	cfg := t.TempDir()
	store, reason := openCredentialStore(cfg)
	if store == nil {
		t.Fatalf("openCredentialStore on a fresh config dir: %s", reason)
	}
	info, err := os.Stat(filepath.Join(cfg, credentialStoreDir))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Errorf("store directory mode = %#o, want 0700", mode)
	}
}
