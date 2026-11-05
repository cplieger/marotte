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

// The snapshot is served stale when a refresh fails.
type acctUsageCache struct {
	data    *marotte.AccountUsage
	atNanos int64 // wall-clock UnixNano of the last successful fetch
	mu      sync.Mutex
}

// Cached for accountUsageTTL; on a fetch failure it serves the last-known snapshot (marked stale)
// if any, else 503.
func (s *Server) handleAccountUsage(w http.ResponseWriter, r *http.Request) {
	// Gated here, not on the pattern (see ListenAndServe).
	if !httpreply.RequireMethod(w, r, http.MethodGet) {
		return
	}
	if s.accountUsage == nil {
		httpserver.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSON("account usage unavailable"))
		return
	}
	c := &s.acctUsage

	c.mu.Lock()
	if c.data != nil && time.Since(time.Unix(0, c.atNanos)) < accountUsageTTL {
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
		last := c.data
		c.mu.Unlock()
		if last != nil {
			stale := *last
			stale.Stale = true
			httpserver.WriteJSON(w, stale)
			return
		}
		slog.Warn("account usage fetch failed", "error", logsafe.Field(err.Error()))
		httpserver.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSON("account usage unavailable"))
		return
	}

	c.mu.Lock()
	c.data = usage
	c.atNanos = time.Now().UnixNano()
	c.mu.Unlock()
	httpserver.WriteJSON(w, usage)
}
