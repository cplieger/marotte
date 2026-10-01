package server

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// specWorkspace builds a workspace holding one complete spec at
// .kiro/specs/feat, one in a nested repo at myrepo/.kiro/specs/feat, and one
// directory with no markdown at .kiro/specs/empty.
func specWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, ".kiro/specs/feat/tasks.md", "- [ ] 1 one\n- [x] 2 two\n")
	writeFile(t, dir, ".kiro/specs/feat/analysis.md", "# A\n")
	writeFile(t, dir, ".kiro/specs/feat/design.md", "# D\n")
	writeFile(t, dir, ".kiro/specs/feat/bugfix.md", "# B\n")
	writeFile(t, dir, ".kiro/specs/feat/tasks.meta.json", "{}")
	writeFile(t, dir, ".kiro/specs/empty/notes.txt", "not a doc")
	writeFile(t, dir, "myrepo/.kiro/specs/feat/requirements.md", "# R\n")
	return dir
}

// specMux registers the route the way ListenAndServe does, so PathValue is
// the real mux's unescaped segment. The canonical-path gate is left off so the
// table below exercises the HANDLER's own refusals; the gate's earlier 400 on a
// traversal spelling has its own test.
func specMux(workDir string) http.Handler {
	s := &Server{workDir: workDir}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/specs/{dir}", s.handleSpec)
	return mux
}

func getSpec(t *testing.T, h http.Handler, target string, hdr http.Header) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, http.NoBody)
	maps.Copy(req.Header, hdr)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// The handler is called with the dir already set on the request, the way the
// mux hands it over, so the table exercises its own validation and not the
// mux's cleaning redirect (which answers ".." before any handler runs).
func TestHandleSpec_RefusesEverythingButASpecDirectory(t *testing.T) {
	s := &Server{workDir: specWorkspace(t)}
	cases := []struct {
		name string
		dir  string
	}{
		{name: "empty", dir: ""},
		{name: "space", dir: " "},
		{name: "dotdot", dir: ".."},
		{name: "bare_name", dir: "feat"},
		{name: "dotdot_name", dir: ".kiro/specs/.."},
		{name: "unknown_root", dir: "other/.kiro/specs/feat"},
		{name: "no_markdown", dir: ".kiro/specs/empty"},
		{name: "missing_dir", dir: ".kiro/specs/missing"},
		{name: "nested_segment", dir: ".kiro/specs/feat/tasks.md"},
		{name: "double_encoded", dir: ".kiro%2Fspecs%2Ffeat"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/specs/x", http.NoBody)
			req.SetPathValue("dir", tc.dir)
			rec := httptest.NewRecorder()
			s.handleSpec(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Errorf("GET /api/specs/%q status = %d, want 404 (body %q)", tc.dir, rec.Code, rec.Body.String())
			}
		})
	}
}

// The API surface's canonical-path gate runs before the mux, so a traversal
// spelling never reaches the handler: it is 400 there, and the handler's own
// 404 above is the second line.
func TestHandleSpec_TraversalIsRefusedByTheCanonicalPathGate(t *testing.T) {
	h := canonicalAPIPath(specMux(specWorkspace(t)))
	for _, dir := range []string{"..", ".kiro/specs/..", "%2e%2e/.kiro/specs/feat"} {
		rec := getSpec(t, h, "/api/specs/"+dir, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET /api/specs/%s status = %d, want 400", dir, rec.Code)
		}
	}
}

// One spelling reaches the handler: the once-encoded segment. The unencoded
// path has three segments and matches no pattern, and the twice-encoded one
// hands the handler a dir holding literal %2F, which no root spells.
func TestHandleSpec_OnlyTheOnceEncodedSpellingResolves(t *testing.T) {
	h := canonicalAPIPath(specMux(specWorkspace(t)))
	cases := []struct {
		name   string
		target string
		want   int
	}{
		{name: "once_encoded", target: "/api/specs/.kiro%2Fspecs%2Ffeat", want: http.StatusOK},
		{name: "unencoded", target: "/api/specs/.kiro/specs/feat", want: http.StatusNotFound},
		{name: "twice_encoded", target: "/api/specs/.kiro%252Fspecs%252Ffeat", want: http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := getSpec(t, h, tc.target, nil)
			if rec.Code != tc.want {
				t.Errorf("GET %s status = %d, want %d (body %q)", tc.target, rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestHandleSpec_ServesTheOrderedDocs(t *testing.T) {
	h := specMux(specWorkspace(t))
	rec := getSpec(t, h, "/api/specs/"+url.PathEscape(".kiro/specs/feat"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	etag := rec.Header().Get(headerETag)
	if !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `"`) || len(etag) != 66 {
		t.Errorf("ETag = %q, want a quoted hex sha256", etag)
	}
	var sp marotte.Spec
	if err := json.Unmarshal(rec.Body.Bytes(), &sp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if sp.Dir != ".kiro/specs/feat" || sp.Name != "feat" {
		t.Errorf("spec = dir %q name %q, want .kiro/specs/feat and feat", sp.Dir, sp.Name)
	}
	if sp.UpdatedAt.IsZero() {
		t.Error("updated_at is zero, want the newest mtime")
	}
	want := []struct {
		file string
		role marotte.SpecDocRole
	}{
		{"bugfix.md", marotte.SpecDocRoleRequirements},
		{"design.md", marotte.SpecDocRoleDesign},
		{"tasks.md", marotte.SpecDocRoleTasks},
		{"analysis.md", marotte.SpecDocRoleOther},
	}
	if len(sp.Docs) != len(want) {
		t.Fatalf("docs = %d, want %d: %+v", len(sp.Docs), len(want), sp.Docs)
	}
	for i, w := range want {
		d := sp.Docs[i]
		if d.File != w.file || d.Role != w.role {
			t.Errorf("docs[%d] = %s/%s, want %s/%s", i, d.File, d.Role, w.file, w.role)
		}
		if d.Hash == "" || d.Content == "" {
			t.Errorf("docs[%d] %s: hash %q content %q, want both present", i, d.File, d.Hash, d.Content)
		}
	}
	tasks := sp.Docs[2]
	if len(tasks.Tasks) != 2 || tasks.Progress == nil {
		t.Errorf("tasks.md: tasks %d progress %v, want 2 tasks and a progress", len(tasks.Tasks), tasks.Progress)
	} else if tasks.Progress.Completed != 1 || tasks.Progress.Total != 2 {
		t.Errorf("tasks.md progress = %+v, want completed 1 of 2", *tasks.Progress)
	}
	if sp.Docs[0].Tasks != nil {
		t.Errorf("bugfix.md carries tasks %v, want none", sp.Docs[0].Tasks)
	}
}

func TestHandleSpec_ResolvesANestedRepoRoot(t *testing.T) {
	h := specMux(specWorkspace(t))
	rec := getSpec(t, h, "/api/specs/"+url.PathEscape("myrepo/.kiro/specs/feat"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	var sp marotte.Spec
	if err := json.Unmarshal(rec.Body.Bytes(), &sp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if sp.Dir != "myrepo/.kiro/specs/feat" || len(sp.Docs) != 1 || sp.Docs[0].File != "requirements.md" {
		t.Errorf("spec = %+v, want the nested repo's one requirements doc", sp)
	}
}

func TestHandleSpec_ETagRoundTripAnswers304(t *testing.T) {
	h := specMux(specWorkspace(t))
	target := "/api/specs/" + url.PathEscape(".kiro/specs/feat")
	first := getSpec(t, h, target, nil)
	if first.Code != http.StatusOK {
		t.Fatalf("first GET status = %d, want 200", first.Code)
	}
	etag := first.Header().Get(headerETag)

	second := getSpec(t, h, target, http.Header{headerIfNoneMatch: {etag}})
	if second.Code != http.StatusNotModified {
		t.Fatalf("GET with If-None-Match status = %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Errorf("304 body = %q, want empty", second.Body.String())
	}
	if got := second.Header().Get(headerETag); got != etag {
		t.Errorf("304 ETag = %q, want %q", got, etag)
	}

	stale := getSpec(t, h, target, http.Header{headerIfNoneMatch: {`"stale"`}})
	if stale.Code != http.StatusOK {
		t.Errorf("GET with a stale If-None-Match status = %d, want 200", stale.Code)
	}
	if got := stale.Header().Get(headerETag); got != etag {
		t.Errorf("fresh ETag = %q, want %q (unchanged docs)", got, etag)
	}
}

// The three shapes a hash-only digest could not see, plus the separator that
// keeps two fields from running together. An over-size doc carries an EMPTY
// hash (spec.Load lists it with TooLarge and no content), so over hashes alone
// a rename and an over-size doc arriving or leaving are all invisible.
func TestSpecETag_MovesOnTheDocSetAndNotOnHashesAlone(t *testing.T) {
	doc := func(file, hash string) marotte.SpecDoc {
		return marotte.SpecDoc{File: file, Hash: hash}
	}
	tooLarge := func(file string) marotte.SpecDoc {
		return marotte.SpecDoc{File: file, TooLarge: true}
	}

	approved := func(phase, hash string, stale bool) map[string]marotte.SpecApproval {
		return map[string]marotte.SpecApproval{phase: {Hash: hash, Stale: stale}}
	}

	cases := []struct {
		name       string
		a, b       []marotte.SpecDoc
		appA, appB map[string]marotte.SpecApproval
	}{
		{
			name: "a doc renamed",
			a:    []marotte.SpecDoc{doc("design.md", "aa")},
			b:    []marotte.SpecDoc{doc("notes.md", "aa")},
		},
		{
			name: "an over-size doc added",
			a:    []marotte.SpecDoc{doc("design.md", "aa")},
			b:    []marotte.SpecDoc{doc("design.md", "aa"), tooLarge("huge.md")},
		},
		{
			name: "one over-size doc replaced by another",
			a:    []marotte.SpecDoc{tooLarge("huge.md")},
			b:    []marotte.SpecDoc{tooLarge("vast.md")},
		},
		{
			name: "the fields do not run together",
			a:    []marotte.SpecDoc{doc("ab", "cd")},
			b:    []marotte.SpecDoc{doc("a", "bcd")},
		},
		// An approval is part of this endpoint's answer, so a 304 over the docs
		// alone would serve a stale badge.
		{
			name: "an approval arrives",
			a:    []marotte.SpecDoc{doc("design.md", "aa")},
			b:    []marotte.SpecDoc{doc("design.md", "aa")},
			appB: approved("design", "aa", false),
		},
		{
			name: "an approval is re-recorded against a new version",
			a:    []marotte.SpecDoc{doc("design.md", "aa")},
			appA: approved("design", "old", true),
			b:    []marotte.SpecDoc{doc("design.md", "aa")},
			appB: approved("design", "aa", false),
		},
		// The derived Stale is in the digest because a document DISAPPEARING
		// removes its own contribution while flipping Stale, so without it the
		// two changes could cancel.
		{
			name: "the approved document goes away",
			a:    []marotte.SpecDoc{doc("design.md", "aa")},
			appA: approved("design", "aa", false),
			b:    nil,
			appB: approved("design", "aa", true),
		},
		{
			name: "one phase's approval moves to another",
			a:    []marotte.SpecDoc{doc("design.md", "aa")},
			appA: approved("design", "aa", false),
			b:    []marotte.SpecDoc{doc("design.md", "aa")},
			appB: approved("tasks", "aa", true),
		},
		// The badge renders WHEN a phase was approved, so re-approving the
		// version already approved moves nothing else in the digest.
		{
			name: "the same version is re-approved later",
			a:    []marotte.SpecDoc{doc("design.md", "aa")},
			appA: map[string]marotte.SpecApproval{"design": {Hash: "aa", At: time.Unix(1000, 0)}},
			b:    []marotte.SpecDoc{doc("design.md", "aa")},
			appB: map[string]marotte.SpecApproval{"design": {Hash: "aa", At: time.Unix(2000, 0)}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, other := specETag(tc.a, tc.appA), specETag(tc.b, tc.appB); got == other {
				t.Errorf("specETag agreed on both doc sets (%s), want a different digest", got)
			}
		})
	}

	same := []marotte.SpecDoc{doc("design.md", "aa"), tooLarge("huge.md")}
	sameApp := approved("design", "aa", false)
	if got, other := specETag(same, sameApp), specETag(same, sameApp); got != other {
		t.Errorf("specETag(%v) = %s then %s, want one digest", same, got, other)
	}
	// A map's iteration order is random, so a digest built by ranging it would
	// move between two calls over the same input. Several phases is what makes
	// that observable at all.
	several := map[string]marotte.SpecApproval{
		"requirements": {Hash: "r1"},
		"design":       {Hash: "d1"},
		"tasks":        {Hash: "t1", Stale: true},
	}
	first := specETag(same, several)
	for range 20 {
		if got := specETag(same, several); got != first {
			t.Fatalf("specETag over three approvals = %s then %s, want one digest", first, got)
		}
	}
}
