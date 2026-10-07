package server

import (
	"context"
	"log/slog"
	"maps"
	"slices"
)

// SeedKiroSettings writes each allowlisted kiro-cli setting's Seed where kiro-cli holds no value,
// so a user's own choice survives a restart. A locked key is left to its lock, and a settings
// document kiro-cli cannot list seeds nothing. Call it once kiro-cli is installed.
func (s *Server) SeedKiroSettings(ctx context.Context) {
	if s.cliRunner == nil {
		return
	}
	// The same lock as a user write and a lock application, so neither lands between the
	// absence check and the seed.
	s.heldKiroPrefs.mu.Lock()
	defer s.heldKiroPrefs.mu.Unlock()
	listCtx, cancel := context.WithTimeout(ctx, s.cliTimeouts.Settings)
	listed := s.readKiroSettingsList(listCtx)
	cancel()
	if listed == nil {
		slog.Warn("kiro-cli settings could not be listed; seeding none, so an unset switch shows its default")
		return
	}
	seeded := false
	for _, key := range slices.Sorted(maps.Keys(allowedKiroSettings)) {
		if _, set := listed[key]; set {
			continue
		}
		if _, locked := s.kiroSettingLock(key); locked {
			continue
		}
		if err := s.runKiroSetting(ctx, key, allowedKiroSettings[key].Seed); err != nil {
			slog.Warn("kiro-cli setting was not seeded", "key", key, "error", err)
			continue
		}
		seeded = true
	}
	// telemetry.enabled reaches a chat only when its process starts.
	if seeded && s.agent != nil {
		s.agent.ReconcileSessionSettings(ctx)
	}
}
