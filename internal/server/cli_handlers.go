package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/sanitize"
	"github.com/cplieger/marotte/internal/version"
	"github.com/cplieger/webhttp/v3"
)

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	if !httpreply.RequireMethod(w, r, http.MethodGet) {
		return
	}
	payload := map[string]string{"marotte": version.Build}
	ctx, cancel := context.WithTimeout(r.Context(), s.cliTimeouts.Version)
	defer cancel()
	if out, err := s.cliRunner.Run(ctx, "--version"); err == nil {
		payload["kiro_cli"] = strings.TrimSpace(string(out))
	}
	webhttp.WriteJSON(w, payload)
}

func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cliTimeouts.Diagnostics)
	defer cancel()
	// STDOUT only (stderr is logged) and capped so a runaway dump cannot bloat the response.
	out, truncated, err := s.cliRunner.RunStdoutCapped(ctx, diagnosticsMaxBytes, "diagnostic", "--force", "--format", "json-pretty")
	if err != nil {
		// 502, not 200-with-an-error-body: the client's action framework classifies by STATUS.
		slog.Warn("diagnostics: kiro-cli exec failed", "error", logsafe.Field(err.Error()))
		webhttp.WriteJSONStatus(w, http.StatusBadGateway,
			httpreply.ErrorJSON("diagnostic command failed"))
		return
	}
	report := sanitize.Output(string(out))
	if truncated {
		report += "\n\n[truncated]"
	}
	webhttp.WriteJSON(w, map[string]string{"report": report})
}

func (s *Server) handleKiroSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.readKiroSettings(w, r)
	case http.MethodPut:
		s.writeKiroSetting(w, r)
	default:
		httpreply.MethodNotAllowed(w, http.MethodGet, http.MethodPut)
	}
}

// readKiroSettings answers GET /api/kiro-settings?keys=a,b,c (every allowlisted key when
// absent) as {"settings": {key: value}}. One `settings list` spawn answers it, falling back
// per key for a key it lacks (pre-2.20.2). ONE DEADLINE bounds every spawn; a key the
// deadline beats answers "", rendered as the default.
func (s *Server) readKiroSettings(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if unknownKiroSettingsQuery(q) {
		httpreply.BadRequest(w, "unknown query parameter")
		return
	}
	keys := requestedKiroSettings(q.Get(kiroSettingsKeysParam))
	if len(keys) == 0 {
		httpreply.BadRequest(w, "unknown setting key")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cliTimeouts.Settings)
	defer cancel()
	listed := s.readKiroSettingsList(ctx)
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		value, ok := listed[key]
		if !ok {
			value = s.readOneKiroSetting(ctx, key)
		}
		out[key] = value
	}
	s.overlayHeldKiroPrefs(out)
	webhttp.WriteJSON(w, map[string]any{"settings": out})
}

// overlayHeldKiroPrefs reports the user's own value for each setting a lock
// pins, or whose restore is still pending, since kiro-cli holds the
// organization's value there. The client paints the lock over it and shows it
// again on unlock.
func (s *Server) overlayHeldKiroPrefs(out map[string]string) {
	s.heldKiroPrefs.mu.Lock()
	defer s.heldKiroPrefs.mu.Unlock()
	for key, pref := range s.heldKiroPrefs.held {
		if _, asked := out[key]; asked && pref != "" {
			out[key] = pref
		}
	}
}

// readKiroSettingsList reads the whole settings document in one spawn, or nil when kiro-cli
// cannot answer. STDOUT only, because the bytes are parsed as JSON. The deadline is the caller's.
func (s *Server) readKiroSettingsList(ctx context.Context) map[string]string {
	out, truncated, err := s.cliRunner.RunStdoutCapped(ctx, settingsListMaxBytes, settingsListArgs...)
	if err != nil {
		slog.Debug("kiro-cli settings list failed; reading the requested keys one at a time",
			"error", logsafe.Field(err.Error()))
		return nil
	}
	if truncated {
		// A truncated document is not JSON; say which of the two went wrong.
		slog.Warn("kiro-cli settings list exceeded its cap", "cap", settingsListMaxBytes)
		return nil
	}
	listed, err := parseKiroSettingsList(out)
	if err != nil {
		slog.Debug("kiro-cli settings list is not a JSON object; reading the requested keys one at a time",
			"error", logsafe.Field(err.Error()))
		return nil
	}
	return listed
}

// readOneKiroSetting reads a single setting with the per-key invocation, answering "" (unset)
// when kiro-cli declines. The deadline is the caller's.
func (s *Server) readOneKiroSetting(ctx context.Context, key string) string {
	out, err := s.cliRunner.Run(ctx, "settings", key)
	if err != nil {
		return ""
	}
	return parseKiroSettingOutput(string(out))
}

// writeKiroSetting serves PUT /api/kiro-settings: one allowlisted key, one
// validated value.
func (s *Server) writeKiroSetting(w http.ResponseWriter, r *http.Request) {
	webhttp.LimitBody(w, r, webhttp.MaxJSONBody)
	var body struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpreply.BadRequest(w, "bad request")
		return
	}
	key := safeKiroSetting(body.Key)
	if key == "" {
		httpreply.BadRequest(w, "unknown setting key")
		return
	}
	meta := allowedKiroSettings[body.Key]
	value := safeKiroSettingValueFor(body.Value, meta.Kind)
	if value == "" {
		httpreply.BadRequest(w, "invalid setting value")
		return
	}
	// Held across the write so a lock applied meanwhile cannot be overwritten by it.
	s.heldKiroPrefs.mu.Lock()
	defer s.heldKiroPrefs.mu.Unlock()
	if l, locked := s.kiroSettingLock(key); locked {
		if strconv.FormatBool(l.Value) != value {
			httpreply.Conflict(w, l.Reason)
			return
		}
		if _, held := s.heldKiroPrefs.held[key]; held {
			s.heldKiroPrefs.held[key] = value
			webhttp.Ok(w)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cliTimeouts.Settings)
	defer cancel()
	out, err := s.cliRunner.Run(ctx, "settings", key, value)
	if err != nil {
		// 502, not 200: the client classifies by STATUS, so a 200 read every refusal as success.
		slog.Warn("kiro-cli settings write refused", "key", logsafe.Field(key), "error", logsafe.Field(err.Error()))
		webhttp.WriteJSONStatus(w, http.StatusBadGateway,
			httpreply.ErrorJSON(strings.TrimSpace(string(out))))
		return
	}
	// The user's write is kiro-cli's value now, so a restore still pending from
	// a lifted lock must not write the older choice over it.
	delete(s.heldKiroPrefs.held, key)
	webhttp.Ok(w)
}

// kiroSettingLocks maps a lock to the kiro-cli setting it pins.
var kiroSettingLocks = map[string]string{
	marotte.LockTelemetry: "telemetry.enabled",
}

// kiroSettingLock answers the lock pinning kiro-cli setting key, if any.
func (s *Server) kiroSettingLock(key string) (marotte.GovernanceLock, bool) {
	if s.governance == nil {
		return marotte.GovernanceLock{}, false
	}
	for lockKey, setting := range kiroSettingLocks {
		if setting != key {
			continue
		}
		if l, ok := s.governance.GovernanceLocks()[lockKey]; ok {
			return l, true
		}
	}
	return marotte.GovernanceLock{}, false
}

// heldKiroPrefs is the user's own value of each kiro-cli setting a lock currently
// pins, keyed by setting. kiro-cli has one store, so the value is captured before a
// lock overwrites it and written back when the lock lifts. mu serializes every
// lock application and user write to these settings, each reading the locks in
// force under it, so the last to run always leaves kiro-cli matching them.
type heldKiroPrefs struct {
	held map[string]string
	mu   sync.Mutex
}

// ApplyGovernanceLocks writes each locked kiro-cli setting to the administrator's
// value, so kiro-cli's own processes follow the lock, and writes the user's held
// value back to each setting whose lock lifted. Best-effort: a failure warns and the
// next lock change retries.
func (s *Server) ApplyGovernanceLocks(ctx context.Context) {
	if s.cliRunner == nil || s.governance == nil {
		return
	}
	s.heldKiroPrefs.mu.Lock()
	defer s.heldKiroPrefs.mu.Unlock()
	locks := s.governance.GovernanceLocks()
	if s.heldKiroPrefs.held == nil {
		s.heldKiroPrefs.held = map[string]string{}
	}
	for lockKey, setting := range kiroSettingLocks {
		l, locked := locks[lockKey]
		s.applyKiroSettingLock(ctx, setting, l, locked)
	}
}

// applyKiroSettingLock pins one kiro-cli setting to its lock, holding the user's own
// value, or restores that value once the lock lifts. The caller holds
// heldKiroPrefs.mu.
func (s *Server) applyKiroSettingLock(ctx context.Context, setting string, l marotte.GovernanceLock, locked bool) {
	pref, holding := s.heldKiroPrefs.held[setting]
	switch {
	case locked:
		if !holding {
			s.heldKiroPrefs.held[setting] = s.readKiroSettingFor(ctx, setting)
		}
		if err := s.runKiroSetting(ctx, setting, strconv.FormatBool(l.Value)); err != nil {
			slog.Warn("governance: a locked kiro-cli setting was not written", "key", setting, "error", err)
		}
	case holding && pref == "":
		delete(s.heldKiroPrefs.held, setting)
		slog.Warn("governance: a lock lifted with no readable prior value; the organization's value stays", "key", setting)
	case holding:
		if err := s.runKiroSetting(ctx, setting, pref); err != nil {
			slog.Warn("governance: the user's kiro-cli setting was not restored after a lock lifted", "key", setting, "error", err)
			return
		}
		delete(s.heldKiroPrefs.held, setting)
	}
}

func (s *Server) readKiroSettingFor(ctx context.Context, setting string) string {
	cctx, cancel := context.WithTimeout(ctx, s.cliTimeouts.Settings)
	defer cancel()
	return s.readOneKiroSetting(cctx, setting)
}

func (s *Server) runKiroSetting(ctx context.Context, setting, value string) error {
	cctx, cancel := context.WithTimeout(ctx, s.cliTimeouts.Settings)
	defer cancel()
	_, err := s.cliRunner.Run(cctx, "settings", setting, value)
	return err
}
