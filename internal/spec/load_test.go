package spec

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

const testMaxBytes = 1 << 16

func specFixture(t *testing.T, name string) (Root, string) {
	t.Helper()
	work := t.TempDir()
	dir := filepath.Join(work, ".kiro", "specs", name)
	mkdirAll(t, dir)
	return Root{Dir: filepath.Join(work, ".kiro"), Rel: ".kiro"}, dir
}

func fileNames(docs []marotte.SpecDoc) []string {
	out := make([]string, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.File)
	}
	return out
}

func TestLoad_OrdersRolesAndParsesTasks(t *testing.T) {
	root, dir := specFixture(t, "feat")
	writeFile(t, filepath.Join(dir, "tasks.md"), "- [ ] 1 one\n- [x] 2 two\n")
	writeFile(t, filepath.Join(dir, "analysis.md"), "# A\n")
	writeFile(t, filepath.Join(dir, "design.md"), "# D\n")
	writeFile(t, filepath.Join(dir, "bugfix.md"), "# B\n")
	writeFile(t, filepath.Join(dir, "requirements.md"), "# R\n")
	writeFile(t, filepath.Join(dir, "tasks.meta.json"), "{}")
	writeFile(t, filepath.Join(dir, "notes.txt"), "not a doc")
	mkdirAll(t, filepath.Join(dir, "drafts.md"))
	stamp := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(dir, "design.md"), stamp, stamp); err != nil {
		t.Fatalf("Setup: chtimes: %v", err)
	}

	spec, err := Load(t.Context(), root, "feat", testMaxBytes)
	if err != nil {
		t.Fatalf("Load(feat) error: %v", err)
	}
	if spec.Dir != ".kiro/specs/feat" || spec.Name != "feat" {
		t.Errorf("Load(feat) = dir %q name %q, want .kiro/specs/feat and feat", spec.Dir, spec.Name)
	}
	want := []string{"bugfix.md", "requirements.md", "design.md", "tasks.md", "analysis.md"}
	if got := fileNames(spec.Docs); !slices.Equal(got, want) {
		t.Fatalf("Load(feat) files = %v, want %v", got, want)
	}
	roles := []marotte.SpecDocRole{marotte.SpecDocRoleRequirements, marotte.SpecDocRoleRequirements, marotte.SpecDocRoleDesign, marotte.SpecDocRoleTasks, marotte.SpecDocRoleOther}
	for i, d := range spec.Docs {
		if d.Role != roles[i] {
			t.Errorf("Load(feat).Docs[%d] %s role = %q, want %q", i, d.File, d.Role, roles[i])
		}
	}
	tasks := spec.Docs[3]
	if tasks.Content != "- [ ] 1 one\n- [x] 2 two\n" {
		t.Errorf("Load(feat) tasks content = %q", tasks.Content)
	}
	sum := sha256.Sum256([]byte(tasks.Content))
	if tasks.Hash != hex.EncodeToString(sum[:]) {
		t.Errorf("Load(feat) tasks hash = %q, want the content's sha256", tasks.Hash)
	}
	if len(tasks.Tasks) != 2 || tasks.Progress == nil || tasks.Progress.Total != 2 || tasks.Progress.Completed != 1 {
		t.Errorf("Load(feat) tasks parsed = %d nodes, progress %+v, want 2 and {1 pending 1 completed of 2}", len(tasks.Tasks), tasks.Progress)
	}
	if spec.Docs[2].Tasks != nil || spec.Docs[2].Progress != nil {
		t.Errorf("Load(feat) design carries task fields %+v", spec.Docs[2])
	}
	if !spec.UpdatedAt.Equal(stamp) {
		t.Errorf("Load(feat).UpdatedAt = %v, want the newest mtime %v", spec.UpdatedAt, stamp)
	}
}

func TestLoad_OversizeDocIsListedWithoutContent(t *testing.T) {
	root, dir := specFixture(t, "big")
	writeFile(t, filepath.Join(dir, "design.md"), "0123456789")
	writeFile(t, filepath.Join(dir, "tasks.md"), "- [ ] 1 over the cap\n")
	spec, err := Load(t.Context(), root, "big", 12)
	if err != nil {
		t.Fatalf("Load(big) error: %v", err)
	}
	if got := fileNames(spec.Docs); !slices.Equal(got, []string{"design.md", "tasks.md"}) {
		t.Fatalf("Load(big) files = %v", got)
	}
	design := spec.Docs[0]
	if design.TooLarge || design.Content != "0123456789" {
		t.Errorf("Load(big) design (10 bytes under a 12 cap) = too_large %v content %q", design.TooLarge, design.Content)
	}
	tasks := spec.Docs[1]
	if !tasks.TooLarge || tasks.Content != "" || tasks.Hash != "" || tasks.Tasks != nil {
		t.Errorf("Load(big) tasks over the cap = %+v, want too_large with no content, hash or tree", tasks)
	}
}

func TestLoad_RefusesNonRegularEntries(t *testing.T) {
	root, dir := specFixture(t, "odd")
	writeFile(t, filepath.Join(dir, "design.md"), "# D\n")
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo.md"), 0o600); err != nil {
		t.Fatalf("Setup: mkfifo: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.md")
	writeFile(t, outside, "secret")
	if err := os.Symlink(outside, filepath.Join(dir, "escape.md")); err != nil {
		t.Fatalf("Setup: symlink: %v", err)
	}
	if err := os.Symlink("design.md", filepath.Join(dir, "alias.md")); err != nil {
		t.Fatalf("Setup: symlink: %v", err)
	}
	if err := os.Symlink("missing.md", filepath.Join(dir, "dangling.md")); err != nil {
		t.Fatalf("Setup: symlink: %v", err)
	}
	spec, err := Load(t.Context(), root, "odd", testMaxBytes)
	if err != nil {
		t.Fatalf("Load(odd) error: %v", err)
	}
	// An in-root symlink to a document is followed; the FIFO, the escape and
	// the dangling link are not documents.
	if got := fileNames(spec.Docs); !slices.Equal(got, []string{"design.md", "alias.md"}) {
		t.Errorf("Load(odd) files = %v, want [design.md alias.md]", got)
	}
	for _, d := range spec.Docs {
		if d.Content == "secret" {
			t.Errorf("Load(odd) served the file outside the root through %s", d.File)
		}
	}
}

func TestLoad_NoDocsAndMissingDir(t *testing.T) {
	root, dir := specFixture(t, "empty")
	writeFile(t, filepath.Join(dir, "tasks.meta.json"), "{}")
	if _, err := Load(t.Context(), root, "empty", testMaxBytes); !errors.Is(err, ErrNoDocs) {
		t.Errorf("Load(empty) error = %v, want ErrNoDocs", err)
	}
	if _, err := Load(t.Context(), root, "absent", testMaxBytes); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Load(absent) error = %v, want fs.ErrNotExist", err)
	}
	for _, bad := range []string{"", ".", "..", "a/b", "a\x00"} {
		if _, err := Load(t.Context(), root, bad, testMaxBytes); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Load(%q) error = %v, want fs.ErrNotExist", bad, err)
		}
	}
}

func TestLoad_RepoRootRel(t *testing.T) {
	work := t.TempDir()
	dir := filepath.Join(work, "repo", ".kiro", "specs", "x")
	mkdirAll(t, dir)
	writeFile(t, filepath.Join(dir, "requirements.md"), "# R\n")
	root := Root{Dir: filepath.Join(work, "repo", ".kiro"), Rel: "repo/.kiro"}
	spec, err := Load(t.Context(), root, "x", testMaxBytes)
	if err != nil {
		t.Fatalf("Load(x) error: %v", err)
	}
	if spec.Dir != "repo/.kiro/specs/x" {
		t.Errorf("Load(x).Dir = %q, want repo/.kiro/specs/x", spec.Dir)
	}
}
