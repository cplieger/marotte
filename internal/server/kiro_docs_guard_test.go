package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/filebrowse"
)

// A REAL filesystem: symlink resolution across a root boundary is what MapFS cannot express.

// symlinkOr skips when the platform or filesystem refuses symlinks, so the suite
// stays runnable rather than failing for an unrelated reason.
func symlinkOr(t *testing.T, target, name string) {
	t.Helper()
	if err := os.Symlink(target, name); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}
}

// TestKiroDocsGuard_RefusesASymlinkOutOfTheScannedTree pins that a `.kiro/steering` linked
// to a credential directory is refused.
func TestKiroDocsGuard_RefusesASymlinkOutOfTheScannedTree(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o750); err != nil {
		t.Fatal(err)
	}
	secret := "---\ndescription: sk-live-0000\n---\n"
	if err := os.WriteFile(filepath.Join(outside, "creds.md"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	kiro := filepath.Join(base, "work", ".kiro")
	if err := os.MkdirAll(kiro, 0o750); err != nil {
		t.Fatal(err)
	}
	symlinkOr(t, outside, filepath.Join(kiro, "steering"))

	guard := newRootGuard(kiro, "ws/.kiro", filebrowse.Sensitive{})
	if guard.allows("steering") {
		t.Error("the symlinked category directory was admitted; the walk would enumerate its target")
	}
	if guard.allows("steering/creds.md") {
		t.Error("a file outside the scanned tree was admitted")
	}
}

// TestScanKiroDocs_SymlinkedCategoryContributesNothing pins no row and none of the file's
// bytes in the output, not merely an empty description.
func TestScanKiroDocs_SymlinkedCategoryContributesNothing(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "creds.md"), []byte("---\ndescription: sk-live-0000\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(base, "work")
	kiro := filepath.Join(work, ".kiro")
	if err := os.MkdirAll(filepath.Join(kiro, "agents"), 0o750); err != nil {
		t.Fatal(err)
	}
	symlinkOr(t, outside, filepath.Join(kiro, "steering"))
	// A real row beside it, so an empty scan fails rather than passing.
	writeFile(t, kiro, "agents/real.md", "---\nname: real\ndescription: a genuine agent\n---\n")

	srv := &Server{workDir: work, kiroDocs: &docsCache{}}
	docs := srv.collectKiroDocs(t.Context()).Docs

	if _, ok := findDoc(docs, "real"); !ok {
		t.Errorf("the genuine row is missing, so the guard refused too much: %+v", docs)
	}
	for _, d := range docs {
		if d.Category == catSteering {
			t.Errorf("a steering row came from outside the tree: %+v", d)
		}
		if strings.Contains(d.Description, "sk-live-0000") {
			t.Errorf("the outside file's content reached the docs list: %+v", d)
		}
	}
}

// leakName is planted in the escaped target and asserted absent from EVERY field: a listing
// leaks names even when the guarded read refuses.
const leakName = "escaped-from-outside"

// TestScanKiroDocs_SymlinkedFlatCategoryIsNotEnumerated pins the guard ahead of the listing
// for skills, agents and hooks, end to end.
func TestScanKiroDocs_SymlinkedFlatCategoryIsNotEnumerated(t *testing.T) {
	cases := []struct {
		category string
		plant    func(t *testing.T, outside string)
	}{
		{catSkill + "s", func(t *testing.T, outside string) {
			// A manifest-less skill directory is still a row, the shape that leaked.
			writeFile(t, outside, filepath.Join(leakName, "SKILL.md"), "---\nname: "+leakName+"\n---\n")
			if err := os.MkdirAll(filepath.Join(outside, leakName+"-bare"), 0o750); err != nil {
				t.Fatal(err)
			}
		}},
		{catAgent + "s", func(t *testing.T, outside string) {
			writeFile(t, outside, leakName+".md", "---\nname: "+leakName+"\n---\n")
			writeFile(t, outside, leakName+".json", `{"name":"`+leakName+`"}`)
		}},
		{catHook + "s", func(t *testing.T, outside string) {
			writeFile(t, outside, leakName+".json",
				`{"version":"v1","hooks":[{"name":"`+leakName+`","trigger":"PostFileSave",`+
					`"action":{"type":"command","command":"`+leakName+`"}}]}`)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.category, func(t *testing.T) {
			base := t.TempDir()
			outside := filepath.Join(base, "outside")
			if err := os.MkdirAll(outside, 0o750); err != nil {
				t.Fatal(err)
			}
			tc.plant(t, outside)

			work := filepath.Join(base, "work")
			kiro := filepath.Join(work, ".kiro")
			if err := os.MkdirAll(kiro, 0o750); err != nil {
				t.Fatal(err)
			}
			symlinkOr(t, outside, filepath.Join(kiro, tc.category))
			// A real row in a DIFFERENT category, so an empty scan fails.
			writeFile(t, kiro, "steering/real.md", "---\nname: real\ndescription: a genuine doc\n---\n")

			srv := &Server{workDir: work, kiroDocs: &docsCache{}}
			docs := srv.collectKiroDocs(t.Context()).Docs

			if _, ok := findDoc(docs, "real"); !ok {
				t.Errorf("the genuine row is missing, so the guard refused too much: %+v", docs)
			}
			for _, d := range docs {
				if rendered := fmt.Sprintf("%+v", d); strings.Contains(rendered, leakName) {
					t.Errorf("a name from outside the tree reached the docs list: %s", rendered)
				}
			}
			// The row COUNT too: an undescribed row carries no planted name.
			if got := len(docsByCategory(docs, strings.TrimSuffix(tc.category, "s"))); got != 0 {
				t.Errorf("%s rows = %d, want 0 from a category symlinked out of the tree", tc.category, got)
			}
		})
	}
}

// TestKiroDocsGuard_RefusesASymlinkedFlatCategoryDirectory pins the refusal at the category
// directory itself.
func TestKiroDocsGuard_RefusesASymlinkedFlatCategoryDirectory(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o750); err != nil {
		t.Fatal(err)
	}
	kiro := filepath.Join(base, "work", ".kiro")
	if err := os.MkdirAll(kiro, 0o750); err != nil {
		t.Fatal(err)
	}
	guard := newRootGuard(kiro, "ws/.kiro", filebrowse.Sensitive{})
	for _, category := range []string{"skills", "agents", "hooks"} {
		symlinkOr(t, outside, filepath.Join(kiro, category))
		if guard.allows(category) {
			t.Errorf("%q was admitted; the scanner would ReadDir its target", category)
		}
	}
}

// TestKiroDocsGuard_RefusesASymlinkedFileOutOfTheTree pins the symlinked FILE case.
func TestKiroDocsGuard_RefusesASymlinkedFileOutOfTheTree(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "secret"), []byte("---\ndescription: leaked\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(base, "work")
	kiro := filepath.Join(work, ".kiro")
	if err := os.MkdirAll(filepath.Join(kiro, "steering"), 0o750); err != nil {
		t.Fatal(err)
	}
	symlinkOr(t, filepath.Join(base, "secret"), filepath.Join(kiro, "steering", "notes.md"))
	writeFile(t, kiro, "steering/ordinary.md", "---\ndescription: fine\n---\n")

	srv := &Server{workDir: work, kiroDocs: &docsCache{}}
	docs := srv.collectKiroDocs(t.Context()).Docs

	if len(docs) != 1 {
		t.Fatalf("got %d rows, want 1 (the symlink refused, the real file kept): %+v", len(docs), docs)
	}
	if docs[0].Name != "ordinary" {
		t.Errorf("row = %+v, want the ordinary file", docs[0])
	}
}

// TestKiroDocsGuard_AdmitsALinkThatStaysInsideTheTree pins that an in-tree link is admitted.
func TestKiroDocsGuard_AdmitsALinkThatStaysInsideTheTree(t *testing.T) {
	base := t.TempDir()
	work := filepath.Join(base, "work")
	kiro := filepath.Join(work, ".kiro")
	if err := os.MkdirAll(filepath.Join(kiro, "steering"), 0o750); err != nil {
		t.Fatal(err)
	}
	writeFile(t, kiro, "shared/canonical.md", "---\nname: canonical\ndescription: shared\n---\n")
	symlinkOr(t, filepath.Join(kiro, "shared", "canonical.md"), filepath.Join(kiro, "steering", "alias.md"))

	srv := &Server{workDir: work, kiroDocs: &docsCache{}}
	docs := srv.collectKiroDocs(t.Context()).Docs

	if _, ok := findDoc(docs, "canonical"); !ok {
		t.Errorf("an in-tree symlink was refused: %+v", docs)
	}
}

// TestKiroDocsGuard_ASymlinkedRootIsItsOwnBoundary pins that a symlinked `.kiro` is followed
// and its target is the boundary.
func TestKiroDocsGuard_ASymlinkedRootIsItsOwnBoundary(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real-kiro")
	if err := os.MkdirAll(filepath.Join(real, "steering"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "steering", "doc.md"), []byte("---\nname: doc\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(base, "work")
	if err := os.MkdirAll(work, 0o750); err != nil {
		t.Fatal(err)
	}
	symlinkOr(t, real, filepath.Join(work, ".kiro"))

	srv := &Server{workDir: work, kiroDocs: &docsCache{}}
	if docs := srv.collectKiroDocs(t.Context()).Docs; len(docs) != 1 {
		t.Errorf("got %d rows from a symlinked .kiro, want 1: %+v", len(docs), docs)
	}
}

// TestKiroDocsGuard_ConsultsTheSharedSensitiveDenylist pins that the shared deny list is
// checked on the RESOLVED path, the only form that can match.
func TestKiroDocsGuard_ConsultsTheSharedSensitiveDenylist(t *testing.T) {
	for _, sensitive := range []string{
		"/config/home/.aws/sso/cache/token.md",
		"/config/chats/abc.md",
		"/config/mcp.json",
	} {
		if !(filebrowse.Sensitive{}).Blocks(sensitive) {
			t.Fatalf("fixture wrong: %q is not on the shared denylist", sensitive)
		}
	}
	g := &rootGuard{dir: "/config", category: "test"}
	// Inside the root, so only the deny list can refuse it.
	if g.allow("home/.aws/sso/cache/token.md").allowed {
		t.Error("a sensitive path inside the scanned root was admitted")
	}
	if g.allow("chats/abc.md").allowed {
		t.Error("the chat store was admitted")
	}
}

// The guard refuses by the deny list composition hands it, so a config root other
// than /config moves the refusal with it.
func TestKiroDocsGuard_UsesTheConfiguredRoot(t *testing.T) {
	cfg := t.TempDir()
	writeFile(t, cfg, "forge-store/notes.md", "---\ndescription: x\n---\n")
	writeFile(t, cfg, "steering/ok.md", "---\ndescription: ok\n---\n")
	guard := newRootGuard(cfg, "test", filebrowse.NewSensitive(cfg))
	if guard.allows("forge-store/notes.md") {
		t.Error("a file in the configured root's forge-store was admitted")
	}
	if !guard.allows("steering/ok.md") {
		t.Error("an ordinary document under the configured root was refused")
	}
}

// A root the scan cannot resolve refuses everything rather than admitting it.
// The alternative — treating an unnameable root as permissive — is the failure
// mode a guard exists to prevent.
func TestKiroDocsGuard_UnresolvableRootRefusesEverything(t *testing.T) {
	guard := newRootGuard(filepath.Join(t.TempDir(), "nope"), "ws/.kiro", filebrowse.Sensitive{})
	if guard.allows("steering/a.md") {
		t.Error("an unresolvable root admitted a path")
	}
}

// A nil guard admits everything. That is the fstest.MapFS seam the other scanner
// tests use, and it must stay explicit rather than accidental.
func TestKiroDocsGuard_NilAdmitsEverything(t *testing.T) {
	var guard pathGuard
	if !guard.allows("anything/at/all.md") {
		t.Error("a nil guard refused a path; the MapFS tests would silently scan nothing")
	}
}
