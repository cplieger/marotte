package mcp

// marotte RENDERS KAS's own MCP config file from its store and sends nothing
// inline: the file hot-reloads and keeps the `oauth`, `cwd` and `timeout` fields
// the inline path drops, and KAS merges `client > file-based`, so any inline entry
// would shadow the file. The file is shared: KAS also reads `powers.mcpServers`,
// which WritePowersServers renders, so each writer replaces only its own key. `env`/`headers` are records on the
// wire, so the store keeps the ordered form, and `type` is emitted only for a
// registry entry, since KAS infers every other transport from the fields present.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/marotte/internal/workspace"
)

// kasServerKey is the top-level key marotte owns in KAS's config file.
const kasServerKey = "mcpServers"

// kasPowersKey is the top-level key KAS READS for installed legacy Powers'
// servers (`powers.mcpServers`); KAS never writes it. WritePowersServers owns it,
// and writeKASConfig preserves it verbatim.
const kasPowersKey = "powers"

// kasFileMaxBytes bounds the re-read of the existing file. The file is a handful
// of server declarations; a larger one is not something to merge into.
const kasFileMaxBytes = 4 << 20

// kasServer is one entry of KAS's `mcpServers` map, matching its
// McpServerWireSchema. Only the fields marotte has a value for are
// emitted: every one is `omitempty`, because an explicit null or zero is
// a different declaration than an absent field.
//
// Deliberately absent: `cwd` (no field for it; reachable by hand-editing the file).
type kasServer struct {
	// Type is emitted for a registry entry only, where it is the declaration.
	Type          string            `json:"type,omitempty"`
	Command       string            `json:"command,omitempty"`
	Args          []string          `json:"args,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	URL           string            `json:"url,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
	OAuth         *kasOAuth         `json:"oauth,omitempty"`
	DisabledTools []string          `json:"disabledTools,omitempty"`
	// Timeout is milliseconds, and it is both KAS's connect timeout and the
	// budget a waitForReady server gets inside a prompt. Zero is KAS's default.
	Timeout int `json:"timeout,omitempty"`
	// WaitForReady makes KAS hold a prompt until this server's connection
	// attempt settles; a server still connecting past Timeout fails the turn.
	WaitForReady bool `json:"waitForReady,omitempty"`
	// Disabled keeps a switched-off server IN the file rather than omitting it.
	// KAS then reports it with status "disabled" instead of not knowing about it,
	// which is the difference between "off" and "gone" in the UI.
	Disabled bool `json:"disabled,omitempty"`
}

// kasOAuth mirrors KAS's closed oauth schema {clientId, redirectUri,
// clientMetadataUrl}.
type kasOAuth struct {
	ClientID          string `json:"clientId,omitempty"`
	RedirectURI       string `json:"redirectUri,omitempty"`
	ClientMetadataURL string `json:"clientMetadataUrl,omitempty"`
}

// renderKASServers maps the store's servers onto KAS's map shape. Secrets are
// included, so the result must not be logged.
//
// Every server is rendered, enabled or not: a disabled one carries
// `disabled: true`, so KAS reports it disabled rather than not knowing it.
func renderKASServers(servers []*Server, policy kasRenderPolicy) map[string]kasServer {
	out := make(map[string]kasServer, len(servers))
	for _, s := range servers {
		if s == nil || s.Name == "" {
			continue
		}
		entry := kasServer{
			DisabledTools: s.DisabledTools,
			Timeout:       s.TimeoutMS,
			WaitForReady:  policy.waitAll || s.WaitForReady,
			Disabled:      !s.Enabled,
		}
		switch s.Transport {
		case TransportStdio:
			entry.Command = s.Command
			entry.Args = s.Args
			entry.Env = pairsRecord(s.Env)
		case TransportHTTP, TransportSSE:
			// One branch for both: KAS negotiates HTTP vs SSE itself.
			entry.URL = s.URL
			entry.Headers = pairsRecord(s.Headers)
			oauth := kasOAuth{
				ClientID:          s.OAuthClientID,
				RedirectURI:       s.OAuthRedirectURI,
				ClientMetadataURL: s.OAuthClientMetadataURL,
			}
			if oauth != (kasOAuth{}) {
				entry.OAuth = &oauth
			}
		case TransportRegistry:
			entry.Type = string(TransportRegistry)
		default:
			// An unknown transport has neither a command nor a url, so KAS would
			// reject it as "must specify a command or url". Skip it rather than
			// write a declaration that can only produce a warning.
			continue
		}
		out[s.Name] = entry
	}
	return out
}

// kasRenderPolicy is the per-write decision a render takes, RESOLVED rather
// than a settings key: this package has no business holding the settings
// vocabulary.
type kasRenderPolicy struct {
	// waitAll marks every server waitForReady (the mcp_wait_for_ready setting).
	// A server's own pasted value still applies when it is false.
	waitAll bool
}

// pairsRecord flattens ordered KeyPairs into KAS's record shape. Later entries
// win on a duplicate name, which is the same resolution the inline path's own
// record build produced.
func pairsRecord(in []KeyPair) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for _, kv := range in {
		out[kv.Name] = kv.Value
	}
	return out
}

// writeKASConfig renders the server set into KAS's config file, preserving
// every top-level key it does not own. An unreadable existing file is replaced
// rather than fatal, or the agent would stay on a stale server set the UI
// cannot fix. The wait setting is read per write, which is how a settings
// flip reaches the file. MUST be called with s.mu held for writing, or before
// New returns: it records the wait value the file now holds.
func (s *Store) writeKASConfig(ctx context.Context, servers []*Server) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	policy, _ := s.renderPolicy(ctx)
	return s.writeKASConfigWith(ctx, servers, policy)
}

func (s *Store) writeKASConfigWith(ctx context.Context, servers []*Server, policy kasRenderPolicy) error {
	doc := s.readKASConfig()
	rendered, err := renderServersKey(servers, policy)
	if err != nil {
		return err
	}
	doc[kasServerKey] = rendered
	if err := s.writeKASDoc(ctx, doc); err != nil {
		return err
	}
	s.rendered = renderedWait{waitForReady: policy.waitAll, known: true}
	return nil
}

func renderServersKey(servers []*Server, policy kasRenderPolicy) (json.RawMessage, error) {
	rendered, err := json.Marshal(renderKASServers(servers, policy))
	if err != nil {
		return nil, fmt.Errorf("%w kas mcp.json: %w", ErrPersistMarshal, err)
	}
	return rendered, nil
}

// writeKASDoc writes the whole document. 0600: the file holds header values and
// OAuth client secrets, and KAS reads it as the same user.
func (s *Store) writeKASDoc(ctx context.Context, doc map[string]json.RawMessage) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("%w kas mcp.json: %w", ErrPersistMarshal, err)
	}
	if _, err := atomicfile.WriteFile(ctx, s.kasPath, data,
		atomicfile.WithMode(0o600), atomicfile.WithMkdirMode(0o700)); err != nil {
		return fmt.Errorf("%w: %w", ErrPersistWrite, err)
	}
	return nil
}

// WritePowersServers renders `powers.mcpServers` from the installed legacy
// Powers, keeping every other key, and reports whether the file changed. An
// unchanged block is not rewritten, because every write makes KAS reload its MCP
// servers. An empty set removes the member, and `powers` with it when nothing
// else is left in it. The write lock excludes persist and RenderKASConfig, which
// rewrite the same file.
func (s *Store) WritePowersServers(ctx context.Context, servers map[string]json.RawMessage) (bool, error) {
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, docOK := s.readKASDoc()
	rawBlock, hasBlock := doc[kasPowersKey]
	block := objectOrEmpty(rawBlock)
	if docOK && powersBlockMatches(block, hasBlock, servers) {
		return false, nil
	}
	var resent *renderedWait
	if !docOK {
		// The document was replaced whole, so marotte's own key goes back with it.
		policy, _ := s.renderPolicy(ctx)
		rendered, err := renderServersKey(s.servers, policy)
		if err != nil {
			return false, err
		}
		doc[kasServerKey] = rendered
		resent = &renderedWait{waitForReady: policy.waitAll, known: true}
	}
	if len(servers) == 0 {
		delete(block, kasServerKey)
	} else {
		rendered, err := json.Marshal(servers)
		if err != nil {
			return false, fmt.Errorf("%w kas mcp.json: %w", ErrPersistMarshal, err)
		}
		block[kasServerKey] = rendered
	}
	if len(block) == 0 {
		delete(doc, kasPowersKey)
	} else {
		rendered, err := json.Marshal(block)
		if err != nil {
			return false, fmt.Errorf("%w kas mcp.json: %w", ErrPersistMarshal, err)
		}
		doc[kasPowersKey] = rendered
	}
	if err := s.writeKASDoc(ctx, doc); err != nil {
		return true, err
	}
	if resent != nil {
		s.rendered = *resent
	}
	return true, nil
}

// powersBlockMatches reports whether the decoded `powers` block already holds
// exactly servers in its normalized form, so no write is needed.
func powersBlockMatches(block map[string]json.RawMessage, hasBlock bool, servers map[string]json.RawMessage) bool {
	rawCurrent, hasCurrent := block[kasServerKey]
	if len(servers) == 0 {
		return !hasCurrent && (!hasBlock || len(block) > 0)
	}
	return sameServers(objectOrEmpty(rawCurrent), servers)
}

// objectOrEmpty decodes a member that must be a JSON object. Anything else,
// null included, reads as an empty map the caller can write into.
func objectOrEmpty(raw json.RawMessage) map[string]json.RawMessage {
	var out map[string]json.RawMessage
	if json.Unmarshal(raw, &out) != nil || out == nil {
		return map[string]json.RawMessage{}
	}
	return out
}

// sameServers compares two server sets by their compacted JSON, so a reindented
// file reads as unchanged.
func sameServers(a, b map[string]json.RawMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for name, av := range a {
		bv, ok := b[name]
		if !ok {
			return false
		}
		var ab, bb bytes.Buffer
		if json.Compact(&ab, av) != nil || json.Compact(&bb, bv) != nil || !bytes.Equal(ab.Bytes(), bb.Bytes()) {
			return false
		}
	}
	return true
}

// RenderKASConfig re-renders KAS's config file from the stored servers, for an
// mcp_wait_for_ready flip: KAS hot-reloads the file, so running chats take the
// change now rather than at their next start. It reads the setting under the
// write lock, so it renders the newest stored value; a setting that cannot be
// read writes nothing and leaves RenderedWaitForReady as it was.
func (s *Store) RenderKASConfig(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	policy, readable := s.renderPolicy(ctx)
	if !readable {
		return nil
	}
	return s.writeKASConfigWith(ctx, s.servers, policy)
}

// RenderedWaitForReady reports the mcp_wait_for_ready value KAS's config file
// was last written with; known is false until a write of it succeeds.
func (s *Store) RenderedWaitForReady() (waitForReady, known bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rendered.waitForReady, s.rendered.known
}

// readKASConfig returns the existing document's top-level keys minus
// `mcpServers`, so a write can put ours back without touching `powers` or
// anything else.
func (s *Store) readKASConfig() map[string]json.RawMessage {
	doc, _ := s.readKASDoc()
	delete(doc, kasServerKey)
	return doc
}

// readKASDoc returns every top-level key of the existing document, or an empty
// one for an absent, oversized or malformed file. ok is false when a file is
// there but is not a readable JSON object, so the caller knows to replace it.
func (s *Store) readKASDoc() (doc map[string]json.RawMessage, ok bool) {
	empty := map[string]json.RawMessage{}
	info, err := os.Stat(s.kasPath)
	if errors.Is(err, os.ErrNotExist) {
		return empty, true
	}
	if err != nil || info.Size() > kasFileMaxBytes {
		if err != nil {
			slogWarnKAS("stat failed", s.kasPath, err)
		}
		return empty, false
	}
	data, err := os.ReadFile(s.kasPath)
	if err != nil {
		slogWarnKAS("read failed", s.kasPath, err)
		return empty, false
	}
	// KAS parses this with JSONC, so a hand-written file may carry comments that
	// encoding/json rejects. Losing an unknown key is the cost of not vendoring a
	// JSONC parser to preserve keys marotte does not write; it is logged.
	if err := json.Unmarshal(data, &doc); err != nil {
		slogWarnKAS("existing file unparseable, its non-mcpServers keys will be dropped", s.kasPath, err)
		return empty, false
	}
	if doc == nil {
		return empty, false
	}
	return doc, true
}

// kasConfigPath is the file KAS reads for user-level MCP servers.
//
// HOME, not the workspace. KAS reads BOTH the home and workspace paths,
// and the workspace one sits inside the user's repo where it is a
// plausible accidental commit of a file holding OAuth client secrets.
func kasConfigPath() string {
	return workspace.KiroSettingsPath("mcp.json")
}

// slogWarnKAS logs a KAS-config read problem. The path is safe to log; the
// file's CONTENT is not, so no branch here touches it.
func slogWarnKAS(msg, path string, err error) {
	slog.Warn("mcp: kas config "+msg, "path", path, "error", err)
}
