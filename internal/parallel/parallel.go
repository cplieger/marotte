// Package parallel holds the one bounded fan-out helper two packages need.
//
// It exists because `internal/chat` and `internal/chat/archive` each carried a
// byte-identical copy, and neither could import the other's: `chat` imports
// `archive`, so the reverse is a cycle, and exporting a generic worker pool FROM
// `archive` would warp a package about purging chats into a concurrency utility.
// `internal/chat/io.go` records that same reasoning for a JSON helper it chose to
// keep duplicated; the difference here is that only one of the two copies was
// tested, so a divergence in the untested one had nothing to catch it.
//
// Nothing in here knows about chats, files or storage. A caller that needs one
// should be able to read this package in full before using it.
package parallel

import (
	"context"
	"sync"
	"sync/atomic"
)

// Bounded dispatches fn over items with up to min(len(items), maxWorkers) goroutines, stopping
// early when ctx is cancelled, and REPORTS how many items it ran fn for: a cancelled fan-out runs a
// PREFIX, so a caller compares done to len(items) to mark its result incomplete rather than publish
// a subset as whole.
// Workers pull indices from a shared channel, so a slow item stalls nothing else. fn gets the
// INDEX, so results collect into a pre-sized slice with no mutex. ctx is checked per item.
func Bounded[T any](ctx context.Context, items []T, maxWorkers int, fn func(i int, item T)) (done int) {
	if len(items) == 0 {
		return 0
	}
	workers := min(len(items), maxWorkers)
	work := make(chan int, len(items))
	for i := range items {
		work <- i
	}
	close(work)
	var ran atomic.Int64
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			// Counted locally and published once, so the common path costs no
			// contention: an atomic add per item would serialize eight workers on
			// one cache line for work whose real bound is disk.
			local := int64(0)
			for idx := range work {
				if ctx.Err() != nil {
					// BREAK, never return: a worker that returns here skips the
					// publish below and its completed items vanish from the count,
					// which would report a cancelled scan as emptier than it was.
					break
				}
				fn(idx, items[idx])
				local++
			}
			ran.Add(local)
		})
	}
	wg.Wait()
	return int(ran.Load())
}
