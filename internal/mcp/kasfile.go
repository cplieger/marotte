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

// The file is a handful of server declarations; a larger one is refused, neither merged into nor overwritten.
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
func pairsRecord(in []keyPair) map[string]string {
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
// every top-level key it does not own. An existing file it cannot read is left
// as it is and the write fails with ErrKASUnreadable. The wait setting is read
// per write, which is how a settings flip reaches the file. MUST be called on
// the write turn, or before New returns: it records the wait value the file now
// holds.
func (s *Store) writeKASConfig(ctx context.Context, servers []*Server) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	doc, err := s.readKASConfig()
	if err != nil {
		return err
	}
	policy, _ := s.renderPolicy(ctx)
	return s.writeKASConfigWith(ctx, doc, servers, policy)
}

// writeKASConfigWith puts the rendered servers into doc, a readKASConfig result, and writes it.
func (s *Store) writeKASConfigWith(ctx context.Context, doc map[string]json.RawMessage, servers []*Server, policy kasRenderPolicy) error {
	rendered, err := renderServersKey(servers, policy)
	if err != nil {
		return err
	}
	doc[kasServerKey] = rendered
	if err := s.writeKASDoc(ctx, doc); err != nil {
		return err
	}
	s.rendered.Store(&renderedWait{waitForReady: policy.waitAll, known: true})
	return nil
}

func renderServersKey(servers []*Server, policy kasRenderPolicy) (json.RawMessage, error) {
	rendered, err := json.Marshal(renderKASServers(servers, policy))
	if err != nil {
		return nil, fmt.Errorf("%w kas mcp.json: %w", errPersistMarshal, err)
	}
	return rendered, nil
}

// writeKASDoc writes the whole document. 0600: the file holds header values and
// OAuth client secrets, and KAS reads it as the same user.
func (s *Store) writeKASDoc(ctx context.Context, doc map[string]json.RawMessage) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("%w kas mcp.json: %w", errPersistMarshal, err)
	}
	if _, err := atomicfile.WriteFile(ctx, s.kasPath, data,
		atomicfile.WithMode(0o600), atomicfile.WithMkdirMode(0o700)); err != nil {
		return fmt.Errorf("%w: %w", errPersistWrite, err)
	}
	return nil
}

// WritePowersServers renders `powers.mcpServers` from the installed legacy
// Powers, keeping every other key, and reports whether the file changed. An
// unchanged block is not rewritten, because every write makes KAS reload its MCP
// servers. An empty set removes the member, and `powers` with it when nothing
// else is left in it. The write turn excludes persist and RenderKASConfig, which
// rewrite the same file.
func (s *Store) WritePowersServers(ctx context.Context, servers map[string]json.RawMessage) (bool, error) {
	if err := s.acquireWrite(ctx); err != nil {
		return false, err
	}
	defer s.releaseWrite()
	doc, fresh, err := s.readKASDoc()
	if err != nil {
		return false, err
	}
	rawBlock, hasBlock := doc[kasPowersKey]
	block := objectOrEmpty(rawBlock)
	if !fresh && powersBlockMatches(block, hasBlock, servers) {
		return false, nil
	}
	var resent *renderedWait
	if fresh {
		// The file held no document, so marotte's own key goes back with the block.
		policy, _ := s.renderPolicy(ctx)
		rendered, err := renderServersKey(s.servers, policy)
		if err != nil {
			return false, err
		}
		doc[kasServerKey] = rendered
		resent = &renderedWait{waitForReady: policy.waitAll, known: true}
	}
	if err := setOrDelete(block, kasServerKey, servers); err != nil {
		return false, err
	}
	if err := setOrDelete(doc, kasPowersKey, block); err != nil {
		return false, err
	}
	if err := s.writeKASDoc(ctx, doc); err != nil {
		return true, err
	}
	if resent != nil {
		s.rendered.Store(resent)
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

func setOrDelete(obj map[string]json.RawMessage, key string, value map[string]json.RawMessage) error {
	if len(value) == 0 {
		delete(obj, key)
		return nil
	}
	rendered, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("%w kas mcp.json: %w", errPersistMarshal, err)
	}
	obj[key] = rendered
	return nil
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
// change now rather than at their next start. It reads the setting on the write
// turn, so it renders the newest stored value; a setting that cannot be read
// writes nothing and leaves RenderedWaitForReady as it was. It waits for the turn
// only while ctx lasts, returning ctx's error and writing nothing.
func (s *Store) RenderKASConfig(ctx context.Context) error {
	if err := s.acquireWrite(ctx); err != nil {
		return err
	}
	defer s.releaseWrite()
	policy, readable := s.renderPolicy(ctx)
	if !readable {
		return nil
	}
	doc, err := s.readKASConfig()
	if err != nil {
		return err
	}
	return s.writeKASConfigWith(ctx, doc, s.servers, policy)
}

// RenderedWaitForReady reports the mcp_wait_for_ready value KAS's config file
// was last written with; known is false until a write of it succeeds. It takes
// no lock, so it answers while a render or a persist holds the store.
func (s *Store) RenderedWaitForReady() (waitForReady, known bool) {
	r := s.renderedState()
	return r.waitForReady, r.known
}

// readKASConfig returns the existing document's top-level keys minus
// `mcpServers`, so a write can put ours back without touching `powers` or
// anything else.
func (s *Store) readKASConfig() (map[string]json.RawMessage, error) {
	doc, _, err := s.readKASDoc()
	if err != nil {
		return nil, err
	}
	delete(doc, kasServerKey)
	return doc, nil
}

// ErrKASUnreadable refuses a write over a KAS config file that exists but is not a JSON object
// marotte can read: rewriting it would drop every key marotte does not own. KAS parses the file
// as JSONC, so a hand-written comment is valid to KAS and still lands here. The store logs each
// refusal of one file state once, so a caller retrying it need not log it again.
var ErrKASUnreadable = errors.New("kas mcp.json is not a readable JSON object")

// kasUnreadableHint is ErrKASUnreadable's text for the person whose save it refused.
const kasUnreadableHint = "marotte could not read ~/.kiro/settings/mcp.json, so it left the file unchanged. The file must be plain JSON with no comments and under 4 MB. Fix the file or move it aside, then save again."

// readKASDoc returns every top-level key of the existing document; an absent file is an empty
// one. fresh is true when the file holds no document (only whitespace or JSON null, after an
// optional UTF-8 BOM), so the caller replaces it whole and puts marotte's own key back. A file
// that cannot be stat'd, read or parsed, or is over kasFileMaxBytes, returns ErrKASUnreadable.
// MUST be called on the write turn, or before New returns: it records the last refusal.
func (s *Store) readKASDoc() (doc map[string]json.RawMessage, fresh bool, err error) {
	info, err := os.Stat(s.kasPath)
	if errors.Is(err, os.ErrNotExist) {
		s.kasRefused = ""
		return map[string]json.RawMessage{}, false, nil
	}
	if err != nil {
		return nil, false, s.refuseKAS(nil, err)
	}
	if info.Size() > kasFileMaxBytes {
		return nil, false, s.refuseKAS(info, fmt.Errorf("%d bytes is over the %d-byte merge cap", info.Size(), kasFileMaxBytes))
	}
	data, err := os.ReadFile(s.kasPath)
	if err != nil {
		return nil, false, s.refuseKAS(info, err)
	}
	data, empty := decodeUserJSON(data)
	if !empty {
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, false, s.refuseKAS(info, err)
		}
	}
	s.kasRefused = ""
	if doc == nil {
		return map[string]json.RawMessage{}, true, nil
	}
	return doc, false, nil
}

// refuseKAS returns the refusal, logged at Error the first time for one file state (size,
// modification time and cause) and at Debug while that state repeats. The path is safe to log;
// the file's CONTENT is not, and no caller passes it.
func (s *Store) refuseKAS(info os.FileInfo, cause error) error {
	key := cause.Error()
	if info != nil {
		key = fmt.Sprintf("%d:%d:%s", info.Size(), info.ModTime().UnixNano(), key)
	}
	msg := "mcp: kas config file is not a readable JSON object; leaving it unchanged and refusing the write"
	if key == s.kasRefused {
		slog.Debug(msg, "path", s.kasPath, "error", cause)
	} else {
		s.kasRefused = key
		slog.Error(msg, "path", s.kasPath, "error", cause)
	}
	return fmt.Errorf("%w: %w", ErrKASUnreadable, cause)
}

// utf8BOM is the byte-order mark a Windows editor prepends to a saved UTF-8 file.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// decodeUserJSON prepares a hand-editable JSON file for json.Unmarshal: it strips a leading
// UTF-8 BOM, and empty reports content that is only JSON whitespace (RFC 8259 §2: space, tab,
// LF, CR), which both readers take as no servers. \v and \f are not JSON whitespace, so
// bytes.TrimSpace would wave through a file JSON rejects.
func decodeUserJSON(data []byte) (doc []byte, empty bool) {
	doc = bytes.TrimPrefix(data, utf8BOM)
	return doc, len(bytes.Trim(doc, " \t\n\r")) == 0
}

// kasConfigPath is the file KAS reads for user-level MCP servers.
//
// HOME, not the workspace. KAS reads BOTH the home and workspace paths,
// and the workspace one sits inside the user's repo where it is a
// plausible accidental commit of a file holding OAuth client secrets.
func kasConfigPath() string {
	return workspace.KiroSettingsPath("mcp.json")
}
