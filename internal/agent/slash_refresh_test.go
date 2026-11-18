package agent

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// recallCommandsFrame is the menu KAS sends once a prompt was renamed so its command reads recall.
func recallCommandsFrame(t *testing.T, sid string) *marotte.RPCResponse {
	t.Helper()
	return &marotte.RPCResponse{Method: marotte.MethodSessionUpdate, Params: mustJSON(t, map[string]any{
		"sessionId": sid,
		"update": map[string]any{
			"sessionUpdate": "available_commands_update",
			"availableCommands": []map[string]any{
				{"name": "recall", "description": "(file prompt)", "_meta": map[string]any{"kiro": map[string]any{"type": "prompt"}}},
			},
		},
	})}
}

// heldPasses serves every later observer pass from a fake whose door sends no menu, and lists the passes opened.
func heldPasses(t *testing.T, h *Runtime) func() []*fakeBridge {
	t.Helper()
	src := fakeSourceOf(t, h)
	var mu sync.Mutex
	var passes []*fakeBridge
	src.setObserver(func() engineSession {
		b := newFakeBridge()
		b.account, b.noDoorSeeds = &src.account, true
		mu.Lock()
		passes = append(passes, b)
		mu.Unlock()
		return b
	})
	return func() []*fakeBridge {
		mu.Lock()
		defer mu.Unlock()
		var opened []*fakeBridge
		for _, b := range passes {
			if b.startCount() > 0 {
				opened = append(opened, b)
			}
		}
		return opened
	}
}

func refreshRunning(h *Runtime) bool {
	h.slashRefresh.mu.Lock()
	defer h.slashRefresh.mu.Unlock()
	return h.slashRefresh.running
}

func awaitPasses(t *testing.T, opened func() []*fakeBridge, n int) []*fakeBridge {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		if got := opened(); len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d observer passes opened after 5s, want %d", len(opened()), n)
		}
	}
}

func awaitPassClosed(t *testing.T, b *fakeBridge) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !sessionClosed(b); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the observer pass stayed open 5s after KAS sent its menu")
		}
	}
}

// A refresh opens an observer pass, adopts the menu KAS sends at its door and announces it once, then closes the pass.
func TestRefreshSlashCommands_OpensAPassAndAdoptsItsList(t *testing.T) {
	t.Parallel()
	h, _, _ := newTestHub()
	opened := heldPasses(t, h)
	before := h.bus.fanout.Position().Head

	h.RefreshSlashCommands()
	pass := awaitPasses(t, opened, 1)[0]
	pass.deliver(recallCommandsFrame(t, string(pass.SessionID())))
	awaitPassClosed(t, pass)

	rec := httptest.NewRecorder()
	h.handleSlashCommands(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/slash-commands", http.NoBody))
	if body := rec.Body.String(); !strings.Contains(body, `"recall"`) {
		t.Errorf("GET /api/slash-commands = %s, want the refreshed menu naming recall", body)
	}
	if types := extractTypes(t, bufferedSince(h, before)); !slices.Equal(types, []string{string(marotte.EventSlashCommandsChanged)}) {
		t.Errorf("events = %v, want one slash_commands_changed", types)
	}
}

// Calls made while a pass is open queue one more pass, not one each, and the queued pass runs after the first closes.
func TestRefreshSlashCommands_CoalescesCallsDuringAPass(t *testing.T) {
	t.Parallel()
	h, _, _ := newTestHub()
	opened := heldPasses(t, h)

	h.RefreshSlashCommands()
	first := awaitPasses(t, opened, 1)[0]
	h.RefreshSlashCommands()
	h.RefreshSlashCommands()
	h.RefreshSlashCommands()
	first.deliver(recallCommandsFrame(t, string(first.SessionID())))
	awaitPassClosed(t, first)
	second := awaitPasses(t, opened, 2)[1]
	second.deliver(recallCommandsFrame(t, string(second.SessionID())))
	awaitPassClosed(t, second)

	for deadline := time.Now().Add(5 * time.Second); refreshRunning(h); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the refresh loop still ran 5s after its second pass closed")
		}
	}
	if got := len(opened()); got != 2 {
		t.Errorf("%d observer passes opened for four calls, three of them during the first pass; want 2", got)
	}
}
