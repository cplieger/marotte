package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
)

// hookTriggers maps each KAS hook id to its canonical trigger, for the translator's hook cards. It holds the
// workspace list (_kiro/hooks/list, replaced on each list) and every session projection _kiro/hooks/didChange
// carried (merged, since only a projection names an agent profile's hooks). A frame's hookId, a list entry's id and
// a projection entry's id are all the KAS registry hook's id.
type hookTriggers struct {
	ctx       context.Context
	list      func(context.Context) ([]kasHook, error)
	spawn     func(func()) bool
	listed    map[string]string
	projected map[string]string
	mu        sync.Mutex
	// fresh: the last list ran after the last change, so a miss is a hook no list names and lists nothing.
	fresh bool
	// listedOnce: a list has succeeded, so a later failure leaves a map worth keeping.
	listedOnce bool
	listing    bool
	again      bool
}

// newHookTriggers lists through list on goroutines spawn starts; ctx bounds every list.
func newHookTriggers(ctx context.Context, list func(context.Context) ([]kasHook, error), spawn func(func()) bool) *hookTriggers {
	return &hookTriggers{ctx: ctx, list: list, spawn: spawn, listed: map[string]string{}, projected: map[string]string{}}
}

// HookTrigger answers from the cache and never waits: a miss since the last change starts one list.
func (c *hookTriggers) HookTrigger(hookID string) (string, bool) {
	c.mu.Lock()
	trigger, ok := c.listed[hookID]
	if !ok {
		trigger, ok = c.projected[hookID]
	}
	stale := !ok && !c.fresh
	c.mu.Unlock()
	if stale {
		c.refresh()
	}
	return trigger, ok
}

// changed applies a _kiro/hooks/didChange: its projection merges in, and the workspace list is read again.
func (c *hookTriggers) changed(params json.RawMessage) {
	var p kasHooksListResult
	if len(params) > 0 && json.Unmarshal(params, &p) == nil {
		c.mu.Lock()
		addTriggers(c.projected, p.Hooks)
		c.mu.Unlock()
	}
	c.stale()
}

// stale marks the list out of date and reads it again: a new engine, or a hook file changed.
func (c *hookTriggers) stale() {
	c.mu.Lock()
	c.fresh = false
	c.mu.Unlock()
	c.refresh()
}

// refresh lists once at a time; a call during a list makes that list run again when it ends.
func (c *hookTriggers) refresh() {
	c.mu.Lock()
	if c.listing {
		c.again = true
		c.mu.Unlock()
		return
	}
	c.listing = true
	c.mu.Unlock()
	if !c.spawn(c.runLists) {
		c.mu.Lock()
		c.listing = false
		c.mu.Unlock()
	}
}

func (c *hookTriggers) runLists() {
	for {
		hooks, err := c.list(c.ctx)
		c.mu.Lock()
		if err == nil {
			c.listed = map[string]string{}
			addTriggers(c.listed, hooks)
			c.listedOnce = true
		}
		// A failure after a good list counts as fresh, or every hook frame would list again; the next change retries.
		// Before one, the map is empty and the next miss retries.
		c.fresh = err == nil || c.listedOnce
		kept := c.listedOnce
		rerun := c.again
		c.again = false
		c.listing = rerun
		c.mu.Unlock()
		switch {
		case err == nil:
		case kept:
			slog.Warn("hooks: the hook list failed; hook cards keep the last known triggers until the next change "+
				"(engine ready or a hook change)", "error", err)
		default:
			slog.Warn("hooks: the hook list failed; the next hook frame lists again", "error", err)
		}
		if !rerun {
			return
		}
	}
}

func addTriggers(into map[string]string, hooks []kasHook) {
	for i := range hooks {
		if trigger, ok := marotte.CanonicalHookTrigger(hooks[i].Meta.Trigger); ok && hooks[i].ID != "" {
			into[hooks[i].ID] = trigger
		}
	}
}
