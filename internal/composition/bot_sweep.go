package composition

import (
	"context"
	"errors"
	"log/slog"

	"github.com/cplieger/marotte/internal/bot"
	"github.com/cplieger/marotte/internal/chat"
)

// sweepBotDirs removes the directory of each Bot whose chat record is gone (bot.SweepDirs) against a fresh scan of
// chats, and logs the outcome.
func sweepBotDirs(ctx context.Context, chats *chat.Store, paths bot.Paths) {
	removed, err := bot.SweepDirs(ctx, paths, func(ctx context.Context) bot.Records {
		scan := chats.SessionOwners(ctx)
		return bot.Records{Read: scan.Read, Complete: scan.Complete}
	})
	if len(removed) > 0 {
		slog.Info("removed the directories of deleted Bots", "chat_ids", removed)
	}
	switch {
	case errors.Is(err, bot.ErrIncompleteScan):
		slog.Warn("Bot directory sweep skipped: some chat records could not be read, so a directory may still be owned")
	case err != nil:
		slog.Warn("Bot directory sweep failed", "error", err)
	}
}
