package agent

import (
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/webhttp/v4"
)

// standingFetchTimeout bounds one workspace getState read.
const standingFetchTimeout = 30 * time.Second

// configStanding caches what kiro-cli loaded but does not use (the standing of each displaced or disabled item) and
// the load errors it reported for agent files, both keyed by the Docs row's Path. Standing is refetched on the
// configuration push and on a settings write; a load error clears once its file changed or went away.
type configStanding struct {
	fetch     func(ctx context.Context) (json.RawMessage, error)
	broadcast func(ctx context.Context)
	lifetime  *lifetime
	lstat     func(name string) (fs.FileInfo, error)
	now       func() time.Time
	errors    map[string]agentLoadError
	standing  []marotte.ConfigStanding
	mu        sync.Mutex
	running   bool
	again     bool
}

// agentLoadError is one cached load error with the file's size and mtime when it was reported.
type agentLoadError struct {
	mtime time.Time
	marotte.AgentLoadError
	size int64
}

func newConfigStanding(lt *lifetime, fetch func(ctx context.Context) (json.RawMessage, error), broadcast func(context.Context)) *configStanding {
	return &configStanding{
		fetch: fetch, broadcast: broadcast, lifetime: lt,
		lstat: os.Lstat, now: time.Now, errors: map[string]agentLoadError{},
	}
}

// refetchSoon coalesces standing reads: one in flight, at most one more queued.
func (c *configStanding) refetchSoon() {
	c.mu.Lock()
	if c.running {
		c.again = true
		c.mu.Unlock()
		return
	}
	c.running = true
	c.mu.Unlock()
	if !c.lifetime.goUnlessDraining(c.refetchLoop) {
		c.mu.Lock()
		c.running = false
		c.mu.Unlock()
	}
}

func (c *configStanding) refetchLoop() {
	for {
		c.refetch(c.lifetime.shutdownCtx)
		c.mu.Lock()
		if !c.again || c.lifetime.shutdownCtx.Err() != nil {
			c.running = false
			c.mu.Unlock()
			return
		}
		c.again = false
		c.mu.Unlock()
	}
}

// refetch reads the workspace's standing once and broadcasts config_standing_changed when it moved.
func (c *configStanding) refetch(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, standingFetchTimeout)
	defer cancel()
	raw, err := c.fetch(ctx)
	if err != nil {
		slog.Warn("configuration standing: the workspace read failed; the previous standing stands", "error", err)
		return
	}
	next, ok := translate.DecodeConfigurationStanding(raw)
	if !ok {
		slog.Warn("configuration standing: the workspace read did not decode")
		return
	}
	c.mu.Lock()
	changed := !slices.Equal(c.standing, next)
	c.standing = next
	c.mu.Unlock()
	if changed {
		c.broadcast(ctx)
	}
}

// noteAgentFrame records an agent load error from any lane: KAS sends one at every session creation and agent
// refresh. A relative path joins no row and is not kept.
func (c *configStanding) noteAgentFrame(msg *marotte.RPCResponse) {
	path, code, text, ok := translate.DecodeAgentLoadError(msg.Method, msg.Params)
	if !ok {
		return
	}
	key, ok := translate.DocKeyFromAbs(path)
	if !ok {
		slog.Debug("configuration standing: an agent load error names a relative path", "path", logsafe.Field(path))
		return
	}
	e := agentLoadError{At: c.now().UTC(), ReasonCode: code, Error: text}
	if fi, err := c.lstat("/" + key); err == nil {
		e.size, e.mtime = fi.Size(), fi.ModTime()
	}
	c.mu.Lock()
	prev, had := c.errors[key]
	c.errors[key] = e
	c.mu.Unlock()
	if !had || prev.ReasonCode != e.ReasonCode || prev.Error != e.Error {
		c.broadcast(c.lifetime.shutdownCtx)
	}
}

// response is GET /api/config/standing's answer; it first drops each load error whose file is gone or changed.
func (c *configStanding) response() marotte.ConfigStandingResponse {
	c.mu.Lock()
	keys := slices.Collect(maps.Keys(c.errors))
	c.mu.Unlock()
	var stale []string
	for _, key := range keys {
		c.mu.Lock()
		e := c.errors[key]
		c.mu.Unlock()
		fi, err := c.lstat("/" + key)
		if err != nil || fi.Size() != e.size || !fi.ModTime().Equal(e.mtime) {
			stale = append(stale, key)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, key := range stale {
		delete(c.errors, key)
	}
	out := marotte.ConfigStandingResponse{
		Standing:   slices.Clone(c.standing),
		LoadErrors: make(map[string]marotte.AgentLoadError, len(c.errors)),
	}
	if out.Standing == nil {
		out.Standing = []marotte.ConfigStanding{}
	}
	for k, e := range c.errors {
		out.LoadErrors[k] = e.AgentLoadError
	}
	return out
}

func (c *configStanding) handle(w http.ResponseWriter, _ *http.Request) {
	webhttp.WriteJSON(w, c.response())
}
