package composition

import (
	"context"
	"testing"
)

func TestLateLockSink_AppliesAChangePublishedBeforeTheBind(t *testing.T) {
	var sink lateLockSink
	sink.run(t.Context())

	applied := 0
	sink.bind(t.Context(), func(context.Context) { applied++ })
	if applied != 1 {
		t.Fatalf("bind applied the locks %d times, want 1: a change published before it reached no writer", applied)
	}
	sink.run(t.Context())
	if applied != 2 {
		t.Errorf("a change after the bind applied %d times in total, want 2", applied)
	}
}
