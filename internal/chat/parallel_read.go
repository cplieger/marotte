package chat

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/parallel"
)

type chatEntry struct {
	id   string
	path string
}

// scannedRow is one header a scan read, with the id it was read under.
type scannedRow struct {
	id     marotte.ChatID
	header marotte.ChatHeader
}

// readHeadersParallel reads chat headers concurrently (8 workers). skipped names every chat that exists and was not
// read: a read failure, or a slot the fan-out never visited. No per-chat lock: reads are read-only and writes are
// atomic renames.
func readHeadersParallel(ctx context.Context, valid []chatEntry) (rows []scannedRow, skipped []marotte.ChatID) {
	if len(valid) == 0 {
		return nil, nil
	}
	const maxWorkers = 8
	type result struct {
		row     scannedRow
		ok      bool
		visited bool
		// lost: the chat exists but could not be read; a visited !ok && !lost slot vanished meanwhile.
		lost bool
	}
	results := make([]result, len(valid))

	parallel.Bounded(ctx, valid, maxWorkers, func(idx int, ce chatEntry) {
		results[idx].visited = true
		c, err := NewEntryHeader(ce.path).Read(ctx)
		if err != nil {
			// ENOENT is a concurrent delete; anything else leaves an existing chat missing, which a keep-list caller must not
			// read as complete.
			if !errors.Is(err, os.ErrNotExist) {
				slog.Warn("chat: skipping unreadable file",
					"chat_id", ce.id, "error", err)
				results[idx].lost = true
			}
			return
		}
		results[idx].row = scannedRow{id: marotte.ChatID(ce.id), header: c.Header()}
		results[idx].ok = true
	})

	rows = make([]scannedRow, 0, len(valid))
	for i := range results {
		switch {
		case results[i].ok:
			rows = append(rows, results[i].row)
		case results[i].lost || !results[i].visited:
			// An unvisited slot is a chat the scan never read: a truncated scan marked complete would let the session
			// reaper delete the KAS sessions of every chat it missed.
			skipped = append(skipped, marotte.ChatID(valid[i].id))
		}
	}
	return rows, skipped
}
