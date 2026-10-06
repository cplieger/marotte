package server

import (
	"log/slog"
	"net/http"
	"os/exec"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/toolbelt/v3"
	"github.com/cplieger/toolbelt/v3/httpapi"
	"github.com/cplieger/webhttp/v3"
)

// statusBinaries is the set of binaries /api/tools/status probes. Each
// key is a name kiro-cli or a marotte feature panel expects on PATH
// (the MCP add modal gates on node/npx/uv).
var statusBinaries = []string{
	"node", "npm", "npx",
	"go", "gofmt",
	"java",
	"cargo", "rustc",
	"uv", "uvx",
	"typescript-language-server", "tsc",
	"pyright", "pyrefly",
	"gopls", "rust-analyzer", "clangd",
	"jdtls", "kotlin-language-server",
}

// handleToolStatus serves GET /api/tools/status: bare PATH presence probes for the binaries
// feature panels gate on.
func handleToolStatus(w http.ResponseWriter, r *http.Request) {
	// Gated here, not on the pattern: a GET pattern would hand PATCH/DELETE /api/tools/status to
	// toolbelt's /api/tools/{name} with name="status". See ListenAndServe.
	if !httpreply.RequireMethod(w, r, http.MethodGet) {
		return
	}
	out := make(map[string]bool, len(statusBinaries))
	for _, b := range statusBinaries {
		_, err := exec.LookPath(b)
		out[b] = err == nil
	}
	webhttp.WriteJSON(w, out)
}

// handleToolReconcile serves POST /api/tools/reconcile: converge on tools.json now. Answers
// 202 {job} like every toolbelt mutation; a null job means nothing to converge.
func (s *Server) handleToolReconcile(w http.ResponseWriter, r *http.Request) {
	if !httpreply.RequireMethod(w, r, http.MethodPost) {
		return
	}
	if s.tools == nil {
		httpreply.NotFound(w, "the tools engine is not wired")
		return
	}
	job, _, err := s.tools.Reconcile(toolbelt.ReconcileFull)
	if err != nil {
		// A hand-edited tools.json is a caller-triggerable refusal: 400 with the reason.
		slog.Warn("tools: reconcile refused", "error", logsafe.Field(err.Error()))
		httpreply.BadRequest(w, err.Error())
		return
	}
	webhttp.WriteJSONStatus(w, http.StatusAccepted, httpapi.JobResponse{Job: job})
}
