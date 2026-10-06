package agent

// The fake utility bridge is seeded with one canned _kiro/knowledge result per method.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func TestParseKnowledgeResult(t *testing.T) {
	t.Run("ShowEntries", func(t *testing.T) {
		raw := `{"success":true,"entries":[` +
			`{"name":"docs","id":"abc12345","description":"d","item_count":7,"path":"/w/docs"},` +
			`{"name":"big","id":"op1","description":"","item_count":0,"items_display":"42%","indexing":true,"path":"/w/big"}]}`
		res, err := parseKnowledgeResult(json.RawMessage(raw))
		if err != nil {
			t.Fatalf("parseKnowledgeResult: %v", err)
		}
		if !res.Success || len(res.Entries) != 2 {
			t.Fatalf("res = %+v", res)
		}
		if res.Entries[1].Indexing != true || res.Entries[1].ItemsDisplay != "42%" {
			t.Errorf("active-op entry = %+v", res.Entries[1])
		}
	})

	t.Run("Message", func(t *testing.T) {
		res, err := parseKnowledgeResult(json.RawMessage(`{"success":true,"message":"Removed 'docs'"}`))
		if err != nil || !res.Success || res.Message != "Removed 'docs'" {
			t.Errorf("res = %+v err = %v", res, err)
		}
	})

	t.Run("Failure", func(t *testing.T) {
		res, err := parseKnowledgeResult(json.RawMessage(`{"success":false,"message":"Entry not found: x"}`))
		if err != nil || res.Success {
			t.Errorf("res = %+v err = %v", res, err)
		}
	})

	t.Run("Empty", func(t *testing.T) {
		if _, err := parseKnowledgeResult(nil); err == nil {
			t.Fatal("expected error for empty result")
		}
	})

	t.Run("Malformed", func(t *testing.T) {
		if _, err := parseKnowledgeResult(json.RawMessage(`{nope`)); err == nil {
			t.Fatal("expected error for malformed JSON")
		}
	})
}

func TestResolveKnowledgePath(t *testing.T) {
	h, _, _ := newTestHub() // workDir = /tmp/work
	if got := h.config.resolveKnowledgePath("docs"); got != "/tmp/work/docs" {
		t.Errorf("relative resolve = %q, want /tmp/work/docs", got)
	}
	if got := h.config.resolveKnowledgePath("a/../b"); got != "/tmp/work/b" {
		t.Errorf("relative clean = %q, want /tmp/work/b", got)
	}
	if got := h.config.resolveKnowledgePath("/abs/path"); got != "/abs/path" {
		t.Errorf("absolute passthrough = %q, want /abs/path", got)
	}
}

// seedKnowledge wires a canned _kiro/knowledge result onto the shared fake so
// the utility-bridge call the handlers make returns it.
func seedKnowledge(br *fakeBridge, result string) {
	br.callResults = map[string]json.RawMessage{methodKiroKnowledge: json.RawMessage(result)}
}

func TestHandleKnowledgeList_OK(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(h.stopUtilityBridge)
	seedKnowledge(br, `{"success":true,"entries":[`+
		`{"name":"docs","id":"abc12345","item_count":7,"path":"/w/docs"},`+
		`{"name":"big","id":"op1","item_count":0,"items_display":"42%","indexing":true}]}`)

	rec := httptest.NewRecorder()
	h.config.handleKnowledgeList(rec, httptest.NewRequest(http.MethodGet, "/api/knowledge", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var body knowledgeListResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Contexts) != 2 {
		t.Fatalf("contexts = %+v", body.Contexts)
	}
	if body.Contexts[0].Name != "docs" || body.Contexts[0].ItemCount != 7 {
		t.Errorf("context[0] = %+v", body.Contexts[0])
	}
	if !body.Contexts[1].Indexing || body.Contexts[1].ItemsDisplay != "42%" {
		t.Errorf("context[1] = %+v (indexing/progress not preserved)", body.Contexts[1])
	}
	// show must omit sessionId (global store).
	params := br.paramsFor(methodKiroKnowledge)
	if params["subcommand"] != "show" {
		t.Errorf("subcommand = %v, want show", params["subcommand"])
	}
	if _, hasSession := params["sessionId"]; hasSession {
		t.Error("knowledge show must NOT carry a sessionId (targets the global default store)")
	}
}

func TestHandleKnowledgeList_BridgeError(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(h.stopUtilityBridge)
	seedKnowledge(br, `{"success":false,"message":"boom"}`)

	rec := httptest.NewRecorder()
	h.config.handleKnowledgeList(rec, httptest.NewRequest(http.MethodGet, "/api/knowledge", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502 (%s)", rec.Code, rec.Body.String())
	}
}

func TestHandleKnowledgeAdd_MissingPath(t *testing.T) {
	h, _, _ := newTestHub()
	rec := postJSON(h.config.handleKnowledgeAdd, "/api/knowledge", `{"path":""}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

func TestHandleKnowledgeAdd_OK_ResolvesPathAndDerivesName(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(h.stopUtilityBridge)
	seedKnowledge(br, `{"success":true,"message":"Indexing 'docs' in background\nFiles: 3"}`)

	rec := postJSON(h.config.handleKnowledgeAdd, "/api/knowledge", `{"path":"docs"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	params := br.paramsFor(methodKiroKnowledge)
	if params["subcommand"] != "add" {
		t.Errorf("subcommand = %v, want add", params["subcommand"])
	}
	if params["name"] != "docs" {
		t.Errorf("name = %v, want docs (derived from base)", params["name"])
	}
	if params["path"] != "/tmp/work/docs" {
		t.Errorf("path = %v, want /tmp/work/docs (resolved against workDir)", params["path"])
	}
	if _, hasSession := params["sessionId"]; hasSession {
		t.Error("knowledge add must NOT carry a sessionId")
	}
}

func TestHandleKnowledgeAdd_ExplicitName(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(h.stopUtilityBridge)
	seedKnowledge(br, `{"success":true,"message":"Indexing 'Project docs' in background"}`)

	rec := postJSON(h.config.handleKnowledgeAdd, "/api/knowledge", `{"path":"/abs/docs","name":"Project docs"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	params := br.paramsFor(methodKiroKnowledge)
	if params["name"] != "Project docs" || params["path"] != "/abs/docs" {
		t.Errorf("params = %+v, want name='Project docs' path=/abs/docs", params)
	}
}

func TestHandleKnowledgeAdd_Failure(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(h.stopUtilityBridge)
	seedKnowledge(br, `{"success":false,"message":"Usage: /knowledge add <name> <path>"}`)

	rec := postJSON(h.config.handleKnowledgeAdd, "/api/knowledge", `{"path":"docs"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestHandleKnowledgeRemove_MissingName(t *testing.T) {
	h, _, _ := newTestHub()
	req := httptest.NewRequest(http.MethodDelete, "/api/knowledge/", nil)
	rec := httptest.NewRecorder()
	h.config.handleKnowledgeRemove(rec, req) // no path value set
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

func TestHandleKnowledgeRemove_OK(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(h.stopUtilityBridge)
	seedKnowledge(br, `{"success":true,"message":"Removed 'docs'"}`)

	req := httptest.NewRequest(http.MethodDelete, "/api/knowledge/docs", nil)
	req.SetPathValue("name", "docs")
	rec := httptest.NewRecorder()
	h.config.handleKnowledgeRemove(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	params := br.paramsFor(methodKiroKnowledge)
	if params["subcommand"] != "remove" || params["target"] != "docs" {
		t.Errorf("params = %+v, want subcommand=remove target=docs", params)
	}
	if _, hasSession := params["sessionId"]; hasSession {
		t.Error("knowledge remove must NOT carry a sessionId")
	}
}

func TestHandleKnowledgeRemove_NotFound(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(h.stopUtilityBridge)
	seedKnowledge(br, `{"success":false,"message":"Entry not found: gone"}`)

	req := httptest.NewRequest(http.MethodDelete, "/api/knowledge/gone", nil)
	req.SetPathValue("name", "gone")
	rec := httptest.NewRecorder()
	h.config.handleKnowledgeRemove(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}

// TestCleanKnowledgeMsg pins KAS's own message whenever there is one, the sentinel only for an empty one.
func TestCleanKnowledgeMsg(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "an empty message falls back", in: "", want: "knowledge operation failed"},
		{name: "whitespace only falls back", in: "  \n\t ", want: "knowledge operation failed"},
		{name: "KAS's own message survives", in: "path does not exist", want: "path does not exist"},
		{name: "and is trimmed", in: "  already indexed\n", want: "already indexed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cleanKnowledgeMsg(c.in); got != c.want {
				t.Errorf("cleanKnowledgeMsg(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestRegisterKnowledgeRoutes_RefusedMethodsCarryAllow pins 405 + Allow under the /api/ subtree fallback stand-in.
func TestRegisterKnowledgeRoutes_RefusedMethodsCarryAllow(t *testing.T) {
	h, _, _ := newTestHub()
	const fallbackStatus = 299 // a status no marotte handler produces

	mux := http.NewServeMux()
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(fallbackStatus)
	})
	h.config.registerKnowledgeRoutes(mux)

	tests := map[string]struct {
		method    string
		path      string
		wantAllow string
	}{
		"the collection refuses PUT":        {http.MethodPut, "/api/knowledge", "GET, POST, DELETE"},
		"the collection refuses HEAD":       {http.MethodHead, "/api/knowledge", "GET, POST, DELETE"},
		"one base refuses GET":              {http.MethodGet, "/api/knowledge/docs", "DELETE"},
		"the re-index route refuses GET":    {http.MethodGet, "/api/knowledge/docs/reindex", "POST"},
		"the re-index route refuses DELETE": {http.MethodDelete, "/api/knowledge/docs/reindex", "POST"},
		"the cancel route refuses GET":      {http.MethodGet, "/api/knowledge/docs/cancel", "POST"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			if rec.Code == fallbackStatus {
				t.Fatalf("%s %s reached the /api/ fallback, want the route's own 405", tc.method, tc.path)
			}
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s = %d, want 405 (%s)", tc.method, tc.path, rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Allow"); got != tc.wantAllow {
				t.Errorf("Allow = %q, want %q", got, tc.wantAllow)
			}
		})
	}
}

// TestRegisterKnowledgeRoutes_TheDispatchedMethodsReachTheirHandler is the control: a wall would 405 everything.
func TestRegisterKnowledgeRoutes_TheDispatchedMethodsReachTheirHandler(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(h.stopUtilityBridge)
	seedKnowledge(br, `{"success":true,"entries":[{"name":"docs","id":"a1","item_count":1,"path":"/w/docs"}]}`)

	tests := map[string]struct {
		method string
		path   string
	}{
		"GET lists the collection": {http.MethodGet, "/api/knowledge"},
		"DELETE removes one base":  {http.MethodDelete, "/api/knowledge/docs"},
		"POST re-indexes one base": {http.MethodPost, "/api/knowledge/docs/reindex"},
		"DELETE clears every base": {http.MethodDelete, "/api/knowledge"},
		"POST stops an index":      {http.MethodPost, "/api/knowledge/docs/cancel"},
	}
	mux := http.NewServeMux()
	h.config.registerKnowledgeRoutes(mux)
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			if rec.Code == http.StatusMethodNotAllowed {
				t.Errorf("%s %s = 405, want its own handler to answer", tc.method, tc.path)
			}
		})
	}
}

// TestHandleKnowledgeReindex_ResolvesTheNameToItsPath pins that KAS's `update` matches on path, so the server looks it up.
func TestHandleKnowledgeReindex_ResolvesTheNameToItsPath(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(h.stopUtilityBridge)
	seedKnowledge(br, `{"success":true,"entries":[`+
		`{"name":"other","id":"b2","item_count":2,"path":"/w/other"},`+
		`{"name":"docs","id":"a1","item_count":7,"path":"/w/docs"}]}`)

	req := httptest.NewRequest(http.MethodPost, "/api/knowledge/docs/reindex", nil)
	req.SetPathValue("name", "docs")
	rec := httptest.NewRecorder()
	h.config.handleKnowledgeReindex(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	// paramsFor keeps the latest call: the update the show fed.
	params := br.paramsFor(methodKiroKnowledge)
	if params["subcommand"] != "update" {
		t.Errorf("subcommand = %v, want update", params["subcommand"])
	}
	if params["path"] != "/w/docs" {
		t.Errorf("path = %v, want /w/docs (the entry's own sourcePath)", params["path"])
	}
	if _, hasName := params["name"]; hasName {
		t.Error("update carries no name: KAS keys it on the path alone")
	}
	if _, hasSession := params["sessionId"]; hasSession {
		t.Error("knowledge update must NOT carry a sessionId")
	}
}

// TestHandleKnowledgeReindex_RefusesANameNoSettledBaseHolds asserts the RPC count, so the 404 is never KAS's.
func TestHandleKnowledgeReindex_RefusesANameNoSettledBaseHolds(t *testing.T) {
	tests := map[string]struct {
		entries string
		name    string
	}{
		"no entry carries the name": {`[{"name":"other","id":"b2","path":"/w/other"}]`, "docs"},
		"the entry is still indexing": {
			`[{"name":"docs","id":"a1","items_display":"42%","indexing":true,"path":"/w/docs"}]`, "docs",
		},
		"the entry carries no path": {`[{"name":"docs","id":"a1"}]`, "docs"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			t.Cleanup(h.stopUtilityBridge)
			seedKnowledge(br, `{"success":true,"entries":`+tc.entries+`}`)

			req := httptest.NewRequest(http.MethodPost, "/api/knowledge/"+tc.name+"/reindex", nil)
			req.SetPathValue("name", tc.name)
			rec := httptest.NewRecorder()
			h.config.handleKnowledgeReindex(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("code = %d, want 404 (%s)", rec.Code, rec.Body.String())
			}
			var knowledgeCalls int
			for _, m := range br.callLog() {
				if m == methodKiroKnowledge {
					knowledgeCalls++
				}
			}
			if knowledgeCalls != 1 {
				t.Errorf("_kiro/knowledge calls = %d, want 1 (the show only, no update)", knowledgeCalls)
			}
		})
	}
}

func TestHandleKnowledgeReindex_MissingName(t *testing.T) {
	h, _, _ := newTestHub()
	rec := httptest.NewRecorder()
	// No path value, as a bare /api/knowledge//reindex produces.
	h.config.handleKnowledgeReindex(rec, httptest.NewRequest(http.MethodPost, "/api/knowledge//reindex", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

func TestHandleKnowledgeReindex_BridgeError(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(h.stopUtilityBridge)
	br.setCallErr(methodKiroKnowledge, errKnowledgeProbe)

	req := httptest.NewRequest(http.MethodPost, "/api/knowledge/docs/reindex", nil)
	req.SetPathValue("name", "docs")
	rec := httptest.NewRecorder()
	h.config.handleKnowledgeReindex(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502 (%s)", rec.Code, rec.Body.String())
	}
}

// TestHandleKnowledgeCancel_SendsTheInFlightEntrysOperationID pins that a base and its re-index share a
// name, and only the in-flight id is cancellable.
func TestHandleKnowledgeCancel_SendsTheInFlightEntrysOperationID(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(h.stopUtilityBridge)
	seedKnowledge(br, `{"success":true,"message":"Cancelled","entries":[`+
		`{"name":"docs","id":"ctx12345","item_count":7,"path":"/w/docs"},`+
		`{"name":"docs","id":"op987654","items_display":"42%","indexing":true}]}`)

	req := httptest.NewRequest(http.MethodPost, "/api/knowledge/docs/cancel", nil)
	req.SetPathValue("name", "docs")
	rec := httptest.NewRecorder()
	h.config.handleKnowledgeCancel(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	params := br.paramsFor(methodKiroKnowledge)
	if params["subcommand"] != "cancel" || params["operationId"] != "op987654" {
		t.Errorf("params = %+v, want subcommand=cancel operationId=op987654", params)
	}
	if _, hasName := params["name"]; hasName {
		t.Error("cancel carries no name: KAS keys it on the operation id alone")
	}
	if _, hasSession := params["sessionId"]; hasSession {
		t.Error("knowledge cancel must NOT carry a sessionId")
	}
}

// TestHandleKnowledgeCancel_RefusesANameWithNothingIndexing asserts the RPC count, so the 404 is never an empty-id cancel.
func TestHandleKnowledgeCancel_RefusesANameWithNothingIndexing(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(h.stopUtilityBridge)
	seedKnowledge(br, `{"success":true,"entries":[{"name":"docs","id":"ctx12345","item_count":7,"path":"/w/docs"}]}`)

	req := httptest.NewRequest(http.MethodPost, "/api/knowledge/docs/cancel", nil)
	req.SetPathValue("name", "docs")
	rec := httptest.NewRecorder()
	h.config.handleKnowledgeCancel(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
	var knowledgeCalls int
	for _, m := range br.callLog() {
		if m == methodKiroKnowledge {
			knowledgeCalls++
		}
	}
	if knowledgeCalls != 1 {
		t.Errorf("_kiro/knowledge calls = %d, want 1 (the show only, no cancel)", knowledgeCalls)
	}
}

// TestHandleKnowledgeCancel_KASRefusalIs404 covers the operation finishing between show and cancel.
func TestHandleKnowledgeCancel_KASRefusalIs404(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(h.stopUtilityBridge)
	br.onCall = func(method string, params map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method != methodKiroKnowledge {
			return nil, nil, false
		}
		if params["subcommand"] == "cancel" {
			return json.RawMessage(`{"success":false,"message":"Operation op987654 not found"}`), nil, true
		}
		return json.RawMessage(`{"success":true,"entries":[` +
			`{"name":"docs","id":"op987654","items_display":"42%","indexing":true}]}`), nil, true
	}

	req := httptest.NewRequest(http.MethodPost, "/api/knowledge/docs/cancel", nil)
	req.SetPathValue("name", "docs")
	rec := httptest.NewRecorder()
	h.config.handleKnowledgeCancel(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Operation op987654 not found") {
		t.Errorf("body = %s, want KAS's refusal", rec.Body.String())
	}
}

func TestHandleKnowledgeClear_SendsClearWithoutASession(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(h.stopUtilityBridge)
	seedKnowledge(br, `{"success":true,"message":"Cleared 2 knowledge bases"}`)

	rec := httptest.NewRecorder()
	h.config.handleKnowledgeClear(rec, httptest.NewRequest(http.MethodDelete, "/api/knowledge", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	params := br.paramsFor(methodKiroKnowledge)
	if params["subcommand"] != "clear" {
		t.Errorf("subcommand = %v, want clear", params["subcommand"])
	}
	if _, hasSession := params["sessionId"]; hasSession {
		t.Error("knowledge clear must NOT carry a sessionId")
	}
}

func TestHandleKnowledgeClear_RefusalIsAServerError(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(h.stopUtilityBridge)
	seedKnowledge(br, `{"success":false,"message":"store locked"}`)

	rec := httptest.NewRecorder()
	h.config.handleKnowledgeClear(rec, httptest.NewRequest(http.MethodDelete, "/api/knowledge", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500 (%s)", rec.Code, rec.Body.String())
	}
}

// errKnowledgeProbe stands in for a bridge fault, the one reindex failure reported as 502.
var errKnowledgeProbe = errors.New("bridge gone")

// TestKnowledgeNameUnaddressable pins the two refusals; any other free-text name is legal.
func TestKnowledgeNameUnaddressable(t *testing.T) {
	tests := map[string]struct {
		name    string
		wantBad bool
	}{
		"a plain name":                 {"docs", false},
		"a dotted name":                {"my.base", false},
		"a name with a space":          {"Project docs", false},
		"a leading dot":                {".kiro", false},
		"three dots":                   {"...", false},
		"a name that is a dot":         {".", true},
		"a name that is two dots":      {"..", true},
		"a name holding a slash":       {"a/b", true},
		"a name that is just a slash":  {"/", true},
		"a name with a trailing slash": {"docs/", true},
		// The routes trim the name, so one that does not survive its own trim is unaddressable.
		"a name with a trailing space": {"docs ", true},
		"a name with a leading space":  {" docs", true},
		"a name that is only spaces":   {"   ", true},
	}
	for desc, tc := range tests {
		t.Run(desc, func(t *testing.T) {
			reason := knowledgeNameUnaddressable(tc.name)
			if bad := reason != ""; bad != tc.wantBad {
				t.Fatalf("knowledgeNameUnaddressable(%q) = %q, want unaddressable = %v", tc.name, reason, tc.wantBad)
			}
			if tc.wantBad && !strings.Contains(reason, "knowledge base") {
				t.Errorf("reason = %q, want it to name what it is refusing", reason)
			}
		})
	}
}

// TestHandleKnowledgeAdd_RefusesAnUnaddressableName pins the refusal at add time.
func TestHandleKnowledgeAdd_RefusesAnUnaddressableName(t *testing.T) {
	tests := map[string]string{
		"a supplied name holding a slash": `{"path":"/abs/docs","name":"a/b"}`,
		"a supplied name that is a dot":   `{"path":"/abs/docs","name":"."}`,
		"a name derived from the root":    `{"path":"/"}`,
		// A supplied name is trimmed first; only a derived one can carry whitespace.
		"a name derived with a trailing space": `{"path":"/abs/docs /"}`,
		"a name derived with a leading space":  `{"path":"/abs/ docs"}`,
	}
	for desc, body := range tests {
		t.Run(desc, func(t *testing.T) {
			h, _, br := newTestHub()
			t.Cleanup(h.stopUtilityBridge)
			seedKnowledge(br, `{"success":true,"message":"Indexing in background"}`)

			rec := postJSON(h.config.handleKnowledgeAdd, "/api/knowledge", body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("code = %d, want 400 (%s)", rec.Code, rec.Body.String())
			}
			for _, m := range br.callLog() {
				if m == methodKiroKnowledge {
					t.Fatal("a refused name must not reach _kiro/knowledge")
				}
			}
		})
	}
}
