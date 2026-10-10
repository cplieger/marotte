package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The uploads directory is a SIBLING of the workspace. Symlink-resolved for canonTmp's reason.
func twoRoots(t *testing.T) (work, uploads, outside string) {
	t.Helper()
	work = canonTmp(t)
	uploads = canonTmp(t)
	outside = canonTmp(t)
	for _, dir := range []string{work, uploads, outside} {
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s/f.txt: %v", dir, err)
		}
	}
	return work, uploads, outside
}

// Either root resolves an ABSOLUTE path: an uploaded file lives beside the workspace.
func TestConfineAnyAbs_absolutePathResolvesInEitherRoot(t *testing.T) {
	work, uploads, outside := twoRoots(t)
	roots := []string{work, uploads}

	cases := []struct {
		name     string
		in       string
		wantRoot string
		wantRel  string
	}{
		{"in the workspace", filepath.Join(work, "f.txt"), work, "f.txt"},
		{"in the uploads dir", filepath.Join(uploads, "f.txt"), uploads, "f.txt"},
		{"the workspace itself", work, work, "."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, rel, err := ConfineAnyAbs(roots, tc.in)
			if err != nil {
				t.Fatalf("ConfineAnyAbs(roots, %q) err = %v, want nil", tc.in, err)
			}
			if root != tc.wantRoot || rel != tc.wantRel {
				t.Errorf("ConfineAnyAbs(roots, %q) = (%q, %q), want (%q, %q)", tc.in, root, rel, tc.wantRoot, tc.wantRel)
			}
		})
	}

	// A path in NEITHER root is refused with roots[0]'s error, naming the workspace.
	stray := filepath.Join(outside, "f.txt")
	_, _, err := ConfineAnyAbs(roots, stray)
	if err == nil {
		t.Fatalf("ConfineAnyAbs(roots, %q) err = nil, want an error", stray)
	}
	if !strings.Contains(err.Error(), "workspace") {
		t.Errorf("error = %q, want it to name the workspace", err)
	}
}

// A RELATIVE path resolves against roots[0] ONLY, so it names a single file.
func TestConfineAnyAbs_relativePathTakesTheFirstRootOnly(t *testing.T) {
	work, uploads, _ := twoRoots(t)

	root, rel, err := ConfineAnyAbs([]string{work, uploads}, "f.txt")
	if err != nil {
		t.Fatalf("ConfineAnyAbs(roots, %q) err = %v, want nil", "f.txt", err)
	}
	if root != work || rel != "f.txt" {
		t.Errorf("ConfineAnyAbs(roots, %q) = (%q, %q), want the workspace's copy (%q, %q)", "f.txt", root, rel, work, "f.txt")
	}

	// Swapping the order swaps the answer: resolution is positional.
	root, _, err = ConfineAnyAbs([]string{uploads, work}, "f.txt")
	if err != nil {
		t.Fatalf("ConfineAnyAbs(swapped, %q) err = %v, want nil", "f.txt", err)
	}
	if root != uploads {
		t.Errorf("ConfineAnyAbs(swapped, %q) root = %q, want %q", "f.txt", root, uploads)
	}
}

// A ".." escape or a leaving symlink is refused from EITHER root: a second root widens
// which directories may be named, never the confinement.
func TestConfineAnyAbs_refusesEscapes(t *testing.T) {
	work, uploads, outside := twoRoots(t)
	roots := []string{work, uploads}

	if err := os.Symlink(outside, filepath.Join(uploads, "out")); err != nil {
		t.Fatalf("symlink out of uploads: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(work, "out")); err != nil {
		t.Fatalf("symlink out of workspace: %v", err)
	}

	for _, tc := range []struct {
		name string
		in   string
	}{
		{"dot-dot out of the workspace", filepath.Join(work, "..", "elsewhere")},
		{"dot-dot out of the uploads dir", filepath.Join(uploads, "..", "elsewhere")},
		{"symlink out of the uploads dir", filepath.Join(uploads, "out", "f.txt")},
		{"symlink out of the workspace", filepath.Join(work, "out", "f.txt")},
		{"relative symlink out of the workspace", filepath.Join("out", "f.txt")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if root, rel, err := ConfineAnyAbs(roots, tc.in); err == nil {
				t.Errorf("ConfineAnyAbs(roots, %q) = (%q, %q, nil), want an error", tc.in, root, rel)
			}
		})
	}
}

// An empty second root is legal and INERT: pathinside.Root("") contains no path.
func TestConfineAnyAbs_emptySecondRootIsInert(t *testing.T) {
	work, _, outside := twoRoots(t)
	roots := []string{work, ""}

	inside := filepath.Join(work, "f.txt")
	if root, rel, err := ConfineAnyAbs(roots, inside); err != nil || root != work || rel != "f.txt" {
		t.Errorf("ConfineAnyAbs(roots, %q) = (%q, %q, %v), want (%q, %q, nil)", inside, root, rel, err, work, "f.txt")
	}
	stray := filepath.Join(outside, "f.txt")
	if root, rel, err := ConfineAnyAbs(roots, stray); err == nil {
		t.Errorf("ConfineAnyAbs(roots, %q) = (%q, %q, nil), want an error", stray, root, rel)
	}
}

// An empty path is refused, and NO roots is refused rather than confining to nothing.
func TestConfineAnyAbs_refusesDegenerateInputs(t *testing.T) {
	work := canonTmp(t)
	if root, rel, err := ConfineAnyAbs([]string{work}, ""); err == nil {
		t.Errorf("ConfineAnyAbs(roots, %q) = (%q, %q, nil), want an error", "", root, rel)
	}
	if root, rel, err := ConfineAnyAbs(nil, "f.txt"); err == nil {
		t.Errorf("ConfineAnyAbs(nil, %q) = (%q, %q, nil), want an error", "f.txt", root, rel)
	}
}
