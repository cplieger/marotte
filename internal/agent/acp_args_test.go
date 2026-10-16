package agent

import (
	"context"
	"slices"
	"testing"
)

// engineOptsAfterBoot is the options the first engine generation started with.
func engineOptsAfterBoot(t *testing.T, opts ...Option) []string {
	t.Helper()
	cs := newTestChatStore()
	src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
	h := newRuntimeOn(context.Background(), "/tmp/work", src, cs, opts...)
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	if !awaitEngine(h) {
		t.Fatal("the engine never became ready")
	}
	e := src.current()
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.opts.ExtraArgs
}

// TestACPArgsReachTheEngine pins WithACPArgs → the engine's EngineOpts.ExtraArgs: the flags reach every session.
func TestACPArgsReachTheEngine(t *testing.T) {
	want := []string{"-v"}
	if got := engineOptsAfterBoot(t, WithACPArgs(want)); !slices.Equal(got, want) {
		t.Errorf("EngineOpts.ExtraArgs = %v, want %v", got, want)
	}
}

// TestACPArgsUnsetIsEmpty covers the default: no env var, nothing appended to the engine's argv.
func TestACPArgsUnsetIsEmpty(t *testing.T) {
	if got := engineOptsAfterBoot(t); len(got) != 0 {
		t.Errorf("ExtraArgs = %v with no WithACPArgs, want empty", got)
	}
}
