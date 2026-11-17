package filebrowse

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/go/filesafe"
)

// linkedFile stages dir/p/link pointing at dir/other/target.txt, a file in another folder.
func linkedFile(t *testing.T, dir string) (link, target string) {
	t.Helper()
	for _, sub := range []string{"p", "other"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	target = filepath.Join(dir, "other", "target.txt")
	writeTestFile(t, target, "kept")
	link = filepath.Join(dir, "p", "link")
	if err := os.Symlink("../other/target.txt", link); err != nil {
		t.Fatal(err)
	}
	return link, target
}

func isLinkTo(t *testing.T, path, want string) bool {
	t.Helper()
	got, err := os.Readlink(path)
	return err == nil && got == want
}

func TestAction_Rename_RenamesALinkNotItsTarget(t *testing.T) {
	h, dir, _ := testDir(t)
	link, target := linkedFile(t, dir)

	if code, msg := postAction(t, h, "rename", link, "renamed"); code != http.StatusOK {
		t.Fatalf("rename link = %d %q, want 200", code, msg)
	}
	if !isLinkTo(t, filepath.Join(dir, "p", "renamed"), "../other/target.txt") {
		t.Error("p/renamed is not the moved link")
	}
	if _, err := os.Lstat(link); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("p/link still there: %v", err)
	}
	if got := readTestFile(t, target); got != "kept" {
		t.Errorf("target = %q, want it untouched", got)
	}
	if _, err := os.Lstat(filepath.Join(dir, "other", "renamed")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the target's folder gained a renamed entry: %v", err)
	}
}

func TestAction_Rename_ALinkOntoATakenSiblingIsRefused(t *testing.T) {
	h, dir, _ := testDir(t)
	link, target := linkedFile(t, dir)
	taken := filepath.Join(dir, "p", "taken.txt")
	writeTestFile(t, taken, "sibling")

	if code, msg := postAction(t, h, "rename", link, "taken.txt"); code != http.StatusConflict || msg != "taken.txt already exists" {
		t.Errorf("rename link onto taken.txt = %d %q, want 409 %q", code, msg, "taken.txt already exists")
	}
	if got := readTestFile(t, taken); got != "sibling" {
		t.Errorf("taken.txt = %q, want it kept", got)
	}
	if !isLinkTo(t, link, "../other/target.txt") || readTestFile(t, target) != "kept" {
		t.Error("the link or its target changed after the refusal")
	}
}

// A hard link differing only by case is what a case-insensitive mount shows for a case-only
// rename: the destination's Lstat finds the source itself.
func TestAction_Rename_ACaseOnlyRenameOfTheSameFileIsNotRefused(t *testing.T) {
	h, dir, _ := testDir(t)
	src := filepath.Join(dir, "notes.md")
	writeTestFile(t, src, "x")
	if err := os.Link(src, filepath.Join(dir, "Notes.md")); err != nil {
		t.Fatal(err)
	}
	if code, msg := postAction(t, h, "rename", src, "Notes.md"); code != http.StatusOK {
		t.Errorf("case-only rename = %d %q, want 200", code, msg)
	}
}

func TestAction_Rename_AHardLinkUnderAnotherNameIsStillTaken(t *testing.T) {
	h, dir, _ := testDir(t)
	src := filepath.Join(dir, "a.txt")
	writeTestFile(t, src, "x")
	if err := os.Link(src, filepath.Join(dir, "b.txt")); err != nil {
		t.Fatal(err)
	}
	if code, _ := postAction(t, h, "rename", src, "b.txt"); code != http.StatusConflict {
		t.Errorf("rename onto a hard link named b.txt = %d, want 409", code)
	}
}

func TestDelete_RemovesALinkNotItsTarget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		delete func(t *testing.T, h *Handler, link string) error
	}{
		{name: "file_action", delete: func(t *testing.T, h *Handler, link string) error {
			if code, msg := postAction(t, h, "delete", link, ""); code != http.StatusOK {
				return errors.New(msg)
			}
			return nil
		}},
		{name: "Delete", delete: func(_ *testing.T, h *Handler, link string) error { return h.Delete(link) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, dir, _ := testDir(t)
			link, target := linkedFile(t, dir)
			if err := tc.delete(t, h, link); err != nil {
				t.Fatalf("delete link: %v", err)
			}
			if _, err := os.Lstat(link); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("link still there: %v", err)
			}
			if got := readTestFile(t, target); got != "kept" {
				t.Errorf("target = %q, want it untouched", got)
			}
		})
	}
}

func TestDeleteFile_RefusesALinkAndLeavesItsTarget(t *testing.T) {
	h, dir, _ := testDir(t)
	link, target := linkedFile(t, dir)
	if err := h.DeleteFile(link); !errors.Is(err, filesafe.ErrNotRegular) {
		t.Errorf("DeleteFile(link) = %v, want ErrNotRegular", err)
	}
	if !isLinkTo(t, link, "../other/target.txt") {
		t.Error("the link is gone")
	}
	if got := readTestFile(t, target); got != "kept" {
		t.Errorf("target = %q, want it untouched", got)
	}
}

func TestDelete_ALinkToAFolderLeavesTheFolder(t *testing.T) {
	h, dir, _ := testDir(t)
	if err := os.MkdirAll(filepath.Join(dir, "real", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dir, "real", "sub", "f.txt"), "kept")
	link := filepath.Join(dir, "alias")
	if err := os.Symlink("real", link); err != nil {
		t.Fatal(err)
	}
	if err := h.Delete(link); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("alias still there: %v", err)
	}
	if got := readTestFile(t, filepath.Join(dir, "real", "sub", "f.txt")); got != "kept" {
		t.Errorf("real/sub/f.txt = %q, want it kept", got)
	}
}
