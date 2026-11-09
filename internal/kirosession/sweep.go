package kirosession

import (
	"context"
	"log/slog"
	"maps"
	"time"
)

// Holder is a record that names KAS sessions marotte will load again. An error means it could not name everything it
// holds, and the round removes nothing.
type Holder interface {
	HeldSessions(ctx context.Context) ([]string, error)
}

// Holders are the sweep's holders outside KAS's own state, split by where their record lives.
type Holders struct {
	// Records live in marotte's config dir. When together they hold none while sessions exist, the round refuses:
	// KAS's run state and resident sessions come from $KIRO_HOME's side, so they cannot show the two share a volume.
	Records []Holder
	// Live names the sessions open in the agent engine now.
	Live []Holder
}

// sweepInterval is how often the orphan sweep runs after its first round.
const sweepInterval = time.Hour

// OrphanSweep owns the orphan sweep's keep-list: each round unions every holder's answer with the launching session
// of every run KAS keeps state for (runParents), and no other code builds one.
type OrphanSweep struct {
	reaper  *Reaper
	holders Holders
}

// NewOrphanSweep returns the sweep of r's tree, keeping what h and KAS's run state hold.
func NewOrphanSweep(r *Reaper, h Holders) *OrphanSweep {
	return &OrphanSweep{reaper: r, holders: h}
}

// Run waits for gate, runs one round, then one every sweepInterval until ctx ends; a nil gate proceeds at once. The
// gate is the caller's evidence that this process owns the config dir its Records read.
func (s *OrphanSweep) Run(ctx context.Context, gate <-chan struct{}) {
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return
		}
	}
	s.Once(ctx)
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Once(ctx)
		}
	}
}

// Once runs one round. A holder that cannot answer, or a run state that exists but cannot be read, removes nothing.
func (s *OrphanSweep) Once(ctx context.Context) {
	keep := map[string]struct{}{}
	if err := addHeld(ctx, s.holders.Records, keep); err != nil {
		logSkip(err)
		return
	}
	if len(keep) == 0 {
		s.reaper.sweep(keep)
		return
	}
	if err := addHeld(ctx, s.holders.Live, keep); err != nil {
		logSkip(err)
		return
	}
	parents, corrupt, err := s.reaper.runParents()
	if err != nil {
		logSkip(err)
		return
	}
	if len(corrupt) > 0 {
		slog.Warn("kirosession: a run's state file is corrupt; KAS cannot load that run, so its launching session is not kept",
			"paths", corrupt)
	}
	maps.Copy(keep, parents)
	s.reaper.sweep(keep)
}

func addHeld(ctx context.Context, holders []Holder, into map[string]struct{}) error {
	for _, h := range holders {
		held, err := h.HeldSessions(ctx)
		if err != nil {
			return err
		}
		for _, id := range held {
			into[id] = struct{}{}
		}
	}
	return nil
}

func logSkip(err error) {
	slog.Warn("kirosession: skipping orphan sweep, a session holder could not answer", "error", err)
}
