// Tools feature-gating probe. The tools REST surface itself is the
// cplieger/toolbelt httpapi projection, mounted in server.go; this
// file keeps the one marotte-specific endpoint on that prefix.

package server

import (
	"log/slog"
	"net/http"
	"os/exec"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/toolbelt/v3"
	"github.com/cplieger/toolbelt/v3/httpapi"
	"github.com/cplieger/webhttp/v3"
)

// statusBinaries is the set of binaries /api/tools/status probes. Each
// key is a name kiro-cli or a marotte feature panel expects on PATH
// (the MCP add modal gates on node/npx/uv, Sources on the forge CLIs).
var statusBinaries = []string{
	"node", "npm", "npx",
	"go", "gofmt",
	"java",
	"cargo", "rustc",
	"uv", "uvx",
	"gh", "glab", "tea",
	"typescript-language-server", "tsc",
	"pyright", "pyrefly",
	"gopls", "rust-analyzer", "clangd",
	"jdtls", "kotlin-language-server",
}

// handleToolStatus: GET /api/tools/status
//
// Bare PATH presence probes for the well-known binaries feature panels
// gate on (e.g. the MCP modal's "Setting up Node..." spinner).
func handleToolStatus(w http.ResponseWriter, r *http.Request) {
	// Gated here rather than on the ServeMux pattern: a `GET `-prefixed pattern
	// stops being an exact match for a non-GET, which hands PATCH/DELETE
	// /api/tools/status to the /api/tools/ subtree mount — toolbelt's
	// /api/tools/{name} handlers, with name="status". See ListenAndServe.
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

// handleToolReconcile: POST /api/tools/reconcile
//
// Converge the volume on whatever tools.json now says, rather than at the next
// boot only. Job-shaped rather than blocking: Reconcile enqueues and returns, so
// the answer is the same 202 {job} every toolbelt mutation gives and progress
// streams over the tool_job_* SSE; a null job means nothing to converge.
//
// Registered method-lessly; see ListenAndServe for why.
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
		// Reconcile loads the manifest before it enqueues, so a hand-edited
		// tools.json is a caller-triggerable refusal rather than a fault: 400
		// carrying the reason, as every neighbouring /api/tools mutation gives.
		slog.Warn("tools: reconcile refused", "error", err)
		httpreply.BadRequest(w, err.Error())
		return
	}
	webhttp.WriteJSONStatus(w, http.StatusAccepted, httpapi.JobResponse{Job: job})
}
