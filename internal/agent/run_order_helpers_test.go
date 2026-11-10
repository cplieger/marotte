package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// runIDs is the dock's order as the chat's header lists it: each compaction as "compaction", then each row, unread
// or retry entry not yet taken, in list order.
func runIDs(t *testing.T, cs *testChatStore, chatID marotte.ChatID) []string {
	t.Helper()
	c, ok := cs.Get(t.Context(), chatID)
	if !ok {
		return nil
	}
	var out []string
	for _, e := range c.Run {
		switch {
		case e.Kind == marotte.RunKindCompaction:
			out = append(out, "compaction")
		case e.Kind.TurnKind() && e.Kind != marotte.RunKindTurn && !e.State.Live():
			out = append(out, e.ID)
		}
	}
	return out
}

// runWait is what the header says id waits for: "compaction" when it goes when the compaction ahead ends, else "turn".
func runWait(t *testing.T, cs *testChatStore, chatID marotte.ChatID, id string) string {
	t.Helper()
	c, ok := cs.Get(t.Context(), chatID)
	if !ok {
		return "turn"
	}
	for _, e := range c.Header().Run.Entries {
		if e.ID == id && e.Wait.After == marotte.WaitCompaction {
			return "compaction"
		}
	}
	return "turn"
}

// beforeTake runs fn once, in the first close tail from now on, just before its end lets the list take what runs
// next.
func beforeTake(h *Runtime, fn func(ctx context.Context)) {
	var once sync.Once
	tailHooks(h, func(_ marotte.ChatID, ended bool) {
		if !ended {
			once.Do(func() { fn(context.Background()) })
		}
	})
}

// promptGates parks the n-th session/prompt written from now on (0-based) until open(n); the test's cleanup opens
// every gate.
func promptGates(t *testing.T, br *fakeBridge) (open func(n int)) {
	t.Helper()
	var (
		mu     sync.Mutex
		next   int
		all    bool
		gates  = map[int]chan struct{}{}
		opened = map[int]bool{}
	)
	gate := func(n int) chan struct{} {
		if gates[n] == nil {
			gates[n] = make(chan struct{})
			if all {
				close(gates[n])
				opened[n] = true
			}
		}
		return gates[n]
	}
	br.setOnCall(func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method != marotte.MethodPrompt {
			return nil, nil, false
		}
		mu.Lock()
		g := gate(next)
		next++
		mu.Unlock()
		<-g
		return nil, nil, false
	})
	open = func(n int) {
		mu.Lock()
		defer mu.Unlock()
		g := gate(n)
		if !opened[n] {
			opened[n] = true
			close(g)
		}
	}
	t.Cleanup(func() {
		mu.Lock()
		all = true
		for n, g := range gates {
			if !opened[n] {
				opened[n] = true
				close(g)
			}
		}
		mu.Unlock()
	})
	return open
}
