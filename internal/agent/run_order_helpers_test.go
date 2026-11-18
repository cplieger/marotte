package agent

import (
	"encoding/json"
	"slices"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// runIDs is the dock's order as the chat's header lists it: each row, unread or retry entry not yet taken, in list
// order.
func runIDs(t *testing.T, cs *testChatStore, chatID marotte.ChatID) []string {
	t.Helper()
	c, ok := cs.Get(t.Context(), chatID)
	if !ok {
		return nil
	}
	var out []string
	for _, e := range c.Run {
		if e.Kind.TurnKind() && e.Kind != marotte.RunKindTurn && !e.State.Live() {
			out = append(out, e.ID)
		}
	}
	return out
}

// runStates is every entry of the chat's stored list as "<name> <state>": a kiro-cli turn is named "wire" or "run",
// everything else by its id.
func runStates(t *testing.T, cs *testChatStore, chatID marotte.ChatID) []string {
	t.Helper()
	c, ok := cs.Get(t.Context(), chatID)
	if !ok {
		return nil
	}
	var out []string
	for _, e := range c.Run {
		name := e.ID
		switch {
		case e.Kind == marotte.RunKindTurn && e.Origin == marotte.RunFromWire:
			name = "wire"
		case e.Kind == marotte.RunKindTurn && e.Origin == marotte.RunFromRun:
			name = "run"
		}
		out = append(out, name+" "+string(e.State))
	}
	return out
}

// runView is the chat's header's run list.
func runView(t *testing.T, cs *testChatStore, chatID marotte.ChatID) marotte.RunView {
	t.Helper()
	c, ok := cs.Get(t.Context(), chatID)
	if !ok {
		return marotte.RunView{}
	}
	return c.Header().Run
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

// stopUnderHold opens a turn on c1, queues m-q0 behind it, and closes it on the cancel grace while kiro-cli still
// reports the run: the close's KAS hold stands until the test reports kiro-cli idle.
func stopUnderHold(t *testing.T, h *Runtime) {
	t.Helper()
	turnID, _ := h.stagePromptTurn(t, "c1")
	queueTextDuring(t, h, "m-q0", "queued before the stop")
	h.coord.WireTurnActivity("c1", true)
	graceClose(t, h, turnID)
}

// opensInOrder reports that every id has opened a turn, in the order given.
func opensInOrder(t *testing.T, cs *testChatStore, ids ...string) bool {
	t.Helper()
	opened := promptIDs(t, cs, "c1")
	last := -1
	for _, id := range ids {
		at := slices.Index(opened, id)
		if at <= last {
			return false
		}
		last = at
	}
	return true
}
