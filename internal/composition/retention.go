package composition

import (
	"context"
	"time"

	"github.com/cplieger/marotte/internal/agent"
	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/chat/archive"
	"github.com/cplieger/marotte/internal/marotte"
)

// wireRetention builds the retention scheduler with every hook it runs with; Build and the retention seam test both
// call it, so the test runs the wiring production runs.
func wireRetention(appCtx context.Context, cfg *Config, chatStore *chat.Store, h *agent.Runtime) (*archive.PurgeScheduler, error) {
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
				window, purge := runRetention(pctx, cfg.ConfigDir)
				if !purge {
					return archive.PurgeResult{}
				}
				r := h.PurgeParentlessRuns(pctx, window)
				return archive.PurgeResult{NextDeadline: r.NextDeadline, Purged: r.Purged, Kept: r.Kept, Errors: r.Errors}
			},
			h.Membership().SettleOwed,
			func(pctx context.Context) archive.PurgeResult {
				h.ReattributeRuns(pctx)
				return archive.PurgeResult{}
			},
		},
	})
	if err != nil {
		return nil, err
	}
	// A chat kept by a keep fact contributes no wake-up deadline, so closing its tab must trigger a pass.
	h.Membership().SetRetentionWake(sched.Trigger)
	return sched, nil
}
