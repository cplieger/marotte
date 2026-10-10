package forges

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/cplieger/forgeapi"
)

const (
	// keeperInterval is how often the keeper visits every connection. The git
	// credential helper never refreshes, so a rotating token used only by git
	// relies on this to be renewed inside its refresh lead.
	keeperInterval = time.Minute
	// keeperTokenBudget bounds one connection's token read, refresh included.
	keeperTokenBudget = 30 * time.Second
)

// Keeper keeps every connection's credential fresh and persists the rotation
// cursor its client moved, so a restart resumes the Gitea family's fold
// rotation where it stood. The server is the one refreshing process for the
// store; the git credential helper only reads it.
type Keeper struct {
	manager *Manager
	// changed announces a row that turned reconnect-required.
	changed func(context.Context)
	every   time.Duration
}

// NewKeeper builds the keeper of m's connections. changed, which may be nil, is
// called once after a pass in which a row turned reconnect-required.
func NewKeeper(m *Manager, changed func(context.Context)) *Keeper {
	return &Keeper{manager: m, changed: changed, every: keeperInterval}
}

// Run makes one pass at once and one every minute after, and returns when ctx
// ends.
func (k *Keeper) Run(ctx context.Context) {
	tick := time.NewTicker(k.every)
	defer tick.Stop()
	for {
		k.pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// pass asks every connection's credential source for its token, which a source
// answers from the store with no request unless the token is due, and then
// persists each client's moved rotation cursor.
func (k *Keeper) pass(ctx context.Context) {
	m := k.manager
	if m.store == nil {
		return
	}
	recs, err := m.conns.load()
	if err != nil {
		return
	}
	turned := false
	for i := range recs {
		if ctx.Err() != nil {
			return
		}
		c, err := m.clients.clientFor(m.store, &recs[i])
		if err != nil {
			continue
		}
		if k.keepToken(ctx, recs[i].ID, c.cred) {
			turned = true
		}
		m.persistCursor(ctx, &recs[i], c.core)
	}
	if turned && k.changed != nil {
		k.changed(ctx)
	}
}

func (k *Keeper) keepToken(ctx context.Context, id string, src forgeapi.CredentialSource) bool {
	tctx, cancel := context.WithTimeout(ctx, keeperTokenBudget)
	defer cancel()
	_, err := src.Token(tctx)
	switch {
	case err == nil:
		return false
	case isReconnectRequired(err):
		return k.manager.markReconnectRequired(id, err)
	case ctx.Err() == nil:
		slog.Warn("forges: the credential keeper could not read a token", "connection", id, "error", err)
	}
	return false
}

// isReconnectRequired reports whether err is the library's refusal to use or
// renew a credential, which only a new sign-in answers.
func isReconnectRequired(err error) bool {
	var fe *forgeapi.Error
	return errors.As(err, &fe) && fe.Code == forgeapi.CodeReconnectRequired
}

// markReconnectRequired records err on id's row as a credential that needs a
// new sign-in, and reports whether the row was not marked before.
func (m *Manager) markReconnectRequired(id string, err error) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.forges[id]
	if !ok {
		return false
	}
	turned := !f.ReconnectRequired
	f.ReconnectRequired, f.Connected = true, false
	f.recordError(err)
	return turned
}

// persistCursor writes the rotation cursor core reports onto rec's stored
// record when it moved. The client was seeded with the stored cursor, so an
// unmoved one writes nothing.
func (m *Manager) persistCursor(ctx context.Context, rec *connectionRecord, core forgeapi.Core) {
	gov, ok := core.(forgeapi.Governor)
	if !ok {
		return
	}
	cursor := string(gov.BudgetState().RotationCursor)
	if cursor == rec.RotationCursor {
		return
	}
	err := m.conns.update(ctx, func(cur []connectionRecord) []connectionRecord {
		if i := slices.IndexFunc(cur, func(r connectionRecord) bool { return r.ID == rec.ID }); i >= 0 {
			cur[i].RotationCursor = cursor
		}
		return cur
	})
	if err != nil {
		slog.Warn("forges: the rotation cursor could not be persisted", "connection", rec.ID, "error", err)
	}
}
