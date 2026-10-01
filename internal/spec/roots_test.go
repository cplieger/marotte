package spec

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func mkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("Setup: mkdir %s: %v", dir, err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("Setup: write %s: %v", path, err)
	}
}

func TestRoots_EnumeratesTheWorkspaceThenEachRepo(t *testing.T) {
	work := t.TempDir()
	mkdirAll(t, filepath.Join(work, ".kiro", "specs"))
	mkdirAll(t, filepath.Join(work, "beta", ".kiro"))
	mkdirAll(t, filepath.Join(work, "alpha", ".kiro"))
	mkdirAll(t, filepath.Join(work, "plain"))
	mkdirAll(t, filepath.Join(work, ".hidden", ".kiro"))
	writeFile(t, filepath.Join(work, "file"), "")
	mkdirAll(t, filepath.Join(work, "notdir"))
	writeFile(t, filepath.Join(work, "notdir", ".kiro"), "a file named .kiro")

	got := Roots(work)
	want := []Root{
		{Dir: filepath.Join(work, ".kiro"), Rel: ".kiro"},
		{Dir: filepath.Join(work, "alpha", ".kiro"), Rel: "alpha/.kiro"},
		{Dir: filepath.Join(work, "beta", ".kiro"), Rel: "beta/.kiro"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Roots(%s) = %+v, want %+v", work, got, want)
	}
}

func TestRoots_WithoutAWorkspaceKiroStillFindsRepos(t *testing.T) {
	work := t.TempDir()
	mkdirAll(t, filepath.Join(work, "repo", ".kiro"))
	got := Roots(work)
	want := []Root{{Dir: filepath.Join(work, "repo", ".kiro"), Rel: "repo/.kiro"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Roots(%s) = %+v, want %+v", work, got, want)
	}
}

func TestRoots_MissingWorkDirIsEmpty(t *testing.T) {
	got := Roots(filepath.Join(t.TempDir(), "absent"))
	if len(got) != 0 {
		t.Errorf("Roots(absent) = %+v, want none", got)
	}
}

func TestDirOf(t *testing.T) {
	cases := map[string]struct {
		rel  string
		want string
		ok   bool
	}{
		"spec_file":          {".kiro/specs/foo/tasks.md", ".kiro/specs/foo", true},
		"sidecar":            {".kiro/specs/foo/tasks.meta.json", ".kiro/specs/foo", true},
		"the_directory":      {".kiro/specs/foo", ".kiro/specs/foo", true},
		"trailing_slash":     {".kiro/specs/foo/", ".kiro/specs/foo", true},
		"nested_file":        {".kiro/specs/foo/notes/deep.md", ".kiro/specs/foo", true},
		"repo_root":          {"marotte/.kiro/specs/foo/design.md", "marotte/.kiro/specs/foo", true},
		"specs_alone":        {".kiro/specs", "", false},
		"specs_slash":        {".kiro/specs/", "", false},
		"dot_name":           {".kiro/specs/./tasks.md", "", false},
		"dotdot_name":        {".kiro/specs/../steering/x.md", "", false},
		"two_prefixes":       {"a/b/.kiro/specs/foo", "", false},
		"dot_prefix":         {".hidden/.kiro/specs/foo", "", false},
		"absolute":           {"/workspace/.kiro/specs/foo", "", false},
		"steering":           {".kiro/steering/go.md", "", false},
		"outside":            {"src/main.go", "", false},
		"empty":              {"", "", false},
		"specs_in_the_wrong": {"specs/.kiro/foo", "", false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, ok := DirOf(c.rel)
			if got != c.want || ok != c.ok {
				t.Errorf("DirOf(%q) = %q, %v, want %q, %v", c.rel, got, ok, c.want, c.ok)
			}
		})
	}
}
