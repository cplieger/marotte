package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cplieger/marotte/internal/settings"
)

// fakeMCPRender counts re-renders and records whether a render's context was
// still tied to the reader's request (durable.Context answers nil for Done).
type fakeMCPRender struct {
	renders  int
	attached bool
}

func (f *fakeMCPRender) RenderKASConfig(ctx context.Context) error {
	f.renders++
	if ctx.Done() != nil {
		f.attached = true
	}
	return nil
}

func patchSettings(ctx context.Context, t *testing.T, s *Server, body string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/settings", bytes.NewReader([]byte(body))).WithContext(ctx)
	rec := httptest.NewRecorder()
	s.handleSettingsWrite(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH /api/settings = %d, want 200: %s", rec.Code, rec.Body)
	}
}

// The MCP wait setting reaches open chats only through KAS's watched file, so a
// PATCH of it re-renders once, on a context a closed tab cannot cancel.
func TestSettingsWrite_MCPWaitForReadyReRendersKASConfig(t *testing.T) {
	dir := t.TempDir()
	render := &fakeMCPRender{}
	s := &Server{agent: &fakeEngine{}, push: &testPush{}, configDir: dir, mcpRender: render}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	patchSettings(ctx, t, s, `{"`+settings.KeyMCPWaitForReady+`":true}`)

	if render.renders != 1 {
		t.Fatalf("RenderKASConfig ran %d times, want 1", render.renders)
	}
	if render.attached {
		t.Error("the re-render ran on the request's own context: a tab close mid-PATCH cancels it and leaves KAS's mcp.json on the previous wait setting")
	}
}

// TestSettingsWrite_DoesNotRenderForAnUnrelatedKey pins no render for fb_path, which every
// file-browser navigation PATCHes.
func TestSettingsWrite_DoesNotRenderForAnUnrelatedKey(t *testing.T) {
	for _, tc := range []struct{ key, body string }{
		{settings.KeyFBPath, `{"` + settings.KeyFBPath + `":"/workspace/src"}`},
		{settings.KeySecurityProfile, `{"` + settings.KeySecurityProfile + `":"guarded"}`},
	} {
		t.Run(tc.key, func(t *testing.T) {
			dir := t.TempDir()
			render := &fakeMCPRender{}
			s := &Server{agent: &fakeEngine{}, push: &testPush{}, configDir: dir, mcpRender: render}

			patchSettings(t.Context(), t, s, tc.body)

			if render.renders != 0 {
				t.Errorf("RenderKASConfig ran %d times for a patch of %s, want 0", render.renders, tc.key)
			}
		})
	}
}
