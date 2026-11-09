package server

import (
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
)

// accountUsageTTL bounds how often the footer fetch hits KAS (GetUsageLimits may be
// rate-limited).
const accountUsageTTL = 60 * time.Second

// acctUsageCache holds the last reading and the UsageAccount key it describes; a reading is served, fresh or stale,
// only under that key.
type acctUsageCache struct {
	data    *marotte.AccountUsage
	atNanos int64 // wall-clock UnixNano of the last successful fetch
	key     uint64
	mu      sync.Mutex
}

// Cached for accountUsageTTL; on a fetch failure it serves the last-known snapshot (marked stale)
// if any, else 503. While no reading describes the signed-in account it answers 503 and fetches
// nothing, because a fetch would read the account the engine still holds.
func (s *Server) handleAccountUsage(w http.ResponseWriter, r *http.Request) {
	// Gated here, not on the pattern (see ListenAndServe).
	if s.accountUsage == nil {
		writeUsageUnavailable(w)
		return
	}
	key, ok := s.accountUsage.UsageAccount()
	if !ok {
		writeUsageUnavailable(w)
		return
	}
	c := &s.acctUsage

	c.mu.Lock()
	if c.data != nil && c.key == key && time.Since(time.Unix(0, c.atNanos)) < accountUsageTTL {
		fresh := *c.data
		c.mu.Unlock()
		fresh.Stale = false
		httpserver.WriteJSON(w, fresh)
		return
	}
	c.mu.Unlock()

	usage, err := s.accountUsage.AccountUsage(r.Context())
	if err != nil {
		c.mu.Lock()
		var last *marotte.AccountUsage
		if c.key == key {
			last = c.data
		}
		c.mu.Unlock()
		if last != nil {
			stale := *last
			stale.Stale = true
			httpserver.WriteJSON(w, stale)
			return
		}
		slog.Warn("account usage fetch failed", "error", logsafe.Field(err.Error()))
		writeUsageUnavailable(w)
		return
	}
	// A key that moved during the fetch leaves the reading's account unknown.
	if after, ok := s.accountUsage.UsageAccount(); !ok || after != key {
		writeUsageUnavailable(w)
		return
	}

	c.mu.Lock()
	c.data, c.key = usage, key
	c.atNanos = time.Now().UnixNano()
	c.mu.Unlock()
	httpserver.WriteJSON(w, usage)
}

func writeUsageUnavailable(w http.ResponseWriter) {
	httpserver.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSON("account usage unavailable"))
}
