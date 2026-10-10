package filebrowse

import (
	"os"
	"path/filepath"
	"testing"
)

func saveExisting(t *testing.T, perm os.FileMode) os.FileMode {
	t.Helper()
	h, dir, prefix := testDir(t)
	path := filepath.Join(dir, "doc.txt")
	if err := os.WriteFile(path, []byte("old"), perm); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, perm); err != nil {
		t.Fatal(err)
	}
	rec := putReq(t, h, "/api/file?path="+prefix+"/doc.txt", `{"content":"new"}`)
	if rec.Code != 200 {
		t.Fatalf("PUT /api/file status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func TestWriteFile_KeepsAGroupReadableModeAcrossASave(t *testing.T) {
	if got := saveExisting(t, 0o640); got != 0o640 {
		t.Errorf("saving a 0640 file left mode %#o, want 0640", got)
	}
}

func TestWriteFile_EnforcesAnOwnerOnlyModeWithoutAChmod(t *testing.T) {
	var chmods int
	orig := chmodInRoot
	chmodInRoot = func(r *os.Root, name string, mode os.FileMode) error {
		chmods++
		return orig(r, name, mode)
	}
	t.Cleanup(func() { chmodInRoot = orig })

	if got := saveExisting(t, 0o600); got != 0o600 {
		t.Errorf("saving a 0600 file left mode %#o, want 0600", got)
	}
	if chmods != 0 {
		t.Errorf("saving a 0600 file ran %d post-write chmods, want 0: the mode must be enforced on the write itself", chmods)
	}
}
