package agent

import (
	"context"
	"log/slog"
	"sync"
)

// slashRefresher coalesces RefreshSlashCommands: one pass runs, at most one more is queued.
type slashRefresher struct {
	mu      sync.Mutex
	running bool
	again   bool
}

// RefreshSlashCommands re-reads the slash menu through an observer pass, whose door KAS answers with the menu as
// .kiro reads now, and announces a changed list as slash_commands_changed. It returns at once; a call while a pass
// runs queues one more. KAS's own watcher reaches resident sessions only, so a menu read with no chat open needs this.
func (rt *Runtime) RefreshSlashCommands() {
	r := rt.slashRefresh
	r.mu.Lock()
	if r.running {
		r.again = true
		r.mu.Unlock()
		return
	}
	r.running = true
	r.mu.Unlock()
	if !rt.lifecycle.goUnlessDraining(rt.slashRefreshLoop) {
		r.mu.Lock()
		r.running = false
		r.mu.Unlock()
	}
}

func (rt *Runtime) slashRefreshLoop() {
	r := rt.slashRefresh
	for {
		rt.refreshSlashOnce(rt.lifecycle.shutdownCtx)
		r.mu.Lock()
		if !r.again || rt.lifecycle.shutdownCtx.Err() != nil {
			r.running = false
			r.mu.Unlock()
			return
		}
		r.again = false
		r.mu.Unlock()
	}
}

// refreshSlashOnce holds one observer pass open until KAS sent its menu, bounded by bootSeedTimeout like the boot pass;
// observerUpdate applies the list. The wait is taken before the pass opens, so a menu sent at the door is not missed.
func (rt *Runtime) refreshSlashOnce(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, bootSeedTimeout)
	defer cancel()
	next := rt.slash.nextUpdate()
	err := rt.observe(ctx, func(ctx context.Context, _ engineSession) error {
		select {
		case <-next:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if err != nil {
		slog.Debug("slash menu: refresh pass ended without a menu", "error", err)
	}
}
