package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"

	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/kascap"
	"github.com/cplieger/marotte/internal/marotte"
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

// sessionConfigOption is one v3 configOptions entry. The model catalog is the "model" entry; v3 has no top-level
// `models` block.
type sessionConfigOption struct {
	ID           string                `json:"id"`
	CurrentValue json.RawMessage       `json:"currentValue"`
	Options      []sessionConfigChoice `json:"options"`
}

// sessionConfigChoice is one selectable value in a config-option select. For the model option the rate
// multiplier, effort capability and default tier ride _meta.kiro, decoded through the shared marotte.ModelChoiceMeta.
type sessionConfigChoice struct {
	Value       string                  `json:"value"`
	Name        string                  `json:"name"`
	Description string                  `json:"description"`
	Meta        marotte.ModelChoiceMeta `json:"_meta"`
}

// sessionCreated is the session/new and session/load result. _meta is KAS's session metadata spread flat on the
// result, not under `_meta.kiro` (probed 2026-08-02). session/load returns no `sessionId`, so loadSession sets it.
type sessionCreated struct {
	Modes     *sessionModes `json:"modes"`
	SessionID string        `json:"sessionId"`
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

// validIdent delegates to ids.ValidIdent.
func validIdent(s string) bool {
	return ids.ValidIdent(s)
}

// withSessionMeta adds the session door's _meta.kiro block to a session/new or session/load parameter map and
// returns it. Both verbs: KAS falls back to the creation value, so a new-only key stops working at the first resume.
// Skipped when the projection is empty.
func (b *Bridge) withSessionMeta(params map[string]any) map[string]any {
	if meta := kascap.SessionMeta(b.spawn()); len(meta) > 0 {
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
// restores them. The model must come with the level, since KAS drops a level sent while on `auto`.
func (b *Bridge) withSessionChoices(params map[string]any, opts *marotte.StartOpts) map[string]any {
	choices := make(map[string]any, 3)
	if opts.Model != "" && opts.Model != marotte.ModelAuto {
		choices[metaKeyModelID] = opts.Model
	}
	if opts.Effort != "" && marotte.EffortLevel(opts.Effort).Valid() {
		choices[metaKeyEffortLevel] = opts.Effort
	}
	if opts.Mode != "" {
		choices[metaKeyModeID] = opts.Mode
	}
	if len(choices) == 0 {
		return params
	}
	maps.Copy(sessionKiroMeta(params), choices)
	return params
}

// withClientSteering adds the chat's client steering to the session door on both verbs: KAS persists none of it.
// Nil or empty sends no key.
func withClientSteering(params map[string]any, opts *marotte.StartOpts) map[string]any {
	if len(opts.Steering) == 0 {
		return params
	}
	sessionKiroMeta(params)[metaKeySteering] = opts.Steering
	return params
}

func (b *Bridge) newSession(ctx context.Context, opts *marotte.StartOpts) error {
	resp, err := b.Call(ctx, methodSessionNew, withClientSteering(b.withSessionChoices(b.withSessionMeta(map[string]any{
		"cwd": b.workDir, "mcpServers": []any{},
	}), opts), opts))
	if err != nil {
		return fmt.Errorf("session/new: %w", err)
	}
	var result sessionCreated
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return fmt.Errorf("parse session/new: %w", err)
	}
	if !ids.ValidSessionID(result.SessionID) {
		return fmt.Errorf("session/new returned invalid session id: %q", result.SessionID)
	}
	b.mu.Lock()
	b.sessionID = marotte.SessionID(result.SessionID)
	b.applySessionResultLocked(&result, "")
	sid := string(b.sessionID)
	b.mu.Unlock()

	// Model and level rode _meta.kiro, so these repair only on a mismatch.
	b.applyInitialModel(ctx, opts.Model)
	// Thinking first: turning it off caps a high tier, and the coordinator's effort is already capped.
	b.applyThinking(ctx, sid, opts.Thinking)
	b.applyInitialEffort(ctx, sid, opts.Effort)
	b.applySupervised(ctx, sid, opts.Supervised)
	b.applyContentCollection(ctx, sid)
	return nil
}

// applyContentCollection asserts the content-collection value on a session door either way: kiro-cli starts KAS
// opted in. Logged at Error, not fatal.
func (b *Bridge) applyContentCollection(ctx context.Context, sessionID string) {
	if enabled, err := b.AssertContentCollection(ctx); err != nil {
		slog.Error("content collection not applied; model requests from this process may be opted in to content collection",
			"session_id", sessionID, "enabled", enabled, "error", err)
	}
}

// AssertContentCollection resolves StartOpts.ContentCollection and sets it on the live session, serialized with
// every other assert so a stale resolution never lands last. It reports the resolved value; no resolver sends
// nothing.
func (b *Bridge) AssertContentCollection(ctx context.Context) (bool, error) {
	if b.contentCollection == nil {
		return false, nil
	}
	b.contentCollectionMu.Lock()
	defer b.contentCollectionMu.Unlock()
	enabled := b.contentCollection(ctx)
	return enabled, b.setContentCollection(ctx, enabled)
}

// setContentCollection sends the option and checks the reply, which carries the option KAS now holds.
func (b *Bridge) setContentCollection(ctx context.Context, enabled bool) error {
	b.mu.Lock()
	sid := string(b.sessionID)
	b.mu.Unlock()
	if sid == "" {
		return errors.New("no session")
	}
	resp, err := b.Call(ctx, marotte.MethodSetConfigOption, marotte.ContentCollectionParams(sid, enabled))
	if err != nil {
		return err
	}
	if got, ok := marotte.ContentCollectionReported(resp.Result); ok && got != enabled {
		return fmt.Errorf("kiro-cli reports content collection enabled=%v", got)
	}
	return nil
}

// applyThinking asserts the chat's thinking choice; best-effort.
func (b *Bridge) applyThinking(ctx context.Context, sessionID, choice string) {
	if err := b.EnsureThinking(ctx, choice); err != nil {
		slog.Warn("apply thinking choice", "thinking", choice, "session_id", sessionID, "error", err)
	}
}

// applyInitialModel selects the chat's model on a new session. Not a launch flag: kiro-cli refuses `--model` with
// `--agent-engine=v3` and exits. Best-effort; a failure leaves KAS's default.
func (b *Bridge) applyInitialModel(ctx context.Context, model string) {
	if model == "" || model == marotte.ModelAuto {
		return
	}
	b.mu.Lock()
	current := string(b.modelID)
	b.mu.Unlock()
	if model == current {
		return
	}
	if err := b.SetModel(ctx, model); err != nil {
		slog.Warn("apply initial session model", "model", model, "error", err)
	}
}

// applyInitialEffort applies the chat's effort level (`--effort` is refused with v3 too): a repair on session/new,
// an unconditional assert on resume, whose result has no effortLevel option.
func (b *Bridge) applyInitialEffort(ctx context.Context, sessionID, effort string) {
	if err := b.EnsureEffort(ctx, effort); err != nil {
		slog.Warn("apply initial reasoning effort",
			"effort", effort, "session_id", sessionID, "error", err)
	}
}

// applySupervised enables KAS's turn-approval gate by setting `autopilot` to "off", a string (a boolean gets
// -32602). No-op for an unsupervised chat. Set once at creation; KAS persists it. Best-effort but at Error: reviewed
// writes would apply unreviewed.
func (b *Bridge) applySupervised(ctx context.Context, sessionID string, supervised bool) {
	if !supervised {
		return
	}
	if _, err := b.Call(ctx, marotte.MethodSetConfigOption, map[string]any{
		marotte.KeySessionID: sessionID,
		keyConfigID:          marotte.ConfigOptionAutopilot,
		keyConfigValue:       marotte.ConfigValueAutopilotOff,
	}); err != nil {
		// The outcome is readable, so the coordinator reports the divergence to the client as for a refused mode.
		slog.Error("supervised mode not applied; this session will NOT ask before writing",
			"session_id", sessionID, "error", err)
		return
	}
	// Recorded only once accepted. session/new does not fail on a refusal: the chat is usable, it just will not ask
	// before writing, which the report says.
	b.mu.Lock()
	b.supervised = true
	b.mu.Unlock()
}

// applyIgnoreFiles tells KAS which ignore files to enforce, once per bridge before the first session verb: the value
// is connection-scoped. Resolved here, since a settings write between registration and Start reaches this bridge.
// Nil or empty sends nothing: `{files: []}` clears the list. Best-effort at Warn; KAS no-ops a malformed payload.
func (b *Bridge) applyIgnoreFiles(ctx context.Context, resolve func(context.Context) []string) {
	if resolve == nil {
		return
	}
	names := resolve(ctx)
	if len(names) == 0 {
		return
	}
	if err := b.Notify(ctx, marotte.MethodPolicyIgnoreFilesChanged, map[string]any{
		marotte.ParamIgnoreFiles: names,
	}); err != nil {
		slog.Warn("apply agent ignore files", "files", names, "error", err)
	}
}

// resolveTerminalTimeout reads the shell tool's default timeout into the initialize features. Nil keeps the
// captured value.
func (b *Bridge) resolveTerminalTimeout(ctx context.Context, resolve func(context.Context) int) {
	if resolve != nil {
		b.features.TerminalCommandTimeoutMs = resolve(ctx)
	}
}

// reapplyTerminalTimeout re-reads the timeout after initialize and sends a changed one: the live push refuses a
// bridge without stdin, so this is that save's only channel. Best-effort at Warn.
func (b *Bridge) reapplyTerminalTimeout(ctx context.Context, resolve func(context.Context) int) {
	if resolve == nil {
		return
	}
	ms := resolve(ctx)
	if ms == b.features.TerminalCommandTimeoutMs {
		return
	}
	b.features.TerminalCommandTimeoutMs = ms
	if err := b.Notify(ctx, marotte.MethodTerminalSettingsChanged, marotte.TerminalSettingsParams(ms)); err != nil {
		slog.Warn("apply terminal settings", "command_timeout_ms", ms, "error", err)
	}
}

func (b *Bridge) loadSession(ctx context.Context, opts *marotte.StartOpts) error {
	// CallAt: KAS replays the session as notifications before the result, so the caller needs its position.
	resp, seq, err := b.CallAt(ctx, methodSessionLoad, withClientSteering(b.withSessionMeta(map[string]any{
		marotte.KeySessionID: opts.SessionID, "cwd": b.workDir, "mcpServers": []any{},
	}), opts))
	if err != nil {
		return fmt.Errorf("session/load: %w", err)
	}
	b.adoptLoadedSession(opts.SessionID, opts.Model, resp)
	b.mu.Lock()
	b.loadSeq = seq
	sid := string(b.sessionID)
	b.mu.Unlock()

	// Re-assert effort on resume: Chat.Effort is the user's choice and nothing else heals a lost level.
	b.applyThinking(ctx, sid, opts.Thinking)
	b.applyInitialEffort(ctx, sid, opts.Effort)
	// KAS's fork copies no `autopilot`, so a supervised chat's tangent would run in autopilot. A no-op when unsupervised.
	b.applySupervised(ctx, sid, opts.Supervised)
	// A resumed session keeps its memory mode, but reflection has a live setter.
	b.applyMemoryReflection(ctx, sid, opts.Memory.Reflection)
	// A fresh process starts at the wrapper's default, so a resume re-asserts it.
	b.applyContentCollection(ctx, sid)
	return nil
}

// applyMemoryReflection sets the memoryReflection option to the preference; best-effort.
func (b *Bridge) applyMemoryReflection(ctx context.Context, sessionID string, on bool) {
	value := marotte.ConfigValueAutopilotOff
	if on {
		value = marotte.ConfigValueAutopilotOn
	}
	if _, err := b.Call(ctx, marotte.MethodSetConfigOption, map[string]any{
		marotte.KeySessionID: sessionID,
		keyConfigID:          marotte.ConfigOptionMemoryReflection,
		keyConfigValue:       value,
	}); err != nil {
		slog.Warn("memory reflection not re-applied on session/load",
			"session_id", sessionID, "reflection", on, "error", err)
	}
}

// adoptLoadedSession copies a session/load result onto the bridge, falling back to the requested model when the
// result is absent or unparseable, and releases the lock before the post-load config calls.
func (b *Bridge) adoptLoadedSession(acpSessionID, fallbackModel string, resp *marotte.RPCResponse) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sessionID = marotte.SessionID(acpSessionID)
	if resp.Result != nil {
		var result sessionCreated
		parseErr := json.Unmarshal(resp.Result, &result)
		if parseErr == nil {
			b.applySessionResultLocked(&result, fallbackModel)
			return
		}
		slog.Warn("session/load: unparseable result, using fallback",
			"error", parseErr, "result_len", len(resp.Result))
	}
	if b.modelID == "" {
		b.modelID = marotte.ModelID(fallbackModel)
	}
}

// applySessionResultLocked copies the ACP session response into the bridge's state. Caller holds b.mu.
func (b *Bridge) applySessionResultLocked(r *sessionCreated, fallbackModel string) {
	if r.Modes != nil {
		b.currentMode = r.Modes.CurrentModeID
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
			b.modes.Store(&modes)
		}
	}
	b.sessionTitle = r.Meta.Title
	b.sessionTitleSetByUser = r.Meta.TitleSetByUser
	b.applyContextUsageLocked(r)
	b.reportWorkflowsDisagreement(r.Meta.WorkflowsEnabled)
	b.applyModelConfigOptionLocked(r.ConfigOptions)
	if b.modelID == "" {
		b.modelID = marotte.ModelID(fallbackModel)
	}
}

// applyContextUsageLocked records the summarization threshold, keeping the previous value when absent. Caller holds
// b.mu.
func (b *Bridge) applyContextUsageLocked(r *sessionCreated) {
	if r.Meta.ContextUsage == nil {
		return
	}
	if v := r.Meta.ContextUsage.SummarizationThreshold; v != nil && *v > 0 {
		b.summarizationPct = *v
	}
}

// reportWorkflowsDisagreement logs when the session resolved settings.workflows differently from this spawn's
// declaration. KAS freezes it at creation, so only a log. The declared side is read off the built door. Caller holds
// b.mu.
func (b *Bridge) reportWorkflowsDisagreement(resolved *bool) {
	if resolved == nil {
		return
	}
	declared := declaredSessionWorkflows(b.spawn())
	if *resolved == declared {
		return
	}
	slog.Warn("session resolved the workflows setting against what marotte declared; "+
		"the agent's workflow tools are not what this spawn asked for",
		"declared", declared, "resolved", *resolved)
}

// declaredSessionWorkflows reports whether a spawn's session door, as kascap builds it, enables settings.workflows.
func declaredSessionWorkflows(s *kascap.Spawn) bool {
	settings, ok := kascap.SessionMeta(s)["settings"].(map[string]any)
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

// applyEffortConfigOptionLocked records the effort level from the `effortLevel` option's currentValue. Caller holds
// b.mu. An absent option is unknown, so the previous value stands: KAS omits it for tierless models and on loads.
func (b *Bridge) applyEffortConfigOptionLocked(opts []sessionConfigOption) {
	for i := range opts {
		opt := &opts[i]
		if opt.ID != marotte.ConfigOptionEffort {
			continue
		}
		var current string
		_ = json.Unmarshal(opt.CurrentValue, &current) // string; ignore non-string
		if current != "" {
			b.effortLevel = current
		}
		return
	}
}

// applyThinkingConfigOptionLocked records the `thinking` option's currentValue ("on" or "off"); absent (an
// untoggleable model) keeps the previous value. Caller holds b.mu.
func (b *Bridge) applyThinkingConfigOptionLocked(opts []sessionConfigOption) {
	for i := range opts {
		if opts[i].ID != marotte.ConfigOptionThinking {
			continue
		}
		var current string
		_ = json.Unmarshal(opts[i].CurrentValue, &current) // string; ignore non-string
		if current == marotte.ThinkingOn || current == marotte.ThinkingOff {
			b.thinking = current
		}
		return
	}
}

// applyModelConfigOptionLocked takes the current model and catalog from the v3 "model" select. Caller holds b.mu.
// models is for display, without end-of-life entries; servedModels is every advertised id, the only sound input to
// an entitlement check.
func (b *Bridge) applyModelConfigOptionLocked(opts []sessionConfigOption) {
	b.applyEffortConfigOptionLocked(opts)
	b.applyThinkingConfigOptionLocked(opts)
	for i := range opts {
		opt := &opts[i]
		if opt.ID != marotte.ConfigOptionModel {
			continue
		}
		var current string
		_ = json.Unmarshal(opt.CurrentValue, &current) // string; ignore non-string
		if current != "" {
			b.modelID = marotte.ModelID(current)
		}
		// No choices means the catalog is unknown, not empty; currentValue still applies.
		if len(opt.Options) == 0 {
			slog.Warn("session reported an empty model catalog; keeping the previous one",
				"current_model", b.modelID)
			return
		}
		catalog := make([]marotte.SessionModel, 0, len(opt.Options))
		for _, c := range opt.Options {
			if c.Value == "" {
				continue
			}
			catalog = append(catalog, marotte.SessionModel{
				ID: c.Value, Name: c.Name, Description: c.Description,
				RateMultiplier:     c.Meta.Kiro.RateMultiplier,
				HasEffort:          c.Meta.Kiro.HasEffort,
				DefaultEffortLevel: c.Meta.Kiro.DefaultEffortLevel,
				ThinkingToggleable: c.Meta.Kiro.ThinkingToggleable,
				ThinkingDefaultOff: c.Meta.ThinkingDefaultOff(),
			})
		}
		b.catalog.Store(&catalog)
		return
	}
}
