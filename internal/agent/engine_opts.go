package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"

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
	case reasonTelemetry:
		return "Usage telemetry"
	case marotte.EngineReasonKiroCLIVersion:
		return "the new kiro-cli version"
	case marotte.EngineReasonAccountSwitch:
		return "the new Kiro account"
	}
	return string(r)
}

// engineHeapMiB reads the heap limit; an absent or invalid value is 0, kiro-cli's default.
func engineHeapMiB(ctx context.Context, configDir string) int {
	var n int
	if settings.FieldInto(ctx, configDir, settings.KeyEngineHeapMiB, &n) && n >= 0 {
		return n
	}
	return 0
}

// resolveEngine reads the engine options a spawn at path starts with, and the fingerprint of their restart-only part.
// The readers are stat-checked cache reads, so the caller's cancellation is dropped: a cancelled read would answer
// defaults no engine spawned under.
func (rt *Runtime) resolveEngine(ctx context.Context, path string) engineSpec {
	ctx = context.WithoutCancel(ctx)
	dir := rt.lifecycle.configDir
	features := engineFeatures(ctx, dir)
	knowledge := knowledgeEnabled(ctx, dir)
	telemetryOff := rt.lifecycle.telemetryDisabled(rt.config.GovernanceLocks)
	heap := engineHeapMiB(ctx, dir)
	opts := &marotte.EngineOpts{
		Lifetime:          rt.lifecycle.shutdownCtx,
		Background:        rt.lifecycle.goUnlessDraining,
		ReapSession:       func(id marotte.SessionID) { rt.reapSessions([]string{string(id)}) },
		IgnoreFiles:       func(c context.Context) []string { return spawnIgnoreFiles(c, dir) },
		TerminalTimeout:   func(c context.Context) int { return terminalCommandTimeoutMs(c, dir) },
		ContentCollection: contentCollectionResolver(dir, rt.config.GovernanceLocks),
		ExtraArgs:         rt.acpArgs,
		AuthArgs:          rt.signin.spawnArgs(),
		Features:          features,
		HeapMiB:           heap,
		SecretStorage:     rt.secrets != nil,
		Knowledge:         knowledge,
		DisableTelemetry:  telemetryOff,
	}
	fp := map[marotte.EngineReason]string{
		reasonKnowledge:                    strconv.FormatBool(knowledge),
		reasonSteeringReminders:            strconv.FormatBool(features.SteeringReminders),
		reasonCloudFormation:               features.InfraSafetyMonitor,
		reasonModelFallback:                strconv.FormatBool(features.ModelFallback),
		reasonAutoRouting:                  features.AutoRouting,
		reasonAutoDelegation:               features.AutoDelegation,
		reasonHeap:                         strconv.Itoa(heap),
		reasonTelemetry:                    strconv.FormatBool(telemetryOff),
		marotte.EngineReasonKiroCLIVersion: path,
		marotte.EngineReasonAccountSwitch:  strconv.FormatBool(rt.signin.accountPending()),
	}
	return engineSpec{opts: opts, fingerprint: fp}
}

// sessionSettings are a chat session door's inputs; a move reloads resident chat sessions at their next idle open.
// Live settings (ignore files, the shell timeout, content collection) are pushed instead.
type sessionSettings struct {
	Presets               []string                 `json:"presets"`
	Memory                marotte.MemoryPreference `json:"memory"`
	Features              marotte.SessionFeatures  `json:"features"`
	DisableAutoCompaction bool                     `json:"disable_auto_compaction"`
}

func (rt *Runtime) readSessionSettings(ctx context.Context) sessionSettings {
	ctx = context.WithoutCancel(ctx)
	dir := rt.lifecycle.configDir
	return sessionSettings{
		Presets:               securityPresets(ctx, dir),
		Memory:                memoryPreference(ctx, dir),
		Features:              sessionFeatures(ctx, dir, currentLocks(rt.config.GovernanceLocks)),
		DisableAutoCompaction: sessionDisablesAutoCompaction(autoCompactionPolicy(ctx, dir)),
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
	return &marotte.SessionOpts{
		Role:                  marotte.SessionRoleChat,
		Model:                 ask.model,
		Effort:                ask.effort,
		Mode:                  rec.CurrentModeID,
		Thinking:              ask.thinking,
		Presets:               s.Presets,
		Steering:              steering,
		Memory:                s.Memory,
		Features:              s.Features,
		Supervised:            rec.SupervisedMode,
		DisableAutoCompaction: s.DisableAutoCompaction,
	}
}
