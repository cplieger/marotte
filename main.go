// Marotte for Kiro: ACP-based web interface for kiro-cli.
//
// One kiro-cli subprocess per active chat; browsers on the same chat share its bridge.
// The server is the source of truth; the browser projects its state via SSE + GET /api/chats.
package main

import (
	"context"
	_ "embed"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/cplieger/marotte/internal/composition"
	"github.com/cplieger/marotte/internal/forges"
	"github.com/cplieger/marotte/internal/workspace"
	"github.com/cplieger/toolbelt/v3"
)

// requiredToolsList is the required-tools.txt the image build verifies the baked catalog
// against, embedded so every runtime catalog refresh applies the same gate.
//
//go:embed required-tools.txt
var requiredToolsList string

func main() {
	os.Exit(runMain())
}

// runMain is the startup sequence, split from main so deferred shutdown runs (os.Exit skips defers).
func runMain() int {
	// Ahead of ConfigFromEnv, whose warnings reach stderr: git shows a helper's stderr to the user.
	if len(os.Args) > 1 && os.Args[1] == forges.HelperCommand {
		return forges.RunCredentialHelper(context.Background(), os.Args[2:], os.Stdin, os.Stdout, os.Stderr)
	}
	cfg := composition.ConfigFromEnv()
	cfg.ToolCatalogRequire = toolbelt.ParseRequireList(requiredToolsList)

	workspace.SetKiroHomeResolver(func() string {
		if h := os.Getenv("KIRO_HOME"); h != "" {
			return h
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return ".kiro"
		}
		return filepath.Join(home, ".kiro")
	})

	app, err := composition.Build(context.Background(), &cfg, staticFS)
	if err != nil {
		slog.Error("build", "error", err)
		return 1
	}
	defer app.Shutdown()
	if err := app.Run(); err != nil {
		return 1
	}
	return 0
}
