package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// noticeRunBoundTimeout caps a notice's run bound read: it waits on the run cell's query slot, which an inspect may
// hold for 45 s, and a notice raised at position 0 only loses its end by an ack.
const noticeRunBoundTimeout = 3 * time.Second

// newReadBound answers the read bound of a chat or run tab, 0 when it cannot be read.
func newReadBound(
	chatBound func(context.Context, marotte.ChatID) (uint64, error),
	runBound func(context.Context, string) (uint64, error),
	_ time.Duration,
) func(context.Context, marotte.TabKind, string) uint64 {
	return func(ctx context.Context, kind marotte.TabKind, ref string) uint64 {
		var (
			bound uint64
			err   error
		)
		switch kind {
		case marotte.TabKindChat:
			bound, err = chatBound(ctx, marotte.ChatID(ref))
		case marotte.TabKindRun:
			bound, err = runBound(ctx, ref)
		default:
			return 0
		}
		if err != nil {
			slog.Debug("notice: read bound not read, raised at position 0", "kind", string(kind), "error", err)
			return 0
		}
		return bound
	}
}

// noticePosition is the read bound of the tab n names, read at raise; 0 for an ask notice and a subject naming no tab.
func (bc *bridgeCoordinator) noticePosition(ctx context.Context, n *marotte.NotificationPayload) uint64 {
	if n.Ask != "" || bc.readBound == nil {
		return 0
	}
	kind, ref, ok := n.Tab()
	if !ok {
		return 0
	}
	return bc.readBound(ctx, kind, ref)
}
