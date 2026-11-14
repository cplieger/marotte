package chat

import (
	"context"
	"fmt"
	"time"

	"github.com/cplieger/marotte/internal/chat/archive"
	"github.com/cplieger/marotte/internal/marotte"
)

// PurgeConfig is every hook the retention scheduler runs with, all given at construction.
type PurgeConfig struct {
	// Retention answers the current window; zero or less keeps every chat. Required.
	Retention func() time.Duration
	// OnPurge receives each removed chat and its session chain, after the record lock is released. Required.
	OnPurge func(chatID marotte.ChatID, sessionChain []string)
	// SidePasses run on every wake whatever Retention answers.
	SidePasses []func(context.Context) archive.PurgeResult
}

// NewPurgeScheduler builds the retention scheduler over store. A nil Retention or OnPurge is archive.ErrMissingHook.
// Its context arrives at PurgeScheduler.Start.
func NewPurgeScheduler(store *Store, cfg PurgeConfig) (*archive.PurgeScheduler, error) {
	switch {
	case cfg.Retention == nil:
		return nil, fmt.Errorf("%w: Retention", archive.ErrMissingHook)
	case cfg.OnPurge == nil:
		return nil, fmt.Errorf("%w: OnPurge", archive.ErrMissingHook)
	}
	return archive.NewPurgeScheduler(archive.New(store, cfg.OnPurge), cfg.Retention, cfg.SidePasses...), nil
}
