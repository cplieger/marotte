package composition

import (
	"context"
	"time"

	"github.com/cplieger/marotte/internal/agent"
	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/chat/archive"
	"github.com/cplieger/marotte/internal/kirosession"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/tabs"
)

// retentionStores are the stores the keep-state reconcile reads beside the chat store and the runtime.
type retentionStores struct {
	tabs   *tabs.Store
	leases *runlease.Store
	reaper *kirosession.Reaper
}

// wireRetention builds the retention scheduler with every hook it runs with, and the keep-state reconcile's start,
// which returns its stop; Build and the retention seam test both call it, so the test runs the wiring production runs.
func wireRetention(appCtx context.Context, cfg *Config, chatStore *chat.Store, h *agent.Runtime, stores retentionStores) (*archive.PurgeScheduler, func() (stop func()), error) {
	sched, err := chat.NewPurgeScheduler(chatStore, chat.PurgeConfig{
		Retention: func() time.Duration { return chatRetention(appCtx, cfg.ConfigDir) },
		OnPurge: heldUnlessDraining(h.HoldUnlessDraining, func(id marotte.ChatID, sessionChain []string) {
			// heldUnlessDraining skips this once Shutdown began; the chain then stays in teardown until exit, which only
			// refuses work in a stopping process.
			defer chatStore.EndTeardown(id, sessionChain)
			h.Membership().RetentionClose(appCtx, id, sessionChain)
		}),
		SidePasses: []func(context.Context) archive.PurgeResult{
			func(pctx context.Context) archive.PurgeResult {
				return purgeResult(h.PurgeFinishedRuns(pctx, runRetention(pctx, cfg.ConfigDir)))
			},
			func(pctx context.Context) archive.PurgeResult {
				return purgeResult(h.SweepOrphanRunLogs(pctx, runRetention(pctx, cfg.ConfigDir)))
			},
			h.Membership().SettleOwed,
			func(pctx context.Context) archive.PurgeResult {
				h.ReattributeRuns(pctx)
				return archive.PurgeResult{}
			},
		},
	})
	if err != nil {
		return nil, nil, err
	}
	// A chat kept by a keep fact contributes no wake-up deadline, so closing its tab must trigger a pass.
	h.Membership().SetRetentionWake(sched.Trigger)
	deps := &keepStateDeps{
		chats: chatStore, membership: h.Membership(), tabs: stores.tabs, leases: stores.leases, runs: h.Runs(),
		runStates: stores.reaper.RunStates, ready: h.EngineReady(),
	}
	start := func() func() {
		return runBackground(appCtx, "keep-state reconcile", func(ctx context.Context) { reconcileKeepState(ctx, deps) })
	}
	return sched, start, nil
}

func purgeResult(r agent.RunPurgeResult) archive.PurgeResult {
	return archive.PurgeResult{NextDeadline: r.NextDeadline, Purged: r.Purged, Kept: r.Kept, Errors: r.Errors}
}
