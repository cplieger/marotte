//go:build marotte_test

package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/preview"
	"github.com/cplieger/marotte/internal/push"
	"github.com/cplieger/webhttp/v3"
)

// testPortEnv names the listener port for a test binary. The browser-mode suite
// starts one beside whatever already holds the default port.
const testPortEnv = "MAROTTE_TEST_PORT"

func init() {
	if p := os.Getenv(testPortEnv); p != "" {
		listenPort = p
	}
}

// sseProbe is the runtime as the SSE control surface reads it. *agent.Runtime
// satisfies it; a server wired to a narrower engine mounts no hooks.
type sseProbe interface {
	SSEClientCount() int
	SSEConnects() (legacy, v3 uint64)
	CloseNextSSEAfter(n int)
}

// pushProbe is the push service as the control surface reads it: the presence
// table and the send filter's counters. *push.Service satisfies it; a server
// wired to a narrower push service reports an empty table.
type pushProbe interface {
	PresenceRows() []push.PresenceRow
	PresenceTransitions() (alive, expired uint64)
	Suppressed(kind marotte.PushKind) uint64
}

// sseProbeResponse is GET /api/test/sse's body.
type sseProbeResponse struct {
	Suppressed      map[marotte.PushKind]uint64 `json:"push_suppressed_total"`
	Presence        []presenceRow               `json:"presence"`
	Clients         int                         `json:"clients"`
	LegacyConnect   uint64                      `json:"legacy_connect"`
	V3Connect       uint64                      `json:"v3_connect"`
	PresenceAlive   uint64                      `json:"presence_alive"`
	PresenceExpired uint64                      `json:"presence_expired"`
}

// presenceRow is one tag's fold: its connection count, its last acknowledgement
// (RFC 3339) and the verdict the send filter reads.
type presenceRow struct {
	Tag         string `json:"tag"`
	LastAliveAt string `json:"lastAliveAt"`
	Connected   int    `json:"connected"`
	Gone        bool   `json:"gone"`
}

type closeAfterRequest struct {
	After int `json:"after"`
}

// registerTestHooks mounts the SSE control surface the browser-mode suite drives (connection
// census with presence, the close-after cut) plus the preview token minter. Test builds only.
func (s *Server) registerTestHooks(mux *http.ServeMux) {
	s.registerPreviewTestHook(mux)
	probe, ok := s.agent.(sseProbe)
	if !ok {
		return
	}
	pushP, _ := s.push.(pushProbe)
	slog.Warn("test-only SSE control surface mounted under /api/test/; this is a marotte_test build")
	mux.HandleFunc("GET /api/test/sse", func(w http.ResponseWriter, _ *http.Request) {
		legacy, v3 := probe.SSEConnects()
		resp := sseProbeResponse{
			Presence:      []presenceRow{},
			Suppressed:    map[marotte.PushKind]uint64{},
			Clients:       probe.SSEClientCount(),
			LegacyConnect: legacy,
			V3Connect:     v3,
		}
		if pushP != nil {
			for _, row := range pushP.PresenceRows() {
				resp.Presence = append(resp.Presence, presenceRow{
					Tag:         row.Tag,
					LastAliveAt: row.LastAliveAt.UTC().Format(time.RFC3339Nano),
					Connected:   row.Connected,
					Gone:        row.Gone,
				})
			}
			resp.PresenceAlive, resp.PresenceExpired = pushP.PresenceTransitions()
			for _, kr := range push.Kinds() {
				resp.Suppressed[kr.Kind] = pushP.Suppressed(kr.Kind)
			}
		}
		webhttp.WriteJSON(w, resp)
	})
	mux.HandleFunc("POST /api/test/sse/close-after", func(w http.ResponseWriter, r *http.Request) {
		var req closeAfterRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil || req.After <= 0 {
			httpreply.BadRequest(w, "after must be a positive frame count")
			return
		}
		probe.CloseNextSSEAfter(req.After)
		w.WriteHeader(http.StatusNoContent)
	})
}

type previewGranter interface {
	Grant(path string, exp time.Time) (marotte.PreviewGrant, error)
}

type previewTokenRequest struct {
	ExpiresAt time.Time `json:"expires_at"`
	Path      string    `json:"path"`
}

// registerPreviewTestHook mounts the preview controls a browser check needs: a
// grant with a chosen expiry, a resolver answering ENOSYS or EAGAIN, and a file
// growing after its size check. Independent of the SSE probe.
func (s *Server) registerPreviewTestHook(mux *http.ServeMux) {
	g, ok := s.preview.(previewGranter)
	if !ok {
		return
	}
	mux.HandleFunc("POST /api/test/preview-token", func(w http.ResponseWriter, r *http.Request) {
		var req previewTokenRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
			httpreply.BadRequest(w, "body must be {path, expires_at}")
			return
		}
		grant, err := g.Grant(req.Path, req.ExpiresAt)
		if err != nil {
			httpreply.BadRequest(w, err.Error())
			return
		}
		webhttp.WriteJSON(w, grant)
	})
	mux.HandleFunc("POST /api/test/preview-resolver", func(w http.ResponseWriter, r *http.Request) {
		var req previewResolverRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
			httpreply.BadRequest(w, "body must be {unavailable}")
			return
		}
		preview.SetResolverUnavailable(req.Unavailable)
		preview.SetResolverBusy(req.Busy)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/test/preview-grow", func(w http.ResponseWriter, r *http.Request) {
		var req previewGrowRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
			httpreply.BadRequest(w, "body must be {path, bytes}")
			return
		}
		if req.Bytes < 1 || req.Bytes > 1<<20 {
			httpreply.BadRequest(w, "bytes must be 1 to 1048576")
			return
		}
		if st, err := os.Lstat(req.Path); err != nil || !st.Mode().IsRegular() {
			httpreply.BadRequest(w, "path must name a regular file")
			return
		}
		preview.GrowAfterNextSizeCheck(req.Path, req.Bytes)
		w.WriteHeader(http.StatusNoContent)
	})
}

type previewResolverRequest struct {
	Unavailable bool `json:"unavailable"`
	Busy        bool `json:"busy"`
}

type previewGrowRequest struct {
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
}
