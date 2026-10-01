package mcp

// `GET /api/mcp` carries the auto-approve POSTURE beside the servers, because the
// panel that renders the `auto_approve` chips reads this response and nothing else
// tells it whether the grant is in force. These cases pin the field's presence, both
// of its values, and — the one that matters — that it comes from the SAME resolver
// the KAS render reads, so the chips and KAS's own config file cannot disagree.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// fileCarriesAutoApprove reports whether the rendered entry for one server has an
// `autoApprove` key at all. Presence and not contents, because absent is the whole
// signal: KAS reads a missing key as "this server auto-approves nothing".
func fileCarriesAutoApprove(t *testing.T, kasPath, server string) bool {
	t.Helper()
	entry, ok := readKASServers(t, kasPath)[server]
	if !ok {
		t.Fatalf("kas config has no entry for %q", server)
	}
	_, present := entry["autoApprove"]
	return present
}

// postureOf drives GET /api/mcp through the real mux and returns the posture field.
// It fails when the field is absent, which is the contract the client's decoder
// depends on: it reads the key as REQUIRED, so a server that stopped sending it
// would blank the whole panel rather than degrade.
func postureOf(t *testing.T, mux http.Handler) bool {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/mcp", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/mcp status = %d, want 200", rec.Code)
	}
	var body struct {
		Honours *bool `json:"honours_auto_approve"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode GET /api/mcp: %v", err)
	}
	if body.Honours == nil {
		t.Fatal("GET /api/mcp carries no honours_auto_approve; the client reads it as REQUIRED, " +
			"so an absent field blanks the whole panel")
	}
	return *body.Honours
}

// TestCollection_PostureIsUnwiredSuspends pins the fail-closed default at the HTTP
// door as well as at the render: a composition that never wires WithAutoApprove
// answers what the ladder's default rung answers, rather than claiming a grant the
// file does not carry.
func TestCollection_PostureIsUnwiredSuspends(t *testing.T) {
	_, mux := newRoutedStore(t)

	if postureOf(t, mux) {
		t.Error("honours_auto_approve = true with no resolver wired, want false (suspended)")
	}
}

// TestCollection_PostureAgreesWithTheRenderedFile is the case worth having: it
// asserts the ENDPOINT and the FILE against each other rather than each against a
// literal, so the two cannot drift onto separate sources. A panel that says
// "suspended" over a file carrying the grant is worse than either error alone.
func TestCollection_PostureAgreesWithTheRenderedFile(t *testing.T) {
	dir := t.TempDir()
	kas := filepath.Join(dir, "kas", "mcp.json")
	var honour atomic.Bool
	s, err := New(t.Context(), dir, nil, WithKASConfigPath(kas),
		WithAutoApprove(func(context.Context) bool { return honour.Load() }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)

	if _, err := s.Create(t.Context(), autoApproveServer()); err != nil {
		t.Fatalf("Create: %v", err)
	}

	for _, want := range []bool{false, true, false} {
		honour.Store(want)
		if err := s.RenderKASConfig(t.Context()); err != nil {
			t.Fatalf("RenderKASConfig(honour=%v): %v", want, err)
		}
		if got := postureOf(t, mux); got != want {
			t.Errorf("honours_auto_approve = %v, want %v", got, want)
		}
		rendered := fileCarriesAutoApprove(t, kas, "everything")
		if rendered != want {
			t.Errorf("with honours_auto_approve = %v the file %s carry autoApprove; "+
				"the endpoint and the render read different sources",
				want, map[bool]string{true: "does not", false: "does"}[want])
		}
	}
}
