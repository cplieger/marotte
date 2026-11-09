package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cplieger/go/filesafe"
	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
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

// steeringDocBody is the GET's payload. ETag is the save token, the identity representation's
// strong tag, which If-Match matches as a current representation's (RFC 9110 section 13.1.1).
// The ETag header is no carrier for it: a gzip-coded answer gets a distinct, weak tag there
// (section 8.8.3.3), and a weak tag never passes If-Match.
type steeringDocBody struct {
	Content string `json:"content"`
	ETag    string `json:"etag"`
}

// steeringConflictBody is the 409 payload: marotte's error envelope plus the two
// values the client needs to recover without a second round trip.
type steeringConflictBody struct {
	Error   string `json:"error"`
	ETag    string `json:"etag"`
	Content string `json:"content"`
}

// steeringSaveBody is a PUT's 200 payload: the ack plus the token for what the write left on
// disk. "" means no token could be read, and the caller keeps what it had. No ETag header: the
// stored document is not the request content (RFC 9110 section 9.3.4).
type steeringSaveBody struct {
	ETag string `json:"etag"`
	OK   bool   `json:"ok"`
}

// An ABSENT file is an empty document. It does NOT fail open (a save PUTs the whole textarea, so an
// empty answer for an unreadable file lets the first keystroke replace it). OpenRegularNoFollow refuses
// symlinks and FIFOs.
func steeringDoc(ctx context.Context, path string) (content, etag string, err error) {
	// Absolute because OpenRegularNoFollow requires it.
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", "", err
	}
	f, info, err := filesafe.OpenRegularNoFollow(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", steeringAbsentETag, nil
		}
		return "", "", err
	}
	defer func() { _ = f.Close() }()
	data, err := filesafe.ReadBoundedFile(ctx, f, maxSteeringBytes)
	if err != nil {
		return "", "", err
	}
	return string(data), steeringETag(info), nil
}

// Two same-length writes inside one timestamp tick share a token, failing toward accepting the
// second.
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
	httpserver.WriteJSON(w, steeringDocBody{Content: content, ETag: etag})
}

// steeringWriteAllowed gates a save on the caller's If-Match token, the document's whole
// concurrency control: another device or an agent may edit custom.md. The token is compared
// strongly against the stored document's, whatever coding the caller's GET was answered in.
func steeringWriteAllowed(w http.ResponseWriter, r *http.Request, path string) bool {
	if len(entityTags(r.Header, headerIfMatch)) == 0 {
		httpserver.WriteJSONStatus(w, http.StatusPreconditionRequired,
			httpserver.ErrorResponse{Error: msgSteeringIfMatchRequired})
		return false
	}
	content, etag, err := steeringDoc(r.Context(), path)
	if err != nil {
		httpreply.ServerError(w, msgSteeringUnreadable, err)
		return false
	}
	if ifMatchHolds(r, etag) {
		return true
	}
	httpserver.WriteJSONStatus(w, http.StatusConflict, steeringConflictBody{
		Error:   msgSteeringConflict,
		ETag:    etag,
		Content: content,
	})
	return false
}

// savedSteeringETag is the token for what the write left on disk, "" when it cannot be read.
func savedSteeringETag(r *http.Request, path string) string {
	_, etag, err := steeringDoc(r.Context(), path)
	if err != nil {
		slog.Warn("steering: saved, but the validator token could not be read back",
			"path", path, "error", logsafe.Field(err.Error()))
		return ""
	}
	return etag
}

func handleSteeringPut(w http.ResponseWriter, r *http.Request, path string) {
	httpserver.LimitBody(w, r, httpserver.MaxJSONBody)
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
		httpserver.WriteJSON(w, steeringSaveBody{OK: true, ETag: steeringAbsentETag})
		return
	}
	if r.Context().Err() != nil {
		return
	}
	// A non-nil error means the content did NOT land (500); res.Durable==false means it landed
	// with the dir fsync unconfirmed (the library already warned).
	res, err := filesafe.WriteFile(r.Context(), path, []byte(body.Content),
		filesafe.WithMode(0o600), filesafe.WithMkdirMode(0o700))
	if err != nil {
		httpreply.InternalError(w, err)
		return
	}
	if !res.Durable {
		slog.Warn("steering: saved but parent-dir fsync unconfirmed; not guaranteed durable across an immediate crash",
			"path", path)
	}
	httpserver.WriteJSON(w, steeringSaveBody{OK: true, ETag: savedSteeringETag(r, path)})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		handleSettingsGet(w, settings.AgentDirs{Workspace: s.workDir, Config: s.configDir}, s.kiroDefaults)
	case http.MethodPatch:
		s.handleSettingsWrite(w, r)
	default:
		httpreply.MethodNotAllowed(w, http.MethodGet, http.MethodPatch)
	}
}

// handleSettingsGet answers the EFFECTIVE settings: every rendered preference with defaults
// resolved underneath the stored file. It FAILS OPEN (an unreadable file warns and serves
// defaults); the write path refuses the same file, so the data stays protected. Unknown stored
// keys are not sent, and PATCH merges against the file, so they survive. Each stored additional
// directory is checked against dirs at this GET.
func handleSettingsGet(w http.ResponseWriter, dirs settings.AgentDirs, defaults kiroDefaultsReader) {
	path := filepath.Join(dirs.Config, settings.Filename)
	stored, err := settings.Stored(dirs.Config)
	if err != nil {
		slog.Warn("settings: serving defaults, stored config unreadable", "path", path, "error", logsafe.Field(err.Error()))
		effective := settings.EffectiveDefaults()
		fillKiroDefaults(&effective, defaults)
		httpserver.WriteJSON(w, effective)
		return
	}
	httpserver.WriteJSON(w, effectiveOf(stored, path, dirs, defaults))
}

// GET and the PATCH answer share this resolution, so a save never paints settings a later GET
// would resolve differently.
func effectiveOf(stored map[string]json.RawMessage, path string, dirs settings.AgentDirs, defaults kiroDefaultsReader) marotte.EffectiveSettings {
	effective, rejected := settings.EffectiveFrom(stored)
	if len(rejected) > 0 {
		// The key, never the value: a settings value may be a pasted token, and this line is logged.
		slog.Warn("settings: stored values did not fit their type, defaults applied",
			"path", path, "keys", rejected)
	}
	fillKiroDefaults(&effective, defaults)
	_, effective.AdditionalDirectoriesRefused = dirs.Assess(effective.AdditionalDirectories)
	return effective
}

// fillKiroDefaults reports kiro-cli's hooks.showStatus and its resolution for the three-state settings left unset; a
// chosen value is never shadowed by one.
func fillKiroDefaults(e *marotte.EffectiveSettings, defaults kiroDefaultsReader) {
	if defaults == nil {
		return
	}
	e.HookStatusShown = defaults.HookStatusShown()
	for key, d := range defaults.KiroDefaults() {
		if choice, threeState := settings.ThreeStateChoice(e, key); threeState && choice == settings.FeatureFollowKiro {
			e.KiroDefaults[key] = d
		}
	}
}

// msgSettingsUnreadable is a settings write's answer when the stored file could not be read:
// unreadable, and nothing was written over it.
const msgSettingsUnreadable = "config.json could not be read, so your settings were not overwritten"

// patchRefusedError is a refusal found while merging, under settings.Update's lock; it answers 400.
type patchRefusedError struct{ error }

func (r patchRefusedError) Unwrap() error { return r.error }

// There is no replacing arm (only GET and PATCH are served). The additional directories are
// judged against the document they replace, read under the same lock as the write.
func mergeSettingsPatch(dirs settings.AgentDirs, patch map[string]json.RawMessage) func(map[string]json.RawMessage) error {
	return func(doc map[string]json.RawMessage) error {
		if err := dirs.ValidatePatch(doc, patch); err != nil {
			return patchRefusedError{err}
		}
		maps.Copy(doc, patch)
		return nil
	}
}

// handleSettingsWrite merges the patch into config.json and answers the effective settings of
// the document it committed, resolved as a GET resolves it.
func (s *Server) handleSettingsWrite(w http.ResponseWriter, r *http.Request) {
	httpserver.LimitBody(w, r, httpserver.MaxJSONBody)
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
	for _, key := range []string{settings.KeySettingsEpoch, settings.KeySettingsRevision} {
		if _, ok := patch[key]; ok {
			httpreply.BadRequest(w, key+" is counted by the server and cannot be written")
			return
		}
	}
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
	if err := settings.ValidateTriggerCapsPatch(patch); err != nil {
		httpreply.BadRequest(w, err.Error())
		return
	}
	if r.Context().Err() != nil {
		return
	}
	dirs := settings.AgentDirs{Workspace: s.workDir, Config: s.configDir}
	path := filepath.Join(s.configDir, settings.Filename)
	merge := mergeSettingsPatch(dirs, patch)
	var committed map[string]json.RawMessage
	var answer marotte.EffectiveSettings
	_, err := settings.Update(r.Context(), s.configDir, func(doc map[string]json.RawMessage) error {
		committed = doc
		return merge(doc)
	}, func(ctx context.Context, evt marotte.ServerEvent) {
		// Resolved before the event leaves: a page adopts a read the event prompts over this answer
		// at the same version (settings-state.accepts), so the read must be no older.
		answer = effectiveOf(committed, path, dirs, s.kiroDefaults)
		s.agent.Broadcast(ctx, evt)
	})
	if err != nil {
		if rp := (patchRefusedError{}); errors.As(err, &rp) {
			httpreply.BadRequest(w, rp.Error())
			return
		}
		// Two 500 bodies: an unreadable document is the user's file, a failed write is ours.
		if errors.Is(err, settings.ErrUnreadable) {
			httpreply.ServerError(w, msgSettingsUnreadable, err)
			return
		}
		httpreply.ServerError(w, "config.json could not be saved", err)
		return
	}
	httpserver.WriteJSON(w, answer)
	// Durable: the write landed, so a closed tab must not leave bridges on the old values.
	s.agent.ReconcileSessionSettings(durable.Context(r.Context()))
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
