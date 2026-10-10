package server

import (
	"net/http"
	"os/exec"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/webhttp/v3"
)

// Each key is a name kiro-cli or a marotte feature panel expects on PATH (the MCP add modal gates
// on node/npx/uv).
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
