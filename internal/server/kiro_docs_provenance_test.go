package server

// Row provenance: `read_only` has no source yet, and absent means unrestricted. A symlinked
// entry stays editable (the save resolves the link); only its delete is withheld.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestKiroDocs_OrdinaryFilesCarryNoRestriction pins the default direction: a plain file
// carries neither flag.
func TestKiroDocs_OrdinaryFilesCarryNoRestriction(t *testing.T) {
	base := t.TempDir()
	work := filepath.Join(base, "work")
	kiro := filepath.Join(work, ".kiro")
	if err := os.MkdirAll(kiro, 0o750); err != nil {
		t.Fatal(err)
	}
	writeFile(t, kiro, "steering/plain.md", "---\nname: plain\ndescription: ordinary\n---\n")
	writeFile(t, kiro, "agents/agent.md", "---\nname: agent\n---\n")
	writeFile(t, kiro, "skills/thing/SKILL.md", "---\nname: thing\n---\n")
	writeFile(t, kiro, "specs/feat/design.md", "# Design\n")
	writeFile(t, kiro, "hooks/h.json",
		`{"version":"v1","hooks":[{"name":"h","trigger":"PostFileSave",`+
			`"action":{"type":"command","command":"echo"}}]}`)

	srv := &Server{workDir: work, kiroDocs: &docsCache{}}
	docs := srv.collectKiroDocs(t.Context()).Docs
	if len(docs) == 0 {
		t.Fatal("no rows scanned; the fixture is wrong")
	}
	for _, d := range docs {
		if d.ReadOnly {
			t.Errorf("an ordinary file arrived read-only, so its edit is gated off: %+v", d)
		}
		if d.DeleteProtected {
			t.Errorf("an ordinary file arrived delete-protected: %+v", d)
		}
	}
}

// TestKiroDocs_SymlinkedEntryKeepsItsEditAndLosesItsDelete pins both halves on one row.
func TestKiroDocs_SymlinkedEntryKeepsItsEditAndLosesItsDelete(t *testing.T) {
	base := t.TempDir()
	work := filepath.Join(base, "work")
	kiro := filepath.Join(work, ".kiro")
	if err := os.MkdirAll(filepath.Join(kiro, "steering"), 0o750); err != nil {
		t.Fatal(err)
	}
	writeFile(t, kiro, "shared/canonical.md", "---\nname: canonical\ndescription: shared\n---\n")
	writeFile(t, kiro, "steering/ordinary.md", "---\nname: ordinary\ndescription: plain\n---\n")
	symlinkOr(t, filepath.Join(kiro, "shared", "canonical.md"),
		filepath.Join(kiro, "steering", "alias.md"))

	srv := &Server{workDir: work, kiroDocs: &docsCache{}}
	docs := srv.collectKiroDocs(t.Context()).Docs

	// The row carries the TARGET's front-matter name; its bits are what matter.
	linked, ok := findDocByPath(docs, "steering/alias.md")
	if !ok {
		t.Fatalf("the symlinked entry is absent; an in-root link IS listed: %+v", docs)
	}
	if linked.ReadOnly {
		t.Error("a symlinked entry arrived read-only: the write resolves the link and " +
			"succeeds, so the row would be claiming something the editor contradicts")
	}
	if !linked.DeleteProtected {
		t.Error("a symlinked entry offers its delete: the route canonicalizes the path, " +
			"so the user would remove the file the alias points at")
	}

	plain, ok := findDoc(docs, "ordinary")
	if !ok {
		t.Fatalf("the ordinary sibling is missing: %+v", docs)
	}
	if plain.ReadOnly || plain.DeleteProtected {
		t.Errorf("a gate spread to a file reached without a link: %+v", plain)
	}
}

// TestKiroDocs_FileUnderASymlinkedDirectoryIsUnrestricted pins that a file beneath an
// in-root linked directory keeps every affordance.
func TestKiroDocs_FileUnderASymlinkedDirectoryIsUnrestricted(t *testing.T) {
	base := t.TempDir()
	work := filepath.Join(base, "work")
	kiro := filepath.Join(work, ".kiro")
	if err := os.MkdirAll(kiro, 0o750); err != nil {
		t.Fatal(err)
	}
	writeFile(t, kiro, "elsewhere/inner.md", "---\nname: inner\ndescription: real\n---\n")
	symlinkOr(t, filepath.Join(kiro, "elsewhere"), filepath.Join(kiro, "steering"))

	srv := &Server{workDir: work, kiroDocs: &docsCache{}}
	docs := srv.collectKiroDocs(t.Context()).Docs
	row, ok := findDocByPath(docs, "steering/inner.md")
	if !ok {
		t.Fatalf("a file under a symlinked category directory is absent: %+v", docs)
	}
	if row.ReadOnly {
		t.Error("a file under a symlinked directory arrived read-only; that write works")
	}
	if row.DeleteProtected {
		t.Error("a file under a symlinked directory lost its delete; its own final " +
			"component is not a link, so the delete removes exactly that file")
	}
}

// TestKiroDocs_SymlinkedFlatCategoryEntryIsDeleteProtected covers the categories
// that enumerate a directory rather than walking it, so the gate is not a property
// only the recursive walk happens to have.
func TestKiroDocs_SymlinkedFlatCategoryEntryIsDeleteProtected(t *testing.T) {
	cases := []struct {
		name    string
		dir     string
		target  string
		linkRel string
		body    string
	}{
		{
			name: "agent", dir: "agents", target: "shared/real-agent.md",
			linkRel: "agents/linked.md", body: "---\nname: linked-agent\n---\n",
		},
		{
			name: "hook", dir: "hooks", target: "shared/real-hook.json",
			linkRel: "hooks/linked.json",
			body: `{"version":"v1","hooks":[{"name":"linked-hook","trigger":"PostFileSave",` +
				`"action":{"type":"command","command":"echo"}}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			work := filepath.Join(base, "work")
			kiro := filepath.Join(work, ".kiro")
			if err := os.MkdirAll(filepath.Join(kiro, tc.dir), 0o750); err != nil {
				t.Fatal(err)
			}
			writeFile(t, kiro, tc.target, tc.body)
			symlinkOr(t, filepath.Join(kiro, tc.target), filepath.Join(kiro, tc.linkRel))

			srv := &Server{workDir: work, kiroDocs: &docsCache{}}
			docs := srv.collectKiroDocs(t.Context()).Docs
			row, ok := findDocByPath(docs, tc.linkRel)
			if !ok {
				t.Fatalf("the symlinked %s is absent: %+v", tc.name, docs)
			}
			if !row.DeleteProtected {
				t.Errorf("a symlinked %s offers its delete: %+v", tc.name, row)
			}
			if row.ReadOnly {
				t.Errorf("a symlinked %s arrived read-only: %+v", tc.name, row)
			}
		})
	}
}

// TestKiroDocs_RestrictionsAreOmittedWhenFalse pins that both bits are omitted when false.
func TestKiroDocs_RestrictionsAreOmittedWhenFalse(t *testing.T) {
	base := t.TempDir()
	work := filepath.Join(base, "work")
	kiro := filepath.Join(work, ".kiro")
	if err := os.MkdirAll(kiro, 0o750); err != nil {
		t.Fatal(err)
	}
	writeFile(t, kiro, "steering/plain.md", "---\nname: plain\n---\n")

	srv := &Server{workDir: work, kiroDocs: &docsCache{}}
	docs := srv.collectKiroDocs(t.Context()).Docs
	if len(docs) != 1 {
		t.Fatalf("got %d rows, want 1: %+v", len(docs), docs)
	}
	raw, err := json.Marshal(docs[0])
	if err != nil {
		t.Fatalf("marshal row: %v", err)
	}
	for _, field := range []string{`"read_only"`, `"delete_protected"`} {
		if strings.Contains(string(raw), field) {
			t.Errorf("an unrestricted row states %s on the wire: %s", field, raw)
		}
	}
}

// findDocByPath locates a row by its path tail: a symlink's row carries its target's name.
func findDocByPath(docs []KiroDoc, suffix string) (KiroDoc, bool) {
	for _, d := range docs {
		if strings.HasSuffix(d.Path, suffix) {
			return d, true
		}
	}
	return KiroDoc{}, false
}
