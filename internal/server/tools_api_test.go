package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/cplieger/toolbelt/v3"
)

type switchableTools struct {
	engine *toolbelt.Engine
	down   error
	mu     sync.Mutex
}

func (s *switchableTools) Engine() (*toolbelt.Engine, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.engine == nil {
		return nil, s.down
	}
	return s.engine, nil
}

func (s *switchableTools) bringUp(e *toolbelt.Engine) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.engine = e
}

func serveTools(t *testing.T, api http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, http.NoBody))
	return rec
}

func TestToolsAPI_SaysWhyWhileTheEngineIsDown(t *testing.T) {
	api := &toolsAPI{src: &switchableTools{down: errors.New("tools.json is invalid: manifest version 1, want 2")}}

	rec := serveTools(t, api, "/api/tools")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /api/tools with the engine down = %d, want 503", rec.Code)
	}
	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if body.Code != errCodeToolsUnavailable || body.Error != "tools.json is invalid: manifest version 1, want 2" {
		t.Errorf("GET /api/tools body = %+v, want code %q and the source's reason", body, errCodeToolsUnavailable)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control on the refusal = %q, want no-store", got)
	}
}

func TestToolsAPI_ServesAnEngineThatCameUpAfterTheRoutesWereMounted(t *testing.T) {
	src := &switchableTools{down: errors.New("down")}
	api := &toolsAPI{src: src}
	if rec := serveTools(t, api, "/api/tools"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("Setup: GET /api/tools with the engine down = %d, want 503", rec.Code)
	}
	dir := t.TempDir()
	engine, err := toolbelt.New(&toolbelt.Config{ConfigDir: dir, ToolsDir: filepath.Join(dir, "tools")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(engine.Close)

	src.bringUp(engine)
	rec := serveTools(t, api, "/api/tools")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/tools after the engine came up = %d %s, want 200", rec.Code, rec.Body.String())
	}
	var inv toolbelt.Inventory
	if err := json.Unmarshal(rec.Body.Bytes(), &inv); err != nil {
		t.Errorf("GET /api/tools body %q is not an inventory: %v", rec.Body.String(), err)
	}
}
