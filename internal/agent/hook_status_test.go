package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cplieger/atomicfile/v4"
)

// A size mismatch is a miss even when the mtime matches.
func TestHookStatusCache_SizeTermInvalidates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cli.json")
	if err := os.WriteFile(path, []byte(`{"k":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	c := newCachedBoolField(path, "k", false)
	// A matching identity with a different size isolates the size term, which catches a same-tick in-place rewrite.
	c.value = true
	c.id = atomicfile.Identify(info)
	c.size = info.Size() + 1

	if got := c.get(); got != false {
		t.Errorf("cachedBoolField.get() = %v, want false (size mismatch must invalidate the cache)", got)
	}
}

// TestHookStatusCache_EqualLengthRenamePublishInvalidates pins the os.SameFile leg: kiro-cli
// publishes cli.json by rename (2.19.0), so an equal-length generation on the same mtime differs only by inode.
func TestHookStatusCache_EqualLengthRenamePublishInvalidates(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	path := filepath.Join(dir, "cli.json")
	fixed := time.Unix(1700000000, 0)

	const v1 = `{"k":true,"x":11}`
	const v2 = `{"k":false,"x":1}` // same length, so only the inode differs

	publish := func(content string) {
		t.Helper()
		if _, err := atomicfile.WriteFile(ctx, path, []byte(content), atomicfile.WithMode(0o600)); err != nil {
			t.Fatalf("publish %s: %v", content, err)
		}
		if err := os.Chtimes(path, fixed, fixed); err != nil {
			t.Fatalf("chtimes %s: %v", content, err)
		}
	}

	publish(v1)
	c := newCachedBoolField(path, "k", false)
	if got := c.get(); !got {
		t.Fatalf("cachedBoolField.get() with %s = false, want true", v1)
	}
	info1, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat v1: %v", err)
	}

	publish(v2)
	info2, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat v2: %v", err)
	}
	// Guards against a vacuous pass.
	if os.SameFile(info1, info2) {
		t.Fatalf("setup: the second publish reused the inode; this case needs a rename publish")
	}
	if info1.Size() != info2.Size() || !info1.ModTime().Equal(info2.ModTime()) {
		t.Fatalf("setup: generations differ in size (%d vs %d) or mtime (%v vs %v); only the inode may differ",
			info1.Size(), info2.Size(), info1.ModTime(), info2.ModTime())
	}

	if got := c.get(); got {
		t.Errorf("cachedBoolField.get() after an equal-length rename publish of %s = true, want false", v2)
	}
}
