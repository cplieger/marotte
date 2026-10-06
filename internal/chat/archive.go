package chat

import (
	"context"
	"time"

	"github.com/cplieger/marotte/internal/chat/archive"
)

// archiveSvc returns the Store's archive service, creating it lazily.
// The service is created on first access rather than in NewStore to
// avoid a construction-time dependency cycle (the archive.Service
// needs the Store to be fully constructed first).
func (s *Store) archiveSvc() *archive.Service {
	s.archiveOnce.Do(func() {
		var opts []archive.Option
		if s.onPurge != nil {
			opts = append(opts, archive.WithOnPurge(s.onPurge))
		}
		if s.isLive != nil {
			opts = append(opts, archive.WithLiveChats(s.isLive))
		}
		if s.hasOpenTab != nil {
			opts = append(opts, archive.WithOpenTabs(s.hasOpenTab))
		}
		if s.broadcast != nil {
			opts = append(opts, archive.WithBroadcaster(s.broadcast.Broadcast))
		}
		s.archive = archive.New(s, opts...)
	})
	return s.archive
}

// purgeExpired deletes chats whose last activity is older than maxAge. The archive sub-package names the retention
// concept; nothing is archived. Production uses NewPurgeScheduler; this gives the store's tests a synchronous pass.
func (s *Store) purgeExpired(ctx context.Context, maxAge time.Duration) {
	s.archiveSvc().Purge(ctx, maxAge)
}
