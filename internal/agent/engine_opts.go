package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strconv"

	"github.com/cplieger/marotte/internal/bot"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

// The fingerprint keys of the engine values that only a restart applies. A Settings key names its row; the others
// have no row.
const (
	reasonKnowledge         = marotte.EngineReason(settings.KeyKnowledgeEnabled)
	reasonSteeringReminders = marotte.EngineReason(settings.KeySteeringReminders)
	reasonCloudFormation    = marotte.EngineReason(settings.KeyCloudFormationSafety)
	reasonModelFallback     = marotte.EngineReason(settings.KeyModelFallback)
	reasonAutoRouting       = marotte.EngineReason(settings.KeyAutoRouting)
	reasonAutoDelegation    = marotte.EngineReason(settings.KeyAutoDelegation)
	reasonHeap              = marotte.EngineReason(settings.KeyEngineHeapMiB)
	reasonDebugLogs         = marotte.EngineReason(settings.KeyDebugLogs)
	reasonHighThroughput    = marotte.EngineReason(settings.KeyHighThroughput)
	reasonTelemetry         = marotte.EngineReason(kiroTelemetryKey)
)

// engineReasonLabel is the user's name for what a restart applies.
func engineReasonLabel(r marotte.EngineReason) string {
	switch r {
	case reasonKnowledge:
		return "Knowledge"
	case reasonSteeringReminders:
		return "Steering reminders"
	case reasonCloudFormation:
		return "CloudFormation safety check"
	case reasonModelFallback:
		return "Model fallback"
	case reasonAutoRouting:
		return "Auto routing"
	case reasonAutoDelegation:
		return "Auto delegation"
	case reasonHeap:
		return "Agent engine memory limit"
	case reasonDebugLogs:
		return "Debug logs"
	case reasonHighThroughput:
		return "High throughput optimizations"
	case reasonTelemetry:
		return "Usage telemetry"
	case marotte.EngineReasonKiroCLIVersion:
		return "the new kiro-cli version"
	case marotte.EngineReasonAccountSwitch:
		return "the new Kiro account"
	}
	return string(r)
}

// engineDocument is the config.json part of an engine spawn.
type engineDocument struct {
	// ignoreFiles is floor-led; the floor alone when the key does not decode, which sets ignoreInvalid.
	ignoreFiles       []string
	features          marotte.EngineFeatures
	heapMiB           int
	knowledge         bool
	debugLogs         bool
	throughput        bool
	ignoreInvalid     bool
	contentCollection bool // the stored switch, before the organization lock
}

// sessionDocument is the config.json part of a session door. profile and dirEntries are kept as stored and mapped at
// the door, whose mapping logs.
type sessionDocument struct {
	profile    string
	dirEntries []string
	features   marotte.SessionFeatures // before any administrator lock
	memory     marotte.MemoryPreference
	// autoCompactPct and autoCompactEnabled are autoCompactionPolicy's answer, read by marotte's compactor.
	autoCompactPct     int
	dirsInvalid        bool // additional_directories is present and does not decode: the door grants nothing
	autoCompactEnabled bool
}

// engineHeapMiB reads the heap limit; an absent or invalid value is 0, kiro-cli's default.
func engineHeapMiB(d settings.Document) int {
	var n int
	if d.Into(settings.KeyEngineHeapMiB, &n) && n >= 0 {
		return n
	}
	return 0
}

// resolveEngine reads the engine options a spawn at path starts with, and the fingerprint of their restart-only part.
// Its config.json values come from the configDoc owner, so over an unreadable document they are the last readable
// ones and source says so.
func (rt *Runtime) resolveEngine(ctx context.Context, path string) engineSpec {
	doc, src := rt.configDocument(ctx)
	e := doc.engine
	telemetryOff := rt.lifecycle.telemetryDisabled(rt.config.GovernanceLocks)
	opts := &marotte.EngineOpts{
		Lifetime:          rt.lifecycle.shutdownCtx,
		Background:        rt.lifecycle.goUnlessDraining,
		ReapSession:       func(id marotte.SessionID) { rt.reapSessions([]string{string(id)}) },
		IgnoreFiles:       rt.spawnIgnoreFiles,
		TerminalTimeout:   rt.terminalTimeout,
		ContentCollection: rt.contentCollection,
		ExtraArgs:         rt.acpArgs,
		AuthArgs:          rt.signin.spawnArgs(),
		Features:          e.features,
		HeapMiB:           e.heapMiB,
		SecretStorage:     rt.inbound.secrets != nil,
		Knowledge:         e.knowledge,
		DisableTelemetry:  telemetryOff,
		DebugLogs:         e.debugLogs,
		HighThroughput:    e.throughput,
	}
	fp := map[marotte.EngineReason]string{
		reasonKnowledge:                    strconv.FormatBool(e.knowledge),
		reasonSteeringReminders:            strconv.FormatBool(e.features.SteeringReminders),
		reasonCloudFormation:               e.features.InfraSafetyMonitor,
		reasonModelFallback:                strconv.FormatBool(e.features.ModelFallback),
		reasonAutoRouting:                  e.features.AutoRouting,
		reasonAutoDelegation:               e.features.AutoDelegation,
		reasonHeap:                         strconv.Itoa(e.heapMiB),
		reasonDebugLogs:                    strconv.FormatBool(e.debugLogs),
		reasonHighThroughput:               strconv.FormatBool(e.throughput),
		reasonTelemetry:                    strconv.FormatBool(telemetryOff),
		marotte.EngineReasonKiroCLIVersion: path,
		marotte.EngineReasonAccountSwitch:  strconv.FormatBool(rt.signin.accountPending()),
	}
	return engineSpec{opts: opts, fingerprint: fp, source: src}
}

// terminalTimeout is EngineOpts.TerminalTimeout: the configDoc owner's shell timeout.
func (rt *Runtime) terminalTimeout(ctx context.Context) int {
	doc, _ := rt.configDocument(ctx)
	return doc.engine.features.TerminalCommandTimeoutMs
}

// sessionSettings are a chat session door's inputs; a move reloads resident chat sessions (markSessionsForReload).
// Live settings (ignore files, the shell timeout, content collection) are pushed instead.
type sessionSettings struct {
	Presets               []string                 `json:"presets"`
	AdditionalDirectories []string                 `json:"additional_directories"`
	Memory                marotte.MemoryPreference `json:"memory"`
	Features              marotte.SessionFeatures  `json:"features"`
}

// readSessionSettings is every session door's inputs, from the configDoc owner with the administrator locks applied
// live.
func (rt *Runtime) readSessionSettings(ctx context.Context) sessionSettings {
	doc, src := rt.configDocument(ctx)
	s := doc.session
	features := s.features
	features.Workflows = lockedBool(currentLocks(rt.config.GovernanceLocks), marotte.LockWorkflows, features.Workflows)
	// A lock that turns workflows off leaves no launch mode, so a change with them off reloads no chat.
	if !features.Workflows {
		features.WorkflowLaunch = ""
	}
	return sessionSettings{
		Presets:               securityPresets(s.profile),
		AdditionalDirectories: rt.lifecycle.sessionDirs(s.dirEntries, src == fromDefaults || s.dirsInvalid),
		Memory:                s.memory,
		Features:              features,
	}
}

// sessionFingerprint is a chat session door's fingerprint; ok is false when it could not be computed.
func (rt *Runtime) sessionFingerprint(ctx context.Context) (string, bool) {
	raw, err := json.Marshal(rt.readSessionSettings(ctx))
	if err != nil {
		slog.Error("session settings could not be fingerprinted; this change does not reach open chats", "error", err)
		return "", false
	}
	return string(raw), true
}

// chatSessionOpts is what a chat's session door sends.
func (rt *Runtime) chatSessionOpts(ctx context.Context, rec *marotte.Chat, ask *sessionAsk, steering []marotte.ClientSteeringDoc) *marotte.SessionOpts {
	s := rt.readSessionSettings(ctx)
	opts := &marotte.SessionOpts{
		Role:                  marotte.SessionRoleChat,
		Model:                 ask.model,
		Effort:                ask.effort,
		Mode:                  doorMode(rec),
		Thinking:              ask.thinking,
		Presets:               s.Presets,
		AdditionalDirectories: s.AdditionalDirectories,
		Steering:              steering,
		Memory:                s.Memory,
		Features:              s.Features,
		Supervised:            rec.SupervisedMode,
		// marotte's autoCompactor is a chat's one compactor, at a turn's end.
		DisableAutoCompaction: true,
	}
	if rec.IsStartedBot() {
		opts.CustomAgents = []marotte.ClientCustomAgent{bot.Role(rec, botPaths())}
		if doc, ok := bot.SteeringDoc(rec); ok {
			opts.Steering = append(slices.Clone(steering), doc)
		}
		// A root, so KAS's own checks treat the memory file as the session's; marotte's grant is confineBotDir.
		if dir, ok := bot.Dir(rec.ID, botPaths()); ok && rec.Bot.Memory {
			opts.AdditionalDirectories = append(slices.Clone(s.AdditionalDirectories), dir)
		}
	}
	return opts
}

// doorMode is the mode a chat's door asks for. Only a started Bot's door registers the role, so it asks for the role
// whatever its record was left on, and any other record naming the role (a draft's, which marotte.Chat.AdoptMode holds
// there) asks for the default mode.
func doorMode(rec *marotte.Chat) string {
	switch {
	case rec.IsStartedBot():
		return marotte.BotRoleID
	case rec.CurrentModeID == marotte.BotRoleID:
		return marotte.DefaultModeID
	}
	return rec.CurrentModeID
}
