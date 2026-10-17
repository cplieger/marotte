package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/kascap"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
)

// The per-session keys marotte adds inside _meta.kiro beside kascap's: the composer choices on session/new
// (`modelId`, `effortLevel`, `modeId`) and the chat's client steering on both verbs.
const (
	metaKeyModelID     = "modelId"
	metaKeyEffortLevel = "effortLevel"
	metaKeyModeID      = "modeId"
	metaKeySteering    = "steering"
)

// session/set_config_option param keys, shared by several call sites.
const (
	keyConfigID    = "configId"
	keyConfigValue = "value"
)

type sessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Meta carries kiro-cli's v3 per-mode metadata; only source is used: "bundled" (workflow modes, Kiro agents) or
	// "workspace" (.kiro/agents/).
	Meta struct {
		Kiro struct {
			Source string `json:"source"`
		} `json:"kiro"`
	} `json:"_meta"`
}

type sessionModes struct {
	CurrentModeID  string        `json:"currentModeId"`
	AvailableModes []sessionMode `json:"availableModes"`
}

// The model catalog is the "model" entry; v3 has no top-level `models` block.
type sessionConfigOption struct {
	ID           string                `json:"id"`
	CurrentValue json.RawMessage       `json:"currentValue"`
	Options      []sessionConfigChoice `json:"options"`
}

// For the model option the rate multiplier, effort capability and default tier ride _meta.kiro, decoded through the
// shared marotte.ModelChoiceMeta.
type sessionConfigChoice struct {
	Value       string                  `json:"value"`
	Name        string                  `json:"name"`
	Description string                  `json:"description"`
	Meta        marotte.ModelChoiceMeta `json:"_meta"`
}

// sessionCreated is the session/new and session/load result. _meta is KAS's session metadata spread flat on the
// result, not under `_meta.kiro`. session/load returns no `sessionId`.
type sessionCreated struct {
	Modes     *sessionModes     `json:"modes"`
	SessionID marotte.SessionID `json:"sessionId"`
	Meta      struct {
		// WorkflowsEnabled is KAS's resolved settings.workflows. A pointer because absent differs from false, and the
		// failure is silent: the agent loses workflowChatTools with no error.
		WorkflowsEnabled *bool `json:"workflowsEnabled"`
		// ContextUsage carries the session's summarization threshold. Pointers because absent differs from 0, and absent
		// keeps the previous value.
		ContextUsage *struct {
			SummarizationThreshold *float64 `json:"summarizationThreshold"`
		} `json:"contextUsage"`
		Title string `json:"title"`
		// TitleSetByUser is KAS's latch that the title is the user's and agent titles stop.
		TitleSetByUser bool `json:"titleSetByUser"`
	} `json:"_meta"`
	ConfigOptions []sessionConfigOption `json:"configOptions"`
}

// mcpServers is always `[]`: KAS reads the user's servers from its own hot-reloading file (internal/mcp/kasfile.go),
// and a client entry would outrank it, so UI edits would do nothing. The key is required since 2.16.

func validIdent(s string) bool { return ids.ValidIdent(s) }

// sessionSpawnOf is o's session door input.
func sessionSpawnOf(o *marotte.SessionOpts) kascap.SessionSpawn {
	return kascap.SessionSpawn{
		MemoryMode:            o.Memory.Mode,
		MemoryReflection:      o.Memory.Reflection,
		SpecPlan:              o.Features.SpecPlan,
		SpecAskClarification:  o.Features.SpecAskClarification,
		WorkValidation:        o.Features.WorkValidation,
		Workflows:             o.Features.Workflows,
		Presets:               slices.Clone(o.Presets),
		DisableAutoCompaction: o.DisableAutoCompaction,
	}
}

// errSessionResident refuses a door for an id whose session is open on this engine.
var errSessionResident = errors.New("bridge: that session is already open on the engine")

// newSessionHandle registers a lane under id in state opening, before anything is written, so every frame naming id
// reaches it. A handle still closing under the same id is waited for, bounded by ctx.
func (e *Engine) newSessionHandle(ctx context.Context, id marotte.SessionID, o *marotte.SessionOpts) (*Session, error) {
	for {
		if e.readerExited.Load() {
			return nil, &marotte.TransportError{Err: errBridgeExited, Retryable: true}
		}
		e.lanesMu.Lock()
		if held, ok := e.releasing[id]; ok {
			e.lanesMu.Unlock()
			select {
			case <-held:
				continue
			case <-ctx.Done():
				return nil, marotte.ErrSessionClosing
			}
		}
		prev := e.sessions[id]
		if prev == nil {
			s := &Session{
				engine:    e,
				lane:      newLane(),
				closeDone: make(chan struct{}),
				id:        id,
				role:      o.Role,
				spawn:     sessionSpawnOf(o),
				state:     stateOpening,
			}
			e.sessions[id] = s
			e.lanesMu.Unlock()
			return s, nil
		}
		e.lanesMu.Unlock()
		prev.mu.Lock()
		closing := prev.state == stateClosing
		prev.mu.Unlock()
		if !closing {
			return nil, errSessionResident
		}
		if err := prev.awaitClosed(ctx); err != nil {
			return nil, err
		}
	}
}

// failDoor closes a session whose door failed and logs it with the engine's KAS log directory, exactly when an
// operator needs it. The lane leaves the table, so later frames naming the id reach the engine lane.
func (s *Session) failDoor(phase string, err error) {
	s.closed()
	slog.Warn("session door failed",
		"session_id", s.id, "role", s.role, "phase", phase,
		"kas_log_dir", logsafe.Field(s.engine.KASLogDir()), "error", err)
}

// NewSession creates a session under an id it mints (ids.NewSessionID) and sends as `_meta.kiro.overrideSessionId`.
// attach runs once the lane is registered and before session/new is written, so a consumer reads every frame KAS
// sends for the session, the requests during creation included, which must be answered for the door to finish.
// handshakeBudget bounds the door on top of ctx. A reply that comes after the door gave up is still acted on: the
// session KAS created is closed and its files reaped, never leaked.
func (e *Engine) NewSession(ctx context.Context, o *marotte.SessionOpts, attach func(*Session)) (*Session, error) {
	if o.Model != "" && !validIdent(o.Model) {
		return nil, fmt.Errorf("invalid model identifier: %q", o.Model)
	}
	minted := marotte.SessionID(ids.NewSessionID())
	s, err := e.newSessionHandle(ctx, minted, o)
	if err != nil {
		return nil, err
	}
	s.spawn.OverrideSessionID = string(minted)
	if attach != nil {
		attach(s)
	}
	dctx, cancel := context.WithTimeout(ctx, handshakeBudget)
	defer cancel()
	start := time.Now()
	params := withClientSteering(withSessionChoices(s.withSessionMeta(map[string]any{
		"cwd": e.workDir, "mcpServers": []any{},
	}), o), o)
	resp, _, err := e.callAt(dctx, s.lane, minted, methodSessionNew, params, e.lateSessionNew(minted, params))
	if err != nil {
		err = handshakeTimeout(fmt.Errorf("session/new: %w", err), "session start", handshakeBudget)
		s.failDoor("new", err)
		return nil, err
	}
	var result sessionCreated
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		err = fmt.Errorf("parse session/new: %w", err)
		s.failDoor("new", err)
		e.closeAndReap(minted)
		return nil, err
	}
	if result.SessionID != minted {
		s.failDoor("new", marotte.ErrSessionMetaRefused)
		e.refusedOverride(minted, result.SessionID, params)
		return nil, marotte.ErrSessionMetaRefused
	}
	s.mu.Lock()
	s.applySessionResultLocked(&result, "")
	s.mu.Unlock()
	// Model and level rode _meta.kiro, so these repair only on a mismatch.
	s.applyInitialModel(dctx, o.Model)
	s.settleServedModel(dctx)
	// Thinking first: turning it off caps a high tier, and the caller's effort is already capped.
	s.applyThinking(dctx, o.Thinking)
	s.applyInitialEffort(dctx, o.Effort)
	s.applySupervised(dctx, o.Supervised)
	s.applyContentCollection(dctx)
	// Fail closed when the budget expired inside the appliers: they are best-effort, and an expiry would silently leave
	// the session on the wrong mode or model, or in autopilot for a supervised chat.
	if err := dctx.Err(); err != nil {
		err = handshakeTimeout(fmt.Errorf("session/new: %w", err), "session start", handshakeBudget)
		s.failDoor("new", err)
		e.closeAndReap(minted)
		return nil, err
	}
	s.open()
	slog.Info("session opened",
		"session_id", minted, "role", o.Role, "model", s.ModelID(), "phase", "new",
		"elapsed_ms", time.Since(start).Milliseconds())
	return s, nil
}

// lateSessionNew finishes an abandoned session/new from the id KAS actually created: the minted one is closed and
// reaped, a different one (KAS ignored the override) is closed, reaped and logged, and an error reply created nothing.
func (e *Engine) lateSessionNew(minted marotte.SessionID, params map[string]any) func(*marotte.RPCResponse) {
	return func(resp *marotte.RPCResponse) {
		if resp.Error != nil {
			return
		}
		var result sessionCreated
		_ = json.Unmarshal(resp.Result, &result)
		if result.SessionID != "" && result.SessionID != minted {
			e.refusedOverride(minted, result.SessionID, params)
			return
		}
		e.closeAndReap(minted)
	}
}

// refusedOverride handles a session/new reply naming another id than the minted one: KAS parses `_meta.kiro` all or
// nothing, so a field that failed its schema dropped every session setting. The session KAS made is closed and reaped.
func (e *Engine) refusedOverride(minted, returned marotte.SessionID, params map[string]any) {
	slog.Error("engine: kiro-cli ignored the session id marotte sent, so a session setting failed its schema",
		"minted_session_id", minted, "returned_session_id", logsafe.Field(string(returned)),
		"meta_keys", metaKeysOf(params))
	if ids.ValidSessionID(string(returned)) {
		e.closeAndReap(returned)
	}
}

func metaKeysOf(params map[string]any) []string {
	kiro := sessionKiroMeta(maps.Clone(params))
	return slices.Sorted(maps.Keys(kiro))
}

// closeAndReap sends an engine-level session/close for id and reaps its files: the session exists in KAS, but no
// handle holds it.
func (e *Engine) closeAndReap(id marotte.SessionID) {
	budget := closeBudget
	e.goBackground(func() {
		cctx, cancel := context.WithTimeout(e.lifecycleCtx, budget)
		defer cancel()
		if _, err := e.Call(cctx, methodSessionClose, map[string]any{marotte.KeySessionID: id}); err != nil {
			slog.Warn("could not close a session no handle holds", "session_id", id, "error", err)
		}
		if e.reapSession != nil {
			e.reapSession(id)
		}
	})
}

// LoadSession loads o.SessionID with a lane registered under it first, attach running before the write as
// NewSession's does. replayBudget bounds the door on top of ctx: KAS replays the transcript before the reply. A reply
// that comes after the door gave up closes the session unless a newer handle for the id exists.
func (e *Engine) LoadSession(ctx context.Context, o *marotte.SessionOpts, attach func(*Session)) (*Session, error) {
	id := marotte.SessionID(o.SessionID)
	if !ids.ValidSessionID(o.SessionID) {
		return nil, fmt.Errorf("invalid acp session id: %q", o.SessionID)
	}
	if o.Model != "" && !validIdent(o.Model) {
		return nil, fmt.Errorf("invalid model identifier: %q", o.Model)
	}
	s, err := e.newSessionHandle(ctx, id, o)
	if err != nil {
		return nil, err
	}
	if attach != nil {
		attach(s)
	}
	dctx, cancel := context.WithTimeout(ctx, replayBudget)
	defer cancel()
	start := time.Now()
	// CallAt: KAS replays the session as notifications before the result, so the caller needs its position.
	resp, seq, err := e.callAt(dctx, s.lane, id, methodSessionLoad, withClientSteering(s.withSessionMeta(map[string]any{
		marotte.KeySessionID: id, "cwd": e.workDir, "mcpServers": []any{},
	}), o), e.lateSessionLoad(s))
	if err != nil {
		err = handshakeTimeout(fmt.Errorf("session/load: %w", err), "session resume", replayBudget)
		s.failDoor("load", err)
		return nil, err
	}
	s.adoptLoadedSession(o.Model, resp)
	s.mu.Lock()
	s.loadSeq = seq
	s.mu.Unlock()
	s.settleServedModel(dctx)
	// Re-assert effort on resume: KAS's fork and a model tier mismatch drop it.
	s.applyThinking(dctx, o.Thinking)
	s.applyInitialEffort(dctx, o.Effort)
	// KAS's fork copies no `autopilot`, so a supervised chat's tangent would run in autopilot. A no-op when unsupervised.
	s.applySupervised(dctx, o.Supervised)
	// A loaded session keeps its memory mode, but reflection has a live setter.
	s.applyMemoryReflection(dctx, o.Memory.Reflection)
	s.applyContentCollection(dctx)
	s.open()
	slog.Info("session opened",
		"session_id", id, "role", o.Role, "model", s.ModelID(), "phase", "load",
		"elapsed_ms", time.Since(start).Milliseconds())
	return s, nil
}

// lateSessionLoad closes a session whose load reply came after its door gave up, unless a newer handle took the id.
// The id stays held until the close settles, so a door for it opened meanwhile waits instead of being closed.
func (e *Engine) lateSessionLoad(s *Session) func(*marotte.RPCResponse) {
	return func(resp *marotte.RPCResponse) {
		if resp.Error != nil {
			return
		}
		e.lanesMu.Lock()
		// s itself may still be registered: callAt gives up before LoadSession's failDoor removes it.
		if cur := e.sessions[s.id]; (cur != nil && cur != s) || e.releasing[s.id] != nil {
			e.lanesMu.Unlock()
			return
		}
		held := make(chan struct{})
		e.releasing[s.id] = held
		e.lanesMu.Unlock()
		release := func() {
			e.lanesMu.Lock()
			delete(e.releasing, s.id)
			e.lanesMu.Unlock()
			close(held)
		}
		budget := closeBudget
		if !e.goBackground(func() {
			defer release()
			cctx, cancel := context.WithTimeout(e.lifecycleCtx, budget)
			defer cancel()
			if _, err := e.Call(cctx, methodSessionClose, map[string]any{marotte.KeySessionID: s.id}); err != nil {
				slog.Warn("could not close a session whose load was abandoned", "session_id", s.id, "error", err)
			}
		}) {
			release()
		}
	}
}

func (s *Session) open() {
	s.mu.Lock()
	if s.state == stateOpening {
		s.state = stateOpen
	}
	s.mu.Unlock()
}

// withSessionMeta adds the session door's _meta.kiro block to a session/new or session/load parameter map. Both
// verbs: KAS falls back to the creation value, so a new-only key stops working at the first resume.
func (s *Session) withSessionMeta(params map[string]any) map[string]any {
	if meta := kascap.SessionMeta(&s.spawn); len(meta) > 0 {
		params["_meta"] = map[string]any{metaKeyKiro: meta}
	}
	return params
}

// sessionKiroMeta returns a session parameter map's _meta.kiro map, creating both levels. Every door helper writes
// through it, because a second _meta block would replace the first.
func sessionKiroMeta(params map[string]any) map[string]any {
	meta, ok := params["_meta"].(map[string]any)
	if !ok {
		meta = make(map[string]any, 1)
		params["_meta"] = meta
	}
	kiro, ok := meta[metaKeyKiro].(map[string]any)
	if !ok {
		kiro = make(map[string]any)
		meta[metaKeyKiro] = kiro
	}
	return kiro
}

// withSessionChoices adds the chat's model, effort level and mode to session/new's _meta.kiro. Not on load: KAS
// restores them, and settleServedModel moves a restored model the catalog does not list. The model must come with
// the level, since KAS drops a level sent while on `auto`.
func withSessionChoices(params map[string]any, o *marotte.SessionOpts) map[string]any {
	choices := make(map[string]any, 3)
	if o.Model != "" && o.Model != marotte.ModelAuto {
		choices[metaKeyModelID] = o.Model
	}
	if o.Effort != "" && marotte.EffortLevel(o.Effort).Valid() {
		choices[metaKeyEffortLevel] = o.Effort
	}
	if o.Mode != "" {
		choices[metaKeyModeID] = o.Mode
	}
	if len(choices) == 0 {
		return params
	}
	maps.Copy(sessionKiroMeta(params), choices)
	return params
}

// withClientSteering adds the chat's client steering to the session door on both verbs: KAS persists none of it.
// Nil or empty sends no key.
func withClientSteering(params map[string]any, o *marotte.SessionOpts) map[string]any {
	if len(o.Steering) == 0 {
		return params
	}
	sessionKiroMeta(params)[metaKeySteering] = o.Steering
	return params
}

// applyContentCollection asserts the content-collection value on a session door either way: kiro-cli starts KAS
// opted in. Logged at Error, not fatal.
func (s *Session) applyContentCollection(ctx context.Context) {
	if enabled, err := s.engine.assertContentCollection(ctx, s.id); err != nil {
		slog.Error("content collection not applied; model requests from the agent engine may be opted in to content collection",
			"session_id", s.id, "enabled", enabled, "error", err)
	}
}

// applyThinking asserts the chat's thinking choice; best-effort.
func (s *Session) applyThinking(ctx context.Context, choice string) {
	if err := s.ensureThinking(ctx, choice); err != nil {
		slog.Warn("apply thinking choice", "thinking", choice, "session_id", s.id, "error", err)
	}
}

// Not a launch flag: kiro-cli refuses `--model` with `--agent-engine=v3` and exits. Best-effort; a failure leaves
// KAS's default.
func (s *Session) applyInitialModel(ctx context.Context, model string) {
	if model == "" || model == marotte.ModelAuto {
		return
	}
	if model == string(s.ModelID()) {
		return
	}
	if err := s.setModel(ctx, model); err != nil {
		slog.Warn("apply initial session model", "model", model, "error", err)
	}
}

// settleServedModel moves the session off a model its own catalog does not list, onto `auto` when listed, else the
// first listed id: KAS restores a persisted model on session/load without judging it, and the service then refuses
// every prompt. An unknown catalog judges nothing. Best-effort; a refused move leaves the session as it is.
func (s *Session) settleServedModel(ctx context.Context) {
	current := string(s.ModelID())
	catalog := s.Catalog()
	served := make([]string, len(catalog))
	for i := range catalog {
		served[i] = catalog[i].ID
	}
	if marotte.ModelServed(current, served) {
		return
	}
	to := served[0]
	if slices.Contains(served, marotte.ModelAuto) {
		to = marotte.ModelAuto
	}
	if err := s.setModel(ctx, to); err != nil {
		slog.Warn("session runs a model its catalog does not list, and the move off it was refused",
			"session_id", s.id, "model", logsafe.Field(current), "to", logsafe.Field(to), "error", err)
		return
	}
	slog.Warn("moved a session off a model its catalog does not list",
		"session_id", s.id, "from", logsafe.Field(current), "to", logsafe.Field(to))
}

// applyInitialEffort applies the chat's effort level (`--effort` is refused with v3 too): a repair on session/new,
// an unconditional assert on a load, whose result has no effortLevel option.
func (s *Session) applyInitialEffort(ctx context.Context, effort string) {
	if err := s.ensureEffort(ctx, effort); err != nil {
		slog.Warn("apply initial reasoning effort", "effort", effort, "session_id", s.id, "error", err)
	}
}

// applySupervised enables KAS's turn-approval gate by setting `autopilot` to "off", a string (a boolean gets -32602).
// No-op for an unsupervised chat. Best-effort but at Error: reviewed writes would apply unreviewed.
func (s *Session) applySupervised(ctx context.Context, supervised bool) {
	if !supervised {
		return
	}
	if _, err := s.doorSetConfig(ctx, map[string]any{
		marotte.KeySessionID: s.id,
		keyConfigID:          marotte.ConfigOptionAutopilot,
		keyConfigValue:       marotte.ConfigValueAutopilotOff,
	}); err != nil {
		// The outcome is readable, so the caller reports the divergence to the client as for a refused mode.
		slog.Error("supervised mode not applied; this session will NOT ask before writing",
			"session_id", s.id, "error", err)
		return
	}
	// Recorded only once accepted: a refusal leaves the chat usable, and the report says it will not ask.
	s.mu.Lock()
	s.supervised = true
	s.mu.Unlock()
}

// applyMemoryReflection sets the memoryReflection option to the preference; best-effort.
func (s *Session) applyMemoryReflection(ctx context.Context, on bool) {
	value := marotte.ConfigValueAutopilotOff
	if on {
		value = marotte.ConfigValueAutopilotOn
	}
	if _, err := s.doorSetConfig(ctx, map[string]any{
		marotte.KeySessionID: s.id,
		keyConfigID:          marotte.ConfigOptionMemoryReflection,
		keyConfigValue:       value,
	}); err != nil {
		slog.Warn("memory reflection not re-applied on session/load",
			"session_id", s.id, "reflection", on, "error", err)
	}
}

// adoptLoadedSession copies a session/load result onto the session, falling back to the requested model when the
// result is absent or unparseable.
func (s *Session) adoptLoadedSession(fallbackModel string, resp *marotte.RPCResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if resp.Result != nil {
		var result sessionCreated
		parseErr := json.Unmarshal(resp.Result, &result)
		if parseErr == nil {
			s.applySessionResultLocked(&result, fallbackModel)
			return
		}
		slog.Warn("session/load: unparseable result, using fallback",
			"error", parseErr, "result_len", len(resp.Result))
	}
	if s.modelID == "" {
		s.modelID = marotte.ModelID(fallbackModel)
	}
}

// Caller holds s.mu.
func (s *Session) applySessionResultLocked(r *sessionCreated, fallbackModel string) {
	if r.Modes != nil {
		s.currentMode = r.Modes.CurrentModeID
		// Absent and present-but-empty differ: no block keeps the previous list, while an empty list once emptied the mode
		// picker for the session.
		if len(r.Modes.AvailableModes) == 0 {
			slog.Warn("session reported an empty mode list; keeping the previous catalog",
				"current_mode", r.Modes.CurrentModeID)
		} else {
			modes := make([]marotte.SessionMode, 0, len(r.Modes.AvailableModes))
			for _, m := range r.Modes.AvailableModes {
				modes = append(modes, marotte.SessionMode{
					ID: m.ID, Name: m.Name, Description: m.Description, Source: m.Meta.Kiro.Source,
				})
			}
			s.modes.Store(&modes)
		}
	}
	s.sessionTitle = r.Meta.Title
	s.titleSetByUser = r.Meta.TitleSetByUser
	s.applyContextUsageLocked(r)
	s.reportWorkflowsDisagreement(r.Meta.WorkflowsEnabled)
	s.applyModelConfigOptionLocked(r.ConfigOptions)
	if s.modelID == "" {
		s.modelID = marotte.ModelID(fallbackModel)
	}
}

// applyContextUsageLocked records the summarization threshold, keeping the previous value when absent.
func (s *Session) applyContextUsageLocked(r *sessionCreated) {
	if r.Meta.ContextUsage == nil {
		return
	}
	if v := r.Meta.ContextUsage.SummarizationThreshold; v != nil && *v > 0 {
		s.summarizationPct = *v
	}
}

// reportWorkflowsDisagreement logs when the session resolved settings.workflows differently from the door's
// declaration. KAS freezes it at creation, so only a log. The declared side is read off the built door.
func (s *Session) reportWorkflowsDisagreement(resolved *bool) {
	if resolved == nil {
		return
	}
	declared := declaredSessionWorkflows(&s.spawn)
	if *resolved == declared {
		return
	}
	slog.Warn("session resolved the workflows setting against what marotte declared; "+
		"the agent's workflow tools are not what this session asked for",
		"declared", declared, "resolved", *resolved)
}

func declaredSessionWorkflows(sp *kascap.SessionSpawn) bool {
	settings, ok := kascap.SessionMeta(sp)["settings"].(map[string]any)
	if !ok {
		return false
	}
	wf, ok := settings["workflows"].(map[string]any)
	if !ok {
		return false
	}
	on, _ := wf["enabled"].(bool)
	return on
}

// An absent option is unknown, so the previous value stands: KAS omits it for tierless models and on loads.
func (s *Session) applyEffortConfigOptionLocked(opts []sessionConfigOption) {
	for i := range opts {
		opt := &opts[i]
		if opt.ID != marotte.ConfigOptionEffort {
			continue
		}
		var current string
		_ = json.Unmarshal(opt.CurrentValue, &current) // string; ignore non-string
		if current != "" {
			s.effortLevel = current
		}
		return
	}
}

// Absent (an untoggleable model) keeps the previous value.
func (s *Session) applyThinkingConfigOptionLocked(opts []sessionConfigOption) {
	for i := range opts {
		if opts[i].ID != marotte.ConfigOptionThinking {
			continue
		}
		var current string
		_ = json.Unmarshal(opts[i].CurrentValue, &current) // string; ignore non-string
		if current == marotte.ThinkingOn || current == marotte.ThinkingOff {
			s.thinking = current
		}
		return
	}
}

func (s *Session) applyModelConfigOptionLocked(opts []sessionConfigOption) {
	s.applyEffortConfigOptionLocked(opts)
	s.applyThinkingConfigOptionLocked(opts)
	for i := range opts {
		if opts[i].ID == marotte.ConfigOptionModel {
			s.applyModelOptionLocked(&opts[i])
			return
		}
	}
}

// An empty or cold-pin choice list keeps the previous catalog (marotte.ColdCatalogPin owns the pin's shape).
func (s *Session) applyModelOptionLocked(opt *sessionConfigOption) {
	var current string
	_ = json.Unmarshal(opt.CurrentValue, &current) // string; ignore non-string
	if current != "" {
		s.modelID = marotte.ModelID(current)
	}
	// No choices means the catalog is unknown, not empty; currentValue still applies.
	if len(opt.Options) == 0 {
		slog.Warn("session reported an empty model catalog; keeping the previous one",
			"current_model", s.modelID)
		return
	}
	values := make([]string, len(opt.Options))
	for i := range opt.Options {
		values[i] = opt.Options[i].Value
	}
	if marotte.ColdCatalogPin(current, values) {
		return
	}
	catalog := make([]marotte.SessionModel, 0, len(opt.Options))
	for _, c := range opt.Options {
		if c.Value == "" {
			continue
		}
		catalog = append(catalog, c.Meta.SessionModel(c.Value, c.Name, c.Description))
	}
	s.catalog.Store(&catalog)
}
