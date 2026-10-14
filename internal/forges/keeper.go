package forges

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// keeperInterval is how often the keeper starts a pass. The git credential
// helper never refreshes, so a rotating token used only by git relies on this to
// be renewed inside its refresh lead.
const keeperInterval = time.Minute

// Keeper is the one automatic checker: each pass heals unusable persistence,
// checks every usable connection in id order (which renews a due token and
// persists a moved rotation cursor), and restores git's helper registration.
type Keeper struct {
	manager *Manager
	every   time.Duration
}

// NewKeeper builds the keeper of m's connections.
func NewKeeper(m *Manager) *Keeper {
	return &Keeper{manager: m, every: keeperInterval}
}

// Run makes one pass at once and one every minute after, and returns when ctx
// ends. A pass longer than the interval is followed by the next at once.
func (k *Keeper) Run(ctx context.Context) {
	tick := time.NewTicker(k.every)
	defer tick.Stop()
	for {
		k.pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (k *Keeper) pass(ctx context.Context) {
	m := k.manager
	start := time.Now()
	m.mu.RLock()
	total := len(m.conns)
	m.mu.RUnlock()
	slog.Info("forges: keeper pass started", "connections", total)
	if !m.healthy() {
		m.reload(ctx)
	}
	checked, changed := 0, 0
	hs := m.readers()
	for i := range hs {
		if ctx.Err() != nil {
			break
		}
		moved, err := m.check(ctx, hs[i].id, checkBudget)
		if !errors.Is(err, errCheckBusy) && ctx.Err() == nil {
			checked++
		}
		if moved {
			changed++
		}
	}
	m.reconcileGit(ctx)
	var hits, misses, saved int
	var bytes int64
	hs = m.readers()
	for i := range hs {
		rc := hs[i].core.BudgetState().ReadCache
		hits, misses, saved, bytes = hits+rc.Hits, misses+rc.Misses, saved+rc.Saved, bytes+rc.Bytes
	}
	slog.Info("forges: keeper pass done", "connections", total, "checked", checked, "changed", changed,
		"duration", time.Since(start), "hits", hits, "misses", misses, "saved", saved, "bytes", bytes)
}
