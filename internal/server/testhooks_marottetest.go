//go:build marotte_test

package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/preview"
	"github.com/cplieger/marotte/internal/push"
	"github.com/cplieger/marotte/internal/route"
)

// The browser-mode suite starts one beside whatever already holds the default port.
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

// SSEProbe is GET /api/test/sse's body.
type SSEProbe struct {
	Suppressed      map[marotte.PushKind]uint64 `json:"push_suppressed_total"`
	Presence        []SSEPresenceRow            `json:"presence"`
	Clients         int                         `json:"clients"`
	LegacyConnect   uint64                      `json:"legacy_connect"`
	V3Connect       uint64                      `json:"v3_connect"`
	PresenceAlive   uint64                      `json:"presence_alive"`
	PresenceExpired uint64                      `json:"presence_expired"`
}

// SSEPresenceRow is one tag's fold: its connection count, its last acknowledgement
// (RFC 3339) and the verdict the send filter reads.
type SSEPresenceRow struct {
	Tag         string `json:"tag"`
	LastAliveAt string `json:"lastAliveAt"`
	Connected   int    `json:"connected"`
	Gone        bool   `json:"gone"`
}

// SSECloseAfter is POST /api/test/sse/close-after's body.
type SSECloseAfter struct {
	After int `json:"after"`
}

// registerTestHooks binds the SSE control surface the browser-mode suite drives (connection
// census with presence, the close-after cut) plus the preview controls. Test builds only. Every
// hook is bound on every test boot; one whose dependency is absent answers the fallback's 404.
func (s *Server) registerTestHooks(m routeMux) {
	s.registerPreviewTestHook(m)
	probe, ok := s.agent.(sseProbe)
	if ok {
		slog.Warn("test-only SSE control surface mounted under /api/test/; this is a marotte_test build")
	}
	pushP, _ := s.push.(pushProbe)
	m.Bind(route.JSON[struct{}, SSEProbe]("GET /api/test/sse", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !ok {
			httpreply.UnknownEndpoint(w, nil)
			return
		}
		legacy, v3 := probe.SSEConnects()
		resp := SSEProbe{
			Presence:      []SSEPresenceRow{},
			Suppressed:    map[marotte.PushKind]uint64{},
			Clients:       probe.SSEClientCount(),
			LegacyConnect: legacy,
			V3Connect:     v3,
		}
		if pushP != nil {
			for _, row := range pushP.PresenceRows() {
				resp.Presence = append(resp.Presence, SSEPresenceRow{
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
		httpserver.WriteJSON(w, resp)
	})))
	m.Bind(route.WithStatus(route.JSON[SSECloseAfter, struct{}]("POST /api/test/sse/close-after", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ok {
			httpreply.UnknownEndpoint(w, r)
			return
		}
		var req SSECloseAfter
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil || req.After <= 0 {
			httpreply.BadRequest(w, "after must be a positive frame count")
			return
		}
		probe.CloseNextSSEAfter(req.After)
		w.WriteHeader(http.StatusNoContent)
	})), http.StatusNoContent))
}

type previewGranter interface {
	Grant(path string, exp time.Time) (marotte.PreviewGrant, error)
}

// PreviewTokenRequest is POST /api/test/preview-token's body.
type PreviewTokenRequest struct {
	ExpiresAt time.Time `json:"expires_at"`
	Path      string    `json:"path"`
}

// registerPreviewTestHook mounts the preview controls a browser check needs: a
// grant with a chosen expiry, a resolver answering ENOSYS or EAGAIN, and a file
// growing after its size check. Independent of the SSE probe.
func (s *Server) registerPreviewTestHook(m routeMux) {
	g, ok := s.preview.(previewGranter)
	absent := func(w http.ResponseWriter, r *http.Request) bool {
		if !ok {
			httpreply.UnknownEndpoint(w, r)
		}
		return !ok
	}
	m.Bind(route.JSON[PreviewTokenRequest, marotte.PreviewGrant]("POST /api/test/preview-token", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if absent(w, r) {
			return
		}
		var req PreviewTokenRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
			httpreply.BadRequest(w, "body must be {path, expires_at}")
			return
		}
		grant, err := g.Grant(req.Path, req.ExpiresAt)
		if err != nil {
			httpreply.BadRequest(w, err.Error())
			return
		}
		httpserver.WriteJSON(w, grant)
	})))
	m.Bind(route.WithStatus(route.JSON[PreviewResolverRequest, struct{}]("POST /api/test/preview-resolver", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if absent(w, r) {
			return
		}
		var req PreviewResolverRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
			httpreply.BadRequest(w, "body must be {unavailable}")
			return
		}
		preview.SetResolverUnavailable(req.Unavailable)
		preview.SetResolverBusy(req.Busy)
		w.WriteHeader(http.StatusNoContent)
	})), http.StatusNoContent))
	m.Bind(route.WithStatus(route.JSON[PreviewGrowRequest, struct{}]("POST /api/test/preview-grow", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if absent(w, r) {
			return
		}
		var req PreviewGrowRequest
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
	})), http.StatusNoContent))
}

// PreviewResolverRequest is POST /api/test/preview-resolver's body.
type PreviewResolverRequest struct {
	Unavailable bool `json:"unavailable"`
	Busy        bool `json:"busy"`
}

// PreviewGrowRequest is POST /api/test/preview-grow's body.
type PreviewGrowRequest struct {
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
}
