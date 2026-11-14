package server

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/marotte/internal/marotte"
)

// docsDeleteServer serves the docs routes and the real file browser over one temp workspace,
// so a row deleted through the route the client calls is read back through the next listing.
func docsDeleteServer(t *testing.T) (http.Handler, string) {
	t.Helper()
	base := t.TempDir()
	work := filepath.Join(base, "work")
	if err := os.MkdirAll(filepath.Join(work, ".kiro"), 0o750); err != nil {
		t.Fatal(err)
	}
	sensitive := filebrowse.NewSensitive(filepath.Join(base, "config"))
	files, err := filebrowse.New(sensitive, []string{work})
	if err != nil {
		t.Fatal(err)
	}
	srv := docServer(work, WithSensitive(sensitive))
	srv.files = files
	srv.docDeleter = files
	return srv.routes(), work
}

// listDocs fails rather than hangs when the scan blocks, as an open of a fifo does.
func listDocs(t *testing.T, h http.Handler) []KiroDoc {
	t.Helper()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/workspace/kiro-docs", http.NoBody))
		done <- rec
	}()
	var rec *httptest.ResponseRecorder
	select {
	case rec = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("GET kiro-docs blocked")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("GET kiro-docs: status %d: %s", rec.Code, rec.Body.String())
	}
	var res KiroDocsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode kiro-docs: %v", err)
	}
	return res.Docs
}

// deleteRow sends what static-src/actions/docs.ts deleteDoc sends for a row.
func deleteRow(t *testing.T, h http.Handler, doc *KiroDoc) *httptest.ResponseRecorder {
	t.Helper()
	return deletePath(t, h, doc.Path)
}

func deletePath(t *testing.T, h http.Handler, p string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"path":` + jsonString(t, p) + `}`
	req := httptest.NewRequest(http.MethodPost, "/api/workspace/kiro-docs/delete", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func jsonString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mustRow(t *testing.T, docs []KiroDoc, category, name string) KiroDoc {
	t.Helper()
	d, ok := findDoc(docsByCategory(docs, category), name)
	if !ok {
		t.Fatalf("no %s row %q in %+v", category, name, docs)
	}
	return d
}

func requireGone(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s survived the delete (lstat err %v)", p, err)
	}
}

// TestKiroDocsDelete_SkillRowRemovesItsDirectoryAndRow is the reported case: a skill's row
// names its SKILL.md, and the delete must take the whole skill with it.
func TestKiroDocsDelete_SkillRowRemovesItsDirectoryAndRow(t *testing.T) {
	cases := []struct {
		name  string
		extra []string
	}{
		{name: "manifest_only"},
		{name: "with_references_and_scripts", extra: []string{"references/guide.md", "scripts/run.sh"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, work := docsDeleteServer(t)
			kiro := filepath.Join(work, ".kiro")
			writeFile(t, kiro, "skills/forme/SKILL.md", "---\nname: forme\ndescription: Governed documents\n---\n")
			writeFile(t, kiro, "skills/keep/SKILL.md", "---\nname: keep\ndescription: Untouched\n---\n")
			for _, rel := range tc.extra {
				writeFile(t, kiro, "skills/forme/"+rel, "x\n")
			}

			row := mustRow(t, listDocs(t, h), catSkill, "forme")
			if rec := deleteRow(t, h, &row); rec.Code != http.StatusOK {
				t.Fatalf("delete: status %d: %s", rec.Code, rec.Body.String())
			}

			requireGone(t, filepath.Join(kiro, "skills", "forme"))
			after := listDocs(t, h)
			if d, ok := findDoc(docsByCategory(after, catSkill), "forme"); ok {
				t.Errorf("the deleted skill's row is still listed: %+v", d)
			}
			mustRow(t, after, catSkill, "keep")
		})
	}
}

// TestKiroDocsDelete_EveryCategoryRowLeavesTheNextListing covers the sibling categories: each
// row's delete must remove everything that row was built from.
func TestKiroDocsDelete_EveryCategoryRowLeavesTheNextListing(t *testing.T) {
	cases := []struct {
		name     string
		category string
		row      string
		files    map[string]string
	}{
		{name: "steering", category: catSteering, row: "gone", files: map[string]string{
			"steering/nested/gone.md": "---\nname: gone\n---\n",
		}},
		{name: "agent_md_json_pair", category: catAgent, row: "gone", files: map[string]string{
			"agents/gone.md":   "---\nname: gone\n---\n",
			"agents/gone.json": `{"name":"gone"}`,
		}},
		{name: "agent_json_only", category: catAgent, row: "gone", files: map[string]string{
			"agents/gone.json": `{"name":"gone"}`,
		}},
		{name: "spec_last_document", category: catSpec, row: "Gone", files: map[string]string{
			"specs/feat/requirements.md": "# Gone\n",
		}},
		{name: "hook", category: catHook, row: "gone", files: map[string]string{
			"hooks/gone.json": `{"version":"v1","hooks":[{"name":"gone","trigger":"PostFileSave",` +
				`"action":{"type":"command","command":"echo"}}]}`,
		}},
		{name: "prompt", category: catPrompt, row: "gone", files: map[string]string{
			"prompts/gone.md": "# Gone\n",
		}},
		{name: "steering_deepest_walked_level", category: catSteering, row: "gone", files: map[string]string{
			"steering/a/b/c/gone.md": "---\nname: gone\n---\n",
		}},
		{name: "spec_deepest_walked_level", category: catSpec, row: "Gone", files: map[string]string{
			"specs/a/b/c/gone.md": "# Gone\n",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, work := docsDeleteServer(t)
			kiro := filepath.Join(work, ".kiro")
			for rel, body := range tc.files {
				writeFile(t, kiro, rel, body)
			}
			row := mustRow(t, listDocs(t, h), tc.category, tc.row)
			if rec := deleteRow(t, h, &row); rec.Code != http.StatusOK {
				t.Fatalf("delete: status %d: %s", rec.Code, rec.Body.String())
			}
			if d, ok := findDoc(docsByCategory(listDocs(t, h), tc.category), tc.row); ok {
				t.Errorf("the deleted row is still listed: %+v", d)
			}
		})
	}
}

// TestKiroDocsDelete_RefusesWhatTheRowDoesNotOwn pins the guards: a linked skill directory or
// agent twin is refused with its target intact, a directory wearing an agent twin's name is left
// alone, and a path no row lists is not deleted.
func TestKiroDocsDelete_RefusesWhatTheRowDoesNotOwn(t *testing.T) {
	t.Run("linked_skill_directory", func(t *testing.T) {
		h, work := docsDeleteServer(t)
		kiro := filepath.Join(work, ".kiro")
		writeFile(t, kiro, "shared/forme/SKILL.md", "---\nname: forme\n---\n")
		if err := os.MkdirAll(filepath.Join(kiro, "skills"), 0o750); err != nil {
			t.Fatal(err)
		}
		symlinkOr(t, filepath.Join(kiro, "shared", "forme"), filepath.Join(kiro, "skills", "forme"))
		row := mustRow(t, listDocs(t, h), catSkill, "forme")
		if !row.DeleteProtected {
			t.Errorf("a linked skill directory offers its delete: %+v", row)
		}
		if rec := deleteRow(t, h, &row); rec.Code != http.StatusForbidden {
			t.Errorf("delete: status %d, want 403: %s", rec.Code, rec.Body.String())
		}
		if _, err := os.Stat(filepath.Join(kiro, "shared", "forme", "SKILL.md")); err != nil {
			t.Errorf("the link's target was touched: %v", err)
		}
	})
	t.Run("linked_agent_twin", func(t *testing.T) {
		h, work := docsDeleteServer(t)
		kiro := filepath.Join(work, ".kiro")
		writeFile(t, kiro, "agents/pair.md", "---\nname: pair\n---\n")
		writeFile(t, kiro, "shared/other.json", `{"name":"other"}`)
		symlinkOr(t, filepath.Join(kiro, "shared", "other.json"), filepath.Join(kiro, "agents", "pair.json"))
		row := mustRow(t, listDocs(t, h), catAgent, "pair")
		if !row.DeleteProtected {
			t.Errorf("an agent whose twin is a link offers its delete: %+v", row)
		}
		if rec := deleteRow(t, h, &row); rec.Code != http.StatusForbidden {
			t.Errorf("delete: status %d, want 403: %s", rec.Code, rec.Body.String())
		}
		if _, err := os.Stat(filepath.Join(kiro, "shared", "other.json")); err != nil {
			t.Errorf("the twin link's target was removed: %v", err)
		}
		if _, err := os.Stat(filepath.Join(kiro, "agents", "pair.md")); err != nil {
			t.Errorf("a refused delete removed pair.md: %v", err)
		}
		if after := mustRow(t, listDocs(t, h), catAgent, "pair"); after.Path != row.Path {
			t.Errorf("after the refusal the row is %+v, want it still at %s", after, row.Path)
		}
	})
	t.Run("twin_is_a_directory", func(t *testing.T) {
		h, work := docsDeleteServer(t)
		kiro := filepath.Join(work, ".kiro")
		writeFile(t, kiro, "agents/pair.md", "---\nname: pair\n---\n")
		writeFile(t, kiro, "agents/pair.json/keep.txt", "kept\n")
		row := mustRow(t, listDocs(t, h), catAgent, "pair")
		if rec := deleteRow(t, h, &row); rec.Code != http.StatusOK {
			t.Fatalf("delete: status %d, want 200: %s", rec.Code, rec.Body.String())
		}
		requireGone(t, filepath.Join(kiro, "agents", "pair.md"))
		if _, err := os.Stat(filepath.Join(kiro, "agents", "pair.json", "keep.txt")); err != nil {
			t.Errorf("a directory wearing the twin's name was removed: %v", err)
		}
		if d, ok := findDoc(docsByCategory(listDocs(t, h), catAgent), "pair"); ok {
			t.Errorf("the deleted agent is still listed: %+v", d)
		}
	})
	t.Run("skill_link_to_its_own_tree", func(t *testing.T) {
		h, work := docsDeleteServer(t)
		kiro := filepath.Join(work, ".kiro")
		writeFile(t, kiro, "steering/a.md", "# A\n")
		if err := os.MkdirAll(filepath.Join(kiro, "skills"), 0o750); err != nil {
			t.Fatal(err)
		}
		symlinkOr(t, "..", filepath.Join(kiro, "skills", "root"))
		listDocs(t, h)
		p := strings.TrimPrefix(filepath.Join(kiro, "skills", "root", "SKILL.md"), "/")
		if rec := deletePath(t, h, p); rec.Code != http.StatusNotFound {
			t.Errorf("delete: status %d, want 404: %s", rec.Code, rec.Body.String())
		}
		if _, err := os.Stat(filepath.Join(kiro, "steering", "a.md")); err != nil {
			t.Errorf("the .kiro tree behind the link was touched: %v", err)
		}
	})
	t.Run("unlisted_path", func(t *testing.T) {
		h, work := docsDeleteServer(t)
		writeFile(t, work, "notes.md", "# Notes\n")
		listDocs(t, h)
		if rec := deletePath(t, h, strings.TrimPrefix(filepath.Join(work, "notes.md"), "/")); rec.Code != http.StatusNotFound {
			t.Errorf("delete: status %d, want 404: %s", rec.Code, rec.Body.String())
		}
		if _, err := os.Stat(filepath.Join(work, "notes.md")); err != nil {
			t.Errorf("a file no row lists was deleted: %v", err)
		}
	})
}

func mkfifoOr(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
}

// socketAt binds a Unix socket in a short directory and moves it to p, since a temp path can
// exceed the socket address length.
func socketAt(t *testing.T, p string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	l, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Skipf("unix socket unavailable: %v", err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	t.Cleanup(func() { _ = l.Close() })
	if err := os.Rename(filepath.Join(dir, "s"), p); err != nil {
		t.Skipf("cannot move the socket into place: %v", err)
	}
}

// TestKiroDocsDelete_AgentTwinThatIsNoFileLeavesNoRow pins that a fifo or socket wearing the
// twin's name is no agent file: the delete removes the row's own file and no row comes back.
func TestKiroDocsDelete_AgentTwinThatIsNoFileLeavesNoRow(t *testing.T) {
	cases := []struct {
		plant func(t *testing.T, kiro string)
		name  string
	}{
		{name: "fifo", plant: func(t *testing.T, kiro string) {
			mkfifoOr(t, filepath.Join(kiro, "agents", "pair.json"))
		}},
		{name: "socket", plant: func(t *testing.T, kiro string) {
			socketAt(t, filepath.Join(kiro, "agents", "pair.json"))
		}},
		{name: "link_to_a_fifo", plant: func(t *testing.T, kiro string) {
			mkfifoOr(t, filepath.Join(kiro, "shared", "pipe"))
			symlinkOr(t, filepath.Join(kiro, "shared", "pipe"), filepath.Join(kiro, "agents", "pair.json"))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, work := docsDeleteServer(t)
			kiro := filepath.Join(work, ".kiro")
			writeFile(t, kiro, "agents/pair.md", "---\nname: pair\n---\n")
			tc.plant(t, kiro)
			row := mustRow(t, listDocs(t, h), catAgent, "pair")
			if rec := deleteRow(t, h, &row); rec.Code != http.StatusOK {
				t.Fatalf("delete: status %d, want 200: %s", rec.Code, rec.Body.String())
			}
			requireGone(t, filepath.Join(kiro, "agents", "pair.md"))
			if d, ok := findDoc(docsByCategory(listDocs(t, h), catAgent), "pair"); ok {
				t.Errorf("the deleted agent is still listed: %+v", d)
			}
		})
	}
}

// TestKiroDocsDelete_BroadcastsSoEveryDeviceRefetches pins the event other devices refetch on.
func TestKiroDocsDelete_BroadcastsSoEveryDeviceRefetches(t *testing.T) {
	base := t.TempDir()
	work := filepath.Join(base, "work")
	writeFile(t, filepath.Join(work, ".kiro"), "prompts/gone.md", "# Gone\n")
	sensitive := filebrowse.NewSensitive(filepath.Join(base, "config"))
	files, err := filebrowse.New(sensitive, []string{work})
	if err != nil {
		t.Fatal(err)
	}
	srv := docServer(work, WithSensitive(sensitive), WithDocDeleter(files))
	engine := &fakeEngine{}
	srv.agent = engine
	h := srv.routes()
	row := mustRow(t, listDocs(t, h), catPrompt, "gone")
	if rec := deleteRow(t, h, &row); rec.Code != http.StatusOK {
		t.Fatalf("delete: status %d: %s", rec.Code, rec.Body.String())
	}
	if len(engine.events) != 1 || engine.events[0].Type != marotte.EventKiroDocsChanged {
		t.Errorf("events = %+v, want one kiro_docs_changed", engine.events)
	}
}

// TestKiroDocs_ListsASkillAddedOnDiskWithoutARestart pins live pickup through the memoized scan,
// including a skill whose directory, or a link to it, was listed before its SKILL.md landed.
func TestKiroDocs_ListsASkillAddedOnDiskWithoutARestart(t *testing.T) {
	h, work := docsDeleteServer(t)
	kiro := filepath.Join(work, ".kiro")
	writeFile(t, kiro, "skills/keep/SKILL.md", "---\nname: keep\n---\n")
	if err := os.MkdirAll(filepath.Join(kiro, "skills", "fresh"), 0o750); err != nil {
		t.Fatal(err)
	}
	if d, ok := findDoc(docsByCategory(listDocs(t, h), catSkill), "fresh"); ok {
		t.Errorf("an empty skill directory is a row: %+v", d)
	}
	writeFile(t, kiro, "skills/fresh/SKILL.md", "---\nname: fresh\ndescription: Just added\n---\n")
	if d := mustRow(t, listDocs(t, h), catSkill, "fresh"); d.Description != "Just added" {
		t.Errorf("fresh = %+v, want its manifest's description", d)
	}
	writeFile(t, kiro, "skills/other/SKILL.md", "---\nname: other\n---\n")
	mustRow(t, listDocs(t, h), catSkill, "other")

	if err := os.MkdirAll(filepath.Join(kiro, "shared", "linked"), 0o750); err != nil {
		t.Fatal(err)
	}
	symlinkOr(t, filepath.Join(kiro, "shared", "linked"), filepath.Join(kiro, "skills", "linked"))
	if d, ok := findDoc(docsByCategory(listDocs(t, h), catSkill), "linked"); ok {
		t.Errorf("a link to an empty directory is a row: %+v", d)
	}
	writeFile(t, kiro, "shared/linked/SKILL.md", "---\nname: linked\n---\n")
	mustRow(t, listDocs(t, h), catSkill, "linked")
}
