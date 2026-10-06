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

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logctl"
	"github.com/cplieger/marotte/internal/logsafe"
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
const maxSteeringBytes = 1 << 20

// msgSteeringUnreadable is a steering read's answer when custom.md exists and could not be
// read: unreadable, and nothing was written over it.
const msgSteeringUnreadable = "custom.md could not be read, so your instructions were not overwritten"

// msgSteeringConflict is the 409, answered with the fresh token and the document
// beside it so the reader's box can be re-seeded from the refusal itself.
const msgSteeringConflict = "custom.md changed since you loaded it, so your text was not saved"

// msgSteeringIfMatchRequired is the 428: no validator token was offered, so this
// write cannot be told from one that would overwrite a newer document.
const msgSteeringIfMatchRequired = "If-Match is required to save custom.md"

const (
	headerETag    = "ETag"
	headerIfMatch = "If-Match"
)

// steeringAbsentETag is the token for "no file at custom.md", a real state (an empty document
// is stored as an absent file), so a fresh volume's first save has a token to send.
const steeringAbsentETag = `"absent"`

// steeringConflictBody is the 409 payload: marotte's error envelope plus the two
// values the client needs to recover without a second round trip.
type steeringConflictBody struct {
	Error   string `json:"error"`
	ETag    string `json:"etag"`
	Content string `json:"content"`
}

// steeringSaveBody is a PUT's 200 payload: the ack plus the validator for what the write left
// on disk (also in the ETag header; the action framework reads only the body). "" means no
// token could be read, and the caller keeps what it had.
type steeringSaveBody struct {
	ETag string `json:"etag"`
	OK   bool   `json:"ok"`
}

// steeringDoc reads custom.md and derives its validator token; an ABSENT file is an empty
// document. It does NOT fail open (a save PUTs the whole textarea, so an empty answer for an
// unreadable file lets the first keystroke replace it). OpenRegular refuses symlinks and FIFOs.
func steeringDoc(ctx context.Context, path string) (content, etag string, err error) {
	// Absolute because OpenRegular requires it.
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

// steeringETag renders the token for a present file from its mtime and size; two same-length
// writes inside one timestamp tick share a token, failing toward accepting the second.
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

// steeringWriteAllowed gates a save on the caller's If-Match token, the document's whole
// concurrency control: another device or an agent may edit custom.md.
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

// publishSteeringETag sets the ETag for what the write left on disk and RETURNS it for the
// body. An unreadable token leaves the header unset and answers "".
func publishSteeringETag(w http.ResponseWriter, r *http.Request, path string) string {
	_, etag, err := steeringDoc(r.Context(), path)
	if err != nil {
		slog.Warn("steering: saved, but the validator token could not be read back",
			"path", path, "error", logsafe.Field(err.Error()))
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
	// Whitespace-only content removes the file; kiro-cli would otherwise load an empty file.
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
	// A non-nil error means the content did NOT land (500); res.Durable==false means it landed
	// with the dir fsync unconfirmed (the library already warned).
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
	// Before WriteJSON commits the status: publishSteeringETag sets a header.
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

// handleSettingsGet answers the EFFECTIVE settings: every rendered preference with defaults
// resolved underneath the stored file. It FAILS OPEN (an unreadable file warns and serves
// defaults); the write path refuses the same file, so the data stays protected. Unknown stored
// keys are not sent, and PATCH merges against the file, so they survive.
func handleSettingsGet(w http.ResponseWriter, path string) {
	stored, err := readStoredSettings(path)
	if err != nil {
		slog.Warn("settings: serving defaults, stored config unreadable", "path", path, "error", logsafe.Field(err.Error()))
		webhttp.WriteJSON(w, settings.EffectiveDefaults())
		return
	}
	effective, rejected := settings.EffectiveFrom(stored)
	if len(rejected) > 0 {
		// The key, never the value: a settings value may be a pasted token, and this line is logged.
		slog.Warn("settings: stored values did not fit their type, defaults applied",
			"path", path, "keys", rejected)
	}
	webhttp.WriteJSON(w, effective)
}

// maxSettingsBytes caps the existing settings file read+merged on PATCH so a
// corrupt or runaway config can't pin memory.
const maxSettingsBytes = 1 << 20

// msgSettingsUnreadable is a settings write's answer when the stored file could not be read:
// unreadable, and nothing was written over it.
const msgSettingsUnreadable = "config.json could not be read, so your settings were not overwritten"

// readStoredSettings reads and parses config.json for the READ side; an ABSENT file is an
// empty map and nil error, every other fault an error. Shared mechanics with the write, not
// its policy: settings.Update refuses an unreadable document, a read fails open.
// OpenRegular because os.Open blocks forever on a FIFO and follows a final symlink.
func readStoredSettings(path string) (map[string]json.RawMessage, error) {
	// Absolute because OpenRegular requires it.
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
	// A top-level `null` unmarshals to a NIL map with no error, and maps.Copy onto nil panics.
	if existing == nil {
		return nil, fmt.Errorf("settings: %s contains a top-level null, not an object", abs)
	}
	return existing, nil
}

// mergeSettingsPatch merges the request body's keys over the document on disk, as the merge
// step of one settings.Update. There is no replacing arm (only GET and PATCH are served).
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
	// Warn, don't reject, on unknown keys; they persist verbatim (readers use Field[T]).
	patchKeys := make([]string, 0, len(patch))
	for k := range patch {
		patchKeys = append(patchKeys, k)
	}
	_ = settings.WarnUnknownKeys(patchKeys, r.Method+" "+r.URL.Path)
	// Refuse an ignore-file entry kiro-cli would refuse, before the write, or config.json would
	// disagree with what is enforced.
	if msg, ok := agentIgnorePatchRefusal(patch); !ok {
		httpreply.BadRequest(w, msg)
		return
	}
	if err := settings.ValidateCompactionPatch(patch); err != nil {
		httpreply.BadRequest(w, err.Error())
		return
	}
	if err := settings.ValidateAgentPatch(patch); err != nil {
		httpreply.BadRequest(w, err.Error())
		return
	}
	if r.Context().Err() != nil {
		return
	}
	merged, err := settings.Update(r.Context(), s.configDir, mergeSettingsPatch(patch))
	if err != nil {
		// Two 500 bodies: an unreadable document is the user's file, a failed write is ours.
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
		// Durable: the write landed, so a closed tab must not leave bridges on the old list.
		s.agent.PushAgentIgnoreFiles(durable.Context(r.Context()))
	}
	if _, touched := patch[settings.KeyTerminalCommandTimeoutMs]; touched {
		s.agent.PushTerminalSettings(durable.Context(r.Context()))
	}
	if _, touched := patch[settings.KeyContentCollectionEnabled]; touched {
		s.agent.PushContentCollection(durable.Context(r.Context()))
	}
	s.renderMCPForPatch(durable.Context(r.Context()), patch)
}

// renderMCPForPatch re-renders KAS's MCP config when the patch touched the MCP
// wait setting, which the render reads. ctx must be durable: the write has
// already landed. A failure still answers 200, because the setting has landed,
// so the log names the consequence instead.
func (s *Server) renderMCPForPatch(ctx context.Context, patch map[string]json.RawMessage) {
	if _, touched := patch[settings.KeyMCPWaitForReady]; !touched || s.mcpRender == nil {
		return
	}
	if err := s.mcpRender.RenderKASConfig(ctx); err != nil {
		slog.Error("settings: re-rendering the MCP config failed; the previous MCP wait setting stands until the next MCP change or restart",
			"error", err)
	}
}

// agentIgnorePatchRefusal reports whether a patch's agent_ignore_files is one kiro-cli will
// enforce, and the message otherwise. A malformed VALUE is refused too (the GET would read it
// as the default). A patch without the key passes.
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

// syncPushPreferences forwards the patch's notification toggles to the push service. The
// kind set is DERIVED from push.Kinds(). A key the patch lacks resolves against the PERSISTED
// settings before the registry default: seeding defaults would re-enable a kind the user
// turned off. PushKindPermission has no settings key, so nothing here can lower it.
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
	// The MASTER switch, resolved patch -> persisted and applied LAST so nothing re-widens it;
	// it is enforced here, not only by the browser deleting its subscription. Only an explicit
	// false zeroes: its default is off while each kind has its own, so absent is not a decision.
	if notificationsRefused(patch, &persisted) {
		for kind := range prefs {
			prefs[kind] = false
		}
	}
	s.push.SetPreferences(prefs)
}

// notificationsRefused reports whether the master switch is explicitly OFF, patch first,
// then persisted. Absent or malformed is not a refusal.
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

// lazySettings reads a settings document at most once, only when a key is asked for. A read
// failure answers absent for every key, leaving the caller's default. Single-goroutine.
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
				"error", logsafe.Field(err.Error()))
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
