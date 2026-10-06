package chat

import (
	"context"
	"time"

	"github.com/cplieger/marotte/internal/chat/archive"
)

// NewPurgeScheduler builds a scheduler that purges by the value `retention` returns, plus side passes on the same
// wake-ups. Its context arrives at PurgeScheduler.Start.
func NewPurgeScheduler(store *Store, retention func() time.Duration, sidePasses ...func(context.Context) archive.PurgeResult) *archive.PurgeScheduler {
	return archive.NewPurgeScheduler(store.archiveSvc(), retention, sidePasses...)
}
