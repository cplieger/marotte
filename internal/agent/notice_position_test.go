package agent

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// A run's bound waits on the run cell's query slot; a notice never waits past the run read's own deadline for it.
func TestReadBound_ARunReadPastItsDeadlineReadsZero(t *testing.T) {
	blocked := func(ctx context.Context, _ string) (uint64, error) {
		<-ctx.Done()
		return 7, ctx.Err()
	}
	read := newReadBound(func(context.Context, marotte.ChatID) (uint64, error) { return 0, nil }, blocked, 20*time.Millisecond)
	got := make(chan uint64, 1)
	go func() { got <- read(context.WithoutCancel(t.Context()), marotte.TabKindRun, "wf_1") }()
	select {
	case bound := <-got:
		if bound != 0 {
			t.Errorf("bound = %d, want 0 for a read past its deadline", bound)
		}
	case <-time.After(time.Second):
		t.Fatal("the run bound read outlived its deadline")
	}
}

// A run notice whose bound read timed out is still raised, at position 0.
func TestNotify_RaisesAtPositionZeroWhenTheRunBoundTimesOut(t *testing.T) {
	h, fp := newRunPushHub(t)
	h.coord.readBound = newReadBound(
		func(context.Context, marotte.ChatID) (uint64, error) { return 0, nil },
		func(ctx context.Context, _ string) (uint64, error) { <-ctx.Done(); return 7, ctx.Err() },
		20*time.Millisecond,
	)
	n := marotte.NotificationPayload{PushSubject: marotte.RunSubject("wf_1"), Kind: marotte.PushKindRunOutcome, Title: "t", Body: "b"}

	h.coord.Notify(t.Context(), "", &n)

	awaitRunPush(t, fp)
	if got := fp.positions(); !slices.Equal(got, []uint64{0}) {
		t.Errorf("Raise positions = %v, want [0]", got)
	}
}
