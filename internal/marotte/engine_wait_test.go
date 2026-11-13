package marotte

import (
	"context"
	"testing"
)

// A request's mark reaches what it derives, and work detached from the request lifts it, since it outlives the read.
func TestEngineWait_MarkAndLift(t *testing.T) {
	if EngineWaitRefused(t.Context()) {
		t.Error("an unmarked context refuses the engine wait")
	}
	marked, cancel := context.WithCancel(WithoutEngineWait(t.Context()))
	defer cancel()
	if !EngineWaitRefused(marked) {
		t.Error("a context derived from a marked one waits for the engine")
	}
	if EngineWaitRefused(WithEngineWait(context.WithoutCancel(marked))) {
		t.Error("WithEngineWait left the mark standing")
	}
}
