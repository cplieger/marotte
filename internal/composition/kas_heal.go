package composition

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/mcp"
)

// kasHealInterval bounds how long a server another writer removed from KAS's MCP file stays
// missing from live chats; each tick is one small file read.
const kasHealInterval = 30 * time.Second

// kasHealer keeps KAS's MCP file equal to the store's render. It is driven by its ticker and by
// session opens, never by a watch on the file, so its own write cannot re-trigger it.
type kasHealer struct {
	heal func(ctx context.Context, trigger string) (healed bool, err error)
	// afterHeal puts back what a heal cannot: the Powers block of a file that was deleted or emptied.
	afterHeal func(context.Context)
	lastErr   string
	every     time.Duration
	mu        sync.Mutex
}

// healOnce is safe for concurrent use: the poll and every session open call it.
func (k *kasHealer) healOnce(ctx context.Context, trigger string) {
	healed, err := k.heal(ctx, trigger)
	if healed && k.afterHeal != nil {
		k.afterHeal(ctx)
	}
	k.report(trigger, err)
}

// report warns once per distinct failure. ErrKASUnreadable was already logged by the store, and a
// cancelled ctx is shutdown or a request that walked away.
func (k *kasHealer) report(trigger string, err error) {
	msg := ""
	if err != nil && !errors.Is(err, mcp.ErrKASUnreadable) &&
		!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		msg = err.Error()
	}
	k.mu.Lock()
	repeat := msg == k.lastErr
	k.lastErr = msg
	k.mu.Unlock()
	if msg != "" && !repeat {
		slog.Warn("mcp: kas config heal failed", "trigger", trigger, "error", err)
	}
}

func (k *kasHealer) run(ctx context.Context) {
	t := time.NewTicker(k.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			k.healOnce(ctx, "poll")
		}
	}
}
