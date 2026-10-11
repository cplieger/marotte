package agent

import (
	"context"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// TestACPArgsReachChatBridges pins WithACPArgs → coordinator → StartOpts.ExtraArgs on a chat spawn.
func TestACPArgsReachChatBridges(t *testing.T) {
	cs := newTestChatStore()
	br := newFakeBridge()
	want := []string{"-v"}
	h := New(context.Background(), "/tmp/work", func() ACPBridge { return br }, cs, WithACPArgs(want))
	cs.wire(h)
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })

	if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	opts := br.lastStartOpts()
	if opts == nil {
		t.Fatal("the bridge was never started")
	}
	if !slices.Equal(opts.ExtraArgs, want) {
		t.Errorf("chat StartOpts.ExtraArgs = %v, want %v", opts.ExtraArgs, want)
	}
}

// TestACPArgsNeverReachTheUtilityBridge pins the per-spawn exclusion: the utility bridge
// shares the chat factory, and an operator `--effort max` there spends credits on titles.
func TestACPArgsNeverReachTheUtilityBridge(t *testing.T) {
	cs := newTestChatStore()
	br := newFakeBridge()
	h := New(context.Background(), "/tmp/work", func() ACPBridge { return br }, cs, WithACPArgs([]string{"--effort", "max"}))
	cs.wire(h)

	u := h.utility.get()
	if _, err := u.session.acquire(t.Context()); err != nil {
		t.Fatalf("acquire utility session: %v", err)
	}
	defer u.session.Stop()

	opts := br.lastStartOpts()
	if opts == nil {
		t.Fatal("the utility bridge was never started")
	}
	if len(opts.ExtraArgs) != 0 {
		t.Errorf("utility StartOpts.ExtraArgs = %v, want empty: operator launch flags must not reach the utility bridge", opts.ExtraArgs)
	}
}

// TestACPArgsUnsetIsEmpty covers the default: no env var, nothing appended to any spawn.
func TestACPArgsUnsetIsEmpty(t *testing.T) {
	cs := newTestChatStore()
	br := newFakeBridge()
	h := New(context.Background(), "/tmp/work", func() ACPBridge { return br }, cs)
	cs.wire(h)
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })

	if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	if opts := br.lastStartOpts(); opts != nil && len(opts.ExtraArgs) != 0 {
		t.Errorf("ExtraArgs = %v with no WithACPArgs, want empty", opts.ExtraArgs)
	}
}
