package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

func TestAutoCompactPolicy(t *testing.T) {
	tests := []struct {
		name string
		r    compactReading
		want bool
	}{
		{name: "switch off never compacts", r: compactReading{enabled: false, pct: 60, contextPct: 95, hasRealData: true, kasAutoOff: true}},
		{name: "80 with KAS auto on adds nothing", r: compactReading{enabled: true, pct: 80, contextPct: 85, hasRealData: true}},
		{name: "60 with KAS auto on fires at 60", r: compactReading{enabled: true, pct: 60, contextPct: 60, hasRealData: true}, want: true},
		{name: "60 with KAS auto on holds at 59", r: compactReading{enabled: true, pct: 60, contextPct: 59, hasRealData: true}},
		{name: "60 at or above the session's own point is KAS's", r: compactReading{enabled: true, pct: 60, contextPct: 70, hasRealData: true, sessionThreshold: 55}},
		{name: "85 on a session frozen auto-off fires at 85", r: compactReading{enabled: true, pct: 85, contextPct: 85, hasRealData: true, kasAutoOff: true}, want: true},
		{name: "frozen auto-off with the setting back at 80 still compacts", r: compactReading{enabled: true, pct: 80, contextPct: 80, hasRealData: true, kasAutoOff: true}, want: true},
		{name: "no real data never compacts", r: compactReading{enabled: true, pct: 60, contextPct: 90, kasAutoOff: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldAutoCompact(tc.r); got != tc.want {
				t.Errorf("shouldAutoCompact(%+v) = %v, want %v", tc.r, got, tc.want)
			}
		})
	}
}

// compactRig is an autoCompactor over stubbed inputs, recording every compact call.
type compactRig struct {
	ac      *autoCompactor
	bridge  *fakeBridge
	usage   marotte.Usage
	block   chan struct{}
	calls   int
	enabled bool
	pct     int
	live    bool
	mu      sync.Mutex
}

func newCompactRig(pct int, kasAutoOff bool) *compactRig {
	r := &compactRig{enabled: true, pct: pct, bridge: newFakeBridge()}
	r.bridge.startOpts = &marotte.StartOpts{DisableAutoCompaction: kasAutoOff}
	r.ac = &autoCompactor{
		chats: map[marotte.ChatID]*autoCompactState{},
		live: func(marotte.ChatID) bool {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.live
		},
		bridgeOf: func(marotte.ChatID) ACPBridge { return r.bridge },
		usage: func(context.Context, marotte.ChatID) (marotte.Usage, bool) {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.usage, true
		},
		policy: func(context.Context) (bool, int) { return r.enabled, r.pct },
		compact: func(ctx context.Context, _ command.CompactBridge) (bool, error) {
			r.mu.Lock()
			r.calls++
			block := r.block
			r.mu.Unlock()
			if block != nil {
				select {
				case <-block:
				case <-ctx.Done():
					return false, ctx.Err()
				}
			}
			return true, nil
		},
	}
	return r
}

func (r *compactRig) reading(pct float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.usage = marotte.Usage{ContextPct: pct, HasRealData: true}
}

func (r *compactRig) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// closeAt models one turn closing at pct: closer bookkeeping, then the turn-end trigger.
func (r *compactRig) closeAt(t *testing.T, pct float64) {
	t.Helper()
	r.reading(pct)
	r.ac.noteTurnClosed("c1")
	r.ac.afterTurn(t.Context(), "c1")
}

func TestAutoCompact_MinEffectLatch(t *testing.T) {
	r := newCompactRig(60, false)
	r.closeAt(t, 66)
	r.closeAt(t, 64)
	r.closeAt(t, 64)
	if got := r.count(); got != 2 {
		t.Fatalf("closes at 66, 64, 64 with pct 60 made %d compact calls, want 2 (the second close judges a 2-point drop ineffective)", got)
	}
	r.closeAt(t, 55)
	r.closeAt(t, 61)
	if got := r.count(); got != 3 {
		t.Errorf("after a reading below the point and a close at 61, calls = %d, want 3 (re-armed once)", got)
	}
}

func TestAutoCompact_TurnEndSkipsALiveChat(t *testing.T) {
	r := newCompactRig(60, false)
	r.live = true
	r.closeAt(t, 70)
	if got := r.count(); got != 0 {
		t.Errorf("a live chat got %d compact calls at turn end, want 0", got)
	}
}

func TestPreSendCompact_OnlyWhereMarotteIsTheCompactor(t *testing.T) {
	tests := []struct {
		name       string
		pct        int
		kasAutoOff bool
		enabled    bool
		want       int
	}{
		{name: "KAS auto on at 60", pct: 60, enabled: true},
		{name: "KAS auto on at 80", pct: 80, enabled: true},
		{name: "switch off", pct: 85, kasAutoOff: true},
		{name: "auto-off session at 85", pct: 85, kasAutoOff: true, enabled: true, want: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newCompactRig(tc.pct, tc.kasAutoOff)
			r.enabled = tc.enabled
			r.reading(95)
			r.ac.preSend(t.Context(), "c1")
			if got := r.count(); got != tc.want {
				t.Errorf("preSend made %d compact calls, want %d", got, tc.want)
			}
		})
	}

	t.Run("twice with no turn between", func(t *testing.T) {
		r := newCompactRig(85, true)
		r.reading(95)
		r.ac.preSend(t.Context(), "c1")
		r.ac.preSend(t.Context(), "c1")
		if got := r.count(); got != 1 {
			t.Errorf("two sends with no turn between made %d compact calls, want 1: the reading is pre-compaction until the next turn", got)
		}
	})
}

func TestPreSendCompact_AwaitsInFlightAndIsBounded(t *testing.T) {
	t.Run("awaits the running compaction", func(t *testing.T) {
		r := newCompactRig(85, true)
		r.block = make(chan struct{})
		r.reading(95)
		done := make(chan struct{})
		go func() {
			r.closeAt(t, 95)
			close(done)
		}()
		waitFor(t, func() bool { return r.count() == 1 })
		sent := make(chan struct{})
		go func() {
			r.ac.preSend(context.Background(), "c1")
			close(sent)
		}()
		select {
		case <-sent:
			t.Fatal("preSend returned while the turn-end compaction was still running")
		case <-time.After(50 * time.Millisecond):
		}
		close(r.block)
		<-sent
		<-done
		if got := r.count(); got != 1 {
			t.Errorf("compact calls = %d, want 1: the send awaited instead of issuing a second", got)
		}
	})

	t.Run("awaits a compaction claimed while it read the policy", func(t *testing.T) {
		r := newCompactRig(85, true)
		r.block = make(chan struct{})
		r.reading(95)
		entered, release := make(chan struct{}), make(chan struct{})
		var policyCalls atomic.Int32
		inner := r.ac.policy
		r.ac.policy = func(ctx context.Context) (bool, int) {
			if policyCalls.Add(1) == 1 {
				close(entered)
				<-release
			}
			return inner(ctx)
		}
		sent := make(chan struct{})
		go func() {
			r.ac.preSend(context.Background(), "c1")
			close(sent)
		}()
		<-entered
		done := make(chan struct{})
		go func() {
			r.closeAt(t, 95)
			close(done)
		}()
		waitFor(t, func() bool { return r.count() == 1 })
		close(release)
		select {
		case <-sent:
			t.Fatal("preSend returned while a compaction claimed during its policy read was still running")
		case <-time.After(50 * time.Millisecond):
		}
		close(r.block)
		<-sent
		<-done
		if got := r.count(); got != 1 {
			t.Errorf("compact calls = %d, want 1: the send joined the running compaction instead of issuing a second", got)
		}
	})

	t.Run("a compaction that never answers is bounded", func(t *testing.T) {
		prev := autoCompactPreSendBudget
		autoCompactPreSendBudget = 50 * time.Millisecond
		t.Cleanup(func() { autoCompactPreSendBudget = prev })
		r := newCompactRig(85, true)
		r.block = make(chan struct{})
		t.Cleanup(func() { close(r.block) })
		r.reading(95)
		start := time.Now()
		r.ac.preSend(t.Context(), "c1")
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("preSend held the prompt %s against a %s budget", elapsed, autoCompactPreSendBudget)
		}
	})
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAfterTurnClose_Order(t *testing.T) {
	var mu sync.Mutex
	var order []string
	record := func(step string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, step)
	}
	r := newCompactRig(60, false)
	r.reading(70)
	inner := r.ac.compact
	r.ac.compact = func(ctx context.Context, b command.CompactBridge) (bool, error) {
		record("compact")
		return inner(ctx, b)
	}
	bc := &BridgeCoordinator{
		turns:             newTurnRegistry(),
		lifecycle:         &lifetime{shutdownCtx: t.Context()},
		applyPendingModel: func(context.Context, marotte.ChatID) { record("model") },
		autoCompact:       r.ac,
		resolveEnds: func(context.Context, marotte.ChatID, command.TurnFence) command.EndFacts {
			record("resolve")
			return command.EndFacts{}
		},
		drainAfterClose: func(context.Context, marotte.ChatID, command.CloseFacts, command.EndFacts) { record("drain") },
	}
	bc.afterTurnClose(t.Context(), "c1", closeFacts{outcome: marotte.TurnOutcomeCompleted})
	if want := []string{"resolve", "model", "compact", "drain"}; !slices.Equal(order, want) {
		t.Errorf("afterTurnClose order = %v, want %v", order, want)
	}
}

// TestAutoCompact_TurnEndCallsCompact pins that a real closer reaches the trigger through afterTurnClose.
func TestAutoCompact_TurnEndCallsCompact(t *testing.T) {
	h, _, _ := newTestHub()
	r := newCompactRig(60, false)
	r.reading(62)
	h.coord.autoCompact = r.ac
	id, _ := h.stagePromptTurn(t, "c1")
	h.AbandonInFlightTurn(t.Context(), "c1", id, marotte.StopReasonInterrupted, "the pipe died", "", 0)
	waitFor(t, func() bool { return r.count() == 1 })
}

func writeCompactionSetting(t *testing.T, dir string, enabled bool, pct int) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		settings.KeyAutoCompactionEnabled: enabled,
		settings.KeyAutoCompactPct:        pct,
	})
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
}

func TestAutoCompactionPolicy_ReadsTheSettingAndFailsToTheDefault(t *testing.T) {
	if enabled, pct := autoCompactionPolicy(t.Context(), t.TempDir()); !enabled || pct != 80 {
		t.Errorf("autoCompactionPolicy with no config.json = (%v, %d), want (true, 80)", enabled, pct)
	}
	dir := t.TempDir()
	writeCompactionSetting(t, dir, false, 85)
	if enabled, pct := autoCompactionPolicy(t.Context(), dir); enabled || pct != 85 {
		t.Errorf("autoCompactionPolicy(stored false, 85) = (%v, %d), want (false, 85)", enabled, pct)
	}
	writeCompactionSetting(t, dir, true, 83)
	if _, pct := autoCompactionPolicy(t.Context(), dir); pct != 80 {
		t.Errorf("autoCompactionPolicy(stored 83) pct = %d, want 80", pct)
	}
	for _, tc := range []struct {
		enabled bool
		pct     int
		want    bool
	}{{true, 80, false}, {true, 60, false}, {true, 85, true}, {false, 60, true}} {
		if got := sessionDisablesAutoCompaction(tc.enabled, tc.pct); got != tc.want {
			t.Errorf("sessionDisablesAutoCompaction(%v, %d) = %v, want %v", tc.enabled, tc.pct, got, tc.want)
		}
	}
}

// TestChatSpawn_SendsTheCompactionPolicyAndOtherSpawnsDoNot pins that a chat spawn carries
// the policy's door value and the utility session always sends false.
func TestChatSpawn_SendsTheCompactionPolicyAndOtherSpawnsDoNot(t *testing.T) {
	dir := t.TempDir()
	writeCompactionSetting(t, dir, true, 85)
	var mu sync.Mutex
	var spawned []*recordingStartBridge
	cs := newTestChatStore()
	h := New(t.Context(), t.TempDir(), func() ACPBridge {
		rb := newRecordingStartBridge()
		mu.Lock()
		spawned = append(spawned, rb)
		mu.Unlock()
		return rb
	}, cs, WithConfigDir(dir))
	cs.wire(h)
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true }); err != nil {
		t.Fatalf("seed the chat: %v", err)
	}
	if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	if _, err := h.utility.get().session.acquire(t.Context()); err != nil {
		t.Fatalf("utility session: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	var chat, other int
	for _, rb := range spawned {
		o := rb.startOpts()
		if o.Lifetime == nil {
			continue
		}
		if o.DisableAutoCompaction {
			chat++
		} else {
			other++
		}
	}
	if chat != 1 || other < 1 {
		t.Errorf("spawns sending disableAutoCompaction true/false = %d/%d, want exactly the chat true and the utility session false", chat, other)
	}
}
