package chat

import (
	"github.com/cplieger/marotte/internal/chat/archive"
)

// The service is created on first access rather than in NewStore to avoid a construction-time
// dependency cycle (the archive.Service needs the Store to be fully constructed first).
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
