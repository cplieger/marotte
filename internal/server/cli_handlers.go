package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/sanitize"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/marotte/internal/version"
)

// VersionInfo is GET /api/version's body.
type VersionInfo struct {
	// ConfigDir is where the client opens the editable config files, so a moved KIRO_CONFIG_DIR is honoured.
	ConfigDir string `json:"config_dir,omitempty"`
	KiroCLI   string `json:"kiro_cli,omitempty"`
	Marotte   string `json:"marotte"`
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	payload := VersionInfo{Marotte: version.Build, ConfigDir: s.configDir}
	ctx, cancel := context.WithTimeout(r.Context(), s.cliTimeouts.Version)
	defer cancel()
	if out, err := s.cliRunner.Run(ctx, "--version"); err == nil {
		payload.KiroCLI = strings.TrimSpace(string(out))
	}
	httpserver.WriteJSON(w, payload)
}

// DiagnosticsReport is POST /api/diagnostics' body.
type DiagnosticsReport struct {
	Report string `json:"report"`
}

func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.cliTimeouts.Diagnostics)
	defer cancel()
	// STDOUT only (stderr is logged) and capped so a runaway dump cannot bloat the response.
	out, truncated, err := s.cliRunner.RunStdoutCapped(ctx, diagnosticsMaxBytes, "diagnostic", "--force", "--format", "json-pretty")
	if err != nil {
		// 502, not 200-with-an-error-body: the client's action framework classifies by STATUS.
		slog.Warn("diagnostics: kiro-cli exec failed", "error", logsafe.Field(err.Error()))
		httpserver.WriteJSONStatus(w, http.StatusBadGateway,
			httpreply.ErrorJSON("diagnostic command failed"))
		return
	}
	report := sanitize.Output(string(out))
	if truncated {
		report += "\n\n[truncated]"
	}
	httpserver.WriteJSON(w, DiagnosticsReport{Report: report})
}

// KiroSettingsQuery is GET /api/kiro-settings' query: a comma-separated key list, every allowlisted key when absent.
type KiroSettingsQuery struct {
	Keys string `json:"keys,omitempty"`
}

// KiroSettings is GET /api/kiro-settings' body.
type KiroSettings struct {
	Settings map[string]string `json:"settings"`
}

// KiroSettingWrite is PUT /api/kiro-settings' body.
type KiroSettingWrite struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func (s *Server) handleKiroSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		s.readKiroSettings(w, r)
	case http.MethodPut:
		s.writeKiroSetting(w, r)
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
	httpserver.WriteJSON(w, KiroSettings{Settings: out})
}

// overlayHeldKiroPrefs reports the user's own value for each setting a lock
// pins, or whose restore is still pending, since kiro-cli holds the
// organization's value there. The client paints the lock over it and shows it
// again on unlock.
func (s *Server) overlayHeldKiroPrefs(out map[string]string) {
	s.heldKiroPrefs.mu.Lock()
	defer s.heldKiroPrefs.mu.Unlock()
	for key, pref := range s.heldKiroPrefs.held {
		if _, asked := out[key]; !asked || pref == "" {
			continue
		}
		if pref == heldUnset {
			pref = ""
		}
		out[key] = pref
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
	httpserver.LimitBody(w, r, httpserver.MaxJSONBody)
	var body KiroSettingWrite
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
			httpreply.OK(w)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cliTimeouts.Settings)
	defer cancel()
	out, err := s.cliRunner.Run(ctx, "settings", key, value)
	if err != nil {
		// 502, not 200: the client classifies by STATUS, so a 200 read every refusal as success.
		slog.Warn("kiro-cli settings write refused", "key", logsafe.Field(key), "error", logsafe.Field(err.Error()))
		httpserver.WriteJSONStatus(w, http.StatusBadGateway,
			httpreply.ErrorJSON(strings.TrimSpace(string(out))))
		return
	}
	// The user's write is kiro-cli's value now, so a restore still pending from
	// a lifted lock must not write the older choice over it.
	delete(s.heldKiroPrefs.held, key)
	httpreply.OK(w)
	if s.agent != nil {
		s.agent.ReconcileSessionSettings(durable.Context(r.Context()))
		// The effective settings mirror this key, so every tab rereads them.
		if key == marotte.KiroHookStatusKey {
			settings.Announce(durable.Context(r.Context()), s.configDir, s.agent.Broadcast)
		}
	}
}

var kiroSettingLocks = map[string]string{
	marotte.LockTelemetry: "telemetry.enabled",
}

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

// heldUnset is held for a setting kiro-cli had no value for, so lifting the lock deletes the
// organization's value and kiro-cli's own default applies again. No setting kind accepts it.
const heldUnset = "\x00unset"

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
	// A lifted lock's restore moves what a spawn resolves after the agent's own lock check ran.
	if s.agent != nil {
		s.agent.ReconcileSessionSettings(ctx)
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
			s.heldKiroPrefs.held[setting] = s.readHeldKiroSetting(ctx, setting)
		}
		if err := s.runKiroSetting(ctx, setting, strconv.FormatBool(l.Value)); err != nil {
			slog.Warn("governance: a locked kiro-cli setting was not written", "key", setting, "error", err)
		}
	case holding && pref == "":
		delete(s.heldKiroPrefs.held, setting)
		slog.Warn("governance: a lock lifted with no readable prior value; the organization's value stays", "key", setting)
	case holding:
		if err := s.restoreKiroSetting(ctx, setting, pref); err != nil {
			slog.Warn("governance: the user's kiro-cli setting was not restored after a lock lifted", "key", setting, "error", err)
			return
		}
		delete(s.heldKiroPrefs.held, setting)
	}
}

// readHeldKiroSetting reads the value a lock is about to overwrite: heldUnset when the listed
// document lacks it, "" when kiro-cli cannot say. A failed list falls back to the per-key read,
// which answers "" for unset too, so that lift keeps the organization's value.
func (s *Server) readHeldKiroSetting(ctx context.Context, setting string) string {
	cctx, cancel := context.WithTimeout(ctx, s.cliTimeouts.Settings)
	defer cancel()
	if listed := s.readKiroSettingsList(cctx); listed != nil {
		if v, ok := listed[setting]; ok {
			return v
		}
		return heldUnset
	}
	return s.readOneKiroSetting(cctx, setting)
}

func (s *Server) restoreKiroSetting(ctx context.Context, setting, pref string) error {
	if pref != heldUnset {
		return s.runKiroSetting(ctx, setting, pref)
	}
	cctx, cancel := context.WithTimeout(ctx, s.cliTimeouts.Settings)
	defer cancel()
	_, err := s.cliRunner.Run(cctx, "settings", "--delete", setting)
	return err
}

func (s *Server) runKiroSetting(ctx context.Context, setting, value string) error {
	cctx, cancel := context.WithTimeout(ctx, s.cliTimeouts.Settings)
	defer cancel()
	_, err := s.cliRunner.Run(cctx, "settings", setting, value)
	return err
}
