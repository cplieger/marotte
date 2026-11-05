// Package mcp persists and serves the user's configured MCP servers: one ordered array of Server
// records in <configDir>/mcp.json, mode 0600, written atomically. One user-global scope, shared by
// every session on the agent engine.
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
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/cplieger/marotte/internal/marotte"
)

// Transport names the MCP transports mcp.json accepts: "stdio", "http" (Streamable HTTP, 2025-03-26
// spec), "sse" (legacy HTTP+SSE) and "registry" (a catalog server enabled by name). "sse" is stored as itself: KAS accepts a distinct {type:"sse",
// url, headers} entry (KAS 2.12). sse and http share the remote wire shape and differ only in the
// ACP `type`.
type Transport string

// TransportStdio, transportHTTP, transportSSE and transportRegistry are the four valid Transport values.
const (
	TransportStdio Transport = "stdio"
	transportHTTP  Transport = "http"
	transportSSE   Transport = "sse"
	// transportRegistry enables a server from the organization's MCP registry by
	// name; KAS resolves its command or url from the catalog, so the entry carries
	// neither, and env and headers are its only transport-specific overrides.
	transportRegistry Transport = "registry"
)

// parseTransport validates a raw string as one of the four known transports. "sse" stays transportSSE, not
// folded into "http" (see the Transport doc).
func parseTransport(s string) (Transport, error) {
	switch Transport(s) {
	case TransportStdio, transportHTTP, transportSSE, transportRegistry:
		return Transport(s), nil
	default:
		return "", fmt.Errorf("unknown transport: %q", s)
	}
}

// valid reports whether t is one of the known transport values.
func (t Transport) valid() bool {
	switch t {
	case TransportStdio, transportHTTP, transportSSE, transportRegistry:
		return true
	default:
		return false
	}
}

// versionNegotiation is KAS's per-server versionNegotiation, the closed set of its mcp.json enum.
type versionNegotiation string

// versionNegotiationAuto and versionNegotiationLegacy are the two values KAS accepts.
const (
	versionNegotiationAuto   versionNegotiation = "auto"
	versionNegotiationLegacy versionNegotiation = "legacy"
)

// valid reports whether v is unset or one of KAS's values, compared exactly because KAS does not trim.
func (v versionNegotiation) valid() bool {
	switch v {
	case "", versionNegotiationAuto, versionNegotiationLegacy:
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
	// VersionNegotiation empty leaves KAS's `auto`, which starts a stdio server once to send
	// server/discover and again for the real connection. `legacy` uses the initialize handshake
	// and starts it once, and fails a server that speaks only the 2026-07-28 protocol. Written
	// only by a paste or a raw-JSON edit.
	VersionNegotiation versionNegotiation `json:"version_negotiation,omitempty"`
	Args               []string           `json:"args,omitempty"`
	Env                []keyPair          `json:"env,omitempty"`
	Headers            []keyPair          `json:"headers,omitempty"`
	DisabledTools      []string           `json:"disabled_tools,omitempty"`
	// OAuthScopes is KAS's oauthScopes (remote only), sent space-joined as the registration scope and used for the
	// authorization request when the server names none itself.
	OAuthScopes []string `json:"oauth_scopes,omitempty"`
	CreatedAt   int64    `json:"created_at"`
	UpdatedAt   int64    `json:"updated_at"`
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
	// ctx is the store's lifetime, ended by Shutdown; mu orders each callback's callbacks.Go before
	// Shutdown's cancel, so none is added once Wait may have begun.
	ctx context.Context
	end context.CancelFunc
	// rendered is written on the write turn and read without it, so RenderedWaitForReady never queues behind a render.
	rendered atomic.Pointer[renderedWait]
	// writeTurn is the store's one writer slot, taken before mu: every change to servers and every write of either
	// file holds it, so a holder reads servers without mu. A channel because a turn awaited under a context must be
	// abandonable, which sync.Mutex.Lock is not.
	writeTurn chan struct{}
	onChange  func(context.Context)
	// waitForReady answers the global mcp_wait_for_ready setting. Read on every
	// write rather than captured at construction, because the user changes it
	// while the store is alive.
	waitForReady func(context.Context) (waitForReady, readable bool)
	// held names the KAS mcpServers keys the unreadable record entries may own (renderServersKey).
	// It and orphanCheck are written by load and on the write turn (holdOrphans), and read only on
	// it or before New returns, so without mu.
	held map[string]bool
	// orphanCheck is non-nil while an unreadable entry whose name does not read waits for the
	// first readable KAS file (holdOrphans): the nameKey of every server load read.
	orphanCheck map[string]bool
	path        string
	kasPath     string
	// kasRefused keys the last KAS file state readKASDoc refused, so a retry over it is not
	// reported again; written on the write turn.
	kasRefused string
	servers    []*Server
	// unreadable are the record's entries load could not decode, which persist writes back. Set
	// before New returns and never changed, so read without mu.
	unreadable []json.RawMessage
	callbacks  sync.WaitGroup
	// mu guards servers and onChange; a writer of servers holds it only alongside the turn.
	mu sync.RWMutex
}

// renderedWait is the wait value KAS's config file holds; known is false while no write has landed.
type renderedWait struct {
	waitForReady bool
	known        bool
}

// New loads the file (or initialises empty) and returns a ready store. onChange runs on a fresh
// goroutine, without the store mutex, on every persisted mutation; nil is valid. ctx parents the
// change callbacks, which Shutdown also ends. mcp.json is marotte's record; KAS's
// ~/.kiro/settings/mcp.json is rendered from it (kasfile.go).
func New(ctx context.Context, configDir string, onChange func(context.Context), opts ...Option) (*Store, error) {
	// Required, not defaulted: ctx IS the store's lifetime and parents notifyChange's callback
	// work, so a missing one is a startup error rather than a silent substitution.
	if ctx == nil {
		return nil, errors.New("mcp: New requires a non-nil ctx: it is the store's lifetime and parents the change callback")
	}
	lifetime, end := context.WithCancel(ctx)
	s := &Store{
		ctx:       lifetime,
		end:       end,
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
		end()
		return nil, err
	}
	// Boot reconcile, before anything can race on the store. A failure must not stop the server:
	// the user needs the UI to fix the path or disk, and a stale KAS file keeps the previous set.
	// readKASDoc has already reported an ErrKASUnreadable refusal.
	if err := s.bootReconcile(ctx); err != nil && !errors.Is(err, ErrKASUnreadable) {
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

// notifyChange fires the change callback on a fresh goroutine Shutdown waits for, parented on the
// STORE's lifetime rather than the caller's request context: every caller is an HTTP mutation
// handler, and the production callback starts MCP prewarm, work that must survive the response.
// Nothing fires once Shutdown has begun.
func (s *Store) notifyChange() {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cb := s.onChange
	if cb == nil || s.ctx.Err() != nil {
		return
	}
	s.callbacks.Go(func() { cb(s.ctx) })
}

// Shutdown cancels the change callbacks in flight, refuses any later one, and returns once those
// in flight have, or with ctx's error when ctx ends first. It may be called more than once.
func (s *Store) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.end()
	s.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.callbacks.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("mcp: change callbacks still running: %w", ctx.Err())
	}
}

func (s *Store) indexLocked(id serverID) int {
	for i, sv := range s.servers {
		if sv.ID == id {
			return i
		}
	}
	return -1
}

// nameKey is the identity two server names share when they name the same server: KAS lower-cases a name and
// folds '-' to '_' in its tools' invocation ids (mcp_<server>_<tool>), so names equal under that fold collide.
func nameKey(name string) string {
	return strings.ReplaceAll(strings.ToLower(name), "-", "_")
}

// findByNameLocked returns the stored record whose name matches under nameKey, or nil; the no-op path and the
// conflict path must agree about which record they mean.
func (s *Store) findByNameLocked(name string) *Server {
	key := nameKey(name)
	for _, sv := range s.servers {
		if nameKey(sv.Name) == key {
			return sv
		}
	}
	return nil
}

func (s *Store) hasNameLocked(name string, ignoreID serverID) bool {
	key := nameKey(name)
	for _, sv := range s.servers {
		if sv.ID == ignoreID {
			continue
		}
		if nameKey(sv.Name) == key {
			return true
		}
	}
	return false
}
