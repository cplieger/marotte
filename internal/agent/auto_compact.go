package agent

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/marotte"
)

// autoCompactPreSendBudget bounds one marotte-initiated compaction and a send's wait for
// one. Above KAS's 300 s _kiro/session/compact bound, so it only covers a wedged bridge.
// A var for tests.
var autoCompactPreSendBudget = 310 * time.Second

// autoCompactMinEffect is the drop, in context points, a compaction must show by the next
// close for the chat to stay eligible; otherwise it is suppressed until it reads below the point.
const autoCompactMinEffect = 5.0

// kasDefaultSummarizationPct is KAS's own summarization point, used when the session reported none.
const kasDefaultSummarizationPct = 80.0

// compactReading is everything the policy decides on, gathered at one moment.
type compactReading struct {
	contextPct float64
	// sessionThreshold is the session's reported summarization point; zero when omitted.
	sessionThreshold float64
	pct              int
	enabled          bool
	hasRealData      bool
	// kasAutoOff is the value SENT on the session door; KAS froze it, so it, not the current
	// setting, says whether KAS still compacts this chat.
	kasAutoOff bool
}

// shouldAutoCompact compacts when the switch is on, the reading is real and at or above
// the point, and KAS no longer compacts this session or the point sits below KAS's own.
func shouldAutoCompact(r compactReading) bool {
	if !r.enabled || !r.hasRealData || r.contextPct < float64(r.pct) {
		return false
	}
	if r.kasAutoOff {
		return true
	}
	kasPoint := r.sessionThreshold
	if kasPoint <= 0 {
		kasPoint = kasDefaultSummarizationPct
	}
	return float64(r.pct) < kasPoint
}

// autoCompactState is one chat's in-memory compaction bookkeeping.
type autoCompactState struct {
	// inflight closes when the running compaction returns.
	inflight chan struct{}
	// lastAttemptPct is the reading a successful compaction ran at, judged at the next close while judgePending holds.
	lastAttemptPct float64
	judgePending   bool
	suppressed     bool
	// compactedSinceLastTurn stands in for the context_usage frame KAS does not send after a
	// compaction; without it a send would compact twice.
	compactedSinceLastTurn bool
}

// autoCompactor drives the compaction policy; every compaction is KAS's own verb through command.Compact.
type autoCompactor struct {
	chats    map[marotte.ChatID]*autoCompactState
	live     func(marotte.ChatID) bool
	bridgeOf func(marotte.ChatID) ACPBridge
	usage    func(context.Context, marotte.ChatID) (marotte.Usage, bool)
	policy   func(context.Context) (enabled bool, pct int)
	compact  func(context.Context, command.CompactBridge) (bool, error)
	mu       sync.Mutex
}

func newAutoCompactor(bc *BridgeCoordinator) *autoCompactor {
	return &autoCompactor{
		chats: map[marotte.ChatID]*autoCompactState{},
		live:  bc.turns.live,
		bridgeOf: func(chatID marotte.ChatID) ACPBridge {
			if !bc.bridgeLive(chatID) {
				return nil
			}
			if sb := bc.Bridge(chatID); sb != nil {
				return sb.bridge
			}
			return nil
		},
		usage: func(ctx context.Context, chatID marotte.ChatID) (marotte.Usage, bool) {
			chat, ok := bc.chatStore.Get(ctx, chatID)
			if !ok {
				return marotte.Usage{}, false
			}
			return chat.Usage, true
		},
		policy: func(ctx context.Context) (bool, int) {
			return autoCompactionPolicy(ctx, bc.lifecycle.configDir)
		},
		compact: command.Compact,
	}
}

// stateLocked returns the chat's state, creating it. a.mu must be held.
func (a *autoCompactor) stateLocked(chatID marotte.ChatID) *autoCompactState {
	st := a.chats[chatID]
	if st == nil {
		st = &autoCompactState{}
		a.chats[chatID] = st
	}
	return st
}

// noteTurnClosed starts a new turn's worth of evidence: a prior compaction no longer stands in for a reading.
func (a *autoCompactor) noteTurnClosed(chatID marotte.ChatID) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if st := a.chats[chatID]; st != nil {
		st.compactedSinceLastTurn = false
	}
}

// forget drops the chat's state when the chat goes away.
func (a *autoCompactor) forget(chatID marotte.ChatID) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.chats, chatID)
}

// read gathers the policy's inputs, or false with no live bridge or chat record.
func (a *autoCompactor) read(ctx context.Context, chatID marotte.ChatID) (ACPBridge, compactReading, bool) {
	b := a.bridgeOf(chatID)
	if b == nil {
		return nil, compactReading{}, false
	}
	u, ok := a.usage(ctx, chatID)
	if !ok {
		return nil, compactReading{}, false
	}
	enabled, pct := a.policy(ctx)
	return b, compactReading{
		enabled:          enabled,
		pct:              pct,
		contextPct:       u.ContextPct,
		hasRealData:      u.HasRealData,
		kasAutoOff:       b.AutoCompactionDisabled(),
		sessionThreshold: u.SummarizationThresholdPct,
	}, true
}

// afterTurn is the turn-end trigger, after every closer that leaves a live bridge, only on
// an idle chat: a compaction beside a running turn is aborted upstream.
func (a *autoCompactor) afterTurn(ctx context.Context, chatID marotte.ChatID) {
	if a == nil {
		return
	}
	ctx = durable.Context(ctx)
	if a.live(chatID) {
		return
	}
	b, r, ok := a.read(ctx, chatID)
	if !ok {
		return
	}
	a.mu.Lock()
	st := a.stateLocked(chatID)
	if r.contextPct < float64(r.pct) {
		st.suppressed = false
	}
	// Decided before the judgment below, so a verdict suppresses from the next close on.
	done := st.claimLocked(r)
	if st.judgePending {
		st.judgePending = false
		if r.contextPct >= float64(r.pct) && st.lastAttemptPct-r.contextPct < autoCompactMinEffect {
			st.suppressed = true
			slog.Info("auto-compaction suppressed: the last compaction freed too little context",
				"chat_id", chatID, "before_pct", st.lastAttemptPct, "after_pct", r.contextPct)
		}
	}
	a.mu.Unlock()
	if done != nil {
		a.run(ctx, chatID, st, done, b, r.contextPct, "turn end")
	}
}

// preSend awaits a running compaction so the prompt does not abort it, and compacts first
// only where marotte is the session's one compactor. It never refuses the send.
func (a *autoCompactor) preSend(ctx context.Context, chatID marotte.ChatID) {
	if a == nil {
		return
	}
	b, r, ok := a.read(ctx, chatID)
	// Join or claim under one lock, so a turn-end claim is joined, never raced.
	a.mu.Lock()
	if st := a.chats[chatID]; st != nil && st.inflight != nil {
		running := st.inflight
		a.mu.Unlock()
		a.await(ctx, chatID, running)
		return
	}
	if !ok || !r.kasAutoOff {
		a.mu.Unlock()
		return
	}
	st := a.stateLocked(chatID)
	done := st.claimLocked(r)
	a.mu.Unlock()
	if done != nil {
		a.run(ctx, chatID, st, done, b, r.contextPct, "before send")
	}
}

// claimLocked marks a compaction in flight when the policy fires and returns its handle, or nil. a.mu must be held.
func (st *autoCompactState) claimLocked(r compactReading) chan struct{} {
	if st.inflight != nil || st.suppressed || st.compactedSinceLastTurn || !shouldAutoCompact(r) {
		return nil
	}
	st.inflight = make(chan struct{})
	return st.inflight
}

// await holds a send until the running compaction returns, bounded by autoCompactPreSendBudget.
func (a *autoCompactor) await(ctx context.Context, chatID marotte.ChatID, running <-chan struct{}) {
	wait, cancel := context.WithTimeout(ctx, autoCompactPreSendBudget)
	defer cancel()
	select {
	case <-running:
	case <-wait.Done():
		slog.Warn("a send stopped waiting for the running compaction", "chat_id", chatID, "error", wait.Err())
	}
}

// run performs the claimed compaction. A refusal or error changes nothing and is retried at the next trigger.
func (a *autoCompactor) run(ctx context.Context, chatID marotte.ChatID, st *autoCompactState, done chan struct{}, b ACPBridge, atPct float64, trigger string) {
	callCtx, cancel := context.WithTimeout(ctx, autoCompactPreSendBudget)
	accepted, err := a.compact(callCtx, b)
	cancel()

	a.mu.Lock()
	if cur := a.chats[chatID]; cur == st {
		st.inflight = nil
		if err == nil && accepted {
			st.lastAttemptPct = atPct
			st.judgePending = true
			st.compactedSinceLastTurn = true
		}
	}
	a.mu.Unlock()
	close(done)

	switch {
	case err != nil:
		slog.Warn("auto-compaction failed", "chat_id", chatID, "trigger", trigger, "context_pct", atPct, "error", err)
	case !accepted:
		slog.Info("auto-compaction refused; retried at the next trigger", "chat_id", chatID, "trigger", trigger)
	default:
		slog.Info("auto-compaction accepted", "chat_id", chatID, "trigger", trigger, "context_pct", atPct)
	}
}
