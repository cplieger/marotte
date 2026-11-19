package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/go/httpserver"
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

// httpapi writes its error bodies with its own httpserver major, so a toolbelt on another major
// than the access log's reads a context key Logging never set.
func TestToolsAPI_AnErrorBodyServedThroughTheMiddlewareStackCarriesTheRequestID(t *testing.T) {
	dir := t.TempDir()
	engine, err := toolbelt.New(&toolbelt.Config{ConfigDir: dir, ToolsDir: filepath.Join(dir, "tools")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(engine.Close)
	mux := http.NewServeMux()
	api := &toolsAPI{src: &switchableTools{engine: engine}}
	mux.Handle(toolsAPIPrefix+"/", api)
	idem := newIdempotencyCache(idempotencyTTL)
	t.Cleanup(idem.stop)
	h := httpserver.Chain(mux, (&Server{}).middlewareStack(baseCSPPolicy, idem)...)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, toolsAPIPrefix+"/jobs/no-such-job/cancel", http.NoBody))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST cancel of an unknown job = %d %s, want 404", rec.Code, rec.Body.String())
	}
	var body httpserver.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	logged := rec.Header().Get(httpserver.HeaderRequestID)
	if logged == "" {
		t.Fatalf("the response carries no %s header, so the access log minted no id", httpserver.HeaderRequestID)
	}
	if body.RequestID != logged {
		t.Errorf("error body request_id = %q, want the access log's %q (body %s)", body.RequestID, logged, strings.TrimSpace(rec.Body.String()))
	}
}

// aptRefusalHere is the reason toolbelt gives an apt install on this host. It skips where apt can
// install, or where there is no apt-get.
func aptRefusalHere(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("apt-get"); err != nil {
		t.Skip("no apt-get here, so this is not a Debian-derived host")
	}
	reason := toolbelt.AptUnavailableReason()
	if reason == "" {
		t.Skip("root on a writable root filesystem, where apt can install")
	}
	return reason
}

func TestToolsAPI_AnAptRowWhereAptCannotInstallSaysWhy(t *testing.T) {
	want := aptRefusalHere(t)
	dir := t.TempDir()
	engine, err := toolbelt.New(&toolbelt.Config{ConfigDir: dir, ToolsDir: filepath.Join(dir, "tools")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(engine.Close)
	api := &toolsAPI{src: &switchableTools{engine: engine}}
	send := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		api.ServeHTTP(rec, req)
		return rec
	}
	// A recorded version: toolbelt v3.16.0-dev.7 records no last_error when a versionless row's resolve fails.
	if rec := send(http.MethodPost, toolsAPIPrefix, `{"name":"sl","source":"apt:sl","version":"5.02-1","disabled":true}`); rec.Code != http.StatusAccepted {
		t.Fatalf("Setup: POST the apt:sl template = %d %s, want 202", rec.Code, rec.Body.String())
	}

	rec := send(http.MethodPatch, toolsAPIPrefix+"/sl", `{"disabled":false}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("PATCH sl enabled = %d %s, want 202", rec.Code, rec.Body.String())
	}
	var reply struct {
		Job *toolbelt.Job `json:"job"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil || reply.Job == nil {
		t.Fatalf("PATCH sl reply %q carries no job (decode error %v)", rec.Body.String(), err)
	}
	if _, err := engine.Wait(t.Context(), reply.Job.ID); err != nil {
		t.Fatalf("wait for the install job: %v", err)
	}
	var inv toolbelt.Inventory
	if err := json.Unmarshal(serveTools(t, api, toolsAPIPrefix).Body.Bytes(), &inv); err != nil {
		t.Fatalf("decode the inventory: %v", err)
	}
	i := slices.IndexFunc(inv.Tools, func(r toolbelt.ToolInfo) bool { return r.Name == "sl" })
	if i < 0 {
		t.Fatalf("the inventory has no sl row: %+v", inv.Tools)
	}
	if got := inv.Tools[i].LastError; got != want {
		t.Errorf("sl row last_error = %q, want %q", got, want)
	}
}
