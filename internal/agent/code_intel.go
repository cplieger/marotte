// Code-intelligence activation. The LSP half needs .kiro/settings/lsp.json under the work dir,
// written once by KAS's `init`; sessions then spawn language servers themselves. Init runs
// when the file is absent and an lsp-marked tool is installed, at boot and after an lsp
// tool install. KAS never rewrites the file, so deleting it is how a stale set refreshes.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// codeIntelInitBudget bounds one init call, including its observer pass's door.
const codeIntelInitBudget = 2 * time.Minute

// codeIntelWiring is the activation inputs: the workspace lsp.json path and a gate reporting an installed lsp-marked
// tool.
type codeIntelWiring struct {
	gate func() bool
	path string
}

// SetCodeIntelligence wires the activation inputs; unset in tests. The engine's ready hook may already be reading
// them, since the tools engine that owns the gate is built after the runtime.
func (rt *Runtime) SetCodeIntelligence(lspConfigPath string, gate func() bool) {
	rt.codeIntel.Store(&codeIntelWiring{path: lspConfigPath, gate: gate})
}

// EnsureCodeIntelligence initializes code intelligence when needed. Safe from any goroutine;
// concurrent callers coalesce, and the guard re-arms on failure.
func (rt *Runtime) EnsureCodeIntelligence(ctx context.Context) {
	ci := rt.codeIntel.Load()
	if ci == nil || ci.path == "" || ci.gate == nil {
		return // not wired (tests, or activation disabled)
	}
	if _, err := os.Stat(ci.path); err == nil {
		return // workspace already initialized
	}
	if !ci.gate() {
		return // no enabled+installed language server: init would find nothing
	}
	if !rt.ciBusy.CompareAndSwap(false, true) {
		return // an init is already in flight
	}
	defer rt.ciBusy.Store(false)
	ctx, cancel := context.WithTimeout(ctx, codeIntelInitBudget)
	defer cancel()
	msg, err := rt.codeIntelligenceInit(ctx)
	if err != nil {
		slog.Warn("code intelligence init failed; will retry on the next trigger",
			"error", err)
		return
	}
	slog.Info("code intelligence initialized", "detail", msg, "config", ci.path)
}

// codeIntelligenceInit issues _kiro/codeIntelligence init on an observer pass: KAS answers "Session not found" with no
// resident session. KAS detects the workspace languages and writes .kiro/settings/lsp.json, leaving an existing
// config alone. Returns KAS's message.
func (rt *Runtime) codeIntelligenceInit(ctx context.Context) (string, error) {
	var raw json.RawMessage
	err := rt.observe(ctx, func(ctx context.Context, s engineSession) error {
		var err error
		raw, err = observerCall(ctx, s, "code intelligence init", methodKiroCodeIntel, map[string]any{"subcommand": "init"})
		return err
	})
	if err != nil {
		return "", err
	}
	var out struct {
		Message string `json:"message"`
		Success bool   `json:"success"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("code intelligence init: decode: %w", err)
	}
	if !out.Success {
		return "", fmt.Errorf("code intelligence init: %s", out.Message)
	}
	return out.Message, nil
}
