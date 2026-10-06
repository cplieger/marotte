// Code-intelligence activation. The LSP half needs .kiro/settings/lsp.json under the work dir,
// written once by KAS's `init`; sessions then spawn language servers themselves. Init runs
// when the file is absent and an lsp-marked tool is installed, at boot and after an lsp
// tool install. KAS never rewrites the file, so deleting it is how a stale set refreshes.

package agent

import (
	"context"
	"log/slog"
	"os"
	"time"
)

// codeIntelInitBudget bounds one init call, including a lazy utility-session start.
const codeIntelInitBudget = 2 * time.Minute

// SetCodeIntelligence wires the activation inputs: the workspace lsp.json path and a gate
// reporting an installed lsp-marked tool. Both empty in tests.
func (rt *Runtime) SetCodeIntelligence(lspConfigPath string, gate func() bool) {
	rt.ciPath = lspConfigPath
	rt.ciGate = gate
}

// EnsureCodeIntelligence initializes code intelligence when needed. Safe from any goroutine;
// concurrent callers coalesce, and the guard re-arms on failure.
func (rt *Runtime) EnsureCodeIntelligence(ctx context.Context) {
	if rt.ciPath == "" || rt.ciGate == nil {
		return // not wired (tests, or activation disabled)
	}
	if _, err := os.Stat(rt.ciPath); err == nil {
		return // workspace already initialized
	}
	if !rt.ciGate() {
		return // no enabled+installed language server: init would find nothing
	}
	if !rt.ciBusy.CompareAndSwap(false, true) {
		return // an init is already in flight
	}
	defer rt.ciBusy.Store(false)
	ctx, cancel := context.WithTimeout(ctx, codeIntelInitBudget)
	defer cancel()
	msg, err := rt.utility.get().session.codeIntelligenceInit(ctx)
	if err != nil {
		slog.Warn("code intelligence init failed; will retry on the next trigger",
			"error", err)
		return
	}
	slog.Info("code intelligence initialized", "detail", msg, "config", rt.ciPath)
}
