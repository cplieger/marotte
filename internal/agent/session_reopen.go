package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/securityprofile"
)

// reopenChatSessions is the one way a setting a live session only reads at open reaches open
// chats: every chat bridge is marked, and each chat respawns and reloads its session at its next
// open (openBridge). A busy chat switches after its turn, one hosting a live run after the run.
func (rt *Runtime) reopenChatSessions(reason string) {
	marked := rt.bridge.mgr.markChatBridgesForReopen()
	slog.Info("open chats reopen their session at their next message", "reason", reason, "marked", marked)
}

// SecurityProfileChanged applies a persisted profile change: presets ride the session door and
// KAS has no live setter, so the utility session restarts and open chats reopen.
func (rt *Runtime) SecurityProfileChanged(ctx context.Context) {
	rt.reconcileSettingsWrite(ctx, "security profile")
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
// the last reconcile, restarts the utility session when one it spawns with did, and pushes every
// live process each live setting it has not confirmed. Every writer calls it after its write
// lands, and it returns once each push landed or failed. A hand edit has no writer to call it:
// the next chat open or utility use syncs its own process, then every other one in the background.
func (rt *Runtime) ReconcileSessionSettings(ctx context.Context) {
	rt.reconcileSettingsWrite(ctx, "session settings")
}

func (rt *Runtime) reconcileSettingsWrite(ctx context.Context, reason string) {
	rt.reconcileSessionSettings(ctx, reason, time.Now().Add(ignorePushTimeout))
	rt.pushLiveToAll(ctx)
}

func (rt *Runtime) reconcileUtilityUse(ctx context.Context) {
	rt.reconcileSessionSettings(ctx, "session settings", time.Now().Add(ignorePushTimeout))
	if u := rt.utility.peek(); u != nil {
		u.session.syncLive(ctx, rt.liveSettings)
	}
	rt.fanOutLive(liveSurface{utility: true})
}

// reconcileSessionSettings marks chats for reopen and restarts the utility session as the
// fingerprints moved, and syncs what this server applies itself; it pushes to no chat process.
// The sync and the utility stop wait no later than deadline in all.
func (rt *Runtime) reconcileSessionSettings(ctx context.Context, reason string, deadline time.Time) {
	b := &rt.sessionSeen
	b.mu.Lock()
	now, ok := rt.sessionFingerprint(ctx)
	reopen := ok && now.chat != b.seen.chat
	restart := ok && now.utility != b.seen.utility
	if ok {
		b.seen = now
	}
	start := make(chan struct{})
	var retired <-chan struct{}
	if restart {
		retired = rt.claimUtilityRetire(ctx, start)
	}
	if reopen {
		rt.reopenChatSessions(reason)
	}
	b.mu.Unlock()
	syncCtx, cancel := context.WithDeadline(ctx, deadline)
	rt.syncProcessLive(syncCtx)
	cancel()
	close(start)
	if retired != nil {
		awaitUtilityRetire(retired, deadline)
	}
}

// claimUtilityRetire takes the utility session whose spawn settings moved, for a stop on inflight that begins once
// start closes, and returns a channel closed when that stop ends. It returns nil when no session is built, and once
// Shutdown has begun, which leaves the session to Shutdown's own utility stop.
func (rt *Runtime) claimUtilityRetire(ctx context.Context, start <-chan struct{}) <-chan struct{} {
	done := make(chan struct{})
	claimed := rt.lifecycle.goClaimed(func() func() {
		utility := rt.utility.take()
		if utility == nil {
			return nil
		}
		return func() {
			defer close(done)
			<-start
			// It serves the leases it holds until Stop.
			utility.session.syncLive(ctx, rt.liveSettings)
			utility.session.Stop()
		}
	})
	if !claimed {
		return nil
	}
	return done
}

// awaitUtilityRetire waits until deadline at most: Stop waits for a start in flight and the process teardown, which
// only the handshake budget and the teardown graces bound. A stop still running finishes on inflight.
func awaitUtilityRetire(done <-chan struct{}, deadline time.Time) {
	select {
	case <-done:
		return
	default:
	}
	start := time.Now()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		slog.Warn("utility session stop still running; it finishes in the background",
			"waited_ms", time.Since(start).Milliseconds())
	}
}
