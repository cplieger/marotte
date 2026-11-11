package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"

	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/workflow"
)

// glanceCache is one revision's glance, built once for every client that asks it.
type glanceCache struct {
	paths map[string]bool
	doc   json.RawMessage
	rev   uint64
}

// glanceRead answers GET /api/runs/{id}?view=glance: the whole read as workflow.GlanceRead shapes
// it (each repeat through its latest pass), with the step ends and starts of the nodes it keeps. A
// read it cannot shape goes out whole, which the client also reads.
func (rs *Runs) glanceRead(ctx context.Context, id string) (json.RawMessage, error) {
	raw, x, held := rs.heldWhole(id)
	if !held {
		var err error
		if raw, x, err = rs.wholeRead(ctx, id); err != nil {
			return nil, err
		}
	}
	doc, paths, err := rs.glanceOf(id, raw, x)
	if err != nil {
		slog.Warn("workflow inspect: the run's glance could not be built; answering whole",
			"workflow_id", logsafe.Field(id), "error", err)
		return withRunExtras(raw, x)
	}
	x.ends, x.starts = keptRows(x.ends, paths), keptRows(x.starts, paths)
	out, err := withRunExtras(doc, x)
	if err != nil {
		slog.Warn("workflow inspect: the run's glance could not carry its extras",
			"workflow_id", logsafe.Field(id), "error", err)
		return doc, nil
	}
	return out, nil
}

// heldWhole is the whole read from the run's held revision, as headerRead serves the header from
// it; false when the run's cell holds none, which a fresh read answers.
func (rs *Runs) heldWhole(id string) (json.RawMessage, *runExtras, bool) {
	c := rs.cellFor(id)
	if c == nil {
		return nil, nil, false
	}
	rv := &c.revision
	rv.mu.Lock()
	defer rv.mu.Unlock()
	if rv.rev == 0 || rv.served == nil {
		return nil, nil, false
	}
	x := rs.heldExtras(c, rv)
	x.ends, x.starts = maps.Clone(rv.ends), maps.Clone(rv.starts)
	return rv.served, x, true
}

// glanceOf is raw's glance, from the run's cache when x describes a revision it holds.
func (rs *Runs) glanceOf(id string, raw json.RawMessage, x *runExtras) (doc json.RawMessage, paths map[string]bool, err error) {
	c := rs.asks.get(id)
	if x.rev != 0 && c != nil {
		c.revision.mu.Lock()
		g := c.revision.glance
		c.revision.mu.Unlock()
		if g.rev == x.rev {
			return g.doc, g.paths, nil
		}
	}
	doc, paths, err = workflow.GlanceRead(raw, x.starts)
	if err != nil {
		return nil, nil, err
	}
	if x.rev != 0 && c != nil {
		c.revision.mu.Lock()
		if x.rev >= c.revision.glance.rev {
			c.revision.glance = glanceCache{rev: x.rev, doc: doc, paths: paths}
		}
		c.revision.mu.Unlock()
	}
	return doc, paths, nil
}

// keptRows is rows for the paths a glance keeps.
func keptRows[T any](rows map[string]T, paths map[string]bool) map[string]T {
	out := make(map[string]T, len(paths))
	for path, row := range rows {
		if paths[path] {
			out[path] = row
		}
	}
	return out
}
