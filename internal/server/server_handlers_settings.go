package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logctl"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/push"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/webhttp/v3"
)

func (s *Server) handleSteering(w http.ResponseWriter, r *http.Request) {
	path := s.steering.CustomPath()
	switch r.Method {
	case http.MethodGet:
		handleSteeringGet(w, r, path)
	case http.MethodPut:
		handleSteeringPut(w, r, path)
	default:
		httpreply.MethodNotAllowed(w, http.MethodGet, http.MethodPut)
	}
}

// maxSteeringBytes caps the custom.md read so a runaway file cannot pin memory.
const maxSteeringBytes = 1 << 20 // 1 MiB

// msgSteeringUnreadable is what a steering read answers when a file stands at
// custom.md and could not be read. It states both halves the reader needs: the
// file could not be read, and nothing was written over it.
const msgSteeringUnreadable = "custom.md could not be read; your instructions were not overwritten"

// msgSteeringConflict is the 409, answered with the fresh token and the document
// beside it so the reader's box can be re-seeded from the refusal itself.
const msgSteeringConflict = "custom.md changed since you loaded it; your text was not saved"

// msgSteeringIfMatchRequired is the 428: no validator token was offered, so this
// write cannot be told from one that would overwrite a newer document.
const msgSteeringIfMatchRequired = "If-Match is required to save custom.md"

const (
	headerETag    = "ETag"
	headerIfMatch = "If-Match"
)

// steeringAbsentETag is the token for "no file stands at custom.md", which is a
// real state rather than a missing one: an empty document is stored as an absent
// file, so the first save of a fresh volume needs a token to send.
const steeringAbsentETag = `"absent"`

// steeringConflictBody is the 409 payload: marotte's error envelope plus the two
// values the client needs to recover without a second round trip.
type steeringConflictBody struct {
	Error   string `json:"error"`
	ETag    string `json:"etag"`
	Content string `json:"content"`
}

// steeringSaveBody is the 200 payload for a PUT: the acknowledgement plus the
// validator for what the write just left on disk. The ETag header carries the
// same value; the body carries it too because the client's action framework
// decodes a body and cannot reach a response header.
//
// No omitempty: the field is always present, and an empty string is the explicit
// "no token could be read", which a caller answers by keeping whatever it had
// rather than adopting one that names nothing.
type steeringSaveBody struct {
	ETag string `json:"etag"`
	OK   bool   `json:"ok"`
}

// steeringDoc reads custom.md and derives its validator token. An ABSENT file
// yields an empty document and a nil error: "no custom instructions" is expressed
// as no file.
//
// It does NOT fail open, because a save PUTs the whole textarea as the whole
// document, so answering empty for a file that exists and could not be read is
// what lets the first keystroke replace it. OpenRegular refuses the symlink the
// write path refuses, and cannot block in open(2) on a FIFO.
func steeringDoc(ctx context.Context, path string) (content, etag string, err error) {
	// Absolute because OpenRegular requires it; a relative path resolved against the
	// process cwd means the same thing either way.
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", "", err
	}
	f, info, err := atomicfile.OpenRegular(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", steeringAbsentETag, nil
		}
		return "", "", err
	}
	defer func() { _ = f.Close() }()
	data, err := atomicfile.ReadBoundedFile(ctx, f, maxSteeringBytes)
	if err != nil {
		return "", "", err
	}
	return string(data), steeringETag(info), nil
}

// steeringETag renders the token for a present file from its mtime and size.
// Two writes inside one filesystem timestamp tick that leave the same length
// share a token, which fails toward accepting the second write — the direction a
// validator over metadata cannot avoid without hashing the content.
func steeringETag(info fs.FileInfo) string {
	return fmt.Sprintf("%q", strconv.FormatInt(info.ModTime().UnixNano(), 10)+"-"+strconv.FormatInt(info.Size(), 10))
}

func handleSteeringGet(w http.ResponseWriter, r *http.Request, path string) {
	content, etag, err := steeringDoc(r.Context(), path)
	if err != nil {
		httpreply.ServerError(w, msgSteeringUnreadable, err)
		return
	}
	w.Header().Set(headerETag, etag)
	webhttp.WriteJSON(w, map[string]string{"content": content})
}

// steeringWriteAllowed gates a save on the caller's If-Match token, which is the
// whole of the concurrency control on this document. A second device or an agent
// editing custom.md — which the generated environment.md tells it to do — would
// otherwise be overwritten by a stale panel's next keystroke.
func steeringWriteAllowed(w http.ResponseWriter, r *http.Request, path string) bool {
	offered := strings.TrimSpace(r.Header.Get(headerIfMatch))
	if offered == "" {
		webhttp.WriteJSONStatus(w, http.StatusPreconditionRequired,
			webhttp.ErrorResponse{Error: msgSteeringIfMatchRequired})
		return false
	}
	content, etag, err := steeringDoc(r.Context(), path)
	if err != nil {
		httpreply.ServerError(w, msgSteeringUnreadable, err)
		return false
	}
	if offered == etag {
		return true
	}
	w.Header().Set(headerETag, etag)
	webhttp.WriteJSONStatus(w, http.StatusConflict, steeringConflictBody{
		Error:   msgSteeringConflict,
		ETag:    etag,
		Content: content,
	})
	return false
}

// publishSteeringETag hands back the token for what the write just left on disk,
// so a debounced save can keep writing without a GET between keystrokes. It sets
// the header and RETURNS the token for the response body, because those are the
// same fact for two readers. A token that cannot be read leaves the header unset
// and answers "" rather than guessing at one.
func publishSteeringETag(w http.ResponseWriter, r *http.Request, path string) string {
	_, etag, err := steeringDoc(r.Context(), path)
	if err != nil {
		slog.Warn("steering: saved, but the validator token could not be read back",
			"path", path, "error", err)
		return ""
	}
	w.Header().Set(headerETag, etag)
	return etag
}

func handleSteeringPut(w http.ResponseWriter, r *http.Request, path string) {
	webhttp.LimitBody(w, r, webhttp.MaxJSONBody)
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpreply.BadRequest(w, "bad request")
		return
	}
	if !steeringWriteAllowed(w, r, path) {
		return
	}
	// Empty content (whitespace only) means "no custom instructions":
	// remove the file instead of writing an empty one. Otherwise
	// kiro-cli would include the empty file on every agent load.
	if strings.TrimSpace(body.Content) == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			httpreply.InternalError(w, err)
			return
		}
		w.Header().Set(headerETag, steeringAbsentETag)
		webhttp.WriteJSON(w, steeringSaveBody{OK: true, ETag: steeringAbsentETag})
		return
	}
	if r.Context().Err() != nil {
		return
	}
	// Atomic write via temp+fsync+rename+dir-fsync. WithMkdirMode
	// auto-creates the parent dir (replacing the old SaveBytes
	// behavior); WithMode keeps the 0o600 perm its sibling
	// environment.md already carries. Replaces a bare os.WriteFile
	// that could leave a truncated file on a crash mid-write. A
	// non-nil error means the content did NOT land — surface it as
	// 500. A nil error with res.Durable==false means the content is
	// on disk but the parent-dir fsync was unconfirmed; log and
	// proceed (the library already logged the fsync failure at Warn).
	res, err := atomicfile.WriteFile(r.Context(), path, []byte(body.Content),
		atomicfile.WithMode(0o600), atomicfile.WithMkdirMode(0o700))
	if err != nil {
		httpreply.InternalError(w, err)
		return
	}
	if !res.Durable {
		slog.Warn("steering: saved but parent-dir fsync unconfirmed; not guaranteed durable across an immediate crash",
			"path", path)
	}
	// Separate statement, not an argument: publishSteeringETag sets a header, so
	// it has to run before WriteJSON commits the status.
	etag := publishSteeringETag(w, r, path)
	webhttp.WriteJSON(w, steeringSaveBody{OK: true, ETag: etag})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	path := filepath.Join(s.configDir, settings.Filename)
	switch r.Method {
	case http.MethodGet:
		handleSettingsGet(w, path)
	case http.MethodPatch:
		s.handleSettingsWrite(w, r)
	default:
		httpreply.MethodNotAllowed(w, http.MethodGet, http.MethodPatch)
	}
}

// handleSettingsGet answers the EFFECTIVE settings: the value in force for every
// preference the client renders, defaults resolved underneath the stored file.
//
// It used to echo the file's bytes verbatim when it could read them and emit
// settings.Default() when it could not, which made the response shape depend on
// the file's state. A stored config.json only ever accumulates keys somebody
// explicitly set, so every untouched key was absent from the response and the
// client had to decide what absence meant — which is how it came to carry copies
// of these defaults, and how the agent-ignore list came to render empty while the
// read filter was applying two patterns.
//
// It FAILS OPEN. An unreadable file logs one Warn and serves the defaults, rather
// than refusing: this is a dev-box container whose operator reshapes /config by
// hand, so a surface that shows defaults and says the file is bad gives them more
// to work with than one that shows nothing. The data is protected by the other
// half of the asymmetry — the write path REFUSES the same file (see
// handleSettingsWrite's ErrUnreadable arm), so a user who edits after this gets a
// visible save failure instead of an overwrite that destroys what it could not
// read. Read open, write closed.
//
// Unknown stored keys do not reach the client, which the byte passthrough used to
// allow. Nothing in static-src consumes one, and PATCH merges against the FILE
// rather than against this response, so they survive a round trip untouched.
func handleSettingsGet(w http.ResponseWriter, path string) {
	stored, err := readStoredSettings(path)
	if err != nil {
		slog.Warn("settings: serving defaults, stored config unreadable", "path", path, "error", err)
		webhttp.WriteJSON(w, settings.EffectiveDefaults())
		return
	}
	effective, rejected := settings.EffectiveFrom(stored)
	if len(rejected) > 0 {
		// The key, never the value: a settings file can hold a token somebody pasted
		// into the wrong field, and this line goes to Loki.
		slog.Warn("settings: stored values did not fit their type, defaults applied",
			"path", path, "keys", rejected)
	}
	webhttp.WriteJSON(w, effective)
}

// maxSettingsBytes caps the existing settings file read+merged on PATCH so a
// corrupt or runaway config can't pin memory.
const maxSettingsBytes = 1 << 20 // 1 MiB

// msgSettingsUnreadable is what a settings write answers when it could not read
// what is already stored. It states both halves the user needs: the file could
// not be read, and nothing was written over it — so the preferences they can no
// longer see are still on disk, and the remedy is config.json itself.
const msgSettingsUnreadable = "config.json could not be read; your settings were not overwritten"

// readStoredSettings reads and parses the on-disk settings document for the READ
// side: the GET resolves the effective view over the result, and lazySettings
// answers one notification key out of it. An ABSENT file is the one outcome that
// yields an empty map and a nil error, because a fresh volume has no config.json.
//
// Every other outcome is an error — a stat or read fault, a non-regular file at
// the name, an oversize file, invalid JSON, a top-level null.
//
// The read and the write share these MECHANICS and not the failure POLICY, which
// is the distinction to preserve when editing either. settings.Update REFUSES an
// unreadable document; a read FAILS OPEN and serves defaults, because showing an
// operator the values in force plus a warning beats showing them nothing, and
// because the write's refusal is what actually protects the file.
//
// OpenRegular, not os.Open: os.Open on a FIFO blocks in open(2) with no context
// deadline to rescue it, so one FIFO planted at config.json would strand a
// handler goroutine per request. It also refuses a symlink at the final
// component, which the read side in internal/settings already refuses, so the two
// halves of this file agree about what may stand in for it.
func readStoredSettings(path string) (map[string]json.RawMessage, error) {
	// Absolute because OpenRegular requires it. A relative configDir resolved
	// against the process cwd under os.Open and filepath.Abs preserves exactly
	// that, so no deployment's meaning changes.
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	f, info, err := atomicfile.OpenRegular(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]json.RawMessage{}, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if info.Size() > maxSettingsBytes {
		return nil, fmt.Errorf("settings: %s is %d bytes, over the %d-byte cap", abs, info.Size(), maxSettingsBytes)
	}
	data := make([]byte, info.Size())
	if _, err := io.ReadFull(f, data); err != nil {
		return nil, err
	}
	existing := make(map[string]json.RawMessage)
	if err := json.Unmarshal(data, &existing); err != nil {
		return nil, err
	}
	// A stored top-level `null` is the one JSON value that parses into the wrong Go
	// state without an error: json.Unmarshal sets the map to NIL and returns nil,
	// overriding the make above. maps.Copy onto a nil map then panics, so a
	// four-byte config.json used to make every PATCH fail with an opaque 500 (via
	// webhttp.Recoverer). `[]` and `"str"` both error correctly on their own; null
	// is the gap. Refusing it here is also the right answer for the GET, which
	// cannot show a document that says nothing.
	if existing == nil {
		return nil, fmt.Errorf("settings: %s contains a top-level null, not an object", abs)
	}
	return existing, nil
}

// mergeSettingsPatch merges the request body's keys over the document on disk, as
// the merge step of one settings.Update. There is no replacing arm: handleSettings
// answers 405 to every method but GET and PATCH, which is cheaper to keep correct
// than a list of keys a replace would have to carry over.
func mergeSettingsPatch(patch map[string]json.RawMessage) func(map[string]json.RawMessage) error {
	return func(doc map[string]json.RawMessage) error {
		maps.Copy(doc, patch)
		return nil
	}
}

func (s *Server) handleSettingsWrite(w http.ResponseWriter, r *http.Request) {
	webhttp.LimitBody(w, r, webhttp.MaxJSONBody)
	var patch map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		httpreply.BadRequest(w, "invalid json")
		return
	}
	// Warn (don't reject) on unknown top-level keys so a typo
	// or stale frontend write surfaces in operator logs without
	// breaking forward-compatible additions. The patch still
	// gets persisted verbatim — readers tolerate unknown keys
	// via the Field[T] pattern.
	patchKeys := make([]string, 0, len(patch))
	for k := range patch {
		patchKeys = append(patchKeys, k)
	}
	_ = settings.WarnUnknownKeys(patchKeys, r.Method+" "+r.URL.Path)
	// Refuse an ignore-file entry kiro-cli would refuse, so the panel cannot claim
	// a name is enforced while kiro-cli skips it. Before the write: this is the one
	// value in the document whose validation rule belongs to a foreign system, and
	// persisting one it rejects makes config.json disagree with what is enforced.
	if msg, ok := agentIgnorePatchRefusal(patch); !ok {
		httpreply.BadRequest(w, msg)
		return
	}
	if r.Context().Err() != nil {
		return
	}
	merged, err := settings.Update(r.Context(), s.configDir, mergeSettingsPatch(patch))
	if err != nil {
		// Two 500s with different bodies: an unreadable document is the user's own
		// file and says so, where a failed write is this server's fault.
		if errors.Is(err, settings.ErrUnreadable) {
			httpreply.ServerError(w, msgSettingsUnreadable, err)
			return
		}
		httpreply.ServerError(w, "config.json could not be saved", err)
		return
	}
	webhttp.Ok(w)
	s.agent.Broadcast(r.Context(), marotte.NewEvent(marotte.EventSettingsUpdated, "", marotte.SettingsUpdatedPayload{}))
	s.syncPushPreferences(merged)
	syncDebugLogs(merged)
	if _, touched := patch[settings.KeyAgentIgnoreFiles]; touched {
		// durable.Context because the write has already landed: a reader closing the
		// tab mid-PATCH would otherwise leave every live bridge enforcing the
		// previous list with nothing to correct it before its next spawn.
		s.agent.PushAgentIgnoreFiles(durable.Context(r.Context()))
	}
	if raw, touched := patch[settings.KeySecurityProfile]; touched {
		// This route is the SECOND writer of the rung, and the picker's own endpoint
		// is where the re-render was wired. Without this arm the panel resolves the
		// posture live on every GET /api/mcp and says suspended while KAS's mcp.json
		// still carries autoApprove for that server — the wider grant standing under
		// copy that denies it. durable.Context for the reason stated just above.
		s.renderMCPForProfile(durable.Context(r.Context()), securityProfileAttribution(raw))
	}
}

// securityProfileAttribution is the profile id the re-render names in its failure
// log, empty when the patched value is not a string. Attribution only: the
// renderer resolves the rung by reading the setting itself.
func securityProfileAttribution(raw json.RawMessage) string {
	var id string
	if json.Unmarshal(raw, &id) != nil {
		return ""
	}
	return id
}

// agentIgnorePatchRefusal reports whether a settings patch's agent_ignore_files
// value is one kiro-cli will enforce, and the message to answer with when it is
// not. A patch that does not carry the key passes.
//
// It refuses a malformed VALUE as well as an invalid entry: the GET resolves a
// wrongly-typed stored value to the default, so accepting one here would answer
// 200 to a write whose effect is to empty the list.
func agentIgnorePatchRefusal(patch map[string]json.RawMessage) (msg string, ok bool) {
	raw, touched := patch[settings.KeyAgentIgnoreFiles]
	if !touched {
		return "", true
	}
	var entries []string
	if err := json.Unmarshal(raw, &entries); err != nil {
		return settings.KeyAgentIgnoreFiles + " must be a list of ignore file names", false
	}
	for _, entry := range entries {
		if err := settings.ValidAgentIgnoreEntry(entry); err != nil {
			return err.Error(), false
		}
	}
	return "", true
}

// syncPushPreferences reads notification preference toggles from the settings
// patch and forwards them to the push service.
//
// It DERIVES the kind set from push.Kinds() rather than naming the kinds here.
// This used to be a hand-written map with both kinds spelled out and a single
// `if` reading one key, which made it a THIRD copy of the kind set beside
// marotte.pushKinds and push.kindRegistry — so a newly added kind's toggle persisted
// to config.json and then never reached the running service until the next SSE
// reconnect happened to call ReloadPreferences.
//
// A key the patch does not carry is resolved against the PERSISTED settings
// before it falls back to the registry default, which is the same order
// push.loadPreferences reads (disk, else default) with the patch as the freshest
// layer on top. Seeding the default for an absent key and overriding only on a
// hit is correct only while the argument happens to be the whole merged document:
// on a genuinely sparse map it re-enables every kind the caller did not mention,
// which for a preference the user turned OFF is the one direction that must never
// happen silently. The function owns that resolution rather than trusting its
// caller's shape.
//
// PushKindPermission has no branch that can lower it: its registry entry declares
// no settings key, so neither lookup below can find a value to read for it and
// this write path cannot silence a turn-blocking ask however the body was
// assembled. See the "no notify_permission key" note in
// internal/settings/defaults.go.
func (s *Server) syncPushPreferences(patch map[string]json.RawMessage) {
	kinds := push.Kinds()
	prefs := make(map[marotte.PushKind]bool, len(kinds))
	persisted := lazySettings{path: filepath.Join(s.configDir, settings.Filename)}
	for _, k := range kinds {
		prefs[k.Kind] = k.DefaultOn
		if k.SettingsKey == "" {
			continue // an unconfigurable floor: no key, so nothing to read
		}
		v, ok := patch[k.SettingsKey]
		if !ok {
			if v, ok = persisted.lookup(k.SettingsKey); !ok {
				continue
			}
		}
		var on bool
		if json.Unmarshal(v, &on) == nil {
			prefs[k.Kind] = on
		}
	}
	// The MASTER switch, resolved through the same patch -> persisted ladder the kinds
	// use and applied LAST so nothing above can re-widen it.
	//
	// It had no server reader at all: grep `KeyNotificationsEnabled` and the three hits
	// are its declaration, its KnownKeys entry and its EffectiveSettings decoder.
	// `notifications_enabled` reached the server only INDIRECTLY, through the browser's
	// own `unregisterPush` deleting the subscription, so the claim in
	// internal/settings/defaults.go that it "turns everything off together" held only
	// while a client was connected to act on it. Three ways that indirection fails:
	// another device stays subscribed while this workspace's setting says off,
	// `unregisterPush` is best-effort with no retry, and nothing prunes a subscription
	// whose browser never comes back. `unregisterPush` remains right regardless — a
	// browser should not hold a subscription it does not want — and this is the other
	// half, enforced where the per-kind switches already are.
	//
	// ONLY AN EXPLICIT FALSE ZEROES, and the polarity is the whole reason this is a
	// separate resolution rather than another row in the loop above. This key's default
	// is OFF (it means "the reader has not opted in"), while each keyed kind carries its
	// own declared default (settings.Default*, two ON and one OFF) answering "if the
	// master is on, which kinds", so treating an ABSENT master as a decision would
	// silence every kind for every workspace that has never touched Settings, whatever
	// each kind's own default says — turning an absence into a refusal. The population
	// with an absent key also has no
	// subscribers by construction: `enableEverything` is the only path that starts a
	// subscription and it PATCHes `notifications_enabled: true` in the same body.
	//
	// `permission` is zeroed with the rest: that is what "everything off together"
	// means, and the browser already has no permission-notice path with the master
	// switch off, since notifyIfHidden's first gate is that value.
	if notificationsRefused(patch, &persisted) {
		for kind := range prefs {
			prefs[kind] = false
		}
	}
	s.push.SetPreferences(prefs)
}

// notificationsRefused reports whether the master notifications switch is explicitly
// OFF: the PATCH first (so a body touching only that key takes effect immediately),
// then the persisted document. An absent key is NOT a refusal — see the polarity note
// at the call site.
//
// A malformed value is not a refusal either, matching `settings.decodeInto`'s rule at
// the read path: a value the server cannot parse is not the reader asking for silence.
func notificationsRefused(patch map[string]json.RawMessage, persisted *lazySettings) bool {
	raw, ok := patch[settings.KeyNotificationsEnabled]
	if !ok {
		if raw, ok = persisted.lookup(settings.KeyNotificationsEnabled); !ok {
			return false
		}
	}
	var enabled bool
	if json.Unmarshal(raw, &enabled) != nil {
		return false
	}
	return !enabled
}

// lazySettings reads a settings document at most once, and only when a key is
// actually asked for: the merged document every current syncPushPreferences caller
// passes needs no disk read at all.
//
// A read failure answers absent for every key, leaving the caller's default
// standing — the only answer available once the stored value is unreachable, and
// it is reached only after the write this runs behind already succeeded, so a
// file that cannot be read here is a state no settings write produced.
//
// Single-goroutine by construction: each syncPushPreferences call builds its own.
type lazySettings struct {
	doc  map[string]json.RawMessage
	path string
	read bool
}

func (l *lazySettings) lookup(key string) (json.RawMessage, bool) {
	if !l.read {
		l.read = true
		doc, err := readStoredSettings(l.path)
		if err != nil {
			slog.Warn("settings: notification preferences fell back to defaults; config.json could not be read",
				"error", err)
		}
		l.doc = doc
	}
	v, ok := l.doc[key]
	return v, ok
}

// syncDebugLogs flips the process-wide slog level when the user
// toggles the Debug logs setting.
func syncDebugLogs(patch map[string]json.RawMessage) {
	v, ok := patch[settings.KeyDebugLogs]
	if !ok {
		return
	}
	var on bool
	if err := json.Unmarshal(v, &on); err != nil {
		return
	}
	logctl.SetDebug(on)
}
