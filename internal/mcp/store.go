// Package mcp persists and serves the user's configured MCP servers: one ordered array of Server
// records in <configDir>/mcp.json, mode 0600, written atomically. One user-global scope, shared by
// every bridge in the container.
// Env and header values often hold API keys: public reads mask them as "***", and Update keeps the
// stored value when the client sends "***" back. On disk the file is plaintext 0600, the same
// threat model as the chat files beside it.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cplieger/marotte/internal/filemode"
	"github.com/cplieger/marotte/internal/marotte"
)

// Transport names the MCP transports mcp.json accepts: "stdio", "http" (Streamable HTTP, 2025-03-26
// spec) and "sse" (legacy HTTP+SSE). "sse" is stored as itself: KAS accepts a distinct {type:"sse",
// url, headers} entry (KAS 2.12). sse and http share the remote wire shape and differ only in the
// ACP `type`.
type Transport string

// TransportStdio, TransportHTTP, and TransportSSE define the valid
// Transport values for MCP server connections.
const (
	TransportStdio Transport = "stdio"
	TransportHTTP  Transport = "http"
	TransportSSE   Transport = "sse"
	// TransportRegistry enables a server from the organization's MCP registry by
	// name; KAS resolves its command or url from the catalog, so the entry carries
	// neither.
	TransportRegistry Transport = "registry"
)

// parseTransport validates a raw string as a known transport. All three
// transports (stdio, http, sse) are first-class: "sse" is preserved as
// TransportSSE, not folded into "http" — KAS accepts a distinct SSE
// mcpServers entry over the v3 wire (see the Transport doc).
func parseTransport(s string) (Transport, error) {
	switch Transport(s) {
	case TransportStdio, TransportHTTP, TransportSSE, TransportRegistry:
		return Transport(s), nil
	default:
		return "", fmt.Errorf("unknown transport: %q", s)
	}
}

// valid reports whether t is one of the known transport values.
func (t Transport) valid() bool {
	switch t {
	case TransportStdio, TransportHTTP, TransportSSE, TransportRegistry:
		return true
	default:
		return false
	}
}

// secretMask references the shared marotte.secretMask constant.
const secretMask = marotte.SecretMask

// Server is one user-configured MCP server. ID is a short stable
// identifier used in URLs and events (generated at create time);
// Name is the user-visible label that also becomes the kiro-cli
// mcpServer name (must be unique across the configured set).
type Server struct {
	URL           string `json:"url,omitempty"`
	Name          string `json:"name"`
	Command       string `json:"command,omitempty"`
	OAuthClientID string `json:"oauth_client_id,omitempty"`
	// OAuthClientMetadataURL and OAuthRedirectURI are KAS's other two oauth
	// members (client-ID metadata document, pinned loopback redirect); not secrets.
	OAuthClientMetadataURL string    `json:"oauth_client_metadata_url,omitempty"`
	OAuthRedirectURI       string    `json:"oauth_redirect_uri,omitempty"`
	ID                     serverID  `json:"id"`
	Transport              Transport `json:"transport"`
	Args                   []string  `json:"args,omitempty"`
	Env                    []keyPair `json:"env,omitempty"`
	Headers                []keyPair `json:"headers,omitempty"`
	DisabledTools          []string  `json:"disabled_tools,omitempty"`
	CreatedAt              int64     `json:"created_at"`
	UpdatedAt              int64     `json:"updated_at"`
	// TimeoutMS is KAS's per-server connect timeout and MCP wait budget, in
	// milliseconds; 0 leaves KAS's own default (60 s). WaitForReady makes KAS hold
	// a prompt for this server even with the global wait setting off. Both are
	// written only by a paste or a raw-JSON edit.
	TimeoutMS    int  `json:"timeout_ms,omitempty"`
	Prewarm      bool `json:"prewarm,omitempty"`
	Enabled      bool `json:"enabled"`
	WaitForReady bool `json:"wait_for_ready,omitempty"`
}

// keyPair is an ordered env-var or header entry. Ordered (vs map) so
// the UI can edit entries without dropping duplicates; the on-wire ACP
// format is a JSON object so we flatten on export.
type keyPair struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Store holds the persisted list in memory plus the coordination
// needed to serialise writes and notify watchers on changes.
type Store struct {
	ctx context.Context
	// rendered is written on the write turn and read without it, so RenderedWaitForReady never queues behind a render.
	rendered atomic.Pointer[renderedWait]
	// writeTurn is the store's one writer slot, taken before mu: every change to servers and every write of either
	// file holds it, so a holder reads servers without mu. A channel because a turn awaited under a context must be
	// abandonable, which sync.Mutex.Lock is not.
	writeTurn chan struct{}
	path      string
	kasPath   string
	onChange  func(context.Context)
	// waitForReady answers the global mcp_wait_for_ready setting. Read on every
	// write rather than captured at construction, because the user changes it
	// while the store is alive.
	waitForReady func(context.Context) (waitForReady, readable bool)
	servers      []*Server
	// mu guards servers and onChange; a writer of servers holds it only alongside the turn.
	mu sync.RWMutex
}

// renderedWait is the wait value KAS's config file holds; known is false while no write has landed.
type renderedWait struct {
	waitForReady bool
	known        bool
}

// New loads the file (or initialises empty) and returns a ready store. onChange runs on a fresh
// goroutine, without the store mutex, on every persisted mutation; nil is valid. ctx bounds the
// fire-and-forget persists. mcp.json is marotte's record; KAS's ~/.kiro/settings/mcp.json is
// rendered from it (kasfile.go).
func New(ctx context.Context, configDir string, onChange func(context.Context), opts ...Option) (*Store, error) {
	// Required, not defaulted: ctx IS the store's lifetime and parents notifyChange's callback
	// work, so a missing one is a startup error rather than a silent substitution.
	if ctx == nil {
		return nil, errors.New("mcp: New requires a non-nil ctx: it is the store's lifetime and parents the change callback")
	}
	s := &Store{
		ctx:       ctx,
		writeTurn: make(chan struct{}, 1),
		path:      filepath.Join(configDir, "mcp.json"),
		kasPath:   kasConfigPath(),
		onChange:  onChange,
		servers:   []*Server{},
	}
	for _, opt := range opts {
		opt(s)
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	// Boot reconcile, before anything can race on the store. A failure must not stop the server:
	// the user needs the UI to fix the path or disk, and a stale KAS file keeps the previous set.
	if err := s.writeKASConfig(ctx, s.servers); err != nil {
		slog.Error("mcp: initial kas config write failed; the agent may use a stale server set",
			"path", s.kasPath, "error", err)
	}
	return s, nil
}

// Option configures a Store at construction.
type Option func(*Store)

// WithWaitForReady supplies the resolver for the global MCP wait setting, which
// renders `waitForReady: true` on every server. Unwired means off, KAS's own
// default. readable false means the setting has no answer: a write keeps the
// value the last write rendered, and RenderKASConfig writes nothing. The resolver
// must not call back into the store: writeKASConfig runs it on the write turn,
// with the store's write lock held when a mutation persists.
func WithWaitForReady(fn func(context.Context) (waitForReady, readable bool)) Option {
	return func(s *Store) { s.waitForReady = fn }
}

// renderPolicy runs on the write turn, or before New returns.
func (s *Store) renderPolicy(ctx context.Context) (policy kasRenderPolicy, readable bool) {
	if s.waitForReady == nil {
		return kasRenderPolicy{}, true
	}
	wait, readable := s.waitForReady(ctx)
	if !readable {
		r := s.renderedState()
		wait = r.known && r.waitForReady
	}
	return kasRenderPolicy{waitAll: wait}, readable
}

// acquireWrite takes the write turn, or returns ctx's error once ctx ends first and leaves nothing waiting.
// releaseWrite gives it back.
func (s *Store) acquireWrite(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s.writeTurn <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Store) releaseWrite() {
	<-s.writeTurn
}

// renderedState is the last landed write's wait value, the zero value before any.
func (s *Store) renderedState() renderedWait {
	if r := s.rendered.Load(); r != nil {
		return *r
	}
	return renderedWait{}
}

// SetOnChange replaces the change callback.
func (s *Store) SetOnChange(fn func(context.Context)) {
	s.mu.Lock()
	s.onChange = fn
	s.mu.Unlock()
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read mcp.json: %w", err)
	}
	// mcp.json holds plaintext API keys, so its 0600 is the whole protection; EnforceFile reports a
	// filesystem that stores wider and refuses a symlink. Warn-and-continue, so a reshaped /config
	// still boots and can be repaired.
	if _, chErr := filemode.EnforceFile(s.path, 0o600); chErr != nil {
		slog.Warn("mcp: mcp.json is not 0600 and could not be made 0600; the API keys in it may be readable by other users on this host",
			"path", s.path, "error", chErr)
	}
	var f file
	if uErr := json.Unmarshal(data, &f); uErr != nil {
		corruptPath := fmt.Sprintf("%s.corrupt.%s.%d",
			s.path,
			time.Now().UTC().Format("20060102-150405"),
			os.Getpid())
		if rErr := os.Rename(s.path, corruptPath); rErr != nil {
			slog.Error("mcp: preserve corrupt mcp.json failed",
				"path", s.path, "error", rErr, "parse_error", uErr)
		} else {
			slog.Warn("mcp: mcp.json unparseable, moved aside",
				"from", s.path, "to", corruptPath, "parse_error", uErr)
		}
		return nil
	}
	if f.Servers == nil {
		return nil
	}
	s.servers = f.Servers
	return nil
}

// notifyChange fires the change callback on a fresh goroutine, parented on
// the STORE's lifetime rather than the caller's request context. Every
// caller is an HTTP mutation handler, so a request-scoped parent would
// cancel the callback the moment the handler returned — and the
// production callback runs MCP prewarm, fire-and-forget work that must
// survive the response.
func (s *Store) notifyChange() {
	s.mu.RLock()
	cb := s.onChange
	s.mu.RUnlock()
	if cb == nil {
		return
	}
	// s.ctx is write-once in New and never mutated, so it needs no lock;
	// s.onChange is swappable via SetOnChange, which is why that one does.
	go cb(s.ctx)
}

func (s *Store) indexLocked(id serverID) int {
	for i, sv := range s.servers {
		if sv.ID == id {
			return i
		}
	}
	return -1
}

// findByNameLocked returns the stored record whose name matches, or nil.
// Uses the same case-insensitive rule as hasNameLocked — the no-op path
// and the conflict path must agree about which record they mean.
func (s *Store) findByNameLocked(name string) *Server {
	for _, sv := range s.servers {
		if strings.EqualFold(sv.Name, name) {
			return sv
		}
	}
	return nil
}

func (s *Store) hasNameLocked(name string, ignoreID serverID) bool {
	for _, sv := range s.servers {
		if sv.ID == ignoreID {
			continue
		}
		if strings.EqualFold(sv.Name, name) {
			return true
		}
	}
	return false
}
