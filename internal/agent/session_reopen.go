package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/securityprofile"
)

// ReopenChatSessions is the one way a setting a live session only reads at open reaches open
// chats: every chat bridge is marked, and each chat respawns and reloads its session at its next
// open (OpenBridge). A busy chat switches after its turn, one hosting a live run after the run.
func (rt *Runtime) ReopenChatSessions(reason string) {
	marked := rt.bridge.mgr.markChatBridgesForReopen()
	slog.Info("open chats reopen their session at their next message", "reason", reason, "marked", marked)
}

// SecurityProfileChanged applies a persisted profile change: presets ride the session door and
// KAS has no live setter, so the utility session restarts and open chats reopen.
func (rt *Runtime) SecurityProfileChanged(ctx context.Context) {
	rt.reconcileSessionSettings(ctx, "security profile")
	rt.Broadcast(ctx, marotte.NewEvent(marotte.EventSettingsUpdated, "", marotte.SettingsUpdatedPayload{}))
	rt.Broadcast(ctx, marotte.NewEvent(marotte.EventPermissionsChanged, "",
		marotte.PermissionsChangedPayload{Status: "success"}))
}

// EnsureCustomProfile runs Customize unless the profile already is Custom, so a rule an answer
// saves lands in the policy the picker names. It returns once the user file is written, and
// leaves both files as they were on any refusal but securityprofile.ErrRestoreFailed.
func (rt *Runtime) EnsureCustomProfile(ctx context.Context) error {
	store := &securityprofile.Store{
		List:      rt.config.PolicyList,
		ConfigDir: rt.lifecycle.configDir,
		WorkDir:   rt.lifecycle.workDir,
	}
	switched, err := store.EnsureCustom(ctx)
	if err != nil {
		if errors.Is(err, securityprofile.ErrRestoreFailed) {
			rt.Broadcast(ctx, marotte.NewEvent(marotte.EventPermissionsChanged, "",
				marotte.PermissionsChangedPayload{Status: "failed"}))
		}
		return err
	}
	if !switched {
		return nil
	}
	slog.Info("security profile switched to custom by an always answer")
	rt.SecurityProfileChanged(ctx)
	return nil
}

// sessionSettings leaves out what marotte pushes into live bridges (ignore files, the shell
// timeout, content collection): a field for one would reopen every chat on a change it needs no
// reopen for.
type sessionSettings struct {
	utilitySettings
	Memory                marotte.MemoryPreference `json:"memory"`
	Features              marotte.AgentFeatures    `json:"features"`
	ToolSearch            bool                     `json:"tool_search"`
	Knowledge             bool                     `json:"knowledge"`
	DisableAutoCompaction bool                     `json:"disable_auto_compaction"`
}

// utilitySettings is the part of sessionSettings the utility session spawns with
// (utilitySession.startLocked), so only a move here restarts it.
type utilitySettings struct {
	Presets          []string `json:"presets"`
	DisableTelemetry bool     `json:"disable_telemetry"`
}

type sessionFingerprints struct {
	chat    string
	utility string
}

// sessionBaseline holds the fingerprints every chat and utility process alive was spawned
// under, or has been marked to leave. Set at construction, when none is alive yet.
type sessionBaseline struct {
	seen sessionFingerprints
	mu   sync.Mutex
}

// sessionFingerprint covers the settings a chat session only reads at open, resolved the way
// spawnBridge resolves them, so a file or lock change from any writer moves it. ok is false only
// when it could not be computed.
func (rt *Runtime) sessionFingerprint(ctx context.Context) (fingerprints sessionFingerprints, ok bool) {
	// The readers answer their defaults on a cancelled ctx, which no live process spawned under;
	// they are stat-checked cache reads, so the caller's cancellation has nothing to interrupt.
	ctx = context.WithoutCancel(ctx)
	dir := rt.lifecycle.configDir
	features := agentFeatures(ctx, dir, currentLocks(rt.config.GovernanceLocks))
	features.TerminalCommandTimeoutMs = 0
	// The session door sends the ask-first flag only with planning on (kascap specPlanValue).
	if features.SpecPlan == "" {
		features.SpecAskClarification = false
	}
	utility := utilitySettings{
		Presets:          securityPresets(ctx, dir),
		DisableTelemetry: rt.lifecycle.telemetryDisabled(rt.config.GovernanceLocks),
	}
	chatRaw, chatErr := json.Marshal(sessionSettings{
		Memory:                memoryPreference(ctx, dir),
		Features:              features,
		ToolSearch:            toolSearchEnabled(ctx, dir),
		Knowledge:             knowledgeEnabled(ctx, dir),
		DisableAutoCompaction: sessionDisablesAutoCompaction(autoCompactionPolicy(ctx, dir)),
		utilitySettings:       utility,
	})
	utilityRaw, utilityErr := json.Marshal(utility)
	if err := errors.Join(chatErr, utilityErr); err != nil {
		slog.Error("session settings could not be fingerprinted; this change does not reach open chats", "error", err)
		return sessionFingerprints{}, false
	}
	return sessionFingerprints{chat: string(chatRaw), utility: string(utilityRaw)}, true
}

// ReconcileSessionSettings reopens open chats when a setting they only read at open moved since
// the last reconcile, and restarts the utility session when one it spawns with did. Every writer
// calls it after its write lands; OpenBridge and every utility access call it too, which is how a
// hand edit reaches them.
func (rt *Runtime) ReconcileSessionSettings(ctx context.Context) {
	rt.reconcileSessionSettings(ctx, "session settings")
}

// reconcileSessionSettings marks the chats under the baseline lock, so a caller that returns has
// them marked; the utility process stops outside it.
func (rt *Runtime) reconcileSessionSettings(ctx context.Context, reason string) {
	rt.sessionSeen.mu.Lock()
	now, ok := rt.sessionFingerprint(ctx)
	reopen := ok && now.chat != rt.sessionSeen.seen.chat
	restart := ok && now.utility != rt.sessionSeen.seen.utility
	if !reopen && !restart {
		rt.sessionSeen.mu.Unlock()
		return
	}
	if ok {
		rt.sessionSeen.seen = now
	}
	var utility *utilityRuntime
	if restart {
		utility = rt.utility.take()
	}
	if reopen {
		rt.ReopenChatSessions(reason)
	}
	rt.sessionSeen.mu.Unlock()
	if utility != nil {
		utility.session.Stop()
	}
}
