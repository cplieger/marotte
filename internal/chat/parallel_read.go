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

// readHeadersParallel reads chat headers concurrently (8 workers) and returns those read. No per-chat lock: reads are
// read-only and writes are atomic renames.
func readHeadersParallel(
	ctx context.Context,
	valid []chatEntry,
) (headersOut []marotte.ChatHeader, complete bool) {
	if len(valid) == 0 {
		return nil, true
	}
	const maxWorkers = 8
	type result struct {
		header marotte.ChatHeader
		ok     bool
		// lost: the chat exists but could not be read; !ok also covers a vanished one.
		lost bool
	}
	results := make([]result, len(valid))

	ran := parallel.Bounded(ctx, valid, maxWorkers, func(idx int, ce chatEntry) {
		c, err := NewEntryHeader(ce.path).Read(ctx)
		if err != nil {
			// ENOENT is a concurrent delete; anything else leaves an existing chat missing, which a keep-list caller must not
			// read as complete.
			if !errors.Is(err, os.ErrNotExist) {
				slog.Warn("chat: skipping unreadable file",
					"chat_id", ce.id, "error", err)
				results[idx] = result{lost: true}
			}
			return
		}
		results[idx] = result{header: c.Header(), ok: true}
	})

	headers := make([]marotte.ChatHeader, 0, len(valid))
	// An unvisited slot is zero-valued, so completeness comes from the count: a truncated scan marked complete would let
	// the session reaper delete the KAS sessions of every chat it missed.
	complete = ran == len(valid)
	for i := range results {
		switch {
		case results[i].ok:
			headers = append(headers, results[i].header)
		case results[i].lost:
			complete = false
		}
	}
	return headers, complete
}
