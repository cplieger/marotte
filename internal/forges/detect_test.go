package forges

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/slogx/capture"
)

const (
	detectPath  = "/api/forges/detect"
	detectToken = "tea-detect-secret"
	bothOptIns  = `,"plaintext_http":true,"private_addresses":true`
)

// giteaReads are the answers a Gitea instance gives the reads detection makes.
var giteaReads = map[string]string{
	"/api/v1/version":      `{"version":"1.27.0"}`,
	"/swagger.v1.json":     `{"swagger":"2.0","basePath":"/api/v1","info":{"title":"Gitea API","version":"1.27.0"},"paths":{}}`,
	"/api/v1/settings/api": `{"max_response_items":50}`,
}

// forgeInstance is a loopback instance answering the JSON body registered for
// each path and a plain 404 for any other, recording every request.
type forgeInstance struct {
	srv   *httptest.Server
	seen  []string
	auths []string
	mu    sync.Mutex
}

func newForgeInstance(t *testing.T, bodies map[string]string) *forgeInstance {
	t.Helper()
	inst := &forgeInstance{}
	inst.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inst.mu.Lock()
		inst.seen = append(inst.seen, r.Method+" "+r.URL.Path)
		inst.auths = append(inst.auths, r.Header.Get("Authorization"))
		inst.mu.Unlock()
		body, ok := bodies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(inst.srv.Close)
	return inst
}

func (inst *forgeInstance) requests() []string {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	return slices.Clone(inst.seen)
}

func (inst *forgeInstance) sawCredential(token string) bool {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	return slices.ContainsFunc(inst.auths, func(a string) bool { return strings.Contains(a, token) })
}

func detectRequest(webBase, optIns string) string {
	return `{"token":"` + detectToken + `","web_base_url":"` + webBase + `"` + optIns + `}`
}

func TestDetect_AnswersTheFamilyAndStoresNothing(t *testing.T) {
	inst := newForgeInstance(t, giteaReads)
	h := newConnectHarness(t, nil)

	rec := h.do(t, http.MethodPost, detectPath, detectRequest(inst.srv.URL, bothOptIns))
	if got := decodeBody(t, rec); rec.Code != http.StatusOK || got["kind"] != string(KindGitea) {
		t.Errorf("detect %s = %d %v, want 200 with kind gitea", inst.srv.URL, rec.Code, got)
	}
	if !inst.sawCredential(detectToken) {
		t.Errorf("no request to %s carried the body's token (requests %v)", inst.srv.URL, inst.requests())
	}
	h.assertNothingStored(t, MakeID(KindGitea, strings.TrimPrefix(inst.srv.URL, "http://")), inst.srv.URL)
	if _, err := os.Stat(filepath.Join(h.cfgDir, connectionsFileName)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stat %s after a detection = %v, want no record file", connectionsFileName, err)
	}
}

// An address every family's read refuses is the one case detection itself
// fails on, and its remedy is a corrected address.
func TestDetect_UndetectedIs422(t *testing.T) {
	inst := newForgeInstance(t, nil)
	h := newConnectHarness(t, nil)

	rec := h.do(t, http.MethodPost, detectPath, detectRequest(inst.srv.URL, bothOptIns))
	if got := decodeBody(t, rec); rec.Code != http.StatusUnprocessableEntity || got["code"] != forgeapi.CodeFamilyUndetected {
		t.Errorf("detect %s answering 404 everywhere = %d %v, want 422 coded %q",
			inst.srv.URL, rec.Code, got, forgeapi.CodeFamilyUndetected)
	}
	if len(inst.requests()) == 0 {
		t.Errorf("detect %s sent no request, want every family's read asked", inst.srv.URL)
	}
	h.assertNothingStored(t, MakeID(KindGitea, strings.TrimPrefix(inst.srv.URL, "http://")), inst.srv.URL)
}

// A loopback instance over plain HTTP needs both opt-ins, and the library's own
// refusal names the one that is missing before the token is sent anywhere.
func TestDetect_LoopbackNeedsBothOptIns(t *testing.T) {
	inst := newForgeInstance(t, giteaReads)
	h := newConnectHarness(t, nil)

	refusals := []struct{ optIns, code string }{
		{`,"plaintext_http":true`, forgeapi.CodePrivateAddressRefused},
		{`,"private_addresses":true`, forgeapi.CodePlaintextRefused},
	}
	for _, tc := range refusals {
		rec := h.do(t, http.MethodPost, detectPath, detectRequest(inst.srv.URL, tc.optIns))
		if got := decodeBody(t, rec); rec.Code != http.StatusBadRequest || got["code"] != tc.code {
			t.Errorf("detect %s with only %s = %d %v, want 400 coded %q", inst.srv.URL, tc.optIns, rec.Code, got, tc.code)
		}
	}
	if seen := inst.requests(); len(seen) != 0 {
		t.Errorf("the refused detections sent %v, want no request", seen)
	}

	rec := h.do(t, http.MethodPost, detectPath, detectRequest(inst.srv.URL, bothOptIns))
	if got := decodeBody(t, rec); rec.Code != http.StatusOK || got["kind"] != string(KindGitea) {
		t.Errorf("detect %s with both opt-ins = %d %v, want 200 with kind gitea", inst.srv.URL, rec.Code, got)
	}
}

// The connect that follows a detection addresses the instance by its origin,
// so a detection is refused for any address the connect could not take.
func TestDetect_WebBaseMustBeAnOrigin(t *testing.T) {
	wire := &pathWire{bodies: giteaReads}
	h := newConnectHarness(t, wire)

	for _, webBase := range []string{
		"",
		"forge.test",
		"https://forge.test/gitea",
		"https://alice@forge.test",
		"https://forge.test?x=1",
		"https://forge.test#top",
	} {
		rec := h.do(t, http.MethodPost, detectPath, detectRequest(webBase, ""))
		if got := decodeBody(t, rec); rec.Code != http.StatusBadRequest || got["code"] != codeWebBaseInvalid {
			t.Errorf("detect %q = %d %v, want 400 coded %q", webBase, rec.Code, got, codeWebBaseInvalid)
		}
	}
	if seen := wire.requests(); len(seen) != 0 {
		t.Errorf("the refused detections sent %v, want no request", seen)
	}
}

func TestDetect_NoTokenIsRefusedBeforeAnyRequest(t *testing.T) {
	wire := &pathWire{bodies: giteaReads}
	h := newConnectHarness(t, wire)

	rec := h.do(t, http.MethodPost, detectPath, `{"web_base_url":"https://forge.test"}`)
	if got := decodeBody(t, rec); rec.Code != http.StatusBadRequest || got["code"] != forgeapi.CodeAnonymousRefused {
		t.Errorf("detect with no token = %d %v, want 400 coded %q", rec.Code, got, forgeapi.CodeAnonymousRefused)
	}
	if seen := wire.requests(); len(seen) != 0 {
		t.Errorf("a detection with no token sent %v, want no request", seen)
	}
}

func TestDetectedKind_RoundTripsThroughItsFamily(t *testing.T) {
	for _, family := range []forgeapi.Family{forgeapi.FamilyGitHub, forgeapi.FamilyGitLab, forgeapi.FamilyGitea} {
		kind := detectedKind(family, "forge.example")
		if !kind.Valid() || kind.family() != family {
			t.Errorf("detectedKind(%v) = %q, whose family is %v; want a valid kind of family %v", family, kind, kind.family(), family)
		}
	}
}

// Codeberg is a Gitea instance under a kind of its own, so detection there
// answers the kind its own button connects under: one id for one instance.
func TestDetectedKind_CodebergKeepsItsOwnKind(t *testing.T) {
	if got := detectedKind(forgeapi.FamilyGitea, KindCodeberg.DefaultHost()); got != KindCodeberg {
		t.Errorf("detectedKind(gitea, %q) = %q, want %q", KindCodeberg.DefaultHost(), got, KindCodeberg)
	}
}

// rendered is every captured record as the production text handler writes it.
func rendered(t *testing.T, logs *capture.Recorder) []byte {
	t.Helper()
	var buf bytes.Buffer
	text := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	for _, r := range logs.Records() {
		if err := text.Handle(t.Context(), r); err != nil {
			t.Fatalf("Setup: rendering a captured record: %v", err)
		}
	}
	return buf.Bytes()
}

func TestDetect_TokenIsNeverLogged(t *testing.T) {
	logs := capture.Default(t)
	found := newForgeInstance(t, giteaReads)
	empty := newForgeInstance(t, nil)
	h := newConnectHarness(t, nil)

	for _, body := range []string{
		detectRequest(found.srv.URL, bothOptIns),
		detectRequest(empty.srv.URL, bothOptIns),
		detectRequest(found.srv.URL, `,"plaintext_http":true`),
	} {
		h.do(t, http.MethodPost, detectPath, body)
	}
	if !logs.Contains("forgeapi request") {
		t.Fatalf("captured messages %q hold no per-request line, so the check below reads nothing", logs.Messages())
	}
	if out := rendered(t, logs); bytes.Contains(out, []byte(detectToken)) {
		t.Errorf("the detection logs carry the token:\n%s", out)
	}
}
