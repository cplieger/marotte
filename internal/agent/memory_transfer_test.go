package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// serveMemoryRoute answers req through the routes the Memories tab is registered on.
func serveMemoryRoute(h *Runtime, req *http.Request) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	h.config.registerMemoryRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// comingSoon is the refusal kiro-cli 3.2.1 sends for all three verbs outside the import experiment, as captured live.
func comingSoon() *marotte.RPCError {
	return &marotte.RPCError{
		Code: -32000, Message: "Memory import and export are coming soon.",
		Data: json.RawMessage(`{"errorType":"MemoryImportComingSoonError"}`),
	}
}

func TestMemoryImportPrompt_ComingSoonIs409(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	br.setCallRPCErr(methodKiroMemoryGetImportPrompt, comingSoon())

	rec := serveMemoryRoute(h, httptest.NewRequest(http.MethodGet, "/api/memory/import-prompt", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("GET /api/memory/import-prompt = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Code != "coming_soon" {
		t.Errorf("body = %s, want code coming_soon", rec.Body.String())
	}
}

func TestMemoryImportPrompt_AnswersThePromptWithoutASession(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	const prompt = "Export every memory you hold about me as JSON Lines.\nOne object per line."
	br.setCallResult(methodKiroMemoryGetImportPrompt, json.RawMessage(`{"prompt":"Export every memory you hold about me as JSON Lines.\nOne object per line."}`))

	rec := serveMemoryRoute(h, httptest.NewRequest(http.MethodGet, "/api/memory/import-prompt", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/memory/import-prompt = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Prompt != prompt {
		t.Errorf("body = %s, want the prompt KAS answered", rec.Body.String())
	}
	params := br.paramsFor(methodKiroMemoryGetImportPrompt)
	if params == nil {
		t.Fatal("getImportPrompt never reached the engine")
	}
	if _, has := params["sessionId"]; has {
		t.Error("getImportPrompt carries a sessionId; the gate must read the connection's feature config")
	}
}

func TestMemoryImport_SendsTheBodyAsContentAndReportsPerLine(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	// The answer shape of kiro-cli 3.2.1's runJsonlImport: per-line records in all four lists.
	br.setCallResult(methodKiroMemoryImport, json.RawMessage(`{"totalLines":4,`+
		`"added":[{"line":1,"id":"m1","memoryType":"SEMANTIC","scope":"Global","title":"tabs"}],`+
		`"duplicates":[{"line":2,"title":"build","scope":"Global","duplicateOfId":"m0","duplicateOfTitle":"build"}],`+
		`"skipped":[{"line":3,"reason":"validation","detail":"title: title is empty"}],`+
		`"failed":[{"line":4,"reason":"parse_error","detail":"line is not valid JSON"}]}`))
	before := len(eventsOfType(h, marotte.EventMemoriesChanged))
	content := "{\"memoryType\":\"SEMANTIC\",\"scope\":\"Global\",\"title\":\"tabs\",\"summary\":\"é\",\"content\":\"c\"}\r\n{not json}\n"

	req := httptest.NewRequest(http.MethodPost, "/api/memory/import", strings.NewReader(content))
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	rec := serveMemoryRoute(h, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/memory/import = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	params := br.paramsFor(methodKiroMemoryImport)
	if params["content"] != content {
		t.Errorf("content = %q, want the request body byte for byte", params["content"])
	}
	if _, has := params["sessionId"]; has {
		t.Error("import carries a sessionId")
	}
	var got memoryImportReport
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Added != 1 || got.Duplicates != 1 {
		t.Errorf("report = %+v, want 1 added, 1 duplicate", got)
	}
	if len(got.Skipped) != 1 || got.Skipped[0] != (memoryImportLine{Line: 3, Reason: "validation", Detail: "title: title is empty"}) {
		t.Errorf("skipped = %+v, want line 3 validation with its detail", got.Skipped)
	}
	if len(got.Failed) != 1 || got.Failed[0] != (memoryImportLine{Line: 4, Reason: "parse_error", Detail: "line is not valid JSON"}) {
		t.Errorf("failed = %+v, want line 4 parse_error with its detail", got.Failed)
	}
	awaitEventCount(t, h, marotte.EventMemoriesChanged, before+1)
}

func TestMemoryImport_RefusesAnOversizeBodyBeforeTheWire(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })

	req := httptest.NewRequest(http.MethodPost, "/api/memory/import", strings.NewReader(strings.Repeat("x", 11<<20)))
	req.Header.Set("Content-Type", "text/plain")
	rec := serveMemoryRoute(h, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("POST of 11 MiB = %d, want 413 (%s)", rec.Code, rec.Body.String())
	}
	if br.paramsFor(methodKiroMemoryImport) != nil {
		t.Error("an oversize import reached the engine")
	}
}

func TestMemoryImport_KASContentRefusalIs400WithItsText(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	// The wire shape KAS's ACP layer gives a thrown InvalidMemoryCommandError (badImportContent).
	br.setCallRPCErr(methodKiroMemoryImport, &marotte.RPCError{
		Code: -32603, Message: "Internal error", Data: json.RawMessage(`{"details":"import content is empty."}`),
	})

	rec := serveMemoryRoute(h, httptest.NewRequest(http.MethodPost, "/api/memory/import", strings.NewReader("  \n")))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "import content is empty.") {
		t.Errorf("POST = %d %s, want 400 with KAS's text", rec.Code, rec.Body.String())
	}
}

func TestMemoryImport_UnreadableReportStillRefetches(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	// KAS answered, so lines may have landed, but added is a count where the report carries records.
	br.setCallResult(methodKiroMemoryImport, json.RawMessage(`{"added":3}`))
	before := len(eventsOfType(h, marotte.EventMemoriesChanged))

	rec := serveMemoryRoute(h, httptest.NewRequest(http.MethodPost, "/api/memory/import", strings.NewReader("{}\n")))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("POST = %d %s, want 502 for an unreadable report", rec.Code, rec.Body.String())
	}
	awaitEventCount(t, h, marotte.EventMemoriesChanged, before+1)
	if got := len(eventsOfType(h, marotte.EventMemoriesChanged)); got != before+1 {
		t.Errorf("memories_changed events = %d, want exactly %d", got, before+1)
	}
}

func TestMemoryExport_NoContentIs502(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	br.setCallResult(methodKiroMemoryExport, json.RawMessage(`{"format":"json"}`))

	rec := serveMemoryRoute(h, httptest.NewRequest(http.MethodGet, "/api/memory/export", nil))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("GET /api/memory/export = %d %s, want 502 for a reply with no content", rec.Code, rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != "" {
		t.Errorf("Content-Disposition = %q, want none on a failure", cd)
	}
}

func TestMemoryExport_DownloadsKASContentVerbatim(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	doc := "{\n  \"format\": \"kiro-memory\",\n  \"version\": 1,\n  \"recordCount\": 0,\n  \"records\": []\n}"
	answer, err := json.Marshal(map[string]any{"format": "json", "content": doc, "recordCount": 0})
	if err != nil {
		t.Fatal(err)
	}
	br.setCallResult(methodKiroMemoryExport, answer)

	rec := serveMemoryRoute(h, httptest.NewRequest(http.MethodGet, "/api/memory/export", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/memory/export = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != doc {
		t.Errorf("body = %q, want KAS's document verbatim", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	disposition := regexp.MustCompile(`^attachment; filename="kiro-memories-\d{4}-\d{2}-\d{2}\.json"$`)
	if cd := rec.Header().Get("Content-Disposition"); !disposition.MatchString(cd) {
		t.Errorf("Content-Disposition = %q, want an attachment kiro-memories-<date>.json", cd)
	}
}
