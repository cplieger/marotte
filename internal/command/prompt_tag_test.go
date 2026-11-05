package command

import (
	"context"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// promptTag reads params' _meta.kiro.userMessageTag, "" when absent.
func promptTag(params any) string {
	m, _ := params.(map[string]any)
	meta, _ := m["_meta"].(map[string]any)
	kiro, _ := meta["kiro"].(map[string]any)
	tag, _ := kiro["userMessageTag"].(string)
	return tag
}

func TestBuildPromptParams_TagsThePromptWithItsTurnID(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{}`)
	p := &marotte.PromptCommand{Text: "hi", MessageID: "m-1"}

	params, _ := buildPromptParams(t.Context(), Workspace{ConfigDir: dir}, fixedSession("s"), p, "t-abc", 0)

	if got := promptTag(params); got != "t-abc" {
		t.Errorf("params[_meta].kiro.userMessageTag = %q, want the turn id %q", got, "t-abc")
	}
}

// tagRetryBridges hands the retry a bridge that keeps the session/prompt params it was sent.
type tagRetryBridges struct {
	*recoveryBridges
	bridge *tagRetryBridge
}

func (b *tagRetryBridges) OpenBridge(context.Context, marotte.ChatID, string) (Bridge, error) {
	return b.bridge, nil
}

func (b *tagRetryBridges) OpenBridgeAt(context.Context, marotte.ChatID, string, uint64) (Bridge, error) {
	return b.bridge, nil
}

type tagRetryBridge struct {
	recoveryBridge
	sent []any
}

func (b *tagRetryBridge) PromptAt(ctx context.Context, params any, commit func() error) (*marotte.RPCResponse, uint64, error) {
	return promptViaCallAt(ctx, b, params, commit)
}

func (b *tagRetryBridge) CallAt(_ context.Context, method string, params any) (*marotte.RPCResponse, uint64, error) {
	if method == marotte.MethodPrompt {
		b.sent = append(b.sent, params)
	}
	return &marotte.RPCResponse{}, 0, nil
}

// The retry re-sends the empty turn's params under a turn of its own, so its prompt must carry
// the retry's id: with the empty turn's, the retry's turn_start would never bind it.
func TestRetryEmptyTurnPrompt_TagsTheRetryWithItsOwnTurnID(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{}`)
	p := &marotte.PromptCommand{Text: "q", MessageID: "m1"}
	params, _ := buildPromptParams(t.Context(), Workspace{ConfigDir: dir}, fixedSession("s0"), p, "t-empty", 0)
	outcome := &recoveryOutcome{}
	bridges := &tagRetryBridges{recoveryBridges: &recoveryBridges{benchDeps: newBenchDeps()}, bridge: &tagRetryBridge{}}
	roles := promptRolesOf(bridges)
	roles.admission, roles.turnOutcome = outcome, outcome
	roles.workspace = Workspace{ConfigDir: dir}

	retryEmptyTurnPrompt(t.Context(), roles, testReservation("c1", true), "t-empty", p, params)

	if len(bridges.bridge.sent) != 1 {
		t.Fatalf("the retry sent %d prompts, want 1", len(bridges.bridge.sent))
	}
	if got := promptTag(bridges.bridge.sent[0]); got != "t-1" {
		t.Errorf("the retry's userMessageTag = %q, want its own turn id %q", got, "t-1")
	}
}
