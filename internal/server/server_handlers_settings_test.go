package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cplieger/marotte/internal/settings"
)

func patchSettings(ctx context.Context, t *testing.T, s *Server, body string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/settings", bytes.NewReader([]byte(body))).WithContext(ctx)
	rec := httptest.NewRecorder()
	s.handleSettingsWrite(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH /api/settings = %d, want 200: %s", rec.Code, rec.Body)
	}
}

// The reconcile carries the write to every open chat, so a tab closed mid-PATCH must not cancel
// it after the write has landed.
func TestSettingsWrite_ReconcilesOnADurableContext(t *testing.T) {
	eng := &fakeEngine{}
	s := &Server{agent: eng, push: &testPush{}, configDir: t.TempDir()}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	patchSettings(ctx, t, s, `{"`+settings.KeyMCPWaitForReady+`":true}`)

	if eng.reconciles != 1 {
		t.Fatalf("PATCH %s reconciled %d times, want 1", settings.KeyMCPWaitForReady, eng.reconciles)
	}
	if eng.reconciledOnCancellableCtx {
		t.Error("the reconcile ran on the request's own context: a tab close mid-PATCH cancels the pushes and leaves open chats on the previous value")
	}
}
