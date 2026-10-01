package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/toolbelt/v3"
)

// TestHandleToolReconcile_RefusesWithNoEngine pins the guard rather than the
// happy path: with no engine wired the handler would otherwise nil-dereference
// on the first request, and /api/tools/reconcile is registered unconditionally
// while the toolbelt projection beside it is not.
func TestHandleToolReconcile_RefusesWithNoEngine(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/api/tools/reconcile", http.NoBody)
	rec := httptest.NewRecorder()

	s.handleToolReconcile(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if got := strings.TrimSpace(rec.Body.String()); !strings.Contains(got, "tools engine") {
		t.Errorf("body = %q, want a reason naming the engine", got)
	}
}

// reconcileEngine builds an engine over scratch dirs and returns it with its
// config dir. The seed is what makes a reconcile enqueue at all: an empty
// manifest with no state row is the engine's "nothing to converge" answer.
func reconcileEngine(t *testing.T) (*toolbelt.Engine, string) {
	t.Helper()
	dir := t.TempDir()
	e, err := toolbelt.New(&toolbelt.Config{
		ConfigDir: dir,
		ToolsDir:  t.TempDir(),
		Seed:      toolbelt.DefaultSeed(),
	})
	if err != nil {
		t.Fatalf("toolbelt.New: %v", err)
	}
	t.Cleanup(e.Close)
	return e, dir
}

func postReconcile(t *testing.T, s *Server) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/tools/reconcile", http.NoBody)
	rec := httptest.NewRecorder()
	s.handleToolReconcile(rec, req)
	return rec
}

func TestHandleToolReconcile_EnqueuesOverARealEngine(t *testing.T) {
	e, _ := reconcileEngine(t)
	s := &Server{tools: e}

	rec := postReconcile(t, s)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var body struct {
		Job *struct {
			ID string `json:"id"`
		} `json:"job"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	if body.Job == nil || body.Job.ID == "" {
		t.Errorf("job = %+v, want an enqueued job carrying an id", body.Job)
	}
}

// The 400 arm, pinned on the STATUS and on the body carrying a cause rather
// than on the upstream error text, so a toolbelt bump cannot redden it for no
// defect. The manifest is broken AFTER New(): it is re-read per operation, so
// the engine constructs fine and only Reconcile refuses.
func TestHandleToolReconcile_RefusesABrokenManifestWithItsCause(t *testing.T) {
	e, dir := reconcileEngine(t)
	if err := os.WriteFile(filepath.Join(dir, "tools.json"), []byte("{"), 0o600); err != nil {
		t.Fatalf("break tools.json: %v", err)
	}
	s := &Server{tools: e}

	rec := postReconcile(t, s)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	if strings.TrimSpace(body.Error) == "" {
		t.Errorf("body = %q, want a reason the caller can act on", rec.Body.String())
	}
}
