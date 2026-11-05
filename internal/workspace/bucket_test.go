package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"
)

func TestSessionsBucket(t *testing.T) {
	// KAS computeWorkspaceHash(normalizePath(abs)) on Linux: hex(sha256(abs))[:16].
	dir := "/tmp/vk-ws"
	want := hex.EncodeToString(func() []byte { s := sha256.Sum256([]byte(dir)); return s[:] }())[:16]
	if got := SessionsBucket(dir); got != want {
		t.Errorf("SessionsBucket(%q) = %q, want %q", dir, got, want)
	}
	if len(SessionsBucket("/x")) != 16 {
		t.Errorf("bucket length != 16")
	}
	// Hashing the relative text itself would name a directory KAS never reads.
	t.Run("a relative work dir hashes as its absolute form", func(t *testing.T) {
		base := t.TempDir()
		t.Chdir(base)
		want := SessionsBucket(filepath.Join(base, "rel"))
		if got := SessionsBucket("rel"); got != want {
			t.Errorf("SessionsBucket(%q) = %q, want %q (the bucket of %q)",
				"rel", got, want, filepath.Join(base, "rel"))
		}
	})
}

// The bucket of the default workspace root, as kiro-cli names it on disk: hex(sha256("/workspace"))[:16]. Every
// spelling of the same root must land on it.
func TestSessionsBucketGolden(t *testing.T) {
	const golden = "c52ddf65534b7b46"
	for _, root := range []string{
		"/workspace", "/workspace/", "/workspace/.", "/workspace//", "/tmp/../workspace",
	} {
		if got := SessionsBucket(root); got != golden {
			t.Errorf("SessionsBucket(%q) = %q, want %q", root, got, golden)
		}
	}
}
