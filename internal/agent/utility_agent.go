// Package agent runs the utility text-generation agent (utility_session.go splits the runtime's two roles).
// UtilityPrompt serves one text turn at a time (turnMu); the session's stateless RPC reads do not wait on it.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

const maxUtilityPrompts = 20

// maxUtilityPromptBytes is the cumulative prompt-size budget per session: past about 64 KB the re-sent context costs
// more per turn than a recycle.
const maxUtilityPromptBytes = 64 * 1024

// Its prompt counters and applied effort are keyed to the session generation, so a restart
// underneath it (recycle, reset, idle cull) starts them fresh.
type utilityAgent struct {
	session *utilitySession
	// currentEffort is the reasoning-effort level last applied to the live session (empty = model default).
	currentEffort marotte.EffortLevel

	// turnMu serializes text turns: one session cannot interleave two prompt streams.
	turnMu sync.Mutex
	// counterGen is the session generation the counter and effort fields belong to.
	counterGen  uint64
	promptCount int
	// promptBytes sums every prompt sent on the session. Each turn re-sends the whole conversation, so a byte budget
	// bounds the re-billed context.
	promptBytes int
	// effortUnsupported latches after a failed effortLevel set_config_option (the cheapest model may expose none),
	// and clears on a generation change so a different model re-probes once.
	effortUnsupported bool
}

func newUtilityAgent(session *utilitySession) *utilityAgent {
	return &utilityAgent{session: session}
}

// UtilityPrompt sends a prompt on the utility session and returns the text response, starting the session lazily.
// Safe for concurrent use; text turns serialize on turnMu. The session recycles after maxUtilityPrompts prompts or
// maxUtilityPromptBytes of input. effort is the per-task level ("" keeps the current one).
func (ua *utilityAgent) UtilityPrompt(ctx context.Context, prompt string, effort marotte.EffortLevel) (string, error) {
	ua.turnMu.Lock()
	defer ua.turnMu.Unlock()

	lease, err := ua.session.acquire(ctx)
	if err != nil {
		return "", err
	}
	ua.syncCounters(lease.gen)

	if ua.promptCount >= maxUtilityPrompts || ua.promptBytes >= maxUtilityPromptBytes {
		slog.Info("utility bridge recycled", "prompts", ua.promptCount, "prompt_bytes", ua.promptBytes)
		ua.session.resetIf(lease.gen)
		if lease, err = ua.session.acquire(ctx); err != nil {
			return "", err
		}
		ua.syncCounters(lease.gen)
	}
	ua.promptCount++
	ua.promptBytes += len(prompt)

	ua.applyEffort(ctx, lease, effort)

	drainLeftoverChunks(lease.chunks)

	resp, err := lease.bridge.Call(ctx, marotte.MethodPrompt, utilitySessionParams(lease.bridge, map[string]any{
		marotte.KeyPrompt: []map[string]any{marotte.TextBlock(utilitySystemPrompt + prompt)},
	}))
	if err != nil {
		ua.session.resetIf(lease.gen)
		return "", fmt.Errorf("utility prompt: %w", err)
	}

	return ua.drainResponse(ctx, lease, resp)
}

// syncCounters resets the per-session bookkeeping when the session generation changed, so a restarted session never
// inherits a stale prompt count or effort latch.
func (ua *utilityAgent) syncCounters(gen uint64) {
	if ua.counterGen == gen {
		return
	}
	ua.counterGen = gen
	ua.promptCount = 0
	ua.promptBytes = 0
	ua.currentEffort = ""
	ua.effortUnsupported = false
}

// applyEffort sets the reasoning-effort level via session/set_config_option when it differs from the applied one.
// Best-effort: a failure latches effortUnsupported until the next session start. Caller holds turnMu.
func (ua *utilityAgent) applyEffort(ctx context.Context, lease sessionLease, effort marotte.EffortLevel) {
	if effort == "" || effort == ua.currentEffort || ua.effortUnsupported || !effort.Valid() {
		return
	}
	_, err := lease.bridge.Call(ctx, marotte.MethodSetConfigOption, utilitySessionParams(lease.bridge, map[string]any{
		"configId": marotte.ConfigOptionEffort,
		"value":    string(effort),
	}))
	if err != nil {
		slog.Debug("utility bridge: effortLevel unsupported on this session", "effort", effort, "error", err)
		ua.effortUnsupported = true
		return
	}
	ua.currentEffort = effort
}

// drainLeftoverChunks empties, without blocking, what a prior turn left in the chunk channel: drainResponse returns
// on an idle debounce, so a late chunk can sit in the buffer. A nil channel is a no-op.
func drainLeftoverChunks(chunks <-chan utilityChunkPayload) {
	if chunks == nil {
		return
	}
	for {
		select {
		case <-chunks:
		default:
			return
		}
	}
}

// drainResponse reads the prompt response and collects assistant text from the forwarded response channel. kiro-cli
// sends no end_turn update: the turn ends with the session/prompt response, which Call already awaited. So this
// drains earlier chunks until an idle debounce, ctx or the 60s ceiling.
func (ua *utilityAgent) drainResponse(ctx context.Context, lease sessionLease, resp *marotte.RPCResponse) (string, error) {
	if resp == nil {
		return "", errors.New("nil response")
	}

	const (
		idleDebounce  = 50 * time.Millisecond
		totalDeadline = 60 * time.Second
	)

	var text strings.Builder
	idle := time.NewTimer(idleDebounce)
	defer idle.Stop()
	timeout := time.NewTimer(totalDeadline)
	defer timeout.Stop()

	for {
		select {
		case <-ctx.Done():
			// Only for request-context cancellation: during shutdown stopUtilityBridge cleans up, and a reset here races Stop().
			if !ua.session.shuttingDown() {
				ua.session.resetIf(lease.gen)
			}
			return text.String(), ctx.Err()
		case chunk, ok := <-lease.chunks:
			if !ok {
				return text.String(), nil
			}
			text.WriteString(chunk.Content.Text)
			// Safe without draining: since Go 1.23 a timer's channel is unbuffered.
			idle.Reset(idleDebounce)
		case <-idle.C:
			return text.String(), nil
		case <-timeout.C:
			// Reset so a wedged turn's leftover chunks do not reach the next caller.
			ua.session.resetIf(lease.gen)
			return text.String(), errors.New("utility prompt timeout")
		}
	}
}
